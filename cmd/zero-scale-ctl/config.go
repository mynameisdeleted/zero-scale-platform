package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// DatabaseConfig holds database mapping parameters.
type DatabaseConfig struct {
	Engine      string // "mysql" or "postgres"
	EnvAddress  string // e.g. FLEET_MYSQL_ADDRESS
	EnvHost     string
	EnvPort     string
	EnvDatabase string // e.g. FLEET_MYSQL_DATABASE
	EnvUser     string // e.g. FLEET_MYSQL_USERNAME
	EnvPassword string // e.g. FLEET_MYSQL_PASSWORD
}

// RedisConfig holds Redis mapping parameters.
type RedisConfig struct {
	EnvAddress string // e.g. FLEET_REDIS_ADDRESS
	EnvHost    string
	EnvPort    string
}

// AppConfig represents an application configuration.
type AppConfig struct {
	Hostnames   []string
	BinaryPath  string
	Database    *DatabaseConfig
	Redis       *RedisConfig
	Mounts      string
	ScaleToZero bool
	IdleTimeout string            // e.g. "60s", "5m" (optional custom idle timeout)

	// Process execution & launcher configuration
	Args        string            // CLI arguments or subcommand (e.g. "serve")
	InitCmd     string            // One-time initialization run during apply (e.g. migrations)
	PreStart    string            // Alias for InitCmd
	User        string            // Execution user in container (e.g. "root", "nobody")
	DynamicUser bool              // Whether to use transient DynamicUser=yes (default true)
	Env         map[string]string // Custom environment variables
	ConfigFiles []string          // Auxiliary config files to template and upload to /opt/apps/
	Launcher    string            // Explicit full launcher script override (optional)
}

// RedirectConfig represents an edge NGINX redirect rule.
type RedirectConfig struct {
	SourceHost string
	TargetHost string
}

// CanonicalHost returns the primary hostname.
func (a *AppConfig) CanonicalHost() string {
	if len(a.Hostnames) > 0 {
		return a.Hostnames[0]
	}
	return "app"
}

// SecondaryHosts returns any additional hostnames.
func (a *AppConfig) SecondaryHosts() []string {
	if len(a.Hostnames) > 1 {
		return a.Hostnames[1:]
	}
	return nil
}

// AppName derives a clean short name from the canonical host (e.g. fdm.ffxm.cc -> fdm).
func (a *AppConfig) AppName() string {
	parts := strings.Split(a.CanonicalHost(), ".")
	re := regexp.MustCompile("[^a-z0-9]")
	clean := re.ReplaceAllString(strings.ToLower(parts[0]), "")
	if clean == "" {
		return "app"
	}
	return clean
}

func sha8(s string) string {
	h := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(s))))
	return hex.EncodeToString(h[:])[:8]
}

// DBName returns the deterministic, conflict-free database name.
func (a *AppConfig) DBName() string {
	return fmt.Sprintf("db_%s_%s", a.AppName(), sha8(a.CanonicalHost()))
}

// DBUser returns the unique, unguessable database username.
func (a *AppConfig) DBUser() string {
	return fmt.Sprintf("u_%s_%s", a.AppName(), sha8(a.CanonicalHost()))
}

// SecretName returns the Kubernetes Secret name for this app.
func (a *AppConfig) SecretName() string {
	return fmt.Sprintf("%s-db-secret", a.AppName())
}

// ConfigMapName returns the Kubernetes ConfigMap name for this app.
func (a *AppConfig) ConfigMapName() string {
	return fmt.Sprintf("%s-db-config", a.AppName())
}

// NeedsLauncher returns true if the application requires a custom launcher script
// rather than a direct binary symlink.
func (a *AppConfig) NeedsLauncher() bool {
	return a.Launcher != "" || a.PreStart != "" || a.Args != "" || len(a.Env) > 0
}

// LoadConfig parses a simple 10-line YAML/conf file into AppConfig or RedirectConfig.
func LoadConfig(filePath string) (interface{}, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to open config file: %w", err)
	}
	defer file.Close()

	var (
		rawLines       []string
		hostnames      []string
		binaryPath     string
		targetHost     string
		args           string
		initCmd        string
		user           string
		dynamicUser    = true
		envVars        = make(map[string]string)
		launcherLines  []string
		mounts         string
		scaleToZero    = true
		idleTimeout    string
		configFiles    []string
		currentSection = ""
		currentList    = ""

		dbCfg    *DatabaseConfig
		redisCfg *RedisConfig
	)

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		rawLines = append(rawLines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	for _, line := range rawLines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}

		isIndented := strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t")

		// If in launcher multiline mode, collect indented lines
		if currentSection == "launcher" {
			if isIndented {
				launcherLines = append(launcherLines, strings.TrimRight(line, "\r\n"))
				continue
			}
			// Unindented line breaks out of launcher section
			currentSection = ""
		}

		// If not indented, reset nested section unless it's a section header
		if !isIndented && !strings.HasSuffix(trimmed, ":") {
			currentSection = ""
			currentList = ""
		}

		// List items (e.g. "- fdm.kube.fairfaxmedia.net" or "- ./headscale-config.yaml")
		if strings.HasPrefix(trimmed, "- ") {
			val := strings.Trim(trimmed[2:], " \t\"'")
			if currentList == "hostname" || currentList == "hostnames" || currentList == "host" {
				hostnames = append(hostnames, val)
			} else if currentList == "config_file" || currentList == "config_files" || currentList == "configs" || currentList == "config" {
				configFiles = append(configFiles, val)
			}
			continue
		}

		// Section headers (e.g. "database:", "redis:", "env:", "launcher:")
		if strings.HasSuffix(trimmed, ":") && !strings.Contains(trimmed[:len(trimmed)-1], ":") {
			key := strings.TrimSuffix(trimmed, ":")
			currentSection = key
			currentList = ""
			if key == "database" && dbCfg == nil {
				dbCfg = &DatabaseConfig{Engine: "mysql"}
			} else if key == "redis" && redisCfg == nil {
				redisCfg = &RedisConfig{}
			}
			continue
		}

		// Key-value pairs
		if strings.Contains(trimmed, ":") {
			parts := strings.SplitN(trimmed, ":", 2)
			k := strings.TrimSpace(parts[0])
			v := strings.Trim(strings.TrimSpace(parts[1]), "\"'\t ")

			if v == "" {
				currentList = k
				continue
			}

			if currentSection == "database" && dbCfg != nil {
				switch k {
				case "engine":
					dbCfg.Engine = strings.ToLower(v)
				case "env_address":
					dbCfg.EnvAddress = v
				case "env_host", "env_hostname":
					dbCfg.EnvHost = v
				case "env_port":
					dbCfg.EnvPort = v
				case "env_database":
					dbCfg.EnvDatabase = v
				case "env_user":
					dbCfg.EnvUser = v
				case "env_password":
					dbCfg.EnvPassword = v
				}
			} else if currentSection == "redis" && redisCfg != nil {
				switch k {
				case "env_address":
					redisCfg.EnvAddress = v
				case "env_host", "env_hostname":
					redisCfg.EnvHost = v
				case "env_port":
					redisCfg.EnvPort = v
				}
			} else if currentSection == "env" {
				envVars[k] = v
			} else {
				currentSection = ""
				currentList = ""
				switch k {
				case "hostname", "host":
					hostnames = append(hostnames, v)
				case "target_host", "target":
					targetHost = v
				case "binary_path", "binary":
					binaryPath = v
				case "args", "subcommand", "cmd":
					args = v
				case "init_cmd", "init", "initialize", "pre_start", "prestart":
					initCmd = v
				case "user":
					user = v
				case "dynamic_user", "dynamic-user":
					dynamicUser = strings.ToLower(v) != "false"
				case "launcher":
					if v == "|" || v == ">" {
						currentSection = "launcher"
					} else {
						launcherLines = append(launcherLines, v)
					}
				case "mounts":
					mounts = v
				case "scale_to_zero":
					scaleToZero = strings.ToLower(v) != "false"
				case "idle_timeout", "idle-timeout", "timeout":
					idleTimeout = v
				case "config_file", "config":
					configFiles = append(configFiles, v)
				case "config_files", "configs":
					// Could be comma-separated or space-separated
					for _, item := range strings.Fields(strings.ReplaceAll(v, ",", " ")) {
						if item != "" {
							configFiles = append(configFiles, item)
						}
					}
				}
			}
		}
	}

	// 1. Redirect Mode Check
	if targetHost != "" {
		if len(hostnames) == 0 {
			return nil, fmt.Errorf("redirect config must specify 'hostname'")
		}
		return &RedirectConfig{
			SourceHost: hostnames[0],
			TargetHost: targetHost,
		}, nil
	}

	// 2. Application Mode Check
	if len(hostnames) == 0 {
		return nil, fmt.Errorf("config must specify at least one 'hostname'")
	}
	if binaryPath == "" {
		return nil, fmt.Errorf("config must specify 'binary_path'")
	}

	// Resolve binaryPath relative to config file directory if not absolute
	if !filepath.IsAbs(binaryPath) {
		cfgDir := filepath.Dir(filePath)
		absCfgDir, err := filepath.Abs(cfgDir)
		if err == nil {
			binaryPath = filepath.Clean(filepath.Join(absCfgDir, binaryPath))
		}
	}

	var launcherScript string
	if len(launcherLines) > 0 {
		launcherScript = strings.Join(launcherLines, "\n")
	}

	// Resolve and validate configFiles relative to config file directory
	cfgDir := filepath.Dir(filePath)
	absCfgDir, _ := filepath.Abs(cfgDir)

	var resolvedConfigFiles []string
	binBase := filepath.Base(binaryPath)
	binBase = strings.TrimSuffix(binBase, filepath.Ext(binBase))
	primaryName := strings.ToLower(binBase)

	// Also extract short name from hostname (e.g. hs from hs.ffxm.cc)
	var hostShort string
	if len(hostnames) > 0 {
		parts := strings.Split(hostnames[0], ".")
		re := regexp.MustCompile("[^a-z0-9]")
		hostShort = re.ReplaceAllString(strings.ToLower(parts[0]), "")
	}

	for _, cf := range configFiles {
		cf = strings.TrimSpace(cf)
		if cf == "" {
			continue
		}
		base := filepath.Base(cf)
		baseLower := strings.ToLower(base)

		// Verification: file must start with binary name prefix or app short name prefix
		// e.g. headscale-*, headscale_*, headscale.*, hs-*, hs_*, hs.*
		validPrefix := false
		if strings.HasPrefix(baseLower, primaryName+"-") ||
			strings.HasPrefix(baseLower, primaryName+"_") ||
			strings.HasPrefix(baseLower, primaryName+".") {
			validPrefix = true
		} else if hostShort != "" && (strings.HasPrefix(baseLower, hostShort+"-") ||
			strings.HasPrefix(baseLower, hostShort+"_") ||
			strings.HasPrefix(baseLower, hostShort+".")) {
			validPrefix = true
		}

		if !validPrefix {
			return nil, fmt.Errorf("config_file '%s' must start with service prefix '%s-' or '%s-' (e.g. '%s-config.yaml') to avoid conflicts on shared volume",
				base, primaryName, hostShort, primaryName)
		}

		resolvedPath := cf
		if !filepath.IsAbs(resolvedPath) {
			resolvedPath = filepath.Clean(filepath.Join(absCfgDir, cf))
		}
		resolvedConfigFiles = append(resolvedConfigFiles, resolvedPath)
	}

	return &AppConfig{
		Hostnames:   hostnames,
		BinaryPath:  binaryPath,
		Database:    dbCfg,
		Redis:       redisCfg,
		Mounts:      mounts,
		ScaleToZero: scaleToZero,
		IdleTimeout: idleTimeout,
		Args:        args,
		InitCmd:     initCmd,
		PreStart:    initCmd,
		User:        user,
		DynamicUser: dynamicUser,
		Env:         envVars,
		ConfigFiles: resolvedConfigFiles,
		Launcher:    launcherScript,
	}, nil
}
