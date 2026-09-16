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
		for host, lastTime := range activeMap {
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
