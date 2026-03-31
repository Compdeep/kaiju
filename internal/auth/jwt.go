package auth

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/user/kaiju/internal/db"
)

/*
 * Claims holds the JWT payload.
 * desc: Contains the user identity, permission level, scope list, and standard registered claims.
 */
type Claims struct {
	Username  string   `json:"sub"`
	MaxIntent int      `json:"max_intent"`
	Scopes    []string `json:"scopes"`
	jwt.RegisteredClaims
}

/*
 * JWTService issues and validates JWT tokens.
 * desc: Manages HMAC-SHA256 signed JWTs with a configurable secret and expiry duration.
 */
type JWTService struct {
	secret []byte
	expiry time.Duration
}

/*
 * NewJWTService creates a JWT service.
 * desc: Initializes the service with the given secret or auto-generates one persisted at dataDir/jwt_secret.
 * param: secret - HMAC secret string; if empty, a secret is loaded or generated from disk
 * param: dataDir - directory where the auto-generated jwt_secret file is stored
 * param: expiryHours - token lifetime in hours; defaults to 24 if zero or negative
 * return: configured JWTService and any error from secret generation
 */
func NewJWTService(secret string, dataDir string, expiryHours int) (*JWTService, error) {
	if expiryHours <= 0 {
		expiryHours = 24
	}

	var secretBytes []byte
	if secret != "" {
		secretBytes = []byte(secret)
	} else {
		var err error
		secretBytes, err = loadOrGenerateSecret(filepath.Join(dataDir, "jwt_secret"))
		if err != nil {
			return nil, err
		}
	}

	return &JWTService{
		secret: secretBytes,
		expiry: time.Duration(expiryHours) * time.Hour,
	}, nil
}

/*
 * Issue creates a signed JWT for the given user.
 * desc: Builds claims from the user record and signs them with HMAC-SHA256.
 * param: user - the database user to create a token for
 * return: the signed token string, its expiration time, and any signing error
 */
func (j *JWTService) Issue(user *db.User) (string, time.Time, error) {
	expires := time.Now().Add(j.expiry)
	claims := Claims{
		Username:  user.Username,
		MaxIntent: user.MaxIntent,
		Scopes:    user.Scopes,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(expires),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			Issuer:    "kaiju",
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString(j.secret)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("auth: sign token: %w", err)
	}
	return signed, expires, nil
}

/*
 * Validate parses and verifies a JWT, returning the claims.
 * desc: Parses the token string, checks the HMAC signature, and extracts the Claims payload.
 * param: tokenStr - the raw JWT string to validate
 * return: the parsed Claims and any validation error
 */
func (j *JWTService) Validate(tokenStr string) (*Claims, error) {
	token, err := jwt.ParseWithClaims(tokenStr, &Claims{}, func(token *jwt.Token) (any, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("auth: unexpected signing method: %v", token.Header["alg"])
		}
		return j.secret, nil
	})
	if err != nil {
		return nil, fmt.Errorf("auth: invalid token: %w", err)
	}

	claims, ok := token.Claims.(*Claims)
	if !ok || !token.Valid {
		return nil, fmt.Errorf("auth: invalid token claims")
	}
	return claims, nil
}

/*
 * loadOrGenerateSecret reads a secret from disk or generates a new 32-byte random secret.
 * desc: Attempts to read an existing secret file; if missing or too short, generates and persists a new one.
 * param: path - filesystem path for the secret file
 * return: the secret bytes and any error
 */
func loadOrGenerateSecret(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err == nil && len(data) >= 32 {
		return data, nil
	}

	// Generate 32 bytes of randomness
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, fmt.Errorf("auth: generate secret: %w", err)
	}

	encoded := []byte(hex.EncodeToString(secret))
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, fmt.Errorf("auth: create dir: %w", err)
	}
	if err := os.WriteFile(path, encoded, 0600); err != nil {
		return nil, fmt.Errorf("auth: write secret: %w", err)
	}
	return encoded, nil
}
