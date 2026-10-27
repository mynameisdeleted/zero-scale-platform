// Package registry maps request hostnames to zero-scale apps using the
// zero-scale-app-* ConfigMaps, so every hostname of an app resolves to one
// canonical host, one systemd unit and one set of configuration.
package registry

import (
	"strings"
	"sync"
	"time"

	"git.fairfaxmedia.net/open-cream-cheese/zero-scale-platform/internal/appname"
	"git.fairfaxmedia.net/open-cream-cheese/zero-scale-platform/internal/runner"
)

// App is a resolved application.
type App struct {
	Name          string            // app name (zero-scale-app-<Name>)
	CanonicalHost string            // host used for the binary path and unit name
	Hosts         []string          // every host the app serves
	Data          map[string]string // ConfigMap data, nil when no ConfigMap exists
}

// Lister lists the app ConfigMaps in a namespace.
type Lister func(namespace string) ([]runner.ConfigMapItem, error)

// Fetcher fetches one ConfigMap's data by name.
type Fetcher func(name, namespace string) (map[string]string, error)

// Registry caches the host -> app mapping and refreshes it every TTL.
type Registry struct {
	namespace string
	list      Lister
	fetch     Fetcher
	ttl       time.Duration
	now       func() time.Time

	mu        sync.Mutex
	byHost    map[string]*App
	refreshed time.Time
}

// New returns a Registry backed by the in-cluster Kubernetes API.
func New(namespace string, ttl time.Duration) *Registry {
	return NewWith(namespace, ttl,
		func(ns string) ([]runner.ConfigMapItem, error) {
			return runner.ListK8sConfigMaps(ns, "zero-scale-app=true")
		},
		func(name, ns string) (map[string]string, error) {
			return runner.FetchK8sResource("configmaps", name, ns)
		})
}

// NewWith returns a Registry with injectable list and fetch functions (used by tests).
func NewWith(namespace string, ttl time.Duration, list Lister, fetch Fetcher) *Registry {
	return &Registry{
		namespace: namespace,
		list:      list,
		fetch:     fetch,
		ttl:       ttl,
		now:       time.Now,
		byHost:    map[string]*App{},
	}
}

// Lookup resolves a request host (port allowed) to its app. It never returns nil:
// a host with no ConfigMap resolves to an app of its own with nil Data.
func (r *Registry) Lookup(host string) *App {
	host = strings.ToLower(appname.StripPort(host))

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.now().Sub(r.refreshed) > r.ttl {
		r.refreshLocked()
	}
	if app, ok := r.byHost[host]; ok {
		return app
	}

	// Not labelled or created since the last refresh: try the ConfigMap names directly.
	app := &App{Name: appname.Name(host), CanonicalHost: host, Hosts: []string{host}}
	for _, name := range []string{appname.ConfigMapName(host), appname.LegacyConfigMapName(host)} {
		if data, err := r.fetch(name, r.namespace); err == nil && data != nil {
			app = fromData(strings.TrimPrefix(name, "zero-scale-app-"), host, data)
			break
		}
	}
	// Cache hits and misses alike until the next refresh, so unknown hosts cost one lookup per TTL.
	for _, h := range app.Hosts {
		r.byHost[h] = app
	}
	r.byHost[host] = app
	return app
}

// AppConfig returns the ConfigMap data for a host, or nil.
func (r *Registry) AppConfig(host string) map[string]string {
	return r.Lookup(host).Data
}

func (r *Registry) refreshLocked() {
	r.refreshed = r.now()
	items, err := r.list(r.namespace)
	if err != nil {
		// Keep the previous mapping; lookups fall back to direct fetches for new hosts.
		return
	}
	byHost := map[string]*App{}
	for _, it := range items {
		app := fromData(strings.TrimPrefix(it.Name, "zero-scale-app-"), "", it.Data)
		for _, h := range app.Hosts {
			byHost[h] = app
		}
	}
	r.byHost = byHost
}

func fromData(name, requestHost string, data map[string]string) *App {
	hosts := appname.Hosts(data)
	if len(hosts) == 0 && requestHost != "" {
		hosts = []string{requestHost}
	}
	canonical := requestHost
	if len(hosts) > 0 {
		canonical = hosts[0]
	}
	return &App{Name: name, CanonicalHost: canonical, Hosts: hosts, Data: data}
}
