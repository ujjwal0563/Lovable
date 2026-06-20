package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"

	"lovable-backend/internal/ai"
	"lovable-backend/internal/config"
	"lovable-backend/internal/db"
	"lovable-backend/internal/email"
	"lovable-backend/internal/handlers"
	authmw "lovable-backend/internal/middleware"
)

func main() {
	cfg := config.Load()

	// ── Database ──────────────────────────────────────────
	pool, err := db.NewPool(cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("Cannot connect to PostgreSQL: %v\n\nRun: docker compose up -d\n", err)
	}
	if err := db.RunMigrations(pool); err != nil {
		log.Fatalf("Migration failed: %v", err)
	}
	log.Println("✓ PostgreSQL connected and migrated")
	defer pool.Close()

	// ── Email sender ──────────────────────────────────────
	mailer := email.NewSender(cfg.SMTPHost, cfg.SMTPPort, cfg.SMTPUser, cfg.SMTPPassword, cfg.SMTPFrom)
	if cfg.SMTPUser == "" {
		log.Println("⚠  SMTP not configured — password reset emails will be printed to stdout")
	}

	// ── AI client ─────────────────────────────────────────
	var aiClient *ai.Client
	var groqClient *ai.GroqClient

	if cfg.AIProvider == "groq" && cfg.GroqAPIKey != "" {
		groqClient = ai.NewGroqClient(cfg.GroqAPIKey)
		log.Println("✓ Groq AI ready (FREE — llama-3.3-70b)")
	} else if cfg.AnthropicKey != "" {
		aiClient = ai.NewClient(cfg.AnthropicKey)
		log.Println("✓ Anthropic Claude ready")
	} else {
		log.Println("⚠  No AI provider configured — set GROQ_API_KEY or ANTHROPIC_API_KEY")
	}

	// ── Handlers ──────────────────────────────────────────
	authH := handlers.NewAuthHandler(pool, handlers.AuthConfig{
		JWTSecret:      cfg.JWTSecret,
		FrontendOrigin: cfg.FrontendOrigin,
		Email:          mailer,
		GoogleClientID: cfg.GoogleClientID,
		GoogleSecret:   cfg.GoogleClientSecret,
		GitHubClientID: cfg.GitHubClientID,
		GitHubSecret:   cfg.GitHubClientSecret,
	})
	projH := handlers.NewProjectsHandler(pool)
	filesH := handlers.NewFilesHandler(pool)
	chatH := handlers.NewChatHandler(pool, aiClient, groqClient)
	versionsH := handlers.NewVersionsHandler(pool)
	dupH := handlers.NewDuplicateHandler(pool)
	exportH := handlers.NewExportHandler(pool)
	sandboxH := handlers.NewSandboxHandler(pool, cfg.E2BApiKey)
	uploadH := handlers.NewUploadHandler(pool)
	templatesH := handlers.NewTemplatesHandler(pool)
	usageH := handlers.NewUsageHandler(pool)
	searchH := handlers.NewSearchHandler(pool)
	shareH := handlers.NewShareHandler(pool)
	profileH := handlers.NewProfileHandler(pool)
	notifH := handlers.NewNotificationsHandler(pool, mailer)
	teamH := handlers.NewTeamHandler(pool, mailer, cfg.FrontendOrigin)
	realtimeH := handlers.NewRealtimeHandler(pool)

	// ── Rate limiters ─────────────────────────────────────
	chatLimiter := authmw.NewRateLimiter(20)
	apiLimiter := authmw.NewRateLimiter(120)

	// ── Router ────────────────────────────────────────────
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Compress(5))
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   []string{"http://localhost:5173", "http://localhost:3000", cfg.FrontendOrigin},
		AllowedMethods:   []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type", "X-Request-ID"},
		ExposedHeaders:   []string{"Content-Disposition"},
		AllowCredentials: true,
		MaxAge:           300,
	}))

	// ── Public ────────────────────────────────────────────
	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok"}`))
	})

	// Debug endpoint — check messages for any project directly
	r.Get("/debug/messages/{projectId}", func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "projectId")
		rows, err := pool.Query(r.Context(),
			`SELECT id, role, content->>'text' as text, created_at 
			 FROM messages WHERE project_id = $1 
			 ORDER BY created_at ASC`, projectID)
		if err != nil {
			w.Write([]byte(`{"error":"` + err.Error() + `"}`))
			return
		}
		defer rows.Close()
		type row struct {
			ID        string `json:"id"`
			Role      string `json:"role"`
			Text      string `json:"text"`
			CreatedAt string `json:"created_at"`
		}
		var result []row
		for rows.Next() {
			var r row
			rows.Scan(&r.ID, &r.Role, &r.Text, &r.CreatedAt)
			result = append(result, r)
		}
		w.Header().Set("Content-Type", "application/json")
		data, _ := json.Marshal(map[string]any{
			"project_id":    projectID,
			"message_count": len(result),
			"messages":      result,
		})
		w.Write(data)
	})

	// Auth (public)
	r.Post("/api/auth/signup", authH.Signup)
	r.Post("/api/auth/login", authH.Login)
	r.Post("/api/auth/forgot-password", authH.ForgotPassword)
	r.Post("/api/auth/reset-password", authH.ResetPassword)

	// OAuth redirects (browser navigates here)
	r.Get("/api/auth/github", authH.GitHubLogin)
	r.Get("/api/auth/github/callback", authH.GitHubCallback)
	r.Get("/api/auth/google", authH.GoogleLogin)
	r.Get("/api/auth/google/callback", authH.GoogleCallback)

	// ── Protected ─────────────────────────────────────────
	r.Group(func(r chi.Router) {
		r.Use(authmw.RequireAuth(cfg.JWTSecret))
		r.Use(apiLimiter.Middleware())

		r.Get("/api/auth/me", authH.Me)

		// Projects
		r.Get("/api/projects", projH.List)
		r.Post("/api/projects", projH.Create)
		r.Get("/api/projects/{id}", projH.Get)
		r.Patch("/api/projects/{id}", projH.Update)
		r.Delete("/api/projects/{id}", projH.Delete)
		r.Post("/api/projects/{id}/duplicate", dupH.Duplicate)

		// Export (zip download)
		r.Get("/api/projects/{id}/export", exportH.Export)

		// Sandbox (E2B)
		r.Post("/api/projects/{id}/sandbox", sandboxH.Create)
		r.Delete("/api/projects/{id}/sandbox", sandboxH.Destroy)
		r.Post("/api/projects/{id}/sandbox/sync", sandboxH.Sync)
		r.Get("/api/projects/{id}/sandbox/status", sandboxH.Status)

		// Files
		r.Get("/api/projects/{projectId}/files", filesH.List)
		r.Post("/api/projects/{projectId}/files", filesH.Write)
		r.Delete("/api/projects/{projectId}/files", filesH.Delete)

		// Versions
		r.Get("/api/projects/{projectId}/versions", versionsH.List)
		r.Post("/api/projects/{projectId}/versions", versionsH.Create)
		r.Post("/api/projects/{projectId}/versions/{versionId}/restore", versionsH.Restore)

		// Chat (SSE — tighter rate limit)
		r.Group(func(r chi.Router) {
			r.Use(chatLimiter.Middleware())
			r.Post("/api/projects/{projectId}/chat/stream", chatH.Stream)
		})
		r.Get("/api/projects/{projectId}/messages", chatH.GetMessages)

		// Image upload
		r.Post("/api/projects/{projectId}/upload", uploadH.Upload)
		r.Get("/api/projects/{projectId}/images", uploadH.ListImages)
		r.Delete("/api/projects/{projectId}/images/{imageId}", uploadH.DeleteImage)

		// Templates
		r.Get("/api/templates", templatesH.List)
		r.Post("/api/templates/{templateId}/use", templatesH.CreateFromTemplate)

		// Search
		r.Get("/api/search", searchH.Search)

		// Usage
		r.Get("/api/usage", usageH.GetUsage)
		r.Get("/api/projects/{id}/usage", usageH.GetProjectUsage)

		// Public sharing
		r.Post("/api/projects/{id}/share", shareH.Enable)
		r.Delete("/api/projects/{id}/share", shareH.Disable)

		// Profile
		r.Patch("/api/auth/profile", profileH.UpdateProfile)
		r.Post("/api/auth/change-password", profileH.ChangePassword)
		r.Delete("/api/auth/account", profileH.DeleteAccount)

		// Email notification preferences
		r.Get("/api/notifications/preferences", notifH.GetPreferences)
		r.Patch("/api/notifications/preferences", notifH.UpdatePreferences)
		r.Post("/api/notifications/test", notifH.SendTestEmail)

		// Team collaboration
		r.Get("/api/projects/{id}/members", teamH.ListMembers)
		r.Post("/api/projects/{id}/members/invite", teamH.InviteMember)
		r.Patch("/api/projects/{id}/members/{userId}", teamH.UpdateMemberRole)
		r.Delete("/api/projects/{id}/members/{userId}", teamH.RemoveMember)
		r.Post("/api/projects/{id}/leave", teamH.LeaveProject)
		r.Post("/api/invites/{token}/accept", teamH.AcceptInvite)

		// Real-time cursors (SSE)
		r.Get("/api/projects/{id}/realtime", realtimeH.Connect)
		r.Post("/api/projects/{id}/realtime/emit", realtimeH.Emit)
		r.Get("/api/projects/{id}/realtime/online", realtimeH.GetOnlineUsers)
	})

	// ── Public routes (no auth) ────────────────────────────
	r.Get("/api/share/{token}", shareH.GetShared)

	fmt.Printf("🚀 Server running on http://localhost:%s\n", cfg.Port)
	if err := http.ListenAndServe(":"+cfg.Port, r); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}
