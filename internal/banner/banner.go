package banner

import (
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"git.fairfaxmedia.net/open-cream-cheese/zero-scale-platform/internal/config"
)

// PrintStartup prints the initial gateway startup banner to console/journal
func PrintStartup(cfg *config.Config) {
	log.Println("================================================================================")
	log.Printf("🚀 Zero-Scale Platform Gateway Ready on %s", cfg.ListenAddr)
	log.Printf("📁 Applications directory: %s", cfg.AppsDir)
	log.Printf("☸️  Kubernetes Pod:        %s", cfg.PodName)
	log.Printf("☸️  Pod Log Viewer:        %s", cfg.KubeLogURL())
	log.Printf("☸️  Pod Web Shell:         %s", cfg.KubeShellURL())
	log.Println("--------------------------------------------------------------------------------")
	if entries, err := os.ReadDir(cfg.AppsDir); err == nil {
		for _, e := range entries {
			if !e.IsDir() && !strings.HasSuffix(e.Name(), ".type") && e.Name() != "lost+found" {
				PrintRegistration(e.Name())
			}
		}
	}
	log.Println("================================================================================")
}

// PrintRegistration prints service registration details with Web Logs link and CLI command
func PrintRegistration(host string) {
	unitName := fmt.Sprintf("app-%s", host)
	log.Printf("   ⭐ Service Registered: https://%s/", host)
	log.Printf("      🖥️  Web Logs: https://%s/_logs", host)
	log.Printf("      📋 CLI Logs: kubectl exec -n default deployment/zero-scale-gateway -- journalctl -u %s -f", unitName)
}

// PrintScaleUp prints the scale-up lifecycle banner
func PrintScaleUp(host string, port int) {
	unitName := fmt.Sprintf("app-%s", host)
	log.Println("--------------------------------------------------------------------------------")
	log.Printf("⚡ [SCALE-UP] %s (port %d)", host, port)
	log.Printf("   🌐 App URL:    https://%s/", host)
	log.Printf("   🖥️  Web Logs:   https://%s/_logs", host)
	log.Printf("   📋 CLI Logs:   kubectl exec -n default deployment/zero-scale-gateway -- journalctl -u %s -f", unitName)
	log.Println("--------------------------------------------------------------------------------")
}

// PrintScaleToZero prints the scale-to-zero lifecycle banner
func PrintScaleToZero(host string, idleTimeout time.Duration) {
	unitName := fmt.Sprintf("app-%s", host)
	log.Printf("💤 [SCALE-TO-ZERO] %s idle for %v (deactivated unit %s)", host, idleTimeout, unitName)
	log.Printf("   🖥️  Web Logs:   https://%s/_logs", host)
	log.Printf("   📋 CLI Logs:   kubectl exec -n default deployment/zero-scale-gateway -- journalctl -u %s -n 50", unitName)
}
