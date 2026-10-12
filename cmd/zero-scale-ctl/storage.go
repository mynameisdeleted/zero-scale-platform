package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

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

	// Check if target binary exists on persistent storage volume
	checkOut, _ := ExecPod(pod, namespace, "systemd-runner", []string{
		"sh", "-c", fmt.Sprintf("test -f '%s' && echo 'EXISTS' || echo 'MISSING'", targetBinPath),
	}, "")

	if strings.Contains(checkOut, "MISSING") {
		fmt.Printf("--> Uploading local binary '%s' to %s:%s...\n", app.BinaryPath, pod, targetBinPath)
		err = CpToPod(app.BinaryPath, targetBinPath, pod, namespace, "systemd-runner")
		if err != nil {
			return fmt.Errorf("failed to copy binary to pod: %w", err)
		}
		_, err = ExecPod(pod, namespace, "systemd-runner", []string{"chmod", "+x", targetBinPath}, "")
		if err != nil {
			return fmt.Errorf("failed to chmod binary: %w", err)
		}
		fmt.Printf("--> Successfully deployed and set executable: %s\n", targetBinPath)
	} else {
		fmt.Printf("--> Binary '%s' already exists on shared persistent volume.\n", targetBinPath)
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
