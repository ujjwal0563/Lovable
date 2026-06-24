package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const groqAPIURL = "https://api.groq.com/openai/v1/chat/completions"

// GroqSystemPrompt — lean prompt, write all files in ONE API call
const GroqSystemPrompt = `You are an AI app builder. Build React + TypeScript + Tailwind CSS apps.
IMPORTANT: Call ALL write_file tools you need in a SINGLE response. Do not wait between tool calls.
Rules:
- Write COMPLETE file content every time
- Use Tailwind CSS for ALL styling
- Default export from src/App.tsx
- Use lucide-react for icons
- After writing files give ONE short sentence only`

// groqModels — active models ordered by rate limit (highest first)
var groqModels = []string{
	"llama-3.1-8b-instant",
	"llama-3.3-70b-versatile",
	"llama3-groq-70b-8192-tool-use-preview",
	"llama3-groq-8b-8192-tool-use-preview",
}

// ─── Types ────────────────────────────────────────────────────────────────────

type GroqClient struct {
	apiKey string
	http   *http.Client
}

func NewGroqClient(apiKey string) *GroqClient {
	return &GroqClient{apiKey: apiKey, http: &http.Client{}}
}

type groqMsg struct {
	Role       string         `json:"role"`
	Content    string         `json:"content,omitempty"`
	ToolCalls  []groqToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
	Name       string         `json:"name,omitempty"`
}

type groqToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function groqFuncCall `json:"function"`
}

type groqFuncCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type groqTool struct {
	Type     string           `json:"type"`
	Function groqToolFunction `json:"function"`
}

type groqToolFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

type groqResponse struct {
	Choices []groqChoice `json:"choices"`
	Error   *groqError   `json:"error"`
}

type groqChoice struct {
	Message      groqMsg `json:"message"`
	FinishReason string  `json:"finish_reason"`
}

type groqError struct {
	Message string `json:"message"`
}

// ─── Tools ────────────────────────────────────────────────────────────────────

var groqTools = []groqTool{
	{
		Type: "function",
		Function: groqToolFunction{
			Name:        "write_file",
			Description: "Write a complete file. Call multiple times in one response for multiple files.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path":    map[string]any{"type": "string", "description": "File path e.g. src/App.tsx"},
					"content": map[string]any{"type": "string", "description": "Complete file content"},
				},
				"required": []string{"path", "content"},
			},
		},
	},
	{
		Type: "function",
		Function: groqToolFunction{
			Name:        "read_file",
			Description: "Read an existing project file.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{"type": "string"},
				},
				"required": []string{"path"},
			},
		},
	},
	{
		Type: "function",
		Function: groqToolFunction{
			Name:        "list_files",
			Description: "List all project files.",
			Parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
		},
	},
	{
		Type: "function",
		Function: groqToolFunction{
			Name:        "delete_file",
			Description: "Delete a project file.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{"type": "string"},
				},
				"required": []string{"path"},
			},
		},
	},
}

// ─── Stream ───────────────────────────────────────────────────────────────────
// Single-shot: ONE API call → execute all tools → done
// This avoids the second API call that causes rate limit errors

func (c *GroqClient) Stream(ctx context.Context, req StreamRequest) error {
	// Build message history
	msgs := []groqMsg{{Role: "system", Content: GroqSystemPrompt}}
	for _, m := range req.Messages {
		switch v := m.Content.(type) {
		case string:
			if v != "" {
				msgs = append(msgs, groqMsg{Role: m.Role, Content: v})
			}
		case []ContentBlock:
			var sb strings.Builder
			for _, b := range v {
				if b.Type == "text" {
					sb.WriteString(b.Text)
				}
			}
			if sb.Len() > 0 {
				msgs = append(msgs, groqMsg{Role: m.Role, Content: sb.String()})
			}
		}
	}

	// ONE API call only
	resp, err := c.callWithFallback(ctx, msgs)
	if err != nil {
		return err
	}
	if resp.Error != nil {
		return fmt.Errorf("groq: %s", resp.Error.Message)
	}
	if len(resp.Choices) == 0 {
		if req.OnDone != nil {
			req.OnDone(req.Messages)
		}
		return nil
	}

	msg := resp.Choices[0].Message

	// Execute ALL tool calls from this single response
	filesWritten := 0
	for _, tc := range msg.ToolCalls {
		input := json.RawMessage(tc.Function.Arguments)
		if req.OnTool != nil {
			_, err := req.OnTool(tc.ID, tc.Function.Name, input)
			if err == nil && tc.Function.Name == "write_file" {
				filesWritten++
			}
		}
	}

	// Build summary text
	var summaryText string
	if msg.Content != "" {
		summaryText = msg.Content
	} else if filesWritten == 1 {
		summaryText = "Done! File updated successfully."
	} else if filesWritten > 1 {
		summaryText = fmt.Sprintf("Done! %d files written successfully.", filesWritten)
	} else {
		summaryText = "Done!"
	}

	// Stream summary tokens
	if req.OnToken != nil {
		words := strings.Fields(summaryText)
		for i, w := range words {
			if i < len(words)-1 {
				req.OnToken(w + " ")
			} else {
				req.OnToken(w)
			}
		}
	}

	// Call OnDone AFTER tokens are streamed
	// This ensures assistantText in chat.go is fully populated before saving
	if req.OnDone != nil {
		req.OnDone(req.Messages)
	}
	return nil
}

// ─── API call with fallback ───────────────────────────────────────────────────

func (c *GroqClient) callWithFallback(ctx context.Context, messages []groqMsg) (*groqResponse, error) {
	var lastErr error
	for _, model := range groqModels {
		result, err := c.callModel(ctx, messages, model)
		if err == nil {
			return result, nil
		}
		errStr := err.Error()
		// Skip deprecated or rate-limited models
		if strings.Contains(errStr, "429") ||
			strings.Contains(errStr, "rate_limit") ||
			strings.Contains(errStr, "Rate limit") ||
			strings.Contains(errStr, "decommissioned") ||
			strings.Contains(errStr, "deprecated") ||
			strings.Contains(errStr, "not supported") {
			lastErr = err
			continue
		}
		return nil, err
	}
	return nil, fmt.Errorf("all Groq models unavailable: %v", lastErr)
}

func (c *GroqClient) callModel(ctx context.Context, messages []groqMsg, model string) (*groqResponse, error) {
	payload := map[string]any{
		"model":       model,
		"messages":    messages,
		"tools":       groqTools,
		"tool_choice": "auto",
		"max_tokens":  4096,
		"temperature": 0.1,
		"stream":      false,
	}

	b, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", groqAPIURL, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)

	httpResp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("HTTP: %w", err)
	}
	defer httpResp.Body.Close()

	respBody, _ := io.ReadAll(httpResp.Body)
	if httpResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("groq error %d: %s", httpResp.StatusCode, string(respBody))
	}

	var result groqResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	return &result, nil
}
