package volumes

import (
	"reflect"
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	got, err := Parse("uploads:/app/uploads, cache:/var/cache/app:ro\n/data/legacy")
	if err != nil {
		t.Fatal(err)
	}
	want := []Volume{
		{Name: "uploads", Mount: "/app/uploads"},
		{Name: "cache", Mount: "/var/cache/app", ReadOnly: true},
		{Name: "legacy", Mount: "/data/legacy"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v", got)
	}
	if Format(got) != "uploads:/app/uploads,cache:/var/cache/app:ro,legacy:/data/legacy" {
		t.Errorf("Format = %s", Format(got))
	}
	if v, _ := Parse(""); len(v) != 0 {
		t.Error("empty spec should give no volumes")
	}
}

func TestParseRejects(t *testing.T) {
	for _, bad := range []string{
		"uploads", "Up:/x", "x:relative", "x:/a/../b", "x:/etc", "x:/proc/1", "x:/opt/apps",
		"x:/a:rx", "x:/a:ro:extra", "x:/a,x:/b", "x:/a,y:/a", "x:/a'b", "x:/srv/zero-scale-volumes",
		"x:/var/lib/zero-scale-scratch", "x:/var/lib/zero-scale-scratch/y:tmp",
	} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestSystemdProperties(t *testing.T) {
	vols := []Volume{{Name: "uploads", Mount: "/app/uploads"}, {Name: "seed", Mount: "/app/seed", ReadOnly: true}}
	props := SystemdProperties("/srv/zero-scale-volumes", "fdm", "zs-fdm", vols)
	if len(props) != 3 {
		t.Fatalf("props = %v", props)
	}
	if !strings.HasPrefix(props[0], "ExecStartPre=+/bin/sh -c '") || !strings.Contains(props[0], "chown -R zs-fdm:") {
		t.Errorf("chown step = %s", props[0])
	}
	if !strings.Contains(props[0], `[ "$$(stat -c %%U "$$d")" = zs-fdm ]`) {
		t.Errorf("$ and %% must be escaped for systemd: %s", props[0])
	}
	if props[1] != "BindPaths=/srv/zero-scale-volumes/fdm/uploads:/app/uploads" || props[2] != "BindReadOnlyPaths=/srv/zero-scale-volumes/fdm/seed:/app/seed" {
		t.Errorf("binds = %v", props[1:])
	}
	if p := SystemdProperties("/r", "fdm", "root", vols); strings.Contains(strings.Join(p, " "), "chown") {
		t.Error("root needs no chown")
	}
}

func TestTemporaryVolumes(t *testing.T) {
	got, err := Parse("data:/var/lib/app, cache:/var/cache/app:tmp")
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Temporary || !got[1].Temporary || got[1].ReadOnly {
		t.Fatalf("got %+v", got)
	}
	if Format(got) != "data:/var/lib/app,cache:/var/cache/app:tmp" {
		t.Errorf("Format = %s", Format(got))
	}
	// Persistent data stays on the shared volume; :tmp goes to the pod's own disk.
	if s := Source(DefaultRoot, "myapp", got[0]); s != "/srv/zero-scale-volumes/myapp/data" {
		t.Errorf("persistent source = %s", s)
	}
	if s := Source(DefaultRoot, "myapp", got[1]); s != "/var/lib/zero-scale-scratch/myapp/cache" {
		t.Errorf("temporary source = %s", s)
	}
	props := strings.Join(SystemdProperties(DefaultRoot, "myapp", "zs-myapp", got), "\n")
	if !strings.Contains(props, "BindPaths=/var/lib/zero-scale-scratch/myapp/cache:/var/cache/app") {
		t.Errorf("props:\n%s", props)
	}
}
