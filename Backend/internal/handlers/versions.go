package handlers

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	authmw "lovable-backend/internal/middleware"
)

type VersionsHandler struct {
	db *pgxpool.Pool
}

func NewVersionsHandler(db *pgxpool.Pool) *VersionsHandler {
	return &VersionsHandler{db: db}
}

type versionDTO struct {
	ID        string          `json:"id"`
	ProjectID string          `json:"project_id"`
	Label     string          `json:"label"`
	Snapshot  json.RawMessage `json:"snapshot"`
	CreatedAt time.Time       `json:"created_at"`
}

// ownsProject checks project ownership.
func (h *VersionsHandler) ownsProject(r *http.Request, projectID string) bool {
	userID := authmw.GetUserID(r)
	var count int
	_ = h.db.QueryRow(r.Context(),
		`SELECT COUNT(*) FROM projects WHERE id = $1 AND user_id = $2`,
		projectID, userID,
	).Scan(&count)
	return count > 0
}

// List returns all versions for a project.
func (h *VersionsHandler) List(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	if !h.ownsProject(r, projectID) {
		writeError(w, "not found", http.StatusNotFound)
		return
	}

	rows, err := h.db.Query(r.Context(),
		`SELECT id, project_id, label, snapshot, created_at
		 FROM project_versions WHERE project_id = $1
		 ORDER BY created_at DESC`,
		projectID,
	)
	if err != nil {
		writeError(w, "failed to list versions", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	versions := []versionDTO{}
	for rows.Next() {
		var v versionDTO
		if err := rows.Scan(&v.ID, &v.ProjectID, &v.Label, &v.Snapshot, &v.CreatedAt); err != nil {
			continue
		}
		versions = append(versions, v)
	}

	writeJSON(w, versions, http.StatusOK)
}

// Create snapshots the current file state as a named version.
func (h *VersionsHandler) Create(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	if !h.ownsProject(r, projectID) {
		writeError(w, "not found", http.StatusNotFound)
		return
	}

	var req struct {
		Label string `json:"label"`
	}
	json.NewDecoder(r.Body).Decode(&req)

	// Build snapshot from current files
	rows, err := h.db.Query(r.Context(),
		`SELECT path, content FROM project_files WHERE project_id = $1 ORDER BY path`,
		projectID,
	)
	if err != nil {
		writeError(w, "failed to read files", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	type fileSnap struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	var snapshotFiles []fileSnap
	for rows.Next() {
		var f fileSnap
		rows.Scan(&f.Path, &f.Content)
		snapshotFiles = append(snapshotFiles, f)
	}
	snapshot, _ := json.Marshal(snapshotFiles)

	label := req.Label
	if label == "" {
		label = time.Now().Format("Jan 2, 2006 15:04")
	}

	var v versionDTO
	err = h.db.QueryRow(r.Context(),
		`INSERT INTO project_versions (project_id, label, snapshot)
		 VALUES ($1, $2, $3)
		 RETURNING id, project_id, label, snapshot, created_at`,
		projectID, label, snapshot,
	).Scan(&v.ID, &v.ProjectID, &v.Label, &v.Snapshot, &v.CreatedAt)
	if err != nil {
		writeError(w, "failed to create version", http.StatusInternalServerError)
		return
	}

	writeJSON(w, v, http.StatusCreated)
}

// Restore rolls the project files back to a saved version.
func (h *VersionsHandler) Restore(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	versionID := chi.URLParam(r, "versionId")

	if !h.ownsProject(r, projectID) {
		writeError(w, "not found", http.StatusNotFound)
		return
	}

	var rawSnapshot json.RawMessage
	err := h.db.QueryRow(r.Context(),
		`SELECT snapshot FROM project_versions WHERE id = $1 AND project_id = $2`,
		versionID, projectID,
	).Scan(&rawSnapshot)
	if err != nil {
		writeError(w, "version not found", http.StatusNotFound)
		return
	}

	type fileSnap struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	var snapFiles []fileSnap
	if err := json.Unmarshal(rawSnapshot, &snapFiles); err != nil {
		writeError(w, "invalid snapshot", http.StatusInternalServerError)
		return
	}

	// Delete current files, write snapshot files — in a transaction
	tx, err := h.db.Begin(r.Context())
	if err != nil {
		writeError(w, "transaction failed", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback(r.Context())

	tx.Exec(r.Context(), `DELETE FROM project_files WHERE project_id = $1`, projectID)

	for _, f := range snapFiles {
		tx.Exec(r.Context(),
			`INSERT INTO project_files (project_id, path, content) VALUES ($1, $2, $3)`,
			projectID, f.Path, f.Content,
		)
	}

	// Bump project updated_at
	tx.Exec(r.Context(), `UPDATE projects SET updated_at = NOW() WHERE id = $1`, projectID)

	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, "commit failed", http.StatusInternalServerError)
		return
	}

	writeJSON(w, map[string]string{"status": "restored"}, http.StatusOK)
}
