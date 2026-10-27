package platform

import (
	"fmt"
	"strings"
)

// Operator describes how to find, grade and reach one kind of database cluster.
type Operator struct {
	ID      string // value stored in the platform ConfigMap
	Engine  string // "postgres" or "mysql"
	Display string
	CRD     string // empty for non-CRD sources

	instances func(o object) int
	ready     func(o object) string // "yes", "no", "unknown"
	ha        func(o object, instances int) bool
	rwService func(o object, ns, name string) string
	admin     func(o object, name string) (secret, key, user string) // MySQL only

	// ExecSelectors are label selectors (tried in order) for the pod that runs SQL.
	ExecSelectors func(t *DBTarget) []string
	// ExecContainer is the container inside that pod.
	ExecContainer string
}

func svc(name, ns, port string) string {
	return fmt.Sprintf("%s.%s.svc.cluster.local:%s", name, ns, port)
}

func instancesAt(path ...string) func(object) int {
	return func(o object) int {
		if n, ok := o.num(path...); ok {
			return n
		}
		return 1
	}
}

func sumReplicas(o object) int {
	total := 0
	for _, set := range o.list("spec", "instances") {
		if n, ok := set.num("replicas"); ok {
			total += n
		} else {
			total++
		}
	}
	if total == 0 {
		return 1
	}
	return total
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func minTwo(_ object, n int) bool { return n >= 2 }

// PostgresOperators are the Postgres operators zero-scale-ctl can provision on.
var PostgresOperators = []*Operator{
	{
		ID: "cnpg", Engine: "postgres", Display: "CloudNativePG", CRD: "clusters.postgresql.cnpg.io",
		instances: instancesAt("spec", "instances"),
		ready: func(o object) string {
			want, _ := o.num("spec", "instances")
			got, _ := o.num("status", "readyInstances")
			return yesNo(want > 0 && got >= want)
		},
		ha:        minTwo,
		rwService: func(_ object, ns, name string) string { return svc(name+"-rw", ns, "5432") },
		ExecSelectors: func(t *DBTarget) []string {
			return []string{
				"cnpg.io/cluster=" + t.Name + ",cnpg.io/instanceRole=primary",
				"cnpg.io/cluster=" + t.Name + ",role=primary",
			}
		},
		ExecContainer: "postgres",
	},
	{
		ID: "zalando", Engine: "postgres", Display: "Zalando postgres-operator", CRD: "postgresqls.acid.zalan.do",
		instances: instancesAt("spec", "numberOfInstances"),
		ready:     func(o object) string { return yesNo(o.str("status", "PostgresClusterStatus") == "Running") },
		ha:        minTwo,
		rwService: func(_ object, ns, name string) string { return svc(name, ns, "5432") },
		ExecSelectors: func(t *DBTarget) []string {
			return []string{"cluster-name=" + t.Name + ",spilo-role=master"}
		},
		ExecContainer: "postgres",
	},
	{
		ID: "crunchy", Engine: "postgres", Display: "Crunchy PGO", CRD: "postgresclusters.postgres-operator.crunchydata.com",
		instances: sumReplicas,
		ready: func(o object) string {
			want, got := sumReplicas(o), 0
			for _, s := range o.list("status", "instances") {
				n, _ := s.num("readyReplicas")
				got += n
			}
			return yesNo(got >= want)
		},
		ha:        minTwo,
		rwService: func(_ object, ns, name string) string { return svc(name+"-primary", ns, "5432") },
		ExecSelectors: func(t *DBTarget) []string {
			return []string{"postgres-operator.crunchydata.com/cluster=" + t.Name + ",postgres-operator.crunchydata.com/role=master"}
		},
		ExecContainer: "database",
	},
	{
		ID: "percona-pg", Engine: "postgres", Display: "Percona Operator for PostgreSQL", CRD: "perconapgclusters.pgv2.percona.com",
		instances: sumReplicas,
		ready:     func(o object) string { return yesNo(o.str("status", "state") == "ready") },
		ha:        minTwo,
		rwService: func(_ object, ns, name string) string { return svc(name+"-primary", ns, "5432") },
		ExecSelectors: func(t *DBTarget) []string {
			return []string{"postgres-operator.crunchydata.com/cluster=" + t.Name + ",postgres-operator.crunchydata.com/role=master"}
		},
		ExecContainer: "database",
	},
	{
		ID: "stackgres", Engine: "postgres", Display: "StackGres", CRD: "sgclusters.stackgres.io",
		instances: instancesAt("spec", "instances"),
		ready:     func(object) string { return "unknown" },
		ha:        minTwo,
		rwService: func(_ object, ns, name string) string { return svc(name, ns, "5432") },
		ExecSelectors: func(t *DBTarget) []string {
			return []string{"app=StackGresCluster,stackgres.io/cluster-name=" + t.Name + ",role=master"}
		},
		ExecContainer: "patroni",
	},
}

// MySQLOperators are the MySQL-compatible sources zero-scale-ctl can provision on.
var MySQLOperators = []*Operator{
	{
		ID: "innodbcluster", Engine: "mysql", Display: "MySQL Operator (InnoDB Cluster)", CRD: "innodbclusters.mysql.oracle.com",
		instances: instancesAt("spec", "instances"),
		ready:     func(o object) string { return yesNo(o.str("status", "cluster", "status") == "ONLINE") },
		ha:        minTwo,
		rwService: func(_ object, ns, name string) string { return svc(name, ns, "3306") },
		admin: func(o object, name string) (string, string, string) {
			return o.str("spec", "secretName"), "rootPassword", "root"
		},
		ExecSelectors: func(t *DBTarget) []string {
			return []string{"mysql.oracle.com/cluster=" + t.Name + ",component=mysqld"}
		},
		ExecContainer: "mysql",
	},
	{
		ID: "pxc", Engine: "mysql", Display: "Percona XtraDB Cluster", CRD: "perconaxtradbclusters.pxc.percona.com",
		instances: instancesAt("spec", "pxc", "size"),
		ready:     func(o object) string { return yesNo(o.str("status", "state") == "ready") },
		ha:        minTwo,
		rwService: func(o object, ns, name string) string {
			if o.str("spec", "haproxy", "enabled") == "true" {
				return svc(name+"-haproxy", ns, "3306")
			}
			return svc(name+"-proxysql", ns, "3306")
		},
		admin: func(o object, name string) (string, string, string) {
			secret := o.str("spec", "secretsName")
			if secret == "" {
				secret = name + "-secrets"
			}
			return secret, "root", "root"
		},
		ExecSelectors: func(t *DBTarget) []string {
			return []string{"app.kubernetes.io/instance=" + t.Name + ",app.kubernetes.io/component=pxc"}
		},
		ExecContainer: "pxc",
	},
	{
		ID: "mariadb", Engine: "mysql", Display: "MariaDB Operator", CRD: "mariadbs.k8s.mariadb.com",
		instances: instancesAt("spec", "replicas"),
		ready: func(o object) string {
			for _, c := range o.list("status", "conditions") {
				if c.str("type") == "Ready" {
					return yesNo(c.str("status") == "True")
				}
			}
			return "unknown"
		},
		ha: func(o object, n int) bool {
			return n >= 2 && (o.str("spec", "galera", "enabled") == "true" || o.str("spec", "replication", "enabled") == "true")
		},
		rwService: func(o object, ns, name string) string {
			if o.str("spec", "galera", "enabled") == "true" || o.str("spec", "replication", "enabled") == "true" {
				return svc(name+"-primary", ns, "3306")
			}
			return svc(name, ns, "3306")
		},
		admin: func(o object, name string) (string, string, string) {
			return o.str("spec", "rootPasswordSecretKeyRef", "name"), o.str("spec", "rootPasswordSecretKeyRef", "key"), "root"
		},
		ExecSelectors: func(t *DBTarget) []string {
			return []string{"app.kubernetes.io/instance=" + t.Name + ",app.kubernetes.io/name=mariadb"}
		},
		ExecContainer: "mariadb",
	},
}

// legacyMySQL is the single-replica StatefulSet from deploy/shared-data-services.yaml.
var legacyMySQL = &Operator{
	ID: "shared-mysql", Engine: "mysql", Display: "shared-mysql StatefulSet (no replication)",
	instances: instancesAt("spec", "replicas"),
	ready: func(o object) string {
		want, _ := o.num("spec", "replicas")
		got, _ := o.num("status", "readyReplicas")
		return yesNo(want > 0 && got >= want)
	},
	ha:        func(object, int) bool { return false },
	rwService: func(_ object, ns, name string) string { return svc(name, ns, "3306") },
	admin: func(object, string) (string, string, string) {
		return "shared-mysql-admin", "MYSQL_ROOT_PASSWORD", "root"
	},
	ExecSelectors: func(t *DBTarget) []string { return []string{"app=" + t.Name} },
	ExecContainer: "mysql",
}

func (op *Operator) candidate(o object) DBCandidate {
	ns, name := o.str("metadata", "namespace"), o.str("metadata", "name")
	n := op.instances(o)
	c := DBCandidate{
		DBTarget:  DBTarget{Operator: op.ID, Namespace: ns, Name: name, RWService: op.rwService(o, ns, name)},
		Instances: n,
		Ready:     op.ready(o),
		Grade:     NotHA,
		Reason:    fmt.Sprintf("%s, %d instance(s)", op.Display, n),
	}
	if op.admin != nil {
		c.AdminSecret, c.AdminKey, c.AdminUser = op.admin(o, name)
	}
	if op.ha(o, n) {
		c.Grade = HA
	}
	if c.Ready == "no" {
		c.Reason += ", not ready"
	}
	return c
}

// OperatorFor returns the operator with the given engine and ID.
func OperatorFor(engine, id string) (*Operator, error) {
	list := PostgresOperators
	if engine == "mysql" {
		list = append(append([]*Operator{}, MySQLOperators...), legacyMySQL)
	}
	for _, op := range list {
		if op.ID == id {
			return op, nil
		}
	}
	var ids []string
	for _, op := range list {
		ids = append(ids, op.ID)
	}
	return nil, fmt.Errorf("unknown %s operator %q (known: %s)", engine, id, strings.Join(ids, ", "))
}
