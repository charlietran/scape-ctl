// Package config handles loading/saving the scape-ctl configuration.
//
// Config file location: ~/.config/scape-ctl/config.toml (macOS: ~/Library/Application Support/scape-ctl/)
package config

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	toml "github.com/pelletier/go-toml/v2"
)

type Config struct {
	Settings Settings      `toml:"settings"`
	Triggers []TriggerRule `toml:"triggers,omitempty"`
}

type Settings struct {
	PollIntervalMS int    `toml:"poll_interval_ms"`
	TrayDisplay    string `toml:"tray_display"` // "black", "white", or "text"
	TrayText       string `toml:"tray_text"`    // custom text when tray_display is "text" (max 16 chars)
	Verbose        bool   `toml:"verbose"`      // enable verbose logging
}

type TriggerRule struct {
	Event    string `toml:"event"`    // Event name (see docs)
	Script   string `toml:"script"`   // Shell command or script path
	Label    string `toml:"label"`    // Human-readable name
	Enabled  bool   `toml:"enabled"`
	Cooldown int    `toml:"cooldown"` // Minimum seconds between firings (0 = no cooldown)
	Battery  int    `toml:"battery"`  // For BatteryLevel event: fire when battery <= this % (default: 20)
}

func DefaultConfig() *Config {
	return &Config{
		Settings: Settings{
			PollIntervalMS: 1500,
			TrayDisplay:    "text",
			TrayText:       "Scape",
		},
		Triggers: nil,
	}
}

// Dir returns the config directory path.
func Dir() string {
	if cfgDir, err := os.UserConfigDir(); err == nil {
		return filepath.Join(cfgDir, "scape-ctl")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "scape-ctl")
}

// Path returns the config file path.
func Path() string {
	return filepath.Join(Dir(), "config.toml")
}

// Load reads config from disk, returning defaults if not found.
func Load() *Config {
	cfg, err := LoadErr()
	if err != nil {
		log.Printf("[config] %v, using defaults", err)
		return DefaultConfig()
	}
	return cfg
}

// LoadErr reads config from disk, returning an error on parse failure.
// Returns defaults (nil error) if the file doesn't exist.
func LoadErr() (*Config, error) {
	cfg := DefaultConfig()
	data, err := os.ReadFile(Path())
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return cfg, fmt.Errorf("read error: %w", err)
	}
	if err := toml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse error: %w", err)
	}
	log.Printf("[config] loaded from %s (%d triggers)", Path(), len(cfg.Triggers))
	return cfg, nil
}

// SetValue updates a single key in the config file in-place, preserving
// comments and formatting. If the key exists, its value is replaced.
// If not, it's appended under [settings].
func SetValue(key, value string) error {
	path := Path()
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}

	quoted := fmt.Sprintf("%s = %q", key, value)
	lines := strings.Split(string(data), "\n")
	found := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, key+" ") || strings.HasPrefix(trimmed, key+"=") {
			lines[i] = quoted
			found = true
			break
		}
	}

	if !found {
		// Append after [settings] header
		for i, line := range lines {
			if strings.TrimSpace(line) == "[settings]" {
				lines = append(lines[:i+1], append([]string{quoted}, lines[i+1:]...)...)
				found = true
				break
			}
		}
	}

	if !found {
		lines = append(lines, quoted)
	}

	return os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o644)
}

// defaultConfigTOML is the commented default config written on first run.
const defaultConfigTOML = `# scape-ctl configuration
# Location: ` + "`" + `scape-ctl help` + "`" + ` shows the path for your OS.

[settings]

# How often to poll the USB bus for device changes (milliseconds).
# Minimum: 200. Default: 1000.
poll_interval_ms = 1500

# Tray icon display mode: "black", "white", or "text".
# "black" and "white" show the Scape icon (pick whichever suits your
# menu bar theme). "text" shows a text label instead — see tray_text.
# Changing between icon and text modes restarts the tray app.
tray_display = "text"

# Text shown in the menu bar when tray_display = "text".
# Maximum 16 characters (longer strings are truncated).
tray_text = "Scape"

# Enable verbose logging (shows device open/close, status polls, etc.).
verbose = false

# ── Trigger scripts ──────────────────────────────────────────────
#
# Scripts that run automatically on device events.
#
# Events:
#   "DongleConnected"      USB dongle plugged in
#   "DongleDisconnected"   USB dongle unplugged
#   "HeadsetPowerOn"       Headset turned on (detected via dongle)
#   "HeadsetPowerOff"      Headset turned off or out of range
#   "BatteryLevel"         Battery update (use "battery" field for threshold)
#   "MicMuted"             Mic muted (boom flipped up)
#   "MicUnmuted"           Mic unmuted (boom flipped down)
#   "EqChanged"            EQ preset slot changed
#   "RgbOn"                RGB lighting turned on
#   "RgbOff"               RGB lighting turned off
#
# Fields:
#   event    = Event name (required)
#   script   = Shell command to run (required)
#   label    = Human-readable name (optional)
#   enabled  = true/false (required)
#   cooldown = Minimum seconds between firings (optional, default: 0)
#   battery  = For BatteryLevel: fire when battery <= this % (optional, default: 20)
#
# Environment variables passed to scripts:
#
#   SCAPE_EVENT     Event name
#   SCAPE_DEVICE    Product name (e.g. "Fractal Scape Dongle")
#   SCAPE_VID       Vendor ID in hex (e.g. "36bc")
#   SCAPE_PID       Product ID in hex (e.g. "0001")
#   SCAPE_PATH      OS device path
#   SCAPE_TIMESTAMP ISO 8601 timestamp
#   SCAPE_JSON      Full event as JSON
#   SCAPE_BATTERY   Battery percentage (BatteryLevel events only)
#
# Examples (uncomment to enable):
#
# [[triggers]]
# event    = "HeadsetPowerOn"
# script   = "osascript -e 'display notification \"Headset connected\" with title \"Scape\"'"
# label    = "Headset on notification"
# enabled  = true
# cooldown = 5
#
# [[triggers]]
# event    = "BatteryLevel"
# script   = "osascript -e 'display notification \"Battery at $SCAPE_BATTERY%\" with title \"Scape\"'"
# label    = "Low battery warning"
# enabled  = true
# battery  = 20
# cooldown = 300
`

// EnsureExists creates a default config file if none exists.
func EnsureExists() {
	if _, err := os.Stat(Path()); os.IsNotExist(err) {
		dir := Dir()
		if err := os.MkdirAll(dir, 0o755); err != nil {
			log.Printf("[config] failed to create dir: %v", err)
			return
		}
		if err := os.WriteFile(Path(), []byte(defaultConfigTOML), 0o644); err != nil {
			log.Printf("[config] failed to write default config: %v", err)
			return
		}
		log.Printf("[config] created default config at %s", Path())
	}
}
