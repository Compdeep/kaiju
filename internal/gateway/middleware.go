package gateway

import (
	"context"
	"crypto/subtle"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/user/kaiju/internal/auth"
)

/*
 * WithLogging wraps an HTTP handler to log each request.
 * desc: Logs the HTTP method, path, and elapsed time for every request.
 * param: next - the handler to wrap
 * return: a new handler that logs requests before delegating to next
 */
func WithLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("[gateway] %s %s %s", r.Method, r.URL.Path, time.Since(start).Round(time.Millisecond))
	})
}

/*
 * WithCORS adds permissive CORS headers (for development).
 * desc: Sets Access-Control-Allow-Origin to * and handles OPTIONS preflight requests.
 * param: next - the handler to wrap
 * return: a new handler that adds CORS headers before delegating to next
 */
func WithCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

/*
 * WithAuth returns middleware that checks for a Bearer token.
 * desc: If token is non-empty, requires a matching Authorization header; if empty, passes through all requests.
 * param: token - the expected bearer token; empty means no auth required
 * return: a middleware function that wraps an http.Handler with bearer token validation
 */
func WithAuth(token string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if token == "" {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			authHeader := r.Header.Get("Authorization")
			expected := "Bearer " + token
			if subtle.ConstantTimeCompare([]byte(authHeader), []byte(expected)) != 1 {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// ─── JWT Auth ───────────────────────────────────────────────────────────────

type claimsKey struct{}

/*
 * WithJWTAuth validates JWT tokens and injects claims into the request context.
 * desc: Extracts the Bearer token from the Authorization header, validates it, and stores claims in context.
 * param: jwtSvc - the JWT service used for token validation
 * return: a middleware function that wraps an http.Handler with JWT authentication
 */
func WithJWTAuth(jwtSvc *auth.JWTService) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			authHeader := r.Header.Get("Authorization")
			if !strings.HasPrefix(authHeader, "Bearer ") {
				http.Error(w, `{"error":"missing or invalid authorization header"}`, http.StatusUnauthorized)
				return
			}
			tokenStr := strings.TrimPrefix(authHeader, "Bearer ")

			claims, err := jwtSvc.Validate(tokenStr)
			if err != nil {
				http.Error(w, `{"error":"invalid or expired token"}`, http.StatusUnauthorized)
				return
			}

			ctx := context.WithValue(r.Context(), claimsKey{}, claims)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

/*
 * WithJWTAuthOrQuery validates JWT from Authorization header OR ?token= query param.
 * desc: For endpoints accessed by browser elements (img, video, iframe) that can't send headers.
 */
func WithJWTAuthOrQuery(jwtSvc *auth.JWTService) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var tokenStr string
			authHeader := r.Header.Get("Authorization")
			if strings.HasPrefix(authHeader, "Bearer ") {
				tokenStr = strings.TrimPrefix(authHeader, "Bearer ")
			} else {
				tokenStr = r.URL.Query().Get("token")
			}
			if tokenStr == "" {
				http.Error(w, `{"error":"missing authorization"}`, http.StatusUnauthorized)
				return
			}
			claims, err := jwtSvc.Validate(tokenStr)
			if err != nil {
				http.Error(w, `{"error":"invalid or expired token"}`, http.StatusUnauthorized)
				return
			}
			ctx := context.WithValue(r.Context(), claimsKey{}, claims)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

/*
 * ClaimsFromContext extracts JWT claims from a request context.
 * desc: Retrieves the auth.Claims value stored by WithJWTAuth middleware.
 * param: ctx - the request context that may contain JWT claims
 * return: the Claims pointer and a boolean indicating whether claims were found
 */
func ClaimsFromContext(ctx context.Context) (*auth.Claims, bool) {
	claims, ok := ctx.Value(claimsKey{}).(*auth.Claims)
	return claims, ok
}
