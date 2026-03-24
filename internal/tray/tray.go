// Package tray implements the system tray icon and menu.
package tray

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"fyne.io/systray"

	"github.com/charlietran/scapectl/internal/autostart"
	"github.com/charlietran/scapectl/internal/config"
	"github.com/charlietran/scapectl/internal/hid"
	"github.com/charlietran/scapectl/internal/monitor"
	"github.com/charlietran/scapectl/internal/triggers"
)

//go:embed icons/icon_black.png
var iconBlackPNG []byte // macOS template icon (black + alpha)

//go:embed icons/icon_white.png
var iconWhitePNG []byte // Linux icon

//go:embed icons/icon_white.ico
var iconWhiteICO []byte // Windows icon

// clickCh returns the ClickedCh for a menu item, or nil if the item is nil.
// A nil channel is never selected in a select statement.
func clickCh(item *systray.MenuItem) <-chan struct{} {
	if item == nil {
		return nil
	}
	return item.ClickedCh
}

// effectiveMode resolves a raw config display mode to the actual mode on this
// platform. macOS maps everything except "text" to "icon" (template icon).
// Windows always uses "icon" (SetTitle is a no-op). Linux maps "icon" to
// "white"; keeps "white" and "text".
func effectiveMode(mode string) string {
	if mode == "" {
		mode = "icon"
	}
	switch runtime.GOOS {
	case "darwin":
		if mode == "text" {
			return "text"
		}
		return "icon"
	case "windows":
		return "icon"
	default: // linux
		if mode == "icon" {
			return "white"
		}
		if mode == "white" || mode == "text" {
			return mode
		}
		return "white"
	}
}

// App holds the tray application state.
type App struct {
	cfg      *config.Config
	mon      *monitor.Monitor
	triggers *triggers.Runner
	events   <-chan monitor.Event
	version  string
	mu       sync.Mutex

	// Menu items
	mReceiver    *systray.MenuItem
	mDongleFW    *systray.MenuItem
	mHeadset     *systray.MenuItem
	mHeadsetFW   *systray.MenuItem
	mBattery     *systray.MenuItem
	dongleFWDone  bool // true after dongle FW has been queried
	headsetFWDone bool // true after headset FW has been queried
	headsetShown  bool // true after headset controls have been shown
	mEqParent  *systray.MenuItem
	mEq        [3]*systray.MenuItem
	mLightTog  *systray.MenuItem
	lightOn    bool
	mMicMute   *systray.MenuItem
	mMNCTog       *systray.MenuItem
	mncOn         bool
	mSidetone        *systray.MenuItem
	mSidetoneLvl     [11]*systray.MenuItem // 0%, 10%, 20%... 100%
	sidetoneSetAt    time.Time            // when sidetone was last manually set
	// Cached UI state to avoid redundant systray calls
	lastBattery    int
	lastEqSlot     int
	lastLightOn    bool
	lastMNCOn      bool
	lastSidetone   int
	lastMicBoom    bool
	lastMicMuted   bool
	mDispIcon    *systray.MenuItem // macOS only: template icon (auto light/dark)
	mDispWhite   *systray.MenuItem // Linux only: white icon
	mDispText    *systray.MenuItem // macOS + Linux: text label
	mTriggers    *systray.MenuItem
	mAutostart   *systray.MenuItem
	mConfigDir   *systray.MenuItem
	mReload      *systray.MenuItem
	mVersion     *systray.MenuItem
	mUpdate      *systray.MenuItem
	updateURL    string // set when an update is found
	mQuit        *systray.MenuItem
}

// New creates the tray app.
func New(cfg *config.Config, mon *monitor.Monitor, tr *triggers.Runner, events <-chan monitor.Event, version string) *App {
	return &App{
		cfg:      cfg,
		mon:      mon,
		triggers: tr,
		events:   events,
		version:  version,
	}
}

// OnReady is called by systray when the tray icon is ready.
func (a *App) OnReady() {
	systray.SetTooltip("Scape Control")
	a.applyDisplay()

	// ── Receiver status ──
	a.mReceiver = systray.AddMenuItem("Checking device status...", "")
	a.mReceiver.Disable()
	a.mDongleFW = systray.AddMenuItem("", "")
	a.mDongleFW.Disable()
	a.mDongleFW.Hide()

	systray.AddSeparator()

	// ── Headset section (all hidden until headset connects) ──
	a.mHeadset = systray.AddMenuItem("Headset: Disconnected", "")
	a.mHeadset.Disable()
	a.mHeadsetFW = systray.AddMenuItem("", "")
	a.mHeadsetFW.Disable()
	a.mHeadsetFW.Hide()
	a.mMicMute = systray.AddMenuItem("", "")
	a.mMicMute.Disable()
	a.mMicMute.Hide()
	a.mBattery = systray.AddMenuItem("", "")
	a.mBattery.Disable()
	a.mBattery.Hide()
	a.mEqParent = systray.AddMenuItem("EQ Preset", "")
	a.mEq[0] = a.mEqParent.AddSubMenuItem("Slot 1", "")
	a.mEq[1] = a.mEqParent.AddSubMenuItem("Slot 2", "")
	a.mEq[2] = a.mEqParent.AddSubMenuItem("Slot 3", "")
	a.mEqParent.Hide()
	a.mLightTog = systray.AddMenuItem("RGB: Off", "")
	a.mLightTog.Hide()
	a.mMNCTog = systray.AddMenuItem("Mic Noise Cancellation: Off", "")
	a.mMNCTog.Hide()
	a.mSidetone = systray.AddMenuItem("Sidetone: 0%", "")
	for i := 0; i <= 10; i++ {
		a.mSidetoneLvl[i] = a.mSidetone.AddSubMenuItem(fmt.Sprintf("%d%%", i*10), "")
	}
	a.mSidetone.Hide()

	systray.AddSeparator()

	// ── Display (hidden on Windows — only one mode available) ──
	if runtime.GOOS != "windows" {
		mDisp := systray.AddMenuItem("Tray Display", "")
		if runtime.GOOS == "darwin" {
			a.mDispIcon = mDisp.AddSubMenuItem("Icon", "")
		} else {
			a.mDispWhite = mDisp.AddSubMenuItem("White Icon", "")
		}
		a.mDispText = mDisp.AddSubMenuItem("Text", "")
		a.updateDispCheck()
	}

	// ── Utility ──
	a.mTriggers = systray.AddMenuItem("Trigger Scripts", "")
	a.updateTriggersCheck()
	a.mAutostart = systray.AddMenuItem("Launch at Login", "")
	a.updateAutostartCheck()
	a.mConfigDir = systray.AddMenuItem("Open Config Folder", "")
	a.mReload = systray.AddMenuItem("Reload Config", "")

	systray.AddSeparator()
	a.mVersion = systray.AddMenuItem(fmt.Sprintf("ScapeCtl %s", a.version), "")
	a.mUpdate = systray.AddMenuItem("Check for Updates", "")
	a.mQuit = systray.AddMenuItem("Quit", "")

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
		case <-a.mSidetoneLvl[0].ClickedCh:
			a.setSidetone(0)
		case <-a.mSidetoneLvl[1].ClickedCh:
			a.setSidetone(10)
		case <-a.mSidetoneLvl[2].ClickedCh:
			a.setSidetone(20)
		case <-a.mSidetoneLvl[3].ClickedCh:
			a.setSidetone(30)
		case <-a.mSidetoneLvl[4].ClickedCh:
			a.setSidetone(40)
		case <-a.mSidetoneLvl[5].ClickedCh:
			a.setSidetone(50)
		case <-a.mSidetoneLvl[6].ClickedCh:
			a.setSidetone(60)
		case <-a.mSidetoneLvl[7].ClickedCh:
			a.setSidetone(70)
		case <-a.mSidetoneLvl[8].ClickedCh:
			a.setSidetone(80)
		case <-a.mSidetoneLvl[9].ClickedCh:
			a.setSidetone(90)
		case <-a.mSidetoneLvl[10].ClickedCh:
			a.setSidetone(100)
		case <-clickCh(a.mDispIcon):
			a.setDisplay("icon")
		case <-clickCh(a.mDispWhite):
			a.setDisplay("white")
		case <-clickCh(a.mDispText):
			a.setDisplay("text")
		case <-a.mTriggers.ClickedCh:
			a.toggleTriggers()
		case <-a.mAutostart.ClickedCh:
			a.toggleAutostart()
		case <-a.mVersion.ClickedCh:
			a.openURL("https://github.com/charlietran/scapectl")
		case <-a.mUpdate.ClickedCh:
			if a.updateURL != "" {
				a.openURL(a.updateURL)
			} else {
				go a.checkForUpdate()
			}
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
			a.mu.Lock()
			a.dongleFWDone = false
			a.headsetFWDone = false
			a.headsetShown = false
			a.mu.Unlock()
			a.mDongleFW.Hide()
			a.mHeadsetFW.Hide()
			a.resetCachedState()

		case monitor.EventDongleDisconnected:
			a.mReceiver.SetTitle("USB Receiver: Disconnected")
			a.mDongleFW.Hide()
			a.mHeadset.SetTitle("Headset: Disconnected")
			a.mHeadsetFW.Hide()
			a.hideHeadsetControls()
			a.mu.Lock()
			a.dongleFWDone = false
			a.headsetFWDone = false
			a.headsetShown = false
			a.mu.Unlock()
			a.resetCachedState()

		case monitor.EventHeadsetPowerOn:
			a.mHeadset.SetTitle("Headset: Connected")

		case monitor.EventHeadsetPowerOff:
			a.mHeadset.SetTitle("Headset: Disconnected")
			a.mHeadsetFW.Hide()
			a.hideHeadsetControls()
			a.mu.Lock()
			a.headsetFWDone = false
			a.headsetShown = false
			a.mu.Unlock()
			a.resetCachedState()

		case monitor.EventHeadsetStatus:
			s := evt.Status
			if s == nil {
				continue
			}
			a.queryFirmwareOnce()

			// Show headset controls once on first status
			a.mu.Lock()
			wasShown := a.headsetShown
			a.mu.Unlock()
			if !wasShown {
				a.mReceiver.SetTitle("USB Receiver: Connected")
				a.mHeadset.SetTitle("Headset: Connected")
				a.mHeadset.Show()
				a.mEqParent.Show()
				a.mLightTog.Show()
				a.mMNCTog.Show()
				a.mSidetone.Show()
				a.mu.Lock()
				a.headsetShown = true
				a.mu.Unlock()
			}

			// Only update menu items when values change
			if s.BoomMicConnected != a.lastMicBoom || s.Muted != a.lastMicMuted {
				a.lastMicBoom = s.BoomMicConnected
				a.lastMicMuted = s.Muted
				a.updateMicStatus(s.BoomMicConnected, s.Muted)
			}
			if s.BatteryPercent >= 0 && s.BatteryPercent != a.lastBattery {
				a.lastBattery = s.BatteryPercent
				a.mBattery.SetTitle(fmt.Sprintf("Battery: %d%%", s.BatteryPercent))
				a.mBattery.Show()
			}
			if s.EqSlot != a.lastEqSlot {
				a.lastEqSlot = s.EqSlot
				a.updateEqCheck(s.EqSlot)
			}
			lightOn := s.LightSlot > 0
			if lightOn != a.lastLightOn {
				a.lastLightOn = lightOn
				a.mu.Lock()
				a.lightOn = lightOn
				a.mu.Unlock()
				a.updateLightStatus(lightOn)
			}
			if s.MNCOn != a.lastMNCOn {
				a.lastMNCOn = s.MNCOn
				a.mu.Lock()
				a.mncOn = s.MNCOn
				a.mu.Unlock()
				a.updateMNCStatus(s.MNCOn)
			}
			// Don't overwrite sidetone UI from stale status data
			// for 10s after a manual change
			a.mu.Lock()
			recentSet := time.Since(a.sidetoneSetAt) < 10*time.Second
			a.mu.Unlock()
			if !recentSet && s.SidetoneVol != a.lastSidetone {
				a.lastSidetone = s.SidetoneVol
				a.updateSidetoneCheck(s.SidetoneVol)
			}
		}
	}
}

// sendCommand runs a function on the device while pausing the monitor's polling.
func (a *App) sendCommand(fn func(dev *hid.Device) error) error {
	return a.mon.RunCommand(fn)
}

// queryFirmwareOnce fetches dongle and headset firmware versions the first
// time after a connection is established. Runs in a goroutine to avoid
// blocking the event loop.
func (a *App) queryFirmwareOnce() {
	a.mu.Lock()
	needDongle := !a.dongleFWDone
	needHeadset := !a.headsetFWDone
	a.mu.Unlock()

	if !needDongle && !needHeadset {
		return
	}

	go func() {
		if needDongle {
			if err := a.sendCommand(func(dev *hid.Device) error {
				fw, err := dev.GetDongleFW()
				if err != nil {
					return err
				}
				a.mDongleFW.SetTitle(fmt.Sprintf("  Firmware: %s", fw))
				a.mDongleFW.Show()
				a.mu.Lock()
				a.dongleFWDone = true
				a.mu.Unlock()
				return nil
			}); err != nil {
				log.Printf("[tray] get dongle FW: %v", err)
			}
		}
		if needHeadset {
			if err := a.sendCommand(func(dev *hid.Device) error {
				fw, err := dev.GetHeadsetFW()
				if err != nil {
					return err
				}
				a.mHeadsetFW.SetTitle(fmt.Sprintf("  Firmware: %s", fw))
				a.mHeadsetFW.Show()
				a.mu.Lock()
				a.headsetFWDone = true
				a.mu.Unlock()
				return nil
			}); err != nil {
				log.Printf("[tray] get headset FW: %v", err)
			}
		}
	}()
}

func (a *App) setEq(slot int) {
	if err := a.sendCommand(func(dev *hid.Device) error {
		return dev.SetActiveEq(slot)
	}); err != nil {
		log.Printf("[tray] set EQ slot %d error: %v", slot, err)
	} else {
		log.Printf("[tray] switched to EQ slot %d", slot)
		a.lastEqSlot = slot
		a.updateEqCheck(slot)
	}
}

func (a *App) updateEqCheck(slot int) {
	a.mEqParent.SetTitle(fmt.Sprintf("EQ Preset: %d", slot))
	for i := 0; i < 3; i++ {
		if i+1 == slot {
			a.mEq[i].SetTitle(fmt.Sprintf("● Slot %d", i+1))
		} else {
			a.mEq[i].SetTitle(fmt.Sprintf("  Slot %d", i+1))
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
		a.lastLightOn = on
		a.updateLightStatus(on)
		log.Printf("[tray] RGB %v", on)
	}
}

func (a *App) updateLightStatus(on bool) {
	if on {
		a.mLightTog.SetTitle("RGB: On")
	} else {
		a.mLightTog.SetTitle("RGB: Off")
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
		a.lastMNCOn = on
		a.updateMNCStatus(on)
	}
}

func (a *App) hideHeadsetControls() {
	a.mMicMute.Hide()
	a.mBattery.Hide()
	a.mEqParent.Hide()
	a.mLightTog.Hide()
	a.mMNCTog.Hide()
	a.mSidetone.Hide()
}

func (a *App) resetCachedState() {
	a.lastBattery = -1
	a.lastEqSlot = 0
	a.lastLightOn = false
	a.lastMNCOn = false
	a.lastSidetone = -1
	a.lastMicBoom = false
	a.lastMicMuted = false
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
	} else {
		a.mMNCTog.SetTitle("Mic Noise Cancellation: Off")
	}
}

func (a *App) setSidetone(pct int) {
	if err := a.sendCommand(func(dev *hid.Device) error {
		// Max sidetone is 75 steps. Reset to 0 in two chunks with
		// a status poll between each command (required for the dongle
		// to relay to the headset).
		steps := []struct {
			action byte
			value  byte
		}{
			{hid.SidetoneVolDown, 50},
			{hid.SidetoneVolDown, 25},
		}
		if pct > 0 {
			// Then go up to target
			if pct > 50 {
				steps = append(steps, struct{ action, value byte }{hid.SidetoneVolUp, 50})
				steps = append(steps, struct{ action, value byte }{hid.SidetoneVolUp, byte(pct - 50)})
			} else {
				steps = append(steps, struct{ action, value byte }{hid.SidetoneVolUp, byte(pct)})
			}
		}
		for _, s := range steps {
			rid, payload := hid.BuildSidetoneCmd(s.action, s.value)
			if err := dev.Send(rid, payload); err != nil {
				return err
			}
			// Poll between commands — dongle requires this to relay to headset
			dev.GetStatus()
		}
		return nil
	}); err != nil {
		log.Printf("[tray] set sidetone error: %v", err)
	} else {
		a.mu.Lock()
		a.sidetoneSetAt = time.Now()
		a.mu.Unlock()
		a.lastSidetone = pct
		a.updateSidetoneCheck(pct)
		log.Printf("[tray] sidetone %d%%", pct)
	}
}

func (a *App) updateSidetoneCheck(pct int) {
	a.mSidetone.SetTitle(fmt.Sprintf("Sidetone: %d%%", pct))
	for i := 0; i <= 10; i++ {
		if i*10 == pct {
			a.mSidetoneLvl[i].SetTitle(fmt.Sprintf("● %d%%", i*10))
		} else {
			a.mSidetoneLvl[i].SetTitle(fmt.Sprintf("  %d%%", i*10))
		}
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
	mode := effectiveMode(a.cfg.Settings.TrayDisplay)
	text := a.cfg.Settings.TrayText
	a.mu.Unlock()

	if text == "" {
		text = "Scape"
	}
	if len(text) > 16 {
		text = text[:16]
	}

	switch mode {
	case "icon":
		switch runtime.GOOS {
		case "darwin":
			systray.SetTemplateIcon(iconBlackPNG, iconBlackPNG)
		case "windows":
			systray.SetIcon(iconWhiteICO)
		default:
			systray.SetIcon(iconWhitePNG)
		}
		systray.SetTitle("")
	case "white":
		if runtime.GOOS == "windows" {
			systray.SetIcon(iconWhiteICO)
		} else {
			systray.SetIcon(iconWhitePNG)
		}
		systray.SetTitle("")
	default: // "text"
		systray.SetTitle(text)
	}
}

func (a *App) updateDispCheck() {
	a.mu.Lock()
	mode := effectiveMode(a.cfg.Settings.TrayDisplay)
	a.mu.Unlock()

	if a.mDispIcon != nil {
		if mode == "icon" {
			a.mDispIcon.SetTitle("● Icon")
		} else {
			a.mDispIcon.SetTitle("  Icon")
		}
	}
	if a.mDispWhite != nil {
		if mode == "white" {
			a.mDispWhite.SetTitle("● White Icon")
		} else {
			a.mDispWhite.SetTitle("  White Icon")
		}
	}
	if a.mDispText != nil {
		if mode == "text" {
			a.mDispText.SetTitle("● Text")
		} else {
			a.mDispText.SetTitle("  Text")
		}
	}
}

func (a *App) setDisplay(mode string) {
	a.mu.Lock()
	oldMode := a.cfg.Settings.TrayDisplay
	a.cfg.Settings.TrayDisplay = mode
	a.mu.Unlock()

	if err := config.SetValue("tray_display", mode); err != nil {
		log.Printf("[tray] failed to save display setting: %v", err)
		return
	}

	// Switching to/from text mode requires a restart since systray
	// can't remove an icon once set (and can't add one after startup).
	oldEff := effectiveMode(oldMode)
	newEff := effectiveMode(mode)
	if (oldEff == "text") != (newEff == "text") {
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
	if err := cmd.Start(); err != nil {
		log.Printf("[tray] failed to restart: %v", err)
		return
	}
	systray.Quit()
}

func (a *App) updateTriggersCheck() {
	a.mu.Lock()
	on := a.cfg.Settings.TriggersEnabled
	a.mu.Unlock()
	if on {
		a.mTriggers.Check()
	} else {
		a.mTriggers.Uncheck()
	}
}

func (a *App) toggleTriggers() {
	a.mu.Lock()
	on := !a.cfg.Settings.TriggersEnabled
	a.cfg.Settings.TriggersEnabled = on
	a.mu.Unlock()

	if err := config.SetRawValue("triggers_enabled", fmt.Sprintf("%t", on)); err != nil {
		log.Printf("[tray] failed to save triggers setting: %v", err)
		return
	}
	a.updateTriggersCheck()
	log.Printf("[tray] triggers %v", on)
}

func (a *App) updateAutostartCheck() {
	if autostart.Enabled() {
		a.mAutostart.Check()
	} else {
		a.mAutostart.Uncheck()
	}
}

func (a *App) toggleAutostart() {
	if autostart.Enabled() {
		if err := autostart.Disable(); err != nil {
			log.Printf("[tray] disable autostart: %v", err)
			return
		}
		log.Println("[tray] autostart disabled")
	} else {
		if err := autostart.Enable(); err != nil {
			log.Printf("[tray] enable autostart: %v", err)
			return
		}
		log.Println("[tray] autostart enabled")
	}
	a.updateAutostartCheck()
}

func (a *App) openConfigDir() {
	dir := config.Dir()
	config.EnsureExists()
	a.openURL(dir)
}

func (a *App) openURL(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		log.Printf("[tray] failed to open %s: %v", url, err)
	}
}

// checkForUpdate queries the GitHub API for the latest release and updates
// the version menu item if a newer version is available.
func (a *App) checkForUpdate() {
	a.mUpdate.SetTitle("Checking...")
	a.mUpdate.Disable()
	defer a.mUpdate.Enable()

	if a.version == "dev" || a.version == "" {
		a.mUpdate.SetTitle("Check for Updates (dev build)")
		return
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get("https://api.github.com/repos/charlietran/scapectl/releases/latest")
	if err != nil {
		log.Printf("[tray] update check failed: %v", err)
		a.mUpdate.SetTitle("Check for Updates (failed)")
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		a.mUpdate.SetTitle("Check for Updates (failed)")
		return
	}

	var release struct {
		TagName string `json:"tag_name"`
		HTMLURL string `json:"html_url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		a.mUpdate.SetTitle("Check for Updates (failed)")
		return
	}

	latest := strings.TrimPrefix(release.TagName, "v")
	current := strings.TrimPrefix(a.version, "v")

	if latest != current && isNewer(latest, current) {
		a.mVersion.SetTitle(fmt.Sprintf("ScapeCtl %s (update: v%s)", a.version, latest))
		a.mUpdate.SetTitle("Download Update")
		a.updateURL = release.HTMLURL
		log.Printf("[tray] update available: v%s → v%s", current, latest)
		a.openURL(release.HTMLURL)
	} else {
		a.mUpdate.SetTitle("Check for Updates (up to date)")
	}
}

// isNewer returns true if version a is newer than version b (simple semver comparison).
func isNewer(a, b string) bool {
	partsA := strings.SplitN(a, ".", 3)
	partsB := strings.SplitN(b, ".", 3)
	for i := 0; i < 3; i++ {
		var va, vb int
		if i < len(partsA) {
			fmt.Sscanf(partsA[i], "%d", &va)
		}
		if i < len(partsB) {
			fmt.Sscanf(partsB[i], "%d", &vb)
		}
		if va > vb {
			return true
		}
		if va < vb {
			return false
		}
	}
	return false
}
