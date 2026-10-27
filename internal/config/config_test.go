package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseEnviron(t *testing.T) {
	got := parseEnviron([]byte("POD_NAMESPACE=qa\x00IDLE_TIMEOUT=60s\x00EMPTY=\x00junk\x00"))
	if got["POD_NAMESPACE"] != "qa" || got["IDLE_TIMEOUT"] != "60s" || len(got) != 3 {
		t.Errorf("%v", got)
	}
}

func TestServiceAccountNamespace(t *testing.T) {
	f := filepath.Join(t.TempDir(), "namespace")
	old := serviceAccountNamespaceFile
	t.Cleanup(func() { serviceAccountNamespaceFile = old })

	serviceAccountNamespaceFile = f
	if ns := serviceAccountNamespace(); ns != "default" {
		t.Errorf("missing file: %s", ns)
	}
	os.WriteFile(f, []byte("qa\n"), 0644)
	if ns := serviceAccountNamespace(); ns != "qa" {
		t.Errorf("got %s", ns)
	}
}
