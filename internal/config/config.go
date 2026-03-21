// Package config handles loading/saving the scape-ctl configuration.
//
// Config file location: ~/.config/scape-ctl/config.json
//
// Example:
//
//	{
//	  "settings": {
//	    "poll_interval_ms": 1000,
//	    "notifications": true
//	  },
//	  "triggers": [
//	    {
//	      "event": "Connected",
//	      "script": "notify-send 'Scape' 'Headset connected'",
//	      "label": "Connect notification",
//	      "enabled": true
//	    }
//	  ]
//	}
package config

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
)

type Config struct {
	Settings Settings      `json:"settings"`
	Triggers []TriggerRule `json:"triggers,omitempty"`
}

type Settings struct {
	PollIntervalMS int  `json:"poll_interval_ms"`
	Notifications  bool `json:"notifications"`
}

type TriggerRule struct {
	Event   string `json:"event"`   // "Connected" or "Disconnected"
	Script  string `json:"script"`  // Shell command or script path
	Label   string `json:"label"`   // Human-readable name
	Enabled bool   `json:"enabled"`
}

func DefaultConfig() *Config {
	return &Config{
		Settings: Settings{
			PollIntervalMS: 1000,
			Notifications:  true,
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
	return filepath.Join(Dir(), "config.json")
}

// Load reads config from disk, returning defaults if not found.
func Load() *Config {
	cfg := DefaultConfig()
	data, err := os.ReadFile(Path())
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("[config] read error: %v, using defaults", err)
		}
		return cfg
	}
	if err := json.Unmarshal(data, cfg); err != nil {
		log.Printf("[config] parse error: %v, using defaults", err)
		return DefaultConfig()
	}
	log.Printf("[config] loaded from %s (%d triggers)", Path(), len(cfg.Triggers))
	return cfg
}

// Save writes config to disk.
func Save(cfg *Config) error {
	dir := Dir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := os.WriteFile(Path(), data, 0o644); err != nil {
		return err
	}
	log.Printf("[config] saved to %s", Path())
	return nil
}

// EnsureExists creates a default config file if none exists.
func EnsureExists() {
	if _, err := os.Stat(Path()); os.IsNotExist(err) {
		log.Printf("[config] creating default config at %s", Path())
		_ = Save(DefaultConfig())
	}
}
