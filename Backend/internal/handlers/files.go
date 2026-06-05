package handlers

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	authmw "lovable-backend/internal/middleware"
)

type FilesHandler struct {
	db *pgxpool.Pool
}

func NewFilesHandler(db *pgxpool.Pool) *FilesHandler {
	return &FilesHandler{db: db}
}

type fileDTO struct {
	Path      string    `json:"path"`
	Content   string    `json:"content"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ownsProject verifies the project belongs to the user.
func (h *FilesHandler) ownsProject(r *http.Request, projectID string) bool {
	userID := authmw.GetUserID(r)
	var count int
	_ = h.db.QueryRow(r.Context(),
		`SELECT COUNT(*) FROM projects WHERE id = $1 AND user_id = $2`,
		projectID, userID,
	).Scan(&count)
	return count > 0
}

func (h *FilesHandler) List(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	if !h.ownsProject(r, projectID) {
		writeError(w, "not found", http.StatusNotFound)
		return
	}

	rows, err := h.db.Query(r.Context(),
		`SELECT path, content, updated_at FROM project_files
		 WHERE project_id = $1 ORDER BY path`,
		projectID,
	)
	if err != nil {
		writeError(w, "failed to list files", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	files := []fileDTO{}
	for rows.Next() {
		var f fileDTO
		if err := rows.Scan(&f.Path, &f.Content, &f.UpdatedAt); err != nil {
			continue
		}
		files = append(files, f)
	}

	writeJSON(w, files, http.StatusOK)
}

func (h *FilesHandler) Write(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	if !h.ownsProject(r, projectID) {
		writeError(w, "not found", http.StatusNotFound)
		return
	}

	var req struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Path == "" {
		writeError(w, "path and content required", http.StatusBadRequest)
		return
	}

	// Prevent path traversal
	if strings.Contains(req.Path, "..") || strings.HasPrefix(req.Path, "/") {
		writeError(w, "invalid path", http.StatusBadRequest)
		return
	}

	_, err := h.db.Exec(r.Context(),
		`INSERT INTO project_files (project_id, path, content, updated_at)
		 VALUES ($1, $2, $3, NOW())
		 ON CONFLICT (project_id, path)
		 DO UPDATE SET content = EXCLUDED.content, updated_at = NOW()`,
		projectID, req.Path, req.Content,
	)
	if err != nil {
		writeError(w, "failed to write file", http.StatusInternalServerError)
		return
	}

	writeJSON(w, map[string]string{"status": "ok"}, http.StatusOK)
}

func (h *FilesHandler) Delete(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	if !h.ownsProject(r, projectID) {
		writeError(w, "not found", http.StatusNotFound)
		return
	}

	var req struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Path == "" {
		writeError(w, "path required", http.StatusBadRequest)
		return
	}

	_, err := h.db.Exec(r.Context(),
		`DELETE FROM project_files WHERE project_id = $1 AND path = $2`,
		projectID, req.Path,
	)
	if err != nil {
		writeError(w, "failed to delete file", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
