package registry

import (
	"errors"
	"testing"
	"time"

	"git.fairfaxmedia.net/open-cream-cheese/zero-scale-platform/internal/runner"
)

func fixture() ([]runner.ConfigMapItem, map[string]map[string]string) {
	items := []runner.ConfigMapItem{
		{Name: "zero-scale-app-fdm", Data: map[string]string{"host": "fdm.ffxm.cc", "hosts": "fdm.ffxm.cc,fleet.example.com", "PORT_X": "1"}},
		{Name: "zero-scale-app-hoppscotchweb", Data: map[string]string{"host": "hoppscotch-web.example.com"}},
	}
	byName := map[string]map[string]string{
		"zero-scale-app-legacy-svc": {"args": "serve"},
	}
	return items, byName
}

func newTestRegistry(t *testing.T, listCalls *int) *Registry {
	items, byName := fixture()
	return NewWith("ns", time.Minute,
		func(ns string) ([]runner.ConfigMapItem, error) {
			*listCalls++
			return items, nil
		},
		func(name, ns string) (map[string]string, error) {
			if d, ok := byName[name]; ok {
				return d, nil
			}
			return nil, errors.New("not found")
		})
}

func TestSecondaryHostResolvesToCanonical(t *testing.T) {
	calls := 0
	r := newTestRegistry(t, &calls)
	app := r.Lookup("Fleet.example.com:443")
	if app.CanonicalHost != "fdm.ffxm.cc" || app.Name != "fdm" {
		t.Fatalf("got %+v", app)
	}
	if app.Data["PORT_X"] != "1" {
		t.Fatalf("secondary host lost app config: %+v", app.Data)
	}
}

func TestDashedHostFindsCleanedConfigMap(t *testing.T) {
	calls := 0
	r := newTestRegistry(t, &calls)
	app := r.Lookup("hoppscotch-web.example.com")
	if app.Name != "hoppscotchweb" || app.Data == nil {
		t.Fatalf("got %+v", app)
	}
}

func TestLegacyConfigMapName(t *testing.T) {
	calls := 0
	r := newTestRegistry(t, &calls)
	app := r.Lookup("legacy-svc.example.com")
	if app.Data["args"] != "serve" || app.CanonicalHost != "legacy-svc.example.com" {
		t.Fatalf("got %+v", app)
	}
}

func TestUnknownHostIsCachedUntilRefresh(t *testing.T) {
	calls := 0
	r := newTestRegistry(t, &calls)
	fetches := 0
	r.fetch = func(name, ns string) (map[string]string, error) {
		fetches++
		return nil, errors.New("not found")
	}
	now := time.Unix(1000, 0)
	r.now = func() time.Time { return now }
	for i := 0; i < 3; i++ {
		app := r.Lookup("nobody.example.com")
		if app.Data != nil || app.CanonicalHost != "nobody.example.com" {
			t.Fatalf("got %+v", app)
		}
	}
	if calls != 1 || fetches != 2 {
		t.Fatalf("list calls = %d, fetches = %d; want 1 and 2", calls, fetches)
	}
	now = now.Add(2 * time.Minute)
	r.Lookup("nobody.example.com")
	if calls != 2 {
		t.Fatalf("expected a refresh after TTL, list calls = %d", calls)
	}
}
