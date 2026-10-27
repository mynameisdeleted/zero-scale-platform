package platform

import (
	"fmt"
	"strings"
)

// Pinned defaults; each install command accepts --version to override.
const (
	LonghornVersion   = "1.9.1"
	CNPGChartVersion  = "0.24.0" // ships operator 1.26.0
	CNPGVersion       = "1.26.0"
	longhornChartRepo = "https://charts.longhorn.io"
	cnpgChartRepo     = "https://cloudnative-pg.github.io/charts"
)

// Step is one action of an install plan.
type Step struct {
	Title    string
	Command  []string // a command to run (helm or kubectl); empty when Manifest is set
	Manifest string   // YAML applied with kubectl apply -f -
	// Optional steps failing does not abort the install (e.g. waits that time out).
	Optional bool
}

// String renders the step as it would be typed.
func (s Step) String() string {
	if s.Manifest != "" {
		return "kubectl apply -f - <<'EOF'\n" + strings.TrimRight(s.Manifest, "\n") + "\nEOF"
	}
	return strings.Join(s.Command, " ")
}

// Nodes summarises schedulable worker capacity.
type Nodes struct {
	Schedulable int
	Names       []string
}

// CountNodes returns Ready nodes that accept regular pods.
func CountNodes(k Kube) (Nodes, error) {
	items, err := getItems(k, "get", "nodes")
	if err != nil {
		return Nodes{}, err
	}
	var n Nodes
	for _, node := range items {
		if node.str("spec", "unschedulable") == "true" {
			continue
		}
		noSchedule := false
		for _, t := range node.list("spec", "taints") {
			if t.str("effect") == "NoSchedule" || t.str("effect") == "NoExecute" {
				noSchedule = true
			}
		}
		ready := false
		for _, c := range node.list("status", "conditions") {
			if c.str("type") == "Ready" && c.str("status") == "True" {
				ready = true
			}
		}
		if ready && !noSchedule {
			n.Schedulable++
			n.Names = append(n.Names, node.str("metadata", "name"))
		}
	}
	return n, nil
}

// replicasFor caps replica counts at the node count, up to 3.
func replicasFor(nodes int) int {
	switch {
	case nodes >= 3:
		return 3
	case nodes < 1:
		return 1
	}
	return nodes
}

// HAWarning explains what a node count means for redundancy, or "" when it is fine.
func HAWarning(nodes int) string {
	switch {
	case nodes < 2:
		return fmt.Sprintf("only %d schedulable node(s): data has a single copy, so this is NOT HA", nodes)
	case nodes == 2:
		return "only 2 schedulable nodes: data survives one node loss but has no spare copy until it returns; 3+ nodes recommended"
	}
	return ""
}

// LonghornOptions configure the Longhorn install plan.
type LonghornOptions struct {
	Version       string
	Nodes         int
	Helm          bool   // helm is on PATH
	BackupTarget  string // e.g. s3://bucket@region/ ; empty skips backups
	BackupSecret  string // Secret in longhorn-system with the target's credentials
	MakeDefault   bool   // mark longhorn-ha as the cluster default StorageClass
	BlockClass    string // default longhorn-ha
	RWXClass      string // default longhorn-ha-rwx
	AlreadyExists bool   // Longhorn's CRDs are present: skip the install, keep classes
}

// LonghornPlan returns the steps that install Longhorn and the HA StorageClasses.
func LonghornPlan(o LonghornOptions) []Step {
	if o.Version == "" {
		o.Version = LonghornVersion
	}
	if o.BlockClass == "" {
		o.BlockClass = "longhorn-ha"
	}
	if o.RWXClass == "" {
		o.RWXClass = "longhorn-ha-rwx"
	}
	r := replicasFor(o.Nodes)
	var steps []Step

	if !o.AlreadyExists {
		if o.Helm {
			steps = append(steps,
				Step{Title: "Add the Longhorn chart repo", Command: []string{"helm", "repo", "add", "longhorn", longhornChartRepo, "--force-update"}},
				Step{Title: "Install Longhorn " + o.Version, Command: []string{"helm", "upgrade", "--install", "longhorn", "longhorn/longhorn",
					"--namespace", "longhorn-system", "--create-namespace", "--version", o.Version,
					"--set", fmt.Sprintf("defaultSettings.defaultReplicaCount=%d", r),
					"--set", fmt.Sprintf("persistence.defaultClassReplicaCount=%d", r),
					"--wait", "--timeout", "15m"}},
			)
		} else {
			steps = append(steps,
				Step{Title: "Install Longhorn " + o.Version + " (release manifest)", Command: []string{"kubectl", "apply",
					"-f", fmt.Sprintf("https://raw.githubusercontent.com/longhorn/longhorn/v%s/deploy/longhorn.yaml", o.Version)}},
				Step{Title: "Wait for longhorn-manager", Command: []string{"kubectl", "-n", "longhorn-system", "rollout", "status", "daemonset/longhorn-manager", "--timeout=15m"}},
				Step{Title: "Wait for Longhorn settings to exist", Command: []string{"kubectl", "-n", "longhorn-system", "wait", "--for=create", "settings.longhorn.io/default-replica-count", "--timeout=5m"}, Optional: true},
				Step{Title: fmt.Sprintf("Set default replica count to %d", r), Command: []string{"kubectl", "-n", "longhorn-system", "patch",
					"settings.longhorn.io", "default-replica-count", "--type", "merge", "-p", fmt.Sprintf(`{"value":"%d"}`, r)}},
			)
		}
	}

	defaultAnn := ""
	if o.MakeDefault {
		defaultAnn = "\n  annotations:\n    storageclass.kubernetes.io/is-default-class: \"true\""
	}
	classes := fmt.Sprintf(`apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: %[1]s
  labels:
    app.kubernetes.io/part-of: zero-scale-platform%[4]s
provisioner: driver.longhorn.io
allowVolumeExpansion: true
reclaimPolicy: Retain
volumeBindingMode: Immediate
parameters:
  numberOfReplicas: "%[3]d"
  staleReplicaTimeout: "30"
  dataLocality: best-effort
---
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: %[2]s
  labels:
    app.kubernetes.io/part-of: zero-scale-platform
provisioner: driver.longhorn.io
allowVolumeExpansion: true
reclaimPolicy: Retain
volumeBindingMode: Immediate
parameters:
  numberOfReplicas: "%[3]d"
  staleReplicaTimeout: "30"
  nfsOptions: "vers=4.1,noresvport,softerr,timeo=600,retrans=5"
`, o.BlockClass, o.RWXClass, r, defaultAnn)
	steps = append(steps, Step{Title: fmt.Sprintf("Create StorageClasses %s (RWO) and %s (RWX), %d replicas", o.BlockClass, o.RWXClass, r), Manifest: classes})

	if o.BackupTarget != "" {
		patch := fmt.Sprintf(`{"spec":{"backupTargetURL":%q,"credentialSecret":%q}}`, o.BackupTarget, o.BackupSecret)
		steps = append(steps,
			Step{Title: "Point Longhorn's default backup target at " + o.BackupTarget, Command: []string{"kubectl", "-n", "longhorn-system", "patch",
				"backuptargets.longhorn.io", "default", "--type", "merge", "-p", patch}},
			Step{Title: "Daily snapshots (keep 7) and weekly backups (keep 4)", Manifest: `apiVersion: longhorn.io/v1beta2
kind: RecurringJob
metadata:
  name: zero-scale-daily-snapshot
  namespace: longhorn-system
spec:
  task: snapshot
  cron: "0 2 * * *"
  retain: 7
  concurrency: 2
  groups: ["default"]
---
apiVersion: longhorn.io/v1beta2
kind: RecurringJob
metadata:
  name: zero-scale-weekly-backup
  namespace: longhorn-system
spec:
  task: backup
  cron: "0 3 * * 0"
  retain: 4
  concurrency: 1
  groups: ["default"]
`},
		)
	}
	return steps
}

// PostgresOptions configure the CloudNativePG install plan.
type PostgresOptions struct {
	ChartVersion    string
	OperatorVersion string
	Helm            bool
	OperatorExists  bool // CNPG CRDs are already installed
	Namespace       string
	Name            string
	Instances       int
	Size            string
	StorageClass    string
	Nodes           int
}

// PostgresPlan returns the steps that install CloudNativePG and an HA cluster.
func PostgresPlan(o PostgresOptions) ([]Step, error) {
	if o.StorageClass == "" {
		return nil, fmt.Errorf("no HA block StorageClass to put Postgres on; run 'zero-scale-ctl install longhorn' or pass --storage-class")
	}
	if o.ChartVersion == "" {
		o.ChartVersion = CNPGChartVersion
	}
	if o.OperatorVersion == "" {
		o.OperatorVersion = CNPGVersion
	}
	if o.Namespace == "" {
		o.Namespace = "cnpg-system"
	}
	if o.Name == "" {
		o.Name = "ha-postgres"
	}
	if o.Instances == 0 {
		o.Instances = 3
	}
	if o.Size == "" {
		o.Size = "10Gi"
	}
	if err := ValidateIdentifier(strings.ReplaceAll(o.Name, "-", "_")); err != nil {
		return nil, fmt.Errorf("invalid cluster name %q", o.Name)
	}

	var steps []Step
	if !o.OperatorExists {
		if o.Helm {
			steps = append(steps,
				Step{Title: "Add the CloudNativePG chart repo", Command: []string{"helm", "repo", "add", "cnpg", cnpgChartRepo, "--force-update"}},
				Step{Title: "Install the CloudNativePG operator (chart " + o.ChartVersion + ")", Command: []string{"helm", "upgrade", "--install", "cnpg", "cnpg/cloudnative-pg",
					"--namespace", "cnpg-system", "--create-namespace", "--version", o.ChartVersion, "--wait", "--timeout", "10m"}},
			)
		} else {
			minor := o.OperatorVersion[:strings.LastIndex(o.OperatorVersion, ".")]
			steps = append(steps,
				Step{Title: "Install the CloudNativePG operator " + o.OperatorVersion, Command: []string{"kubectl", "apply", "--server-side", "-f",
					fmt.Sprintf("https://raw.githubusercontent.com/cloudnative-pg/cloudnative-pg/release-%s/releases/cnpg-%s.yaml", minor, o.OperatorVersion)}},
				Step{Title: "Wait for the operator", Command: []string{"kubectl", "-n", "cnpg-system", "rollout", "status", "deployment/cnpg-controller-manager", "--timeout=10m"}},
			)
		}
	}
	steps = append(steps, Step{Title: "Wait for the Cluster CRD", Command: []string{"kubectl", "wait", "--for=condition=Established", "crd/clusters.postgresql.cnpg.io", "--timeout=5m"}})

	if o.Namespace != "cnpg-system" {
		steps = append(steps, Step{Title: "Create namespace " + o.Namespace, Manifest: fmt.Sprintf("apiVersion: v1\nkind: Namespace\nmetadata:\n  name: %s\n", o.Namespace)})
	}
	affinity := "preferred"
	if o.Nodes >= o.Instances {
		affinity = "required"
	}
	steps = append(steps,
		Step{Title: fmt.Sprintf("Create Cluster %s/%s: %d instances, %s on %s", o.Namespace, o.Name, o.Instances, o.Size, o.StorageClass), Manifest: fmt.Sprintf(`apiVersion: postgresql.cnpg.io/v1
kind: Cluster
metadata:
  name: %s
  namespace: %s
  labels:
    app.kubernetes.io/part-of: zero-scale-platform
spec:
  instances: %d
  enableSuperuserAccess: true
  primaryUpdateStrategy: unsupervised
  storage:
    size: %s
    storageClass: %s
  affinity:
    enablePodAntiAffinity: true
    topologyKey: kubernetes.io/hostname
    podAntiAffinityType: %s
`, o.Name, o.Namespace, o.Instances, o.Size, o.StorageClass, affinity)},
		Step{Title: "Wait for the cluster to be ready", Command: []string{"kubectl", "-n", o.Namespace, "wait", "--for=condition=Ready", "cluster.postgresql.cnpg.io/" + o.Name, "--timeout=20m"}},
	)
	return steps, nil
}
