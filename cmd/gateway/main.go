package main

import (
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	appsDir        = "/opt/apps"
	configPath     = "/etc/zero-scale/artifacts.json"
	allocatedPorts = make(map[string]int)
	lastActive     = make(map[string]time.Time)
	mu             sync.Mutex

	idleTimeout = 15 * time.Minute
	checkPeriod = 30 * time.Second
)

type ArtifactItem struct {
	URL  string `json:"url"`
	Type string `json:"type"`
}

type ConfigMapData struct {
	Artifacts map[string]ArtifactItem `json:"artifacts"`
}

func main() {
	if dir := os.Getenv("APPS_DIR"); dir != "" {
		appsDir = dir
	}
	os.MkdirAll(appsDir, 0755)

	go startReaper()
	go startArtifactSync()

	http.HandleFunc("/", handleRequest)

	log.Println("Starting Zero-Scale Gateway on :80...")
	if err := http.ListenAndServe(":80", nil); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}

func handleRequest(w http.ResponseWriter, r *http.Request) {
	host := r.Host
	if strings.Contains(host, ":") {
		host = strings.Split(host, ":")[0]
	}
	log.Printf("Received %s request for host: %s (path: %s)", r.Method, host, r.URL.Path)

	binaryPath := filepath.Join(appsDir, host)
	if !fileExists(binaryPath) {
		// Fallback lookup: match shortName (e.g. bdash2 -> bdash2.kube.fairfaxmedia.net)
		shortName := strings.Split(host, ".")[0]
		entries, err := os.ReadDir(appsDir)
		if err == nil {
			for _, entry := range entries {
				if !entry.IsDir() && (strings.HasPrefix(entry.Name(), shortName+".") || entry.Name() == shortName) {
					binaryPath = filepath.Join(appsDir, entry.Name())
					log.Printf("Resolved host %s to binary %s", host, binaryPath)
					break
				}
			}
		}
	}
	if !fileExists(binaryPath) {
		http.Error(w, fmt.Sprintf("Application %s not found", host), http.StatusNotFound)
		return
	}

	unitName := fmt.Sprintf("app-%s", host)

	mu.Lock()
	lastActive[host] = time.Now()

	active, err := isServiceActive(unitName)
	if err != nil {
		mu.Unlock()
		log.Printf("Error checking status of unit %s: %v", unitName, err)
		http.Error(w, "Failed to check service status", http.StatusInternalServerError)
		return
	}

	port, exists := allocatedPorts[host]
	if active && !exists {
		// Discover active port from systemd unit Environment if gateway restarted
		if activePort := getActiveServicePort(unitName); activePort > 0 {
			allocatedPorts[host] = activePort
			port = activePort
			exists = true
			log.Printf("Discovered active service %s running on port %d", host, port)
		}
	}

	if !active || !exists {
		// Service is stopped or unallocated; allocate fresh free port and start
		if active {
			stopServiceLocked(host)
		}
		freePort, err := getFreePort()
		if err != nil {
			mu.Unlock()
			log.Printf("Failed to get free port for %s: %v", host, err)
			http.Error(w, "Internal Server Error: No available ports", http.StatusInternalServerError)
			return
		}
		allocatedPorts[host] = freePort
		port = freePort

		log.Printf("Host %s matches binary %s. Starting sandboxed systemd unit on port %d...", host, binaryPath, port)
		err = startTransientService(host, port, binaryPath)
		if err != nil {
			delete(allocatedPorts, host)
			mu.Unlock()
			log.Printf("Failed to start transient systemd unit for %s: %v", host, err)
			http.Error(w, "Failed to start application process", http.StatusInternalServerError)
			return
		}
		mu.Unlock()

		// Wait for the app socket to bind and respond
		targetAddr := fmt.Sprintf("127.0.0.1:%d", port)
		if err := waitPortReady(targetAddr, 30*time.Second); err != nil {
			log.Printf("Service %s on port %d did not bind in time: %v", host, port, err)
			stopService(host)
			http.Error(w, "Application startup timeout", http.StatusGatewayTimeout)
			return
		}
		log.Printf("Service %s successfully scaled up on port %d!", host, port)
	} else {
		mu.Unlock()
	}

	// Reverse proxy request
	targetURL, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", port))
	proxy := httputil.NewSingleHostReverseProxy(targetURL)
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		log.Printf("Proxy error for %s on port %d: %v. Cleaning up service...", host, port, err)
		stopService(host)
		http.Error(w, "Bad Gateway", http.StatusBadGateway)
	}
	proxy.ServeHTTP(w, r)
}

// getFreePort queries kernel for free ephemeral port
func getFreePort() (int, error) {
	addr, err := net.ResolveTCPAddr("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	l, err := net.ListenTCP("tcp", addr)
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// isServiceActive checks if the dynamic systemd unit is running
func isServiceActive(unit string) (bool, error) {
	cmd := exec.Command("systemctl", "is-active", unit)
	err := cmd.Run()
	if err != nil {
		if _, ok := err.(*exec.ExitError); ok {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// getActiveServicePort reads PORT from running systemd service environment
func getActiveServicePort(unit string) int {
	out, err := exec.Command("systemctl", "show", "-p", "Environment", unit).Output()
	if err != nil {
		return 0
	}
	str := string(out)
	for _, env := range strings.Fields(str) {
		if strings.HasPrefix(env, "PORT=") || strings.HasPrefix(env, "Environment=PORT=") {
			parts := strings.Split(env, "=")
			if len(parts) >= 2 {
				val := parts[len(parts)-1]
				if p, err := strconv.Atoi(val); err == nil && p > 0 {
					return p
				}
			}
		}
	}
	return 0
}

// fetchK8sResource dynamically retrieves a ConfigMap or Secret from the in-cluster Kubernetes API
func fetchK8sResource(resourceType, name string) (map[string]string, error) {
	tokenBytes, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/token")
	if err != nil {
		return nil, err
	}
	token := strings.TrimSpace(string(tokenBytes))

	k8sHost := os.Getenv("KUBERNETES_SERVICE_HOST")
	if k8sHost == "" {
		k8sHost = "kubernetes.default.svc"
	}
	k8sPort := os.Getenv("KUBERNETES_SERVICE_PORT")
	if k8sPort == "" {
		k8sPort = "443"
	}

	reqURL := fmt.Sprintf("https://%s:%s/api/v1/namespaces/default/%s/%s", k8sHost, k8sPort, resourceType, name)
	req, err := http.NewRequest("GET", reqURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)

	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}
	client := &http.Client{Transport: tr, Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("k8s API returned status %d for %s/%s", resp.StatusCode, resourceType, name)
	}

	var res struct {
		Data map[string]string `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return nil, err
	}

	if resourceType == "secrets" {
		decoded := make(map[string]string)
		for k, v := range res.Data {
			b, err := base64.StdEncoding.DecodeString(v)
			if err == nil {
				decoded[k] = string(b)
			} else {
				decoded[k] = v
			}
		}
		return decoded, nil
	}

	return res.Data, nil
}

// startTransientService executes systemd-run with dynamic environment injection
func startTransientService(host string, port int, binaryPath string) error {
	unitName := fmt.Sprintf("app-%s", host)
	typePath := binaryPath + ".type"

	runType := "elf"
	if fileExists(typePath) {
		tBytes, err := os.ReadFile(typePath)
		if err == nil {
			runType = strings.TrimSpace(string(tBytes))
		}
	}

	baseArgs := []string{
		fmt.Sprintf("--unit=%s", unitName),
		"-p", "DynamicUser=yes",
		"-p", "PrivateTmp=yes",
		"-p", "ProtectSystem=strict",
		fmt.Sprintf("--setenv=PORT=%d", port),
	}

	if _, err := os.Stat("/app/uploads"); err == nil {
		baseArgs = append(baseArgs, "-p", "BindPaths=/app/uploads:/app/uploads")
	}

	envKeys := []string{
		"DATABASE_URL", "DATABASE_HOST", "DATABASE_PORT", "DATABASE_USER", "DATABASE_PASSWORD", "DATABASE_NAME",
		"PGHOST", "PGPORT", "PGUSER", "PGPASSWORD", "PGDATABASE",
	}
	for _, k := range envKeys {
		if v := os.Getenv(k); v != "" {
			baseArgs = append(baseArgs, fmt.Sprintf("--setenv=%s=%s", k, v))
		}
	}

	shortName := strings.Split(host, ".")[0]
	appCMName := "zero-scale-app-" + shortName
	if appData, err := fetchK8sResource("configmaps", appCMName); err == nil {
		for k, v := range appData {
			if k != "host" && k != "url" && k != "type" && k != "mounts" && k != "scale-to-zero" && k != "database_configmap" && k != "database_secret" {
				baseArgs = append(baseArgs, fmt.Sprintf("--setenv=%s=%s", k, v))
			}
		}
		if dbCM := appData["database_configmap"]; dbCM != "" {
			if cmData, err := fetchK8sResource("configmaps", dbCM); err == nil {
				for k, v := range cmData {
					baseArgs = append(baseArgs, fmt.Sprintf("--setenv=%s=%s", k, v))
				}
			}
		}
		if dbSec := appData["database_secret"]; dbSec != "" {
			if secData, err := fetchK8sResource("secrets", dbSec); err == nil {
				for k, v := range secData {
					baseArgs = append(baseArgs, fmt.Sprintf("--setenv=%s=%s", k, v))
				}
			}
		}
	}

	var cmd *exec.Cmd
	if runType == "wasm" {
		args := append(baseArgs,
			"/usr/local/bin/wasmtime", "run",
			"--tcplisten", fmt.Sprintf("127.0.0.1:%d", port),
			binaryPath,
		)
		cmd = exec.Command("systemd-run", args...)
	} else {
		args := append(baseArgs, binaryPath, "-port", strconv.Itoa(port))
		cmd = exec.Command("systemd-run", args...)
	}

	log.Printf("Executing: %s", cmd.String())
	return cmd.Run()
}

// stopServiceLocked shuts down dynamic systemd service unit and clears port state (assumes mu is held)
func stopServiceLocked(host string) error {
	unit := fmt.Sprintf("app-%s", host)
	cmd := exec.Command("systemctl", "stop", unit)
	_ = cmd.Run()
	cmdReset := exec.Command("systemctl", "reset-failed", unit)
	_ = cmdReset.Run()

	delete(allocatedPorts, host)
	return nil
}

// stopService safely locks mu and calls stopServiceLocked
func stopService(host string) error {
	mu.Lock()
	defer mu.Unlock()
	return stopServiceLocked(host)
}

// waitPortReady checks port every 200ms
func waitPortReady(addr string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err == nil {
			conn.Close()
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("timeout waiting for %s", addr)
}

// startReaper checks active services and stops idle ones
func startReaper() {
	ticker := time.NewTicker(checkPeriod)
	for range ticker.C {
		mu.Lock()
		now := time.Now()
		for host, lastTime := range lastActive {
			unitName := fmt.Sprintf("app-%s", host)
			active, err := isServiceActive(unitName)
			if err == nil && active && now.Sub(lastTime) > idleTimeout {
				log.Printf("Service %s has been idle for %v. Scaling to zero...", host, idleTimeout)
				if err := stopServiceLocked(host); err != nil {
					log.Printf("Failed to stop idle unit %s: %v", unitName, err)
				}
			}
		}
		mu.Unlock()
	}
}

// startArtifactSync polls artifacts config map and downloads binaries dynamically
func startArtifactSync() {
	ticker := time.NewTicker(15 * time.Second)
	for range ticker.C {
		if !fileExists(configPath) {
			continue
		}
		data, err := os.ReadFile(configPath)
		if err != nil {
			log.Printf("Failed to read artifacts config: %v", err)
			continue
		}
		var cfg ConfigMapData
		if err := json.Unmarshal(data, &cfg); err != nil {
			log.Printf("Failed to parse artifacts config JSON: %v", err)
			continue
		}

		for host, item := range cfg.Artifacts {
			targetPath := filepath.Join(appsDir, host)
			typePath := targetPath + ".type"

			_ = os.WriteFile(typePath, []byte(item.Type), 0644)

			if !fileExists(targetPath) {
				log.Printf("Downloading artifact for %s from %s...", host, item.URL)
				err := downloadFile(item.URL, targetPath)
				if err != nil {
					log.Printf("Failed to download %s: %v", item.URL, err)
					continue
				}
				os.Chmod(targetPath, 0755)
				log.Printf("Successfully deployed %s to %s", host, targetPath)
			}
		}
	}
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return false
	}
	return err == nil && !info.IsDir()
}

func downloadFile(url, targetPath string) error {
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("bad status: %s", resp.Status)
	}

	out, err := os.Create(targetPath)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, resp.Body)
	return err
}
