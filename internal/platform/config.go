// Package platform detects the HA building blocks a cluster already has
// (storage classes, Postgres and MySQL operators, ingress, the gateway) and
// stores the chosen ones in the zero-scale-platform ConfigMap.
package platform

import (
	"fmt"
	"sort"
	"strings"
)

// ConfigMapName is the cluster-wide platform ConfigMap.
const ConfigMapName = "zero-scale-platform"

// DefaultNamespace holds the platform ConfigMap unless overridden.
const DefaultNamespace = "zero-scale-system"

// Config is the platform choice set, stored as flat ConfigMap keys so it can be
// read and edited with plain kubectl.
type Config struct {
	Storage  StorageConfig
	Postgres *DBTarget // nil when no Postgres is configured
	MySQL    *DBTarget // nil when no MySQL is configured
	Redis    string    // host:port
	Ingress  IngressConfig
	Gateway  GatewayConfig
}

// StorageConfig names the HA storage classes.
type StorageConfig struct {
	BlockClass string
	RWXClass   string
}

// DBTarget is a database cluster managed by an operator.
type DBTarget struct {
	Operator  string // see PostgresOperators / MySQLOperators
	Namespace string
	Name      string
	RWService string // host:port that apps connect to

	// MySQL only: where the admin password lives (a Secret in Namespace).
	AdminSecret string
	AdminKey    string
	AdminUser   string
}

// IngressConfig is filled in by the gateway and ingress commands.
type IngressConfig struct {
	Class string
	Hosts []string          // individual hosts and wildcards (*.apps.example.com) routed to the gateway
	TLS   map[string]string // host -> "secret:<name>" or "issuer:<ClusterIssuer>"
}

// GatewayConfig records where the gateway runs and who manages it.
type GatewayConfig struct {
	Namespace string
	Service   string
	ManagedBy string // "zero-scale-ctl", "Helm", ...
}

// Ref returns namespace/name.
func (t *DBTarget) Ref() string { return t.Namespace + "/" + t.Name }

// Host returns the host part of RWService.
func (t *DBTarget) Host() string {
	h, _ := splitHostPort(t.RWService)
	return h
}

// Port returns the port part of RWService.
func (t *DBTarget) Port() string {
	_, p := splitHostPort(t.RWService)
	return p
}

func splitHostPort(s string) (string, string) {
	if i := strings.LastIndex(s, ":"); i >= 0 {
		return s[:i], s[i+1:]
	}
	return s, ""
}

// ToData renders the config as ConfigMap data.
func (c *Config) ToData() map[string]string {
	d := map[string]string{"version": "1"}
	set := func(k, v string) {
		if v != "" {
			d[k] = v
		}
	}
	set("storage.blockClass", c.Storage.BlockClass)
	set("storage.rwxClass", c.Storage.RWXClass)
	for prefix, t := range map[string]*DBTarget{"postgres": c.Postgres, "mysql": c.MySQL} {
		if t == nil {
			continue
		}
		set(prefix+".operator", t.Operator)
		set(prefix+".cluster", t.Ref())
		set(prefix+".rwService", t.RWService)
		set(prefix+".adminSecret", t.AdminSecret)
		set(prefix+".adminKey", t.AdminKey)
		set(prefix+".adminUser", t.AdminUser)
	}
	set("redis.service", c.Redis)
	set("ingress.class", c.Ingress.Class)
	set("ingress.hosts", strings.Join(c.Ingress.Hosts, ","))
	var tls []string
	for _, h := range c.Ingress.Hosts {
		if v := c.Ingress.TLS[h]; v != "" {
			tls = append(tls, h+"="+v)
		}
	}
	set("ingress.tls", strings.Join(tls, ","))
	set("gateway.namespace", c.Gateway.Namespace)
	set("gateway.service", c.Gateway.Service)
	set("gateway.managedBy", c.Gateway.ManagedBy)
	return d
}

// FromData parses ConfigMap data written by ToData.
func FromData(d map[string]string) (*Config, error) {
	if v := d["version"]; v != "" && v != "1" {
		return nil, fmt.Errorf("unsupported %s version %q (this zero-scale-ctl understands version 1)", ConfigMapName, v)
	}
	c := &Config{
		Storage: StorageConfig{BlockClass: d["storage.blockClass"], RWXClass: d["storage.rwxClass"]},
		Redis:   d["redis.service"],
		Ingress: IngressConfig{Class: d["ingress.class"]},
		Gateway: GatewayConfig{Namespace: d["gateway.namespace"], Service: d["gateway.service"], ManagedBy: d["gateway.managedBy"]},
	}
	for _, h := range strings.Split(d["ingress.hosts"], ",") {
		if h = strings.TrimSpace(h); h != "" {
			c.Ingress.Hosts = append(c.Ingress.Hosts, h)
		}
	}
	for _, pair := range strings.Split(d["ingress.tls"], ",") {
		if h, v, ok := strings.Cut(strings.TrimSpace(pair), "="); ok && h != "" && v != "" {
			if c.Ingress.TLS == nil {
				c.Ingress.TLS = map[string]string{}
			}
			c.Ingress.TLS[h] = v
		}
	}
	for _, prefix := range []string{"postgres", "mysql"} {
		op := d[prefix+".operator"]
		if op == "" {
			continue
		}
		ns, name, ok := strings.Cut(d[prefix+".cluster"], "/")
		if !ok || ns == "" || name == "" {
			return nil, fmt.Errorf("%s.cluster must be <namespace>/<name>, got %q", prefix, d[prefix+".cluster"])
		}
		t := &DBTarget{Operator: op, Namespace: ns, Name: name, RWService: d[prefix+".rwService"],
			AdminSecret: d[prefix+".adminSecret"], AdminKey: d[prefix+".adminKey"], AdminUser: d[prefix+".adminUser"]}
		if prefix == "postgres" {
			c.Postgres = t
		} else {
			c.MySQL = t
		}
	}
	return c, nil
}

// Diff lists the keys that differ between two configs as "key: old -> new" lines.
func Diff(old, new *Config) []string {
	a, b := map[string]string{}, new.ToData()
	if old != nil {
		a = old.ToData()
	}
	keys := map[string]bool{}
	for k := range a {
		keys[k] = true
	}
	for k := range b {
		keys[k] = true
	}
	var sorted []string
	for k := range keys {
		sorted = append(sorted, k)
	}
	sort.Strings(sorted)
	var out []string
	for _, k := range sorted {
		if a[k] != b[k] {
			out = append(out, fmt.Sprintf("%s: %q -> %q", k, a[k], b[k]))
		}
	}
	return out
}
