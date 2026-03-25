package handlers

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"go.uber.org/zap"

	agentmw "github.com/davidestf/sistemo/internal/agent/api/middleware"
	"github.com/davidestf/sistemo/internal/db"
)

var safeUsername = regexp.MustCompile(`^[a-zA-Z0-9_-]{3,64}$`)

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

	user, err := db.ValidateAdmin(h.db, req.Username, req.Password)
	if err != nil {
		h.logger.Error("validate admin failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "authentication error")
		return
	}

	if user == nil {
		db.LogAction(h.db, "admin.login_failed", "auth", "", req.Username, "Invalid credentials", false)
		h.logger.Warn("failed login attempt", zap.String("username", req.Username))
		writeError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}

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
