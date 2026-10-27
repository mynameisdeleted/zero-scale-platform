package platform

import (
	"strings"
	"testing"
)

func TestPostgresProvisionSQL(t *testing.T) {
	sql, err := PostgresProvisionSQL("db_fdm_1a2b3c4d", "u_fdm_1a2b3c4d", "Secret123", Owner{App: "fdm", Namespace: "qa", Env: "qa"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`CREATE ROLE "u_fdm_1a2b3c4d" WITH LOGIN PASSWORD 'Secret123'`,
		`format('CREATE DATABASE %I OWNER %I', 'db_fdm_1a2b3c4d', 'u_fdm_1a2b3c4d')`,
		`\gexec`,
		`COMMENT ON DATABASE "db_fdm_1a2b3c4d" IS 'zero-scale: env=qa namespace=qa app=fdm'`,
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("missing %q in:\n%s", want, sql)
		}
	}
}

func TestRejectsUnsafeInput(t *testing.T) {
	if _, err := PostgresProvisionSQL(`db"; DROP`, "u", "Secret123", Owner{}); err == nil {
		t.Error("expected identifier error")
	}
	if _, err := PostgresProvisionSQL("db", "u", "it's$$bad", Owner{}); err == nil {
		t.Error("expected password error")
	}
	if _, err := MySQLProvisionSQL("db", "u", "short"); err == nil {
		t.Error("expected password length error")
	}
	if _, err := PostgresProvisionSQL("db", "u", "Secret123", Owner{App: "a'b"}); err == nil {
		t.Error("expected owner error")
	}
}

func TestPostgresDropSQLChecksOwner(t *testing.T) {
	sql, err := PostgresDropSQL("db_x", "u_x", Owner{App: "x", Namespace: "feat-1", Env: "feat-1"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sql, "shobj_description(d.oid, 'pg_database') = 'zero-scale: env=feat-1 namespace=feat-1 app=x'") {
		t.Errorf("drop not guarded by owner comment:\n%s", sql)
	}
}

func TestMySQLClientScriptKeepsPasswordOutOfArgv(t *testing.T) {
	s := MySQLClientScript("mysql-ha-haproxy.databases.svc.cluster.local", "3306", "root")
	if !strings.HasPrefix(s, "read -r MYSQL_PWD;") || strings.Contains(s, "-p") {
		t.Errorf("script = %s", s)
	}
	if got := shellQuote("a'b"); got != `'a'\''b'` {
		t.Errorf("shellQuote = %s", got)
	}
}
