package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeKubectl puts a kubectl on PATH that answers from internal/platform/testdata
// and records every call (and any stdin) to a log file. It returns the log path.
func fakeKubectl(t *testing.T, extra string) string {
	t.Helper()
	dir := t.TempDir()
	fixtures, _ := filepath.Abs("../../internal/platform/testdata")
	log := filepath.Join(dir, "calls.log")
	script := `#!/bin/sh
F="` + fixtures + `"
echo "kubectl $*" >> "` + log + `"
case "$*" in
` + extra + `
  *" -f -"*|"exec -i "*) cat >> "` + log + `"; exit 0 ;;
  "get storageclass -o json") cat "$F/storageclass.json" ;;
  "get crd -o json") cat "$F/crd.json" ;;
  "get clusters.postgresql.cnpg.io -A -o json") cat "$F/cnpg.json" ;;
  "get perconaxtradbclusters.pxc.percona.com -A -o json") cat "$F/pxc.json" ;;
  "get statefulset -A -l app=shared-mysql -o json") cat "$F/shared-mysql.json" ;;
  "get ingressclass -o json") cat "$F/ingressclass.json" ;;
  "get deployment -A -l app=zero-scale-gateway -o json") cat "$F/gateway.json" ;;
  "get deployment -A -l app.kubernetes.io/name=zero-scale-gateway -o json") cat "$F/empty.json" ;;
  "get service -A -l app=shared-redis -o json") cat "$F/redis.json" ;;
  "get namespace "*) exit 0 ;;
  *) echo "Error from server (NotFound): not found" >&2; exit 1 ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "kubectl"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return log
}

func readLog(t *testing.T, path string) string {
	b, _ := os.ReadFile(path)
	return string(b)
}

func TestInitWritesPlatformConfigMap(t *testing.T) {
	log := fakeKubectl(t, "")
	if err := runInit([]string{"--yes"}); err != nil {
		t.Fatal(err)
	}
	got := readLog(t, log)
	for _, want := range []string{
		"name: zero-scale-platform",
		"namespace: zero-scale-system",
		`storage.blockClass: "longhorn"`,
		`postgres.cluster: "cnpg-system/ha-postgres"`,
		`postgres.rwService: "ha-postgres-rw.cnpg-system.svc.cluster.local:5432"`,
		`mysql.operator: "pxc"`,
		`gateway.managedBy: "Helm"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("applied ConfigMap missing %q\n%s", want, got)
		}
	}
}

func TestDoctorRunsReadOnly(t *testing.T) {
	log := fakeKubectl(t, "")
	if err := runDoctor(nil); err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(readLog(t, log), "\n") {
		if strings.HasPrefix(line, "kubectl ") && !strings.HasPrefix(line, "kubectl get ") {
			t.Errorf("doctor made a non-read call: %s", line)
		}
	}
}

func TestProvisionPostgresUsesPlatformTarget(t *testing.T) {
	cm := `{"data":{"version":"1","postgres.operator":"cnpg","postgres.cluster":"cnpg-system/ha-postgres","postgres.rwService":"ha-postgres-rw.cnpg-system.svc.cluster.local:5432"}}`
	log := fakeKubectl(t, `  "get configmap zero-scale-platform -n zero-scale-system -o json") echo '`+cm+`' ;;
  "get pods -n cnpg-system -l cnpg.io/cluster=ha-postgres,cnpg.io/instanceRole=primary "*) printf ha-postgres-1 ;;`)
	app := &AppConfig{Hostnames: []string{"hoppscotch-web.example.com"}, Database: &DatabaseConfig{Engine: "postgres", EnvPassword: "DATABASE_PASSWORD"}}
	_, _, _, target, err := ProvisionPostgres(app, "qa")
	if err != nil {
		t.Fatal(err)
	}
	if target.Host() != "ha-postgres-rw.cnpg-system.svc.cluster.local" {
		t.Errorf("host = %s", target.Host())
	}
	got := readLog(t, log)
	if !strings.Contains(got, `CREATE DATABASE %I OWNER %I', 'db_hoppscotchweb_`) || !strings.Contains(got, "namespace=qa app=hoppscotchweb") {
		t.Errorf("SQL not sent over stdin as expected:\n%s", got)
	}
	if strings.Contains(got, "psql -U postgres -c") {
		t.Errorf("SQL passed on the command line:\n%s", got)
	}
}

func TestProvisionMySQLKeepsAdminPasswordOffArgv(t *testing.T) {
	cm := `{"data":{"version":"1","mysql.operator":"pxc","mysql.cluster":"databases/mysql-ha","mysql.rwService":"mysql-ha-haproxy.databases.svc.cluster.local:3306","mysql.adminSecret":"mysql-ha-secrets","mysql.adminKey":"root","mysql.adminUser":"root"}}`
	secret := `{"data":{"root":"c3VwZXJzZWNyZXRyb290"}}` // base64("supersecretroot")
	log := fakeKubectl(t, `  "get configmap zero-scale-platform -n zero-scale-system -o json") echo '`+cm+`' ;;
  "get secret mysql-ha-secrets -n databases -o json") echo '`+secret+`' ;;
  "get pods -n databases -l app.kubernetes.io/instance=mysql-ha,app.kubernetes.io/component=pxc "*) printf mysql-ha-pxc-0 ;;`)
	app := &AppConfig{Hostnames: []string{"fdm.ffxm.cc"}, Database: &DatabaseConfig{Engine: "mysql"}}
	if _, _, _, _, err := ProvisionMySQL(app, "default"); err != nil {
		t.Fatal(err)
	}
	var argv, stdin []string
	for _, line := range strings.Split(readLog(t, log), "\n") {
		if strings.HasPrefix(line, "kubectl ") {
			argv = append(argv, line)
		} else {
			stdin = append(stdin, line)
		}
	}
	if strings.Contains(strings.Join(argv, "\n"), "supersecretroot") {
		t.Errorf("admin password on a command line:\n%s", strings.Join(argv, "\n"))
	}
	if len(stdin) == 0 || stdin[0] != "supersecretroot" || !strings.Contains(strings.Join(stdin, "\n"), "CREATE DATABASE IF NOT EXISTS `db_fdm_") {
		t.Errorf("stdin = %v", stdin)
	}
}

func TestInstallPostgresDryRunChangesNothing(t *testing.T) {
	log := fakeKubectl(t, `  "get nodes -o json") cat "$F/nodes.json" ;;`)
	if err := runInstall([]string{"postgres", "--storage-class", "longhorn-ha"}); err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(readLog(t, log), "\n") {
		if strings.HasPrefix(line, "kubectl ") && !strings.HasPrefix(line, "kubectl get ") {
			t.Errorf("dry run made a change: %s", line)
		}
	}
}

func TestInstallPostgresAppliesAndRecords(t *testing.T) {
	log := fakeKubectl(t, `  "get nodes -o json") cat "$F/nodes.json" ;;
  "get crd clusters.postgresql.cnpg.io") exit 0 ;;
  "wait "*|"-n cnpg-system wait "*) exit 0 ;;`)
	if err := runInstall([]string{"postgres", "--storage-class", "longhorn-ha", "--yes"}); err != nil {
		t.Fatal(err)
	}
	got := readLog(t, log)
	for _, want := range []string{
		"kind: Cluster",
		"storageClass: longhorn-ha",
		"podAntiAffinityType: preferred", // fixture has 2 schedulable nodes for 3 instances
		`postgres.cluster: "cnpg-system/ha-postgres"`,
		`postgres.rwService: "ha-postgres-rw.cnpg-system.svc.cluster.local:5432"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "helm") || strings.Contains(got, "cnpg-1.26.0.yaml") {
		t.Errorf("operator already present, should not be reinstalled:\n%s", got)
	}
}

const platformCM = `{"data":{"version":"1","storage.blockClass":"longhorn","storage.rwxClass":"longhorn","gateway.namespace":"zero-scale-system","gateway.service":"zero-scale-gateway","ingress.class":"nginx"}}`

func mutatingCalls(log string) []string {
	var out []string
	for _, line := range strings.Split(log, "\n") {
		if strings.HasPrefix(line, "kubectl ") && !strings.HasPrefix(line, "kubectl get ") {
			out = append(out, line)
		}
	}
	return out
}

func TestGatewayInstallDryRunAndHelmGuard(t *testing.T) {
	log := fakeKubectl(t, `  "get configmap zero-scale-platform -n zero-scale-system -o json") echo '`+platformCM+`' ;;`)
	if err := runGateway([]string{"install", "--image", "reg/zs:1"}); err != nil {
		t.Fatal(err)
	}
	if m := mutatingCalls(readLog(t, log)); len(m) != 0 {
		t.Errorf("dry run changed things: %v", m)
	}

	helmDeploy := `{"metadata":{"labels":{"app.kubernetes.io/managed-by":"Helm"}}}`
	log = fakeKubectl(t, `  "get configmap zero-scale-platform -n zero-scale-system -o json") echo '`+platformCM+`' ;;
  "get deployment zero-scale-gateway -n zero-scale-system -o json") echo '`+helmDeploy+`' ;;`)
	if err := runGateway([]string{"install", "--image", "reg/zs:1", "--yes"}); err != nil {
		t.Fatal(err)
	}
	got := readLog(t, log)
	if strings.Contains(got, "kind: Deployment") {
		t.Error("must not touch a Helm-managed gateway")
	}
	if !strings.Contains(got, `gateway.managedBy: "Helm"`) {
		t.Errorf("Helm gateway not recorded:\n%s", got)
	}
}

func TestIngressAddHost(t *testing.T) {
	httpOnly := `{"spec":{"acme":{"solvers":[{"http01":{}}]}}}`
	log := fakeKubectl(t, `  "get configmap zero-scale-platform -n zero-scale-system -o json") echo '`+platformCM+`' ;;
  "get clusterissuer le-http -o json") echo '`+httpOnly+`' ;;`)
	if err := runIngress([]string{"add-host", "*.apps.example.com", "--issuer", "le-http"}); err == nil || !strings.Contains(err.Error(), "DNS-01") {
		t.Fatalf("expected DNS-01 refusal, got %v", err)
	}
	if err := runIngress([]string{"add-host", "--tls-secret", "wildcard-tls", "*.apps.example.com"}); err != nil {
		t.Fatal(err)
	}
	got := readLog(t, log)
	for _, want := range []string{
		"name: zero-scale-wildcard-apps-example-com",
		"secretName: wildcard-tls",
		"ingressClassName: nginx",
		`ingress.hosts: "*.apps.example.com"`,
		`ingress.tls: "*.apps.example.com=secret:wildcard-tls"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q:\n%s", want, got)
		}
	}
}

func TestApplySkipsIngressWhenWildcardCovers(t *testing.T) {
	cm := `{"data":{"version":"1","gateway.namespace":"zero-scale-system","ingress.hosts":"*.apps.example.com"}}`
	fakeKubectl(t, `  "get configmap zero-scale-platform -n zero-scale-system -o json") echo '`+cm+`' ;;`)
	if !hostsCoveredByRoutes([]string{"fdm.apps.example.com"}, "zero-scale-system") {
		t.Error("wildcard should cover the app host")
	}
	if hostsCoveredByRoutes([]string{"fdm.apps.example.com", "fdm.other.com"}, "zero-scale-system") {
		t.Error("one uncovered host means a per-app Ingress is needed")
	}
	if hostsCoveredByRoutes([]string{"fdm.apps.example.com"}, "qa") {
		t.Error("routes point at the platform gateway, not another namespace's")
	}
}

func TestDeleteAppPurge(t *testing.T) {
	appCM := `{"data":{"host":"fdm.ffxm.cc","hosts":"fdm.ffxm.cc,fleet.example.com","database_engine":"postgres"}}`
	cnpg := `{"data":{"version":"1","postgres.operator":"cnpg","postgres.cluster":"cnpg-system/ha-postgres","postgres.rwService":"ha-postgres-rw.cnpg-system.svc.cluster.local:5432"}}`
	log := fakeKubectl(t, `  "get configmap zero-scale-app-fdm -n default -o json") echo '`+appCM+`' ;;
  "get configmap zero-scale-platform -n zero-scale-system -o json") echo '`+cnpg+`' ;;
  "get pods -n default -l app=zero-scale-gateway --field-selector=status.phase=Running -o jsonpath={.items[*].metadata.name}") printf 'gw-a gw-b' ;;
  "get pods -n default -l app=zero-scale-gateway --field-selector=status.phase=Running -o jsonpath={.items[0].metadata.name}") printf gw-a ;;
  "get pods -n cnpg-system -l cnpg.io/cluster=ha-postgres,cnpg.io/instanceRole=primary "*) printf ha-postgres-1 ;;
  "exec gw-"*|"delete "*) exit 0 ;;`)
	if err := runDeleteApp([]string{"fdm", "--drop-database"}, "default"); err == nil {
		t.Error("--drop-database without --purge must fail")
	}
	if err := runDeleteApp([]string{"fdm", "--purge", "--drop-database"}, "default"); err != nil {
		t.Fatal(err)
	}
	got := readLog(t, log)
	for _, want := range []string{
		"exec gw-a -n default -c systemd-runner -- systemctl stop app-fdm.ffxm.cc.service",
		"exec gw-b -n default -c systemd-runner -- systemctl stop app-fdm.ffxm.cc.service",
		"rm -f /opt/apps/fleet.example.com",
		"DROP DATABASE %I WITH (FORCE)",
		"delete ingress zero-scale-fdm -n default",
		"delete secret fdm-db-secret -n default",
		"delete configmap zero-scale-app-fdm -n default",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q:\n%s", want, got)
		}
	}
}
