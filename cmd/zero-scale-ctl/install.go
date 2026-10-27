package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"git.fairfaxmedia.net/open-cream-cheese/zero-scale-platform/internal/env"
	"git.fairfaxmedia.net/open-cream-cheese/zero-scale-platform/internal/platform"
)

func runInstall(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: zero-scale-ctl install longhorn|postgres [options] [--yes]")
	}
	switch args[0] {
	case "longhorn":
		return installLonghorn(args[1:])
	case "postgres", "postgresql", "cnpg":
		return installPostgres(args[1:])
	}
	return fmt.Errorf("unknown install target %q (choose longhorn or postgres)", args[0])
}

func haveBinary(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

func crdExists(name string) bool {
	_, err := RunKubectl([]string{"get", "crd", name}, "")
	return err == nil
}

func installLonghorn(args []string) error {
	fs := flag.NewFlagSet("install longhorn", flag.ExitOnError)
	version := fs.String("version", platform.LonghornVersion, "Longhorn version")
	backupTarget := fs.String("backup-target", "", "Backup target URL (s3://bucket@region/ or nfs://host:/path); enables recurring snapshots and backups")
	backupSecret := fs.String("backup-secret", "", "Secret in longhorn-system holding the backup target credentials")
	makeDefault := fs.Bool("make-default", false, "Mark longhorn-ha as the cluster's default StorageClass")
	yes := fs.Bool("yes", false, "Apply the plan (otherwise only print it)")
	_ = fs.Parse(args)

	nodes, err := platform.CountNodes(kubectlKube{})
	if err != nil {
		return fmt.Errorf("counting nodes: %w", err)
	}
	opts := platform.LonghornOptions{
		Version: *version, Nodes: nodes.Schedulable, Helm: haveBinary("helm"),
		BackupTarget: *backupTarget, BackupSecret: *backupSecret, MakeDefault: *makeDefault,
		AlreadyExists: crdExists("volumes.longhorn.io"),
	}

	fmt.Printf("==> Longhorn install plan (%d schedulable node(s): %v)\n", nodes.Schedulable, nodes.Names)
	if w := platform.HAWarning(nodes.Schedulable); w != "" {
		fmt.Printf("⚠️  %s\n", w)
	}
	if opts.AlreadyExists {
		fmt.Println("    Longhorn is already installed; only the zero-scale StorageClasses will be added.")
	}
	fmt.Println("    Every node needs open-iscsi (iscsid running) and nfs-common/nfs-utils for RWX volumes.")

	var steps []platform.Step
	if !opts.AlreadyExists && haveBinary("longhornctl") {
		steps = append(steps, platform.Step{Title: "Check node prerequisites", Command: []string{"longhornctl", "check", "preflight"}})
	} else if !opts.AlreadyExists {
		fmt.Println("    longhornctl is not on PATH, so node prerequisites are not checked automatically.")
	}
	steps = append(steps, platform.LonghornPlan(opts)...)

	if err := executePlan(steps, *yes); err != nil || !*yes {
		return err
	}
	block, rwx := "longhorn-ha", "longhorn-ha-rwx"
	return updatePlatform(func(c *platform.Config) {
		c.Storage.BlockClass, c.Storage.RWXClass = block, rwx
	})
}

func installPostgres(args []string) error {
	fs := flag.NewFlagSet("install postgres", flag.ExitOnError)
	name := fs.String("name", "ha-postgres", "Cluster name")
	ns := fs.String("namespace", "cnpg-system", "Namespace for the cluster")
	instances := fs.Int("instances", 3, "Postgres instances (1 primary + replicas)")
	size := fs.String("size", "10Gi", "Volume size per instance")
	sc := fs.String("storage-class", "", "StorageClass (default: storage.blockClass from the platform config)")
	chart := fs.String("version", platform.CNPGChartVersion, "cloudnative-pg chart version (helm)")
	operator := fs.String("operator-version", platform.CNPGVersion, "CloudNativePG operator version (manifest install)")
	clusterOnly := fs.Bool("cluster-only", false, "Skip the operator install (it is already there)")
	envName := fs.String("env", "", "Create the cluster inside this environment's namespace and record it only for that environment")
	yes := fs.Bool("yes", false, "Apply the plan (otherwise only print it)")
	_ = fs.Parse(args)

	envNS := ""
	if *envName != "" {
		matches, err := listEnvNamespaces(env.Label + "=" + *envName)
		if err != nil {
			return err
		}
		if len(matches) != 1 {
			return fmt.Errorf("environment %q not found; create it first with 'zero-scale-ctl env create %s'", *envName, *envName)
		}
		envNS = matches[0].Metadata.Name
		*ns = envNS
		if *name == "ha-postgres" {
			*name = *envName + "-postgres"
		}
	}

	storageClass := *sc
	if storageClass == "" {
		if p, err := loadPlatform(); err == nil && p != nil {
			storageClass = p.Storage.BlockClass
		}
	}
	nodes, err := platform.CountNodes(kubectlKube{})
	if err != nil {
		return fmt.Errorf("counting nodes: %w", err)
	}
	steps, err := platform.PostgresPlan(platform.PostgresOptions{
		ChartVersion: *chart, OperatorVersion: *operator, Helm: haveBinary("helm"),
		OperatorExists: *clusterOnly || crdExists("clusters.postgresql.cnpg.io"),
		Namespace:      *ns, Name: *name, Instances: *instances, Size: *size,
		StorageClass: storageClass, Nodes: nodes.Schedulable, NamespaceExists: envNS != "",
	})
	if err != nil {
		return err
	}
	fmt.Printf("==> PostgreSQL install plan (%d schedulable node(s))\n", nodes.Schedulable)
	if nodes.Schedulable < *instances {
		fmt.Printf("⚠️  %d instances on %d node(s): some instances will share a node\n", *instances, nodes.Schedulable)
	}
	if envNS != "" {
		fmt.Printf("    recorded for environment %q only (its %s ConfigMap); the platform's Postgres is unchanged\n", *envName, env.ConfigMapName)
	}
	if err := executePlan(steps, *yes); err != nil || !*yes {
		return err
	}
	target := &platform.DBTarget{Operator: "cnpg", Namespace: *ns, Name: *name,
		RWService: fmt.Sprintf("%s-rw.%s.svc.cluster.local:5432", *name, *ns)}
	if envNS != "" {
		return recordEnvPostgres(envNS, target)
	}
	return updatePlatform(func(c *platform.Config) { c.Postgres = target })
}

// recordEnvPostgres points one environment at its own Postgres cluster.
func recordEnvPostgres(namespace string, t *platform.DBTarget) error {
	e, err := loadEnv(namespace)
	if err != nil {
		return err
	}
	if e == nil {
		return fmt.Errorf("namespace %s has no %s ConfigMap", namespace, env.ConfigMapName)
	}
	for k := range e.Overrides {
		if strings.HasPrefix(k, "postgres.") {
			delete(e.Overrides, k)
		}
	}
	e.Overrides["postgres.operator"] = t.Operator
	e.Overrides["postgres.cluster"] = t.Ref()
	e.Overrides["postgres.rwService"] = t.RWService
	fmt.Printf("\n==> Environment %q now provisions Postgres databases on %s\n", e.Name, t.Ref())
	return CreateOrUpdateConfigMap(env.ConfigMapName, namespace, e.ToData(),
		map[string]string{"app.kubernetes.io/part-of": "zero-scale-platform", "app.kubernetes.io/managed-by": "zero-scale-ctl"})
}

// executePlan prints every step, then runs them in order when apply is set.
func executePlan(steps []platform.Step, apply bool) error {
	for i, s := range steps {
		fmt.Printf("\n[%d/%d] %s\n%s\n", i+1, len(steps), s.Title, indent(s.String()))
	}
	if !apply {
		fmt.Println("\nNothing was changed. Re-run with --yes to apply this plan.")
		return nil
	}
	for i, s := range steps {
		fmt.Printf("\n==> [%d/%d] %s\n", i+1, len(steps), s.Title)
		var err error
		if s.Manifest != "" {
			_, err = RunKubectl([]string{"apply", "-f", "-"}, s.Manifest)
		} else {
			cmd := exec.Command(s.Command[0], s.Command[1:]...)
			cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
			err = cmd.Run()
		}
		if err != nil {
			if s.Optional {
				fmt.Printf("⚠️  optional step failed, continuing: %v\n", err)
				continue
			}
			return fmt.Errorf("step %d (%s) failed: %w\n--> Fix the cause and re-run; completed steps are safe to repeat", i+1, s.Title, err)
		}
	}
	return nil
}

func indent(s string) string {
	out := "    "
	for _, r := range s {
		out += string(r)
		if r == '\n' {
			out += "    "
		}
	}
	return out
}

// updatePlatform loads the platform ConfigMap (or starts an empty one), applies
// change, shows the diff and writes it.
func updatePlatform(change func(*platform.Config)) error {
	current, err := loadPlatform()
	if err != nil {
		return err
	}
	next := &platform.Config{}
	if current != nil {
		copied, _ := platform.FromData(current.ToData())
		next = copied
	}
	change(next)
	diff := platform.Diff(current, next)
	if len(diff) == 0 {
		return nil
	}
	fmt.Printf("\n==> Updating %s/%s\n", platformNamespace(), platform.ConfigMapName)
	for _, d := range diff {
		fmt.Println("    " + d)
	}
	if err := ensureNamespace(platformNamespace()); err != nil {
		return err
	}
	return CreateOrUpdateConfigMap(platform.ConfigMapName, platformNamespace(), next.ToData(),
		map[string]string{"app.kubernetes.io/part-of": "zero-scale-platform"})
}
