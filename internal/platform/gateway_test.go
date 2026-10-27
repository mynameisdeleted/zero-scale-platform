package platform

import (
	"os"
	"strings"
	"testing"
)

func TestGatewayManifest(t *testing.T) {
	if _, err := GatewayManifest(GatewayOptions{RWXClass: "x"}); err == nil {
		t.Error("image must be required")
	}
	if _, err := GatewayManifest(GatewayOptions{Image: "img"}); err == nil {
		t.Error("RWX class must be required")
	}
	m, err := GatewayManifest(GatewayOptions{Namespace: "qa", Image: "registry/zs:1", RWXClass: "longhorn-ha-rwx"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"namespace: qa", "replicas: 2", "storageClassName: longhorn-ha-rwx", "image: registry/zs:1",
		"kind: PodDisruptionBudget", "topologyKey: kubernetes.io/hostname", "fieldPath: metadata.namespace",
		"app.kubernetes.io/managed-by: zero-scale-ctl",
	} {
		if !strings.Contains(m, want) {
			t.Errorf("manifest missing %q", want)
		}
	}
	if strings.Contains(m, "*labels") || strings.Contains(m, "&labels") {
		t.Error("YAML anchors do not work across documents")
	}
	if os.Getenv("DUMP_MANIFESTS") != "" {
		os.WriteFile(os.Getenv("DUMP_MANIFESTS")+"/gateway.yaml", []byte(m), 0644)
		r, _ := RouteManifest("*.apps.example.com", "qa", "zero-scale-gateway", "nginx", "issuer:le-dns")
		os.WriteFile(os.Getenv("DUMP_MANIFESTS")+"/route.yaml", []byte(r), 0644)
	}
}

func TestHostCovered(t *testing.T) {
	routed := []string{"*.apps.example.com", "status.example.com"}
	cases := map[string]bool{
		"fdm.apps.example.com":    true,
		"FDM.apps.example.com":    true,
		"a.b.apps.example.com":    false, // wildcards cover one label only
		"apps.example.com":        false,
		"status.example.com":      true,
		"other.example.com":       false,
		"fdm-qa.apps.example.com": true,
	}
	for h, want := range cases {
		if got := HostCovered(h, routed); got != want {
			t.Errorf("HostCovered(%q) = %v, want %v", h, got, want)
		}
	}
}

func TestRouteManifest(t *testing.T) {
	m, err := RouteManifest("*.apps.example.com", "zero-scale-system", "zero-scale-gateway", "nginx", "issuer:letsencrypt-dns")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"name: zero-scale-wildcard-apps-example-com",
		`cert-manager.io/cluster-issuer: "letsencrypt-dns"`,
		`nginx.ingress.kubernetes.io/upstream-hash-by: "$host"`,
		`- hosts: ["*.apps.example.com"]`,
		"secretName: zero-scale-wildcard-apps-example-com-tls",
		"ingressClassName: nginx",
	} {
		if !strings.Contains(m, want) {
			t.Errorf("route missing %q:\n%s", want, m)
		}
	}
	m, _ = RouteManifest("status.example.com", "ns", "svc", "", "")
	if strings.Contains(m, "tls:") || strings.Contains(m, "ingressClassName") || !strings.Contains(m, "name: zero-scale-host-status-example-com") {
		t.Errorf("plain route wrong:\n%s", m)
	}
	for _, bad := range []string{"*.com", "a..b.com", "*.*.x.com", "UPPER.example.com", "x.example.com/path"} {
		if _, err := RouteManifest(bad, "ns", "svc", "", ""); err == nil {
			t.Errorf("accepted bad host %q", bad)
		}
	}
	if _, err := RouteManifest("a.example.com", "ns", "svc", "", "bogus"); err == nil {
		t.Error("accepted bad tls value")
	}
	if n := RouteName("*." + strings.Repeat("abcdefghij.", 8) + "com"); len(n) > 63 {
		t.Errorf("name too long: %s", n)
	}
}

func TestIssuerSupportsWildcard(t *testing.T) {
	cases := map[string]bool{
		`{"spec":{"ca":{"secretName":"root"}}}`:                                     true,
		`{"spec":{"acme":{"solvers":[{"http01":{"ingress":{}}}]}}}`:                 false,
		`{"spec":{"acme":{"solvers":[{"http01":{}},{"dns01":{"cloudflare":{}}}]}}}`: true,
	}
	for in, want := range cases {
		got, err := IssuerSupportsWildcard([]byte(in))
		if err != nil || got != want {
			t.Errorf("%s: got %v %v, want %v", in, got, err, want)
		}
	}
}
