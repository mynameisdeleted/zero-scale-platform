package runner

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"git.fairfaxmedia.net/open-cream-cheese/zero-scale-platform/internal/config"
	"git.fairfaxmedia.net/open-cream-cheese/zero-scale-platform/internal/jwtauth"
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

	// Fetch dynamic app ConfigMap and Secret from Kubernetes API
	shortName := strings.Split(host, ".")[0]
	appCMName := "zero-scale-app-" + shortName
	var appArgs string
	appData, _ := FetchK8sResource("configmaps", appCMName, cfg.KubeNamespace)
	if appData != nil {
		appArgs = appData["args"]
	}

	baseArgs := []string{
		fmt.Sprintf("--unit=%s", unitName),
		"-p", "PrivateTmp=yes",
		fmt.Sprintf("--setenv=PORT=%d", port),
	}

	// Dynamic user configuration (default: DynamicUser=yes, can be disabled via dynamic-user=false or user=<user>)
	if appData != nil && (appData["dynamic-user"] == "false" || appData["dynamic_user"] == "false") {
		if u := appData["user"]; u != "" {
			baseArgs = append(baseArgs, "-p", "User="+u)
		}
	} else if appData != nil && appData["user"] != "" {
		baseArgs = append(baseArgs, "-p", "User="+appData["user"])
	} else {
		baseArgs = append(baseArgs, "-p", "DynamicUser=yes", "-p", "ProtectSystem=strict")
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

	if appData != nil {
		// Set of internal platform configuration keys that should never be passed as process environment variables
		ignoredKeys := map[string]bool{
			"host": true, "url": true, "type": true, "mounts": true,
			"scale-to-zero": true, "scale_to_zero": true,
			"idle-timeout": true, "idle_timeout": true,
			"database_configmap": true, "database_secret": true,
			"args": true, "user": true,
			"dynamic-user": true, "dynamic_user": true,
		}

		for k, v := range appData {
			if ignoredKeys[k] || strings.Contains(k, "-") {
				continue
			}
			v = strings.ReplaceAll(v, "${PORT}", strconv.Itoa(port))
			v = strings.ReplaceAll(v, "${HOST}", host)
			baseArgs = append(baseArgs, fmt.Sprintf("--setenv=%s=%s", k, v))
		}
		if dbCM := appData["database_configmap"]; dbCM != "" {
			if cmData, err := FetchK8sResource("configmaps", dbCM, cfg.KubeNamespace); err == nil {
				for k, v := range cmData {
					v = strings.ReplaceAll(v, "${PORT}", strconv.Itoa(port))
					v = strings.ReplaceAll(v, "${HOST}", host)
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

		// If this app is fronted by OAuth, inject OAUTH_PUBLIC_KEY and LOGOUT_URL
		if appData["oauth-setup"] != "" || appData["oauth_setup"] != "" || appData["oauth"] != "" {
			signer := jwtauth.GetSigner()
			if pubKey := signer.PublicPEM(); pubKey != "" {
				baseArgs = append(baseArgs, fmt.Sprintf("--setenv=OAUTH_PUBLIC_KEY=%s", pubKey))
			}
			logoutURL := fmt.Sprintf("https://%s/oauth2/sign_out", host)
			baseArgs = append(baseArgs, fmt.Sprintf("--setenv=LOGOUT_URL=%s", logoutURL))
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
		execArgs := []string{binaryPath}
		if appArgs != "" {
			for _, arg := range strings.Fields(appArgs) {
				arg = strings.ReplaceAll(arg, "${PORT}", strconv.Itoa(port))
				arg = strings.ReplaceAll(arg, "${HOST}", host)
				execArgs = append(execArgs, arg)
			}
		} else {
			execArgs = append(execArgs, "-port", strconv.Itoa(port))
		}
		args := append(baseArgs, execArgs...)
		log.Printf("Executing: /usr/bin/systemd-run %s", strings.Join(args, " "))
		cmd = exec.Command("systemd-run", args...)
	}

	out, err := cmd.CombinedOutput()
	if err != nil {
		if strings.Contains(string(out), "already loaded") || strings.Contains(string(out), "fragment file") {
			log.Printf("Transient unit %s has stale fragment, cleaning up and retrying...", unitName)
			_ = exec.Command("systemctl", "stop", unitName).Run()
			_ = exec.Command("systemctl", "reset-failed", unitName).Run()
			_ = os.Remove(fmt.Sprintf("/run/systemd/transient/%s.service", unitName))
			_ = exec.Command("systemctl", "daemon-reload").Run()
			cmdRetry := exec.Command("systemd-run", cmd.Args[1:]...)
			outRetry, errRetry := cmdRetry.CombinedOutput()
			if errRetry == nil {
				return nil
			}
			return fmt.Errorf("systemd-run retry failed: %v, output: %s", errRetry, string(outRetry))
		}
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

// ListRunningAppUnits returns the hostnames of all running app-*.service units
func ListRunningAppUnits() []string {
	out, err := exec.Command("systemctl", "list-units", "--type=service", "--state=running", "--no-legend", "--no-pager", "app-*.service").Output()
	if err != nil {
		return nil
	}
	var hosts []string
	lines := strings.Split(string(out), "\n")
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) > 0 && strings.HasPrefix(fields[0], "app-") && strings.HasSuffix(fields[0], ".service") {
			host := strings.TrimPrefix(fields[0], "app-")
			host = strings.TrimSuffix(host, ".service")
			hosts = append(hosts, host)
		}
	}
	return hosts
}

// GetServiceStartTime returns the ActiveEnterTimestamp for a unit
func GetServiceStartTime(unit string) (time.Time, error) {
	out, err := exec.Command("systemctl", "show", "-p", "ActiveEnterTimestamp", unit).Output()
	if err != nil {
		return time.Time{}, err
	}
	str := strings.TrimSpace(string(out))
	str = strings.TrimPrefix(str, "ActiveEnterTimestamp=")
	if str == "" {
		return time.Time{}, fmt.Errorf("empty timestamp")
	}
	return time.Parse("Mon 2006-01-02 15:04:05 MST", str)
}
