package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func fileSHA256(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))
}

// DeployBinary ensures local binary is uploaded to gateway pod /opt/apps/ and symlinks are configured.
func DeployBinary(app *AppConfig, namespace string) error {
	if _, err := os.Stat(app.BinaryPath); os.IsNotExist(err) {
		return fmt.Errorf("local binary not found at '%s'", app.BinaryPath)
	}

	pod, err := GetGatewayPod(namespace)
	if err != nil {
		return err
	}

	binBase := filepath.Base(app.BinaryPath)
	targetBinPath := fmt.Sprintf("/opt/apps/%s", binBase)

	localHash := fileSHA256(app.BinaryPath)
	remoteOut, _ := ExecPod(pod, namespace, "systemd-runner", []string{
		"sh", "-c", fmt.Sprintf("sha256sum '%s' 2>/dev/null | awk '{print $1}'", targetBinPath),
	}, "")
	remoteHash := strings.TrimSpace(remoteOut)

	if remoteHash == "" || (localHash != "" && localHash != remoteHash) {
		fmt.Printf("--> Uploading local binary '%s' to %s:%s...\n", app.BinaryPath, pod, targetBinPath)
		err = CpToPod(app.BinaryPath, targetBinPath, pod, namespace, "systemd-runner")
		if err != nil {
			return fmt.Errorf("failed to copy binary to pod: %w", err)
		}
		_, err = ExecPod(pod, namespace, "systemd-runner", []string{"chmod", "+x", targetBinPath}, "")
		if err != nil {
			return fmt.Errorf("failed to chmod binary: %w", err)
		}
		// Stop running transient unit if any so next request launches the updated binary
		_, _ = ExecPod(pod, namespace, "systemd-runner", []string{
			"systemctl", "stop", fmt.Sprintf("app-%s.service", app.CanonicalHost()),
		}, "")
		fmt.Printf("--> Successfully deployed and set executable: %s\n", targetBinPath)
	} else {
		fmt.Printf("--> Binary '%s' is up to date on shared persistent volume (sha256: %s).\n", targetBinPath, localHash[:12])
	}

	// Configure entrypoint for canonical host
	canonicalLauncher := fmt.Sprintf("/opt/apps/%s", app.CanonicalHost())
	if app.Launcher != "" {
		fmt.Printf("--> Writing custom launcher script to %s...\n", canonicalLauncher)
		script := app.Launcher
		script = strings.ReplaceAll(script, "${HOSTNAME}", app.CanonicalHost())
		script = strings.ReplaceAll(script, "${BINARY}", targetBinPath)
		cmd := fmt.Sprintf("cat > '%s' && chmod +x '%s'", canonicalLauncher, canonicalLauncher)
		_, err = ExecPod(pod, namespace, "systemd-runner", []string{"sh", "-c", cmd}, script)
		if err != nil {
			return fmt.Errorf("failed to write custom launcher: %w", err)
		}
	} else {
		// Native direct execution: symlink canonical host directly to binary
		fmt.Printf("--> Symlinking %s -> %s (direct native binary execution, 0 bash latency)...\n", canonicalLauncher, targetBinPath)
		_, err = ExecPod(pod, namespace, "systemd-runner", []string{
			"ln", "-sf", targetBinPath, canonicalLauncher,
		}, "")
		if err != nil {
			return fmt.Errorf("failed to create symlink: %w", err)
		}
	}

	// Configure symlinks for any secondary hostnames
	for _, secondary := range app.SecondaryHosts() {
		secondaryLauncher := fmt.Sprintf("/opt/apps/%s", secondary)
		fmt.Printf("--> Symlinking secondary host %s -> %s...\n", secondaryLauncher, canonicalLauncher)
		_, err = ExecPod(pod, namespace, "systemd-runner", []string{
			"ln", "-sf", canonicalLauncher, secondaryLauncher,
		}, "")
		if err != nil {
			return fmt.Errorf("failed to symlink secondary host: %w", err)
		}
	}

	return nil
}

// DeployConfigFiles templates auxiliary configuration files (replacing ${VAR}) and copies them to /opt/apps/.
func DeployConfigFiles(app *AppConfig, namespace string, envMap map[string]string) error {
	if len(app.ConfigFiles) == 0 {
		return nil
	}

	pod, err := GetGatewayPod(namespace)
	if err != nil {
		return err
	}

	for _, cfPath := range app.ConfigFiles {
		data, err := os.ReadFile(cfPath)
		if err != nil {
			return fmt.Errorf("failed to read config file '%s': %w", cfPath, err)
		}

		content := string(data)

		// Substitute all ${VAR} tokens from envMap
		for k, v := range envMap {
			content = strings.ReplaceAll(content, "${"+k+"}", v)
		}

		// Also provide standard default expansions
		content = strings.ReplaceAll(content, "${HOST}", app.CanonicalHost())
		content = strings.ReplaceAll(content, "${HOSTNAME}", app.CanonicalHost())
		content = strings.ReplaceAll(content, "${PORT}", "8080")
		content = strings.ReplaceAll(content, "${NAMESPACE}", namespace)

		// Smart database fallbacks if specific variables were used in template
		if app.Database != nil {
			engine := strings.ToLower(app.Database.Engine)
			dbPort := "3306"
			dbHost := fmt.Sprintf("shared-mysql.%s.svc.cluster.local", namespace)
			if engine == "postgres" || engine == "postgresql" {
				dbPort = "5432"
				dbHost = fmt.Sprintf("ha-postgres-rw.%s.svc.cluster.local", namespace)
			}
			content = strings.ReplaceAll(content, "${DATABASE_PORT}", dbPort)
			content = strings.ReplaceAll(content, "${DATABASE_HOST}", dbHost)
			if app.Database.EnvAddress != "" {
				content = strings.ReplaceAll(content, "${"+app.Database.EnvAddress+"_PORT}", dbPort)
				content = strings.ReplaceAll(content, "${"+app.Database.EnvAddress+"_HOST}", dbHost)
			}
			if app.Database.EnvHost != "" {
				content = strings.ReplaceAll(content, "${"+app.Database.EnvHost+"_PORT}", dbPort)
			}
		}

		baseName := filepath.Base(cfPath)
		targetPath := fmt.Sprintf("/opt/apps/%s", baseName)

		fmt.Printf("--> Templating and uploading config '%s' -> %s:%s (permissions 0600)...\n", baseName, pod, targetPath)

		// Stream content directly to pod without leaking to local disk or CLI args
		cmd := fmt.Sprintf("cat > '%s' && chmod 600 '%s'", targetPath, targetPath)
		_, err = ExecPod(pod, namespace, "systemd-runner", []string{"sh", "-c", cmd}, content)
		if err != nil {
			return fmt.Errorf("failed to write templated config file to pod: %w", err)
		}
	}

	return nil
}
