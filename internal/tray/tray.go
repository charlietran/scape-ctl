// Package tray implements the system tray icon and menu.
package tray

import (
	"fmt"
	"log"
	"os/exec"
	"runtime"
	"sync"
	"time"

	"github.com/getlantern/systray"

	"github.com/definitelygames/scape-ctl/internal/config"
	"github.com/definitelygames/scape-ctl/internal/hid"
	"github.com/definitelygames/scape-ctl/internal/monitor"
)

// App holds the tray application state.
type App struct {
	cfg     *config.Config
	mon     *monitor.Monitor
	events  <-chan monitor.Event
	device  *hid.Device
	mu      sync.Mutex

	// Menu items
	mStatus    *systray.MenuItem
	mBattery   *systray.MenuItem
	mEq        [3]*systray.MenuItem
	mLightOff  *systray.MenuItem
	mLightOn   *systray.MenuItem
	mDevices   *systray.MenuItem
	mConfig    *systray.MenuItem
	mQuit      *systray.MenuItem
}

// New creates the tray app.
func New(cfg *config.Config, mon *monitor.Monitor, events <-chan monitor.Event) *App {
	return &App{
		cfg:    cfg,
		mon:    mon,
		events: events,
	}
}

// OnReady is called by systray when the tray icon is ready.
func (a *App) OnReady() {
	systray.SetTitle("Scape")
	systray.SetTooltip("Scape Control")

	// ── Status section ──
	a.mStatus = systray.AddMenuItem("⊘ No device", "Connection status")
	a.mStatus.Disable()
	a.mBattery = systray.AddMenuItem("Battery: --", "Battery level")
	a.mBattery.Disable()

	systray.AddSeparator()

	// ── EQ presets ──
	mEqParent := systray.AddMenuItem("EQ Preset", "Switch EQ")
	a.mEq[0] = mEqParent.AddSubMenuItem("Slot 1", "EQ Slot 1")
	a.mEq[1] = mEqParent.AddSubMenuItem("Slot 2", "EQ Slot 2")
	a.mEq[2] = mEqParent.AddSubMenuItem("Slot 3", "EQ Slot 3")

	// ── Lighting ──
	mLight := systray.AddMenuItem("Lighting", "Toggle lighting")
	a.mLightOff = mLight.AddSubMenuItem("Off", "Turn off LEDs")
	a.mLightOn = mLight.AddSubMenuItem("On (last mode)", "Turn on LEDs")

	systray.AddSeparator()

	// ── Utility ──
	a.mDevices = systray.AddMenuItem("List Devices", "Show connected HID devices")
	a.mConfig = systray.AddMenuItem("Edit Config", "Open config file")

	systray.AddSeparator()
	a.mQuit = systray.AddMenuItem("Quit", "Exit scape-ctl")

	// Start click handlers
	go a.handleClicks()

	// Start status polling
	go a.pollStatus()

	// Listen for monitor events to update tray
	go a.handleMonitorEvents()

	// Try connecting to a device immediately
	go a.tryConnect()
}

// OnExit is called when the tray app is shutting down.
func (a *App) OnExit() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.device != nil {
		a.device.Close()
	}
	a.mon.Stop()
	log.Println("[tray] exiting")
}

func (a *App) tryConnect() {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.device != nil {
		return // already connected
	}

	dev, err := hid.OpenFirst()
	if err != nil {
		log.Printf("[tray] no device available: %v", err)
		return
	}
	a.device = dev
	a.mStatus.SetTitle(fmt.Sprintf("● %s", dev.Info.ProductName))
}

func (a *App) handleClicks() {
	for {
		select {
		case <-a.mEq[0].ClickedCh:
			a.setEq(1)
		case <-a.mEq[1].ClickedCh:
			a.setEq(2)
		case <-a.mEq[2].ClickedCh:
			a.setEq(3)
		case <-a.mLightOff.ClickedCh:
			a.setLight(false)
		case <-a.mLightOn.ClickedCh:
			a.setLight(true)
		case <-a.mDevices.ClickedCh:
			a.showDevices()
		case <-a.mConfig.ClickedCh:
			a.openConfig()
		case <-a.mQuit.ClickedCh:
			systray.Quit()
			return
		}
	}
}

func (a *App) handleMonitorEvents() {
	for evt := range a.events {
		switch evt.Type {
		case monitor.EventConnected:
			log.Printf("[tray] device connected: %s", evt.Device)
			a.mStatus.SetTitle(fmt.Sprintf("● %s", evt.Device.ProductName))
			go a.tryConnect()

		case monitor.EventDisconnected:
			log.Printf("[tray] device disconnected: %s", evt.Device)
			a.mu.Lock()
			if a.device != nil {
				a.device.Close()
				a.device = nil
			}
			a.mu.Unlock()
			a.mStatus.SetTitle("⊘ No device")
			a.mBattery.SetTitle("Battery: --")
		}
	}
}

func (a *App) pollStatus() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		a.mu.Lock()
		dev := a.device
		a.mu.Unlock()

		if dev == nil {
			continue
		}

		status, err := dev.GetStatus()
		if err != nil {
			log.Printf("[tray] status poll error: %v", err)
			continue
		}
		if status == nil {
			continue
		}

		if !status.Connected {
			a.mStatus.SetTitle("● Dongle connected (headset off)")
			a.mBattery.SetTitle("Battery: --")
			continue
		}

		if status.BatteryPercent >= 0 {
			icon := "🔋"
			if status.Charging {
				icon = "⚡"
			}
			a.mBattery.SetTitle(fmt.Sprintf("%s Battery: %d%%", icon, status.BatteryPercent))
		}
		a.mStatus.SetTitle(fmt.Sprintf("● %s", dev.Info.ProductName))
	}
}

func (a *App) setEq(slot int) {
	a.mu.Lock()
	dev := a.device
	a.mu.Unlock()

	if dev == nil {
		log.Println("[tray] no device connected")
		return
	}
	if err := dev.SetActiveEq(slot); err != nil {
		log.Printf("[tray] set EQ slot %d error: %v", slot, err)
	} else {
		log.Printf("[tray] switched to EQ slot %d", slot)
	}
}

func (a *App) setLight(on bool) {
	a.mu.Lock()
	dev := a.device
	a.mu.Unlock()

	if dev == nil {
		log.Println("[tray] no device connected")
		return
	}

	cfg := &hid.LightingConfig{
		Mode:       hid.LightOff,
		Brightness: 0,
	}
	if on {
		cfg.Mode = hid.LightStatic
		cfg.Brightness = 100
		cfg.Color = hid.RGB{R: 120, G: 140, B: 255}
	}

	if err := dev.SetLighting(cfg); err != nil {
		log.Printf("[tray] set lighting error: %v", err)
	}
}

func (a *App) showDevices() {
	info := hid.DumpDevices()
	log.Println(info)
	// Also try to show in a notification or dialog
	if a.cfg.Settings.Notifications {
		notify("Scape Devices", info)
	}
}

func (a *App) openConfig() {
	path := config.Path()
	config.EnsureExists()

	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", path)
	case "windows":
		cmd = exec.Command("notepad", path)
	default:
		// Try xdg-open, fall back to showing path
		cmd = exec.Command("xdg-open", path)
	}

	if err := cmd.Start(); err != nil {
		log.Printf("[tray] failed to open config: %v", err)
		log.Printf("[tray] config location: %s", path)
	}
}

// notify sends a desktop notification (best-effort).
func notify(title, body string) {
	switch runtime.GOOS {
	case "linux":
		_ = exec.Command("notify-send", title, body).Start()
	case "darwin":
		script := fmt.Sprintf(`display notification "%s" with title "%s"`, body, title)
		_ = exec.Command("osascript", "-e", script).Start()
	case "windows":
		// PowerShell toast
		ps := fmt.Sprintf(
			`[Windows.UI.Notifications.ToastNotificationManager, Windows.UI.Notifications, ContentType = WindowsRuntime] | Out-Null; `+
				`$xml = [Windows.UI.Notifications.ToastNotificationManager]::GetTemplateContent(0); `+
				`$xml.GetElementsByTagName('text')[0].AppendChild($xml.CreateTextNode('%s')) | Out-Null; `+
				`[Windows.UI.Notifications.ToastNotificationManager]::CreateToastNotifier('scape-ctl').Show([Windows.UI.Notifications.ToastNotification]::new($xml))`,
			title+": "+body,
		)
		_ = exec.Command("powershell", "-Command", ps).Start()
	}
}
