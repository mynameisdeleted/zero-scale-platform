package main

import (
	"flag"
	"fmt"
	"git.fairfaxmedia.net/open-cream-cheese/zero-scale-platform/internal/appname"
	"git.fairfaxmedia.net/open-cream-cheese/zero-scale-platform/internal/platform"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func usage() {
	fmt.Println(`Zero-Scale Platform Management CLI — Serverless systemd on Kubernetes

Usage:
  zero-scale-ctl [command] [options]

Platform setup:
  doctor             Detect HA storage, Postgres/MySQL operators, ingress and gateways (read-only)
  init               Record the chosen platform pieces in the zero-scale-platform ConfigMap
                     [--storage-class X] [--rwx-class Y] [--postgres ns/name|none] [--mysql ns/name|none]
                     [--allow-non-ha] [--dry-run] [--yes]
  install longhorn   Install Longhorn and HA StorageClasses (prints the plan; applies with --yes)
                     [--version V] [--backup-target URL --backup-secret S] [--make-default]
  install postgres   Install CloudNativePG and a 3-instance cluster on the HA storage class
                     [--name ha-postgres] [--namespace cnpg-system] [--instances 3] [--size 10Gi]
                     [--storage-class X] [--cluster-only] [--version V]
                     [--env qa: put the cluster in that environment's namespace and use it only there]
  gateway install    Deploy an HA gateway (replicas, PDB, spread, RBAC, RWX /opt/apps) [--image I] [--replicas 2]
                     [--namespace zero-scale-system] [--rwx-class X] [--volumes-size 20Gi] [--idle-timeout 60s] [--yes]
  gateway upgrade    Same as install, for an existing zero-scale-ctl managed gateway
  gateway status     Replicas, volume and which apps are running on which replica
  ingress add-host   Route a host or wildcard to the gateway: <host|*.domain> [--tls-secret S | --issuer I]
  ingress remove-host <host>
  ingress list

Environments (QA, feature branches) on one cluster:
  env create <name>  Namespace + quota + gateway for an environment; hosts use <app>${ENV_SUFFIX}.<domain>
                     [--ttl 7d] [--namespace N] [--primary] [--replicas 1] [--image I]
                     [--route '*.qa.example.com' --tls-secret S | --issuer I]
                     [--postgres ns/name --postgres-service host:port] [--adopt] [--yes]
  env delete <name>  Drop the env's databases, delete its volumes and namespace [--keep-data] [--force] [--yes]
  env gc             Delete environments past their --ttl [--yes]
  env list

Commands:
  apply              Reconcile an app or redirect from a simple 10-line declarative config file
  oauth-setup <name> Create or update a shared ingress-nginx OAuth2 setup profile
                     [--auth-host auth.apps.example.com: one callback host for every app and environment]
  status <host>      Inspect systemd service status of an app [-n <namespace>]
  logs <host>        Fetch journalctl logs for an app unit [-n <lines>] [--namespace <ns>]
  list-apps          List all registered zero-scale applications
  delete-app <name>  Delete an application ConfigMap; --purge removes its Ingress, Secrets, host entries
                     and OAuth routes and stops it everywhere; --drop-database also drops its database,
                     --delete-volumes its volume data
  upload-binary      Upload a local binary directly to platform storage
  provision-db       Provision schema and credentials on shared database operator

Options for apply:
  -f, --file <file>      Path to 10-line application config file [Required]
  --expand <file>        Also write the expanded Kubernetes YAML to <file> (apply still runs; it is not a dry run)
  -n, --namespace <ns>   Target namespace (default: $NAMESPACE or 'default')

Examples:
  zero-scale-ctl apply -f ./fleetdm.conf
  zero-scale-ctl oauth-setup mdm --url https://git.fairfaxmedia.net --group open-cream-cheese
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

	// status, logs, list-apps and delete-app take -n/--namespace anywhere in their
	// arguments (for logs, a numeric -n is still the line count).
	switch cmd {
	case "status", "logs", "list-apps", "delete-app":
		namespace, os.Args = extractNamespace(os.Args, namespace, cmd == "logs")
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
			fmt.Fprintln(os.Stderr, "Usage: zero-scale-ctl status <host-or-app>")
			os.Exit(1)
		}
		host := resolveAppHost(os.Args[2], namespace)
		unit := fmt.Sprintf("app-%s", host)
		forEachGatewayPod(namespace, func(pod string) {
			fmt.Printf("==> '%s' in pod '%s' (%s)\n", unit, pod, namespace)
			statusCmd := exec.Command("kubectl", "exec", "-n", namespace, pod, "-c", "systemd-runner", "--", "systemctl", "status", "--no-pager", unit)
			statusCmd.Stdout = os.Stdout
			statusCmd.Stderr = os.Stderr
			_ = statusCmd.Run()
		})

	case "logs":
		if len(os.Args) < 3 {
			fmt.Fprintln(os.Stderr, "Usage: zero-scale-ctl logs <host-or-app> [-n <lines>]")
			os.Exit(1)
		}
		host := resolveAppHost(os.Args[2], namespace)
		lines := "50"
		if len(os.Args) >= 5 && (os.Args[3] == "-n" || os.Args[3] == "--lines") {
			lines = os.Args[4]
		}
		unit := fmt.Sprintf("app-%s", host)
		forEachGatewayPod(namespace, func(pod string) {
			fmt.Printf("==> Last %s journal lines for '%s' in pod '%s'\n", lines, unit, pod)
			logCmd := exec.Command("kubectl", "exec", "-n", namespace, pod, "-c", "systemd-runner", "--", "journalctl", "-u", unit, "--no-pager", "-n", lines)
			logCmd.Stdout = os.Stdout
			logCmd.Stderr = os.Stderr
			_ = logCmd.Run()
		})

	case "list-apps":
		fmt.Printf("==> Configured Zero-Scale Applications in namespace '%s':\n", namespace)
		listCmd := exec.Command("kubectl", "get", "configmaps", "-n", namespace, "-l", "zero-scale-app=true",
			"-o", "custom-columns=NAME:.metadata.name,HOST:.data.host,TYPE:.data.type,DB_CONFIG:.data.database_configmap,SCALE_TO_ZERO:.data.scale-to-zero")
		listCmd.Stdout = os.Stdout
		listCmd.Stderr = os.Stderr
		_ = listCmd.Run()

	case "delete-app":
		if err := runDeleteApp(os.Args[2:], namespace); err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
			os.Exit(1)
		}

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

	case "oauth-setup", "auth-setup":
		if len(os.Args) < 3 {
			fmt.Fprintln(os.Stderr, "Usage: zero-scale-ctl oauth-setup <name> --url <oauth-url> [--group <group>] [--host <host>]")
			os.Exit(1)
		}
		setupName := os.Args[2]
		oauthFlags := flag.NewFlagSet("oauth-setup", flag.ExitOnError)
		urlFlag := oauthFlags.String("url", "", "OAuth2 / OIDC provider base URL (e.g. https://git.fairfaxmedia.net)")
		groupFlag := oauthFlags.String("group", "", "Allowed Gitea group(s)/org(s) (comma-separated)")
		hostFlag := oauthFlags.String("host", "", "Optional application hostname(s) to register Redirect URI for")
		nsFlag := oauthFlags.String("n", namespace, "Target namespace")
		authHostFlag := oauthFlags.String("auth-host", "", "Serve every app's login from this one callback host (e.g. auth.apps.example.com), with a cookie for its parent domain; recorded in the platform config")
		_ = oauthFlags.Parse(os.Args[3:])
		if *authHostFlag != "" {
			h := strings.ToLower(strings.TrimSpace(*authHostFlag))
			if err := platform.ValidateHost(h); err != nil || strings.HasPrefix(h, "*.") || strings.Count(h, ".") < 2 {
				fmt.Fprintf(os.Stderr, "ERROR: --auth-host must be a single host with a parent domain, like auth.apps.example.com\n")
				os.Exit(1)
			}
			if err := updatePlatform(func(c *platform.Config) { c.OAuth = platform.OAuthConfig{Host: h, Namespace: *nsFlag} }); err != nil {
				fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
				os.Exit(1)
			}
		}

		var hosts []string
		if *hostFlag != "" {
			for _, h := range strings.Split(*hostFlag, ",") {
				if strings.TrimSpace(h) != "" {
					hosts = append(hosts, strings.TrimSpace(h))
				}
			}
		}
		if err := ReconcileOAuthSetup(setupName, *urlFlag, *groupFlag, hosts, *nsFlag); err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("✅ Shared OAuth2 setup '%s' configured and deployed!\n", setupName)

	case "doctor":
		if err := runDoctor(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
			os.Exit(1)
		}

	case "gateway":
		if err := runGateway(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
			os.Exit(1)
		}

	case "ingress":
		if err := runIngress(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
			os.Exit(1)
		}

	case "env":
		if err := runEnv(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
			os.Exit(1)
		}

	case "install":
		if err := runInstall(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
			os.Exit(1)
		}

	case "init":
		if err := runInit(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
			os.Exit(1)
		}

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
		e, err := loadEnv(namespace)
		if err != nil {
			return err
		}
		redir.SourceHost, redir.TargetHost = e.Expand(redir.SourceHost), e.Expand(redir.TargetHost)
		if err := e.CheckHost(redir.SourceHost); err != nil {
			return err
		}
		fmt.Printf("==> Applying Edge NGINX Redirect: https://%s -> https://%s...\n", redir.SourceHost, redir.TargetHost)
		ingressName := fmt.Sprintf("redirect-%s", redir.SourceHost)
		ingressName = strings.ReplaceAll(ingressName, ".", "-")
		err = ApplyRedirectIngress(ingressName, namespace, redir.SourceHost, redir.TargetHost, "zero-scale-gateway")
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

	if err := bindEnv(app, namespace); err != nil {
		return err
	}
	fmt.Printf("==> Reconciling Zero-Scale Application '%s' for host '%s'...\n", app.AppName(), app.CanonicalHost())

	// Step A: Provision database schema, user, and secrets
	if app.Database != nil || app.Redis != nil || len(app.Secrets) > 0 {
		fmt.Println("--> Step 1: Checking and provisioning shared data layer & secrets...")
		err = ProvisionDataLayer(app, namespace)
		if err != nil {
			return fmt.Errorf("data layer/secrets provisioning failed: %w", err)
		}
	} else {
		fmt.Println("--> Step 1: No database or secrets configured. Skipping data layer.")
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
		if app.Database != nil || len(app.Secrets) > 0 {
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
		"hosts":         strings.Join(app.Hostnames, ","),
		"url":           fmt.Sprintf("local://%s", app.AppName()),
		"type":          "elf",
		"scale-to-zero": "true",
	}
	if !app.ScaleToZero {
		cmData["scale-to-zero"] = "false"
	}
	if app.IdleTimeout != "" {
		cmData["idle-timeout"] = app.IdleTimeout
	}
	if app.Database != nil {
		cmData["database_engine"] = strings.ToLower(app.Database.Engine)
		cmData["database_configmap"] = app.ConfigMapName()
		cmData["database_secret"] = app.SecretName()
	} else if len(app.Secrets) > 0 {
		cmData["database_secret"] = app.SecretName()
	}
	if app.Mounts != "" {
		cmData["volumes"] = app.Mounts
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
	if oauthSetup := app.OAuthSetupName(); oauthSetup != "" {
		cmData["oauth-setup"] = oauthSetup
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
			if app.Database != nil || len(app.Secrets) > 0 {
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

	appLabels := map[string]string{
		"zero-scale-app": "true",
		"app":            app.AppName(),
	}
	if oauthSetup := app.OAuthSetupName(); oauthSetup != "" {
		appLabels["zero-scale-oauth-setup"] = oauthSetup
	}
	err = CreateOrUpdateConfigMap(fmt.Sprintf("zero-scale-app-%s", app.AppName()), namespace, cmData, appLabels)
	if err != nil {
		return fmt.Errorf("failed to register app configmap: %w", err)
	}

	// Step C.5: Reconcile shared ingress-nginx OAuth2 setup if configured in gateway section
	oauthSetup := app.OAuthSetupName()
	if oauthSetup != "" {
		fmt.Printf("--> Step 3.5: Reconciling shared ingress-nginx OAuth2 setup '%s'...\n", oauthSetup)
		if err := ReconcileOAuthSetup(oauthSetup, app.Gateway.OAuthURL, app.Gateway.OAuthGroup, app.Hostnames, namespace); err != nil {
			return fmt.Errorf("oauth-setup reconciliation failed: %w", err)
		}
	}

	// Step D: Configure public Ingress, unless the gateway's routed hosts (ingress add-host,
	// e.g. a wildcard) already cover every hostname and no OAuth annotations are needed.
	ingressName := fmt.Sprintf("zero-scale-%s", app.AppName())
	if covered := hostsCoveredByRoutes(app.Hostnames, namespace); covered && oauthSetup == "" {
		fmt.Printf("--> Step 4: %v already routed to the gateway by platform ingress hosts; no per-app Ingress needed.\n", app.Hostnames)
		_, _ = RunKubectl([]string{"delete", "ingress", ingressName, "-n", namespace, "--ignore-not-found"}, "")
	} else {
		fmt.Printf("--> Step 4: Configuring public Ingress for %v...\n", app.Hostnames)
		err = ApplyIngress(ingressName, namespace, "zero-scale-gateway", 80, app.Hostnames, oauthSetup)
		if err != nil {
			return fmt.Errorf("failed to apply ingress: %w", err)
		}
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
	if oauthSetup != "" {
		fmt.Printf("🔐 Protected by shared OAuth2 setup: '%s' (%s)\n", oauthSetup, app.Gateway.OAuthURL)
	}
	fmt.Println("==============================================================================")
	return nil
}

// extractNamespace removes -n/--namespace <ns> (or --namespace=ns) from args and
// returns the namespace. When numericN is set, "-n <digits>" is left in place.
func extractNamespace(args []string, namespace string, numericN bool) (string, []string) {
	out := []string{}
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case strings.HasPrefix(a, "--namespace="):
			namespace = strings.TrimPrefix(a, "--namespace=")
			continue
		case (a == "-n" || a == "--namespace") && i+1 < len(args):
			v := args[i+1]
			if a == "-n" && numericN && isDigits(v) {
				break
			}
			namespace = v
			i++
			continue
		}
		out = append(out, a)
	}
	return namespace, out
}

func isDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

func resolveAppHost(hostOrApp, namespace string) string {
	hostOrApp = strings.TrimSpace(hostOrApp)
	if strings.Contains(hostOrApp, ".") {
		return hostOrApp
	}
	if cm, err := GetConfigMapData(appname.ConfigMapName(hostOrApp), namespace); err == nil {
		if h := strings.TrimSpace(cm["host"]); h != "" {
			return h
		}
	}
	return hostOrApp
}
