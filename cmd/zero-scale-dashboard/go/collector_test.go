package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIsValidFQDN(t *testing.T) {
	valid := []string{
		"bdash2.fairfaxmedia.net",
		"bdash2.kube.fairfaxmedia.net",
		"fdm.ffxm.cc",
		"hopscotch.kube.fairfaxmedia.net",
		"hs.ffxm.cc",
		"kstat.fairfaxmedia.net",
		"kstat.kube.fairfaxmedia.net",
		"mdm.ffxm.cc",
		"mock-qa.ffxm.cc",
		"status.fairfaxmedia.net",
		"status.kube.fairfaxmedia.net",
		"example.com",
		"my-app.sub.example.org",
	}

	invalid := []string{
		"headscale-config.yaml",
		"headscale-config.yml",
		"hs_noise.key",
		"headscale.conf",
		"app.json",
		"config.toml",
		"notes.txt",
		"readme.md",
		"server.log",
		"tls.crt",
		"tls.pem",
		"id_rsa.pub",
		"backup.bak",
		"old.old",
		"temp.tmp",
		"deploy.sh",
		"script.bash",
		"helper.py",
		"main.go",
		"archive.tar",
		"archive.gz",
		"bundle.zip",
		"my-app.type",
		"fleet",
		"mdm-controller",
		"lost+found",
		"notadomain",
		"nodots",
		"bad_underscore.com",
		"-leadingdash.com",
		"trailingdash-.com",
		"",
	}

	for _, v := range valid {
		if !isValidFQDN(v) {
			t.Errorf("isValidFQDN(%q) = false, want true", v)
		}
	}

	for _, inv := range invalid {
		if isValidFQDN(inv) {
			t.Errorf("isValidFQDN(%q) = true, want false", inv)
		}
	}
}

func TestCollectZeroScaleAppsFiltersNonFQDN(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "test-apps-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Create sample entries in /opt/apps
	testFiles := []string{
		"bdash2.fairfaxmedia.net",
		"headscale-config.yaml",
		"hs_noise.key",
		"hs.ffxm.cc",
		"fleet",
		"status.kube.fairfaxmedia.net",
	}
	for _, f := range testFiles {
		if err := os.WriteFile(filepath.Join(tmpDir, f), []byte("dummy"), 0755); err != nil {
			t.Fatal(err)
		}
	}

	c := &Collector{
		appsDir: tmpDir,
	}

	apps := c.collectZeroScaleApps()
	appHosts := make(map[string]bool)
	for _, a := range apps {
		appHosts[a.Host] = true
	}

	// Should include valid FQDNs
	if !appHosts["bdash2.fairfaxmedia.net"] {
		t.Error("expected bdash2.fairfaxmedia.net to be present")
	}
	if !appHosts["hs.ffxm.cc"] {
		t.Error("expected hs.ffxm.cc to be present")
	}
	if !appHosts["status.kube.fairfaxmedia.net"] {
		t.Error("expected status.kube.fairfaxmedia.net to be present")
	}

	// Should filter out non-FQDN files
	if appHosts["headscale-config.yaml"] {
		t.Error("headscale-config.yaml should be filtered out")
	}
	if appHosts["hs_noise.key"] {
		t.Error("hs_noise.key should be filtered out")
	}
	if appHosts["fleet"] {
		t.Error("fleet should be filtered out")
	}
}
