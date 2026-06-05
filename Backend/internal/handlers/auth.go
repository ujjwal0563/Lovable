package handlers

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"

	"lovable-backend/internal/email"
	authmw "lovable-backend/internal/middleware"
)

type AuthHandler struct {
	db             *pgxpool.Pool
	jwtSecret      string
	frontendOrigin string
	emailSender    *email.Sender
	googleClientID string
	googleSecret   string
	githubClientID string
	githubSecret   string
}

type AuthConfig struct {
	JWTSecret      string
	FrontendOrigin string
	Email          *email.Sender
	GoogleClientID string
	GoogleSecret   string
	GitHubClientID string
	GitHubSecret   string
}

func NewAuthHandler(db *pgxpool.Pool, cfg AuthConfig) *AuthHandler {
	return &AuthHandler{
		db:             db,
		jwtSecret:      cfg.JWTSecret,
		frontendOrigin: cfg.FrontendOrigin,
		emailSender:    cfg.Email,
		googleClientID: cfg.GoogleClientID,
		googleSecret:   cfg.GoogleSecret,
		githubClientID: cfg.GitHubClientID,
		githubSecret:   cfg.GitHubSecret,
	}
}

// ── DTOs ─────────────────────────────────────────────────
type authResponse struct {
	Token string  `json:"token"`
	User  userDTO `json:"user"`
}

type userDTO struct {
	ID    string `json:"id"`
	Email string `json:"email"`
	Name  string `json:"name"`
}

// ── Signup ───────────────────────────────────────────────
func (h *AuthHandler) Signup(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
		Name     string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Email == "" || req.Password == "" {
		writeError(w, "email and password required", http.StatusBadRequest)
		return
	}
	if len(req.Password) < 8 {
		writeError(w, "password must be at least 8 characters", http.StatusBadRequest)
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		writeError(w, "internal error", http.StatusInternalServerError)
		return
	}

	var user userDTO
	if err := h.db.QueryRow(r.Context(),
		`INSERT INTO users (email, password, name) VALUES ($1, $2, $3) RETURNING id, email, name`,
		strings.ToLower(req.Email), string(hash), req.Name,
	).Scan(&user.ID, &user.Email, &user.Name); err != nil {
		writeError(w, "email already in use", http.StatusConflict)
		return
	}

	token, err := h.generateToken(user.ID)
	if err != nil {
		writeError(w, "could not generate token", http.StatusInternalServerError)
		return
	}
	writeJSON(w, authResponse{Token: token, User: user}, http.StatusCreated)
}

// ── Login ────────────────────────────────────────────────
func (h *AuthHandler) Login(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, "invalid request body", http.StatusBadRequest)
		return
	}

	var user userDTO
	var hash string
	if err := h.db.QueryRow(r.Context(),
		`SELECT id, email, name, password FROM users WHERE email = $1`,
		strings.ToLower(req.Email),
	).Scan(&user.ID, &user.Email, &user.Name, &hash); err != nil {
		writeError(w, "invalid credentials", http.StatusUnauthorized)
		return
	}

	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(req.Password)); err != nil {
		writeError(w, "invalid credentials", http.StatusUnauthorized)
		return
	}

	token, err := h.generateToken(user.ID)
	if err != nil {
		writeError(w, "could not generate token", http.StatusInternalServerError)
		return
	}
	writeJSON(w, authResponse{Token: token, User: user}, http.StatusOK)
}

// ── Me ───────────────────────────────────────────────────
func (h *AuthHandler) Me(w http.ResponseWriter, r *http.Request) {
	userID := authmw.GetUserID(r)
	var user userDTO
	if err := h.db.QueryRow(r.Context(),
		`SELECT id, email, name FROM users WHERE id = $1`, userID,
	).Scan(&user.ID, &user.Email, &user.Name); err != nil {
		writeError(w, "user not found", http.StatusNotFound)
		return
	}
	writeJSON(w, user, http.StatusOK)
}

// ── Forgot password ──────────────────────────────────────
func (h *AuthHandler) ForgotPassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email string `json:"email"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Email == "" {
		writeError(w, "email required", http.StatusBadRequest)
		return
	}

	var userID, userEmail string
	err := h.db.QueryRow(r.Context(),
		`SELECT id, email FROM users WHERE email = $1`, strings.ToLower(req.Email),
	).Scan(&userID, &userEmail)

	// Always respond OK to avoid email enumeration
	writeJSON(w, map[string]string{"message": "If that email exists, a reset link has been sent."}, http.StatusOK)

	if err != nil {
		return // user not found — still returned 200 above
	}

	// Generate secure token
	rawToken := make([]byte, 32)
	rand.Read(rawToken)
	tokenStr := hex.EncodeToString(rawToken)
	expires := time.Now().Add(1 * time.Hour)

	h.db.Exec(r.Context(),
		`INSERT INTO password_reset_tokens (user_id, token, expires_at) VALUES ($1, $2, $3)`,
		userID, tokenStr, expires,
	)

	resetURL := fmt.Sprintf("%s/reset-password?token=%s", h.frontendOrigin, tokenStr)
	go h.emailSender.SendPasswordReset(userEmail, resetURL)
}

// ── Reset password ───────────────────────────────────────
func (h *AuthHandler) ResetPassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token    string `json:"token"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Token == "" || req.Password == "" {
		writeError(w, "token and password required", http.StatusBadRequest)
		return
	}
	if len(req.Password) < 8 {
		writeError(w, "password must be at least 8 characters", http.StatusBadRequest)
		return
	}

	var tokenID, userID string
	var expiresAt time.Time
	var used bool
	err := h.db.QueryRow(r.Context(),
		`SELECT id, user_id, expires_at, used FROM password_reset_tokens WHERE token = $1`,
		req.Token,
	).Scan(&tokenID, &userID, &expiresAt, &used)
	if err != nil || used || time.Now().After(expiresAt) {
		writeError(w, "invalid or expired reset token", http.StatusBadRequest)
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		writeError(w, "internal error", http.StatusInternalServerError)
		return
	}

	tx, _ := h.db.Begin(r.Context())
	defer tx.Rollback(r.Context())
	tx.Exec(r.Context(), `UPDATE users SET password = $1 WHERE id = $2`, string(hash), userID)
	tx.Exec(r.Context(), `UPDATE password_reset_tokens SET used = TRUE WHERE id = $1`, tokenID)
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, "failed to reset password", http.StatusInternalServerError)
		return
	}

	writeJSON(w, map[string]string{"message": "Password reset successfully."}, http.StatusOK)
}

// ── OAuth: GitHub ─────────────────────────────────────────
// Step 1: redirect to GitHub
func (h *AuthHandler) GitHubLogin(w http.ResponseWriter, r *http.Request) {
	if h.githubClientID == "" {
		writeError(w, "GitHub OAuth not configured", http.StatusNotImplemented)
		return
	}
	url := fmt.Sprintf(
		"https://github.com/login/oauth/authorize?client_id=%s&scope=user:email&state=gh",
		h.githubClientID,
	)
	http.Redirect(w, r, url, http.StatusTemporaryRedirect)
}

// Step 2: GitHub callback
func (h *AuthHandler) GitHubCallback(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("code")
	if code == "" {
		http.Redirect(w, r, h.frontendOrigin+"/login?error=oauth_failed", http.StatusTemporaryRedirect)
		return
	}

	// Exchange code for access token
	accessToken, err := h.githubExchangeCode(r.Context(), code)
	if err != nil {
		http.Redirect(w, r, h.frontendOrigin+"/login?error=oauth_failed", http.StatusTemporaryRedirect)
		return
	}

	// Get GitHub user info
	ghUser, err := h.githubGetUser(r.Context(), accessToken)
	if err != nil {
		http.Redirect(w, r, h.frontendOrigin+"/login?error=oauth_failed", http.StatusTemporaryRedirect)
		return
	}

	userID, err := h.findOrCreateOAuthUser(r.Context(), "github", ghUser["id"], ghUser["email"], ghUser["name"])
	if err != nil {
		http.Redirect(w, r, h.frontendOrigin+"/login?error=oauth_failed", http.StatusTemporaryRedirect)
		return
	}

	token, _ := h.generateToken(userID)
	http.Redirect(w, r, fmt.Sprintf("%s/oauth/callback?token=%s", h.frontendOrigin, token), http.StatusTemporaryRedirect)
}

// ── OAuth: Google ─────────────────────────────────────────
func (h *AuthHandler) GoogleLogin(w http.ResponseWriter, r *http.Request) {
	if h.googleClientID == "" {
		writeError(w, "Google OAuth not configured", http.StatusNotImplemented)
		return
	}
	redirectURI := h.frontendOrigin + "/api/auth/google/callback"
	url := fmt.Sprintf(
		"https://accounts.google.com/o/oauth2/v2/auth?client_id=%s&redirect_uri=%s&response_type=code&scope=openid+email+profile&state=google",
		h.googleClientID, redirectURI,
	)
	http.Redirect(w, r, url, http.StatusTemporaryRedirect)
}

func (h *AuthHandler) GoogleCallback(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("code")
	if code == "" {
		http.Redirect(w, r, h.frontendOrigin+"/login?error=oauth_failed", http.StatusTemporaryRedirect)
		return
	}

	redirectURI := "http://localhost:8080/api/auth/google/callback"
	googleUser, err := h.googleExchangeCode(r.Context(), code, redirectURI)
	if err != nil {
		http.Redirect(w, r, h.frontendOrigin+"/login?error=oauth_failed", http.StatusTemporaryRedirect)
		return
	}

	userID, err := h.findOrCreateOAuthUser(r.Context(), "google", googleUser["sub"], googleUser["email"], googleUser["name"])
	if err != nil {
		http.Redirect(w, r, h.frontendOrigin+"/login?error=oauth_failed", http.StatusTemporaryRedirect)
		return
	}

	token, _ := h.generateToken(userID)
	http.Redirect(w, r, fmt.Sprintf("%s/oauth/callback?token=%s", h.frontendOrigin, token), http.StatusTemporaryRedirect)
}

// ── Helpers ───────────────────────────────────────────────
func (h *AuthHandler) findOrCreateOAuthUser(ctx context.Context, provider, subject, email, name string) (string, error) {
	// Check if OAuth account already linked
	var userID string
	err := h.db.QueryRow(ctx,
		`SELECT user_id FROM oauth_accounts WHERE provider = $1 AND subject = $2`,
		provider, subject,
	).Scan(&userID)
	if err == nil {
		return userID, nil
	}

	// Check if user with same email exists
	err = h.db.QueryRow(ctx,
		`SELECT id FROM users WHERE email = $1`, strings.ToLower(email),
	).Scan(&userID)

	if err != nil {
		// Create new user (no password — OAuth only)
		err = h.db.QueryRow(ctx,
			`INSERT INTO users (email, password, name) VALUES ($1, $2, $3) RETURNING id`,
			strings.ToLower(email), "", name,
		).Scan(&userID)
		if err != nil {
			return "", fmt.Errorf("failed to create user: %w", err)
		}
	}

	// Link OAuth account
	h.db.Exec(ctx,
		`INSERT INTO oauth_accounts (user_id, provider, subject) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`,
		userID, provider, subject,
	)

	return userID, nil
}

func (h *AuthHandler) githubExchangeCode(ctx context.Context, code string) (string, error) {
	req, _ := http.NewRequestWithContext(ctx, "POST",
		"https://github.com/login/oauth/access_token", nil)
	q := req.URL.Query()
	q.Set("client_id", h.githubClientID)
	q.Set("client_secret", h.githubSecret)
	q.Set("code", code)
	req.URL.RawQuery = q.Encode()
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var result map[string]string
	json.NewDecoder(resp.Body).Decode(&result)
	token := result["access_token"]
	if token == "" {
		return "", fmt.Errorf("no access token")
	}
	return token, nil
}

func (h *AuthHandler) githubGetUser(ctx context.Context, token string) (map[string]string, error) {
	req, _ := http.NewRequestWithContext(ctx, "GET", "https://api.github.com/user", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var user map[string]any
	json.NewDecoder(resp.Body).Decode(&user)

	result := map[string]string{
		"id":    fmt.Sprintf("%v", user["id"]),
		"name":  fmt.Sprintf("%v", user["name"]),
		"email": fmt.Sprintf("%v", user["email"]),
	}

	// GitHub may not return email in /user — fetch from /user/emails
	if result["email"] == "<nil>" || result["email"] == "" {
		emailReq, _ := http.NewRequestWithContext(ctx, "GET", "https://api.github.com/user/emails", nil)
		emailReq.Header.Set("Authorization", "Bearer "+token)
		emailReq.Header.Set("Accept", "application/json")
		er, err := http.DefaultClient.Do(emailReq)
		if err == nil {
			defer er.Body.Close()
			var emails []map[string]any
			json.NewDecoder(er.Body).Decode(&emails)
			for _, e := range emails {
				if primary, _ := e["primary"].(bool); primary {
					result["email"] = fmt.Sprintf("%v", e["email"])
					break
				}
			}
		}
	}
	return result, nil
}

func (h *AuthHandler) googleExchangeCode(ctx context.Context, code, redirectURI string) (map[string]string, error) {
	// Exchange code for tokens
	reqBody := fmt.Sprintf(
		"code=%s&client_id=%s&client_secret=%s&redirect_uri=%s&grant_type=authorization_code",
		code, h.googleClientID, h.googleSecret, redirectURI,
	)
	req, _ := http.NewRequestWithContext(ctx, "POST",
		"https://oauth2.googleapis.com/token",
		strings.NewReader(reqBody),
	)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var tokenResp map[string]any
	json.NewDecoder(resp.Body).Decode(&tokenResp)
	accessToken, _ := tokenResp["access_token"].(string)
	if accessToken == "" {
		return nil, fmt.Errorf("no access token from Google")
	}

	// Get user info
	uReq, _ := http.NewRequestWithContext(ctx, "GET",
		"https://www.googleapis.com/oauth2/v3/userinfo", nil)
	uReq.Header.Set("Authorization", "Bearer "+accessToken)

	uResp, err := http.DefaultClient.Do(uReq)
	if err != nil {
		return nil, err
	}
	defer uResp.Body.Close()

	body, _ := io.ReadAll(uResp.Body)
	var info map[string]any
	json.Unmarshal(body, &info)

	return map[string]string{
		"sub":   fmt.Sprintf("%v", info["sub"]),
		"email": fmt.Sprintf("%v", info["email"]),
		"name":  fmt.Sprintf("%v", info["name"]),
	}, nil
}

func (h *AuthHandler) generateToken(userID string) (string, error) {
	claims := jwt.MapClaims{
		"sub": userID,
		"exp": time.Now().Add(7 * 24 * time.Hour).Unix(),
		"iat": time.Now().Unix(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(h.jwtSecret))
}
