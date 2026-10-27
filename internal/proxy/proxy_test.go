package proxy

import (
	"os"
	"path/filepath"
	"testing"

	"git.fairfaxmedia.net/open-cream-cheese/zero-scale-platform/internal/config"
)

func TestResolveBinary(t *testing.T) {
	dir := t.TempDir()
	touch := func(name string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0755); err != nil {
			t.Fatal(err)
		}
	}
	touch("fdm.ffxm.cc")
	touch("bdash2.kube.example.net")
	g := New(&config.Config{AppsDir: dir}, nil)

	cases := []struct{ host, request, want string }{
		{"fdm.ffxm.cc", "fleet.example.com", "fdm.ffxm.cc"},                     // secondary host uses canonical binary
		{"other.example.com", "fdm.ffxm.cc", "fdm.ffxm.cc"},                     // falls back to the request host
		{"bdash2.example.com", "bdash2.example.com", "bdash2.kube.example.net"}, // legacy short-name match
		{"missing.example.com", "missing.example.com", ""},
	}
	for _, c := range cases {
		got := g.resolveBinary(c.host, c.request)
		if c.want == "" {
			if got != "" {
				t.Errorf("resolveBinary(%q, %q) = %q, want none", c.host, c.request, got)
			}
			continue
		}
		if got != filepath.Join(dir, c.want) {
			t.Errorf("resolveBinary(%q, %q) = %q, want %q", c.host, c.request, got, c.want)
		}
	}
}
