package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"git.fairfaxmedia.net/open-cream-cheese/zero-scale-platform/internal/platform"
)

// GetGatewayPods lists every running gateway pod in namespace.
func GetGatewayPods(namespace string) ([]string, error) {
	for _, label := range []string{"app=zero-scale-gateway", "app.kubernetes.io/name=zero-scale-platform"} {
		out, err := RunKubectl([]string{"get", "pods", "-n", namespace, "-l", label,
			"--field-selector=status.phase=Running", "-o", "jsonpath={.items[*].metadata.name}"}, "")
		if pods := strings.Fields(out); err == nil && len(pods) > 0 {
			return pods, nil
		}
	}
	_, err := GetGatewayPod(namespace) // same lookup, with the remediation message
	return nil, err
}

// forEachGatewayPod runs fn for every gateway replica, printing an error if there are none.
func forEachGatewayPod(namespace string, fn func(pod string)) {
	pods, err := GetGatewayPods(namespace)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
		os.Exit(1)
	}
	for _, pod := range pods {
		fn(pod)
	}
}

// stopAppEverywhere stops an app's unit on every gateway replica.
func stopAppEverywhere(host, namespace string) {
	pods, _ := GetGatewayPods(namespace)
	for _, pod := range pods {
		_, _ = ExecPod(pod, namespace, "systemd-runner", []string{"systemctl", "stop", fmt.Sprintf("app-%s.service", host)}, "")
	}
}

// ---------------------------------------------------------------- gateway

func runGateway(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: zero-scale-ctl gateway install|upgrade|status [options]")
	}
	switch args[0] {
	case "install", "upgrade":
		return gatewayInstall(args[0], args[1:])
	case "status":
		return gatewayStatus(args[1:])
	}
	return fmt.Errorf("unknown gateway command %q", args[0])
}

type deployment struct {
	Metadata struct {
		Labels map[string]string `json:"labels"`
	} `json:"metadata"`
}

func existingGatewayManager(ns string) (string, bool) {
	out, err := RunKubectl([]string{"get", "deployment", "zero-scale-gateway", "-n", ns, "-o", "json"}, "")
	if err != nil {
		return "", false
	}
	var d deployment
	_ = json.Unmarshal([]byte(out), &d)
	return d.Metadata.Labels["app.kubernetes.io/managed-by"], true
}

func gatewayInstall(verb string, args []string) error {
	fs := flag.NewFlagSet("gateway "+verb, flag.ExitOnError)
	ns := fs.String("namespace", platform.DefaultNamespace, "Namespace for the gateway")
	image := fs.String("image", "", "Gateway image (required)")
	replicas := fs.Int("replicas", 2, "Gateway replicas")
	rwx := fs.String("rwx-class", "", "RWX StorageClass for /opt/apps (default: storage.rwxClass from the platform config)")
	size := fs.String("volume-size", "5Gi", "Size of the /opt/apps volume")
	dataSize := fs.String("volumes-size", "20Gi", "Size of the shared app-volumes PVC at /srv/zero-scale-volumes")
	idle := fs.String("idle-timeout", "60s", "Default idle time before an app scales to zero")
	yes := fs.Bool("yes", false, "Apply (otherwise only print the manifests)")
	_ = fs.Parse(args)

	p, err := loadPlatform()
	if err != nil {
		return err
	}
	if manager, exists := existingGatewayManager(*ns); exists {
		if manager == "Helm" {
			fmt.Printf("==> A Helm-managed gateway already runs in '%s'; leaving it alone. Upgrade it with Helm.\n", *ns)
			if !*yes {
				return nil
			}
			return updatePlatform(func(c *platform.Config) {
				c.Gateway = platform.GatewayConfig{Namespace: *ns, Service: "zero-scale-gateway", ManagedBy: "Helm"}
			})
		}
		if verb == "install" {
			fmt.Printf("==> A gateway already exists in '%s'; this will update it in place.\n", *ns)
		}
	} else if verb == "upgrade" {
		return fmt.Errorf("no gateway in namespace '%s'; use 'zero-scale-ctl gateway install'", *ns)
	}

	rwxClass := *rwx
	if rwxClass == "" && p != nil {
		rwxClass = p.Storage.RWXClass
	}
	manifest, err := platform.GatewayManifest(platform.GatewayOptions{
		Namespace: *ns, Image: *image, Replicas: *replicas, RWXClass: rwxClass, VolumeSize: *size, DataSize: *dataSize, IdleTimeout: *idle,
	})
	if err != nil {
		return err
	}
	steps := []platform.Step{
		{Title: "Gateway ServiceAccount, RBAC, /opt/apps and app-volumes PVCs, Deployment, Service and PodDisruptionBudget", Manifest: manifest},
		{Title: "Wait for the rollout", Command: []string{"kubectl", "-n", *ns, "rollout", "status", "deployment/zero-scale-gateway", "--timeout=5m"}},
	}
	if *replicas < 2 {
		fmt.Println("⚠️  fewer than 2 replicas: the gateway is a single point of failure")
	}
	if err := ensureNamespaceStep(*ns, *yes); err != nil {
		return err
	}
	if err := executePlan(steps, *yes); err != nil || !*yes {
		return err
	}
	return updatePlatform(func(c *platform.Config) {
		c.Gateway = platform.GatewayConfig{Namespace: *ns, Service: "zero-scale-gateway", ManagedBy: "zero-scale-ctl"}
	})
}

func ensureNamespaceStep(ns string, apply bool) error {
	if !apply {
		return nil
	}
	return ensureNamespace(ns)
}

func gatewayStatus(args []string) error {
	fs := flag.NewFlagSet("gateway status", flag.ExitOnError)
	ns := fs.String("namespace", "", "Gateway namespace (default: from the platform config)")
	_ = fs.Parse(args)
	namespace := *ns
	if namespace == "" {
		if p, _ := loadPlatform(); p != nil && p.Gateway.Namespace != "" {
			namespace = p.Gateway.Namespace
		} else {
			namespace = platform.DefaultNamespace
		}
	}
	out, err := RunKubectl([]string{"get", "deployment,pvc,pdb", "-n", namespace, "-l", "app=zero-scale-gateway"}, "")
	if err != nil {
		return err
	}
	fmt.Print(out)
	pods, err := GetGatewayPods(namespace)
	if err != nil {
		return err
	}
	fmt.Println("\n==> Running apps per replica")
	for _, pod := range pods {
		units, _ := ExecPod(pod, namespace, "systemd-runner", []string{"systemctl", "list-units", "--type=service",
			"--state=running", "--no-legend", "--no-pager", "app-*.service"}, "")
		var apps []string
		for _, line := range strings.Split(units, "\n") {
			if f := strings.Fields(line); len(f) > 0 {
				apps = append(apps, strings.TrimSuffix(strings.TrimPrefix(f[0], "app-"), ".service"))
			}
		}
		if len(apps) == 0 {
			apps = []string{"(none, all scaled to zero)"}
		}
		fmt.Printf("    %s: %s\n", pod, strings.Join(apps, ", "))
	}
	return nil
}

// ---------------------------------------------------------------- ingress routes

func runIngress(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: zero-scale-ctl ingress add-host|remove-host|list ...")
	}
	switch args[0] {
	case "add-host":
		return ingressAddHost(args[1:])
	case "remove-host":
		return ingressRemoveHost(args[1:])
	case "list":
		return ingressList()
	}
	return fmt.Errorf("unknown ingress command %q", args[0])
}

func gatewayTarget(p *platform.Config) (ns, svc string, err error) {
	if p == nil || p.Gateway.Namespace == "" {
		return "", "", fmt.Errorf("no gateway recorded in the platform config; run 'zero-scale-ctl gateway install' or 'zero-scale-ctl init'")
	}
	svc = p.Gateway.Service
	if svc == "" {
		svc = "zero-scale-gateway"
	}
	return p.Gateway.Namespace, svc, nil
}

// splitHostArgs lets value flags (--tls-secret S, --issuer I) come before or after the host.
func splitHostArgs(args []string) (host string, rest []string) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "-") {
			rest = append(rest, a)
			if !strings.Contains(a, "=") && i+1 < len(args) {
				rest = append(rest, args[i+1])
				i++
			}
			continue
		}
		if host == "" {
			host = a
		} else {
			rest = append(rest, a)
		}
	}
	return host, rest
}

func ingressAddHost(args []string) error {
	host, rest := splitHostArgs(args)
	fs := flag.NewFlagSet("ingress add-host", flag.ExitOnError)
	secret := fs.String("tls-secret", "", "Existing TLS Secret (in the gateway namespace) for this host")
	issuer := fs.String("issuer", "", "cert-manager ClusterIssuer to request a certificate from")
	_ = fs.Parse(rest)
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return fmt.Errorf("usage: zero-scale-ctl ingress add-host <host|*.domain> [--tls-secret S | --issuer I]")
	}
	if err := platform.ValidateHost(host); err != nil {
		return err
	}
	if *secret != "" && *issuer != "" {
		return fmt.Errorf("use either --tls-secret or --issuer, not both")
	}
	tls := ""
	switch {
	case *secret != "":
		tls = "secret:" + *secret
	case *issuer != "":
		out, err := RunKubectl([]string{"get", "clusterissuer", *issuer, "-o", "json"}, "")
		if err != nil {
			return fmt.Errorf("ClusterIssuer %q not found: %w", *issuer, err)
		}
		if strings.HasPrefix(host, "*.") {
			ok, err := platform.IssuerSupportsWildcard([]byte(out))
			if err != nil {
				return err
			}
			if !ok {
				return fmt.Errorf("ClusterIssuer %q is ACME without a DNS-01 solver; wildcard certificates need DNS-01", *issuer)
			}
		}
		tls = "issuer:" + *issuer
	}

	p, err := loadPlatform()
	if err != nil {
		return err
	}
	ns, svc, err := gatewayTarget(p)
	if err != nil {
		return err
	}
	manifest, err := platform.RouteManifest(host, ns, svc, resolveIngressClass(), tls)
	if err != nil {
		return err
	}
	if err := ApplyYAML(manifest, ns); err != nil {
		return err
	}
	fmt.Printf("✅ %s routes to %s/%s (Ingress %s)\n", host, ns, svc, platform.RouteName(host))
	return updatePlatform(func(c *platform.Config) {
		if !hasString(c.Ingress.Hosts, host) {
			c.Ingress.Hosts = append(c.Ingress.Hosts, host)
		}
		if c.Ingress.TLS == nil {
			c.Ingress.TLS = map[string]string{}
		}
		if tls == "" {
			delete(c.Ingress.TLS, host)
		} else {
			c.Ingress.TLS[host] = tls
		}
	})
}

func ingressRemoveHost(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: zero-scale-ctl ingress remove-host <host>")
	}
	host := strings.ToLower(strings.TrimSpace(args[0]))
	p, err := loadPlatform()
	if err != nil {
		return err
	}
	ns, _, err := gatewayTarget(p)
	if err != nil {
		return err
	}
	if _, err := RunKubectl([]string{"delete", "ingress", platform.RouteName(host), "-n", ns, "--ignore-not-found"}, ""); err != nil {
		return err
	}
	fmt.Printf("✅ Removed route for %s\n", host)
	return updatePlatform(func(c *platform.Config) {
		var keep []string
		for _, h := range c.Ingress.Hosts {
			if h != host {
				keep = append(keep, h)
			}
		}
		c.Ingress.Hosts = keep
		delete(c.Ingress.TLS, host)
	})
}

func ingressList() error {
	p, err := loadPlatform()
	if err != nil {
		return err
	}
	if p == nil || len(p.Ingress.Hosts) == 0 {
		fmt.Println("No hosts routed to the gateway. Add one with 'zero-scale-ctl ingress add-host <host|*.domain>'.")
		return nil
	}
	ns, svc, _ := gatewayTarget(p)
	fmt.Printf("==> Hosts routed to %s/%s\n", ns, svc)
	for _, h := range p.Ingress.Hosts {
		tls := p.Ingress.TLS[h]
		if tls == "" {
			tls = "no TLS at the ingress"
		}
		fmt.Printf("    %-40s %s\n", h, tls)
	}
	return nil
}

func hasString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// hostsCoveredByRoutes reports whether every host is served by a routed platform ingress host
// in the same namespace as this app's gateway.
func hostsCoveredByRoutes(hosts []string, namespace string) bool {
	p, err := loadPlatform()
	if err != nil || p == nil || len(p.Ingress.Hosts) == 0 || p.Gateway.Namespace != namespace {
		return false
	}
	for _, h := range hosts {
		if !platform.HostCovered(h, p.Ingress.Hosts) {
			return false
		}
	}
	return true
}
