package platform

import (
	"fmt"
	"strings"
)

// Options override or constrain what Choose picks.
type Options struct {
	BlockClass string // StorageClass name
	RWXClass   string // StorageClass name
	Postgres   string // namespace/name, or "none"
	MySQL      string // namespace/name, or "none"
	AllowNonHA bool
}

// AmbiguousError means several HA candidates fit and none is marked default.
type AmbiguousError struct {
	What    string   // e.g. "block storage class"
	Flag    string   // flag that resolves it
	Choices []string // candidate names
}

func (e *AmbiguousError) Error() string {
	return fmt.Sprintf("several %s candidates fit: %s (choose one with %s)", e.What, strings.Join(e.Choices, ", "), e.Flag)
}

// MissingError means a required piece has no acceptable candidate.
type MissingError struct {
	Missing []string // human descriptions
	Hints   []string // commands that would fix it
}

func (e *MissingError) Error() string {
	return "missing required platform pieces: " + strings.Join(e.Missing, "; ")
}

func acceptable(g Grade, allowNonHA bool) bool {
	return g == HA || allowNonHA
}

// Choose turns a detection report into a platform Config.
func Choose(r *Report, o Options) (*Config, error) {
	c := &Config{}
	missing := &MissingError{}

	// Block storage.
	if o.BlockClass != "" {
		sc := findClass(r, o.BlockClass)
		if sc == nil {
			return nil, fmt.Errorf("storage class %q not found", o.BlockClass)
		}
		if !acceptable(sc.Grade, o.AllowNonHA) {
			return nil, fmt.Errorf("storage class %q is %s (%s); pass --allow-non-ha to use it anyway", sc.Name, sc.Grade, sc.Reason)
		}
		c.Storage.BlockClass = sc.Name
	} else {
		name, err := pickClass(r, "block storage class", "--storage-class", func(sc StorageClass) bool { return acceptable(sc.Grade, o.AllowNonHA) })
		if err != nil {
			return nil, err
		}
		if name == "" {
			missing.Missing = append(missing.Missing, "an HA block StorageClass")
			missing.Hints = append(missing.Hints, "zero-scale-ctl install longhorn")
		}
		c.Storage.BlockClass = name
	}

	// RWX storage: the block class itself when it can serve RWX.
	switch {
	case o.RWXClass != "":
		sc := findClass(r, o.RWXClass)
		if sc == nil {
			return nil, fmt.Errorf("storage class %q not found", o.RWXClass)
		}
		if !sc.RWX {
			return nil, fmt.Errorf("storage class %q (%s) cannot serve ReadWriteMany volumes", sc.Name, sc.Provisioner)
		}
		if !acceptable(sc.Grade, o.AllowNonHA) {
			return nil, fmt.Errorf("storage class %q is %s (%s); pass --allow-non-ha to use it anyway", sc.Name, sc.Grade, sc.Reason)
		}
		c.Storage.RWXClass = sc.Name
	default:
		if sc := findClass(r, c.Storage.BlockClass); sc != nil && sc.RWX {
			c.Storage.RWXClass = sc.Name
			break
		}
		name, err := pickClass(r, "RWX storage class", "--rwx-class", func(sc StorageClass) bool { return sc.RWX && acceptable(sc.Grade, o.AllowNonHA) })
		if err != nil {
			return nil, err
		}
		if name == "" {
			missing.Missing = append(missing.Missing, "an HA ReadWriteMany StorageClass")
			if !contains(missing.Hints, "zero-scale-ctl install longhorn") {
				missing.Hints = append(missing.Hints, "zero-scale-ctl install longhorn")
			}
		}
		c.Storage.RWXClass = name
	}

	var err error
	if c.Postgres, err = pickDB(r.Postgres, "Postgres cluster", "--postgres", o.Postgres, o.AllowNonHA); err != nil {
		return nil, err
	}
	if c.MySQL, err = pickDB(r.MySQL, "MySQL cluster", "--mysql", o.MySQL, o.AllowNonHA); err != nil {
		return nil, err
	}

	if len(r.Gateways) == 1 {
		g := r.Gateways[0]
		c.Gateway = GatewayConfig{Namespace: g.Namespace, Service: g.Name, ManagedBy: g.ManagedBy}
	}
	for _, redis := range r.Redis {
		if c.Gateway.Namespace != "" && strings.Contains(redis, "."+c.Gateway.Namespace+".svc") {
			c.Redis = redis
		}
	}
	if c.Redis == "" && len(r.Redis) > 0 {
		c.Redis = r.Redis[0]
	}
	for _, ic := range r.IngressClasses {
		if ic.Default || (c.Ingress.Class == "" && ic.Controller == "k8s.io/ingress-nginx") {
			c.Ingress.Class = ic.Name
		}
	}

	if len(missing.Missing) > 0 {
		return c, missing
	}
	return c, nil
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func findClass(r *Report, name string) *StorageClass {
	for i := range r.StorageClasses {
		if r.StorageClasses[i].Name == name {
			return &r.StorageClasses[i]
		}
	}
	return nil
}

func pickClass(r *Report, what, flag string, ok func(StorageClass) bool) (string, error) {
	var fits []StorageClass
	for _, sc := range r.StorageClasses {
		if ok(sc) {
			fits = append(fits, sc)
		}
	}
	switch len(fits) {
	case 0:
		return "", nil
	case 1:
		return fits[0].Name, nil
	}
	var names []string
	for _, sc := range fits {
		if sc.Default {
			return sc.Name, nil
		}
		names = append(names, sc.Name)
	}
	return "", &AmbiguousError{What: what, Flag: flag, Choices: names}
}

func pickDB(cands []DBCandidate, what, flag, override string, allowNonHA bool) (*DBTarget, error) {
	if override == "none" {
		return nil, nil
	}
	if override != "" {
		for i := range cands {
			if cands[i].Ref() == override {
				if !acceptable(cands[i].Grade, allowNonHA) {
					return nil, fmt.Errorf("%s %s is %s (%s); pass --allow-non-ha to use it anyway", what, override, cands[i].Grade, cands[i].Reason)
				}
				t := cands[i].DBTarget
				return &t, nil
			}
		}
		return nil, fmt.Errorf("%s %q not found", what, override)
	}
	var fits []DBCandidate
	for _, c := range cands {
		if acceptable(c.Grade, allowNonHA) && c.Ready != "no" {
			fits = append(fits, c)
		}
	}
	switch len(fits) {
	case 0:
		return nil, nil // optional: only needed once an app asks for this engine
	case 1:
		t := fits[0].DBTarget
		return &t, nil
	}
	var names []string
	for _, c := range fits {
		names = append(names, c.Ref())
	}
	return nil, &AmbiguousError{What: what, Flag: flag, Choices: names}
}
