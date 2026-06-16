package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"lovable-backend/internal/email"
	authmw "lovable-backend/internal/middleware"
)

type NotificationsHandler struct {
	db     *pgxpool.Pool
	mailer *email.Sender
}

func NewNotificationsHandler(db *pgxpool.Pool, mailer *email.Sender) *NotificationsHandler {
	return &NotificationsHandler{db: db, mailer: mailer}
}

// GetPreferences GET /api/notifications/preferences
func (h *NotificationsHandler) GetPreferences(w http.ResponseWriter, r *http.Request) {
	userID := authmw.GetUserID(r)

	var welcomeEmail, buildComplete, sandboxExpired, weeklyDigest, teamInvites bool

	err := h.db.QueryRow(r.Context(),
		`SELECT welcome_email, build_complete, sandbox_expired, weekly_digest, team_invites
		 FROM notification_preferences WHERE user_id = $1`,
		userID,
	).Scan(&welcomeEmail, &buildComplete, &sandboxExpired, &weeklyDigest, &teamInvites)

	// If no row yet — return defaults (all enabled)
	if err != nil {
		writeJSON(w, map[string]bool{
			"welcome_email":   true,
			"build_complete":  true,
			"sandbox_expired": true,
			"weekly_digest":   true,
			"team_invites":    true,
		}, http.StatusOK)
		return
	}

	writeJSON(w, map[string]bool{
		"welcome_email":   welcomeEmail,
		"build_complete":  buildComplete,
		"sandbox_expired": sandboxExpired,
		"weekly_digest":   weeklyDigest,
		"team_invites":    teamInvites,
	}, http.StatusOK)
}

// UpdatePreferences PATCH /api/notifications/preferences
func (h *NotificationsHandler) UpdatePreferences(w http.ResponseWriter, r *http.Request) {
	userID := authmw.GetUserID(r)

	var req struct {
		WelcomeEmail   *bool `json:"welcome_email"`
		BuildComplete  *bool `json:"build_complete"`
		SandboxExpired *bool `json:"sandbox_expired"`
		WeeklyDigest   *bool `json:"weekly_digest"`
		TeamInvites    *bool `json:"team_invites"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, "invalid request body", http.StatusBadRequest)
		return
	}

	// Upsert preferences
	_, err := h.db.Exec(r.Context(),
		`INSERT INTO notification_preferences
		   (user_id, welcome_email, build_complete, sandbox_expired, weekly_digest, team_invites)
		 VALUES ($1,
		   COALESCE($2, true),
		   COALESCE($3, true),
		   COALESCE($4, true),
		   COALESCE($5, true),
		   COALESCE($6, true))
		 ON CONFLICT (user_id) DO UPDATE SET
		   welcome_email   = COALESCE($2, notification_preferences.welcome_email),
		   build_complete  = COALESCE($3, notification_preferences.build_complete),
		   sandbox_expired = COALESCE($4, notification_preferences.sandbox_expired),
		   weekly_digest   = COALESCE($5, notification_preferences.weekly_digest),
		   team_invites    = COALESCE($6, notification_preferences.team_invites)`,
		userID,
		req.WelcomeEmail, req.BuildComplete, req.SandboxExpired,
		req.WeeklyDigest, req.TeamInvites,
	)
	if err != nil {
		writeError(w, "failed to update preferences", http.StatusInternalServerError)
		return
	}

	h.GetPreferences(w, r)
}

// SendTestEmail POST /api/notifications/test
// Lets you test if email is working
func (h *NotificationsHandler) SendTestEmail(w http.ResponseWriter, r *http.Request) {
	userID := authmw.GetUserID(r)

	var userEmail, userName string
	h.db.QueryRow(r.Context(),
		`SELECT email, name FROM users WHERE id = $1`, userID,
	).Scan(&userEmail, &userName)

	var req struct {
		Type string `json:"type"` // welcome | build_complete | sandbox_expired | weekly_digest
	}
	json.NewDecoder(r.Body).Decode(&req)

	var err error
	switch req.Type {
	case "welcome":
		err = h.mailer.SendWelcome(userEmail, userName)
	case "build_complete":
		err = h.mailer.SendBuildComplete(userEmail, "My Test App", "AI built 5 files including App.tsx and components.")
	case "sandbox_expired":
		err = h.mailer.SendSandboxExpired(userEmail, "My Test App")
	case "weekly_digest":
		err = h.mailer.SendWeeklySummary(userEmail, userName, 3, 47)
	default:
		err = h.mailer.SendWelcome(userEmail, userName)
	}

	if err != nil {
		writeError(w, "failed to send email: "+err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, map[string]string{
		"message": "Email sent to " + userEmail,
		"type":    req.Type,
	}, http.StatusOK)
}
