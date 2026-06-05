package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	authmw "lovable-backend/internal/middleware"
)

const e2bAPIURL = "https://api.e2b.dev/sandboxes"

type SandboxHandler struct {
	db        *pgxpool.Pool
	e2bAPIKey string
}

func NewSandboxHandler(db *pgxpool.Pool, e2bAPIKey string) *SandboxHandler {
	return &SandboxHandler{db: db, e2bAPIKey: e2bAPIKey}
}

type e2bSandbox struct {
	SandboxID  string `json:"sandboxId"`
	TemplateID string `json:"templateId"`
	ClientID   string `json:"clientId"`
}

// Create boots a new E2B sandbox for a project and stores the ID + preview URL.
func (h *SandboxHandler) Create(w http.ResponseWriter, r *http.Request) {
	if h.e2bAPIKey == "" {
		writeError(w, "E2B_API_KEY not configured — sandbox unavailable", http.StatusNotImplemented)
		return
	}

	userID := authmw.GetUserID(r)
	projectID := chi.URLParam(r, "id")

	var projectName string
	if err := h.db.QueryRow(r.Context(),
		`SELECT name FROM projects WHERE id = $1 AND user_id = $2`,
		projectID, userID,
	).Scan(&projectName); err != nil {
		writeError(w, "project not found", http.StatusNotFound)
		return
	}

	// Create E2B sandbox with a React/Vite template
	sandbox, err := h.createE2BSandbox(r.Context())
	if err != nil {
		writeError(w, fmt.Sprintf("failed to create sandbox: %v", err), http.StatusInternalServerError)
		return
	}

	// Write all project files into the sandbox
	files, _ := h.loadProjectFiles(r.Context(), projectID)
	for _, f := range files {
		h.writeFileToSandbox(r.Context(), sandbox.SandboxID, f.path, f.content)
	}

	// Run npm install + vite dev in sandbox
	h.runCommandInSandbox(r.Context(), sandbox.SandboxID, "npm install")
	h.runCommandInSandbox(r.Context(), sandbox.SandboxID, "npm run dev &")

	// E2B preview URL pattern
	previewURL := fmt.Sprintf("https://%s-3000.e2b.dev", sandbox.SandboxID)

	// Save sandbox ID and preview URL to the project
	h.db.Exec(r.Context(),
		`UPDATE projects SET sandbox_id = $1, preview_url = $2, updated_at = NOW() WHERE id = $3`,
		sandbox.SandboxID, previewURL, projectID,
	)

	writeJSON(w, map[string]string{
		"sandbox_id":  sandbox.SandboxID,
		"preview_url": previewURL,
	}, http.StatusOK)
}

// Destroy stops a sandbox.
func (h *SandboxHandler) Destroy(w http.ResponseWriter, r *http.Request) {
	if h.e2bAPIKey == "" {
		writeError(w, "E2B not configured", http.StatusNotImplemented)
		return
	}

	userID := authmw.GetUserID(r)
	projectID := chi.URLParam(r, "id")

	var sandboxID string
	if err := h.db.QueryRow(r.Context(),
		`SELECT sandbox_id FROM projects WHERE id = $1 AND user_id = $2 AND sandbox_id IS NOT NULL`,
		projectID, userID,
	).Scan(&sandboxID); err != nil {
		writeError(w, "no active sandbox", http.StatusNotFound)
		return
	}

	h.deleteE2BSandbox(r.Context(), sandboxID)
	h.db.Exec(r.Context(),
		`UPDATE projects SET sandbox_id = NULL, preview_url = NULL WHERE id = $1`, projectID,
	)

	w.WriteHeader(http.StatusNoContent)
}

// ── E2B API calls ─────────────────────────────────────────
func (h *SandboxHandler) createE2BSandbox(ctx context.Context) (*e2bSandbox, error) {
	body := map[string]any{
		"templateID": "base",
		"timeout":    3600, // 1 hour
	}
	b, _ := json.Marshal(body)

	req, err := http.NewRequestWithContext(ctx, "POST", e2bAPIURL, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", h.e2bAPIKey)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("E2B API error %d: %s", resp.StatusCode, string(body))
	}

	var sandbox e2bSandbox
	json.NewDecoder(resp.Body).Decode(&sandbox)
	return &sandbox, nil
}

func (h *SandboxHandler) writeFileToSandbox(ctx context.Context, sandboxID, path, content string) error {
	url := fmt.Sprintf("https://api.e2b.dev/sandboxes/%s/files", sandboxID)
	body, _ := json.Marshal(map[string]string{"path": "/home/user/" + path, "content": content})

	req, _ := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", h.e2bAPIKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

func (h *SandboxHandler) runCommandInSandbox(ctx context.Context, sandboxID, cmd string) (string, error) {
	url := fmt.Sprintf("https://api.e2b.dev/sandboxes/%s/commands", sandboxID)
	body, _ := json.Marshal(map[string]any{"cmd": cmd, "timeout": 60})

	req, _ := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", h.e2bAPIKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var result map[string]any
	json.NewDecoder(resp.Body).Decode(&result)
	stdout, _ := result["stdout"].(string)
	return stdout, nil
}

func (h *SandboxHandler) deleteE2BSandbox(ctx context.Context, sandboxID string) {
	url := fmt.Sprintf("%s/%s", e2bAPIURL, sandboxID)
	req, _ := http.NewRequestWithContext(ctx, "DELETE", url, nil)
	req.Header.Set("X-API-Key", h.e2bAPIKey)
	resp, err := http.DefaultClient.Do(req)
	if err == nil {
		resp.Body.Close()
	}
}

type fileRow struct{ path, content string }

func (h *SandboxHandler) loadProjectFiles(ctx context.Context, projectID string) ([]fileRow, error) {
	rows, err := h.db.Query(ctx,
		`SELECT path, content FROM project_files WHERE project_id = $1 ORDER BY path`,
		projectID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var files []fileRow
	for rows.Next() {
		var f fileRow
		rows.Scan(&f.path, &f.content)
		files = append(files, f)
	}
	return files, nil
}
