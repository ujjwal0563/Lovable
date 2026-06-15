package main

import (
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
	aiClient := ai.NewClient(cfg.AnthropicKey)
	if cfg.AnthropicKey == "" {
		log.Println("⚠  ANTHROPIC_API_KEY not set")
	} else {
		log.Println("✓ Anthropic Claude ready")
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
	chatH := handlers.NewChatHandler(pool, aiClient)
	versionsH := handlers.NewVersionsHandler(pool)
	dupH := handlers.NewDuplicateHandler(pool)
	exportH := handlers.NewExportHandler(pool)
	sandboxH := handlers.NewSandboxHandler(pool, cfg.E2BApiKey)

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
	})

	fmt.Printf("🚀 Server running on http://localhost:%s\n", cfg.Port)
	if err := http.ListenAndServe(":"+cfg.Port, r); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}
