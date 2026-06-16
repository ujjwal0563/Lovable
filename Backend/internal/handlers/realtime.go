package handlers

import (
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	authmw "lovable-backend/internal/middleware"
)

// ── Event Types ───────────────────────────────────────────────────────────────

const (
	EventCursorMove  = "cursor_move"
	EventFileOpen    = "file_open"
	EventUserJoined  = "user_joined"
	EventUserLeft    = "user_left"
	EventOnlineUsers = "online_users"
	EventFileSaved   = "file_saved"
	EventChatMessage = "chat_message"
)

// ── Types ──────────────────────────────────────────────────────────────────────

type RealtimeEvent struct {
	Type      string          `json:"type"`
	UserID    string          `json:"user_id"`
	UserName  string          `json:"user_name"`
	ProjectID string          `json:"project_id"`
	Payload   json.RawMessage `json:"payload"`
	Timestamp time.Time       `json:"timestamp"`
}

type CursorPayload struct {
	File   string `json:"file"`
	Line   int    `json:"line"`
	Column int    `json:"column"`
}

type FileOpenPayload struct {
	File string `json:"file"`
}

type OnlineUser struct {
	UserID   string    `json:"user_id"`
	UserName string    `json:"user_name"`
	File     string    `json:"file"`
	Color    string    `json:"color"`
	JoinedAt time.Time `json:"joined_at"`
}

// ── Client (SSE connection) ───────────────────────────────────────────────────

type RealtimeClient struct {
	UserID      string
	UserName    string
	ProjectID   string
	Color       string
	CurrentFile string
	Events      chan RealtimeEvent
	Done        chan struct{}
	JoinedAt    time.Time
}

// ── Room (one per project) ────────────────────────────────────────────────────

type Room struct {
	mu      sync.RWMutex
	clients map[string]*RealtimeClient // userID → client
}

func (room *Room) addClient(c *RealtimeClient) {
	room.mu.Lock()
	defer room.mu.Unlock()
	room.clients[c.UserID] = c
}

func (room *Room) removeClient(userID string) {
	room.mu.Lock()
	defer room.mu.Unlock()
	if c, ok := room.clients[userID]; ok {
		close(c.Done)
		delete(room.clients, userID)
	}
}

func (room *Room) broadcast(evt RealtimeEvent, excludeUserID string) {
	room.mu.RLock()
	defer room.mu.RUnlock()
	for uid, c := range room.clients {
		if uid == excludeUserID {
			continue
		}
		select {
		case c.Events <- evt:
		default:
			// Client too slow — skip
		}
	}
}

func (room *Room) onlineUsers() []OnlineUser {
	room.mu.RLock()
	defer room.mu.RUnlock()
	users := []OnlineUser{}
	for _, c := range room.clients {
		users = append(users, OnlineUser{
			UserID:   c.UserID,
			UserName: c.UserName,
			File:     c.CurrentFile,
			Color:    c.Color,
			JoinedAt: c.JoinedAt,
		})
	}
	return users
}

func (room *Room) count() int {
	room.mu.RLock()
	defer room.mu.RUnlock()
	return len(room.clients)
}

// ── Manager ───────────────────────────────────────────────────────────────────

type RealtimeManager struct {
	mu    sync.RWMutex
	rooms map[string]*Room // projectID → Room
}

var globalManager = &RealtimeManager{
	rooms: make(map[string]*Room),
}

func (m *RealtimeManager) getOrCreateRoom(projectID string) *Room {
	m.mu.Lock()
	defer m.mu.Unlock()
	if room, ok := m.rooms[projectID]; ok {
		return room
	}
	room := &Room{clients: make(map[string]*RealtimeClient)}
	m.rooms[projectID] = room
	return room
}

func (m *RealtimeManager) cleanupRoom(projectID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if room, ok := m.rooms[projectID]; ok {
		if room.count() == 0 {
			delete(m.rooms, projectID)
		}
	}
}

// ── Cursor colors assigned per user ──────────────────────────────────────────

var cursorColors = []string{
	"#7c3aed", "#2563eb", "#059669", "#d97706",
	"#dc2626", "#db2777", "#0891b2", "#65a30d",
}

func colorForUser(userID string) string {
	sum := 0
	for _, c := range userID {
		sum += int(c)
	}
	return cursorColors[sum%len(cursorColors)]
}

// ── Handler ───────────────────────────────────────────────────────────────────

type RealtimeHandler struct {
	db      *pgxpool.Pool
	manager *RealtimeManager
}

func NewRealtimeHandler(db *pgxpool.Pool) *RealtimeHandler {
	return &RealtimeHandler{db: db, manager: globalManager}
}

// Connect handles GET /api/projects/:id/realtime
// Uses Server-Sent Events (SSE) — same as chat stream
func (h *RealtimeHandler) Connect(w http.ResponseWriter, r *http.Request) {
	userID := authmw.GetUserID(r)
	projectID := chi.URLParam(r, "id")

	// Verify user can access this project (owner or member)
	var ownerID string
	h.db.QueryRow(r.Context(),
		`SELECT user_id FROM projects WHERE id = $1`, projectID,
	).Scan(&ownerID)

	var memberCount int
	h.db.QueryRow(r.Context(),
		`SELECT COUNT(*) FROM project_members WHERE project_id = $1 AND user_id = $2`,
		projectID, userID,
	).Scan(&memberCount)

	if ownerID != userID && memberCount == 0 {
		writeError(w, "project not found", http.StatusNotFound)
		return
	}

	// Get user info
	var userName string
	h.db.QueryRow(r.Context(),
		`SELECT name FROM users WHERE id = $1`, userID,
	).Scan(&userName)

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

	// Create client
	client := &RealtimeClient{
		UserID:    userID,
		UserName:  userName,
		ProjectID: projectID,
		Color:     colorForUser(userID),
		Events:    make(chan RealtimeEvent, 32),
		Done:      make(chan struct{}),
		JoinedAt:  time.Now(),
	}

	room := h.manager.getOrCreateRoom(projectID)
	room.addClient(client)

	// Announce join to others
	joinPayload, _ := json.Marshal(map[string]string{
		"user_id":   userID,
		"user_name": userName,
		"color":     client.Color,
	})
	room.broadcast(RealtimeEvent{
		Type:      EventUserJoined,
		UserID:    userID,
		UserName:  userName,
		ProjectID: projectID,
		Payload:   joinPayload,
		Timestamp: time.Now(),
	}, userID)

	// Send current online users list to the newcomer
	onlinePayload, _ := json.Marshal(room.onlineUsers())
	sendSSE(w, flusher, RealtimeEvent{
		Type:      EventOnlineUsers,
		ProjectID: projectID,
		Payload:   onlinePayload,
		Timestamp: time.Now(),
	})

	log.Printf("[realtime] user %s joined project %s (%d online)",
		userName, projectID, room.count())

	// Keep connection open — send events
	defer func() {
		room.removeClient(userID)

		// Announce departure to remaining users
		leavePayload, _ := json.Marshal(map[string]string{
			"user_id":   userID,
			"user_name": userName,
		})
		room.broadcast(RealtimeEvent{
			Type:      EventUserLeft,
			UserID:    userID,
			UserName:  userName,
			ProjectID: projectID,
			Payload:   leavePayload,
			Timestamp: time.Now(),
		}, "")

		h.manager.cleanupRoom(projectID)
		log.Printf("[realtime] user %s left project %s (%d online)",
			userName, projectID, room.count())
	}()

	// Heartbeat ticker
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case evt := <-client.Events:
			sendSSE(w, flusher, evt)

		case <-ticker.C:
			// Send heartbeat to keep connection alive
			sendSSE(w, flusher, RealtimeEvent{
				Type:      "ping",
				Timestamp: time.Now(),
			})

		case <-r.Context().Done():
			return

		case <-client.Done:
			return
		}
	}
}

// Emit handles POST /api/projects/:id/realtime/emit
// Client sends cursor moves, file opens, etc.
func (h *RealtimeHandler) Emit(w http.ResponseWriter, r *http.Request) {
	userID := authmw.GetUserID(r)
	projectID := chi.URLParam(r, "id")

	var req struct {
		Type    string          `json:"type"`
		Payload json.RawMessage `json:"payload"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, "invalid body", http.StatusBadRequest)
		return
	}

	// Validate event type
	allowed := map[string]bool{
		EventCursorMove: true,
		EventFileOpen:   true,
		EventFileSaved:  true,
	}
	if !allowed[req.Type] {
		writeError(w, "invalid event type — allowed: cursor_move, file_open, file_saved", http.StatusBadRequest)
		return
	}

	var userName string
	h.db.QueryRow(r.Context(),
		`SELECT name FROM users WHERE id = $1`, userID,
	).Scan(&userName)

	room := h.manager.getOrCreateRoom(projectID)

	// Update client's current file if file_open event
	if req.Type == EventFileOpen {
		room.mu.Lock()
		if c, ok := room.clients[userID]; ok {
			var fp FileOpenPayload
			if json.Unmarshal(req.Payload, &fp) == nil {
				c.CurrentFile = fp.File
			}
		}
		room.mu.Unlock()
	}

	// Broadcast to all other users in this project
	evt := RealtimeEvent{
		Type:      req.Type,
		UserID:    userID,
		UserName:  userName,
		ProjectID: projectID,
		Payload:   req.Payload,
		Timestamp: time.Now(),
	}
	room.broadcast(evt, userID)

	writeJSON(w, map[string]any{
		"broadcast_to": room.count() - 1,
		"type":         req.Type,
	}, http.StatusOK)
}

// GetOnlineUsers handles GET /api/projects/:id/realtime/online
// Returns list of currently connected users
func (h *RealtimeHandler) GetOnlineUsers(w http.ResponseWriter, r *http.Request) {
	userID := authmw.GetUserID(r)
	projectID := chi.URLParam(r, "id")

	// Verify access
	var ownerID string
	h.db.QueryRow(r.Context(),
		`SELECT user_id FROM projects WHERE id = $1`, projectID,
	).Scan(&ownerID)

	var memberCount int
	h.db.QueryRow(r.Context(),
		`SELECT COUNT(*) FROM project_members WHERE project_id = $1 AND user_id = $2`,
		projectID, userID,
	).Scan(&memberCount)

	if ownerID != userID && memberCount == 0 {
		writeError(w, "project not found", http.StatusNotFound)
		return
	}

	room := h.manager.getOrCreateRoom(projectID)
	writeJSON(w, map[string]any{
		"online":      room.onlineUsers(),
		"total_count": room.count(),
	}, http.StatusOK)
}

// ── SSE helper ────────────────────────────────────────────────────────────────

func sendSSE(w http.ResponseWriter, f http.Flusher, evt RealtimeEvent) {
	data, _ := json.Marshal(evt)
	w.Write([]byte("data: "))
	w.Write(data)
	w.Write([]byte("\n\n"))
	f.Flush()
}
