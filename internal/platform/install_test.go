package platform

import (
	"strings"
	"testing"
)

func TestCountNodes(t *testing.T) {
	n, err := CountNodes(fakeKube{"get nodes": "nodes.json"})
	if err != nil {
		t.Fatal(err)
	}
	if n.Schedulable != 2 || strings.Join(n.Names, ",") != "w-1,w-2" {
		t.Errorf("got %+v", n)
	}
	if HAWarning(3) != "" || !strings.Contains(HAWarning(1), "NOT HA") || HAWarning(2) == "" {
		t.Error("HAWarning thresholds wrong")
	}
}

func joinPlan(steps []Step) string {
	var b strings.Builder
	for _, s := range steps {
		b.WriteString(s.Title + "\n" + s.String() + "\n")
	}
	return b.String()
}

func TestLonghornPlanHelm(t *testing.T) {
	plan := joinPlan(LonghornPlan(LonghornOptions{Nodes: 5, Helm: true}))
	for _, want := range []string{
		"helm upgrade --install longhorn longhorn/longhorn --namespace longhorn-system --create-namespace --version " + LonghornVersion,
		"defaultSettings.defaultReplicaCount=3",
		"name: longhorn-ha\n",
		"name: longhorn-ha-rwx\n",
		`numberOfReplicas: "3"`,
		"reclaimPolicy: Retain",
	} {
		if !strings.Contains(plan, want) {
			t.Errorf("plan missing %q:\n%s", want, plan)
		}
	}
	if strings.Contains(plan, "is-default-class") || strings.Contains(plan, "RecurringJob") {
		t.Error("default class and backups must be opt-in")
	}
}

func TestLonghornPlanManifestTwoNodesWithBackups(t *testing.T) {
	plan := joinPlan(LonghornPlan(LonghornOptions{Nodes: 2, BackupTarget: "s3://bk@us-east-1/", BackupSecret: "s3-creds", MakeDefault: true}))
	for _, want := range []string{
		"https://raw.githubusercontent.com/longhorn/longhorn/v" + LonghornVersion + "/deploy/longhorn.yaml",
		`{"value":"2"}`,
		`numberOfReplicas: "2"`,
		`"backupTargetURL":"s3://bk@us-east-1/"`,
		"kind: RecurringJob",
		`is-default-class: "true"`,
	} {
		if !strings.Contains(plan, want) {
			t.Errorf("plan missing %q:\n%s", want, plan)
		}
	}
}

func TestLonghornPlanExistingInstallOnlyAddsClasses(t *testing.T) {
	steps := LonghornPlan(LonghornOptions{Nodes: 3, Helm: true, AlreadyExists: true})
	if len(steps) != 1 || steps[0].Manifest == "" {
		t.Errorf("got %d steps: %s", len(steps), joinPlan(steps))
	}
}

func TestPostgresPlan(t *testing.T) {
	if _, err := PostgresPlan(PostgresOptions{}); err == nil {
		t.Error("expected error without a storage class")
	}
	steps, err := PostgresPlan(PostgresOptions{StorageClass: "longhorn-ha", Helm: true, Nodes: 3})
	if err != nil {
		t.Fatal(err)
	}
	plan := joinPlan(steps)
	for _, want := range []string{
		"helm upgrade --install cnpg cnpg/cloudnative-pg --namespace cnpg-system --create-namespace --version " + CNPGChartVersion,
		"name: ha-postgres\n  namespace: cnpg-system",
		"instances: 3",
		"storageClass: longhorn-ha",
		"enableSuperuserAccess: true",
		"podAntiAffinityType: required",
		"--for=condition=Ready cluster.postgresql.cnpg.io/ha-postgres",
	} {
		if !strings.Contains(plan, want) {
			t.Errorf("plan missing %q:\n%s", want, plan)
		}
	}

	steps, _ = PostgresPlan(PostgresOptions{StorageClass: "ceph", OperatorExists: true, Namespace: "databases", Name: "pg", Nodes: 2})
	plan = joinPlan(steps)
	if strings.Contains(plan, "helm") || strings.Contains(plan, "cnpg-"+CNPGVersion) || !strings.Contains(plan, "podAntiAffinityType: preferred") || !strings.Contains(plan, "kind: Namespace") {
		t.Errorf("cluster-only plan wrong:\n%s", plan)
	}
	steps, _ = PostgresPlan(PostgresOptions{StorageClass: "ceph", Nodes: 3})
	if plan = joinPlan(steps); !strings.Contains(plan, "release-1.26/releases/cnpg-1.26.0.yaml") {
		t.Errorf("manifest URL wrong:\n%s", plan)
	}
}
