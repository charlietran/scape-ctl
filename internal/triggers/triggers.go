// Package triggers runs user-configured scripts when device events occur.
//
// Scripts receive event context via environment variables:
//
//	SCAPE_EVENT      = "Connected" or "Disconnected"
//	SCAPE_DEVICE     = product name
//	SCAPE_VID        = vendor ID (hex)
//	SCAPE_PID        = product ID (hex)
//	SCAPE_PATH       = device path
//	SCAPE_TIMESTAMP  = ISO 8601 timestamp
package triggers

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"runtime"

	"github.com/charlietran/scape-ctl/internal/config"
	"github.com/charlietran/scape-ctl/internal/monitor"
)

// Runner listens for monitor events and fires matching trigger scripts.
type Runner struct {
	cfg *config.Config
}

// New creates a trigger runner with the given config.
func New(cfg *config.Config) *Runner {
	return &Runner{cfg: cfg}
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

	for _, rule := range r.cfg.Triggers {
		if !rule.Enabled {
			continue
		}
		if rule.Event != evtStr {
			continue
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

	cmd.Env = append(os.Environ(),
		"SCAPE_EVENT="+evt.Type.String(),
		"SCAPE_DEVICE="+evt.Device.ProductName,
		"SCAPE_VID="+fmt.Sprintf("%04x", evt.Device.VendorID),
		"SCAPE_PID="+fmt.Sprintf("%04x", evt.Device.ProductID),
		"SCAPE_PATH="+evt.Device.Path,
		"SCAPE_TIMESTAMP="+evt.Timestamp.Format("2006-01-02T15:04:05Z07:00"),
		"SCAPE_JSON="+string(evtJSON),
	)

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
