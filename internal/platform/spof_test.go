package platform

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type dirKube string

func (d dirKube) GetJSON(args ...string) ([]byte, error) {
	return os.ReadFile(filepath.Join(string(d), args[1]+".json"))
}

func TestIngressExposures(t *testing.T) {
	got, err := IngressExposures(dirKube("testdata/spof"))
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, e := range got {
		lines = append(lines, e.Namespace+"/"+e.Service+" "+strings.Join(e.Hosts, ",")+" "+e.Problem())
	}
	want := []string{
		"default/hoppscotch-web hopscotch.kube.fairfaxmedia.net 1 pod, on didi-macbookpro14-3",
		"default/hopscotch-admin hopscotch-admin.kube.fairfaxmedia.net Service not found",
		"default/oauth2-proxy-mdm fdm.ffxm.cc 2 pods, all on sv2d",
		"default/zero-scale-gateway fdm.ffxm.cc,hopscotch.kube.fairfaxmedia.net,hs.ffxm.cc 1 pod, on steve-disks",
		"nextcloud/nextcloud nextcloud.kube.fairfaxmedia.net 1 pod, on didi-macbookpro14-3; persistent data on node-local nextcloud-pvc (microk8s-hostpath)",
		"qa/zero-scale-gateway mock-qa.ffxm.cc ",
	}
	if strings.Join(lines, "\n") != strings.Join(want, "\n") {
		t.Errorf("got:\n%s\nwant:\n%s", strings.Join(lines, "\n"), strings.Join(want, "\n"))
	}
	for i, e := range got {
		if e.Survives() != (i == 5) {
			t.Errorf("%s/%s: Survives() = %v", e.Namespace, e.Service, e.Survives())
		}
	}
}

func TestNodeIPClaims(t *testing.T) {
	got, err := NodeIPClaims(dirKube("testdata/spof"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Service != "kourier" || got[0].Node != "steve-disks" || got[0].IP != "192.168.1.134" || len(got[0].Ports) != 2 {
		t.Errorf("got %+v", got)
	}
}

func TestNodeLocalVolumes(t *testing.T) {
	got, err := NodeLocalVolumes(dirKube("testdata/spof"))
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, v := range got {
		lines = append(lines, fmt.Sprintf("%s/%s %s %s temporary=%v replicated=%v atRisk=%v",
			v.Namespace, v.PVC, v.Class, strings.Join(v.Nodes, ","), v.Temporary, v.Replicated(), v.AtRisk()))
	}
	// default/unused has no running pod and default/data is on an HA class: both skipped.
	want := []string{
		"default/prometheus-server microk8s-hostpath steve-disks temporary=false replicated=false atRisk=true",
		"default/scratch microk8s-hostpath sv2d temporary=true replicated=false atRisk=false",
		"nextcloud/nextcloud-pvc microk8s-hostpath didi-macbookpro14-3 temporary=false replicated=false atRisk=true",
		"qa/datadir-qa-pxc-pxc-0 longhorn-db-local sv2d temporary=false replicated=true atRisk=false",
		"qa/datadir-qa-pxc-pxc-1 longhorn-db-local steve-disks temporary=false replicated=true atRisk=false",
		"qa/datadir-qa-pxc-pxc-2 longhorn-db-local didi-macbookpro14-3 temporary=false replicated=true atRisk=false",
	}
	if strings.Join(lines, "\n") != strings.Join(want, "\n") {
		t.Errorf("got:\n%s\nwant:\n%s", strings.Join(lines, "\n"), strings.Join(want, "\n"))
	}
	if got[3].Owner != "StatefulSet/qa-pxc-pxc" || got[3].OwnerNodes != 3 {
		t.Errorf("owner: %+v", got[3])
	}
}

func TestRouteConflicts(t *testing.T) {
	got, err := RouteConflicts(dirKube("testdata/spof"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Host != "hopscotch.kube.fairfaxmedia.net" || got[0].Path != "/" ||
		strings.Join(got[0].Routes, "; ") != "default/hoppscotch-web -> default/hoppscotch-web; default/zero-scale-gateway -> default/zero-scale-gateway" {
		t.Errorf("got %+v", got)
	}
}

func TestKnativeOnlyHosts(t *testing.T) {
	got, err := KnativeOnlyHosts(dirKube("testdata/spof"))
	if err != nil || strings.Join(got, ",") != "papi.fairfaxmedia.net" {
		t.Errorf("got %v, %v", got, err)
	}
	// No Knative CRD: nothing to report, no error.
	if got, err := KnativeOnlyHosts(dirKube("testdata/none")); got != nil || err != nil {
		t.Errorf("without Knative: %v, %v", got, err)
	}
}
