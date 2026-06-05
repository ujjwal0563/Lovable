package handlers

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	authmw "lovable-backend/internal/middleware"
)

type ProjectsHandler struct {
	db *pgxpool.Pool
}

func NewProjectsHandler(db *pgxpool.Pool) *ProjectsHandler {
	return &ProjectsHandler{db: db}
}

type projectDTO struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	SandboxID   *string   `json:"sandbox_id"`
	PreviewURL  *string   `json:"preview_url"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func (h *ProjectsHandler) List(w http.ResponseWriter, r *http.Request) {
	userID := authmw.GetUserID(r)

	rows, err := h.db.Query(r.Context(),
		`SELECT id, name, description, sandbox_id, preview_url, created_at, updated_at
		 FROM projects WHERE user_id = $1 ORDER BY updated_at DESC`,
		userID,
	)
	if err != nil {
		writeError(w, "failed to list projects", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	projects := []projectDTO{}
	for rows.Next() {
		var p projectDTO
		if err := rows.Scan(&p.ID, &p.Name, &p.Description, &p.SandboxID, &p.PreviewURL, &p.CreatedAt, &p.UpdatedAt); err != nil {
			continue
		}
		projects = append(projects, p)
	}

	writeJSON(w, projects, http.StatusOK)
}

func (h *ProjectsHandler) Create(w http.ResponseWriter, r *http.Request) {
	userID := authmw.GetUserID(r)

	var req struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
		writeError(w, "name is required", http.StatusBadRequest)
		return
	}

	var p projectDTO
	err := h.db.QueryRow(r.Context(),
		`INSERT INTO projects (user_id, name, description)
		 VALUES ($1, $2, $3)
		 RETURNING id, name, description, sandbox_id, preview_url, created_at, updated_at`,
		userID, req.Name, req.Description,
	).Scan(&p.ID, &p.Name, &p.Description, &p.SandboxID, &p.PreviewURL, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		writeError(w, "failed to create project", http.StatusInternalServerError)
		return
	}

	writeJSON(w, p, http.StatusCreated)
}

func (h *ProjectsHandler) Get(w http.ResponseWriter, r *http.Request) {
	userID := authmw.GetUserID(r)
	projectID := chi.URLParam(r, "id")

	var p projectDTO
	err := h.db.QueryRow(r.Context(),
		`SELECT id, name, description, sandbox_id, preview_url, created_at, updated_at
		 FROM projects WHERE id = $1 AND user_id = $2`,
		projectID, userID,
	).Scan(&p.ID, &p.Name, &p.Description, &p.SandboxID, &p.PreviewURL, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		writeError(w, "project not found", http.StatusNotFound)
		return
	}

	writeJSON(w, p, http.StatusOK)
}

func (h *ProjectsHandler) Update(w http.ResponseWriter, r *http.Request) {
	userID := authmw.GetUserID(r)
	projectID := chi.URLParam(r, "id")

	var req struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, "invalid body", http.StatusBadRequest)
		return
	}

	_, err := h.db.Exec(r.Context(),
		`UPDATE projects SET name = COALESCE(NULLIF($1,''), name),
		 description = COALESCE(NULLIF($2,''), description),
		 updated_at = NOW()
		 WHERE id = $3 AND user_id = $4`,
		req.Name, req.Description, projectID, userID,
	)
	if err != nil {
		writeError(w, "failed to update project", http.StatusInternalServerError)
		return
	}

	h.Get(w, r)
}

func (h *ProjectsHandler) Delete(w http.ResponseWriter, r *http.Request) {
	userID := authmw.GetUserID(r)
	projectID := chi.URLParam(r, "id")

	_, err := h.db.Exec(r.Context(),
		`DELETE FROM projects WHERE id = $1 AND user_id = $2`,
		projectID, userID,
	)
	if err != nil {
		writeError(w, "failed to delete project", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
