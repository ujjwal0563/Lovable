package handlers

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	authmw "lovable-backend/internal/middleware"
)

type ShareHandler struct {
	db *pgxpool.Pool
}

func NewShareHandler(db *pgxpool.Pool) *ShareHandler {
	return &ShareHandler{db: db}
}

// EnableSharing generates a public share token for a project
// POST /api/projects/:id/share
func (h *ShareHandler) Enable(w http.ResponseWriter, r *http.Request) {
	userID := authmw.GetUserID(r)
	projectID := chi.URLParam(r, "id")

	// Verify ownership
	var ownerID string
	if err := h.db.QueryRow(r.Context(),
		`SELECT user_id FROM projects WHERE id = $1`, projectID,
	).Scan(&ownerID); err != nil || ownerID != userID {
		writeError(w, "project not found", http.StatusNotFound)
		return
	}

	// Check if already has a share token
	var existing *string
	h.db.QueryRow(r.Context(),
		`SELECT share_token FROM projects WHERE id = $1`, projectID,
	).Scan(&existing)

	if existing != nil && *existing != "" {
		writeJSON(w, map[string]string{
			"share_token": *existing,
			"share_url":   "http://localhost:5173/share/" + *existing,
			"status":      "existing",
		}, http.StatusOK)
		return
	}

	// Generate new share token
	b := make([]byte, 16)
	rand.Read(b)
	token := hex.EncodeToString(b)

	_, err := h.db.Exec(r.Context(),
		`UPDATE projects SET share_token = $1 WHERE id = $2`,
		token, projectID,
	)
	if err != nil {
		writeError(w, "failed to enable sharing", http.StatusInternalServerError)
		return
	}

	writeJSON(w, map[string]string{
		"share_token": token,
		"share_url":   "http://localhost:5173/share/" + token,
		"status":      "created",
	}, http.StatusOK)
}

// DisableSharing removes the share token
// DELETE /api/projects/:id/share
func (h *ShareHandler) Disable(w http.ResponseWriter, r *http.Request) {
	userID := authmw.GetUserID(r)
	projectID := chi.URLParam(r, "id")

	var ownerID string
	if err := h.db.QueryRow(r.Context(),
		`SELECT user_id FROM projects WHERE id = $1`, projectID,
	).Scan(&ownerID); err != nil || ownerID != userID {
		writeError(w, "project not found", http.StatusNotFound)
		return
	}

	h.db.Exec(r.Context(),
		`UPDATE projects SET share_token = NULL WHERE id = $1`, projectID,
	)

	w.WriteHeader(http.StatusNoContent)
}

// GetShared returns a public read-only view of a project
// GET /api/share/:token  (PUBLIC — no auth needed)
func (h *ShareHandler) GetShared(w http.ResponseWriter, r *http.Request) {
	token := chi.URLParam(r, "token")

	// Find project by share token
	var projectID, name, description string
	err := h.db.QueryRow(r.Context(),
		`SELECT id, name, description FROM projects WHERE share_token = $1`,
		token,
	).Scan(&projectID, &name, &description)
	if err != nil {
		writeError(w, "shared project not found or link expired", http.StatusNotFound)
		return
	}

	// Load files (read-only)
	rows, err := h.db.Query(r.Context(),
		`SELECT path, content, updated_at FROM project_files
		 WHERE project_id = $1 ORDER BY path`,
		projectID,
	)
	if err != nil {
		writeError(w, "failed to load files", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	type sharedFile struct {
		Path      string `json:"path"`
		Content   string `json:"content"`
		UpdatedAt string `json:"updated_at"`
	}

	var files []sharedFile
	for rows.Next() {
		var f sharedFile
		if err := rows.Scan(&f.Path, &f.Content, &f.UpdatedAt); err != nil {
			continue
		}
		files = append(files, f)
	}

	writeJSON(w, map[string]any{
		"id":          projectID,
		"name":        name,
		"description": description,
		"files":       files,
		"file_count":  len(files),
		"readonly":    true,
	}, http.StatusOK)
}
