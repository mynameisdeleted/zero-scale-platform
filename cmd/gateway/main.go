package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"git.fairfaxmedia.net/open-cream-cheese/zero-scale-platform/internal/banner"
	"git.fairfaxmedia.net/open-cream-cheese/zero-scale-platform/internal/config"
	"git.fairfaxmedia.net/open-cream-cheese/zero-scale-platform/internal/proxy"
	"git.fairfaxmedia.net/open-cream-cheese/zero-scale-platform/internal/reaper"
	"git.fairfaxmedia.net/open-cream-cheese/zero-scale-platform/internal/sync"
)

func main() {
	// 1. Load configuration and CLI flags
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("Configuration error: %v", err)
	}

	// Ensure apps directory exists
	if err := os.MkdirAll(cfg.AppsDir, 0755); err != nil {
		log.Fatalf("Failed to create apps directory %s: %v", cfg.AppsDir, err)
	}

	// 2. Instantiate gateway proxy
	gw := proxy.New(cfg)
	gw.DiscoverExistingServices()

	// 3. Start background workers
	rp := reaper.New(cfg, gw.GetLastActiveMap, gw.StopService)
	go rp.Start()
	go sync.StartArtifactSync(cfg)

	// 4. Print startup banner with direct URLs and journalctl CLI commands
	banner.PrintStartup(cfg)

	// 5. Configure HTTP server
	server := &http.Server{
		Addr:    cfg.ListenAddr,
		Handler: gw,
	}

	// 6. Graceful shutdown handler
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	go func() {
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("Gateway server error: %v", err)
		}
	}()

	<-stop
	log.Println("Shutting down Zero-Scale Gateway...")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := server.Shutdown(ctx); err != nil {
		log.Printf("Server shutdown error: %v", err)
	}
	log.Println("Zero-Scale Gateway stopped.")
}
