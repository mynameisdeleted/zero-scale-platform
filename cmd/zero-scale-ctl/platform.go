package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"git.fairfaxmedia.net/open-cream-cheese/zero-scale-platform/internal/env"
	"git.fairfaxmedia.net/open-cream-cheese/zero-scale-platform/internal/platform"
)

// kubectlKube runs detection queries through kubectl.
type kubectlKube struct{}

func (kubectlKube) GetJSON(args ...string) ([]byte, error) {
	out, err := RunKubectl(append(args, "-o", "json"), "")
	return []byte(out), err
}

// platformNamespace is where the zero-scale-platform ConfigMap lives.
func platformNamespace() string {
	if ns := os.Getenv("ZERO_SCALE_SYSTEM_NAMESPACE"); ns != "" {
		return ns
	}
	return platform.DefaultNamespace
}

// loadPlatformData reads the platform ConfigMap's data. It returns nil, nil when none exists yet.
func loadPlatformData() (map[string]string, error) {
	out, err := RunKubectl([]string{"get", "configmap", platform.ConfigMapName, "-n", platformNamespace(), "-o", "json"}, "")
	if err != nil {
		if isNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return configMapDataFromJSON(out)
}

// loadPlatform reads the platform ConfigMap. It returns nil, nil when none exists yet.
func loadPlatform() (*platform.Config, error) {
	data, err := loadPlatformData()
	if err != nil || data == nil {
		return nil, err
	}
	return platform.FromData(data)
}

// loadPlatformFor is the platform config as seen from namespace: an environment's
// zero-scale-env overrides (its own DB cluster, its own routes and gateway) applied.
func loadPlatformFor(namespace string) (*platform.Config, error) {
	data, err := loadPlatformData()
	if err != nil {
		return nil, err
	}
	e, err := loadEnv(namespace)
	if err != nil {
		return nil, err
	}
	if data == nil && e == nil {
		return nil, nil
	}
	if data == nil {
		data = map[string]string{}
	}
	return platform.FromData(e.Overlay(data, namespace))
}

// loadEnv reads namespace's zero-scale-env ConfigMap. It returns nil, nil when the
// namespace is not a zero-scale environment, which behaves like the primary env.
func loadEnv(namespace string) (*env.Config, error) {
	out, err := RunKubectl([]string{"get", "configmap", env.ConfigMapName, "-n", namespace, "-o", "json"}, "")
	if err != nil {
		if isNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	data, err := configMapDataFromJSON(out)
	if err != nil {
		return nil, err
	}
	return env.FromData(data)
}

func isNotFound(err error) bool {
	return strings.Contains(err.Error(), "NotFound") || strings.Contains(err.Error(), "not found")
}

// bindEnv attaches namespace's environment to app and expands ${ENV_SUFFIX} and
// ${ENV} in its hosts, args and env values. It is safe to call more than once.
func bindEnv(app *AppConfig, namespace string) error {
	if app.envBound {
		return nil
	}
	e, err := loadEnv(namespace)
	if err != nil {
		return fmt.Errorf("reading %s in namespace %s: %w", env.ConfigMapName, namespace, err)
	}
	for i, h := range app.Hostnames {
		h = strings.ToLower(e.Expand(h))
		if strings.Contains(h, "${") {
			return fmt.Errorf("hostname %q: only ${ENV_SUFFIX} and ${ENV} can be used in hostnames", h)
		}
		if err := e.CheckHost(h); err != nil {
			return err
		}
		app.Hostnames[i] = h
	}
	app.Args = e.Expand(app.Args)
	app.Launcher = e.Expand(app.Launcher)
	app.PreStart = e.Expand(app.PreStart)
	app.InitCmd = e.Expand(app.InitCmd)
	for k, v := range app.Env {
		app.Env[k] = e.Expand(v)
	}
	app.Environment = e
	app.envBound = true
	if e != nil {
		fmt.Printf("--> Environment '%s' (namespace %s): hosts %v\n", e.Name, namespace, app.Hostnames)
	}
	return nil
}

// Owner identifies this app's databases for later cleanup.
func (a *AppConfig) Owner(namespace string) platform.Owner {
	o := platform.Owner{App: a.AppName(), Namespace: namespace}
	if a.Environment.Scoped() {
		o.Env = a.Environment.Name
	}
	return o
}

var warnedNoPlatform bool

// resolveDBTarget picks the database cluster for engine: the platform ConfigMap when
// present, otherwise the pre-ConfigMap defaults (CNPG ha-postgres, shared-mysql).
func resolveDBTarget(engine, namespace string) (*platform.DBTarget, *platform.Operator, error) {
	p, err := loadPlatformFor(namespace)
	if err != nil {
		return nil, nil, fmt.Errorf("reading platform config: %w", err)
	}
	if p != nil {
		t := p.Postgres
		if engine == "mysql" {
			t = p.MySQL
		}
		if t == nil {
			return nil, nil, fmt.Errorf("no %s cluster is configured in %s/%s\n--> Run 'zero-scale-ctl doctor' to see what is installed, then 'zero-scale-ctl init --%s <namespace>/<name>'%s",
				engine, platformNamespace(), platform.ConfigMapName, engine, map[bool]string{true: " or 'zero-scale-ctl install postgres'"}[engine == "postgres"])
		}
		op, err := platform.OperatorFor(engine, t.Operator)
		return t, op, err
	}

	if !warnedNoPlatform {
		warnedNoPlatform = true
		fmt.Printf("⚠️  No %s ConfigMap in namespace '%s'; using legacy defaults. Run 'zero-scale-ctl init' to record your platform.\n", platform.ConfigMapName, platformNamespace())
	}
	return legacyDBTarget(engine, namespace)
}

func legacyDBTarget(engine, namespace string) (*platform.DBTarget, *platform.Operator, error) {
	op, _ := platform.OperatorFor(engine, map[string]string{"postgres": "cnpg", "mysql": "shared-mysql"}[engine])
	if engine == "postgres" {
		for _, ns := range []string{namespace, "cnpg-system"} {
			t := &platform.DBTarget{Operator: "cnpg", Namespace: ns, Name: "ha-postgres",
				RWService: fmt.Sprintf("ha-postgres-rw.%s.svc.cluster.local:5432", ns)}
			if _, err := findExecPod(t, op); err == nil {
				return t, op, nil
			}
		}
		return nil, nil, fmt.Errorf(`CloudNativePG PostgreSQL cluster ('ha-postgres') is not running in namespace '%s' or 'cnpg-system'
--> Remediation: run 'zero-scale-ctl doctor' to find an existing HA Postgres, or 'zero-scale-ctl install postgres'
--> Documentation: https://git.fairfaxmedia.net/open-cream-cheese/zero-scale-platform#postgresql-cloudnativepg`, namespace)
	}

	t := &platform.DBTarget{Operator: "shared-mysql", Namespace: namespace, Name: "shared-mysql",
		RWService:   fmt.Sprintf("shared-mysql.%s.svc.cluster.local:3306", namespace),
		AdminSecret: "shared-mysql-admin", AdminKey: "MYSQL_ROOT_PASSWORD", AdminUser: "root"}
	if _, err := findExecPod(t, op); err != nil {
		return nil, nil, fmt.Errorf(`shared-mysql pod is not running in namespace '%s'
--> Remediation: run 'zero-scale-ctl doctor' to find an existing HA MySQL, or deploy shared MySQL with: kubectl apply -f deploy/shared-data-services.yaml -n %s
--> Documentation: https://git.fairfaxmedia.net/open-cream-cheese/zero-scale-platform#mysql-and-redis`, namespace, namespace)
	}
	// The single shared-mysql reads its root password from this Secret on first start.
	if data, err := GetSecretData(t.AdminSecret, namespace); err != nil || data[t.AdminKey] == "" {
		if err := CreateOrUpdateSecret(t.AdminSecret, namespace, map[string]string{t.AdminKey: GeneratePassword(24)},
			map[string]string{"app": "shared-mysql", "app.kubernetes.io/part-of": "zero-scale-platform"}); err != nil {
			return nil, nil, fmt.Errorf("failed to auto-create '%s' secret: %w", t.AdminSecret, err)
		}
	}
	return t, op, nil
}

// findExecPod returns a running pod of the cluster where SQL can be run.
func findExecPod(t *platform.DBTarget, op *platform.Operator) (string, error) {
	for _, sel := range op.ExecSelectors(t) {
		out, err := RunKubectl([]string{"get", "pods", "-n", t.Namespace, "-l", sel,
			"--field-selector=status.phase=Running", "-o", "jsonpath={.items[0].metadata.name}"}, "")
		if pod := strings.TrimSpace(out); err == nil && pod != "" {
			return pod, nil
		}
	}
	return "", fmt.Errorf("no running %s pod for %s (selectors: %s)", op.Display, t.Ref(), strings.Join(op.ExecSelectors(t), " | "))
}

// runPostgres pipes a psql script into the primary over stdin.
func runPostgres(t *platform.DBTarget, op *platform.Operator, sql string) error {
	pod, err := findExecPod(t, op)
	if err != nil {
		return err
	}
	_, err = ExecPod(pod, t.Namespace, op.ExecContainer,
		[]string{"psql", "-v", "ON_ERROR_STOP=1", "-U", "postgres", "-d", "postgres", "-q", "-f", "-"}, sql)
	return err
}

// runMySQL pipes SQL into a MySQL client that connects to the cluster's RW service.
// The admin password travels on stdin, never on a command line.
func runMySQL(t *platform.DBTarget, op *platform.Operator, sql string) error {
	pod, err := findExecPod(t, op)
	if err != nil {
		return err
	}
	admin, err := GetSecretData(t.AdminSecret, t.Namespace)
	if err != nil || admin[t.AdminKey] == "" {
		return fmt.Errorf("admin password not found in Secret %s/%s key %s", t.Namespace, t.AdminSecret, t.AdminKey)
	}
	user := t.AdminUser
	if user == "" {
		user = "root"
	}
	_, err = ExecPod(pod, t.Namespace, op.ExecContainer,
		[]string{"sh", "-c", platform.MySQLClientScript(t.Host(), t.Port(), user)}, admin[t.AdminKey]+"\n"+sql)
	return err
}

// DoctorFilter restricts doctor output to a specific environment, namespace, or zero-scale apps.
type DoctorFilter struct {
	Env           string
	Namespace     string
	ZeroScaleOnly bool
}

func (f DoctorFilter) Active() bool {
	return f.Env != "" || f.Namespace != "" || f.ZeroScaleOnly
}

func resolveEnvNamespace(k platform.Kube, envName string) (string, error) {
	if envName == "" {
		return "", nil
	}
	raw, err := k.GetJSON("get", "namespace", "-l", env.Label+"="+envName)
	if err == nil {
		var list struct {
			Items []struct {
				Metadata struct {
					Name string `json:"name"`
				} `json:"metadata"`
			} `json:"items"`
		}
		if json.Unmarshal(raw, &list) == nil && len(list.Items) > 0 {
			return list.Items[0].Metadata.Name, nil
		}
	}
	return envName, nil
}

func filterDBCandidates(cands []platform.DBCandidate, f DoctorFilter, activeTarget *platform.DBTarget) []platform.DBCandidate {
	if !f.Active() {
		return cands
	}
	var out []platform.DBCandidate
	for _, c := range cands {
		match := true
		if f.Namespace != "" {
			match = (c.Namespace == f.Namespace) || (activeTarget != nil && c.Namespace == activeTarget.Namespace && c.Name == activeTarget.Name)
		}
		if match && f.ZeroScaleOnly {
			isZS := c.Operator == "shared-mysql" ||
				(activeTarget != nil && c.Namespace == activeTarget.Namespace && c.Name == activeTarget.Name) ||
				c.Namespace == platformNamespace()
			if !isZS {
				match = false
			}
		}
		if match {
			out = append(out, c)
		}
	}
	return out
}

func filterGateways(gws []platform.Gateway, f DoctorFilter) []platform.Gateway {
	if f.Namespace == "" {
		return gws
	}
	var out []platform.Gateway
	for _, g := range gws {
		if g.Namespace == f.Namespace {
			out = append(out, g)
		}
	}
	return out
}

func filterRedis(redis []string, f DoctorFilter) []string {
	if f.Namespace == "" {
		return redis
	}
	var out []string
	for _, r := range redis {
		if strings.Contains(r, "."+f.Namespace+".") || strings.HasPrefix(r, f.Namespace+"/") {
			out = append(out, r)
		}
	}
	return out
}

// ---------------------------------------------------------------- doctor / init

func runDoctor(args []string) error {
	fs := flag.NewFlagSet("doctor", flag.ExitOnError)
	envFlag := fs.String("env", "", "Filter diagnostics to a specific environment name")
	envShort := fs.String("e", "", "Filter diagnostics to a specific environment name (shorthand)")
	nsFlag := fs.String("namespace", "", "Filter diagnostics to a specific namespace")
	nsShort := fs.String("n", "", "Filter diagnostics to a specific namespace (shorthand)")
	zsFlag := fs.Bool("zero-scale-only", false, "Diagnose zero-scale managed apps only (hide third-party workloads)")
	zsShort := fs.Bool("z", false, "Diagnose zero-scale managed apps only (shorthand)")
	appsFlag := fs.Bool("apps-only", false, "Diagnose zero-scale managed apps only (alias)")
	_ = fs.Parse(args)

	kube := kubectlKube{}
	envName := *envFlag
	if envName == "" {
		envName = *envShort
	}
	targetNS := *nsFlag
	if targetNS == "" {
		targetNS = *nsShort
	}
	if envName != "" && targetNS == "" {
		ns, err := resolveEnvNamespace(kube, envName)
		if err != nil {
			return err
		}
		targetNS = ns
	}

	filter := DoctorFilter{
		Env:           envName,
		Namespace:     targetNS,
		ZeroScaleOnly: *zsFlag || *zsShort || *appsFlag,
	}

	r, err := platform.Detect(kube)
	if err != nil {
		return err
	}

	if filter.Active() {
		var parts []string
		if filter.Env != "" {
			if filter.Namespace != "" && filter.Namespace != filter.Env {
				parts = append(parts, fmt.Sprintf("environment %q (namespace %s)", filter.Env, filter.Namespace))
			} else {
				parts = append(parts, fmt.Sprintf("environment %q", filter.Env))
			}
		} else if filter.Namespace != "" {
			parts = append(parts, fmt.Sprintf("namespace %q", filter.Namespace))
		}
		if filter.ZeroScaleOnly {
			parts = append(parts, "zero-scale managed apps only")
		}
		fmt.Printf("==> Scope: %s\n\n", strings.Join(parts, ", "))
	}

	printReport(r, filter)
	printNodeFailure(r, filter)

	fmt.Println("\n==> Next steps")
	current, _ := loadPlatform()
	if current != nil {
		fmt.Printf("    Platform config exists in %s/%s. Re-run 'zero-scale-ctl init' to refresh it.\n", platformNamespace(), platform.ConfigMapName)
	}
	if filter.Namespace != "" && filter.Namespace != platformNamespace() {
		if e, _ := loadEnv(filter.Namespace); e != nil {
			fmt.Printf("    Environment config exists in %s/%s.\n", filter.Namespace, env.ConfigMapName)
		}
	}
	_, chooseErr := platform.Choose(r, recordedOptions(current))
	switch e := chooseErr.(type) {
	case nil:
		if current == nil {
			fmt.Println("    Everything required is present. Run 'zero-scale-ctl init' to record it.")
		}
	case *platform.MissingError:
		for _, m := range e.Missing {
			fmt.Printf("    Missing: %s\n", m)
		}
		for _, h := range e.Hints {
			fmt.Printf("    Fix:     %s\n", h)
		}
	case *platform.AmbiguousError:
		fmt.Printf("    %v\n    Then run 'zero-scale-ctl init %s <name>'.\n", e, e.Flag)
	default:
		fmt.Printf("    %v\n", e)
	}
	gws := filterGateways(r.Gateways, filter)
	if len(gws) == 0 {
		if filter.Namespace != "" {
			fmt.Printf("    No zero-scale gateway found in namespace %s: 'zero-scale-ctl gateway install --namespace %s'.\n", filter.Namespace, filter.Namespace)
		} else {
			fmt.Println("    No zero-scale gateway found: 'zero-scale-ctl gateway install' (or deploy your Helm chart).")
		}
	}
	return nil
}

// recordedOptions turns an existing platform config into Choose options, so
// doctor judges the recorded choices instead of asking for them again.
func recordedOptions(c *platform.Config) platform.Options {
	if c == nil {
		return platform.Options{}
	}
	o := platform.Options{BlockClass: c.Storage.BlockClass, RWXClass: c.Storage.RWXClass, Postgres: "none", MySQL: "none"}
	if c.Postgres != nil {
		o.Postgres = c.Postgres.Ref()
	}
	if c.MySQL != nil {
		o.MySQL = c.MySQL.Ref()
	}
	return o
}

// printNodeFailure lists what one node going down would take off the air, and
// Services that hijack a node's IP so that node cannot serve ingress traffic.
func printNodeFailure(r *platform.Report, filter DoctorFilter) {
	fmt.Println("\n==> Single node failure")
	var defaults []string
	for _, sc := range r.StorageClasses {
		if sc.Default {
			defaults = append(defaults, sc.Name)
		}
	}
	if len(defaults) > 1 {
		fmt.Printf("    ⚠️  %d default StorageClasses (%s): PVCs without a class land on whichever the API server picks.\n", len(defaults), strings.Join(defaults, ", "))
	}
	for _, sc := range r.StorageClasses {
		if sc.Default && sc.NodeLocal {
			fmt.Printf("    ⚠️  Default StorageClass %s is node-local: PVCs without a class keep their data on one node. Fix:\n", sc.Name)
			fmt.Printf("        kubectl annotate storageclass %s storageclass.kubernetes.io/is-default-class=false --overwrite\n", sc.Name)
		}
	}
	locals, err := platform.NodeLocalVolumes(kubectlKube{})
	if err != nil {
		fmt.Printf("    (could not check node-local volumes: %v)\n", err)
	}
	for _, v := range locals {
		if filter.Namespace != "" && v.Namespace != filter.Namespace {
			continue
		}
		if filter.ZeroScaleOnly && !v.ZeroScale {
			continue
		}
		where := fmt.Sprintf("%s/%s (%s, on %s, used by %s)", v.Namespace, v.PVC, v.Class, strings.Join(v.Nodes, ", "), strings.Join(v.Pods, ", "))
		switch {
		case v.Temporary:
			fmt.Printf("    ℹ️  %s: temporary data (%s=temporary); its pods still cannot move off that node.\n", where, platform.TemporaryDataAnnotation)
		case v.Replicated():
			fmt.Printf("    ✅ %s: node-local, but %s runs on %d nodes (replicated or sharded by the workload itself).\n", where, v.Owner, v.OwnerNodes)
		default:
			fmt.Printf("    ❌ %s: persistent data on node-local storage is lost or unavailable with that node.\n", where)
			fmt.Printf("        Move it to an HA class, or if it only holds temporary data: kubectl -n %s annotate pvc %s %s=temporary\n", v.Namespace, v.PVC, platform.TemporaryDataAnnotation)
		}
	}
	conflicts, err := platform.RouteConflicts(kubectlKube{})
	if err != nil {
		fmt.Printf("    (could not check Ingress routes: %v)\n", err)
	}
	for _, c := range conflicts {
		if filter.ZeroScaleOnly && !c.ZeroScale {
			continue
		}
		if filter.Namespace != "" {
			nsMatch := false
			for _, routeStr := range c.Routes {
				if strings.HasPrefix(routeStr, filter.Namespace+"/") {
					nsMatch = true
					break
				}
			}
			if !nsMatch {
				continue
			}
		}
		fmt.Printf("    ⚠️  %s%s is routed by several Ingresses to different Services; only one wins: %s\n", c.Host, c.Path, strings.Join(c.Routes, "; "))
	}
	if !filter.ZeroScaleOnly && filter.Namespace == "" {
		knativeOnly, _ := platform.KnativeOnlyHosts(kubectlKube{})
		for _, h := range knativeOnly {
			fmt.Printf("    ⚠️  %s is a Knative DomainMapping with no Ingress rule: it only answers on the Knative gateway's own address, not on every node.\n", h)
		}
	}
	claims, err := platform.NodeIPClaims(kubectlKube{})
	if err != nil {
		fmt.Printf("    (could not check Service externalIPs: %v)\n", err)
	}
	for _, c := range claims {
		if filter.Namespace != "" && c.Namespace != filter.Namespace {
			continue
		}
		if filter.ZeroScaleOnly && !c.ZeroScale {
			continue
		}
		fmt.Printf("    ❌ %s/%s claims %s (node %s) on ports %v: that node's ingress on those ports goes to this Service instead.\n", c.Namespace, c.Service, c.IP, c.Node, c.Ports)
	}
	exps, err := platform.IngressExposures(kubectlKube{})
	if err != nil {
		fmt.Printf("    (could not check Ingress backends: %v)\n", err)
		return
	}
	var filteredExps []platform.Exposure
	for _, e := range exps {
		if filter.Namespace != "" && e.Namespace != filter.Namespace {
			continue
		}
		if filter.ZeroScaleOnly && !e.ZeroScale {
			continue
		}
		filteredExps = append(filteredExps, e)
	}
	ok := 0
	for _, e := range filteredExps {
		if e.Survives() {
			ok++
			continue
		}
		fmt.Printf("    ❌ %s/%s: %s (%s)\n", e.Namespace, e.Service, e.Problem(), strings.Join(e.Hosts, ", "))
	}
	var scopeDesc string
	if filter.Env != "" && filter.ZeroScaleOnly {
		scopeDesc = fmt.Sprintf(" (environment %q, zero-scale managed apps)", filter.Env)
	} else if filter.Env != "" {
		scopeDesc = fmt.Sprintf(" (environment %q)", filter.Env)
	} else if filter.Namespace != "" && filter.ZeroScaleOnly {
		scopeDesc = fmt.Sprintf(" (namespace %s, zero-scale managed apps)", filter.Namespace)
	} else if filter.Namespace != "" {
		scopeDesc = fmt.Sprintf(" (namespace %s)", filter.Namespace)
	} else if filter.ZeroScaleOnly {
		scopeDesc = " (zero-scale managed apps)"
	}
	fmt.Printf("    %d of %d Ingress backends survive losing any one node%s.\n", ok, len(filteredExps), scopeDesc)
}

func printReport(r *platform.Report, filter DoctorFilter) {
	mark := func(g platform.Grade) string {
		switch g {
		case platform.HA:
			return "✅ HA     "
		case platform.NotHA:
			return "❌ not HA "
		}
		return "❔ unknown"
	}
	fmt.Println("==> Storage classes")
	if len(r.StorageClasses) == 0 {
		fmt.Println("    (none)")
	}
	for _, sc := range r.StorageClasses {
		extra := ""
		if sc.RWX {
			extra += " RWX"
		}
		if sc.Default {
			extra += " default"
		}
		if sc.NodeLocal {
			extra += " node-local (temporary data only)"
		}
		fmt.Printf("    %s %-24s %-32s %s%s\n", mark(sc.Grade), sc.Name, sc.Provisioner, sc.Reason, extra)
	}

	var activePG, activeMY *platform.DBTarget
	if filter.Namespace != "" {
		if p, _ := loadPlatformFor(filter.Namespace); p != nil {
			activePG = p.Postgres
			activeMY = p.MySQL
		}
	} else if filter.ZeroScaleOnly {
		if p, _ := loadPlatform(); p != nil {
			activePG = p.Postgres
			activeMY = p.MySQL
		}
	}

	for _, sec := range []struct {
		title  string
		cands  []platform.DBCandidate
		target *platform.DBTarget
	}{
		{"PostgreSQL clusters", r.Postgres, activePG},
		{"MySQL clusters", r.MySQL, activeMY},
	} {
		fmt.Printf("\n==> %s\n", sec.title)
		cands := filterDBCandidates(sec.cands, filter, sec.target)
		if len(cands) == 0 {
			fmt.Println("    (none)")
		}
		for _, c := range cands {
			note := ""
			if sec.target != nil && c.Namespace == sec.target.Namespace && c.Name == sec.target.Name {
				if filter.Env != "" {
					note = fmt.Sprintf(" [target for env %s]", filter.Env)
				} else if filter.Namespace != "" {
					note = fmt.Sprintf(" [target for %s]", filter.Namespace)
				}
			}
			fmt.Printf("    %s %-32s %s, ready: %s, rw: %s%s\n", mark(c.Grade), c.Ref(), c.Reason, c.Ready, c.RWService, note)
		}
	}
	fmt.Println("\n==> Ingress classes")
	if len(r.IngressClasses) == 0 {
		fmt.Println("    (none)")
	}
	for _, ic := range r.IngressClasses {
		def := ""
		if ic.Default {
			def = " (default)"
		}
		fmt.Printf("    %-24s %s%s\n", ic.Name, ic.Controller, def)
	}
	fmt.Println("\n==> Zero-scale gateways")
	gws := filterGateways(r.Gateways, filter)
	if len(gws) == 0 {
		fmt.Println("    (none)")
	}
	for _, g := range gws {
		by := g.ManagedBy
		if by == "" {
			by = "unmanaged"
		}
		fmt.Printf("    %s %s/%s %d/%d ready, managed by %s\n", mark(g.Grade), g.Namespace, g.Name, g.Ready, g.Replicas, by)
	}
	redis := filterRedis(r.Redis, filter)
	if len(redis) > 0 {
		fmt.Printf("\n==> Redis\n    %s\n", strings.Join(redis, "\n    "))
	}
	for _, w := range r.Warnings {
		fmt.Printf("\n⚠️  %s\n", w)
	}
}

func runInit(args []string) error {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	block := fs.String("storage-class", "", "HA block StorageClass to use")
	rwx := fs.String("rwx-class", "", "HA ReadWriteMany StorageClass to use")
	pg := fs.String("postgres", "", "Postgres cluster as <namespace>/<name>, or 'none'")
	my := fs.String("mysql", "", "MySQL cluster as <namespace>/<name>, or 'none'")
	allow := fs.Bool("allow-non-ha", false, "Accept non-HA storage or databases")
	yes := fs.Bool("yes", false, "Write without asking")
	dry := fs.Bool("dry-run", false, "Show the result without writing")
	_ = fs.Parse(args)

	r, err := platform.Detect(kubectlKube{})
	if err != nil {
		return err
	}
	opts := platform.Options{BlockClass: *block, RWXClass: *rwx, Postgres: *pg, MySQL: *my, AllowNonHA: *allow}

	var cfg *platform.Config
	for {
		cfg, err = platform.Choose(r, opts)
		amb, ok := err.(*platform.AmbiguousError)
		if !ok {
			break
		}
		if *yes || !stdinIsTerminal() {
			return err
		}
		choice, perr := pick(amb.What, amb.Choices)
		if perr != nil {
			return perr
		}
		switch amb.Flag {
		case "--storage-class":
			opts.BlockClass = choice
		case "--rwx-class":
			opts.RWXClass = choice
		case "--postgres":
			opts.Postgres = choice
		case "--mysql":
			opts.MySQL = choice
		}
	}
	if missing, ok := err.(*platform.MissingError); ok {
		msg := missing.Error() + "\n--> Install what is missing:"
		for _, h := range missing.Hints {
			msg += "\n    " + h
		}
		return fmt.Errorf("%s\n--> Or point at an existing class with --storage-class / --rwx-class (add --allow-non-ha for a non-HA one)", msg)
	}
	if err != nil {
		return err
	}

	current, err := loadPlatform()
	if err != nil {
		return err
	}
	if current != nil {
		// Keep settings that detection does not own.
		cfg.Ingress.Hosts, cfg.Ingress.TLS = current.Ingress.Hosts, current.Ingress.TLS
		if cfg.Gateway.Namespace == "" {
			cfg.Gateway = current.Gateway
		}
	}
	diff := platform.Diff(current, cfg)
	fmt.Printf("==> %s/%s\n", platformNamespace(), platform.ConfigMapName)
	if len(diff) == 0 {
		fmt.Println("    Up to date; nothing to change.")
		return nil
	}
	for _, d := range diff {
		fmt.Println("    " + d)
	}
	if *dry {
		return nil
	}
	if !*yes {
		if !stdinIsTerminal() {
			return fmt.Errorf("re-run with --yes to write these changes")
		}
		if !confirm("Write these changes?") {
			return fmt.Errorf("aborted")
		}
	}
	if err := ensureNamespace(platformNamespace()); err != nil {
		return err
	}
	if err := CreateOrUpdateConfigMap(platform.ConfigMapName, platformNamespace(), cfg.ToData(),
		map[string]string{"app.kubernetes.io/part-of": "zero-scale-platform"}); err != nil {
		return err
	}
	fmt.Println("✅ Platform config written.")
	return nil
}

func ensureNamespace(ns string) error {
	if _, err := RunKubectl([]string{"get", "namespace", ns}, ""); err == nil {
		return nil
	}
	_, err := RunKubectl([]string{"create", "namespace", ns}, "")
	return err
}

func stdinIsTerminal() bool {
	fi, err := os.Stdin.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

func confirm(prompt string) bool {
	fmt.Printf("%s [y/N] ", prompt)
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	line = strings.ToLower(strings.TrimSpace(line))
	return line == "y" || line == "yes"
}

func pick(what string, choices []string) (string, error) {
	fmt.Printf("Several %s candidates fit:\n", what)
	for i, c := range choices {
		fmt.Printf("  %d) %s\n", i+1, c)
	}
	fmt.Print("Choose a number: ")
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	var n int
	if _, err := fmt.Sscanf(strings.TrimSpace(line), "%d", &n); err != nil || n < 1 || n > len(choices) {
		return "", fmt.Errorf("invalid choice")
	}
	return choices[n-1], nil
}
