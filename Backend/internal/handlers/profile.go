package handlers

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"

	authmw "lovable-backend/internal/middleware"
)

type ProfileHandler struct {
	db *pgxpool.Pool
}

func NewProfileHandler(db *pgxpool.Pool) *ProfileHandler {
	return &ProfileHandler{db: db}
}

// UpdateProfile handles PATCH /api/auth/profile
// Updates name and/or email
func (h *ProfileHandler) UpdateProfile(w http.ResponseWriter, r *http.Request) {
	userID := authmw.GetUserID(r)

	var req struct {
		Name  string `json:"name"`
		Email string `json:"email"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, "invalid request body", http.StatusBadRequest)
		return
	}

	// Build dynamic update
	if req.Name == "" && req.Email == "" {
		writeError(w, "provide name or email to update", http.StatusBadRequest)
		return
	}

	var name, email string
	h.db.QueryRow(r.Context(),
		`SELECT name, email FROM users WHERE id = $1`, userID,
	).Scan(&name, &email)

	if req.Name != "" {
		name = strings.TrimSpace(req.Name)
	}
	if req.Email != "" {
		email = strings.ToLower(strings.TrimSpace(req.Email))
	}

	var updated userDTO
	err := h.db.QueryRow(r.Context(),
		`UPDATE users SET name = $1, email = $2 WHERE id = $3
		 RETURNING id, email, name`,
		name, email, userID,
	).Scan(&updated.ID, &updated.Email, &updated.Name)
	if err != nil {
		writeError(w, "failed to update profile — email may already be in use", http.StatusConflict)
		return
	}

	writeJSON(w, updated, http.StatusOK)
}

// ChangePassword handles POST /api/auth/change-password
func (h *ProfileHandler) ChangePassword(w http.ResponseWriter, r *http.Request) {
	userID := authmw.GetUserID(r)

	var req struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.CurrentPassword == "" || req.NewPassword == "" {
		writeError(w, "current_password and new_password required", http.StatusBadRequest)
		return
	}
	if len(req.NewPassword) < 8 {
		writeError(w, "new password must be at least 8 characters", http.StatusBadRequest)
		return
	}

	// Get current hash
	var hash string
	if err := h.db.QueryRow(r.Context(),
		`SELECT password FROM users WHERE id = $1`, userID,
	).Scan(&hash); err != nil {
		writeError(w, "user not found", http.StatusNotFound)
		return
	}

	// Verify current password
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(req.CurrentPassword)); err != nil {
		writeError(w, "current password is incorrect", http.StatusUnauthorized)
		return
	}

	// Hash new password
	newHash, err := bcrypt.GenerateFromPassword([]byte(req.NewPassword), bcrypt.DefaultCost)
	if err != nil {
		writeError(w, "internal error", http.StatusInternalServerError)
		return
	}

	h.db.Exec(r.Context(),
		`UPDATE users SET password = $1 WHERE id = $2`, string(newHash), userID,
	)

	writeJSON(w, map[string]string{"message": "Password changed successfully"}, http.StatusOK)
}

// DeleteAccount handles DELETE /api/auth/account
// Deletes user account and all their data
func (h *ProfileHandler) DeleteAccount(w http.ResponseWriter, r *http.Request) {
	userID := authmw.GetUserID(r)

	var req struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Password == "" {
		writeError(w, "password required to delete account", http.StatusBadRequest)
		return
	}

	// Verify password before deleting
	var hash string
	if err := h.db.QueryRow(r.Context(),
		`SELECT password FROM users WHERE id = $1`, userID,
	).Scan(&hash); err != nil {
		writeError(w, "user not found", http.StatusNotFound)
		return
	}

	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(req.Password)); err != nil {
		writeError(w, "incorrect password", http.StatusUnauthorized)
		return
	}

	// Delete user (cascade deletes all projects, files, messages etc.)
	h.db.Exec(r.Context(), `DELETE FROM users WHERE id = $1`, userID)

	writeJSON(w, map[string]string{"message": "Account deleted successfully"}, http.StatusOK)
}
