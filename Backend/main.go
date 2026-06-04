package main

import (
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

// Custom middleware to prevent browser CORS blocks
func enableCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS, PUT, DELETE")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")

		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func main() {
	r := chi.NewRouter()

	// Base middleware stack
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Logger) // This will beautifully log every request in your terminal!
	r.Use(middleware.Recoverer)
	r.Use(enableCORS) // Keeps your React frontend connected cleanly

	// Root endpoint for your Frontend counter button
	r.Get("/", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("Hello! Your Go Chi Backend is working perfectly."))
	})

	// Health check endpoint
	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("OK"))
	})

	fmt.Println("Starting server on :8080...")
	err := http.ListenAndServe(":8080", r)
	if err != nil {
		fmt.Printf("Error starting server: %s\n", err)
	}
}