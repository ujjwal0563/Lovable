package handlers

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"lovable-backend/internal/email"
	authmw "lovable-backend/internal/middleware"
)

type TeamHandler struct {
	db             *pgxpool.Pool
	mailer         *email.Sender
	frontendOrigin string
}

func NewTeamHandler(db *pgxpool.Pool, mailer *email.Sender, frontendOrigin string) *TeamHandler {
	return &TeamHandler{db: db, mailer: mailer, frontendOrigin: frontendOrigin}
}

// ── List Members ─────────────────────────────────────────────────────────────
// GET /api/projects/:id/members
func (h *TeamHandler) ListMembers(w http.ResponseWriter, r *http.Request) {
	userID := authmw.GetUserID(r)
	projectID := chi.URLParam(r, "id")

	// Must be member or owner to view
	if !h.isMemberOrOwner(r, projectID, userID) {
		writeError(w, "project not found", http.StatusNotFound)
		return
	}

	rows, err := h.db.Query(r.Context(),
		`SELECT pm.user_id, pm.role, pm.joined_at,
		        u.email, u.name
		 FROM project_members pm
		 JOIN users u ON u.id = pm.user_id
		 WHERE pm.project_id = $1
		 ORDER BY pm.joined_at ASC`,
		projectID,
	)
	if err != nil {
		writeError(w, "failed to list members", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	type memberRow struct {
		UserID   string    `json:"user_id"`
		Role     string    `json:"role"`
		JoinedAt time.Time `json:"joined_at"`
		Email    string    `json:"email"`
		Name     string    `json:"name"`
	}

	members := []memberRow{}
	for rows.Next() {
		var m memberRow
		if err := rows.Scan(&m.UserID, &m.Role, &m.JoinedAt, &m.Email, &m.Name); err != nil {
			continue
		}
		members = append(members, m)
	}

	// Also include pending invites
	inviteRows, _ := h.db.Query(r.Context(),
		`SELECT email, role, created_at FROM project_invites
		 WHERE project_id = $1 AND accepted = false AND expires_at > NOW()`,
		projectID,
	)
	defer inviteRows.Close()

	type inviteRow struct {
		Email     string    `json:"email"`
		Role      string    `json:"role"`
		CreatedAt time.Time `json:"created_at"`
		Status    string    `json:"status"`
	}

	invites := []inviteRow{}
	if inviteRows != nil {
		for inviteRows.Next() {
			var inv inviteRow
			inv.Status = "pending"
			inviteRows.Scan(&inv.Email, &inv.Role, &inv.CreatedAt)
			invites = append(invites, inv)
		}
	}

	writeJSON(w, map[string]any{
		"members": members,
		"invites": invites,
	}, http.StatusOK)
}

// ── Invite Member ─────────────────────────────────────────────────────────────
// POST /api/projects/:id/members/invite
func (h *TeamHandler) InviteMember(w http.ResponseWriter, r *http.Request) {
	userID := authmw.GetUserID(r)
	projectID := chi.URLParam(r, "id")

	// Only owner can invite
	if !h.isOwner(r, projectID, userID) {
		writeError(w, "only the project owner can invite members", http.StatusForbidden)
		return
	}

	var req struct {
		Email string `json:"email"`
		Role  string `json:"role"` // "editor" or "viewer"
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Email == "" {
		writeError(w, "email is required", http.StatusBadRequest)
		return
	}
	if req.Role == "" {
		req.Role = "editor"
	}
	if req.Role != "editor" && req.Role != "viewer" {
		writeError(w, "role must be 'editor' or 'viewer'", http.StatusBadRequest)
		return
	}

	// Get inviter info
	var inviterName, projectName string
	h.db.QueryRow(r.Context(),
		`SELECT u.name, p.name FROM users u, projects p
		 WHERE u.id = $1 AND p.id = $2`, userID, projectID,
	).Scan(&inviterName, &projectName)

	// Check if already a member
	var existingCount int
	h.db.QueryRow(r.Context(),
		`SELECT COUNT(*) FROM project_members pm
		 JOIN users u ON u.id = pm.user_id
		 WHERE pm.project_id = $1 AND u.email = $2`,
		projectID, req.Email,
	).Scan(&existingCount)
	if existingCount > 0 {
		writeError(w, "user is already a member", http.StatusConflict)
		return
	}

	// Generate invite token
	b := make([]byte, 16)
	rand.Read(b)
	token := hex.EncodeToString(b)
	expires := time.Now().Add(7 * 24 * time.Hour)

	_, err := h.db.Exec(r.Context(),
		`INSERT INTO project_invites
		   (project_id, email, role, token, expires_at, invited_by)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 ON CONFLICT (project_id, email) DO UPDATE
		   SET token = $4, expires_at = $5, role = $3, accepted = false`,
		projectID, req.Email, req.Role, token, expires, userID,
	)
	if err != nil {
		writeError(w, "failed to create invite", http.StatusInternalServerError)
		return
	}

	acceptURL := h.frontendOrigin + "/invites/" + token

	// Send invite email (async)
	go h.mailer.SendProjectInvite(req.Email, inviterName, projectName, acceptURL)

	writeJSON(w, map[string]any{
		"message":    "Invitation sent to " + req.Email,
		"email":      req.Email,
		"role":       req.Role,
		"invite_url": acceptURL,
		"expires_at": expires,
	}, http.StatusCreated)
}

// ── Accept Invite ─────────────────────────────────────────────────────────────
// POST /api/invites/:token/accept
func (h *TeamHandler) AcceptInvite(w http.ResponseWriter, r *http.Request) {
	userID := authmw.GetUserID(r)
	token := chi.URLParam(r, "token")

	// Get invite details
	var projectID, inviteEmail, role string
	var expiresAt time.Time
	var accepted bool

	err := h.db.QueryRow(r.Context(),
		`SELECT project_id, email, role, expires_at, accepted
		 FROM project_invites WHERE token = $1`,
		token,
	).Scan(&projectID, &inviteEmail, &role, &expiresAt, &accepted)
	if err != nil {
		writeError(w, "invite not found or already used", http.StatusNotFound)
		return
	}
	if accepted {
		writeError(w, "invite already accepted", http.StatusConflict)
		return
	}
	if time.Now().After(expiresAt) {
		writeError(w, "invite has expired", http.StatusGone)
		return
	}

	// Verify the accepting user's email matches
	var userEmail string
	h.db.QueryRow(r.Context(),
		`SELECT email FROM users WHERE id = $1`, userID,
	).Scan(&userEmail)

	if userEmail != inviteEmail {
		writeError(w, "this invite was sent to a different email address", http.StatusForbidden)
		return
	}

	// Add to project_members
	tx, err := h.db.Begin(r.Context())
	if err != nil {
		writeError(w, "transaction failed", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback(r.Context())

	_, err = tx.Exec(r.Context(),
		`INSERT INTO project_members (project_id, user_id, role)
		 VALUES ($1, $2, $3)
		 ON CONFLICT (project_id, user_id) DO UPDATE SET role = $3`,
		projectID, userID, role,
	)
	if err != nil {
		writeError(w, "failed to add member", http.StatusInternalServerError)
		return
	}

	tx.Exec(r.Context(),
		`UPDATE project_invites SET accepted = true WHERE token = $1`, token,
	)

	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, "commit failed", http.StatusInternalServerError)
		return
	}

	writeJSON(w, map[string]any{
		"message":    "Successfully joined the project",
		"project_id": projectID,
		"role":       role,
	}, http.StatusOK)
}

// ── Update Member Role ────────────────────────────────────────────────────────
// PATCH /api/projects/:id/members/:userId
func (h *TeamHandler) UpdateMemberRole(w http.ResponseWriter, r *http.Request) {
	userID := authmw.GetUserID(r)
	projectID := chi.URLParam(r, "id")
	targetUserID := chi.URLParam(r, "userId")

	if !h.isOwner(r, projectID, userID) {
		writeError(w, "only the project owner can change roles", http.StatusForbidden)
		return
	}
	if targetUserID == userID {
		writeError(w, "cannot change your own role", http.StatusBadRequest)
		return
	}

	var req struct {
		Role string `json:"role"` // "editor" or "viewer"
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, "invalid body", http.StatusBadRequest)
		return
	}
	if req.Role != "editor" && req.Role != "viewer" {
		writeError(w, "role must be 'editor' or 'viewer'", http.StatusBadRequest)
		return
	}

	result, err := h.db.Exec(r.Context(),
		`UPDATE project_members SET role = $1
		 WHERE project_id = $2 AND user_id = $3`,
		req.Role, projectID, targetUserID,
	)
	if err != nil || result.RowsAffected() == 0 {
		writeError(w, "member not found", http.StatusNotFound)
		return
	}

	writeJSON(w, map[string]string{
		"message": "Role updated to " + req.Role,
		"role":    req.Role,
	}, http.StatusOK)
}

// ── Remove Member ─────────────────────────────────────────────────────────────
// DELETE /api/projects/:id/members/:userId
func (h *TeamHandler) RemoveMember(w http.ResponseWriter, r *http.Request) {
	userID := authmw.GetUserID(r)
	projectID := chi.URLParam(r, "id")
	targetUserID := chi.URLParam(r, "userId")

	// Owner can remove anyone; members can remove themselves (leave)
	if targetUserID != userID && !h.isOwner(r, projectID, userID) {
		writeError(w, "only the owner can remove other members", http.StatusForbidden)
		return
	}

	// Get removed user email for notification
	var removedEmail, projectName string
	h.db.QueryRow(r.Context(),
		`SELECT u.email, p.name FROM users u, projects p
		 WHERE u.id = $1 AND p.id = $2`, targetUserID, projectID,
	).Scan(&removedEmail, &projectName)

	h.db.Exec(r.Context(),
		`DELETE FROM project_members WHERE project_id = $1 AND user_id = $2`,
		projectID, targetUserID,
	)

	// Notify removed user (only if removed by owner, not self-leave)
	if targetUserID != userID {
		go h.mailer.SendRemovedFromProject(removedEmail, projectName)
	}

	w.WriteHeader(http.StatusNoContent)
}

// ── Leave Project ─────────────────────────────────────────────────────────────
// POST /api/projects/:id/leave
func (h *TeamHandler) LeaveProject(w http.ResponseWriter, r *http.Request) {
	userID := authmw.GetUserID(r)
	projectID := chi.URLParam(r, "id")

	// Owner cannot leave — must delete project instead
	if h.isOwner(r, projectID, userID) {
		writeError(w, "owner cannot leave — delete the project instead", http.StatusBadRequest)
		return
	}

	h.db.Exec(r.Context(),
		`DELETE FROM project_members WHERE project_id = $1 AND user_id = $2`,
		projectID, userID,
	)

	writeJSON(w, map[string]string{"message": "You have left the project"}, http.StatusOK)
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func (h *TeamHandler) isOwner(r *http.Request, projectID, userID string) bool {
	var ownerID string
	h.db.QueryRow(r.Context(),
		`SELECT user_id FROM projects WHERE id = $1`, projectID,
	).Scan(&ownerID)
	return ownerID == userID
}

func (h *TeamHandler) isMemberOrOwner(r *http.Request, projectID, userID string) bool {
	if h.isOwner(r, projectID, userID) {
		return true
	}
	var count int
	h.db.QueryRow(r.Context(),
		`SELECT COUNT(*) FROM project_members WHERE project_id = $1 AND user_id = $2`,
		projectID, userID,
	).Scan(&count)
	return count > 0
}
