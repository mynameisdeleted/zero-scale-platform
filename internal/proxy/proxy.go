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

	"git.fairfaxmedia.net/open-cream-cheese/zero-scale-platform/internal/appname"
	"git.fairfaxmedia.net/open-cream-cheese/zero-scale-platform/internal/banner"
	"git.fairfaxmedia.net/open-cream-cheese/zero-scale-platform/internal/config"
	"git.fairfaxmedia.net/open-cream-cheese/zero-scale-platform/internal/handlers"
	"git.fairfaxmedia.net/open-cream-cheese/zero-scale-platform/internal/jwtauth"
	"git.fairfaxmedia.net/open-cream-cheese/zero-scale-platform/internal/registry"
	"git.fairfaxmedia.net/open-cream-cheese/zero-scale-platform/internal/runner"
)

// Gateway manages dynamic on-demand reverse proxying and zero-scale scaling
type Gateway struct {
	cfg            *config.Config
	allocatedPorts map[string]int
	lastActive     map[string]time.Time
	inFlight       map[string]int
	mu             sync.Mutex
	apps           *registry.Registry
}

// New creates a new Gateway instance
func New(cfg *config.Config, apps *registry.Registry) *Gateway {
	return &Gateway{
		cfg:            cfg,
		apps:           apps,
		allocatedPorts: make(map[string]int),
		lastActive:     make(map[string]time.Time),
		inFlight:       make(map[string]int),
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

// StopService deallocates port and stops the dynamic unit if no requests in flight
func (g *Gateway) StopService(host string) error {
	g.mu.Lock()
	if g.inFlight[host] > 0 {
		// Do not kill service while active HTTP requests are being processed
		g.lastActive[host] = time.Now()
		g.mu.Unlock()
		return nil
	}
	delete(g.allocatedPorts, host)
	delete(g.lastActive, host)
	g.mu.Unlock()
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
	requestHost := strings.ToLower(appname.StripPort(r.Host))

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
		log.Printf("📥 [%s] %s %s", requestHost, r.Method, r.URL.Path)
	}

	// Every hostname of an app maps to its canonical host: one binary path, one unit, one config.
	app := g.apps.Lookup(requestHost)
	host := app.CanonicalHost

	// Intercept log viewer requests
	if r.URL.Path == "/_logs" || strings.HasPrefix(r.URL.Path, "/_logs/") {
		handlers.HandleLogsView(w, r, host, g.cfg, g.GetPort)
		return
	}

	binaryPath := g.resolveBinary(host, requestHost)
	if binaryPath == "" {
		http.Error(w, fmt.Sprintf("Application %s not found", requestHost), http.StatusNotFound)
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
		if err := runner.StartTransientService(host, port, binaryPath, g.cfg.KubeNamespace, app.Data); err != nil {
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

	// Custom Director to mint ephemeral X-ZeroScale-Token JWT when OAuth headers are present
	origDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		origDirector(req)

		// Check for upstream OAuth authentication headers from oauth2-proxy / ingress
		email := req.Header.Get("X-Auth-Request-Email")
		user := req.Header.Get("X-Auth-Request-User")
		if user == "" {
			user = req.Header.Get("X-Auth-Request-Preferred-Username")
		}

		if email != "" || user != "" {
			var groups []string
			if rawGroups := req.Header.Get("X-Auth-Request-Groups"); rawGroups != "" {
				for _, g := range strings.Split(rawGroups, ",") {
					if trimmed := strings.TrimSpace(g); trimmed != "" {
						groups = append(groups, trimmed)
					}
				}
			}

			signer := jwtauth.GetSigner()
			token, err := signer.MintToken(email, user, groups, requestHost)
			if err == nil {
				req.Header.Set("X-ZeroScale-Token", token)
				// Also set standard Bearer Authorization if not already populated
				if req.Header.Get("Authorization") == "" {
					req.Header.Set("Authorization", "Bearer "+token)
				}
			} else {
				log.Printf("⚠️ Failed to mint ZeroScale JWT for %s: %v", host, err)
			}
		}
	}

	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		log.Printf("Proxy error for %s on port %d: %v. Cleaning up service...", host, port, err)
		_ = g.StopService(host)
		http.Error(w, "Bad Gateway", http.StatusBadGateway)
	}

	g.mu.Lock()
	g.inFlight[host]++
	g.lastActive[host] = time.Now()
	g.mu.Unlock()

	defer func() {
		g.mu.Lock()
		g.inFlight[host]--
		if g.inFlight[host] < 0 {
			g.inFlight[host] = 0
		}
		g.lastActive[host] = time.Now()
		g.mu.Unlock()
	}()

	proxy.ServeHTTP(w, r)
}

// resolveBinary finds the executable for an app: the canonical host's entry first,
// then the request host's, then the legacy short-name prefix match. Returns "" if none exists.
func (g *Gateway) resolveBinary(host, requestHost string) string {
	for _, h := range []string{host, requestHost} {
		if p := filepath.Join(g.cfg.AppsDir, h); runner.FileExists(p) {
			return p
		}
	}
	// Fallback lookup: match shortName prefix (e.g. bdash2 -> bdash2.kube.fairfaxmedia.net)
	shortName := strings.Split(requestHost, ".")[0]
	entries, err := os.ReadDir(g.cfg.AppsDir)
	if err != nil {
		return ""
	}
	for _, entry := range entries {
		if !entry.IsDir() && (strings.HasPrefix(entry.Name(), shortName+".") || entry.Name() == shortName) {
			p := filepath.Join(g.cfg.AppsDir, entry.Name())
			log.Printf("Resolved host %s to binary %s", requestHost, p)
			return p
		}
	}
	return ""
}
