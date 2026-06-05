package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"lovable-backend/internal/ai"
	authmw "lovable-backend/internal/middleware"
)

type ChatHandler struct {
	db       *pgxpool.Pool
	aiClient *ai.Client
}

func NewChatHandler(db *pgxpool.Pool, aiClient *ai.Client) *ChatHandler {
	return &ChatHandler{db: db, aiClient: aiClient}
}

type chatRequest struct {
	Message string `json:"message"`
}

// Stream handles POST /api/projects/:projectId/chat/stream — SSE endpoint.
func (h *ChatHandler) Stream(w http.ResponseWriter, r *http.Request) {
	userID := authmw.GetUserID(r)
	projectID := chi.URLParam(r, "projectId")

	// Verify ownership
	var ownerID string
	if err := h.db.QueryRow(r.Context(),
		`SELECT user_id FROM projects WHERE id = $1`, projectID,
	).Scan(&ownerID); err != nil || ownerID != userID {
		writeError(w, "project not found", http.StatusNotFound)
		return
	}

	var req chatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Message) == "" {
		writeError(w, "message is required", http.StatusBadRequest)
		return
	}

	// Persist user message
	userContent, _ := json.Marshal(map[string]string{"text": req.Message})
	if _, err := h.db.Exec(r.Context(),
		`INSERT INTO messages (project_id, role, content) VALUES ($1, 'user', $2)`,
		projectID, userContent,
	); err != nil {
		writeError(w, "failed to save message", http.StatusInternalServerError)
		return
	}

	// Rebuild conversation history for Claude
	// We store messages as {"text":"..."} for user/assistant
	// and {"tool_results":[...]} for tool responses
	rows, err := h.db.Query(r.Context(),
		`SELECT role, content FROM messages WHERE project_id = $1 ORDER BY created_at ASC`,
		projectID,
	)
	if err != nil {
		writeError(w, "failed to load history", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var history []ai.Message
	for rows.Next() {
		var role string
		var raw json.RawMessage
		if err := rows.Scan(&role, &raw); err != nil {
			continue
		}

		switch role {
		case "user", "assistant":
			var stored struct {
				Text string `json:"text"`
			}
			if err := json.Unmarshal(raw, &stored); err == nil && stored.Text != "" {
				history = append(history, ai.Message{Role: role, Content: stored.Text})
			}
		}
	}

	// Set SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	sendEvent := func(event ai.StreamEvent) {
		data, _ := json.Marshal(event)
		fmt.Fprintf(w, "data: %s\n\n", data)
		flusher.Flush()
	}

	ctx := r.Context()
	var assistantText strings.Builder

	if err := h.aiClient.Stream(ctx, ai.StreamRequest{
		Messages: history,
		OnToken: func(text string) {
			assistantText.WriteString(text)
			sendEvent(ai.StreamEvent{Type: "token", Content: text})
		},
		OnTool: func(id, name string, input json.RawMessage) (string, error) {
			sendEvent(ai.StreamEvent{Type: "tool_start", ToolID: id, ToolName: name, Input: input})
			result, err := h.executeTool(ctx, projectID, name, input)
			if err != nil {
				sendEvent(ai.StreamEvent{Type: "tool_error", ToolID: id, Error: err.Error()})
				return "", err
			}
			sendEvent(ai.StreamEvent{Type: "tool_done", ToolID: id, ToolName: name, Result: result})
			return result, nil
		},
		OnDone: func(_ []ai.Message) {
			// Persist assistant reply
			if text := assistantText.String(); text != "" {
				content, _ := json.Marshal(map[string]string{"text": text})
				h.db.Exec(ctx,
					`INSERT INTO messages (project_id, role, content) VALUES ($1, 'assistant', $2)`,
					projectID, content,
				)
			}
			sendEvent(ai.StreamEvent{Type: "done"})
		},
	}); err != nil {
		sendEvent(ai.StreamEvent{Type: "error", Error: err.Error()})
	}
}

// GetMessages returns chat history (user + assistant only) for a project.
func (h *ChatHandler) GetMessages(w http.ResponseWriter, r *http.Request) {
	userID := authmw.GetUserID(r)
	projectID := chi.URLParam(r, "projectId")

	var ownerID string
	if err := h.db.QueryRow(r.Context(),
		`SELECT user_id FROM projects WHERE id = $1`, projectID,
	).Scan(&ownerID); err != nil || ownerID != userID {
		writeError(w, "project not found", http.StatusNotFound)
		return
	}

	rows, err := h.db.Query(r.Context(),
		`SELECT id, role, content, created_at FROM messages
		 WHERE project_id = $1 AND role IN ('user','assistant')
		 ORDER BY created_at ASC`,
		projectID,
	)
	if err != nil {
		writeError(w, "failed to load messages", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	type msgRow struct {
		ID        string          `json:"id"`
		Role      string          `json:"role"`
		Content   json.RawMessage `json:"content"`
		CreatedAt string          `json:"created_at"`
	}
	msgs := []msgRow{}
	for rows.Next() {
		var m msgRow
		if err := rows.Scan(&m.ID, &m.Role, &m.Content, &m.CreatedAt); err != nil {
			continue
		}
		msgs = append(msgs, m)
	}
	writeJSON(w, msgs, http.StatusOK)
}

// executeTool runs a single tool call against the project's file store.
func (h *ChatHandler) executeTool(ctx context.Context, projectID, toolName string, input json.RawMessage) (string, error) {
	switch toolName {
	case "write_file":
		var args struct {
			Path    string `json:"path"`
			Content string `json:"content"`
		}
		if err := json.Unmarshal(input, &args); err != nil {
			return "", fmt.Errorf("invalid args: %w", err)
		}
		if strings.Contains(args.Path, "..") || strings.HasPrefix(args.Path, "/") {
			return "", fmt.Errorf("invalid path: %s", args.Path)
		}
		if _, err := h.db.Exec(ctx,
			`INSERT INTO project_files (project_id, path, content, updated_at)
			 VALUES ($1, $2, $3, NOW())
			 ON CONFLICT (project_id, path) DO UPDATE SET content = EXCLUDED.content, updated_at = NOW()`,
			projectID, args.Path, args.Content,
		); err != nil {
			return "", fmt.Errorf("write failed: %w", err)
		}
		return fmt.Sprintf("wrote %s (%d bytes)", args.Path, len(args.Content)), nil

	case "read_file":
		var args struct {
			Path string `json:"path"`
		}
		if err := json.Unmarshal(input, &args); err != nil {
			return "", fmt.Errorf("invalid args: %w", err)
		}
		var content string
		if err := h.db.QueryRow(ctx,
			`SELECT content FROM project_files WHERE project_id = $1 AND path = $2`,
			projectID, args.Path,
		).Scan(&content); err != nil {
			return "", fmt.Errorf("file not found: %s", args.Path)
		}
		return content, nil

	case "list_files":
		rows, err := h.db.Query(ctx,
			`SELECT path FROM project_files WHERE project_id = $1 ORDER BY path`,
			projectID,
		)
		if err != nil {
			return "", fmt.Errorf("db error: %w", err)
		}
		defer rows.Close()
		var paths []string
		for rows.Next() {
			var p string
			_ = rows.Scan(&p)
			paths = append(paths, p)
		}
		out, _ := json.Marshal(paths)
		return string(out), nil

	case "delete_file":
		var args struct {
			Path string `json:"path"`
		}
		if err := json.Unmarshal(input, &args); err != nil {
			return "", fmt.Errorf("invalid args: %w", err)
		}
		h.db.Exec(ctx,
			`DELETE FROM project_files WHERE project_id = $1 AND path = $2`,
			projectID, args.Path,
		)
		return "deleted " + args.Path, nil

	case "run_command":
		var args struct {
			Command string `json:"command"`
		}
		if err := json.Unmarshal(input, &args); err != nil {
			return "", fmt.Errorf("invalid args: %w", err)
		}
		// E2B sandbox integration point — for now acknowledge
		return fmt.Sprintf("command noted: %s", args.Command), nil

	default:
		return "", fmt.Errorf("unknown tool: %s", toolName)
	}
}
