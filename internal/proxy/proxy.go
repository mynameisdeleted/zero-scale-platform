package proxy

import (
	"fmt"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"git.fairfaxmedia.net/open-cream-cheese/zero-scale-platform/internal/banner"
	"git.fairfaxmedia.net/open-cream-cheese/zero-scale-platform/internal/config"
	"git.fairfaxmedia.net/open-cream-cheese/zero-scale-platform/internal/handlers"
	"git.fairfaxmedia.net/open-cream-cheese/zero-scale-platform/internal/runner"
)

// Gateway manages dynamic on-demand reverse proxying and zero-scale scaling
type Gateway struct {
	cfg            *config.Config
	allocatedPorts map[string]int
	lastActive     map[string]time.Time
	mu             sync.Mutex
}

// New creates a new Gateway instance
func New(cfg *config.Config) *Gateway {
	return &Gateway{
		cfg:            cfg,
		allocatedPorts: make(map[string]int),
		lastActive:     make(map[string]time.Time),
	}
}

// GetPort returns the currently allocated port for host, or 0 if not allocated
func (g *Gateway) GetPort(host string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.allocatedPorts[host]
}

// GetLastActiveMap returns a snapshot copy of the last active timestamps
func (g *Gateway) GetLastActiveMap() map[string]time.Time {
	g.mu.Lock()
	defer g.mu.Unlock()
	cp := make(map[string]time.Time, len(g.lastActive))
	for k, v := range g.lastActive {
		cp[k] = v
	}
	return cp
}

// StopService deallocates port and stops the dynamic unit
func (g *Gateway) StopService(host string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.allocatedPorts, host)
	delete(g.lastActive, host)
	return runner.StopService(host)
}

// DiscoverExistingServices discovers any currently active systemd units
func (g *Gateway) DiscoverExistingServices() {
	g.mu.Lock()
	defer g.mu.Unlock()

	runningHosts := runner.ListRunningAppUnits()
	now := time.Now()
	for _, host := range runningHosts {
		unitName := fmt.Sprintf("app-%s", host)
		port := runner.GetActiveServicePort(unitName)
		if port > 0 {
			g.allocatedPorts[host] = port
		}
		startTime, err := runner.GetServiceStartTime(unitName)
		if err == nil && !startTime.IsZero() {
			g.lastActive[host] = startTime
		} else {
			g.lastActive[host] = now
		}
		log.Printf("Discovered running unit %s (port %d, lastActive: %v)", unitName, port, g.lastActive[host])
	}
}

// ServeHTTP handles incoming HTTP requests, intercepts logs, and reverse proxies
func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	host := r.Host
	if strings.Contains(host, ":") {
		host = strings.Split(host, ":")[0]
	}

	// Filter out noisy static asset logging
	isStatic := strings.HasPrefix(r.URL.Path, "/static/") ||
		strings.HasPrefix(r.URL.Path, "/_logs") ||
		r.URL.Path == "/favicon.ico" ||
		strings.HasSuffix(r.URL.Path, ".css") ||
		strings.HasSuffix(r.URL.Path, ".js") ||
		strings.HasSuffix(r.URL.Path, ".png") ||
		strings.HasSuffix(r.URL.Path, ".jpg") ||
		strings.HasSuffix(r.URL.Path, ".ico")
	if !isStatic {
		log.Printf("📥 [%s] %s %s", host, r.Method, r.URL.Path)
	}

	// Intercept log viewer requests
	if r.URL.Path == "/_logs" || strings.HasPrefix(r.URL.Path, "/_logs/") {
		handlers.HandleLogsView(w, r, host, g.cfg, g.GetPort)
		return
	}

	binaryPath := filepath.Join(g.cfg.AppsDir, host)
	if !runner.FileExists(binaryPath) {
		// Fallback lookup: match shortName prefix (e.g. bdash2 -> bdash2.kube.fairfaxmedia.net)
		shortName := strings.Split(host, ".")[0]
		entries, err := os.ReadDir(g.cfg.AppsDir)
		if err == nil {
			for _, entry := range entries {
				if !entry.IsDir() && (strings.HasPrefix(entry.Name(), shortName+".") || entry.Name() == shortName) {
					binaryPath = filepath.Join(g.cfg.AppsDir, entry.Name())
					log.Printf("Resolved host %s to binary %s", host, binaryPath)
					break
				}
			}
		}
	}
	if !runner.FileExists(binaryPath) {
		http.Error(w, fmt.Sprintf("Application %s not found", host), http.StatusNotFound)
		return
	}

	unitName := fmt.Sprintf("app-%s", host)

	g.mu.Lock()
	g.lastActive[host] = time.Now()

	active, err := runner.IsServiceActive(unitName)
	if err != nil {
		g.mu.Unlock()
		log.Printf("Error checking status of unit %s: %v", unitName, err)
		http.Error(w, "Failed to check service status", http.StatusInternalServerError)
		return
	}

	port, exists := g.allocatedPorts[host]
	if active && !exists {
		if activePort := runner.GetActiveServicePort(unitName); activePort > 0 {
			g.allocatedPorts[host] = activePort
			port = activePort
			exists = true
			log.Printf("Discovered active service %s running on port %d", host, port)
		}
	}

	if !active || !exists {
		// Stop any stale unit without holding lock
		if active {
			delete(g.allocatedPorts, host)
			delete(g.lastActive, host)
			_ = runner.StopService(host)
		}
		freePort, err := runner.GetFreePort()
		if err != nil {
			g.mu.Unlock()
			log.Printf("Failed to get free port for %s: %v", host, err)
			http.Error(w, "Internal Server Error: No available ports", http.StatusInternalServerError)
			return
		}
		g.allocatedPorts[host] = freePort
		port = freePort

		log.Printf("Host %s matches binary %s. Starting sandboxed systemd unit on port %d...", host, binaryPath, port)
		if err := runner.StartTransientService(host, port, binaryPath, g.cfg); err != nil {
			delete(g.allocatedPorts, host)
			g.mu.Unlock()
			log.Printf("Failed to start service %s: %v", host, err)
			http.Error(w, fmt.Sprintf("Failed to start service %s", host), http.StatusInternalServerError)
			return
		}
		g.mu.Unlock()

		targetAddr := fmt.Sprintf("127.0.0.1:%d", port)
		if err := runner.WaitPortReady(targetAddr, 30*time.Second); err != nil {
			log.Printf("Service %s on port %d did not bind in time: %v", host, port, err)
			_ = g.StopService(host)
			http.Error(w, "Application startup timeout", http.StatusGatewayTimeout)
			return
		}
		banner.PrintScaleUp(host, port)
	} else {
		g.mu.Unlock()
	}

	targetURL, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", port))
	proxy := httputil.NewSingleHostReverseProxy(targetURL)
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		log.Printf("Proxy error for %s on port %d: %v. Cleaning up service...", host, port, err)
		_ = g.StopService(host)
		http.Error(w, "Bad Gateway", http.StatusBadGateway)
	}
	proxy.ServeHTTP(w, r)
}
