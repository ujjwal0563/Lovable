package handlers

import (
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	authmw "lovable-backend/internal/middleware"
)

type SearchHandler struct {
	db *pgxpool.Pool
}

func NewSearchHandler(db *pgxpool.Pool) *SearchHandler {
	return &SearchHandler{db: db}
}

// Search handles GET /api/search?q=query
// Searches across projects and files for the current user
func (h *SearchHandler) Search(w http.ResponseWriter, r *http.Request) {
	userID := authmw.GetUserID(r)
	q := r.URL.Query().Get("q")

	if len(q) < 2 {
		writeError(w, "query must be at least 2 characters", http.StatusBadRequest)
		return
	}

	pattern := "%" + q + "%"

	// Search projects by name or description
	projectRows, err := h.db.Query(r.Context(),
		`SELECT id, name, description, updated_at
		 FROM projects
		 WHERE user_id = $1
		 AND (name ILIKE $2 OR description ILIKE $2)
		 ORDER BY updated_at DESC
		 LIMIT 10`,
		userID, pattern,
	)
	if err != nil {
		writeError(w, "search failed", http.StatusInternalServerError)
		return
	}
	defer projectRows.Close()

	type projectResult struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Description string `json:"description"`
		UpdatedAt   string `json:"updated_at"`
		Type        string `json:"type"`
	}

	var projects []projectResult
	for projectRows.Next() {
		var p projectResult
		p.Type = "project"
		if err := projectRows.Scan(&p.ID, &p.Name, &p.Description, &p.UpdatedAt); err != nil {
			continue
		}
		projects = append(projects, p)
	}

	// Search inside file contents
	fileRows, err := h.db.Query(r.Context(),
		`SELECT pf.project_id, pf.path,
		        LEFT(pf.content, 200) as snippet,
		        p.name as project_name
		 FROM project_files pf
		 JOIN projects p ON p.id = pf.project_id
		 WHERE p.user_id = $1
		 AND (pf.path ILIKE $2 OR pf.content ILIKE $2)
		 ORDER BY pf.updated_at DESC
		 LIMIT 10`,
		userID, pattern,
	)
	if err != nil {
		writeError(w, "file search failed", http.StatusInternalServerError)
		return
	}
	defer fileRows.Close()

	type fileResult struct {
		ProjectID   string `json:"project_id"`
		ProjectName string `json:"project_name"`
		Path        string `json:"path"`
		Snippet     string `json:"snippet"`
		Type        string `json:"type"`
	}

	var files []fileResult
	for fileRows.Next() {
		var f fileResult
		f.Type = "file"
		if err := fileRows.Scan(&f.ProjectID, &f.Path, &f.Snippet, &f.ProjectName); err != nil {
			continue
		}
		files = append(files, f)
	}

	// Search messages
	msgRows, err := h.db.Query(r.Context(),
		`SELECT m.project_id, p.name as project_name,
		        LEFT(m.content->>'text', 200) as snippet,
		        m.created_at
		 FROM messages m
		 JOIN projects p ON p.id = m.project_id
		 WHERE p.user_id = $1
		 AND m.role = 'user'
		 AND m.content->>'text' ILIKE $2
		 ORDER BY m.created_at DESC
		 LIMIT 5`,
		userID, pattern,
	)
	if err == nil {
		defer msgRows.Close()
	}

	type msgResult struct {
		ProjectID   string `json:"project_id"`
		ProjectName string `json:"project_name"`
		Snippet     string `json:"snippet"`
		CreatedAt   string `json:"created_at"`
		Type        string `json:"type"`
	}

	var messages []msgResult
	if err == nil {
		for msgRows.Next() {
			var m msgResult
			m.Type = "message"
			if err := msgRows.Scan(&m.ProjectID, &m.ProjectName, &m.Snippet, &m.CreatedAt); err != nil {
				continue
			}
			messages = append(messages, m)
		}
	}

	writeJSON(w, map[string]any{
		"query":    q,
		"projects": projects,
		"files":    files,
		"messages": messages,
		"total":    len(projects) + len(files) + len(messages),
	}, http.StatusOK)
}
