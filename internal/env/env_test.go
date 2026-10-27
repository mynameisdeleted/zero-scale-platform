package env

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestValidateName(t *testing.T) {
	for _, ok := range []string{"qa", "feat-x", "pr-1234", "a"} {
		if err := ValidateName(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "QA", "-qa", "qa-", "feat_x", "a-very-long-branch-name"} {
		if ValidateName(bad) == nil {
			t.Errorf("%q should be rejected", bad)
		}
	}
}

func TestExpandAndCheckHost(t *testing.T) {
	var prod *Config
	if got := prod.Expand("fdm${ENV_SUFFIX}.apps.example.com"); got != "fdm.apps.example.com" {
		t.Errorf("no env: %s", got)
	}
	primary := &Config{Name: "prod", Primary: true}
	if got := primary.Expand("fdm${ENV_SUFFIX}.apps.example.com"); got != "fdm.apps.example.com" {
		t.Errorf("primary: %s", got)
	}
	qa := &Config{Name: "qa"}
	if got := qa.Expand("fdm${ENV_SUFFIX}.apps.example.com"); got != "fdm-qa.apps.example.com" {
		t.Errorf("qa: %s", got)
	}
	if got := qa.Expand("https://${ENV}.example.com"); got != "https://qa.example.com" {
		t.Errorf("qa ${ENV}: %s", got)
	}
	if err := qa.CheckHost("fdm-qa.apps.example.com"); err != nil {
		t.Error(err)
	}
	for _, h := range []string{"fdm.apps.example.com", "*.apps.example.com", "qa.example.com"} {
		if qa.CheckHost(h) == nil {
			t.Errorf("%s must be rejected in qa", h)
		}
	}
	routed := &Config{Name: "qa", Overrides: map[string]string{"ingress.hosts": "*.qa.apps.example.com"}}
	if routed.CheckHost("fdm.qa.apps.example.com") != nil || routed.CheckHost("fdm.apps.example.com") == nil {
		t.Error("hosts under the env's own wildcard are allowed, others not")
	}
	if primary.CheckHost("fdm.apps.example.com") != nil {
		t.Error("primary may use any host")
	}
}

func TestParseTTL(t *testing.T) {
	cases := map[string]time.Duration{"": 0, "0": 0, "7d": 7 * 24 * time.Hour, "36h": 36 * time.Hour}
	for in, want := range cases {
		if got, err := ParseTTL(in); err != nil || got != want {
			t.Errorf("%q: %v %v", in, got, err)
		}
	}
	for _, bad := range []string{"x", "-1d", "0d", "-3h"} {
		if _, err := ParseTTL(bad); err == nil {
			t.Errorf("%q should fail", bad)
		}
	}
}

func TestDBNames(t *testing.T) {
	db, user := (*Config)(nil).DBNames("fdm", "1234abcd")
	if db != "db_fdm_1234abcd" || user != "u_fdm_1234abcd" {
		t.Errorf("unscoped: %s %s", db, user)
	}
	db, user = (&Config{Name: "prod", Primary: true}).DBNames("fdm", "1234abcd")
	if db != "db_fdm_1234abcd" {
		t.Errorf("primary must keep the original names: %s", db)
	}
	db, user = (&Config{Name: "feat-x"}).DBNames("fdmfeatx", "1234abcd")
	if db != "db_feat_x_fdmfeatx_1234abcd" || user != "u_feat_x_fdmfeatx_1234abcd" {
		t.Errorf("scoped: %s %s", db, user)
	}
	_, user = (&Config{Name: "abcdefghijklmnop"}).DBNames("averyveryverylongappname", "1234abcd")
	if len(user) > 32 {
		t.Errorf("user %s is %d chars", user, len(user))
	}
}

func TestRoundTripAndOverlay(t *testing.T) {
	exp := time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC)
	c := &Config{Name: "qa", Expires: exp, Overrides: map[string]string{"postgres.operator": "cnpg", "postgres.cluster": "qa-db/pg", "postgres.rwService": "pg-rw.qa-db.svc:5432"}}
	back, err := FromData(c.ToData())
	if err != nil {
		t.Fatal(err)
	}
	if back.Name != "qa" || !back.Expires.Equal(exp) || back.Overrides["postgres.cluster"] != "qa-db/pg" {
		t.Errorf("round trip: %+v", back)
	}
	if !back.Expired(exp.Add(time.Second)) || back.Expired(exp.Add(-time.Second)) {
		t.Error("Expired")
	}

	plat := map[string]string{
		"postgres.operator": "zalando", "postgres.cluster": "db/main", "postgres.rwService": "main.db.svc:5432", "postgres.adminSecret": "x",
		"ingress.hosts": "*.apps.example.com", "ingress.tls": "*.apps.example.com=secret:wild", "ingress.class": "nginx",
		"gateway.namespace": "zero-scale-system", "storage.rwxClass": "longhorn-ha-rwx",
	}
	got := back.Overlay(plat, "qa")
	if got["postgres.cluster"] != "qa-db/pg" || got["postgres.adminSecret"] != "" {
		t.Errorf("DB override must replace the whole target: %v", got)
	}
	if got["ingress.hosts"] != "" || got["gateway.namespace"] != "qa" || got["ingress.class"] != "nginx" || got["storage.rwxClass"] != "longhorn-ha-rwx" {
		t.Errorf("overlay: %v", got)
	}
	if (&Config{Name: "prod", Primary: true}).Overlay(plat, "prod")["ingress.hosts"] == "" {
		t.Error("the primary env keeps the platform routes")
	}
}

func TestManifest(t *testing.T) {
	c := &Config{Name: "feat-x", Expires: time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC)}
	m := c.Manifest("zs-feat-x", DefaultQuota)
	var kinds []string
	for _, doc := range strings.Split(m, "\n---\n") {
		for _, line := range strings.Split(doc, "\n") {
			if strings.HasPrefix(line, "kind: ") {
				kinds = append(kinds, strings.TrimPrefix(line, "kind: "))
			}
		}
	}
	if os.Getenv("DUMP_MANIFESTS") != "" {
		os.WriteFile(os.Getenv("DUMP_MANIFESTS")+"/env.yaml", []byte(m), 0644)
	}
	if strings.Join(kinds, ",") != "Namespace,ResourceQuota,ConfigMap" {
		t.Errorf("kinds %v", kinds)
	}
	for _, want := range []string{"zero-scale.io/env: feat-x", `zero-scale.io/expires: "2026-11-03T00:00:00Z"`, `env.name: "feat-x"`} {
		if !strings.Contains(m, want) {
			t.Errorf("missing %q in\n%s", want, m)
		}
	}
}
