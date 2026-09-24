package reaper

import (
	"fmt"
	"log"
	"time"

	"git.fairfaxmedia.net/open-cream-cheese/zero-scale-platform/internal/banner"
	"git.fairfaxmedia.net/open-cream-cheese/zero-scale-platform/internal/config"
	"git.fairfaxmedia.net/open-cream-cheese/zero-scale-platform/internal/runner"
)

type Reaper struct {
	cfg         *config.Config
	stopFunc    func(string) error
	getActive   func() map[string]time.Time
}

func New(cfg *config.Config, getActive func() map[string]time.Time, stopFunc func(string) error) *Reaper {
	return &Reaper{
		cfg:       cfg,
		getActive: getActive,
		stopFunc:  stopFunc,
	}
}

// Start begins the background reaper ticker loop
func (r *Reaper) Start() {
	ticker := time.NewTicker(r.cfg.CheckPeriod)
	for range ticker.C {
		now := time.Now()
		activeMap := r.getActive()
		runningHosts := runner.ListRunningAppUnits()

		inspected := make(map[string]bool)

		// 1. Inspect all actively running systemd units across the host
		for _, host := range runningHosts {
			inspected[host] = true
			lastTime, tracked := activeMap[host]
			if !tracked {
				unitName := fmt.Sprintf("app-%s", host)
				startTime, err := runner.GetServiceStartTime(unitName)
				if err == nil && !startTime.IsZero() {
					lastTime = startTime
				} else {
					lastTime = now
				}
			}

			if now.Sub(lastTime) > r.cfg.IdleTimeout {
				unitName := fmt.Sprintf("app-%s", host)
				banner.PrintScaleToZero(host, r.cfg.IdleTimeout)
				if err := r.stopFunc(host); err != nil {
					log.Printf("Failed to stop idle unit %s: %v", unitName, err)
				}
			}
		}

		// 2. Also check any units still in activeMap that might not have been caught in runningHosts
		for host, lastTime := range activeMap {
			if inspected[host] {
				continue
			}
			unitName := fmt.Sprintf("app-%s", host)
			active, err := runner.IsServiceActive(unitName)
			if err == nil && active && now.Sub(lastTime) > r.cfg.IdleTimeout {
				banner.PrintScaleToZero(host, r.cfg.IdleTimeout)
				if err := r.stopFunc(host); err != nil {
					log.Printf("Failed to stop idle unit %s: %v", unitName, err)
				}
			}
		}
	}
}
