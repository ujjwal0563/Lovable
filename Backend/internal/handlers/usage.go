package handlers

import (
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	authmw "lovable-backend/internal/middleware"
)

type UsageHandler struct {
	db *pgxpool.Pool
}

func NewUsageHandler(db *pgxpool.Pool) *UsageHandler {
	return &UsageHandler{db: db}
}

// GetUsage returns usage stats for the current user
// GET /api/usage
func (h *UsageHandler) GetUsage(w http.ResponseWriter, r *http.Request) {
	userID := authmw.GetUserID(r)

	// Count total projects
	var totalProjects int
	h.db.QueryRow(r.Context(),
		`SELECT COUNT(*) FROM projects WHERE user_id = $1`, userID,
	).Scan(&totalProjects)

	// Count total messages this month
	var messagesThisMonth int
	h.db.QueryRow(r.Context(),
		`SELECT COUNT(*) FROM messages m
		 JOIN projects p ON p.id = m.project_id
		 WHERE p.user_id = $1
		 AND m.role = 'user'
		 AND m.created_at >= date_trunc('month', NOW())`,
		userID,
	).Scan(&messagesThisMonth)

	// Count total messages all time
	var totalMessages int
	h.db.QueryRow(r.Context(),
		`SELECT COUNT(*) FROM messages m
		 JOIN projects p ON p.id = m.project_id
		 WHERE p.user_id = $1 AND m.role = 'user'`,
		userID,
	).Scan(&totalMessages)

	// Count total files generated
	var totalFiles int
	h.db.QueryRow(r.Context(),
		`SELECT COUNT(*) FROM project_files pf
		 JOIN projects p ON p.id = pf.project_id
		 WHERE p.user_id = $1`,
		userID,
	).Scan(&totalFiles)

	// Count total versions saved
	var totalVersions int
	h.db.QueryRow(r.Context(),
		`SELECT COUNT(*) FROM project_versions pv
		 JOIN projects p ON p.id = pv.project_id
		 WHERE p.user_id = $1`,
		userID,
	).Scan(&totalVersions)

	// Count images uploaded
	var totalImages int
	h.db.QueryRow(r.Context(),
		`SELECT COUNT(*) FROM project_images pi
		 JOIN projects p ON p.id = pi.project_id
		 WHERE p.user_id = $1`,
		userID,
	).Scan(&totalImages)

	// Get account created date
	var createdAt time.Time
	h.db.QueryRow(r.Context(),
		`SELECT created_at FROM users WHERE id = $1`, userID,
	).Scan(&createdAt)

	writeJSON(w, map[string]any{
		"projects":            totalProjects,
		"messages_this_month": messagesThisMonth,
		"messages_total":      totalMessages,
		"files_generated":     totalFiles,
		"versions_saved":      totalVersions,
		"images_uploaded":     totalImages,
		"member_since":        createdAt,
		// Limits (free tier)
		"limits": map[string]any{
			"projects":           -1, // unlimited for localhost
			"messages_per_month": -1, // unlimited for localhost
		},
	}, http.StatusOK)
}

// GetProjectUsage returns usage stats for a specific project
// GET /api/projects/:id/usage
func (h *UsageHandler) GetProjectUsage(w http.ResponseWriter, r *http.Request) {
	userID := authmw.GetUserID(r)

	// Use chi to get projectId param — but since this is called via main.go we use r.PathValue
	// We'll extract from URL path manually for compatibility
	path := r.URL.Path
	// Extract project ID from path: /api/projects/{id}/usage
	parts := splitPath(path)
	if len(parts) < 4 {
		writeError(w, "invalid path", http.StatusBadRequest)
		return
	}
	projectID := parts[3]

	// Verify ownership
	var ownerID string
	if err := h.db.QueryRow(r.Context(),
		`SELECT user_id FROM projects WHERE id = $1`, projectID,
	).Scan(&ownerID); err != nil || ownerID != userID {
		writeError(w, "project not found", http.StatusNotFound)
		return
	}

	var msgCount, fileCount, versionCount int
	h.db.QueryRow(r.Context(),
		`SELECT COUNT(*) FROM messages WHERE project_id = $1 AND role = 'user'`, projectID,
	).Scan(&msgCount)
	h.db.QueryRow(r.Context(),
		`SELECT COUNT(*) FROM project_files WHERE project_id = $1`, projectID,
	).Scan(&fileCount)
	h.db.QueryRow(r.Context(),
		`SELECT COUNT(*) FROM project_versions WHERE project_id = $1`, projectID,
	).Scan(&versionCount)

	writeJSON(w, map[string]any{
		"messages": msgCount,
		"files":    fileCount,
		"versions": versionCount,
	}, http.StatusOK)
}

func splitPath(path string) []string {
	var parts []string
	current := ""
	for _, c := range path {
		if c == '/' {
			if current != "" {
				parts = append(parts, current)
				current = ""
			}
		} else {
			current += string(c)
		}
	}
	if current != "" {
		parts = append(parts, current)
	}
	return parts
}
