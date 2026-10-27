// Package volumes parses an app's persistent volume claims and turns them into
// systemd properties. Each volume is a subdirectory of the gateway's shared RWX
// volume (Longhorn or any HA RWX class), bound into the app's sandbox.
package volumes

import (
	"fmt"
	"path"
	"regexp"
	"strings"
)

// DefaultRoot is where the gateway mounts the shared volumes PVC.
const DefaultRoot = "/srv/zero-scale-volumes"

// Volume is one persistent directory an app sees at Mount.
type Volume struct {
	Name     string
	Mount    string
	ReadOnly bool
}

var nameRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?$`)

// Paths an app may not mount over.
var reserved = []string{"/", "/proc", "/sys", "/dev", "/run", "/etc", "/usr", "/bin", "/sbin", "/lib", "/lib64", "/opt/apps", "/srv/zero-scale-volumes", "/tmp", "/var/tmp"}

// Parse reads "name:/mount[:ro]" entries separated by commas or whitespace. A bare
// "/mount" (the old mounts: form) takes its name from the last path element.
func Parse(spec string) ([]Volume, error) {
	var out []Volume
	seenName, seenMount := map[string]bool{}, map[string]bool{}
	for _, item := range strings.FieldsFunc(spec, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' || r == '\n' }) {
		var v Volume
		parts := strings.Split(item, ":")
		if strings.HasPrefix(item, "/") {
			v.Mount = parts[0]
			v.Name = strings.ToLower(path.Base(parts[0]))
			parts = append([]string{v.Name}, parts...)
		} else {
			if len(parts) < 2 {
				return nil, fmt.Errorf("volume %q: want name:/mount/path[:ro]", item)
			}
			v.Name, v.Mount = parts[0], parts[1]
		}
		if len(parts) == 3 {
			if parts[2] != "ro" && parts[2] != "rw" {
				return nil, fmt.Errorf("volume %q: mode must be ro or rw", item)
			}
			v.ReadOnly = parts[2] == "ro"
		} else if len(parts) > 3 {
			return nil, fmt.Errorf("volume %q: too many ':' separators", item)
		}
		if !nameRe.MatchString(v.Name) {
			return nil, fmt.Errorf("volume %q: name must be 1-40 of [a-z0-9-]", item)
		}
		if !path.IsAbs(v.Mount) || path.Clean(v.Mount) != v.Mount || strings.ContainsAny(v.Mount, " '\"\\$`") {
			return nil, fmt.Errorf("volume %q: mount must be a clean absolute path", item)
		}
		for _, r := range reserved {
			if v.Mount == r || strings.HasPrefix(v.Mount, "/opt/apps/") || strings.HasPrefix(v.Mount, DefaultRoot+"/") || strings.HasPrefix(v.Mount, "/proc/") || strings.HasPrefix(v.Mount, "/sys/") || strings.HasPrefix(v.Mount, "/dev/") {
				return nil, fmt.Errorf("volume %q: %s is reserved", item, v.Mount)
			}
		}
		if seenName[v.Name] || seenMount[v.Mount] {
			return nil, fmt.Errorf("volume %q: duplicate name or mount", item)
		}
		seenName[v.Name], seenMount[v.Mount] = true, true
		out = append(out, v)
	}
	return out, nil
}

// Format is the inverse of Parse, as stored in the app ConfigMap's "volumes" key.
func Format(vols []Volume) string {
	var parts []string
	for _, v := range vols {
		s := v.Name + ":" + v.Mount
		if v.ReadOnly {
			s += ":ro"
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, ",")
}

// Source is the host directory backing a volume.
func Source(root, app string, v Volume) string {
	return path.Join(root, app, v.Name)
}

// AppDir is the directory holding all of an app's volumes.
func AppDir(root, app string) string {
	return path.Join(root, app)
}

// SystemdProperties returns systemd-run -p values that bind the volumes into the
// unit and, when user is set, give that user ownership of them before start
// (a no-op once ownership already matches, so warm restarts stay fast).
func SystemdProperties(root, app, user string, vols []Volume) []string {
	var props, dirs []string
	for _, v := range vols {
		src := Source(root, app, v)
		dirs = append(dirs, src)
		if v.ReadOnly {
			props = append(props, fmt.Sprintf("BindReadOnlyPaths=%s:%s", src, v.Mount))
		} else {
			props = append(props, fmt.Sprintf("BindPaths=%s:%s", src, v.Mount))
		}
	}
	if user != "" && user != "root" && len(dirs) > 0 {
		script := fmt.Sprintf(`for d in %s; do [ "$(stat -c %%U "$d")" = %s ] || chown -R %s: "$d"; done`,
			strings.Join(dirs, " "), user, user)
		// systemd expands % specifiers and $VARIABLES in Exec lines; escape both for the shell.
		script = strings.NewReplacer("%", "%%", "$", "$$").Replace(script)
		props = append([]string{"ExecStartPre=+/bin/sh -c '" + script + "'"}, props...)
	}
	return props
}
