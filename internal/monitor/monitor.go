// Package monitor polls the USB bus for Fractal device connect/disconnect events
// and maintains a persistent HID connection for status polling.
package monitor

import (
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/charlietran/scape-ctl/internal/hid"
)

// EventType identifies what happened.
type EventType int

const (
	EventDongleConnected    EventType = iota // USB dongle plugged in
	EventDongleDisconnected                  // USB dongle unplugged
	EventHeadsetPowerOn                      // Headset powered on (detected via dongle)
	EventHeadsetPowerOff                     // Headset powered off or out of range
	EventHeadsetStatus                       // Periodic headset status update
	EventBatteryLevel                        // Battery level update (for threshold triggers)
	EventMicMuted                            // Mic muted (boom flipped up)
	EventMicUnmuted                          // Mic unmuted (boom flipped down)
	EventEqChanged                           // EQ preset slot changed
	EventRgbOn                               // RGB lighting turned on
	EventRgbOff                              // RGB lighting turned off
	EventMncOn                               // Mic Noise Cancellation enabled
	EventMncOff                              // Mic Noise Cancellation disabled
)

func (e EventType) String() string {
	switch e {
	case EventDongleConnected:
		return "DongleConnected"
	case EventDongleDisconnected:
		return "DongleDisconnected"
	case EventHeadsetPowerOn:
		return "HeadsetPowerOn"
	case EventHeadsetPowerOff:
		return "HeadsetPowerOff"
	case EventHeadsetStatus:
		return "HeadsetStatus"
	case EventBatteryLevel:
		return "BatteryLevel"
	case EventMicMuted:
		return "MicMuted"
	case EventMicUnmuted:
		return "MicUnmuted"
	case EventEqChanged:
		return "EqChanged"
	case EventRgbOn:
		return "RgbOn"
	case EventRgbOff:
		return "RgbOff"
	case EventMncOn:
		return "MncOn"
	case EventMncOff:
		return "MncOff"
	default:
		return "Unknown"
	}
}

// Event is emitted when a device state changes.
type Event struct {
	Type      EventType
	Device    hid.DeviceInfo
	Status    *hid.DeviceStatus // non-nil for HeadsetStatus events
	Timestamp time.Time
}

// Monitor watches for Fractal HID devices and polls headset status.
type Monitor struct {
	interval       time.Duration
	devMu          sync.Mutex // held during HID I/O to prevent command interleaving
	stop           chan struct{}
	mu             sync.Mutex
	known          map[string]hid.DeviceInfo // path → info
	subs           []chan Event              // fan-out subscriber channels
	running        bool
	dev            *hid.Device // persistent HID connection
	headsetOnline  bool        // last known headset power state
	headsetChecked bool        // true after first status poll
	lastMuted      bool        // last known mic mute state
	lastEqSlot     int         // last known EQ slot
	lastRgbOn      bool        // last known RGB state
	lastMncOn      bool        // last known MNC state
}

// New creates a monitor that polls at the given interval.
func New(interval time.Duration) *Monitor {
	return &Monitor{
		interval: interval,
		stop:     make(chan struct{}),
		known:    make(map[string]hid.DeviceInfo),
	}
}

// Subscribe returns a channel that receives all future events.
func (m *Monitor) Subscribe() <-chan Event {
	m.mu.Lock()
	defer m.mu.Unlock()
	ch := make(chan Event, 32)
	m.subs = append(m.subs, ch)
	return ch
}

// Start begins the polling loop in a goroutine.
func (m *Monitor) Start() {
	m.mu.Lock()
	if m.running {
		m.mu.Unlock()
		return
	}
	m.running = true
	m.mu.Unlock()

	// Seed with currently-connected devices and emit events
	if devices, err := hid.Enumerate(); err == nil {
		m.mu.Lock()
		for _, d := range devices {
			m.known[d.Path] = d
			m.emit(Event{
				Type:      EventDongleConnected,
				Device:    d,
				Timestamp: time.Now(),
			})
		}
		m.mu.Unlock()
	}

	go m.poll()
	log.Printf("[monitor] started (interval: %v)", m.interval)
}

// Stop halts the polling loop and closes all subscriber channels.
func (m *Monitor) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.running {
		return
	}
	close(m.stop)
	m.running = false
	if m.dev != nil {
		m.dev.Close()
		m.dev = nil
	}
	for _, ch := range m.subs {
		close(ch)
	}
	m.subs = nil
	log.Println("[monitor] stopped")
}

// Device returns the persistent HID connection, or nil if not connected.
func (m *Monitor) Device() *hid.Device {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.dev
}

// RunCommand executes a function on the device while pausing the poll loop.
// This prevents command interleaving with status polls.
func (m *Monitor) RunCommand(fn func(dev *hid.Device) error) error {
	m.devMu.Lock()
	defer m.devMu.Unlock()
	m.mu.Lock()
	dev := m.dev
	m.mu.Unlock()
	if dev == nil {
		return fmt.Errorf("no device connected")
	}
	return fn(dev)
}

// KnownDevices returns paths of currently tracked devices.
func (m *Monitor) KnownDevices() []hid.DeviceInfo {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]hid.DeviceInfo, 0, len(m.known))
	for _, d := range m.known {
		out = append(out, d)
	}
	return out
}

// HasDevices returns true if any Fractal device is connected.
func (m *Monitor) HasDevices() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.known) > 0
}

func (m *Monitor) poll() {
	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()

	for {
		select {
		case <-m.stop:
			return
		case <-ticker.C:
			m.tick()
		}
	}
}

func (m *Monitor) tick() {
	m.scanBus()

	m.mu.Lock()
	hasDongle := len(m.known) > 0
	m.mu.Unlock()

	if !hasDongle {
		return
	}

	m.ensureConnected()

	m.mu.Lock()
	dev := m.dev
	m.mu.Unlock()

	if dev == nil {
		return
	}

	m.devMu.Lock()
	m.pollHeadsetStatus(dev)

	// pollHeadsetStatus may have closed dev on error — recheck
	m.mu.Lock()
	stillOpen := m.dev == dev
	m.mu.Unlock()
	if stillOpen {
		dev.SendKeepalive()
	}
	m.devMu.Unlock()
}

func (m *Monitor) scanBus() {
	devices, err := hid.Enumerate()
	if err != nil {
		log.Printf("[monitor] enumeration error: %v", err)
		return
	}

	currentPaths := make(map[string]hid.DeviceInfo, len(devices))
	for _, d := range devices {
		currentPaths[d.Path] = d
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	for path, dev := range currentPaths {
		if _, existed := m.known[path]; !existed {
			log.Printf("[monitor] dongle connected: %s", dev)
			m.known[path] = dev
			m.headsetChecked = false
			m.emit(Event{
				Type:      EventDongleConnected,
				Device:    dev,
				Timestamp: time.Now(),
			})
		}
	}

	for path, dev := range m.known {
		if _, exists := currentPaths[path]; !exists {
			log.Printf("[monitor] dongle disconnected: %s", dev)
			delete(m.known, path)
			if m.dev != nil {
				m.dev.Close()
				m.dev = nil
			}
			if m.headsetOnline {
				m.headsetOnline = false
				m.emit(Event{
					Type:      EventHeadsetPowerOff,
					Device:    dev,
					Timestamp: time.Now(),
				})
			}
			m.headsetChecked = false
			m.emit(Event{
				Type:      EventDongleDisconnected,
				Device:    dev,
				Timestamp: time.Now(),
			})
		}
	}
}

func (m *Monitor) ensureConnected() {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.dev != nil {
		return
	}

	var devInfo hid.DeviceInfo
	for _, d := range m.known {
		devInfo = d
		break
	}

	dev, err := hid.OpenPath(devInfo.Path)
	if err != nil {
		return
	}
	m.dev = dev
}

func (m *Monitor) pollHeadsetStatus(dev *hid.Device) {
	status, err := dev.GetStatus()
	if err != nil {
		m.mu.Lock()
		if m.dev == dev {
			m.dev.Close()
			m.dev = nil
		}
		m.mu.Unlock()
		return
	}
	if status == nil {
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	var devInfo hid.DeviceInfo
	for _, d := range m.known {
		devInfo = d
		break
	}

	// Emit power state change (including first poll)
	if !m.headsetChecked || status.Connected != m.headsetOnline {
		m.headsetChecked = true
		m.headsetOnline = status.Connected
		evtType := EventHeadsetPowerOn
		if !status.Connected {
			evtType = EventHeadsetPowerOff
		}
		m.emit(Event{
			Type:      evtType,
			Device:    devInfo,
			Timestamp: time.Now(),
		})
	}

	// Emit periodic status for UI updates (battery, EQ, etc.)
	if status.Connected {
		m.emit(Event{
			Type:      EventHeadsetStatus,
			Device:    devInfo,
			Status:    status,
			Timestamp: time.Now(),
		})
		if status.BatteryPercent >= 0 {
			m.emit(Event{
				Type:      EventBatteryLevel,
				Device:    devInfo,
				Status:    status,
				Timestamp: time.Now(),
			})
		}

		// Emit state change events for mic, EQ, RGB
		if status.Muted != m.lastMuted {
			m.lastMuted = status.Muted
			evtType := EventMicUnmuted
			if status.Muted {
				evtType = EventMicMuted
			}
			m.emit(Event{
				Type:      evtType,
				Device:    devInfo,
				Status:    status,
				Timestamp: time.Now(),
			})
		}
		if status.EqSlot != m.lastEqSlot && m.lastEqSlot != 0 {
			m.emit(Event{
				Type:      EventEqChanged,
				Device:    devInfo,
				Status:    status,
				Timestamp: time.Now(),
			})
		}
		m.lastEqSlot = status.EqSlot

		rgbOn := status.LightSlot > 0
		if rgbOn != m.lastRgbOn && m.headsetChecked {
			evtType := EventRgbOff
			if rgbOn {
				evtType = EventRgbOn
			}
			m.emit(Event{
				Type:      evtType,
				Device:    devInfo,
				Status:    status,
				Timestamp: time.Now(),
			})
		}
		m.lastRgbOn = rgbOn

		if status.MNCOn != m.lastMncOn && m.headsetChecked {
			evtType := EventMncOff
			if status.MNCOn {
				evtType = EventMncOn
			}
			m.emit(Event{
				Type:      evtType,
				Device:    devInfo,
				Status:    status,
				Timestamp: time.Now(),
			})
		}
		m.lastMncOn = status.MNCOn
	}
}

func (m *Monitor) emit(evt Event) {
	for _, ch := range m.subs {
		select {
		case ch <- evt:
		default:
			log.Println("[monitor] subscriber channel full, dropping event")
		}
	}
}
