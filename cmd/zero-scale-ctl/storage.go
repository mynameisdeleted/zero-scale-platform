package main

import (
	"crypto/sha256"
	"debug/elf"
	"encoding/binary"
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

// ValidateBinaryArchitecture verifies that the binary is compatible with the Linux x86-64 gateway container.
// It detects macOS Mach-O, Windows PE, and mismatched Linux ELF architectures (e.g. ARM64) before deployment.
func ValidateBinaryArchitecture(path string) error {
	if os.Getenv("ZERO_SCALE_SKIP_ARCH_CHECK") == "1" || os.Getenv("ZERO_SCALE_SKIP_ARCH_CHECK") == "true" {
		return nil
	}

	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("failed to open binary '%s': %w", path, err)
	}
	defer f.Close()

	header := make([]byte, 32)
	n, _ := io.ReadFull(f, header)
	if n < 4 {
		return nil
	}

	// 1. Script with shebang (e.g. #!/bin/sh)
	if header[0] == '#' && header[1] == '!' {
		return nil
	}

	// 2. WebAssembly binary (\x00asm)
	if header[0] == 0x00 && header[1] == 0x61 && header[2] == 0x73 && header[3] == 0x6d {
		return nil
	}

	// 3. macOS Mach-O binary
	// Magic numbers: 0xfeedface (32-bit), 0xfeedfacf (64-bit), 0xcafebabe (Fat/Universal)
	magicBE := binary.BigEndian.Uint32(header[0:4])
	magicLE := binary.LittleEndian.Uint32(header[0:4])
	if magicBE == 0xfeedface || magicBE == 0xfeedfacf || magicBE == 0xcafebabe ||
		magicLE == 0xfeedface || magicLE == 0xfeedfacf || magicLE == 0xcafebabe {
		return fmt.Errorf("binary '%s' is a macOS (Mach-O) binary.\nThe zero-scale gateway container runs Linux x86-64 (amd64) and cannot execute macOS binaries (will fail with 'exec format error').\n--> To cross-compile for Linux x86-64, run:\n    GOOS=linux GOARCH=amd64 go build -o %s ...",
			path, filepath.Base(path))
	}

	// 4. Windows PE executable ("MZ")
	if header[0] == 'M' && header[1] == 'Z' {
		return fmt.Errorf("binary '%s' is a Windows (PE) executable.\nThe zero-scale gateway container runs Linux x86-64 (amd64) and cannot execute Windows binaries.\n--> To cross-compile for Linux x86-64, run:\n    GOOS=linux GOARCH=amd64 go build -o %s ...",
			path, filepath.Base(path))
	}

	// 5. Linux ELF binary
	if header[0] == 0x7f && header[1] == 'E' && header[2] == 'L' && header[3] == 'F' {
		if n >= 20 {
			var machine uint16
			if header[5] == 2 { // Big-endian
				machine = binary.BigEndian.Uint16(header[18:20])
			} else { // Little-endian
				machine = binary.LittleEndian.Uint16(header[18:20])
			}
			if elf.Machine(machine) != elf.EM_X86_64 {
				archName := elf.Machine(machine).String()
				if elf.Machine(machine) == elf.EM_AARCH64 {
					archName = "Linux ARM64 (aarch64)"
				}
				return fmt.Errorf("binary '%s' is compiled for %s, but the zero-scale gateway container runs Linux x86-64 (amd64).\nDeploying this binary will cause container startup failure with 'exec format error'.\n--> To cross-compile for Linux x86-64, run:\n    GOOS=linux GOARCH=amd64 go build -o %s ...",
					path, archName, filepath.Base(path))
			}
			return nil
		}
	}

	return nil
}

// DeployBinary ensures local binary is uploaded to gateway pod /opt/apps/ and symlinks are configured.
func DeployBinary(app *AppConfig, namespace string) error {
	if _, err := os.Stat(app.BinaryPath); os.IsNotExist(err) {
		return fmt.Errorf("local binary not found at '%s'", app.BinaryPath)
	}

	if err := ValidateBinaryArchitecture(app.BinaryPath); err != nil {
		return err
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
			return fmt.Errorf("failed to copy binary to pod: %w\n--> Ensure the storage volume for /opt/apps (zero-scale-binaries-pvc) is bound and mounted in the gateway pod.\n--> Documentation: https://git.fairfaxmedia.net/open-cream-cheese/zero-scale-platform#persistent-storage-and-volumes", err)
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

		// Database fallbacks come from the provisioned <app>-db-config values, so they
		// match whichever cluster the platform config points at.
		if app.Database != nil {
			dbHost, dbPort := envMap["DATABASE_HOST"], envMap["DATABASE_PORT"]
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
