package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func loadApp(t *testing.T, conf string) (*AppConfig, error) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "app.conf")
	if err := os.WriteFile(p, []byte(conf), 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(p)
	if err != nil {
		return nil, err
	}
	return cfg.(*AppConfig), nil
}

func TestConfigVolumes(t *testing.T) {
	app, err := loadApp(t, "host: notes.example.com\nbinary: ./notes\nvolumes:\n  - data:/var/lib/notes\n  - assets:/srv/assets:ro\n")
	if err != nil {
		t.Fatal(err)
	}
	if app.Mounts != "data:/var/lib/notes,assets:/srv/assets:ro" {
		t.Errorf("volumes list: got %q", app.Mounts)
	}

	app, err = loadApp(t, "host: notes.example.com\nbinary: ./notes\nmounts: /app/uploads\n")
	if err != nil {
		t.Fatal(err)
	}
	if app.Mounts != "uploads:/app/uploads" {
		t.Errorf("legacy mounts: got %q", app.Mounts)
	}

	if _, err := loadApp(t, "host: notes.example.com\nbinary: ./notes\nvolumes: data:/etc\n"); err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Errorf("reserved mount must be rejected, got %v", err)
	}
}

func TestDeleteAppVolumes(t *testing.T) {
	appCM := `{"data":{"host":"notes.example.com","volumes":"data:/var/lib/notes"}}`
	gw := `  "get pods -n default -l app=zero-scale-gateway --field-selector=status.phase=Running -o jsonpath={.items[*].metadata.name}") printf 'gw-a' ;;
  "get pods -n default -l app=zero-scale-gateway --field-selector=status.phase=Running -o jsonpath={.items[0].metadata.name}") printf gw-a ;;
  "exec gw-"*|"delete "*) exit 0 ;;`
	log := fakeKubectl(t, `  "get configmap zero-scale-app-notes -n default -o json") echo '`+appCM+`' ;;
`+gw)
	if err := runDeleteApp([]string{"notes", "--delete-volumes"}, "default"); err == nil {
		t.Error("--delete-volumes without --purge must fail")
	}
	if err := runDeleteApp([]string{"notes", "--purge"}, "default"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(readLog(t, log), "rm -rf") {
		t.Error("volumes must be kept without --delete-volumes")
	}
	if err := runDeleteApp([]string{"notes", "--purge", "--delete-volumes"}, "default"); err != nil {
		t.Fatal(err)
	}
	if want := "exec gw-a -n default -c systemd-runner -- rm -rf --one-file-system /srv/zero-scale-volumes/notes"; !strings.Contains(readLog(t, log), want) {
		t.Errorf("missing %q:\n%s", want, readLog(t, log))
	}
}
