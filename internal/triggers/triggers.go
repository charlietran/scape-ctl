// Package triggers runs user-configured scripts when device events occur.
//
// Scripts receive event context via environment variables:
//
//	SCAPE_EVENT      = event name
//	SCAPE_DEVICE     = product name
//	SCAPE_VID        = vendor ID (hex)
//	SCAPE_PID        = product ID (hex)
//	SCAPE_PATH       = device path
//	SCAPE_TIMESTAMP  = ISO 8601 timestamp
//	SCAPE_BATTERY    = battery percentage (BatteryLevel events only)
package triggers

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"runtime"
	"sync"
	"time"

	"github.com/charlietran/scape-ctl/internal/config"
	"github.com/charlietran/scape-ctl/internal/monitor"
)

// Runner listens for monitor events and fires matching trigger scripts.
type Runner struct {
	cfg      *config.Config
	mu       sync.Mutex
	lastFire map[string]time.Time // label → last fire time (for cooldown)
}

// New creates a trigger runner with the given config.
func New(cfg *config.Config) *Runner {
	return &Runner{
		cfg:      cfg,
		lastFire: make(map[string]time.Time),
	}
}

// Reload swaps in a new config (e.g. after user edits the file).
func (r *Runner) Reload(cfg *config.Config) {
	r.cfg = cfg
	log.Printf("[triggers] reloaded (%d rules)", len(cfg.Triggers))
}

// Run listens on the event channel and dispatches triggers. Blocking.
func (r *Runner) Run(events <-chan monitor.Event) {
	for evt := range events {
		r.dispatch(evt)
	}
}

func (r *Runner) dispatch(evt monitor.Event) {
	evtStr := evt.Type.String()

	// HeadsetStatus and BatteryLevel fire every poll — only log in verbose mode
	if evt.Type == monitor.EventHeadsetStatus || evt.Type == monitor.EventBatteryLevel {
		if r.cfg.Settings.Verbose {
			log.Printf("[event] %s: %s", evtStr, evt.Device)
		}
	} else {
		log.Printf("[event] %s: %s", evtStr, evt.Device)
	}

	for _, rule := range r.cfg.Triggers {
		if !rule.Enabled {
			continue
		}
		if rule.Event != evtStr {
			continue
		}

		// BatteryLevel: only fire if battery <= configured threshold
		if evt.Type == monitor.EventBatteryLevel && evt.Status != nil {
			threshold := rule.Battery
			if threshold <= 0 {
				threshold = 20 // default
			}
			if evt.Status.BatteryPercent > threshold {
				continue
			}
		}

		// Cooldown: skip if fired too recently
		if rule.Cooldown > 0 {
			key := rule.Label
			if key == "" {
				key = rule.Script
			}
			r.mu.Lock()
			last, exists := r.lastFire[key]
			cooldownDur := time.Duration(rule.Cooldown) * time.Second
			if exists && time.Since(last) < cooldownDur {
				r.mu.Unlock()
				continue
			}
			r.lastFire[key] = time.Now()
			r.mu.Unlock()
		}

		label := rule.Label
		if label == "" {
			label = rule.Script
		}
		log.Printf("[triggers] firing '%s' for %s event", label, evtStr)

		go r.exec(rule, evt)
	}
}

func (r *Runner) exec(rule config.TriggerRule, evt monitor.Event) {
	evtJSON, _ := json.Marshal(map[string]string{
		"event":     evt.Type.String(),
		"device":    evt.Device.ProductName,
		"vid":       fmt.Sprintf("%04x", evt.Device.VendorID),
		"pid":       fmt.Sprintf("%04x", evt.Device.ProductID),
		"path":      evt.Device.Path,
		"timestamp": evt.Timestamp.Format("2006-01-02T15:04:05Z07:00"),
	})

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("cmd", "/C", rule.Script)
	} else {
		cmd = exec.Command("sh", "-c", rule.Script)
	}

	env := append(os.Environ(),
		"SCAPE_EVENT="+evt.Type.String(),
		"SCAPE_DEVICE="+evt.Device.ProductName,
		"SCAPE_VID="+fmt.Sprintf("%04x", evt.Device.VendorID),
		"SCAPE_PID="+fmt.Sprintf("%04x", evt.Device.ProductID),
		"SCAPE_PATH="+evt.Device.Path,
		"SCAPE_TIMESTAMP="+evt.Timestamp.Format("2006-01-02T15:04:05Z07:00"),
		"SCAPE_JSON="+string(evtJSON),
	)

	// Add battery percentage for BatteryLevel events
	if evt.Status != nil && evt.Status.BatteryPercent >= 0 {
		env = append(env, fmt.Sprintf("SCAPE_BATTERY=%d", evt.Status.BatteryPercent))
	}

	cmd.Env = env

	output, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("[triggers] script error '%s': %v\n  output: %s", rule.Script, err, string(output))
	} else {
		log.Printf("[triggers] script ok '%s'", rule.Label)
		if len(output) > 0 {
			log.Printf("[triggers]   output: %s", string(output))
		}
	}
}
