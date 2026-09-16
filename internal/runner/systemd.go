package runner

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"git.fairfaxmedia.net/open-cream-cheese/zero-scale-platform/internal/config"
)

// IsServiceActive checks if the dynamic systemd unit is running
func IsServiceActive(unit string) (bool, error) {
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

// GetActiveServicePort reads PORT from running systemd service environment
func GetActiveServicePort(unit string) int {
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

// StartTransientService executes systemd-run with dynamic environment injection
func StartTransientService(host string, port int, binaryPath string, cfg *config.Config) error {
	unitName := fmt.Sprintf("app-%s", host)
	typePath := binaryPath + ".type"

	runType := "elf"
	if FileExists(typePath) {
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

	// Mount persistent volume if present
	if _, err := os.Stat("/app/uploads"); err == nil {
		baseArgs = append(baseArgs, "-p", "BindPaths=/app/uploads:/app/uploads")
	}

	// Forward standard database environment variables if present on host
	envKeys := []string{
		"DATABASE_URL", "DATABASE_HOST", "DATABASE_PORT", "DATABASE_USER", "DATABASE_PASSWORD", "DATABASE_NAME",
		"PGHOST", "PGPORT", "PGUSER", "PGPASSWORD", "PGDATABASE",
	}
	for _, k := range envKeys {
		if v := os.Getenv(k); v != "" {
			baseArgs = append(baseArgs, fmt.Sprintf("--setenv=%s=%s", k, v))
		}
	}

	// Fetch dynamic app ConfigMap and Secret from Kubernetes API
	shortName := strings.Split(host, ".")[0]
	appCMName := "zero-scale-app-" + shortName
	if appData, err := FetchK8sResource("configmaps", appCMName, cfg.KubeNamespace); err == nil {
		for k, v := range appData {
			if k != "host" && k != "url" && k != "type" && k != "mounts" && k != "scale-to-zero" && k != "database_configmap" && k != "database_secret" {
				baseArgs = append(baseArgs, fmt.Sprintf("--setenv=%s=%s", k, v))
			}
		}
		if dbCM := appData["database_configmap"]; dbCM != "" {
			if cmData, err := FetchK8sResource("configmaps", dbCM, cfg.KubeNamespace); err == nil {
				for k, v := range cmData {
					baseArgs = append(baseArgs, fmt.Sprintf("--setenv=%s=%s", k, v))
				}
			}
		}
		if dbSec := appData["database_secret"]; dbSec != "" {
			if secData, err := FetchK8sResource("secrets", dbSec, cfg.KubeNamespace); err == nil {
				for k, v := range secData {
					baseArgs = append(baseArgs, fmt.Sprintf("--setenv=%s=%s", k, v))
				}
			}
		}
	}

	var cmd *exec.Cmd
	if runType == "wasm" {
		args := append(baseArgs,
			"--setenv=WASM_PORT="+strconv.Itoa(port),
			"/usr/local/bin/wasmtime", "run",
			"--env", fmt.Sprintf("PORT=%d", port),
			"--tcplisten", fmt.Sprintf("127.0.0.1:%d", port),
			binaryPath,
		)
		log.Printf("Executing WASM runner: /usr/bin/systemd-run %s", strings.Join(args, " "))
		cmd = exec.Command("systemd-run", args...)
	} else {
		args := append(baseArgs, binaryPath, "-port", strconv.Itoa(port))
		log.Printf("Executing: /usr/bin/systemd-run %s", strings.Join(args, " "))
		cmd = exec.Command("systemd-run", args...)
	}

	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemd-run failed: %v, output: %s", err, string(out))
	}
	return nil
}

// StopService stops the dynamic systemd unit
func StopService(host string) error {
	unitName := fmt.Sprintf("app-%s", host)
	cmd := exec.Command("systemctl", "stop", unitName)
	return cmd.Run()
}
