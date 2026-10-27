package platform

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// fakeKube answers kubectl queries from testdata files keyed by the joined args.
type fakeKube map[string]string

func (f fakeKube) GetJSON(args ...string) ([]byte, error) {
	file, ok := f[strings.Join(args, " ")]
	if !ok {
		return nil, errors.New("not found")
	}
	return os.ReadFile(filepath.Join("testdata", file))
}

func fullCluster() fakeKube {
	return fakeKube{
		"get storageclass":                   "storageclass.json",
		"get crd":                            "crd.json",
		"get clusters.postgresql.cnpg.io -A": "cnpg.json",
		"get perconaxtradbclusters.pxc.percona.com -A":                   "pxc.json",
		"get statefulset -A -l app=shared-mysql":                         "shared-mysql.json",
		"get ingressclass":                                               "ingressclass.json",
		"get deployment -A -l app=zero-scale-gateway":                    "gateway.json",
		"get deployment -A -l app.kubernetes.io/name=zero-scale-gateway": "gateway.json",
		"get service -A -l app=shared-redis":                             "redis.json",
	}
}

func TestStorageGrades(t *testing.T) {
	r, err := Detect(fullCluster())
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]struct {
		grade Grade
		rwx   bool
	}{
		"longhorn":        {HA, true},
		"longhorn-single": {NotHA, true},
		"local-path":      {NotHA, false},
		"ceph-block":      {HA, false},
		"nfs":             {Unknown, true},
		"gp3":             {NotHA, false},
		"regional":        {HA, false},
	}
	if len(r.StorageClasses) != len(want) {
		t.Fatalf("got %d classes", len(r.StorageClasses))
	}
	for _, sc := range r.StorageClasses {
		w := want[sc.Name]
		if sc.Grade != w.grade || sc.RWX != w.rwx {
			t.Errorf("%s: grade %s rwx %v, want %s %v (%s)", sc.Name, sc.Grade, sc.RWX, w.grade, w.rwx, sc.Reason)
		}
	}
	for _, sc := range r.StorageClasses {
		if sc.NodeLocal != (sc.Name == "local-path") {
			t.Errorf("%s: NodeLocal = %v", sc.Name, sc.NodeLocal)
		}
	}
}

func TestDatabasesDetected(t *testing.T) {
	r, _ := Detect(fullCluster())
	if len(r.Postgres) != 2 || r.Postgres[0].Grade != HA || r.Postgres[1].Grade != NotHA {
		t.Fatalf("postgres: %+v", r.Postgres)
	}
	if got := r.Postgres[0].RWService; got != "ha-postgres-rw.cnpg-system.svc.cluster.local:5432" {
		t.Errorf("cnpg rw service = %s", got)
	}
	if len(r.MySQL) != 2 {
		t.Fatalf("mysql: %+v", r.MySQL)
	}
	pxc := r.MySQL[0]
	if pxc.Grade != HA || pxc.RWService != "mysql-ha-haproxy.databases.svc.cluster.local:3306" || pxc.AdminSecret != "mysql-ha-secrets" || pxc.AdminKey != "root" {
		t.Errorf("pxc: %+v", pxc)
	}
	if r.MySQL[1].Operator != "shared-mysql" || r.MySQL[1].Grade != NotHA {
		t.Errorf("legacy mysql: %+v", r.MySQL[1])
	}
	if len(r.Gateways) != 1 || r.Gateways[0].Grade != HA || r.Gateways[0].ManagedBy != "Helm" {
		t.Errorf("gateways: %+v", r.Gateways)
	}
}

func TestChooseFullCluster(t *testing.T) {
	r, _ := Detect(fullCluster())
	c, err := Choose(r, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if c.Storage.BlockClass != "longhorn" || c.Storage.RWXClass != "longhorn" {
		t.Errorf("storage: %+v", c.Storage)
	}
	if c.Postgres == nil || c.Postgres.Ref() != "cnpg-system/ha-postgres" {
		t.Errorf("postgres: %+v", c.Postgres)
	}
	if c.MySQL == nil || c.MySQL.Ref() != "databases/mysql-ha" {
		t.Errorf("mysql: %+v", c.MySQL)
	}
	if c.Ingress.Class != "public" || c.Gateway.Namespace != "default" || c.Redis != "shared-redis.default.svc.cluster.local:6379" {
		t.Errorf("rest: %+v", c)
	}
}

func TestChooseNothingHA(t *testing.T) {
	k := fakeKube{"get storageclass": "storageclass.json"}
	r, err := Detect(k)
	if err != nil {
		t.Fatal(err)
	}
	// Remove the HA classes to simulate a bare cluster.
	var bare []StorageClass
	for _, sc := range r.StorageClasses {
		if sc.Grade != HA {
			bare = append(bare, sc)
		}
	}
	r.StorageClasses = bare
	_, err = Choose(r, Options{})
	var m *MissingError
	if !errors.As(err, &m) || len(m.Missing) != 2 || !reflect.DeepEqual(m.Hints, []string{"zero-scale-ctl install longhorn"}) {
		t.Fatalf("err = %v", err)
	}
	if len(r.Warnings) == 0 {
		t.Error("expected a warning about CRDs")
	}
}

func TestChooseOverridesAndAmbiguity(t *testing.T) {
	r, _ := Detect(fullCluster())
	findClass(r, "longhorn").Default = false // no default left: longhorn, ceph-block, regional all HA
	_, err := Choose(r, Options{})
	var amb *AmbiguousError
	if !errors.As(err, &amb) || amb.Flag != "--storage-class" {
		t.Fatalf("err = %v", err)
	}
	c, err := Choose(r, Options{BlockClass: "ceph-block", RWXClass: "longhorn", MySQL: "none"})
	if err != nil {
		t.Fatal(err)
	}
	if c.Storage.BlockClass != "ceph-block" || c.Storage.RWXClass != "longhorn" || c.MySQL != nil {
		t.Errorf("got %+v", c)
	}
	if _, err := Choose(r, Options{BlockClass: "gp3"}); err == nil || !strings.Contains(err.Error(), "--allow-non-ha") {
		t.Errorf("non-HA override should need --allow-non-ha, got %v", err)
	}
	// Node-local storage is never platform storage, not even with --allow-non-ha.
	for _, o := range []Options{{BlockClass: "local-path"}, {BlockClass: "local-path", AllowNonHA: true}} {
		if _, err := Choose(r, o); err == nil || !strings.Contains(err.Error(), "node-local") {
			t.Errorf("%+v: want a node-local refusal, got %v", o, err)
		}
	}
	if _, err := Choose(r, Options{BlockClass: "longhorn", RWXClass: "ceph-block"}); err == nil {
		t.Error("ceph-block is not RWX")
	}
}

func TestConfigRoundTrip(t *testing.T) {
	r, _ := Detect(fullCluster())
	c, _ := Choose(r, Options{})
	c.Ingress.Hosts = []string{"*.apps.example.com", "status.example.com"}
	c.Ingress.TLS = map[string]string{"*.apps.example.com": "issuer:letsencrypt-dns"}
	back, err := FromData(c.ToData())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(c, back) {
		t.Errorf("round trip mismatch:\n%+v\n%+v", c, back)
	}
	if d := Diff(c, back); len(d) != 0 {
		t.Errorf("diff = %v", d)
	}
	if _, err := FromData(map[string]string{"version": "2"}); err == nil {
		t.Error("expected version error")
	}
	if _, err := FromData(map[string]string{"postgres.operator": "cnpg", "postgres.cluster": "bad"}); err == nil {
		t.Error("expected cluster ref error")
	}
}

func TestOperatorFor(t *testing.T) {
	op, err := OperatorFor("mysql", "shared-mysql")
	if err != nil || op.ExecContainer != "mysql" {
		t.Fatalf("%v %v", op, err)
	}
	sel := op.ExecSelectors(&DBTarget{Name: "shared-mysql"})
	if sel[0] != "app=shared-mysql" {
		t.Errorf("selector %v", sel)
	}
	if _, err := OperatorFor("postgres", "nope"); err == nil {
		t.Error("expected error")
	}
}

func TestNodeLocalNeverPickedAutomatically(t *testing.T) {
	r := &Report{StorageClasses: []StorageClass{
		{Name: "microk8s-hostpath", Grade: NotHA, NodeLocal: true, Default: true},
		{Name: "longhorn-db-local", Grade: NotHA, NodeLocal: true, RWX: true},
	}}
	_, err := Choose(r, Options{AllowNonHA: true})
	var m *MissingError
	if !errors.As(err, &m) || len(m.Missing) != 2 {
		t.Fatalf("err = %v", err)
	}
}
