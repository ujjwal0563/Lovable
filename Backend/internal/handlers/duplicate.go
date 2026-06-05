package handlers

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	authmw "lovable-backend/internal/middleware"
)

type DuplicateHandler struct {
	db *pgxpool.Pool
}

func NewDuplicateHandler(db *pgxpool.Pool) *DuplicateHandler {
	return &DuplicateHandler{db: db}
}

// Duplicate creates a full copy of a project (metadata + files) for the same user.
func (h *DuplicateHandler) Duplicate(w http.ResponseWriter, r *http.Request) {
	userID := authmw.GetUserID(r)
	projectID := chi.URLParam(r, "id")

	// Verify ownership
	var srcName, srcDesc string
	err := h.db.QueryRow(r.Context(),
		`SELECT name, description FROM projects WHERE id = $1 AND user_id = $2`,
		projectID, userID,
	).Scan(&srcName, &srcDesc)
	if err != nil {
		writeError(w, "project not found", http.StatusNotFound)
		return
	}

	tx, err := h.db.Begin(r.Context())
	if err != nil {
		writeError(w, "transaction failed", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback(r.Context())

	// Create new project row
	var newID string
	err = tx.QueryRow(r.Context(),
		`INSERT INTO projects (user_id, name, description)
		 VALUES ($1, $2, $3) RETURNING id`,
		userID, srcName+" (copy)", srcDesc,
	).Scan(&newID)
	if err != nil {
		writeError(w, "failed to duplicate project", http.StatusInternalServerError)
		return
	}

	// Copy all files
	_, err = tx.Exec(r.Context(),
		`INSERT INTO project_files (project_id, path, content)
		 SELECT $1, path, content FROM project_files WHERE project_id = $2`,
		newID, projectID,
	)
	if err != nil {
		writeError(w, "failed to copy files", http.StatusInternalServerError)
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, "commit failed", http.StatusInternalServerError)
		return
	}

	writeJSON(w, map[string]string{"id": newID}, http.StatusCreated)
}
