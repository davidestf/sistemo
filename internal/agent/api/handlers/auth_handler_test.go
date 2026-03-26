package handlers

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"

	"github.com/davidestf/sistemo/internal/db"
)

const testJWTSecret = "test-jwt-secret-32-bytes-long-!!!"

func setupAuthTestRouter(t *testing.T) (*chi.Mux, *sql.DB) {
	t.Helper()
	database, err := db.New(t.TempDir())
	if err != nil {
		t.Fatalf("create test db: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	logger := zap.NewNop()
	auth := NewAuth(database, logger, []byte(testJWTSecret), 24*time.Hour, func() {})

	r := chi.NewRouter()
	r.Get("/api/v1/auth/status", auth.Status)
	r.Post("/api/v1/auth/setup", auth.Setup)
	r.Post("/api/v1/auth/login", auth.Login)

	return r, database
}

func doRequest(t *testing.T, router *chi.Mux, method, path string, body interface{}, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var reqBody []byte
	if body != nil {
		var err error
		reqBody, err = json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(reqBody))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func decodeJSON(t *testing.T, rec *httptest.ResponseRecorder) map[string]interface{} {
	t.Helper()
	var result map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&result); err != nil {
		t.Fatalf("decode JSON response: %v (body: %s)", err, rec.Body.String())
	}
	return result
}

// --- Tests ---

func TestAuthStatus_NoAdmin(t *testing.T) {
	router, _ := setupAuthTestRouter(t)

	rec := doRequest(t, router, "GET", "/api/v1/auth/status", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status code = %d, want %d", rec.Code, http.StatusOK)
	}

	body := decodeJSON(t, rec)
	if body["setup_required"] != true {
		t.Errorf("setup_required = %v, want true", body["setup_required"])
	}
	if body["authenticated"] != false {
		t.Errorf("authenticated = %v, want false", body["authenticated"])
	}
}

func TestAuthStatus_WithAdmin(t *testing.T) {
	router, database := setupAuthTestRouter(t)

	// Create admin directly in DB
	if err := db.CreateAdmin(database, "admin", "password123"); err != nil {
		t.Fatalf("create admin: %v", err)
	}

	rec := doRequest(t, router, "GET", "/api/v1/auth/status", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status code = %d, want %d", rec.Code, http.StatusOK)
	}

	body := decodeJSON(t, rec)
	if body["setup_required"] != false {
		t.Errorf("setup_required = %v, want false", body["setup_required"])
	}
	if body["authenticated"] != false {
		t.Errorf("authenticated = %v, want false (no JWT provided)", body["authenticated"])
	}
}

func TestAuthSetup_Success(t *testing.T) {
	router, _ := setupAuthTestRouter(t)

	rec := doRequest(t, router, "POST", "/api/v1/auth/setup",
		map[string]string{"username": "admin", "password": "strongpassword123"}, nil)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status code = %d, want %d; body: %s", rec.Code, http.StatusCreated, rec.Body.String())
	}

	body := decodeJSON(t, rec)
	if body["token"] == nil || body["token"] == "" {
		t.Error("expected token in response")
	}
	if body["expires_at"] == nil || body["expires_at"] == "" {
		t.Error("expected expires_at in response")
	}
	if body["message"] == nil {
		t.Error("expected message in response")
	}
}

func TestAuthSetup_DuplicateRejected(t *testing.T) {
	router, _ := setupAuthTestRouter(t)

	// First setup succeeds
	rec := doRequest(t, router, "POST", "/api/v1/auth/setup",
		map[string]string{"username": "admin", "password": "strongpassword123"}, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("first setup: status = %d, want %d", rec.Code, http.StatusCreated)
	}

	// Second setup rejected
	rec = doRequest(t, router, "POST", "/api/v1/auth/setup",
		map[string]string{"username": "admin2", "password": "anotherpassword"}, nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("second setup: status = %d, want %d; body: %s", rec.Code, http.StatusConflict, rec.Body.String())
	}

	body := decodeJSON(t, rec)
	if body["error"] == nil {
		t.Error("expected error message in response")
	}
}

func TestAuthSetup_WeakPassword(t *testing.T) {
	router, _ := setupAuthTestRouter(t)

	rec := doRequest(t, router, "POST", "/api/v1/auth/setup",
		map[string]string{"username": "admin", "password": "short"}, nil)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status code = %d, want %d; body: %s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}

	body := decodeJSON(t, rec)
	errMsg, _ := body["error"].(string)
	if errMsg == "" {
		t.Error("expected error message about password length")
	}
}

func TestAuthSetup_InvalidUsername(t *testing.T) {
	tests := []struct {
		name     string
		username string
	}{
		{"special chars", "admin@host"},
		{"spaces", "admin user"},
		{"too short", "ab"},
		{"empty", ""},
		{"shell injection", "admin;rm -rf /"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			router, _ := setupAuthTestRouter(t)

			rec := doRequest(t, router, "POST", "/api/v1/auth/setup",
				map[string]string{"username": tt.username, "password": "strongpassword123"}, nil)

			if rec.Code != http.StatusBadRequest {
				t.Errorf("username %q: status = %d, want %d; body: %s",
					tt.username, rec.Code, http.StatusBadRequest, rec.Body.String())
			}
		})
	}
}

func TestAuthLogin_Success(t *testing.T) {
	router, _ := setupAuthTestRouter(t)

	// Create admin via setup
	rec := doRequest(t, router, "POST", "/api/v1/auth/setup",
		map[string]string{"username": "admin", "password": "strongpassword123"}, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("setup failed: %d", rec.Code)
	}

	// Login
	rec = doRequest(t, router, "POST", "/api/v1/auth/login",
		map[string]string{"username": "admin", "password": "strongpassword123"}, nil)

	if rec.Code != http.StatusOK {
		t.Fatalf("login: status = %d, want %d; body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	body := decodeJSON(t, rec)
	if body["token"] == nil || body["token"] == "" {
		t.Error("expected token in login response")
	}
	if body["expires_at"] == nil || body["expires_at"] == "" {
		t.Error("expected expires_at in login response")
	}
}

func TestAuthLogin_WrongPassword(t *testing.T) {
	router, _ := setupAuthTestRouter(t)

	// Create admin via setup
	rec := doRequest(t, router, "POST", "/api/v1/auth/setup",
		map[string]string{"username": "admin", "password": "strongpassword123"}, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("setup failed: %d", rec.Code)
	}

	// Login with wrong password
	rec = doRequest(t, router, "POST", "/api/v1/auth/login",
		map[string]string{"username": "admin", "password": "wrongpassword"}, nil)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong password: status = %d, want %d; body: %s",
			rec.Code, http.StatusUnauthorized, rec.Body.String())
	}
}

func TestAuthLogin_UnknownUser(t *testing.T) {
	router, _ := setupAuthTestRouter(t)

	// Create admin via setup
	rec := doRequest(t, router, "POST", "/api/v1/auth/setup",
		map[string]string{"username": "admin", "password": "strongpassword123"}, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("setup failed: %d", rec.Code)
	}

	// Login with unknown user
	rec = doRequest(t, router, "POST", "/api/v1/auth/login",
		map[string]string{"username": "nobody", "password": "strongpassword123"}, nil)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unknown user: status = %d, want %d; body: %s",
			rec.Code, http.StatusUnauthorized, rec.Body.String())
	}
}

func TestAuthStatus_WithValidJWT(t *testing.T) {
	router, _ := setupAuthTestRouter(t)

	// Create admin via setup and get token
	rec := doRequest(t, router, "POST", "/api/v1/auth/setup",
		map[string]string{"username": "admin", "password": "strongpassword123"}, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("setup failed: %d", rec.Code)
	}
	body := decodeJSON(t, rec)
	token := body["token"].(string)

	// Check status with valid JWT
	rec = doRequest(t, router, "GET", "/api/v1/auth/status", nil,
		map[string]string{"Authorization": "Bearer " + token})

	if rec.Code != http.StatusOK {
		t.Fatalf("status code = %d, want %d", rec.Code, http.StatusOK)
	}

	body = decodeJSON(t, rec)
	if body["authenticated"] != true {
		t.Errorf("authenticated = %v, want true", body["authenticated"])
	}
	if body["username"] != "admin" {
		t.Errorf("username = %v, want admin", body["username"])
	}
}

func TestAuthLogin_EmptyCredentials(t *testing.T) {
	router, _ := setupAuthTestRouter(t)

	rec := doRequest(t, router, "POST", "/api/v1/auth/login",
		map[string]string{"username": "", "password": ""}, nil)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("empty credentials: status = %d, want %d; body: %s",
			rec.Code, http.StatusBadRequest, rec.Body.String())
	}
}

func TestAuthSetup_InvalidJSON(t *testing.T) {
	router, _ := setupAuthTestRouter(t)

	req := httptest.NewRequest("POST", "/api/v1/auth/setup", bytes.NewReader([]byte("not json")))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid JSON: status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestAuthLogin_InvalidJSON(t *testing.T) {
	router, _ := setupAuthTestRouter(t)

	req := httptest.NewRequest("POST", "/api/v1/auth/login", bytes.NewReader([]byte("{bad")))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid JSON: status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestAuthSetup_ValidUsernames(t *testing.T) {
	valid := []string{
		"admin",
		"my-user",
		"user_name",
		"User123",
		"abc",
	}
	for _, username := range valid {
		t.Run(username, func(t *testing.T) {
			router, _ := setupAuthTestRouter(t)

			rec := doRequest(t, router, "POST", "/api/v1/auth/setup",
				map[string]string{"username": username, "password": "strongpassword123"}, nil)

			if rec.Code != http.StatusCreated {
				t.Errorf("username %q: status = %d, want %d; body: %s",
					username, rec.Code, http.StatusCreated, rec.Body.String())
			}
		})
	}
}

func TestAuthSetup_PasswordExactly8Chars(t *testing.T) {
	router, _ := setupAuthTestRouter(t)

	rec := doRequest(t, router, "POST", "/api/v1/auth/setup",
		map[string]string{"username": "admin", "password": "12345678"}, nil)

	if rec.Code != http.StatusCreated {
		t.Fatalf("8-char password: status = %d, want %d; body: %s",
			rec.Code, http.StatusCreated, rec.Body.String())
	}
}

func TestAuthSetup_Password7CharsRejected(t *testing.T) {
	router, _ := setupAuthTestRouter(t)

	rec := doRequest(t, router, "POST", "/api/v1/auth/setup",
		map[string]string{"username": "admin", "password": "1234567"}, nil)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("7-char password: status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}
