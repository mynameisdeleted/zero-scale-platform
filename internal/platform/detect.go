package platform

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Kube runs a read-only kubectl-style query and returns its JSON output
// (the caller adds "-o json").
type Kube interface {
	GetJSON(args ...string) ([]byte, error)
}

// Grade says whether a finding survives the loss of a node.
type Grade string

const (
	HA      Grade = "HA"
	NotHA   Grade = "not HA"
	Unknown Grade = "unknown"
)

// StorageClass is one detected StorageClass.
type StorageClass struct {
	Name        string
	Provisioner string
	Default     bool
	Grade       Grade
	RWX         bool // provider can serve ReadWriteMany volumes
	Reason      string
}

// DBCandidate is one detected database cluster.
type DBCandidate struct {
	DBTarget
	Instances int
	Ready     string // "yes", "no" or "unknown"
	Grade     Grade
	Reason    string
}

// IngressClass is one detected IngressClass.
type IngressClass struct {
	Name       string
	Controller string
	Default    bool
}

// Gateway is a detected zero-scale gateway Deployment.
type Gateway struct {
	Namespace string
	Name      string
	Replicas  int
	Ready     int
	ManagedBy string
	Grade     Grade
}

// Report is everything Detect found.
type Report struct {
	StorageClasses []StorageClass
	Postgres       []DBCandidate
	MySQL          []DBCandidate
	IngressClasses []IngressClass
	Gateways       []Gateway
	Redis          []string // host:port of shared-redis Services
	Warnings       []string
}

// object is a loosely typed Kubernetes object.
type object map[string]any

func (o object) str(path ...string) string {
	v := o.get(path...)
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(t)
	}
	return ""
}

func (o object) num(path ...string) (int, bool) {
	switch t := o.get(path...).(type) {
	case float64:
		return int(t), true
	case string:
		n, err := strconv.Atoi(t)
		return n, err == nil
	}
	return 0, false
}

func (o object) get(path ...string) any {
	var cur any = map[string]any(o)
	for _, p := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = m[p]
	}
	return cur
}

func (o object) list(path ...string) []object {
	raw, _ := o.get(path...).([]any)
	out := make([]object, 0, len(raw))
	for _, r := range raw {
		if m, ok := r.(map[string]any); ok {
			out = append(out, object(m))
		}
	}
	return out
}

func getItems(k Kube, args ...string) ([]object, error) {
	out, err := k.GetJSON(args...)
	if err != nil {
		return nil, err
	}
	var root object
	if err := json.Unmarshal(out, &root); err != nil {
		return nil, fmt.Errorf("parsing kubectl %s: %w", strings.Join(args, " "), err)
	}
	return root.list("items"), nil
}

// Detect inspects the cluster. Errors from optional queries become warnings.
func Detect(k Kube) (*Report, error) {
	r := &Report{}

	scs, err := getItems(k, "get", "storageclass")
	if err != nil {
		return nil, fmt.Errorf("listing storage classes: %w", err)
	}
	for _, sc := range scs {
		r.StorageClasses = append(r.StorageClasses, gradeStorageClass(sc))
	}
	sort.Slice(r.StorageClasses, func(i, j int) bool { return r.StorageClasses[i].Name < r.StorageClasses[j].Name })

	crds := map[string]bool{}
	if items, err := getItems(k, "get", "crd"); err == nil {
		for _, c := range items {
			crds[c.str("metadata", "name")] = true
		}
	} else {
		r.Warnings = append(r.Warnings, "cannot list CRDs (needs cluster-scope read): database operators not detected")
	}

	for _, op := range PostgresOperators {
		if !crds[op.CRD] {
			continue
		}
		items, err := getItems(k, "get", op.CRD, "-A")
		if err != nil {
			r.Warnings = append(r.Warnings, fmt.Sprintf("listing %s: %v", op.CRD, err))
			continue
		}
		for _, it := range items {
			r.Postgres = append(r.Postgres, op.candidate(it))
		}
	}
	for _, op := range MySQLOperators {
		if op.CRD == "" || !crds[op.CRD] {
			continue
		}
		items, err := getItems(k, "get", op.CRD, "-A")
		if err != nil {
			r.Warnings = append(r.Warnings, fmt.Sprintf("listing %s: %v", op.CRD, err))
			continue
		}
		for _, it := range items {
			r.MySQL = append(r.MySQL, op.candidate(it))
		}
	}
	// The single shared-mysql StatefulSet from deploy/shared-data-services.yaml.
	if items, err := getItems(k, "get", "statefulset", "-A", "-l", "app=shared-mysql"); err == nil {
		for _, it := range items {
			r.MySQL = append(r.MySQL, legacyMySQL.candidate(it))
		}
	}

	if items, err := getItems(k, "get", "ingressclass"); err == nil {
		for _, ic := range items {
			r.IngressClasses = append(r.IngressClasses, IngressClass{
				Name:       ic.str("metadata", "name"),
				Controller: ic.str("spec", "controller"),
				Default:    ic.str("metadata", "annotations", "ingressclass.kubernetes.io/is-default-class") == "true",
			})
		}
	}

	seen := map[string]bool{}
	for _, sel := range []string{"app=zero-scale-gateway", "app.kubernetes.io/name=zero-scale-gateway"} {
		items, err := getItems(k, "get", "deployment", "-A", "-l", sel)
		if err != nil {
			continue
		}
		for _, d := range items {
			key := d.str("metadata", "namespace") + "/" + d.str("metadata", "name")
			if seen[key] {
				continue
			}
			seen[key] = true
			r.Gateways = append(r.Gateways, gateway(d))
		}
	}

	if items, err := getItems(k, "get", "service", "-A", "-l", "app=shared-redis"); err == nil {
		for _, s := range items {
			r.Redis = append(r.Redis, fmt.Sprintf("%s.%s.svc.cluster.local:6379", s.str("metadata", "name"), s.str("metadata", "namespace")))
		}
	}
	return r, nil
}

func gateway(d object) Gateway {
	replicas, ok := d.num("spec", "replicas")
	if !ok {
		replicas = 1
	}
	ready, _ := d.num("status", "readyReplicas")
	g := Gateway{
		Namespace: d.str("metadata", "namespace"),
		Name:      d.str("metadata", "name"),
		Replicas:  replicas,
		Ready:     ready,
		ManagedBy: d.str("metadata", "labels", "app.kubernetes.io/managed-by"),
		Grade:     NotHA,
	}
	if replicas >= 2 {
		g.Grade = HA
	}
	return g
}

func atLeast(params object, key string, def, min int) (bool, int) {
	n, ok := params.num(key)
	if !ok {
		n = def
	}
	return n >= min, n
}

func gradeStorageClass(sc object) StorageClass {
	p := sc.str("provisioner")
	params, _ := sc.get("parameters").(map[string]any)
	po := object(params)
	s := StorageClass{
		Name:        sc.str("metadata", "name"),
		Provisioner: p,
		Default:     sc.str("metadata", "annotations", "storageclass.kubernetes.io/is-default-class") == "true",
		Grade:       Unknown,
		Reason:      "provisioner not recognised",
	}
	grade := func(ok bool, why string) {
		s.Reason = why
		if ok {
			s.Grade = HA
		} else {
			s.Grade = NotHA
		}
	}
	switch {
	case p == "driver.longhorn.io":
		ok, n := atLeast(po, "numberOfReplicas", 3, 2)
		grade(ok, fmt.Sprintf("Longhorn, %d replicas", n))
		s.RWX = true
	case strings.HasSuffix(p, ".rbd.csi.ceph.com"):
		grade(true, "Ceph RBD (pool replication)")
	case strings.HasSuffix(p, ".cephfs.csi.ceph.com"):
		grade(true, "CephFS (pool replication)")
		s.RWX = true
	case p == "io.openebs.csi-mayastor":
		ok, n := atLeast(po, "repl", 1, 2)
		grade(ok, fmt.Sprintf("OpenEBS Mayastor, repl %d", n))
	case strings.Contains(p, "openebs.io/local") || p == "local.csi.openebs.io":
		grade(false, "OpenEBS local PV (node-local)")
	case p == "linstor.csi.linbit.com":
		n, ok := po.num("placementCount")
		if !ok {
			n, ok = po.num("linstor.csi.linbit.com/placementCount")
		}
		if !ok {
			n = 1
		}
		grade(n >= 2, fmt.Sprintf("LINSTOR, placementCount %d", n))
	case p == "pxd.portworx.com" || p == "kubernetes.io/portworx-volume":
		ok, n := atLeast(po, "repl", 1, 2)
		grade(ok, fmt.Sprintf("Portworx, repl %d", n))
		s.RWX = po.str("sharedv4") == "true" || po.str("shared") == "true"
	case p == "pd.csi.storage.gke.io" || p == "kubernetes.io/gce-pd":
		regional := po.str("replication-type") == "regional-pd"
		grade(regional, map[bool]string{true: "GCE regional PD", false: "GCE zonal PD"}[regional])
	case p == "disk.csi.azure.com" || p == "kubernetes.io/azure-disk":
		zrs := strings.HasSuffix(strings.ToUpper(po.str("skuName")+po.str("skuname")), "_ZRS")
		grade(zrs, map[bool]string{true: "Azure zone-redundant disk", false: "Azure LRS disk"}[zrs])
	case p == "file.csi.azure.com" || p == "kubernetes.io/azure-file":
		zrs := strings.Contains(strings.ToUpper(po.str("skuName")+po.str("skuname")), "ZRS")
		grade(zrs, map[bool]string{true: "Azure Files ZRS", false: "Azure Files LRS"}[zrs])
		s.RWX = true
	case p == "efs.csi.aws.com":
		grade(true, "AWS EFS (multi-AZ)")
		s.RWX = true
	case p == "ebs.csi.aws.com" || p == "kubernetes.io/aws-ebs":
		grade(false, "AWS EBS (single AZ)")
	case strings.Contains(p, "nfs"):
		s.RWX = true
		s.Reason = "NFS: only as HA as its server"
	case p == "rancher.io/local-path" || p == "microk8s.io/hostpath" || p == "kubernetes.io/host-path" ||
		p == "k8s.io/minikube-hostpath" || p == "kubernetes.io/no-provisioner" || p == "docker.io/hostpath":
		grade(false, "node-local storage")
	}
	return s
}

func parseObject(b []byte) (object, error) {
	var o object
	if err := json.Unmarshal(b, &o); err != nil {
		return nil, err
	}
	return o, nil
}
