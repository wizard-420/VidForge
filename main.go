package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"yt-automation-studio/api"
	"yt-automation-studio/config"
	"yt-automation-studio/storage"
	"yt-automation-studio/worker"
)

func main() {
	// Banner
	fmt.Println(`
  ╔═══════════════════════════════════════════════╗
  ║    YouTube Automation Studio  v1.0.0          ║
  ║    AI-Powered Faceless Video Pipeline         ║
  ╚═══════════════════════════════════════════════╝
	`)

	// Load configuration from .env
	config.Load()

	// Initialize SQLite database
	dbPath := filepath.Join("storage", "jobs.db")
	if err := storage.InitDB(dbPath); err != nil {
		log.Fatalf("❌ Database init failed: %v", err)
	}
	defer storage.Close()

	// Set up HTTP router
	mux := http.NewServeMux()

	// Register API routes
	api.RegisterRoutes(mux)

	// Serve frontend UI
	uiDir := "./ui"
	if _, err := os.Stat(uiDir); err == nil {
		mux.Handle("/", http.FileServer(http.Dir(uiDir)))
	} else {
		// Fallback: serve a simple redirect message
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/" {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, `<html><body><h1>YouTube Automation Studio</h1>
				<p>API running. Place index.html in ./ui/ directory.</p>
				<p>API docs: <a href="/api/status">/api/status</a></p>
			</body></html>`)
		})
	}

	// Initialize background worker pool
	worker.InitQueue()

	// CORS middleware wrapper
	handler := corsMiddleware(mux)

	// Start server
	addr := fmt.Sprintf(":%d", config.App.ServerPort)
	log.Printf("🚀 Server starting on http://localhost%s", addr)
	log.Printf("📂 Workspace: %s", config.App.WorkspaceDir)
	log.Printf("📡 API: http://localhost%s/api/status", addr)
	log.Printf("🖥️  Dashboard: http://localhost%s", addr)

	if err := http.ListenAndServe(addr, handler); err != nil {
		log.Fatalf("❌ Server failed: %v", err)
	}
}

// corsMiddleware adds CORS headers for local development
func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")

		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}

		next.ServeHTTP(w, r)
	})
}
