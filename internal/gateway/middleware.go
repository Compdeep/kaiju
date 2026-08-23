package gateway

import (
	"context"
	"net/http"
	"strings"

	"github.com/Compdeep/kaiju/internal/auth"
)

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
