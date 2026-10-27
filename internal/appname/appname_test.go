package appname

import (
	"reflect"
	"testing"
)

func TestName(t *testing.T) {
	cases := map[string]string{
		"fdm.ffxm.cc":                "fdm",
		"hoppscotch-web.example.com": "hoppscotchweb",
		"Hoppscotch-Web.example.com": "hoppscotchweb",
		"papi_prod.example.com:8080": "papiprod",
		"---.example.com":            "app",
		"single":                     "single",
	}
	for in, want := range cases {
		if got := Name(in); got != want {
			t.Errorf("Name(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestConfigMapNames(t *testing.T) {
	if got := ConfigMapName("hoppscotch-web.example.com"); got != "zero-scale-app-hoppscotchweb" {
		t.Errorf("ConfigMapName = %q", got)
	}
	if got := LegacyConfigMapName("hoppscotch-web.example.com:80"); got != "zero-scale-app-hoppscotch-web" {
		t.Errorf("LegacyConfigMapName = %q", got)
	}
}

func TestHosts(t *testing.T) {
	got := Hosts(map[string]string{"host": "fdm.ffxm.cc", "hosts": "fdm.ffxm.cc, Fleet.example.com,,"})
	want := []string{"fdm.ffxm.cc", "fleet.example.com"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Hosts = %v, want %v", got, want)
	}
}
