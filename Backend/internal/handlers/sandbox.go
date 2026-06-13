package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	authmw "lovable-backend/internal/middleware"
)

const e2bAPIBase = "https://api.e2b.dev/sandboxes"

type SandboxHandler struct {
	db        *pgxpool.Pool
	e2bAPIKey string
}

func NewSandboxHandler(db *pgxpool.Pool, e2bAPIKey string) *SandboxHandler {
	return &SandboxHandler{db: db, e2bAPIKey: e2bAPIKey}
}

// e2bCreateResponse matches the real E2B API response shape
type e2bCreateResponse struct {
	SandboxID string `json:"sandboxId"`
}

// ── Create ────────────────────────────────────────────────────────────────────
// POST /api/projects/:id/sandbox
// Boots an E2B sandbox, writes all project files + scaffold, runs npm install + vite dev
func (h *SandboxHandler) Create(w http.ResponseWriter, r *http.Request) {
	if h.e2bAPIKey == "" {
		writeError(w, "E2B_API_KEY not configured — set it in .env to enable live preview", http.StatusNotImplemented)
		return
	}

	userID := authmw.GetUserID(r)
	projectID := chi.URLParam(r, "id")

	// Verify ownership
	var projectName string
	if err := h.db.QueryRow(r.Context(),
		`SELECT name FROM projects WHERE id = $1 AND user_id = $2`,
		projectID, userID,
	).Scan(&projectName); err != nil {
		writeError(w, "project not found", http.StatusNotFound)
		return
	}

	// If project already has a live sandbox, return its URL
	var existingSandboxID, existingPreviewURL *string
	h.db.QueryRow(r.Context(),
		`SELECT sandbox_id, preview_url FROM projects WHERE id = $1`,
		projectID,
	).Scan(&existingSandboxID, &existingPreviewURL)

	if existingSandboxID != nil && *existingSandboxID != "" {
		// Check if sandbox still alive
		if h.sandboxAlive(r.Context(), *existingSandboxID) {
			writeJSON(w, map[string]string{
				"sandbox_id":  *existingSandboxID,
				"preview_url": *existingPreviewURL,
				"status":      "existing",
			}, http.StatusOK)
			return
		}
	}

	// Create new E2B sandbox using React/Vite template
	sandboxID, err := h.createSandbox(r.Context())
	if err != nil {
		writeError(w, fmt.Sprintf("failed to create sandbox: %v", err), http.StatusInternalServerError)
		return
	}

	// Write scaffold files first (package.json, vite.config, index.html, etc.)
	if err := h.writeScaffold(r.Context(), sandboxID); err != nil {
		writeError(w, fmt.Sprintf("failed to write scaffold: %v", err), http.StatusInternalServerError)
		return
	}

	// Write all project files into the sandbox
	projectFiles, _ := h.loadProjectFiles(r.Context(), projectID)
	for _, f := range projectFiles {
		h.writeFile(r.Context(), sandboxID, "project/"+f.path, f.content)
	}

	// Run npm install (blocking — waits for completion)
	installOut, installErr := h.runCommand(r.Context(), sandboxID, "cd /home/user/project && npm install", 120)
	if installErr != nil {
		writeError(w, fmt.Sprintf("npm install failed: %v\n%s", installErr, installOut), http.StatusInternalServerError)
		return
	}

	// Start vite dev server in background (non-blocking)
	h.runCommandBackground(r.Context(), sandboxID, "cd /home/user/project && npm run dev -- --host 0.0.0.0 --port 5173")

	// Give vite 3 seconds to start
	time.Sleep(3 * time.Second)

	// E2B preview URL — port 5173 is the Vite dev server
	previewURL := fmt.Sprintf("https://%s-5173.e2b-tunnel.com", sandboxID)

	// Persist sandbox info
	h.db.Exec(r.Context(),
		`UPDATE projects SET sandbox_id = $1, preview_url = $2, updated_at = NOW() WHERE id = $3`,
		sandboxID, previewURL, projectID,
	)

	writeJSON(w, map[string]string{
		"sandbox_id":  sandboxID,
		"preview_url": previewURL,
		"status":      "created",
	}, http.StatusOK)
}

// ── Destroy ───────────────────────────────────────────────────────────────────
// DELETE /api/projects/:id/sandbox
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

	h.killSandbox(r.Context(), sandboxID)

	h.db.Exec(r.Context(),
		`UPDATE projects SET sandbox_id = NULL, preview_url = NULL, updated_at = NOW() WHERE id = $1`,
		projectID,
	)

	w.WriteHeader(http.StatusNoContent)
}

// ── Sync ──────────────────────────────────────────────────────────────────────
// POST /api/projects/:id/sandbox/sync
// Pushes latest project files into a running sandbox and hot-reloads
func (h *SandboxHandler) Sync(w http.ResponseWriter, r *http.Request) {
	if h.e2bAPIKey == "" {
		writeError(w, "E2B not configured", http.StatusNotImplemented)
		return
	}

	userID := authmw.GetUserID(r)
	projectID := chi.URLParam(r, "id")

	var sandboxID, previewURL string
	if err := h.db.QueryRow(r.Context(),
		`SELECT sandbox_id, COALESCE(preview_url,'') FROM projects WHERE id = $1 AND user_id = $2 AND sandbox_id IS NOT NULL`,
		projectID, userID,
	).Scan(&sandboxID, &previewURL); err != nil {
		writeError(w, "no active sandbox — create one first", http.StatusNotFound)
		return
	}

	// Check sandbox still alive
	if !h.sandboxAlive(r.Context(), sandboxID) {
		h.db.Exec(r.Context(),
			`UPDATE projects SET sandbox_id = NULL, preview_url = NULL WHERE id = $1`, projectID,
		)
		writeError(w, "sandbox expired — please create a new one", http.StatusGone)
		return
	}

	// Push latest files
	projectFiles, _ := h.loadProjectFiles(r.Context(), projectID)
	for _, f := range projectFiles {
		h.writeFile(r.Context(), sandboxID, "project/"+f.path, f.content)
	}

	// Vite HMR picks up changes automatically — no restart needed
	writeJSON(w, map[string]string{
		"status":      "synced",
		"preview_url": previewURL,
		"files":       fmt.Sprintf("%d", len(projectFiles)),
	}, http.StatusOK)
}

// ── Status ────────────────────────────────────────────────────────────────────
// GET /api/projects/:id/sandbox/status
func (h *SandboxHandler) Status(w http.ResponseWriter, r *http.Request) {
	userID := authmw.GetUserID(r)
	projectID := chi.URLParam(r, "id")

	var sandboxID, previewURL *string
	if err := h.db.QueryRow(r.Context(),
		`SELECT sandbox_id, preview_url FROM projects WHERE id = $1 AND user_id = $2`,
		projectID, userID,
	).Scan(&sandboxID, &previewURL); err != nil {
		writeError(w, "project not found", http.StatusNotFound)
		return
	}

	if sandboxID == nil || *sandboxID == "" {
		writeJSON(w, map[string]any{
			"active":      false,
			"sandbox_id":  nil,
			"preview_url": nil,
		}, http.StatusOK)
		return
	}

	alive := h.sandboxAlive(r.Context(), *sandboxID)
	if !alive {
		// Clean up stale record
		h.db.Exec(r.Context(),
			`UPDATE projects SET sandbox_id = NULL, preview_url = NULL WHERE id = $1`, projectID,
		)
		writeJSON(w, map[string]any{
			"active":      false,
			"sandbox_id":  nil,
			"preview_url": nil,
		}, http.StatusOK)
		return
	}

	writeJSON(w, map[string]any{
		"active":      true,
		"sandbox_id":  *sandboxID,
		"preview_url": *previewURL,
	}, http.StatusOK)
}

// ══════════════════════════════════════════════════════════════
// E2B API helpers
// ══════════════════════════════════════════════════════════════

// createSandbox boots a new sandbox and returns its ID
func (h *SandboxHandler) createSandbox(ctx context.Context) (string, error) {
	// E2B uses "base" template for a plain Linux box
	// "Node" template has Node.js pre-installed
	body := map[string]any{
		"templateID": "Node",
		"timeout":    3600,
		"metadata":   map[string]string{"source": "lovable-clone"},
	}
	b, _ := json.Marshal(body)

	req, err := http.NewRequestWithContext(ctx, "POST", e2bAPIBase, bytes.NewReader(b))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", h.e2bAPIKey)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("HTTP error: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("E2B API %d: %s", resp.StatusCode, string(respBody))
	}

	var result e2bCreateResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", fmt.Errorf("parse error: %w (body: %s)", err, string(respBody))
	}
	if result.SandboxID == "" {
		return "", fmt.Errorf("empty sandboxId in response: %s", string(respBody))
	}

	return result.SandboxID, nil
}

// writeFile writes a single file into the sandbox via E2B Files API
func (h *SandboxHandler) writeFile(ctx context.Context, sandboxID, relPath, content string) error {
	// E2B Files API: POST /sandboxes/:id/files
	// body: multipart or JSON depending on SDK version — we use the REST path param approach
	apiURL := fmt.Sprintf("%s/%s/files?path=/home/user/%s", e2bAPIBase, sandboxID, relPath)

	req, err := http.NewRequestWithContext(ctx, "POST", apiURL, strings.NewReader(content))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "text/plain")
	req.Header.Set("X-API-Key", h.e2bAPIKey)

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// runCommand executes a shell command in the sandbox and waits for output
func (h *SandboxHandler) runCommand(ctx context.Context, sandboxID, cmd string, timeoutSec int) (string, error) {
	apiURL := fmt.Sprintf("%s/%s/commands", e2bAPIBase, sandboxID)
	body, _ := json.Marshal(map[string]any{
		"cmd":     cmd,
		"timeout": timeoutSec,
	})

	req, err := http.NewRequestWithContext(ctx, "POST", apiURL, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", h.e2bAPIKey)

	client := &http.Client{Timeout: time.Duration(timeoutSec+10) * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var result struct {
		Stdout   string `json:"stdout"`
		Stderr   string `json:"stderr"`
		ExitCode int    `json:"exitCode"`
	}
	json.NewDecoder(resp.Body).Decode(&result)

	if result.ExitCode != 0 {
		return result.Stdout + result.Stderr, fmt.Errorf("exit code %d", result.ExitCode)
	}
	return result.Stdout, nil
}

// runCommandBackground fires a command without waiting for it to finish
func (h *SandboxHandler) runCommandBackground(ctx context.Context, sandboxID, cmd string) {
	apiURL := fmt.Sprintf("%s/%s/commands", e2bAPIBase, sandboxID)
	body, _ := json.Marshal(map[string]any{
		"cmd":        cmd,
		"background": true,
	})

	req, _ := http.NewRequestWithContext(ctx, "POST", apiURL, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", h.e2bAPIKey)

	resp, err := http.DefaultClient.Do(req)
	if err == nil {
		resp.Body.Close()
	}
}

// sandboxAlive checks if a sandbox is still running via GET /sandboxes/:id
func (h *SandboxHandler) sandboxAlive(ctx context.Context, sandboxID string) bool {
	apiURL := fmt.Sprintf("%s/%s", e2bAPIBase, sandboxID)
	req, err := http.NewRequestWithContext(ctx, "GET", apiURL, nil)
	if err != nil {
		return false
	}
	req.Header.Set("X-API-Key", h.e2bAPIKey)

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// killSandbox terminates a sandbox
func (h *SandboxHandler) killSandbox(ctx context.Context, sandboxID string) {
	apiURL := fmt.Sprintf("%s/%s", e2bAPIBase, sandboxID)
	req, err := http.NewRequestWithContext(ctx, "DELETE", apiURL, nil)
	if err != nil {
		return
	}
	req.Header.Set("X-API-Key", h.e2bAPIKey)
	resp, err := http.DefaultClient.Do(req)
	if err == nil {
		resp.Body.Close()
	}
}

// ── Scaffold ──────────────────────────────────────────────────────────────────

// writeScaffold writes a minimal React+Vite+Tailwind project scaffold
func (h *SandboxHandler) writeScaffold(ctx context.Context, sandboxID string) error {
	files := map[string]string{
		"project/package.json":       scaffoldPackageJSON,
		"project/vite.config.ts":     scaffoldViteConfig,
		"project/index.html":         scaffoldIndexHTML,
		"project/tailwind.config.js": scaffoldTailwindConfig,
		"project/postcss.config.js":  scaffoldPostcssConfig,
		"project/tsconfig.json":      scaffoldTSConfig,
		"project/src/main.tsx":       scaffoldMainTSX,
		"project/src/index.css":      scaffoldIndexCSS,
		"project/src/App.tsx":        scaffoldAppTSX,
	}

	for path, content := range files {
		if err := h.writeFile(ctx, sandboxID, path, content); err != nil {
			return fmt.Errorf("write %s: %w", path, err)
		}
	}
	return nil
}

// loadProjectFiles fetches all files for a project from the database
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

	var result []fileRow
	for rows.Next() {
		var f fileRow
		rows.Scan(&f.path, &f.content)
		result = append(result, f)
	}
	return result, nil
}

// ══════════════════════════════════════════════════════════════
// Scaffold file contents
// ══════════════════════════════════════════════════════════════

const scaffoldPackageJSON = `{
  "name": "lovable-app",
  "private": true,
  "version": "0.0.0",
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
}`

const scaffoldViteConfig = `import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

export default defineConfig({
  plugins: [react()],
  server: {
    host: '0.0.0.0',
    port: 5173,
  },
})`

const scaffoldIndexHTML = `<!doctype html>
<html lang="en">
  <head>
    <meta charset="UTF-8" />
    <meta name="viewport" content="width=device-width, initial-scale=1.0" />
    <title>Lovable App</title>
  </head>
  <body>
    <div id="root"></div>
    <script type="module" src="/src/main.tsx"></script>
  </body>
</html>`

const scaffoldTailwindConfig = `/** @type {import('tailwindcss').Config} */
export default {
  content: ['./index.html', './src/**/*.{js,ts,jsx,tsx}'],
  theme: { extend: {} },
  plugins: [],
}`

const scaffoldPostcssConfig = `export default {
  plugins: {
    tailwindcss: {},
    autoprefixer: {},
  },
}`

const scaffoldTSConfig = `{
  "compilerOptions": {
    "target": "ES2020",
    "useDefineForClassFields": true,
    "lib": ["ES2020", "DOM", "DOM.Iterable"],
    "module": "ESNext",
    "skipLibCheck": true,
    "moduleResolution": "bundler",
    "allowImportingTsExtensions": true,
    "resolveJsonModule": true,
    "isolatedModules": true,
    "noEmit": true,
    "jsx": "react-jsx",
    "strict": true
  },
  "include": ["src"]
}`

const scaffoldMainTSX = `import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import './index.css'
import App from './App.tsx'

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <App />
  </StrictMode>,
)`

const scaffoldIndexCSS = `@tailwind base;
@tailwind components;
@tailwind utilities;`

const scaffoldAppTSX = `export default function App() {
  return (
    <div className="min-h-screen bg-gray-950 text-white flex items-center justify-center">
      <p className="text-white/50">Your app will appear here...</p>
    </div>
  )
}`
