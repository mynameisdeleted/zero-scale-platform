package main

import (
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"git.fairfaxmedia.net/open-cream-cheese/zero-scale-platform/internal/runner"
)

//go:embed dist/*
var embeddedDistFS embed.FS

func extractToken(r *http.Request) string {
	authHeader := r.Header.Get("Authorization")
	if strings.HasPrefix(authHeader, "Bearer ") {
		return strings.TrimSpace(strings.TrimPrefix(authHeader, "Bearer "))
	}
	if c, err := r.Cookie("kube_token"); err == nil && c.Value != "" {
		return strings.TrimSpace(c.Value)
	}
	return strings.TrimSpace(r.URL.Query().Get("token"))
}

func main() {
	port := flag.Int("port", 8080, "Port to listen on")
	gatewayURL := flag.String("gateway-url", "http://zero-scale-gateway.default.svc.cluster.local", "URL of Zero-Scale Gateway")
	appsDir := flag.String("apps-dir", "/opt/apps", "Apps directory")
	staticDir := flag.String("static-dir", "", "Optional local directory to serve assets from directly (for development)")
	flag.Parse()

	collector := NewCollector(*gatewayURL, *appsDir)

	subFS, err := fs.Sub(embeddedDistFS, "dist")
	if err != nil {
		log.Fatalf("Failed to initialize embedded assets filesystem: %v", err)
	}

	var assetHandler http.Handler
	if *staticDir != "" {
		if _, err := os.Stat(*staticDir); err == nil {
			log.Printf("📂 Serving static assets live from local directory: %s", *staticDir)
			assetHandler = http.FileServer(http.Dir(*staticDir))
		} else {
			log.Printf("⚠️ Specified static-dir %s not found, falling back to embedded assets", *staticDir)
		}
	}
	if assetHandler == nil {
		assetHandler = http.FileServer(http.FS(subFS))
	}

	http.HandleFunc("/api/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "SAMEORIGIN")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}

		token := extractToken(r)
		status, err := collector.GetClusterStatus(token)
		if err != nil {
			http.Error(w, fmt.Sprintf(`{"error": %q}`, err.Error()), http.StatusInternalServerError)
			return
		}
		json.NewEncoder(w).Encode(status)
	})

	http.HandleFunc("/api/auth/verify", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "SAMEORIGIN")
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}

		token := extractToken(r)
		if token == "" {
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(map[string]any{"authenticated": false, "reason": "No token provided"})
			return
		}

		valid, reason, err := runner.VerifyKubeToken(token, "default")
		if err != nil || !valid {
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(map[string]any{"authenticated": false, "reason": reason})
			return
		}

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]any{"authenticated": true})
	})

	http.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "SAMEORIGIN")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")

		path := r.URL.Path
		if strings.HasPrefix(path, "/dist/") {
			r2 := new(http.Request)
			*r2 = *r
			r2.URL.Path = strings.TrimPrefix(path, "/dist")
			assetHandler.ServeHTTP(w, r2)
			return
		}

		if path == "/" || path == "/index.html" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-cache, must-revalidate")
			assetHandler.ServeHTTP(w, r)
			return
		}

		if strings.HasSuffix(path, ".css") {
			w.Header().Set("Content-Type", "text/css; charset=utf-8")
			w.Header().Set("Cache-Control", "no-cache, must-revalidate")
		} else if strings.HasSuffix(path, ".js") {
			w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
			w.Header().Set("Cache-Control", "no-cache, must-revalidate")
		}

		assetHandler.ServeHTTP(w, r)
	})

	addr := fmt.Sprintf(":%d", *port)
	log.Printf("================================================================================")
	log.Printf("🚀 Zero-Scale Platform Dashboard listening on %s", addr)
	log.Printf("📁 Zero-Scale apps directory: %s", *appsDir)
	log.Printf("🌐 Gateway upstream:          %s", *gatewayURL)
	log.Printf("🔗 Gitea Source Server:       https://git.fairfaxmedia.net/")
	log.Printf("================================================================================")

	server := &http.Server{
		Addr:         addr,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	if err := server.ListenAndServe(); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}
