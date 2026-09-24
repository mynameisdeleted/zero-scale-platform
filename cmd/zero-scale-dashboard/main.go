package main

import (
	_ "embed"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"time"
)

//go:embed dashboard.html
var dashboardHTML []byte

func main() {
	port := flag.Int("port", 8080, "Port to listen on")
	gatewayURL := flag.String("gateway-url", "http://zero-scale-gateway.default.svc.cluster.local", "URL of Zero-Scale Gateway")
	appsDir := flag.String("apps-dir", "/opt/apps", "Apps directory")
	flag.Parse()

	collector := NewCollector(*gatewayURL, *appsDir)

	http.HandleFunc("/api/status", func(w http.ResponseWriter, r *http.Request) {
		status, err := collector.Collect()
		if err != nil {
			http.Error(w, fmt.Sprintf(`{"error": %q}`, err.Error()), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		json.NewEncoder(w).Encode(status)
	})

	http.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" && r.URL.Path != "/index.html" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(dashboardHTML)
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
