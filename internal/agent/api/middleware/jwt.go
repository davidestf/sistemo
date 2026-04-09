package middleware

import (
	"context"
	"crypto/subtle"
	"fmt"
	"net"
	"net/http"
	"strings"

	"github.com/golang-jwt/jwt/v5"
)

// ContextKeyUsername is the context key for the authenticated username.
const ContextKeyUsername contextKey = "auth_username"

// JWTClaims are the custom claims embedded in dashboard JWT tokens.
type JWTClaims struct {
	Username string `json:"username"`
	jwt.RegisteredClaims
}

// DashboardAuth creates middleware that accepts EITHER:
//  1. X-API-Key header (existing CLI/script auth, bypasses JWT entirely)
//  2. Authorization: Bearer <jwt> (dashboard sessions)
//
// Public paths are always allowed without auth:
//   - /health, /ready (monitoring)
//   - /dashboard/* (static SPA files)
//   - /api/v1/auth/* (login, setup, status)
//
// When no API key is configured AND no admin account exists, all requests
// are allowed (localhost dev mode, matching pre-auth behavior).
func DashboardAuth(apiKey string, jwtSecret []byte, adminExistsFn func() bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			path := r.URL.Path

			// 1. Public paths — always allowed
			if isPublicPath(path) {
				next.ServeHTTP(w, r)
				return
			}

			// 2. Localhost bypass — CLI and local tools skip auth
			if isLocalhost(r) {
				next.ServeHTTP(w, r)
				return
			}

			// 3. API key auth (existing behavior for CLI/scripts)
			if apiKey != "" {
				if checkAPIKey(r, apiKey) {
					next.ServeHTTP(w, r)
					return
				}
			}

			// 4. JWT auth (dashboard sessions — header or query param for WebSocket)
			if len(jwtSecret) > 0 {
				if claims, ok := ParseJWTFromRequest(r, jwtSecret); ok {
					ctx := context.WithValue(r.Context(), ContextKeyUsername, claims.Username)
					next.ServeHTTP(w, r.WithContext(ctx))
					return
				}
			}

			// 5. Open mode: no API key AND no admin → allow everything
			if apiKey == "" && !adminExistsFn() {
				next.ServeHTTP(w, r)
				return
			}

			// 6. Reject — either API key is set (and wasn't provided) or
			//    admin exists (and no valid JWT was provided)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
		})
	}
}

// isPublicPath returns true for paths that never require authentication.
func isPublicPath(path string) bool {
	switch path {
	case "/health", "/ready":
		return true
	}
	if strings.HasPrefix(path, "/dashboard") {
		return true
	}
	if strings.HasPrefix(path, "/api/v1/auth/") {
		return true
	}
	return false
}

// checkAPIKey checks the X-API-Key header or Bearer token against the configured key.
func checkAPIKey(r *http.Request, apiKey string) bool {
	// Check X-API-Key header
	key := r.Header.Get("X-API-Key")
	if key != "" && subtle.ConstantTimeCompare([]byte(key), []byte(apiKey)) == 1 {
		return true
	}

	// Check Authorization: Bearer <api-key> (backward compat)
	auth := r.Header.Get("Authorization")
	if strings.HasPrefix(auth, "Bearer ") {
		token := strings.TrimPrefix(auth, "Bearer ")
		if subtle.ConstantTimeCompare([]byte(token), []byte(apiKey)) == 1 {
			return true
		}
	}
	return false
}

// ParseJWTFromRequest extracts and validates a JWT from the Authorization: Bearer header
// or the ?token= query parameter (used by WebSocket connections which can't set headers).
func ParseJWTFromRequest(r *http.Request, secret []byte) (*JWTClaims, bool) {
	// Try Authorization header first
	tokenStr := ""
	auth := r.Header.Get("Authorization")
	if strings.HasPrefix(auth, "Bearer ") {
		tokenStr = strings.TrimPrefix(auth, "Bearer ")
	}

	// Fall back to query parameter (WebSocket)
	if tokenStr == "" {
		tokenStr = r.URL.Query().Get("token")
	}

	if tokenStr == "" {
		return nil, false
	}

	claims := &JWTClaims{}
	token, err := jwt.ParseWithClaims(tokenStr, claims, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return secret, nil
	})
	if err != nil || !token.Valid {
		return nil, false
	}
	return claims, true
}

// ParseJWTToken validates a raw JWT token string and returns the claims.
// Used by WebSocket handlers that receive the token in a message body.
func ParseJWTToken(tokenStr string, secret []byte) (*JWTClaims, bool) {
	if tokenStr == "" {
		return nil, false
	}
	claims := &JWTClaims{}
	token, err := jwt.ParseWithClaims(tokenStr, claims, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return secret, nil
	})
	if err != nil || !token.Valid {
		return nil, false
	}
	return claims, true
}

// contextKeyOriginalAddr stores the original RemoteAddr before RealIP modifies it.
const contextKeyOriginalAddr contextKey = "original_remote_addr"

// SaveOriginalRemoteAddr stores r.RemoteAddr in context BEFORE RealIP can rewrite it.
// This prevents X-Forwarded-For spoofing from bypassing the localhost auth check.
func SaveOriginalRemoteAddr(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := context.WithValue(r.Context(), contextKeyOriginalAddr, r.RemoteAddr)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// isLocalhost returns true if the ORIGINAL request (before RealIP) comes from 127.0.0.1 or ::1.
// Uses the pre-RealIP address to prevent X-Forwarded-For spoofing.
func isLocalhost(r *http.Request) bool {
	// Use original address saved before RealIP middleware
	addr := r.RemoteAddr
	if orig, ok := r.Context().Value(contextKeyOriginalAddr).(string); ok {
		addr = orig
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	return host == "127.0.0.1" || host == "::1"
}
