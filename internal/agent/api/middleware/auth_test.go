package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})
}

func TestAPIKeyAuth_ValidKey(t *testing.T) {
	handler := APIKeyAuth("test-secret-key")(okHandler())
	req := httptest.NewRequest(http.MethodGet, "/vms", nil)
	req.Header.Set("X-API-Key", "test-secret-key")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("valid key: got %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestAPIKeyAuth_InvalidKey(t *testing.T) {
	handler := APIKeyAuth("test-secret-key")(okHandler())
	req := httptest.NewRequest(http.MethodGet, "/vms", nil)
	req.Header.Set("X-API-Key", "wrong-key")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("invalid key: got %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

func TestAPIKeyAuth_EmptyKey(t *testing.T) {
	handler := APIKeyAuth("test-secret-key")(okHandler())
	req := httptest.NewRequest(http.MethodGet, "/vms", nil)
	// No API key header
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("empty key: got %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

func TestAPIKeyAuth_BearerToken(t *testing.T) {
	handler := APIKeyAuth("test-secret-key")(okHandler())
	req := httptest.NewRequest(http.MethodGet, "/vms", nil)
	req.Header.Set("Authorization", "Bearer test-secret-key")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("bearer token: got %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestAPIKeyAuth_HealthSkipsAuth(t *testing.T) {
	handler := APIKeyAuth("test-secret-key")(okHandler())
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	// No API key
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("/health should skip auth: got %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestAPIKeyAuth_ReadySkipsAuth(t *testing.T) {
	handler := APIKeyAuth("test-secret-key")(okHandler())
	req := httptest.NewRequest(http.MethodGet, "/ready", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("/ready should skip auth: got %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestAPIKeyAuth_ContentTypeOnReject(t *testing.T) {
	handler := APIKeyAuth("test-secret-key")(okHandler())
	req := httptest.NewRequest(http.MethodGet, "/vms", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	ct := rec.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Errorf("401 Content-Type: got %q, want %q", ct, "application/json")
	}
}
