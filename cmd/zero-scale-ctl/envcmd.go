package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"git.fairfaxmedia.net/open-cream-cheese/zero-scale-platform/internal/appname"
	"git.fairfaxmedia.net/open-cream-cheese/zero-scale-platform/internal/env"
	"git.fairfaxmedia.net/open-cream-cheese/zero-scale-platform/internal/platform"
)

// now is swapped in tests.
var now = time.Now

func runEnv(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: zero-scale-ctl env create|delete|gc|list")
	}
	switch args[0] {
	case "create":
		return envCreate(args[1:])
	case "delete":
		return envDeleteCmd(args[1:])
	case "gc":
		return envGC(args[1:])
	case "list", "ls":
		return envList()
	}
	return fmt.Errorf("unknown env command %q (create, delete, gc, list)", args[0])
}

type nsMeta struct {
	Metadata struct {
		Name        string            `json:"name"`
		Labels      map[string]string `json:"labels"`
		Annotations map[string]string `json:"annotations"`
	} `json:"metadata"`
}

// listEnvNamespaces returns the namespaces labelled as zero-scale environments.
func listEnvNamespaces(selector string) ([]nsMeta, error) {
	out, err := RunKubectl([]string{"get", "namespace", "-l", selector, "-o", "json"}, "")
	if err != nil {
		return nil, err
	}
	var list struct{ Items []nsMeta }
	if err := json.Unmarshal([]byte(out), &list); err != nil {
		return nil, fmt.Errorf("parsing namespaces: %w", err)
	}
	return list.Items, nil
}

func getNamespace(name string) (*nsMeta, error) {
	out, err := RunKubectl([]string{"get", "namespace", name, "-o", "json"}, "")
	if err != nil {
		if isNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	var ns nsMeta
	if err := json.Unmarshal([]byte(out), &ns); err != nil {
		return nil, err
	}
	return &ns, nil
}

// currentGatewayImage is the image the platform's gateway runs, so new environments match it.
func currentGatewayImage(p *platform.Config) string {
	if p == nil || p.Gateway.Namespace == "" {
		return ""
	}
	out, err := RunKubectl([]string{"get", "deployment", "-n", p.Gateway.Namespace, "-l", "app=zero-scale-gateway",
		"-o", "jsonpath={.items[0].spec.template.spec.containers[0].image}"}, "")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

func envCreate(args []string) error {
	name, rest := splitHostArgs(args)
	fs := flag.NewFlagSet("env create", flag.ExitOnError)
	nsFlag := fs.String("namespace", "", "Namespace for the environment (default: the env name)")
	ttl := fs.String("ttl", "", "Delete with 'env gc' after this long, e.g. 7d or 36h (default: never)")
	primary := fs.Bool("primary", false, "Mark as the primary environment (prod): unsuffixed hosts and the original database names")
	adopt := fs.Bool("adopt", false, "Allow an existing namespace that is not yet a zero-scale environment")
	image := fs.String("image", "", "Gateway image (default: the image the platform gateway runs)")
	replicas := fs.Int("replicas", 1, "Gateway replicas (use 2 or more for long-lived environments)")
	route := fs.String("route", "", "Optional wildcard or host routed to this env's gateway, e.g. '*.qa.apps.example.com'")
	secret := fs.String("tls-secret", "", "TLS Secret in the env namespace for --route")
	issuer := fs.String("issuer", "", "cert-manager ClusterIssuer for --route (DNS-01 for a wildcard)")
	pgCluster := fs.String("postgres", "", "Use this Postgres cluster (<namespace>/<name>) instead of the platform's")
	pgService := fs.String("postgres-service", "", "host:port of --postgres's read-write service")
	pgOperator := fs.String("postgres-operator", "cnpg", "Operator of --postgres")
	quotaPods := fs.Int("quota-pods", env.DefaultQuota.Pods, "ResourceQuota: max pods")
	quotaStorage := fs.String("quota-storage", env.DefaultQuota.Storage, "ResourceQuota: max requested storage")
	yes := fs.Bool("yes", false, "Apply (otherwise only print the plan)")
	_ = fs.Parse(rest)

	if err := env.ValidateName(name); err != nil {
		return fmt.Errorf("usage: zero-scale-ctl env create <name> [--ttl 7d] [--primary] ...: %w", err)
	}
	ns := *nsFlag
	if ns == "" {
		ns = name
	}
	if *primary && *ttl != "" {
		return fmt.Errorf("the primary environment cannot have a TTL")
	}
	d, err := env.ParseTTL(*ttl)
	if err != nil {
		return err
	}
	if (*pgCluster == "") != (*pgService == "") {
		return fmt.Errorf("--postgres and --postgres-service go together")
	}

	// Refuse to take over a namespace that belongs to something else or to another env.
	existing, err := getNamespace(ns)
	if err != nil {
		return err
	}
	if existing != nil {
		if owner := existing.Metadata.Labels[env.Label]; owner != "" && owner != name {
			return fmt.Errorf("namespace %s already belongs to environment %q", ns, owner)
		} else if owner == "" && !*adopt {
			return fmt.Errorf("namespace %s already exists and is not a zero-scale environment; pass --adopt to turn it into one", ns)
		}
	}
	others, err := listEnvNamespaces(env.Label + "=" + name)
	if err != nil {
		return err
	}
	for _, o := range others {
		if o.Metadata.Name != ns {
			return fmt.Errorf("environment %q already exists in namespace %s", name, o.Metadata.Name)
		}
	}

	p, err := loadPlatform()
	if err != nil {
		return err
	}
	if p == nil {
		return fmt.Errorf("no platform config yet; run 'zero-scale-ctl init' first so environments share the same storage and databases")
	}

	cfg := &env.Config{Name: name, Primary: *primary, Created: now().UTC().Truncate(time.Second), Overrides: map[string]string{}}
	if d > 0 {
		cfg.Expires = cfg.Created.Add(d)
	}
	if *pgCluster != "" {
		cfg.Overrides["postgres.operator"] = *pgOperator
		cfg.Overrides["postgres.cluster"] = *pgCluster
		cfg.Overrides["postgres.rwService"] = *pgService
	}
	var routeManifest string
	if *route != "" {
		host := strings.ToLower(strings.TrimSpace(*route))
		tls, err := resolveRouteTLS(host, *secret, *issuer)
		if err != nil {
			return err
		}
		if routeManifest, err = platform.RouteManifest(host, ns, "zero-scale-gateway", resolveIngressClass(), tls); err != nil {
			return err
		}
		cfg.Overrides["ingress.hosts"] = host
		if tls != "" {
			cfg.Overrides["ingress.tls"] = host + "=" + tls
		}
	}

	steps := []platform.Step{{
		Title:    fmt.Sprintf("Namespace %s (labelled %s=%s), ResourceQuota and %s ConfigMap", ns, env.Label, name, env.ConfigMapName),
		Manifest: cfg.Manifest(ns, env.Quota{Pods: *quotaPods, PVCs: env.DefaultQuota.PVCs, Storage: *quotaStorage}),
	}}
	if manager, exists := existingGatewayManager(ns); exists {
		fmt.Printf("==> A gateway (managed by %s) already runs in %s; keeping it.\n", manager, ns)
	} else {
		img := *image
		if img == "" {
			img = currentGatewayImage(p)
		}
		gw, err := platform.GatewayManifest(platform.GatewayOptions{Namespace: ns, Image: img, Replicas: *replicas, RWXClass: p.Storage.RWXClass})
		if err != nil {
			return err
		}
		steps = append(steps,
			platform.Step{Title: fmt.Sprintf("Gateway in %s (%d replica(s), image %s)", ns, *replicas, img), Manifest: gw},
			platform.Step{Title: "Wait for the rollout", Command: []string{"kubectl", "-n", ns, "rollout", "status", "deployment/zero-scale-gateway", "--timeout=5m"}})
	}
	if routeManifest != "" {
		steps = append(steps, platform.Step{Title: "Route " + *route + " to this environment's gateway", Manifest: routeManifest})
	}

	fmt.Printf("==> Environment %q in namespace %s\n", name, ns)
	if cfg.Scoped() {
		fmt.Printf("    hosts: write them as <app>${ENV_SUFFIX}.<domain>; here that is <app>%s.<domain>\n", cfg.Suffix())
		fmt.Printf("    databases: db_%s_<app>_<hash> on %s\n", strings.ReplaceAll(name, "-", "_"), map[bool]string{true: *pgCluster, false: "the platform's clusters"}[*pgCluster != ""])
	}
	if !cfg.Expires.IsZero() {
		fmt.Printf("    expires: %s ('zero-scale-ctl env gc' deletes it after that)\n", cfg.Expires.Format(time.RFC3339))
	}
	if err := executePlan(steps, *yes); err != nil || !*yes {
		return err
	}
	fmt.Printf("✅ Environment %q is ready. Deploy into it with: zero-scale-ctl apply -f app.conf -n %s\n", name, ns)
	return nil
}

func envDeleteCmd(args []string) error {
	name, rest := splitHostArgs(args)
	fs := flag.NewFlagSet("env delete", flag.ExitOnError)
	keepData := fs.Bool("keep-data", false, "Keep the databases and volumes (only delete the namespace)")
	force := fs.Bool("force", false, "Delete the namespace even if dropping a database fails")
	yes := fs.Bool("yes", false, "Apply (otherwise only print the plan)")
	_ = fs.Parse(rest)
	if err := env.ValidateName(name); err != nil {
		return fmt.Errorf("usage: zero-scale-ctl env delete <name> [--keep-data] [--yes]: %w", err)
	}
	return envDelete(name, *keepData, *force, *yes)
}

type appEntry struct {
	Name string
	Data map[string]string
}

func listApps(namespace string) ([]appEntry, error) {
	out, err := RunKubectl([]string{"get", "configmap", "-n", namespace, "-l", "zero-scale-app=true", "-o", "json"}, "")
	if err != nil {
		return nil, err
	}
	var list struct {
		Items []struct {
			Metadata struct{ Name string } `json:"metadata"`
			Data     map[string]string     `json:"data"`
		}
	}
	if err := json.Unmarshal([]byte(out), &list); err != nil {
		return nil, err
	}
	var apps []appEntry
	for _, it := range list.Items {
		if strings.HasPrefix(it.Metadata.Name, "zero-scale-app-") {
			apps = append(apps, appEntry{strings.TrimPrefix(it.Metadata.Name, "zero-scale-app-"), it.Data})
		}
	}
	return apps, nil
}

// pvNames returns the PersistentVolumes bound to the namespace's claims.
func pvNames(namespace string) ([]string, error) {
	out, err := RunKubectl([]string{"get", "pvc", "-n", namespace, "-o", "jsonpath={.items[*].spec.volumeName}"}, "")
	if err != nil {
		return nil, err
	}
	return strings.Fields(out), nil
}

func envDelete(name string, keepData, force, yes bool) error {
	matches, err := listEnvNamespaces(env.Label + "=" + name)
	if err != nil {
		return err
	}
	if len(matches) != 1 {
		return fmt.Errorf("found %d namespaces labelled %s=%s; expected exactly one", len(matches), env.Label, name)
	}
	ns := matches[0].Metadata.Name
	if ns == platformNamespace() || ns == "default" || strings.HasPrefix(ns, "kube-") {
		return fmt.Errorf("refusing to delete namespace %s", ns)
	}
	e, err := loadEnv(ns)
	if err != nil {
		return err
	}
	if e == nil || e.Name != name {
		return fmt.Errorf("namespace %s has no %s ConfigMap for %q; delete it by hand if that is intended", ns, env.ConfigMapName, name)
	}
	if e.Primary {
		return fmt.Errorf("%q is the primary environment; zero-scale-ctl will not delete it", name)
	}

	apps, err := listApps(ns)
	if err != nil {
		return err
	}
	var withDB []appEntry
	for _, a := range apps {
		if a.Data["database_engine"] != "" {
			withDB = append(withDB, a)
		}
	}
	pvs, err := pvNames(ns)
	if err != nil {
		return err
	}

	fmt.Printf("==> Deleting environment %q (namespace %s, %d app(s))\n", name, ns, len(apps))
	if keepData {
		fmt.Println("    keeping databases and volumes (--keep-data)")
	} else {
		for _, a := range withDB {
			fmt.Printf("    drop the %s database of app %s (only if its owner comment names env=%s)\n", a.Data["database_engine"], a.Name, name)
		}
		for _, pv := range pvs {
			fmt.Printf("    delete PersistentVolume %s with the namespace (reclaim policy set to Delete)\n", pv)
		}
	}
	fmt.Printf("    delete namespace %s\n", ns)
	if !yes {
		fmt.Println("\nNothing was changed. Re-run with --yes to apply this plan.")
		return nil
	}

	if !keepData {
		failed := 0
		for _, a := range withDB {
			if err := dropAppDatabase(a.Name, appname.Hosts(a.Data), a.Data, ns); err != nil {
				failed++
				fmt.Printf("⚠️  app %s: %v\n", a.Name, err)
			}
		}
		if failed > 0 && !force {
			return fmt.Errorf("%d database(s) could not be dropped; the namespace was kept. Fix the cause and re-run, or pass --force", failed)
		}
		for _, pv := range pvs {
			if _, err := RunKubectl([]string{"patch", "pv", pv, "-p", `{"spec":{"persistentVolumeReclaimPolicy":"Delete"}}`}, ""); err != nil {
				fmt.Printf("⚠️  PersistentVolume %s keeps its reclaim policy: %v\n", pv, err)
			}
		}
	} else if len(pvs) > 0 {
		fmt.Printf("--> Volumes kept: %s (delete them by hand when no longer needed)\n", strings.Join(pvs, " "))
	}
	if _, err := RunKubectl([]string{"delete", "namespace", ns, "--wait=false"}, ""); err != nil {
		return err
	}
	fmt.Printf("✅ Environment %q deleted (namespace %s is terminating).\n", name, ns)
	return nil
}

func envGC(args []string) error {
	fs := flag.NewFlagSet("env gc", flag.ExitOnError)
	yes := fs.Bool("yes", false, "Delete the expired environments (otherwise only list them)")
	_ = fs.Parse(args)
	items, err := listEnvNamespaces(env.Label)
	if err != nil {
		return err
	}
	var expired []string
	for _, ns := range items {
		exp := ns.Metadata.Annotations[env.ExpiresAnnotation]
		if exp == "" {
			continue
		}
		t, err := time.Parse(time.RFC3339, exp)
		if err != nil {
			fmt.Printf("⚠️  namespace %s: bad %s %q\n", ns.Metadata.Name, env.ExpiresAnnotation, exp)
			continue
		}
		if now().After(t) {
			expired = append(expired, ns.Metadata.Labels[env.Label])
		}
	}
	sort.Strings(expired)
	if len(expired) == 0 {
		fmt.Println("No expired environments.")
		return nil
	}
	failed := 0
	for _, name := range expired {
		if err := envDelete(name, false, false, *yes); err != nil {
			failed++
			fmt.Printf("⚠️  %s: %v\n", name, err)
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d expired environment(s) could not be deleted", failed, len(expired))
	}
	return nil
}

func envList() error {
	items, err := listEnvNamespaces(env.Label)
	if err != nil {
		return err
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(w, "ENV\tNAMESPACE\tEXPIRES")
	for _, ns := range items {
		exp := ns.Metadata.Annotations[env.ExpiresAnnotation]
		if exp == "" {
			exp = "never"
		} else if t, err := time.Parse(time.RFC3339, exp); err == nil && now().After(t) {
			exp += " (expired)"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\n", ns.Metadata.Labels[env.Label], ns.Metadata.Name, exp)
	}
	return w.Flush()
}
