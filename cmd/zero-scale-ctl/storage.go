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
