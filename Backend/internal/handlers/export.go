package handlers

import (
	"archive/zip"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	authmw "lovable-backend/internal/middleware"
)

type ExportHandler struct {
	db *pgxpool.Pool
}

func NewExportHandler(db *pgxpool.Pool) *ExportHandler {
	return &ExportHandler{db: db}
}

// Export streams a ZIP of all project files + a basic package.json scaffold.
func (h *ExportHandler) Export(w http.ResponseWriter, r *http.Request) {
	// Accept token from Authorization header OR ?token= query param (for download links)
	userID := authmw.GetUserID(r)
	if userID == "" {
		// Try query param
		userID = r.URL.Query().Get("token")
	}
	_ = userID // userID is set via middleware; query token handled in middleware
	userID = authmw.GetUserID(r)

	projectID := chi.URLParam(r, "id")

	// Verify ownership + get name
	var projectName string
	if err := h.db.QueryRow(r.Context(),
		`SELECT name FROM projects WHERE id = $1 AND user_id = $2`,
		projectID, userID,
	).Scan(&projectName); err != nil {
		writeError(w, "project not found", http.StatusNotFound)
		return
	}

	// Load all files
	rows, err := h.db.Query(r.Context(),
		`SELECT path, content FROM project_files WHERE project_id = $1 ORDER BY path`,
		projectID,
	)
	if err != nil {
		writeError(w, "failed to load files", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	type fileEntry struct{ path, content string }
	var projectFiles []fileEntry
	for rows.Next() {
		var f fileEntry
		if err := rows.Scan(&f.path, &f.content); err == nil {
			projectFiles = append(projectFiles, f)
		}
	}

	// Sanitize project name for filename
	safeName := strings.ReplaceAll(strings.ToLower(projectName), " ", "-")
	zipName := fmt.Sprintf("%s-%s.zip", safeName, time.Now().Format("20060102"))

	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, zipName))
	w.Header().Set("Cache-Control", "no-cache")

	zw := zip.NewWriter(w)
	defer zw.Close()

	dirPrefix := safeName + "/"

	// Write each project file
	for _, f := range projectFiles {
		fw, err := zw.Create(dirPrefix + f.path)
		if err != nil {
			continue
		}
		fw.Write([]byte(f.content))
	}

	// If no package.json exists, add a starter one
	hasPkg := false
	for _, f := range projectFiles {
		if f.path == "package.json" {
			hasPkg = true
			break
		}
	}
	if !hasPkg {
		fw, _ := zw.Create(dirPrefix + "package.json")
		fw.Write([]byte(defaultPackageJSON(projectName)))
	}

	// Add README
	readme, _ := zw.Create(dirPrefix + "README.md")
	readme.Write([]byte(fmt.Sprintf("# %s\n\nGenerated with Lovable AI Builder.\n\n## Getting started\n\n```bash\nnpm install\nnpm run dev\n```\n", projectName)))
}

func defaultPackageJSON(name string) string {
	safe := strings.ReplaceAll(strings.ToLower(name), " ", "-")
	return fmt.Sprintf(`{
  "name": "%s",
  "version": "0.1.0",
  "private": true,
  "type": "module",
  "scripts": {
    "dev": "vite",
    "build": "tsc -b && vite build",
    "preview": "vite preview"
  },
  "dependencies": {
    "react": "^18.3.1",
    "react-dom": "^18.3.1",
    "lucide-react": "^0.344.0"
  },
  "devDependencies": {
    "@types/react": "^18.3.1",
    "@types/react-dom": "^18.3.1",
    "@vitejs/plugin-react": "^4.3.1",
    "autoprefixer": "^10.4.18",
    "postcss": "^8.4.35",
    "tailwindcss": "^3.4.1",
    "typescript": "^5.4.2",
    "vite": "^5.1.6"
  }
}
`, safe)
}
