package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func usage() {
	fmt.Println(`Zero-Scale Platform Management CLI — Serverless systemd on Kubernetes

Usage:
  zero-scale-ctl [command] [options]

Commands:
  apply              Reconcile an app or redirect from a simple 10-line declarative config file
  status <host>      Inspect systemd service status of an app
  logs <host>        Fetch journalctl logs for an app unit
  list-apps          List all registered zero-scale applications
  delete-app <name>  Delete an application ConfigMap
  upload-binary      Upload a local binary directly to platform storage
  provision-db       Provision schema and credentials on shared database operator

Options for apply:
  -f, --file <file>      Path to 10-line application config file [Required]
  --expand <file>        Optional: output fully expanded Kubernetes YAML manifest
  -n, --namespace <ns>   Target namespace (default: $NAMESPACE or 'default')

Examples:
  zero-scale-ctl apply -f ./fleetdm.conf
  zero-scale-ctl apply -f ./redirect.conf
  zero-scale-ctl status fdm.ffxm.cc
  zero-scale-ctl logs fdm.ffxm.cc -n 50`)
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(0)
	}

	cmd := os.Args[1]
	namespace := os.Getenv("NAMESPACE")
	if namespace == "" {
		namespace = "default"
	}

	switch cmd {
	case "apply":
		applyFlags := flag.NewFlagSet("apply", flag.ExitOnError)
		fileFlag := applyFlags.String("f", "", "Path to config file")
		fileLongFlag := applyFlags.String("file", "", "Path to config file")
		expandFlag := applyFlags.String("expand", "", "Path to output expanded YAML")
		nsFlag := applyFlags.String("n", namespace, "Target namespace")
		nsLongFlag := applyFlags.String("namespace", "", "Target namespace")
		_ = applyFlags.Parse(os.Args[2:])

		targetFile := *fileFlag
		if targetFile == "" {
			targetFile = *fileLongFlag
		}
		if targetFile == "" && applyFlags.NArg() > 0 {
			targetFile = applyFlags.Arg(0)
		}
		if targetFile == "" {
			fmt.Fprintln(os.Stderr, "Error: Config file path is required. Usage: zero-scale-ctl apply -f <config-file>")
			os.Exit(1)
		}

		targetNS := *nsFlag
		if *nsLongFlag != "" {
			targetNS = *nsLongFlag
		}

		err := handleApply(targetFile, *expandFlag, targetNS)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
			os.Exit(1)
		}

	case "status":
		if len(os.Args) < 3 {
			fmt.Fprintln(os.Stderr, "Usage: zero-scale-ctl status <host>")
			os.Exit(1)
		}
		host := os.Args[2]
		pod, err := GetGatewayPod(namespace)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
			os.Exit(1)
		}
		unit := fmt.Sprintf("app-%s", host)
		fmt.Printf("==> Checking status of '%s' in pod '%s' (%s)...\n", unit, pod, namespace)
		statusCmd := exec.Command("kubectl", "exec", "-n", namespace, pod, "-c", "systemd-runner", "--", "systemctl", "status", unit)
		statusCmd.Stdout = os.Stdout
		statusCmd.Stderr = os.Stderr
		_ = statusCmd.Run()

	case "logs":
		if len(os.Args) < 3 {
			fmt.Fprintln(os.Stderr, "Usage: zero-scale-ctl logs <host> [-n <lines>]")
			os.Exit(1)
		}
		host := os.Args[2]
		lines := "50"
		if len(os.Args) >= 5 && (os.Args[3] == "-n" || os.Args[3] == "--lines") {
			lines = os.Args[4]
		}
		pod, err := GetGatewayPod(namespace)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
			os.Exit(1)
		}
		unit := fmt.Sprintf("app-%s", host)
		fmt.Printf("==> Fetching last %s journal logs for '%s' in pod '%s'...\n", lines, unit, pod)
		logCmd := exec.Command("kubectl", "exec", "-n", namespace, pod, "-c", "systemd-runner", "--", "journalctl", "-u", unit, "--no-pager", "-n", lines)
		logCmd.Stdout = os.Stdout
		logCmd.Stderr = os.Stderr
		_ = logCmd.Run()

	case "list-apps":
		fmt.Printf("==> Configured Zero-Scale Applications in namespace '%s':\n", namespace)
		listCmd := exec.Command("kubectl", "get", "configmaps", "-n", namespace, "-l", "zero-scale-app=true",
			"-o", "custom-columns=NAME:.metadata.name,HOST:.data.host,TYPE:.data.type,DB_CONFIG:.data.database_configmap,SCALE_TO_ZERO:.data.scale-to-zero")
		listCmd.Stdout = os.Stdout
		listCmd.Stderr = os.Stderr
		_ = listCmd.Run()

	case "delete-app":
		if len(os.Args) < 3 {
			fmt.Fprintln(os.Stderr, "Usage: zero-scale-ctl delete-app <name>")
			os.Exit(1)
		}
		name := os.Args[2]
		cmName := name
		if !strings.HasPrefix(cmName, "zero-scale-app-") {
			cmName = "zero-scale-app-" + name
		}
		fmt.Printf("==> Deleting ConfigMap '%s' in namespace '%s'...\n", cmName, namespace)
		_, err := RunKubectl([]string{"delete", "configmap", cmName, "-n", namespace}, "")
		if err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("Done.")

	case "upload-binary":
		upFlags := flag.NewFlagSet("upload-binary", flag.ExitOnError)
		binFlag := upFlags.String("binary", "", "Path to local binary")
		hostFlag := upFlags.String("host", "", "Hostname to map")
		nameFlag := upFlags.String("name", "", "Destination name")
		_ = upFlags.Parse(os.Args[2:])

		if *binFlag == "" {
			fmt.Fprintln(os.Stderr, "Error: --binary is required")
			os.Exit(1)
		}
		targetHost := *hostFlag
		if targetHost == "" {
			targetHost = *nameFlag
		}
		dummy := &AppConfig{
			Hostnames:   []string{targetHost},
			BinaryPath:  *binFlag,
			ScaleToZero: true,
		}
		err := DeployBinary(dummy, namespace)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("==> Binary upload complete.")

	case "provision-db":
		dbFlags := flag.NewFlagSet("provision-db", flag.ExitOnError)
		appFlag := dbFlags.String("app", "", "Application name")
		engineFlag := dbFlags.String("engine", "mysql", "Database engine")
		hostFlag := dbFlags.String("host", "", "Hostname")
		_ = dbFlags.Parse(os.Args[2:])

		if *appFlag == "" {
			fmt.Fprintln(os.Stderr, "Error: --app is required")
			os.Exit(1)
		}
		targetHost := *hostFlag
		if targetHost == "" {
			targetHost = fmt.Sprintf("%s.ffxm.cc", *appFlag)
		}
		dummy := &AppConfig{
			Hostnames:  []string{targetHost},
			BinaryPath: "",
			Database: &DatabaseConfig{
				Engine:      *engineFlag,
				EnvAddress:  "DATABASE_URL",
				EnvDatabase: "DATABASE_NAME",
				EnvUser:     "DATABASE_USER",
				EnvPassword: "DATABASE_PASSWORD",
			},
			ScaleToZero: true,
		}
		err := ProvisionDataLayer(dummy, namespace)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("==> Database provisioned for '%s' on engine '%s'.\n", *appFlag, *engineFlag)

	case "--help", "-h", "help":
		usage()

	default:
		fmt.Fprintf(os.Stderr, "Unknown command '%s'. Run 'zero-scale-ctl help' for available commands.\n", cmd)
		os.Exit(1)
	}
}

func handleApply(configPath, expandPath, namespace string) error {
	cfg, err := LoadConfig(configPath)
	if err != nil {
		return err
	}

	// 1. Edge Redirect Ingress Mode
	if redir, ok := cfg.(*RedirectConfig); ok {
		fmt.Printf("==> Applying Edge NGINX Redirect: https://%s -> https://%s...\n", redir.SourceHost, redir.TargetHost)
		ingressName := fmt.Sprintf("redirect-%s", redir.SourceHost)
		ingressName = strings.ReplaceAll(ingressName, ".", "-")
		err := ApplyRedirectIngress(ingressName, namespace, redir.SourceHost, redir.TargetHost, "zero-scale-gateway")
		if err != nil {
			return fmt.Errorf("failed to apply redirect ingress: %w", err)
		}
		fmt.Println("✅ Edge redirect Ingress applied. Traffic will 301-redirect at the edge with 0 RAM overhead.")
		return nil
	}

	// 2. Zero-Scale Application Mode
	app, ok := cfg.(*AppConfig)
	if !ok {
		return fmt.Errorf("unsupported configuration type")
	}

	fmt.Printf("==> Reconciling Zero-Scale Application '%s' for host '%s'...\n", app.AppName(), app.CanonicalHost())

	// Step A: Provision database schema, user, and secrets
	if app.Database != nil || app.Redis != nil {
		fmt.Println("--> Step 1: Checking and provisioning shared data layer & secrets...")
		err = ProvisionDataLayer(app, namespace)
		if err != nil {
			return fmt.Errorf("data layer provisioning failed: %w", err)
		}
	} else {
		fmt.Println("--> Step 1: No database configured. Skipping data layer.")
	}

	// Step B: Upload local binary directly to persistent volume & configure launchers
	fmt.Printf("--> Step 2: Checking binary storage volume for '%s'...\n", app.BinaryPath)
	err = DeployBinary(app, namespace)
	if err != nil {
		return fmt.Errorf("binary deployment failed: %w", err)
	}

	// Step B.5: Template auxiliary config files (${VAR} expansion with secrets) and upload to /opt/apps/
	if len(app.ConfigFiles) > 0 {
		fmt.Printf("--> Step 2.5: Templating and deploying %d auxiliary config file(s)...\n", len(app.ConfigFiles))
		envMap := make(map[string]string)
		if app.Database != nil {
			if dbCM, err := GetConfigMapData(app.ConfigMapName(), namespace); err == nil {
				for k, v := range dbCM {
					envMap[k] = v
				}
			}
			if dbSec, err := GetSecretData(app.SecretName(), namespace); err == nil {
				for k, v := range dbSec {
					envMap[k] = v
				}
			}
		}
		for k, v := range app.Env {
			envMap[k] = v
		}

		err = DeployConfigFiles(app, namespace, envMap)
		if err != nil {
			return fmt.Errorf("config file deployment failed: %w", err)
		}
	}

	// Step C: Create or update zero-scale-app-<name> ConfigMap
	fmt.Printf("--> Step 3: Registering zero-scale-app-%s with gateway...\n", app.AppName())
	cmData := map[string]string{
		"host":          app.CanonicalHost(),
		"url":           fmt.Sprintf("local://%s", app.AppName()),
		"type":          "elf",
		"scale-to-zero": "true",
	}
	if !app.ScaleToZero {
		cmData["scale-to-zero"] = "false"
	}
	if app.Database != nil {
		cmData["database_configmap"] = app.ConfigMapName()
		cmData["database_secret"] = app.SecretName()
	}
	if app.Mounts != "" {
		cmData["mounts"] = app.Mounts
	}
	if app.Args != "" {
		cmData["args"] = app.Args
	}
	if !app.DynamicUser {
		cmData["dynamic-user"] = "false"
	}
	if app.User != "" {
		cmData["user"] = app.User
	}
	for k, v := range app.Env {
		cmData[k] = v
	}

	// Step C.1: Execute one-time initialization task during apply if specified (e.g. database migrations)
	initCmd := app.InitCmd
	if initCmd == "" {
		initCmd = app.PreStart
	}
	if initCmd != "" {
		fmt.Printf("--> Running initialization command (init_cmd): %s...\n", initCmd)
		pod, err := GetGatewayPod(namespace)
		if err == nil {
			binBase := filepath.Base(app.BinaryPath)
			targetBinPath := fmt.Sprintf("/opt/apps/%s", binBase)

			// Collect all app environment variables to export for the init command
			var envExports []string
			if app.Database != nil {
				if dbCM, err := GetConfigMapData(app.ConfigMapName(), namespace); err == nil {
					for k, v := range dbCM {
						envExports = append(envExports, fmt.Sprintf("export %s=%q", k, v))
					}
				}
				if dbSec, err := GetSecretData(app.SecretName(), namespace); err == nil {
					for k, v := range dbSec {
						envExports = append(envExports, fmt.Sprintf("export %s=%q", k, v))
					}
				}
			}
			for k, v := range app.Env {
				v = strings.ReplaceAll(v, "${PORT}", "8080")
				v = strings.ReplaceAll(v, "${HOST}", app.CanonicalHost())
				envExports = append(envExports, fmt.Sprintf("export %s=%q", k, v))
			}

			fullCmd := strings.ReplaceAll(initCmd, "${BINARY}", targetBinPath)
			fullCmd = strings.ReplaceAll(fullCmd, "${HOSTNAME}", app.CanonicalHost())
			if len(envExports) > 0 {
				fullCmd = fmt.Sprintf("%s && %s", strings.Join(envExports, " && "), fullCmd)
			}

			out, err := ExecPod(pod, namespace, "systemd-runner", []string{"sh", "-c", fullCmd}, "")
			if err != nil {
				fmt.Printf("⚠️ Init command note: %v\n%s\n", err, out)
			} else {
				fmt.Printf("--> Initialization completed successfully:\n%s\n", strings.TrimSpace(out))
			}
		}
	}

	err = CreateOrUpdateConfigMap(fmt.Sprintf("zero-scale-app-%s", app.AppName()), namespace, cmData, map[string]string{
		"zero-scale-app": "true",
		"app":            app.AppName(),
	})
	if err != nil {
		return fmt.Errorf("failed to register app configmap: %w", err)
	}

	// Step D: Configure public Ingress for all hostnames
	fmt.Printf("--> Step 4: Configuring public Ingress for %v...\n", app.Hostnames)
	ingressName := fmt.Sprintf("zero-scale-%s", app.AppName())
	err = ApplyIngress(ingressName, namespace, "zero-scale-gateway", 80, app.Hostnames)
	if err != nil {
		return fmt.Errorf("failed to apply ingress: %w", err)
	}

	// Step E: Optionally write out expanded single-file Kubernetes manifest
	if expandPath != "" {
		fmt.Printf("--> Writing fully expanded Kubernetes manifest to '%s'...\n", expandPath)
		expandedYAML := fmt.Sprintf(`# Auto-generated Zero-Scale Manifest for %s
apiVersion: v1
kind: ConfigMap
metadata:
  name: zero-scale-app-%s
  namespace: %s
  labels:
    zero-scale-app: "true"
data:
  host: %q
  url: "local://%s"
  type: "elf"
  scale-to-zero: %q
`, app.AppName(), app.AppName(), namespace, app.CanonicalHost(), app.AppName(), cmData["scale-to-zero"])

		if app.Database != nil {
			expandedYAML += fmt.Sprintf("  database_configmap: %q\n  database_secret: %q\n", app.ConfigMapName(), app.SecretName())
		}
		_ = os.WriteFile(expandPath, []byte(expandedYAML), 0644)
	}

	fmt.Println("")
	fmt.Println("==============================================================================")
	fmt.Printf("✅ Application '%s' is ready for zero-scale execution!\n", app.AppName())
	fmt.Printf("🔗 Primary Endpoint: https://%s\n", app.CanonicalHost())
	for _, sec := range app.SecondaryHosts() {
		fmt.Printf("🔗 Secondary Endpoint: https://%s\n", sec)
	}
	fmt.Println("==============================================================================")
	return nil
}
