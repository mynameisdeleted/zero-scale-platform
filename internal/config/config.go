package config

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"time"
)

// Config holds all configuration options for the zero-scale gateway
type Config struct {
	ListenAddr    string
	AppsDir       string
	ConfigPath    string
	IdleTimeout   time.Duration
	CheckPeriod   time.Duration
	PodName       string
	KubeNamespace string
}

// Load parses command line flags, environment variables, and defaults.
// Precedence: CLI Flags > Environment Variables > Defaults
func Load() (*Config, error) {
	defaultAddr := getEnv("LISTEN_ADDR", ":80")
	if port := os.Getenv("PORT"); port != "" && defaultAddr == ":80" {
		defaultAddr = ":" + port
	}
	defaultAppsDir := getEnv("APPS_DIR", "/opt/apps")
	defaultConfigPath := getEnv("CONFIG_PATH", "/etc/zero-scale/artifacts.json")
	defaultIdleTimeout := getEnvDuration("IDLE_TIMEOUT", 30*time.Second)
	defaultCheckPeriod := getEnvDuration("CHECK_PERIOD", 2*time.Second)
	defaultNamespace := getEnv("POD_NAMESPACE", "default")

	podName := os.Getenv("POD_NAME")
	if podName == "" {
		if h, err := os.Hostname(); err == nil && h != "" {
			podName = h
		} else {
			podName = "zero-scale-gateway"
		}
	}

	addrFlag := flag.String("addr", defaultAddr, "Address to listen on (e.g. :80)")
	appsDirFlag := flag.String("apps-dir", defaultAppsDir, "Directory containing application binaries")
	configPathFlag := flag.String("config", defaultConfigPath, "Path to artifacts configuration JSON")
	idleTimeoutFlag := flag.Duration("idle-timeout", defaultIdleTimeout, "Duration of inactivity before scaling an app to zero")
	checkPeriodFlag := flag.Duration("check-period", defaultCheckPeriod, "Interval for idle check reaper")
	namespaceFlag := flag.String("namespace", defaultNamespace, "Kubernetes namespace")

	flag.Parse()

	cfg := &Config{
		ListenAddr:    *addrFlag,
		AppsDir:       *appsDirFlag,
		ConfigPath:    *configPathFlag,
		IdleTimeout:   *idleTimeoutFlag,
		CheckPeriod:   *checkPeriodFlag,
		PodName:       podName,
		KubeNamespace: *namespaceFlag,
	}

	return cfg, nil
}

// KubeLogURL returns the direct Kubernetes Dashboard web log URL for this pod, if configured
func (c *Config) KubeLogURL() string {
	base := os.Getenv("KUBE_DASHBOARD_URL")
	if base == "" {
		return ""
	}
	base = strings.TrimRight(base, "/")
	return fmt.Sprintf("%s/#/log/%s/%s/pod?namespace=%s&container=systemd-runner",
		base, c.KubeNamespace, c.PodName, c.KubeNamespace)
}

// KubeShellURL returns the direct Kubernetes Dashboard web shell URL for this pod, if configured
func (c *Config) KubeShellURL() string {
	base := os.Getenv("KUBE_DASHBOARD_URL")
	if base == "" {
		return ""
	}
	base = strings.TrimRight(base, "/")
	return fmt.Sprintf("%s/#/shell/%s/%s/systemd-runner?namespace=%s",
		base, c.KubeNamespace, c.PodName, c.KubeNamespace)
}

func getEnv(key, fallback string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return fallback
}

func getEnvDuration(key string, fallback time.Duration) time.Duration {
	if val := os.Getenv(key); val != "" {
		if d, err := time.ParseDuration(val); err == nil {
			return d
		}
	}
	return fallback
}
