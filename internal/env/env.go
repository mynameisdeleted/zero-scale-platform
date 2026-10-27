// Package env describes a zero-scale environment: a namespace with its own
// gateway (prod, qa, a feature branch) sharing the cluster's HA databases and
// storage. Its settings live in the namespace's zero-scale-env ConfigMap.
package env

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"git.fairfaxmedia.net/open-cream-cheese/zero-scale-platform/internal/platform"
)

// ConfigMapName is the per-namespace environment ConfigMap.
const ConfigMapName = "zero-scale-env"

// Label marks a namespace as a zero-scale environment; its value is the env name.
const Label = "zero-scale.io/env"

// ExpiresAnnotation holds the RFC 3339 time after which 'env gc' deletes the environment.
const ExpiresAnnotation = "zero-scale.io/expires"

// MaxNameLen leaves room for the app name in env-scoped MySQL user names (32 characters max).
const MaxNameLen = 16

var nameRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

// Config is one environment.
type Config struct {
	Name    string
	Primary bool      // the primary env (prod) uses unsuffixed hosts and the original DB names
	Created time.Time // zero when unknown
	Expires time.Time // zero means no TTL
	// Overrides are platform keys (postgres.*, mysql.*, redis.service, ingress.*)
	// that replace the cluster-wide value for this environment.
	Overrides map[string]string
}

// ValidateName checks an environment name.
func ValidateName(name string) error {
	if len(name) == 0 || len(name) > MaxNameLen || !nameRe.MatchString(name) {
		return fmt.Errorf("environment name %q must be 1-%d characters of [a-z0-9-], starting and ending with a letter or digit", name, MaxNameLen)
	}
	return nil
}

// Suffix is what ${ENV_SUFFIX} expands to: "" for the primary env, "-<name>" otherwise.
func (c *Config) Suffix() string {
	if c == nil || c.Primary {
		return ""
	}
	return "-" + c.Name
}

// Scoped reports whether names (hosts, databases) carry this env's name.
func (c *Config) Scoped() bool {
	return c != nil && !c.Primary
}

// Expand replaces ${ENV_SUFFIX} and ${ENV} in s. A nil config is the primary env.
func (c *Config) Expand(s string) string {
	name := ""
	if c != nil {
		name = c.Name
	}
	return strings.NewReplacer("${ENV_SUFFIX}", c.Suffix(), "${ENV}", name).Replace(s)
}

// CheckHost makes sure a scoped env only claims hosts that carry its suffix or fall
// under its own routes, so two environments never fight over one host in the
// ingress controller.
func (c *Config) CheckHost(host string) error {
	if !c.Scoped() || platform.HostCovered(host, c.Routes()) {
		return nil
	}
	label := strings.Split(host, ".")[0]
	if strings.HasPrefix(host, "*.") || !strings.HasSuffix(label, c.Suffix()) {
		return fmt.Errorf("host %q is not specific to environment %q; write it as <name>${ENV_SUFFIX}.<domain> so it expands to <name>%s.<domain>", host, c.Name, c.Suffix())
	}
	return nil
}

// Routes are the env's own routed hosts and wildcards (ingress.hosts override).
func (c *Config) Routes() []string {
	if c == nil {
		return nil
	}
	var out []string
	for _, h := range strings.Split(c.Overrides["ingress.hosts"], ",") {
		if h = strings.TrimSpace(h); h != "" {
			out = append(out, h)
		}
	}
	return out
}

// Expired reports whether the env's TTL has passed.
func (c *Config) Expired(now time.Time) bool {
	return !c.Expires.IsZero() && now.After(c.Expires)
}

// ParseTTL reads durations like 7d, 36h or 90m. "0" or "" means no TTL.
func ParseTTL(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" || s == "0" {
		return 0, nil
	}
	if strings.HasSuffix(s, "d") {
		n, err := strconv.Atoi(strings.TrimSuffix(s, "d"))
		if err != nil || n <= 0 {
			return 0, fmt.Errorf("invalid TTL %q", s)
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("invalid TTL %q (use e.g. 7d, 36h)", s)
	}
	return d, nil
}

// ToData renders the env ConfigMap data.
func (c *Config) ToData() map[string]string {
	d := map[string]string{"env.name": c.Name}
	if c.Primary {
		d["env.primary"] = "true"
	}
	if !c.Created.IsZero() {
		d["env.created"] = c.Created.UTC().Format(time.RFC3339)
	}
	if !c.Expires.IsZero() {
		d["env.expires"] = c.Expires.UTC().Format(time.RFC3339)
	}
	for k, v := range c.Overrides {
		d[k] = v
	}
	return d
}

// overridable are the platform keys an environment may replace.
var overridable = []string{"postgres.", "mysql.", "redis.", "ingress."}

// FromData parses the env ConfigMap data.
func FromData(d map[string]string) (*Config, error) {
	c := &Config{Name: d["env.name"], Primary: d["env.primary"] == "true", Overrides: map[string]string{}}
	if err := ValidateName(c.Name); err != nil {
		return nil, fmt.Errorf("%s: %w", ConfigMapName, err)
	}
	for key, dst := range map[string]*time.Time{"env.created": &c.Created, "env.expires": &c.Expires} {
		if v := d[key]; v != "" {
			t, err := time.Parse(time.RFC3339, v)
			if err != nil {
				return nil, fmt.Errorf("%s: %s: %w", ConfigMapName, key, err)
			}
			*dst = t
		}
	}
	for k, v := range d {
		for _, p := range overridable {
			if strings.HasPrefix(k, p) {
				c.Overrides[k] = v
			}
		}
	}
	return c, nil
}

// Overlay returns the platform ConfigMap data as seen from this environment's
// namespace: the env's overrides win, and routes and the gateway are the env's
// own, so a scoped env never inherits prod's wildcard route.
func (c *Config) Overlay(platformData map[string]string, namespace string) map[string]string {
	out := map[string]string{}
	for k, v := range platformData {
		out[k] = v
	}
	if c == nil {
		return out
	}
	if c.Scoped() {
		for _, k := range []string{"ingress.hosts", "ingress.tls", "gateway.managedBy"} {
			delete(out, k)
		}
		out["gateway.namespace"] = namespace
		out["gateway.service"] = "zero-scale-gateway"
	}
	for _, prefix := range []string{"postgres.", "mysql."} {
		// A DB override replaces the whole target, never a mix of two clusters.
		if hasPrefix(c.Overrides, prefix) {
			for k := range out {
				if strings.HasPrefix(k, prefix) {
					delete(out, k)
				}
			}
		}
	}
	for k, v := range c.Overrides {
		out[k] = v
	}
	return out
}

func hasPrefix(m map[string]string, prefix string) bool {
	for k := range m {
		if strings.HasPrefix(k, prefix) {
			return true
		}
	}
	return false
}

// DBNames returns the database and user names for an app. Unscoped envs keep the
// original db_<app>_<sha8> form so existing databases are found again; scoped envs
// use db_<env>_<app>_<sha8>, with the app part shortened so the user name fits
// MySQL's 32-character limit.
func (c *Config) DBNames(app, hostHash string) (db, user string) {
	if !c.Scoped() {
		return fmt.Sprintf("db_%s_%s", app, hostHash), fmt.Sprintf("u_%s_%s", app, hostHash)
	}
	e := strings.ReplaceAll(c.Name, "-", "_")
	if max := 32 - len("u___") - len(e) - len(hostHash); len(app) > max {
		app = app[:max]
	}
	return fmt.Sprintf("db_%s_%s_%s", e, app, hostHash), fmt.Sprintf("u_%s_%s_%s", e, app, hostHash)
}

// Manifest renders the namespace (labelled and annotated), a ResourceQuota on object
// counts and storage, and the env ConfigMap.
func (c *Config) Manifest(namespace string, quota Quota) string {
	annotations := ""
	if !c.Expires.IsZero() {
		annotations = fmt.Sprintf("\n  annotations:\n    %s: %q", ExpiresAnnotation, c.Expires.UTC().Format(time.RFC3339))
	}
	var data strings.Builder
	d := c.ToData()
	for _, k := range sortedKeys(d) {
		fmt.Fprintf(&data, "  %s: %q\n", k, d[k])
	}
	return fmt.Sprintf(`apiVersion: v1
kind: Namespace
metadata:
  name: %[1]s
  labels:
    %[2]s: %[3]s
    app.kubernetes.io/part-of: zero-scale-platform
    app.kubernetes.io/managed-by: zero-scale-ctl%[4]s
---
apiVersion: v1
kind: ResourceQuota
metadata:
  name: zero-scale-env
  namespace: %[1]s
  labels:
    app.kubernetes.io/part-of: zero-scale-platform
    app.kubernetes.io/managed-by: zero-scale-ctl
spec:
  hard:
    pods: "%[5]d"
    persistentvolumeclaims: "%[6]d"
    requests.storage: %[7]s
    services.loadbalancers: "0"
    services.nodeports: "0"
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: %[8]s
  namespace: %[1]s
  labels:
    app.kubernetes.io/part-of: zero-scale-platform
    app.kubernetes.io/managed-by: zero-scale-ctl
data:
%[9]s`, namespace, Label, c.Name, annotations, quota.Pods, quota.PVCs, quota.Storage, ConfigMapName, data.String())
}

// Quota limits what one environment can claim. It counts objects and storage only,
// so pods without resource requests (the gateway) are still admitted.
type Quota struct {
	Pods    int
	PVCs    int
	Storage string
}

// DefaultQuota fits a gateway, an oauth2-proxy, a small Postgres cluster and some headroom.
var DefaultQuota = Quota{Pods: 12, PVCs: 8, Storage: "100Gi"}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
