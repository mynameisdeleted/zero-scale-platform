package platform

import (
	"fmt"
	"regexp"
	"strings"
)

var (
	identRe    = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,62}$`)
	passwordRe = regexp.MustCompile(`^[A-Za-z0-9_\-.~+/=]{8,128}$`)
	labelRe    = regexp.MustCompile(`^[A-Za-z0-9_.\-/=]{0,128}$`)
)

// ValidateIdentifier checks a database or user name is safe to quote.
func ValidateIdentifier(s string) error {
	if !identRe.MatchString(s) {
		return fmt.Errorf("invalid database identifier %q (want [a-z_][a-z0-9_]{0,62})", s)
	}
	return nil
}

// ValidatePassword rejects passwords that would need escaping in SQL or shell.
func ValidatePassword(s string) error {
	if !passwordRe.MatchString(s) {
		return fmt.Errorf("database password must be 8-128 characters of [A-Za-z0-9_-.~+/=]")
	}
	return nil
}

// Owner identifies who a database belongs to, recorded where the engine allows.
type Owner struct {
	App       string
	Namespace string
	Env       string
}

func (o Owner) comment() string {
	parts := []string{"zero-scale:"}
	if o.Env != "" {
		parts = append(parts, "env="+o.Env)
	}
	parts = append(parts, "namespace="+o.Namespace, "app="+o.App)
	return strings.Join(parts, " ")
}

// PostgresProvisionSQL returns an idempotent psql script that creates or updates
// the login role, creates the database owned by it, and records the owner.
func PostgresProvisionSQL(db, user, password string, owner Owner) (string, error) {
	for _, id := range []string{db, user} {
		if err := ValidateIdentifier(id); err != nil {
			return "", err
		}
	}
	if err := ValidatePassword(password); err != nil {
		return "", err
	}
	if !labelRe.MatchString(owner.App) || !labelRe.MatchString(owner.Namespace) || !labelRe.MatchString(owner.Env) {
		return "", fmt.Errorf("invalid owner %+v", owner)
	}
	return fmt.Sprintf(`DO $zs$
BEGIN
  IF NOT EXISTS (SELECT FROM pg_catalog.pg_roles WHERE rolname = '%[2]s') THEN
    CREATE ROLE "%[2]s" WITH LOGIN PASSWORD '%[3]s';
  ELSE
    ALTER ROLE "%[2]s" WITH LOGIN PASSWORD '%[3]s';
  END IF;
END
$zs$;
SELECT format('CREATE DATABASE %%I OWNER %%I', '%[1]s', '%[2]s')
  WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname = '%[1]s')\gexec
COMMENT ON DATABASE "%[1]s" IS '%[4]s';
`, db, user, password, owner.comment()), nil
}

// PostgresDropSQL drops a database and its role, but only when the database's
// comment shows it belongs to owner. It is a no-op for anything else.
func PostgresDropSQL(db, user string, owner Owner) (string, error) {
	for _, id := range []string{db, user} {
		if err := ValidateIdentifier(id); err != nil {
			return "", err
		}
	}
	return fmt.Sprintf(`SELECT format('DROP DATABASE %%I WITH (FORCE)', d.datname)
  FROM pg_database d
  WHERE d.datname = '%[1]s' AND shobj_description(d.oid, 'pg_database') = '%[3]s'\gexec
SELECT format('DROP ROLE %%I', '%[2]s')
  WHERE EXISTS (SELECT FROM pg_roles WHERE rolname = '%[2]s')
    AND NOT EXISTS (SELECT FROM pg_database WHERE datname = '%[1]s')\gexec
`, db, user, owner.comment()), nil
}

// MySQLProvisionSQL returns idempotent SQL creating the schema and user.
func MySQLProvisionSQL(db, user, password string) (string, error) {
	for _, id := range []string{db, user} {
		if err := ValidateIdentifier(id); err != nil {
			return "", err
		}
	}
	if err := ValidatePassword(password); err != nil {
		return "", err
	}
	return fmt.Sprintf("CREATE DATABASE IF NOT EXISTS `%[1]s` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;\n"+
		"CREATE USER IF NOT EXISTS '%[2]s'@'%%' IDENTIFIED BY '%[3]s';\n"+
		"ALTER USER '%[2]s'@'%%' IDENTIFIED BY '%[3]s';\n"+
		"GRANT ALL PRIVILEGES ON `%[1]s`.* TO '%[2]s'@'%%';\n", db, user, password), nil
}

// MySQLDropSQL drops the schema and user.
func MySQLDropSQL(db, user string) (string, error) {
	for _, id := range []string{db, user} {
		if err := ValidateIdentifier(id); err != nil {
			return "", err
		}
	}
	return fmt.Sprintf("DROP DATABASE IF EXISTS `%s`;\nDROP USER IF EXISTS '%s'@'%%';\n", db, user), nil
}

// MySQLClientScript is the `sh -c` script run in the MySQL exec pod. It reads the
// admin password from the first line of stdin, so it never appears in argv, then
// feeds the rest of stdin to whichever client the image ships.
func MySQLClientScript(host, port, user string) string {
	return fmt.Sprintf(`read -r MYSQL_PWD; export MYSQL_PWD; C=$(command -v mysql || command -v mariadb); exec "$C" -h %s -P %s -u %s`,
		shellQuote(host), shellQuote(port), shellQuote(user))
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
