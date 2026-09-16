package sync

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"git.fairfaxmedia.net/open-cream-cheese/zero-scale-platform/internal/config"
	"git.fairfaxmedia.net/open-cream-cheese/zero-scale-platform/internal/runner"
)

type ArtifactItem struct {
	URL  string `json:"url"`
	Type string `json:"type"`
}

type ConfigMapData struct {
	Artifacts map[string]ArtifactItem `json:"artifacts"`
}

// StartArtifactSync periodically checks the artifacts config and downloads new binaries
func StartArtifactSync(cfg *config.Config) {
	ticker := time.NewTicker(15 * time.Second)
	for range ticker.C {
		if !runner.FileExists(cfg.ConfigPath) {
			continue
		}
		data, err := os.ReadFile(cfg.ConfigPath)
		if err != nil {
			log.Printf("Failed to read artifacts config: %v", err)
			continue
		}
		var cmCfg ConfigMapData
		if err := json.Unmarshal(data, &cmCfg); err != nil {
			log.Printf("Failed to parse artifacts config JSON: %v", err)
			continue
		}

		for host, item := range cmCfg.Artifacts {
			targetPath := filepath.Join(cfg.AppsDir, host)
			typePath := targetPath + ".type"

			_ = os.WriteFile(typePath, []byte(item.Type), 0644)

			if !runner.FileExists(targetPath) {
				log.Printf("Downloading artifact for %s from %s...", host, item.URL)
				err := downloadFile(item.URL, targetPath)
				if err != nil {
					log.Printf("Failed to download %s: %v", item.URL, err)
					continue
				}
				os.Chmod(targetPath, 0755)
				log.Printf("Successfully deployed %s to %s", host, targetPath)
			}
		}
	}
}

func downloadFile(url, targetPath string) error {
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("bad status: %s", resp.Status)
	}

	out, err := os.Create(targetPath)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, resp.Body)
	return err
}
