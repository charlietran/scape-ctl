// Package tray implements the system tray icon and menu.
package tray

import (
	_ "embed"
	"fmt"
	"log"
	"os"
	"os/exec"
	"runtime"
	"sync"
	"syscall"

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

// App holds the tray application state.
type App struct {
	cfg      *config.Config
	mon      *monitor.Monitor
	triggers *triggers.Runner
	events   <-chan monitor.Event
	mu       sync.Mutex

	// Menu items
	mReceiver  *systray.MenuItem
	mHeadset   *systray.MenuItem
	mBattery   *systray.MenuItem
	mEqParent  *systray.MenuItem
	mEq        [3]*systray.MenuItem
	mLightTog  *systray.MenuItem
	lightOn    bool
	mMicMute   *systray.MenuItem
	mMNCTog    *systray.MenuItem
	mncOn      bool
	mDispBlack *systray.MenuItem
	mDispWhite *systray.MenuItem
	mDispText  *systray.MenuItem
	mConfigDir *systray.MenuItem
	mReload    *systray.MenuItem
	mQuit      *systray.MenuItem
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

	// ── Receiver status ──
	a.mReceiver = systray.AddMenuItem("Checking device status...", "Dongle status")
	a.mReceiver.Disable()

	systray.AddSeparator()

	// ── Headset section (all hidden until headset connects) ──
	a.mHeadset = systray.AddMenuItem("Headset: Disconnected", "Headset status")
	a.mHeadset.Disable()
	a.mMicMute = systray.AddMenuItem("", "Mic mute status (hardware)")
	a.mMicMute.Disable()
	a.mMicMute.Hide()
	a.mBattery = systray.AddMenuItem("", "Battery level")
	a.mBattery.Disable()
	a.mBattery.Hide()
	a.mEqParent = systray.AddMenuItem("EQ Preset", "Switch EQ")
	a.mEq[0] = a.mEqParent.AddSubMenuItem("Slot 1", "EQ Slot 1")
	a.mEq[1] = a.mEqParent.AddSubMenuItem("Slot 2", "EQ Slot 2")
	a.mEq[2] = a.mEqParent.AddSubMenuItem("Slot 3", "EQ Slot 3")
	a.mEqParent.Hide()
	a.mLightTog = systray.AddMenuItem("RGB: Off", "Toggle RGB lighting")
	a.mLightTog.Hide()
	a.mMNCTog = systray.AddMenuItem("Mic Noise Cancellation: Off", "Toggle MNC")
	a.mMNCTog.Hide()

	systray.AddSeparator()

	// ── Display ──
	mDisp := systray.AddMenuItem("Tray Display", "Change tray display")
	a.mDispText = mDisp.AddSubMenuItem("Text", "Text label in tray")
	a.mDispBlack = mDisp.AddSubMenuItem("Black Icon", "Black tray icon")
	a.mDispWhite = mDisp.AddSubMenuItem("White Icon", "White tray icon")
	a.updateDispCheck()

	// ── Utility ──
	a.mConfigDir = systray.AddMenuItem("Open Config Folder", "Open config directory")
	a.mReload = systray.AddMenuItem("Reload Config", "Reload config from disk")

	systray.AddSeparator()
	a.mQuit = systray.AddMenuItem("Quit", "Exit scape-ctl")

	// Start click handlers
	go a.handleClicks()

	// Listen for monitor events to update tray UI
	go a.handleMonitorEvents()
}

// OnExit is called when the tray app is shutting down.
func (a *App) OnExit() {
	a.mon.Stop()
	log.Println("[tray] exiting")
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
		case <-a.mMNCTog.ClickedCh:
			a.toggleMNC()
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
		case monitor.EventDongleConnected:
			a.mReceiver.SetTitle("USB Receiver: Connected")
			a.mHeadset.SetTitle("Headset: Checking...")

		case monitor.EventDongleDisconnected:
			a.mReceiver.SetTitle("USB Receiver: Disconnected")
			a.mHeadset.SetTitle("Headset: Disconnected")
			a.hideHeadsetControls()

		case monitor.EventHeadsetPowerOn:
			a.mHeadset.SetTitle("Headset: Connected")

		case monitor.EventHeadsetPowerOff:
			a.mHeadset.SetTitle("Headset: Disconnected")
			a.hideHeadsetControls()

		case monitor.EventHeadsetStatus:
			s := evt.Status
			if s == nil {
				continue
			}
			a.mReceiver.SetTitle("USB Receiver: Connected")
			a.mHeadset.SetTitle("Headset: Connected")
			a.mHeadset.Show()
			a.updateMicStatus(s.BoomMicConnected, s.Muted)
			if s.BatteryPercent >= 0 {
				a.mBattery.SetTitle(fmt.Sprintf("Battery: %d%%", s.BatteryPercent))
				a.mBattery.Show()
			}
			a.mEqParent.Show()
			a.updateEqCheck(s.EqSlot)
			a.mLightTog.Show()
			a.mu.Lock()
			a.lightOn = s.LightSlot > 0
			a.mncOn = s.MNCOn
			a.mu.Unlock()
			a.updateLightStatus(s.LightSlot > 0)
			a.mMNCTog.Show()
			a.updateMNCStatus(s.MNCOn)
		}
	}
}

// sendCommand runs a function on the monitor's persistent device connection.
func (a *App) sendCommand(fn func(dev *hid.Device) error) error {
	dev := a.mon.Device()
	if dev == nil {
		return fmt.Errorf("no device connected")
	}
	return fn(dev)
}

func (a *App) setEq(slot int) {
	if err := a.sendCommand(func(dev *hid.Device) error {
		return dev.SetActiveEq(slot)
	}); err != nil {
		log.Printf("[tray] set EQ slot %d error: %v", slot, err)
	} else {
		log.Printf("[tray] switched to EQ slot %d", slot)
		a.updateEqCheck(slot)
	}
}

func (a *App) updateEqCheck(slot int) {
	a.mEqParent.SetTitle(fmt.Sprintf("EQ Preset: %d", slot))
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
	on := !a.lightOn
	a.mu.Unlock()

	if err := a.sendCommand(func(dev *hid.Device) error {
		rid, payload := hid.BuildSetLightOn(on)
		return dev.Send(rid, payload)
	}); err != nil {
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

func (a *App) toggleMNC() {
	a.mu.Lock()
	on := !a.mncOn
	a.mu.Unlock()

	if err := a.sendCommand(func(dev *hid.Device) error {
		rid, payload := hid.BuildSetMNC(on)
		return dev.Send(rid, payload)
	}); err != nil {
		log.Printf("[tray] set MNC error: %v", err)
	} else {
		a.mu.Lock()
		a.mncOn = on
		a.mu.Unlock()
		a.updateMNCStatus(on)
		log.Printf("[tray] MNC %v", on)
	}
}

func (a *App) hideHeadsetControls() {
	a.mMicMute.Hide()
	a.mBattery.Hide()
	a.mEqParent.Hide()
	a.mLightTog.Hide()
	a.mMNCTog.Hide()
}

func (a *App) updateMicStatus(boomMic, muted bool) {
	mic := "Built-in"
	if boomMic {
		mic = "Boom"
	}
	state := "Unmuted"
	if muted {
		state = "Muted"
	}
	a.mMicMute.SetTitle(fmt.Sprintf("Headset Mic: %s, %s", mic, state))
	a.mMicMute.Show()
}

func (a *App) updateMNCStatus(on bool) {
	if on {
		a.mMNCTog.SetTitle("Mic Noise Cancellation: On")
		a.mMNCTog.Check()
	} else {
		a.mMNCTog.SetTitle("Mic Noise Cancellation: Off")
		a.mMNCTog.Uncheck()
	}
}

func (a *App) reloadConfig() {
	cfg, err := config.LoadErr()
	if err != nil {
		log.Printf("[tray] config reload error: %v", err)
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
}

func (a *App) applyDisplay() {
	a.mu.Lock()
	mode := a.cfg.Settings.TrayDisplay
	text := a.cfg.Settings.TrayText
	a.mu.Unlock()

	if mode == "" {
		mode = "text"
	}
	if text == "" {
		text = "Scape"
	}
	if len(text) > 16 {
		text = text[:16]
	}

	switch mode {
	case "black":
		systray.SetIcon(iconBlack)
		systray.SetTitle("")
	case "white":
		systray.SetIcon(iconWhite)
		systray.SetTitle("")
	default: // "text"
		systray.SetTitle(text)
	}
}

func (a *App) updateDispCheck() {
	a.mu.Lock()
	mode := a.cfg.Settings.TrayDisplay
	a.mu.Unlock()
	if mode == "" {
		mode = "text"
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
	oldMode := a.cfg.Settings.TrayDisplay
	if oldMode == "" {
		oldMode = "black"
	}
	a.cfg.Settings.TrayDisplay = mode
	a.mu.Unlock()

	if err := config.SetValue("tray_display", mode); err != nil {
		log.Printf("[tray] failed to save display setting: %v", err)
		return
	}

	// Switching to/from text mode requires a restart since systray
	// can't remove an icon once set (and can't add one after startup).
	needsRestart := (oldMode == "text") != (mode == "text")
	if needsRestart {
		log.Printf("[tray] display mode changed to %s, restarting...", mode)
		a.restart()
		return
	}

	a.applyDisplay()
	a.updateDispCheck()
}

func (a *App) restart() {
	exe, err := os.Executable()
	if err != nil {
		log.Printf("[tray] failed to get executable path: %v", err)
		return
	}
	cmd := exec.Command(exe)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	// Detach the new process so it survives if the terminal closes
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		log.Printf("[tray] failed to restart: %v", err)
		return
	}
	systray.Quit()
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
