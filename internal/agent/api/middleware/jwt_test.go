package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const testSecret = "test-secret-key-for-jwt-signing-1234"
const testAPIKey = "test-api-key-12345"

func makeJWT(t *testing.T, username string, secret string, expiry time.Duration) string {
	t.Helper()
	claims := &JWTClaims{
		Username: username,
		RegisteredClaims: jwt.RegisteredClaims{
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(expiry)),
			Issuer:    "sistemo",
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenStr, err := token.SignedString([]byte(secret))
	if err != nil {
		t.Fatalf("sign JWT: %v", err)
	}
	return tokenStr
}

func runRequest(t *testing.T, mw func(http.Handler) http.Handler, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	}))
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	return rr
}

func TestDashboardAuth_PublicPaths(t *testing.T) {
	mw := DashboardAuth(testAPIKey, []byte(testSecret), func() bool { return true })

	for _, path := range []string{"/health", "/ready", "/dashboard/", "/dashboard/assets/foo.js", "/api/v1/auth/status", "/api/v1/auth/login"} {
		req := httptest.NewRequest("GET", path, nil)
		rr := runRequest(t, mw, req)
		if rr.Code != 200 {
			t.Errorf("path %q: got %d, want 200", path, rr.Code)
		}
	}
}

func TestDashboardAuth_APIKeyHeader(t *testing.T) {
	mw := DashboardAuth(testAPIKey, []byte(testSecret), func() bool { return true })

	req := httptest.NewRequest("GET", "/api/v1/machines", nil)
	req.Header.Set("X-API-Key", testAPIKey)
	rr := runRequest(t, mw, req)
	if rr.Code != 200 {
		t.Errorf("API key auth: got %d, want 200", rr.Code)
	}
}

func TestDashboardAuth_APIKeyBearer(t *testing.T) {
	mw := DashboardAuth(testAPIKey, []byte(testSecret), func() bool { return true })

	req := httptest.NewRequest("GET", "/api/v1/machines", nil)
	req.Header.Set("Authorization", "Bearer "+testAPIKey)
	rr := runRequest(t, mw, req)
	if rr.Code != 200 {
		t.Errorf("API key via Bearer: got %d, want 200", rr.Code)
	}
}

func TestDashboardAuth_ValidJWT(t *testing.T) {
	mw := DashboardAuth("", []byte(testSecret), func() bool { return true })

	token := makeJWT(t, "admin", testSecret, time.Hour)
	req := httptest.NewRequest("GET", "/api/v1/machines", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rr := runRequest(t, mw, req)
	if rr.Code != 200 {
		t.Errorf("valid JWT: got %d, want 200", rr.Code)
	}
}

func TestDashboardAuth_ExpiredJWT(t *testing.T) {
	mw := DashboardAuth("", []byte(testSecret), func() bool { return true })

	token := makeJWT(t, "admin", testSecret, -time.Hour) // expired
	req := httptest.NewRequest("GET", "/api/v1/machines", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rr := runRequest(t, mw, req)
	if rr.Code != 401 {
		t.Errorf("expired JWT: got %d, want 401", rr.Code)
	}
}

func TestDashboardAuth_InvalidSignature(t *testing.T) {
	mw := DashboardAuth("", []byte(testSecret), func() bool { return true })

	token := makeJWT(t, "admin", "wrong-secret", time.Hour)
	req := httptest.NewRequest("GET", "/api/v1/machines", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rr := runRequest(t, mw, req)
	if rr.Code != 401 {
		t.Errorf("bad signature JWT: got %d, want 401", rr.Code)
	}
}

func TestDashboardAuth_NoAuthNoKeyNoAdmin(t *testing.T) {
	// No API key, no admin → open mode (localhost dev)
	mw := DashboardAuth("", []byte(testSecret), func() bool { return false })

	req := httptest.NewRequest("GET", "/api/v1/machines", nil)
	rr := runRequest(t, mw, req)
	if rr.Code != 200 {
		t.Errorf("open mode: got %d, want 200", rr.Code)
	}
}

func TestDashboardAuth_NoAuthButAdminExists(t *testing.T) {
	// No API key, but admin exists → require JWT
	mw := DashboardAuth("", []byte(testSecret), func() bool { return true })

	req := httptest.NewRequest("GET", "/api/v1/machines", nil)
	rr := runRequest(t, mw, req)
	if rr.Code != 401 {
		t.Errorf("admin exists, no auth: got %d, want 401", rr.Code)
	}
}

func TestDashboardAuth_WrongAPIKey(t *testing.T) {
	mw := DashboardAuth(testAPIKey, []byte(testSecret), func() bool { return true })

	req := httptest.NewRequest("GET", "/api/v1/machines", nil)
	req.Header.Set("X-API-Key", "wrong-key")
	rr := runRequest(t, mw, req)
	if rr.Code != 401 {
		t.Errorf("wrong API key: got %d, want 401", rr.Code)
	}
}
