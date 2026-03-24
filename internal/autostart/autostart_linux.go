package autostart

import (
	"fmt"
	"os"
	"path/filepath"
)

const desktopFileName = "scapectl.desktop"

func desktopFilePath() string {
	cfgDir, err := os.UserConfigDir()
	if err != nil {
		home, _ := os.UserHomeDir()
		cfgDir = filepath.Join(home, ".config")
	}
	return filepath.Join(cfgDir, "autostart", desktopFileName)
}

func Enabled() bool {
	_, err := os.Stat(desktopFilePath())
	return err == nil
}

func Enable() error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("get executable path: %w", err)
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return fmt.Errorf("resolve executable path: %w", err)
	}

	entry := fmt.Sprintf(`[Desktop Entry]
Type=Application
Name=Scape Control
Exec=%s
X-GNOME-Autostart-enabled=true
`, exe)

	dir := filepath.Dir(desktopFilePath())
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create autostart dir: %w", err)
	}
	return os.WriteFile(desktopFilePath(), []byte(entry), 0o644)
}

func Disable() error {
	err := os.Remove(desktopFilePath())
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
