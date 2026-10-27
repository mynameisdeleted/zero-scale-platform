// Package appname holds the naming rules shared by the gateway and zero-scale-ctl,
// so both sides agree on which ConfigMap belongs to which hostname.
package appname

import (
	"regexp"
	"strings"
)

const configMapPrefix = "zero-scale-app-"

var nonAlnum = regexp.MustCompile("[^a-z0-9]")

// Name derives the app name from a hostname: the first DNS label, lowercased,
// with everything except [a-z0-9] removed (e.g. Hoppscotch-Web.example.com -> hoppscotchweb).
func Name(host string) string {
	label := strings.Split(StripPort(host), ".")[0]
	clean := nonAlnum.ReplaceAllString(strings.ToLower(label), "")
	if clean == "" {
		return "app"
	}
	return clean
}

// ConfigMapName returns the zero-scale-app-<name> ConfigMap name for a hostname.
func ConfigMapName(host string) string {
	return configMapPrefix + Name(host)
}

// LegacyConfigMapName returns the name older gateways looked up: the raw first label.
func LegacyConfigMapName(host string) string {
	return configMapPrefix + strings.Split(StripPort(host), ".")[0]
}

// StripPort removes a trailing :port from a Host header value.
func StripPort(host string) string {
	if i := strings.LastIndex(host, ":"); i >= 0 && !strings.Contains(host[i:], "]") {
		return host[:i]
	}
	return host
}

// Hosts returns every hostname an app ConfigMap serves: "host" first, then the
// comma-separated "hosts" key written by zero-scale-ctl, without duplicates.
func Hosts(data map[string]string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(h string) {
		h = strings.ToLower(strings.TrimSpace(h))
		if h != "" && !seen[h] {
			seen[h] = true
			out = append(out, h)
		}
	}
	add(data["host"])
	for _, h := range strings.Split(data["hosts"], ",") {
		add(h)
	}
	return out
}

var validName = regexp.MustCompile("^[a-z0-9]+$")

// Valid reports whether s has the shape Name produces, so it is safe in paths and unit names.
func Valid(s string) bool {
	return validName.MatchString(s)
}
