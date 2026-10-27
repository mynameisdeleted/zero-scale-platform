package platform

import (
	"sort"
	"strings"
)

// TemporaryDataAnnotation marks a PVC whose contents may be lost with its node
// (caches, scratch space). doctor then reports it as a note, not a failure.
const TemporaryDataAnnotation = "zero-scale.io/data"

// LocalVolume is a PVC on a node-local StorageClass that running pods mount.
type LocalVolume struct {
	Namespace  string
	PVC        string
	Class      string
	Pods       []string
	Nodes      []string // nodes of the pods mounting it
	Temporary  bool     // annotated zero-scale.io/data=temporary
	Owner      string   // Kind/name of the mounting pods' first owner, e.g. StatefulSet/qa-pxc-pxc
	OwnerNodes int      // distinct nodes running pods of that owner
	ZeroScale  bool     // volume belongs to zero-scale platform or managed app
}

func isZeroScalePVC(c object) bool {
	name := c.str("metadata", "name")
	if strings.HasPrefix(name, "zero-scale-") || strings.HasPrefix(name, "shared-") {
		return true
	}
	if labels, ok := c.get("metadata", "labels").(map[string]any); ok {
		if labels["zero-scale-app"] == "true" || labels["app.kubernetes.io/part-of"] == "zero-scale-platform" {
			return true
		}
	}
	return false
}

func isZeroScalePod(p object) bool {
	name := p.str("metadata", "name")
	if strings.HasPrefix(name, "zero-scale-") || strings.HasPrefix(name, "shared-") {
		return true
	}
	if labels, ok := p.get("metadata", "labels").(map[string]any); ok {
		if labels["app"] == "zero-scale-gateway" || labels["zero-scale-app"] == "true" || labels["app.kubernetes.io/part-of"] == "zero-scale-platform" {
			return true
		}
	}
	return false
}

// Replicated reports whether the workload owning the pods runs on two or more
// nodes, as a database cluster with one node-local volume per member does. It
// cannot tell replication from sharding.
func (v LocalVolume) Replicated() bool { return v.OwnerNodes >= 2 }

// AtRisk reports whether persistent data is lost or stuck when the node goes down.
func (v LocalVolume) AtRisk() bool { return !v.Temporary && !v.Replicated() }

// NodeLocalVolumes lists PVCs on node-local StorageClasses that running pods
// mount. PVCs no running pod uses are skipped.
func NodeLocalVolumes(k Kube) ([]LocalVolume, error) {
	classes, err := getItems(k, "get", "storageclass")
	if err != nil {
		return nil, err
	}
	pvcs, err := getItems(k, "get", "pvc", "-A")
	if err != nil {
		return nil, err
	}
	pods, err := getItems(k, "get", "pods", "-A")
	if err != nil {
		return nil, err
	}
	local := map[string]bool{}
	for _, sc := range classes {
		if g := gradeStorageClass(sc); g.NodeLocal {
			local[g.Name] = true
		}
	}

	byKey := map[string]*LocalVolume{}
	for _, c := range pvcs {
		class := c.str("spec", "storageClassName")
		if !local[class] {
			continue
		}
		ns, name := c.str("metadata", "namespace"), c.str("metadata", "name")
		byKey[ns+"/"+name] = &LocalVolume{
			Namespace: ns, PVC: name, Class: class,
			Temporary: c.str("metadata", "annotations", TemporaryDataAnnotation) == "temporary",
			ZeroScale: isZeroScalePVC(c),
		}
	}

	ownerNodes := map[string]map[string]bool{} // ns/Kind/name -> nodes
	running := func(p object) bool {
		return p.str("status", "phase") == "Running" && p.get("metadata", "deletionTimestamp") == nil
	}
	for _, p := range pods {
		if !running(p) {
			continue
		}
		if o := podOwner(p); o != "" {
			key := p.str("metadata", "namespace") + "/" + o
			if ownerNodes[key] == nil {
				ownerNodes[key] = map[string]bool{}
			}
			ownerNodes[key][p.str("spec", "nodeName")] = true
		}
	}
	for _, p := range pods {
		if !running(p) {
			continue
		}
		ns := p.str("metadata", "namespace")
		for _, vol := range p.list("spec", "volumes") {
			v := byKey[ns+"/"+vol.str("persistentVolumeClaim", "claimName")]
			if v == nil {
				continue
			}
			if isZeroScalePod(p) {
				v.ZeroScale = true
			}
			v.Pods = append(v.Pods, p.str("metadata", "name"))
			if n := p.str("spec", "nodeName"); !contains(v.Nodes, n) {
				v.Nodes = append(v.Nodes, n)
			}
			if o := podOwner(p); o != "" && v.Owner == "" {
				v.Owner = o
				v.OwnerNodes = len(ownerNodes[ns+"/"+o])
			}
		}
	}

	var out []LocalVolume
	for _, v := range byKey {
		if len(v.Pods) == 0 {
			continue
		}
		sort.Strings(v.Pods)
		sort.Strings(v.Nodes)
		out = append(out, *v)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Namespace+"/"+out[i].PVC < out[j].Namespace+"/"+out[j].PVC
	})
	return out, nil
}

// podOwner is the Kind/name of a pod's first ownerReference, or "".
func podOwner(p object) string {
	refs := p.list("metadata", "ownerReferences")
	if len(refs) == 0 {
		return ""
	}
	return refs[0].str("kind") + "/" + refs[0].str("name")
}
