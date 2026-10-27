package platform

import (
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
		"default/oauth2-proxy-mdm fdm.ffxm.cc 2 pods, all on sv2d",
		"default/zero-scale-gateway fdm.ffxm.cc,hs.ffxm.cc 1 pod, on steve-disks",
		"qa/zero-scale-gateway mock-qa.ffxm.cc ",
	}
	if strings.Join(lines, "\n") != strings.Join(want, "\n") {
		t.Errorf("got:\n%s\nwant:\n%s", strings.Join(lines, "\n"), strings.Join(want, "\n"))
	}
	if !got[2].Survives() || got[1].Survives() {
		t.Error("Survives")
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
