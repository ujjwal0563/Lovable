package ai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const anthropicAPIURL = "https://api.anthropic.com/v1/messages"
const defaultModel = "claude-sonnet-4-5"
const maxTokens = 8096

// Tool definitions sent to Claude
var Tools = []map[string]any{
	{
		"name":        "write_file",
		"description": "Create or overwrite a file in the project. Use this to generate application code.",
		"input_schema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":    map[string]any{"type": "string", "description": "File path relative to project root (e.g. src/App.tsx)"},
				"content": map[string]any{"type": "string", "description": "Full file content"},
			},
			"required": []string{"path", "content"},
		},
	},
	{
		"name":        "read_file",
		"description": "Read the content of an existing project file.",
		"input_schema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{"type": "string"},
			},
			"required": []string{"path"},
		},
	},
	{
		"name":        "list_files",
		"description": "List all files in the project.",
		"input_schema": map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
	},
	{
		"name":        "delete_file",
		"description": "Delete a file from the project.",
		"input_schema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{"type": "string"},
			},
			"required": []string{"path"},
		},
	},
	{
		"name":        "run_command",
		"description": "Run a shell command in the sandbox (e.g. npm install, bun add tailwindcss).",
		"input_schema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command": map[string]any{"type": "string"},
			},
			"required": []string{"command"},
		},
	},
}

// SystemPrompt is the master instruction for Claude.
const SystemPrompt = `You are an expert full-stack engineer inside an AI app builder (like Lovable.dev). Your job is to build complete, working web applications from user prompts.

The project is a React + Vite + TypeScript + Tailwind CSS application. Files you write are stored and shown to the user in a code editor.

## Your workflow:
1. Read the user's request carefully.
2. Plan which files to create or modify.
3. Use write_file to create every needed file with FULL content.
4. Use run_command only for installing new npm packages.
5. Always write complete file contents — never partial snippets or "rest of file unchanged" comments.

## Conventions:
- Use Tailwind CSS for ALL styling. Never write plain CSS files.
- Use TypeScript (.tsx / .ts) for all React files.
- Use lucide-react for icons (it's pre-installed).
- Keep components modular: one component per file in src/components/.
- src/App.tsx is the main entry point — always export a default App component.
- Do NOT modify vite.config.ts, index.html, or src/main.tsx.

## Output rules:
- Be brief in text replies. Code should do the talking.
- After writing files, give a 1-2 sentence summary of what you built.
- Do not ask clarifying questions unless the request is truly ambiguous.`

// Message is an Anthropic API message.
type Message struct {
	Role    string `json:"role"`
	Content any    `json:"content"` // string or []ContentBlock
}

// ContentBlock is one block inside a message (text, tool_use, image, or tool_result).
type ContentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   string          `json:"content,omitempty"`
	// Image fields
	MediaType string       `json:"media_type,omitempty"`
	Data      string       `json:"data,omitempty"`
	Source    *ImageSource `json:"source,omitempty"`
}

// ImageSource is used for Claude vision API
type ImageSource struct {
	Type      string `json:"type"`       // "base64"
	MediaType string `json:"media_type"` // "image/png"
	Data      string `json:"data"`       // base64 string
}

// StreamEvent is the SSE payload sent to the browser.
type StreamEvent struct {
	Type     string          `json:"type"`
	Content  string          `json:"content,omitempty"`
	ToolID   string          `json:"tool_id,omitempty"`
	ToolName string          `json:"tool_name,omitempty"`
	Input    json.RawMessage `json:"input,omitempty"`
	Result   string          `json:"result,omitempty"`
	Error    string          `json:"error,omitempty"`
}

// Client wraps the Anthropic API.
type Client struct {
	apiKey string
	http   *http.Client
}

func NewClient(apiKey string) *Client {
	return &Client{apiKey: apiKey, http: &http.Client{}}
}

// StreamRequest is the input to the agentic loop.
type StreamRequest struct {
	Messages []Message
	Images   []string // base64 data URLs to attach to the first user message
	OnToken  func(text string)
	OnTool   func(id, name string, input json.RawMessage) (string, error)
	OnDone   func(finalMessages []Message)
}

// Stream runs the full agentic tool-use loop until the model stops using tools.
func (c *Client) Stream(ctx context.Context, req StreamRequest) error {
	messages := make([]Message, len(req.Messages))
	copy(messages, req.Messages)

	// If images provided, attach them to the last user message as vision blocks
	if len(req.Images) > 0 && len(messages) > 0 {
		last := &messages[len(messages)-1]
		if last.Role == "user" {
			// Build multi-part content: text + images
			var blocks []ContentBlock
			// Text block
			if text, ok := last.Content.(string); ok && text != "" {
				blocks = append(blocks, ContentBlock{Type: "text", Text: text})
			}
			// Image blocks
			for _, dataURL := range req.Images {
				// Parse data:image/png;base64,xxxxx
				if !strings.HasPrefix(dataURL, "data:") {
					continue
				}
				parts := strings.SplitN(dataURL, ",", 2)
				if len(parts) != 2 {
					continue
				}
				meta := strings.TrimPrefix(parts[0], "data:")
				meta = strings.TrimSuffix(meta, ";base64")
				blocks = append(blocks, ContentBlock{
					Type: "image",
					Source: &ImageSource{
						Type:      "base64",
						MediaType: meta,
						Data:      parts[1],
					},
				})
			}
			last.Content = blocks
		}
	}

	for {
		resp, err := c.callAPI(ctx, messages)
		if err != nil {
			return err
		}

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			return fmt.Errorf("anthropic error %d: %s", resp.StatusCode, string(body))
		}

		assistantBlocks, toolCalls, err := c.parseStream(resp.Body, req)
		resp.Body.Close()
		if err != nil {
			return err
		}

		// Add assistant turn to history
		if len(assistantBlocks) > 0 {
			messages = append(messages, Message{Role: "assistant", Content: assistantBlocks})
		}

		// No tool calls → model is done
		if len(toolCalls) == 0 {
			if req.OnDone != nil {
				req.OnDone(messages)
			}
			return nil
		}

		// Execute all tool calls and collect results
		var toolResults []ContentBlock
		for i := range toolCalls {
			tc := &toolCalls[i]
			result := "ok"
			if req.OnTool != nil {
				r, err := req.OnTool(tc.ID, tc.Name, tc.Input)
				if err != nil {
					result = "error: " + err.Error()
				} else {
					result = r
				}
			}
			toolResults = append(toolResults, ContentBlock{
				Type:      "tool_result",
				ToolUseID: tc.ID,
				Content:   result,
			})
		}

		// Feed results back and loop
		messages = append(messages, Message{Role: "user", Content: toolResults})
	}
}

// parseStream reads the SSE body and fires callbacks for tokens and tool calls.
func (c *Client) parseStream(body io.Reader, req StreamRequest) ([]ContentBlock, []ContentBlock, error) {
	var assistantBlocks []ContentBlock
	var toolCalls []ContentBlock
	toolInputBuilders := map[int]*strings.Builder{} // index → accumulated JSON
	currentTextBuilder := &strings.Builder{}
	currentBlockIndex := -1
	currentBlockType := ""

	scanner := bufio.NewScanner(body)
	// Increase buffer for large tool inputs
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "" {
			continue
		}

		var event map[string]json.RawMessage
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			continue
		}

		var eventType string
		if err := json.Unmarshal(event["type"], &eventType); err != nil {
			continue
		}

		switch eventType {
		case "content_block_start":
			currentBlockIndex++
			var blockWrapper struct {
				Index        int             `json:"index"`
				ContentBlock json.RawMessage `json:"content_block"`
			}
			if err := json.Unmarshal([]byte(data), &blockWrapper); err != nil {
				continue
			}
			var block map[string]string
			if err := json.Unmarshal(blockWrapper.ContentBlock, &block); err != nil {
				continue
			}
			currentBlockType = block["type"]
			if currentBlockType == "tool_use" {
				toolCalls = append(toolCalls, ContentBlock{
					Type: "tool_use",
					ID:   block["id"],
					Name: block["name"],
				})
				toolInputBuilders[len(toolCalls)-1] = &strings.Builder{}
			}

		case "content_block_delta":
			var deltaWrapper struct {
				Index int `json:"index"`
				Delta struct {
					Type        string `json:"type"`
					Text        string `json:"text"`
					PartialJSON string `json:"partial_json"`
				} `json:"delta"`
			}
			if err := json.Unmarshal([]byte(data), &deltaWrapper); err != nil {
				continue
			}
			switch deltaWrapper.Delta.Type {
			case "text_delta":
				text := deltaWrapper.Delta.Text
				currentTextBuilder.WriteString(text)
				if req.OnToken != nil {
					req.OnToken(text)
				}
			case "input_json_delta":
				// Find the tool call by matching block index
				toolIdx := len(toolCalls) - 1
				if b, ok := toolInputBuilders[toolIdx]; ok {
					b.WriteString(deltaWrapper.Delta.PartialJSON)
				}
			}

		case "content_block_stop":
			_ = currentBlockIndex
			if currentBlockType == "text" && currentTextBuilder.Len() > 0 {
				assistantBlocks = append(assistantBlocks, ContentBlock{
					Type: "text",
					Text: currentTextBuilder.String(),
				})
				currentTextBuilder.Reset()
			}
			currentBlockType = ""

		case "message_delta":
			// Contains stop_reason — we handle via tool call count check

		case "message_stop":
			// End of message — finalize tool input JSON
			for idx, b := range toolInputBuilders {
				if idx < len(toolCalls) {
					raw := json.RawMessage(b.String())
					if len(raw) == 0 {
						raw = json.RawMessage(`{}`)
					}
					toolCalls[idx].Input = raw
				}
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return assistantBlocks, toolCalls, err
	}

	// Flush any remaining text
	if currentTextBuilder.Len() > 0 {
		assistantBlocks = append(assistantBlocks, ContentBlock{
			Type: "text",
			Text: currentTextBuilder.String(),
		})
	}

	// Merge tool calls into assistant blocks
	assistantBlocks = append(assistantBlocks, toolCalls...)

	return assistantBlocks, toolCalls, nil
}

func (c *Client) callAPI(ctx context.Context, messages []Message) (*http.Response, error) {
	body := map[string]any{
		"model":      defaultModel,
		"max_tokens": maxTokens,
		"system":     SystemPrompt,
		"messages":   messages,
		"tools":      Tools,
		"stream":     true,
	}

	b, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", anthropicAPIURL, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", c.apiKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")

	return c.http.Do(httpReq)
}
