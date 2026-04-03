package handlers

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"regexp"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"go.uber.org/zap"

	agentmw "github.com/davidestf/sistemo/internal/agent/api/middleware"
	"github.com/davidestf/sistemo/internal/db"
)

var safeUsername = regexp.MustCompile(`^[a-zA-Z0-9_-]{3,64}$`)

// loginTracker tracks failed login attempts per IP for brute-force protection.
type loginTracker struct {
	failures    int
	lastFailure time.Time
	lockedUntil time.Time
}

var (
	loginAttemptsMu sync.Mutex
	loginAttempts   = map[string]*loginTracker{}
)

// stripPort extracts the IP from an addr:port string.
// RemoteAddr includes the port (e.g. "192.168.1.1:54321"), which would
// make every connection a separate tracker — defeating rate limiting entirely.
func stripPort(remoteAddr string) string {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return remoteAddr
	}
	return host
}

// checkLoginRateLimit returns an error if the IP is locked out.
// Must be called BEFORE credential validation.
func checkLoginRateLimit(ip string) error {
	loginAttemptsMu.Lock()
	defer loginAttemptsMu.Unlock()

	tracker, exists := loginAttempts[ip]
	if !exists {
		return nil
	}

	if tracker.lockedUntil.IsZero() {
		return nil
	}

	if time.Now().Before(tracker.lockedUntil) {
		remaining := time.Until(tracker.lockedUntil).Round(time.Second)
		return fmt.Errorf("too many failed attempts. Try again in %s", remaining)
	}

	// Lockout expired — reset tracker entirely
	delete(loginAttempts, ip)
	return nil
}

// recordLoginFailure increments the failure counter and applies lockout.
func recordLoginFailure(ip string) {
	loginAttemptsMu.Lock()
	defer loginAttemptsMu.Unlock()

	// Evict stale entries to prevent unbounded map growth (DoS vector)
	if len(loginAttempts) > 10000 {
		cutoff := time.Now().Add(-1 * time.Hour)
		for k, v := range loginAttempts {
			if v.lastFailure.Before(cutoff) {
				delete(loginAttempts, k)
			}
		}
	}

	tracker, exists := loginAttempts[ip]
	if !exists {
		tracker = &loginTracker{}
		loginAttempts[ip] = tracker
	}

	tracker.failures++
	tracker.lastFailure = time.Now()

	// Progressive lockout: 5 fails → 30s, 10 → 5min, 20+ → 30min
	switch {
	case tracker.failures >= 20:
		tracker.lockedUntil = time.Now().Add(30 * time.Minute)
	case tracker.failures >= 10:
		tracker.lockedUntil = time.Now().Add(5 * time.Minute)
	case tracker.failures >= 5:
		tracker.lockedUntil = time.Now().Add(30 * time.Second)
	}
}

// recordLoginSuccess resets the failure counter for an IP.
func recordLoginSuccess(ip string) {
	loginAttemptsMu.Lock()
	defer loginAttemptsMu.Unlock()
	delete(loginAttempts, ip)
}

// Auth handles dashboard authentication endpoints.
type Auth struct {
	db             *sql.DB
	logger         *zap.Logger
	jwtSecret      []byte
	sessionTTL     time.Duration
	onAdminCreated func()
}

// NewAuth creates an Auth handler.
func NewAuth(database *sql.DB, logger *zap.Logger, jwtSecret []byte, sessionTTL time.Duration, onAdminCreated func()) *Auth {
	return &Auth{
		db:             database,
		logger:         logger,
		jwtSecret:      jwtSecret,
		sessionTTL:     sessionTTL,
		onAdminCreated: onAdminCreated,
	}
}

// Status returns the current auth state for the dashboard.
// GET /api/v1/auth/status
func (h *Auth) Status(w http.ResponseWriter, r *http.Request) {
	exists, err := db.AdminExists(h.db)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to check admin status")
		return
	}

	if !exists {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"setup_required": true,
			"authenticated":  false,
		})
		return
	}

	// Check if the request has a valid JWT
	if claims, ok := agentmw.ParseJWTFromRequest(r, h.jwtSecret); ok {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"setup_required": false,
			"authenticated":  true,
			"username":       claims.Username,
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"setup_required": false,
		"authenticated":  false,
	})
}

// Setup creates the initial admin account.
// POST /api/v1/auth/setup
func (h *Auth) Setup(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	if !safeUsername.MatchString(req.Username) {
		writeError(w, http.StatusBadRequest, "username must be 3-64 alphanumeric characters, hyphens, or underscores")
		return
	}
	if len(req.Password) < 8 {
		writeError(w, http.StatusBadRequest, "password must be at least 8 characters")
		return
	}
	if len(req.Password) > 72 {
		writeError(w, http.StatusBadRequest, "password must be at most 72 characters")
		return
	}

	if err := db.CreateAdmin(h.db, req.Username, req.Password); err != nil {
		if errors.Is(err, db.ErrAdminExists) {
			writeError(w, http.StatusConflict, "admin account already exists")
			return
		}
		h.logger.Error("create admin failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "failed to create admin")
		return
	}

	h.onAdminCreated()
	db.LogAction(h.db, "admin.setup", "auth", "", req.Username, "Admin account created", true)
	h.logger.Info("admin account created", zap.String("username", req.Username))

	// Auto-login: generate JWT immediately
	token, expiresAt, err := h.generateToken(req.Username)
	if err != nil {
		// Account created but token generation failed — user can login manually
		writeJSON(w, http.StatusCreated, map[string]string{"message": "Admin account created. Please log in."})
		return
	}

	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"message":    "Admin account created",
		"token":      token,
		"expires_at": expiresAt.Format(time.RFC3339),
	})
}

// Login validates credentials and returns a JWT.
// POST /api/v1/auth/login
func (h *Auth) Login(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	if req.Username == "" || req.Password == "" {
		writeError(w, http.StatusBadRequest, "username and password are required")
		return
	}

	// Brute-force protection: check if this IP is locked out
	ip := stripPort(r.RemoteAddr)
	if err := checkLoginRateLimit(ip); err != nil {
		writeError(w, http.StatusTooManyRequests, err.Error())
		return
	}

	user, err := db.ValidateAdmin(h.db, req.Username, req.Password)
	if err != nil {
		h.logger.Error("validate admin failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "authentication error")
		return
	}

	if user == nil {
		recordLoginFailure(ip)
		db.LogAction(h.db, "admin.login_failed", "auth", "", req.Username, "Invalid credentials", false)
		h.logger.Warn("failed login attempt", zap.String("username", req.Username), zap.String("ip", ip))
		writeError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}

	recordLoginSuccess(ip)

	token, expiresAt, err := h.generateToken(user.Username)
	if err != nil {
		h.logger.Error("generate JWT failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "failed to generate token")
		return
	}

	db.LogAction(h.db, "admin.login", "auth", "", user.Username, "Dashboard login", true)

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"token":      token,
		"expires_at": expiresAt.Format(time.RFC3339),
	})
}

func (h *Auth) generateToken(username string) (string, time.Time, error) {
	now := time.Now()
	expiresAt := now.Add(h.sessionTTL)

	claims := &agentmw.JWTClaims{
		Username: username,
		RegisteredClaims: jwt.RegisteredClaims{
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(expiresAt),
			Issuer:    "sistemo",
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenStr, err := token.SignedString(h.jwtSecret)
	if err != nil {
		return "", time.Time{}, err
	}
	return tokenStr, expiresAt, nil
}
