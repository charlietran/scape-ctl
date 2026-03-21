// Package tray implements the system tray icon and menu.
package tray

import (
	_ "embed"
	"fmt"
	"log"
	"os/exec"
	"runtime"
	"sync"
	"time"

	"github.com/getlantern/systray"

	"github.com/charlietran/scape-ctl/internal/config"
	"github.com/charlietran/scape-ctl/internal/hid"
	"github.com/charlietran/scape-ctl/internal/monitor"
	"github.com/charlietran/scape-ctl/internal/triggers"
)

//go:embed icons/black.png
var iconBlack []byte

//go:embed icons/white.png
var iconWhite []byte

//go:embed icons/transparent.png
var iconTransparent []byte

// App holds the tray application state.
type App struct {
	cfg     *config.Config
	mon     *monitor.Monitor
	triggers *triggers.Runner
	events  <-chan monitor.Event
	device  *hid.Device
	mu      sync.Mutex

	// Menu items
	mStatus      *systray.MenuItem
	mBattery     *systray.MenuItem
	mEq          [3]*systray.MenuItem
	mLightTog    *systray.MenuItem
	lightOn      bool
	mDispBlack   *systray.MenuItem
	mDispWhite   *systray.MenuItem
	mDispText    *systray.MenuItem
	mConfigDir   *systray.MenuItem
	mReload      *systray.MenuItem
	mQuit        *systray.MenuItem
}

// New creates the tray app.
func New(cfg *config.Config, mon *monitor.Monitor, tr *triggers.Runner, events <-chan monitor.Event) *App {
	return &App{
		cfg:      cfg,
		mon:      mon,
		triggers: tr,
		events:   events,
	}
}

// OnReady is called by systray when the tray icon is ready.
func (a *App) OnReady() {
	systray.SetTooltip("Scape Control")
	a.applyDisplay()

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
	a.mLightTog = systray.AddMenuItem("RGB: Off", "Toggle RGB lighting")

	systray.AddSeparator()

	// ── Display ──
	mDisp := systray.AddMenuItem("Tray Icon", "Change tray display")
	a.mDispBlack = mDisp.AddSubMenuItem("Black Icon", "Black tray icon")
	a.mDispWhite = mDisp.AddSubMenuItem("White Icon", "White tray icon")
	a.mDispText = mDisp.AddSubMenuItem("Text", "Text label in tray")
	a.updateDispCheck()

	// ── Utility ──
	a.mConfigDir = systray.AddMenuItem("Open Config Folder", "Open config directory")
	a.mReload = systray.AddMenuItem("Reload Config", "Reload config from disk")

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
		case <-a.mLightTog.ClickedCh:
			a.toggleLight()
		case <-a.mDispBlack.ClickedCh:
			a.setDisplay("black")
		case <-a.mDispWhite.ClickedCh:
			a.setDisplay("white")
		case <-a.mDispText.ClickedCh:
			a.setDisplay("text")
		case <-a.mConfigDir.ClickedCh:
			a.openConfigDir()
		case <-a.mReload.ClickedCh:
			a.reloadConfig()
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
		a.updateEqCheck(status.EqSlot)
		a.mu.Lock()
		a.lightOn = status.LightSlot > 0
		a.mu.Unlock()
		a.updateLightStatus(status.LightSlot > 0)
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
		a.updateEqCheck(slot)
	}
}

func (a *App) updateEqCheck(slot int) {
	for i := 0; i < 3; i++ {
		if i+1 == slot {
			a.mEq[i].Check()
		} else {
			a.mEq[i].Uncheck()
		}
	}
}

func (a *App) toggleLight() {
	a.mu.Lock()
	dev := a.device
	on := !a.lightOn
	a.mu.Unlock()

	if dev == nil {
		log.Println("[tray] no device connected")
		return
	}

	rid, payload := hid.BuildSetLightOn(on)
	if err := dev.Send(rid, payload); err != nil {
		log.Printf("[tray] set lighting error: %v", err)
	} else {
		a.mu.Lock()
		a.lightOn = on
		a.mu.Unlock()
		a.updateLightStatus(on)
		log.Printf("[tray] RGB %v", on)
	}
}

func (a *App) updateLightStatus(on bool) {
	if on {
		a.mLightTog.SetTitle("RGB: On")
		a.mLightTog.Check()
	} else {
		a.mLightTog.SetTitle("RGB: Off")
		a.mLightTog.Uncheck()
	}
}


func (a *App) reloadConfig() {
	cfg, err := config.LoadErr()
	if err != nil {
		log.Printf("[tray] config reload error: %v", err)
		notify("Scape Config Error", err.Error())
		a.mReload.SetTitle("Reload Config (error!)")
		return
	}
	a.mu.Lock()
	a.cfg = cfg
	a.mu.Unlock()
	if a.triggers != nil {
		a.triggers.Reload(cfg)
	}
	a.mReload.SetTitle("Reload Config")
	log.Printf("[tray] config reloaded (%d triggers)", len(cfg.Triggers))
	notify("Scape", fmt.Sprintf("Config reloaded (%d triggers)", len(cfg.Triggers)))
}

func (a *App) applyDisplay() {
	a.mu.Lock()
	mode := a.cfg.Settings.TrayDisplay
	text := a.cfg.Settings.TrayText
	a.mu.Unlock()

	if mode == "" {
		mode = "black"
	}
	if text == "" {
		text = "Scape"
	}

	switch mode {
	case "white":
		systray.SetIcon(iconWhite)
		systray.SetTitle("")
	case "text":
		systray.SetTemplateIcon(iconTransparent, iconTransparent)
		systray.SetTitle(text)
	default: // "black"
		systray.SetIcon(iconBlack)
		systray.SetTitle("")
	}
}

func (a *App) updateDispCheck() {
	a.mu.Lock()
	mode := a.cfg.Settings.TrayDisplay
	a.mu.Unlock()
	if mode == "" {
		mode = "black"
	}

	a.mDispBlack.Uncheck()
	a.mDispWhite.Uncheck()
	a.mDispText.Uncheck()
	switch mode {
	case "white":
		a.mDispWhite.Check()
	case "text":
		a.mDispText.Check()
	default:
		a.mDispBlack.Check()
	}
}

func (a *App) setDisplay(mode string) {
	a.mu.Lock()
	a.cfg.Settings.TrayDisplay = mode
	cfg := a.cfg
	a.mu.Unlock()

	a.applyDisplay()
	a.updateDispCheck()

	if err := config.Save(cfg); err != nil {
		log.Printf("[tray] failed to save display setting: %v", err)
	}
}

func (a *App) openConfigDir() {
	dir := config.Dir()
	config.EnsureExists()

	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", dir)
	case "windows":
		cmd = exec.Command("explorer", dir)
	default:
		cmd = exec.Command("xdg-open", dir)
	}

	if err := cmd.Start(); err != nil {
		log.Printf("[tray] failed to open config dir: %v", err)
		log.Printf("[tray] config location: %s", dir)
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
