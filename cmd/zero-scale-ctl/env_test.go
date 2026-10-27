package main

import (
	"strings"
	"testing"
	"time"

	"git.fairfaxmedia.net/open-cream-cheese/zero-scale-platform/internal/platform"
)

const pgPlatformCM = `{"data":{"version":"1","storage.rwxClass":"longhorn-rwx","gateway.namespace":"zero-scale-system","ingress.class":"nginx","ingress.hosts":"*.apps.example.com","postgres.operator":"cnpg","postgres.cluster":"cnpg-system/ha-postgres","postgres.rwService":"ha-postgres-rw.cnpg-system.svc.cluster.local:5432"}}`

func fixedNow(t *testing.T, ts string) {
	at, _ := time.Parse(time.RFC3339, ts)
	old := now
	now = func() time.Time { return at }
	t.Cleanup(func() { now = old })
}

func TestEnvCreate(t *testing.T) {
	fixedNow(t, "2026-10-27T10:00:00Z")
	base := `  "get configmap zero-scale-platform -n zero-scale-system -o json") echo '` + pgPlatformCM + `' ;;
  "get namespace -l zero-scale.io/env=feat-x -o json") echo '{"items":[]}' ;;
  "get namespace feat-x -o json") echo 'Error from server (NotFound)' >&2; exit 1 ;;
  "get deployment -n zero-scale-system -l app=zero-scale-gateway -o jsonpath="*) printf reg/zs:7 ;;
  "-n feat-x rollout status "*) exit 0 ;;`
	log := fakeKubectl(t, base)
	if err := runEnv([]string{"create", "feat-x", "--ttl", "3d"}); err != nil {
		t.Fatal(err)
	}
	if m := mutatingCalls(readLog(t, log)); len(m) != 0 {
		t.Errorf("dry run changed things: %v", m)
	}

	log = fakeKubectl(t, base)
	if err := runEnv([]string{"create", "feat-x", "--ttl", "3d", "--yes"}); err != nil {
		t.Fatal(err)
	}
	got := readLog(t, log)
	for _, want := range []string{
		"zero-scale.io/env: feat-x", `zero-scale.io/expires: "2026-10-30T10:00:00Z"`, "kind: ResourceQuota",
		`env.name: "feat-x"`, "image: reg/zs:7", "namespace: feat-x", "replicas: 1", "storageClassName: longhorn-rwx",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q", want)
		}
	}
	if strings.Contains(got, "zero-scale-platform\n") && strings.Contains(got, "gateway.namespace: \"feat-x\"") {
		t.Error("an env's gateway must not replace the platform gateway")
	}
}

func TestEnvCreateRefusesForeignNamespace(t *testing.T) {
	fakeKubectl(t, `  "get namespace team-a -o json") echo '{"metadata":{"name":"team-a","labels":{}}}' ;;`)
	if err := runEnv([]string{"create", "team-a"}); err == nil || !strings.Contains(err.Error(), "--adopt") {
		t.Errorf("want --adopt error, got %v", err)
	}
	fakeKubectl(t, `  "get namespace qa -o json") echo '{"metadata":{"name":"qa","labels":{"zero-scale.io/env":"staging"}}}' ;;`)
	if err := runEnv([]string{"create", "qa"}); err == nil || !strings.Contains(err.Error(), "staging") {
		t.Errorf("want ownership error, got %v", err)
	}
}

const featEnvCM = `{"data":{"env.name":"feat-x","env.expires":"2026-10-30T10:00:00Z"}}`

func TestApplyInEnvExpandsHostsAndScopesDB(t *testing.T) {
	log := fakeKubectl(t, `  "get configmap zero-scale-platform -n zero-scale-system -o json") echo '`+pgPlatformCM+`' ;;
  "get configmap zero-scale-env -n feat-x -o json") echo '`+featEnvCM+`' ;;
  "get pods -n cnpg-system -l cnpg.io/cluster=ha-postgres,cnpg.io/instanceRole=primary "*) printf ha-postgres-1 ;;`)
	app := &AppConfig{Hostnames: []string{"fdm${ENV_SUFFIX}.apps.example.com"}, Env: map[string]string{"BASE_URL": "https://fdm${ENV_SUFFIX}.apps.example.com"},
		Database: &DatabaseConfig{Engine: "postgres"}}
	if err := bindEnv(app, "feat-x"); err != nil {
		t.Fatal(err)
	}
	if app.CanonicalHost() != "fdm-feat-x.apps.example.com" || app.Env["BASE_URL"] != "https://fdm-feat-x.apps.example.com" {
		t.Errorf("expansion: %v %v", app.Hostnames, app.Env)
	}
	if !strings.HasPrefix(app.DBName(), "db_feat_x_fdmfeatx_") || len(app.DBUser()) > 32 {
		t.Errorf("names: %s %s", app.DBName(), app.DBUser())
	}
	if _, _, _, _, err := ProvisionPostgres(app, "feat-x"); err != nil {
		t.Fatal(err)
	}
	if got := readLog(t, log); !strings.Contains(got, "zero-scale: env=feat-x namespace=feat-x app=fdmfeatx") {
		t.Errorf("owner comment missing:\n%s", got)
	}
	if hostsCoveredByRoutes(app.Hostnames, "feat-x") {
		t.Error("prod's wildcard route must not cover an env's hosts")
	}

	bad := &AppConfig{Hostnames: []string{"fdm.apps.example.com"}}
	if err := bindEnv(bad, "feat-x"); err == nil {
		t.Error("an unsuffixed host must be refused in a scoped env")
	}
}

func TestEnvDelete(t *testing.T) {
	apps := `{"items":[{"metadata":{"name":"zero-scale-app-fdmfeatx"},"data":{"host":"fdm-feat-x.apps.example.com","database_engine":"postgres"}},{"metadata":{"name":"zero-scale-app-web"},"data":{"host":"web-feat-x.apps.example.com"}}]}`
	script := `  "get configmap zero-scale-platform -n zero-scale-system -o json") echo '` + pgPlatformCM + `' ;;
  "get namespace -l zero-scale.io/env=feat-x -o json") echo '{"items":[{"metadata":{"name":"feat-x","labels":{"zero-scale.io/env":"feat-x"}}}]}' ;;
  "get configmap zero-scale-env -n feat-x -o json") echo '` + featEnvCM + `' ;;
  "get configmap -n feat-x -l zero-scale-app=true -o json") echo '` + apps + `' ;;
  "get pvc -n feat-x -o jsonpath="*) printf 'pvc-111 pvc-222' ;;
  "get pods -n cnpg-system -l cnpg.io/cluster=ha-postgres,cnpg.io/instanceRole=primary "*) printf ha-postgres-1 ;;
  "patch pv "*|"delete namespace "*) exit 0 ;;`
	log := fakeKubectl(t, script)
	if err := runEnv([]string{"delete", "feat-x"}); err != nil {
		t.Fatal(err)
	}
	if m := mutatingCalls(readLog(t, log)); len(m) != 0 {
		t.Errorf("dry run changed things: %v", m)
	}

	log = fakeKubectl(t, script)
	if err := runEnv([]string{"delete", "feat-x", "--yes"}); err != nil {
		t.Fatal(err)
	}
	got := readLog(t, log)
	for _, want := range []string{
		"zero-scale: env=feat-x namespace=feat-x app=fdmfeatx",
		`kubectl patch pv pvc-111 -p {"spec":{"persistentVolumeReclaimPolicy":"Delete"}}`,
		"kubectl patch pv pvc-222",
		"kubectl delete namespace feat-x",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q:\n%s", want, got)
		}
	}
	if strings.Index(got, "DROP DATABASE") > strings.Index(got, "delete namespace") {
		t.Error("databases must be dropped before the namespace goes")
	}
}

func TestEnvDeleteRefusesPrimary(t *testing.T) {
	fakeKubectl(t, `  "get namespace -l zero-scale.io/env=prod -o json") echo '{"items":[{"metadata":{"name":"apps","labels":{"zero-scale.io/env":"prod"}}}]}' ;;
  "get configmap zero-scale-env -n apps -o json") echo '{"data":{"env.name":"prod","env.primary":"true"}}' ;;`)
	if err := runEnv([]string{"delete", "prod", "--yes"}); err == nil || !strings.Contains(err.Error(), "primary") {
		t.Errorf("want primary refusal, got %v", err)
	}
}

func TestEnvGC(t *testing.T) {
	fixedNow(t, "2026-11-01T00:00:00Z")
	list := `{"items":[
 {"metadata":{"name":"feat-x","labels":{"zero-scale.io/env":"feat-x"},"annotations":{"zero-scale.io/expires":"2026-10-30T10:00:00Z"}}},
 {"metadata":{"name":"feat-y","labels":{"zero-scale.io/env":"feat-y"},"annotations":{"zero-scale.io/expires":"2026-12-01T00:00:00Z"}}},
 {"metadata":{"name":"qa","labels":{"zero-scale.io/env":"qa"}}}]}`
	log := fakeKubectl(t, `  "get namespace -l zero-scale.io/env -o json") echo '`+list+`' ;;
  "get namespace -l zero-scale.io/env=feat-x -o json") echo '{"items":[{"metadata":{"name":"feat-x"}}]}' ;;
  "get configmap zero-scale-env -n feat-x -o json") echo '`+featEnvCM+`' ;;
  "get configmap -n feat-x -l zero-scale-app=true -o json") echo '{"items":[]}' ;;
  "get pvc -n feat-x -o jsonpath="*) printf '' ;;
  "delete namespace "*) exit 0 ;;`)
	if err := runEnv([]string{"gc", "--yes"}); err != nil {
		t.Fatal(err)
	}
	m := mutatingCalls(readLog(t, log))
	if len(m) != 1 || m[0] != "kubectl delete namespace feat-x --wait=false" {
		t.Errorf("gc should delete only feat-x: %v", m)
	}
}

func TestSharedOAuthIngress(t *testing.T) {
	cm := `{"data":{"version":"1","ingress.class":"nginx","oauth.host":"auth.apps.example.com","oauth.namespace":"zero-scale-system"}}`
	log := fakeKubectl(t, `  "get configmap zero-scale-platform -n zero-scale-system -o json") echo '`+cm+`' ;;`)
	if err := ApplyIngress("zero-scale-fdmqa", "qa", "zero-scale-gateway", 80, []string{"fdm-qa.apps.example.com"}, "team"); err != nil {
		t.Fatal(err)
	}
	got := readLog(t, log)
	for _, want := range []string{
		`auth-url: "http://oauth2-proxy-team.zero-scale-system.svc.cluster.local:4180/oauth2/auth"`,
		`auth-signin: "https://auth.apps.example.com/oauth2/start?rd=https://$host$escaped_request_uri"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q:\n%s", want, got)
		}
	}
	if err := ReconcileOAuthSetup("team", "https://git.example.com", "", []string{"fdm.other.org"}, "qa"); err == nil {
		t.Error("a host outside the cookie domain must be refused")
	}
}

func TestSharedOAuthProxyArgs(t *testing.T) {
	log := fakeKubectl(t, `  "get service shared-redis -n zero-scale-system -o json") echo '{}' ;;`)
	shared := sharedOAuthConfig("auth.apps.example.com", "zero-scale-system")
	if err := deploySharedOAuth2Proxy("team", "https://git.example.com", "", []string{"https://auth.apps.example.com/oauth2/callback"}, "zero-scale-system", shared); err != nil {
		t.Fatal(err)
	}
	got := readLog(t, log)
	for _, want := range []string{"--redirect-url=https://auth.apps.example.com/oauth2/callback", "--cookie-domain=.apps.example.com", "--whitelist-domain=.apps.example.com"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q", want)
		}
	}
}

func sharedOAuthConfig(host, ns string) platform.OAuthConfig {
	return platform.OAuthConfig{Host: host, Namespace: ns}
}

func TestInstallPostgresIntoEnv(t *testing.T) {
	log := fakeKubectl(t, `  "get nodes -o json") cat "$F/nodes.json" ;;
  "get crd clusters.postgresql.cnpg.io") exit 0 ;;
  "get namespace -l zero-scale.io/env=qa -o json") echo '{"items":[{"metadata":{"name":"qa","labels":{"zero-scale.io/env":"qa"}}}]}' ;;
  "get configmap zero-scale-env -n qa -o json") echo '{"data":{"env.name":"qa"}}' ;;
  "wait "*|"-n qa wait "*) exit 0 ;;`)
	if err := runInstall([]string{"postgres", "--env", "qa", "--instances", "2", "--size", "5Gi", "--storage-class", "longhorn", "--yes"}); err != nil {
		t.Fatal(err)
	}
	got := readLog(t, log)
	for _, want := range []string{
		"name: qa-postgres\n  namespace: qa", "instances: 2",
		"name: zero-scale-env", `postgres.cluster: "qa/qa-postgres"`, `postgres.rwService: "qa-postgres-rw.qa.svc.cluster.local:5432"`, `env.name: "qa"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "kind: Namespace") {
		t.Error("must not re-apply the env namespace (it would drop its labels)")
	}
	if strings.Contains(got, "name: zero-scale-platform") {
		t.Error("an env cluster must not replace the platform's Postgres")
	}
}

func TestExtractNamespace(t *testing.T) {
	ns, args := extractNamespace([]string{"zsctl", "status", "-n", "qa", "mock-qa.ffxm.cc"}, "default", false)
	if ns != "qa" || strings.Join(args, " ") != "zsctl status mock-qa.ffxm.cc" {
		t.Errorf("status: %s %v", ns, args)
	}
	ns, args = extractNamespace([]string{"zsctl", "logs", "mock", "-n", "100", "--namespace=qa"}, "default", true)
	if ns != "qa" || strings.Join(args, " ") != "zsctl logs mock -n 100" {
		t.Errorf("logs: %s %v", ns, args)
	}
	ns, _ = extractNamespace([]string{"zsctl", "logs", "mock", "-n", "qa"}, "default", true)
	if ns != "qa" {
		t.Errorf("logs -n qa: %s", ns)
	}
}
