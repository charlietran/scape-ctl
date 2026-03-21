// Package monitor polls the USB bus for Fractal device connect/disconnect events
// and polls headset status through the dongle.
package monitor

import (
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
	stop           chan struct{}
	mu             sync.Mutex
	known          map[string]hid.DeviceInfo // path → info
	subs           []chan Event              // fan-out subscriber channels
	running        bool
	headsetOnline  bool // last known headset power state
	headsetChecked bool // true after first status poll
	statusTicks    int  // counter for status polling interval
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
// Call before Start() to avoid missing events. The channel is closed
// when the monitor stops.
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
	for _, ch := range m.subs {
		close(ch)
	}
	m.subs = nil
	log.Println("[monitor] stopped")
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
			m.scan()
		}
	}
}

func (m *Monitor) scan() {
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

	// Detect new dongle connections
	for path, dev := range currentPaths {
		if _, existed := m.known[path]; !existed {
			log.Printf("[monitor] dongle connected: %s", dev)
			m.known[path] = dev
			m.headsetChecked = false
			m.statusTicks = 0
			m.emit(Event{
				Type:      EventDongleConnected,
				Device:    dev,
				Timestamp: time.Now(),
			})
		}
	}

	// Detect dongle disconnections
	for path, dev := range m.known {
		if _, exists := currentPaths[path]; !exists {
			log.Printf("[monitor] dongle disconnected: %s", dev)
			delete(m.known, path)
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

	// Poll headset status every 5 ticks (~5s at default 1s interval)
	hasDongle := len(m.known) > 0
	m.mu.Unlock()

	if hasDongle {
		m.statusTicks++
		if m.statusTicks >= 5 {
			m.statusTicks = 0
			m.pollHeadsetStatus()
		}
	}
}

// pollHeadsetStatus opens the dongle, queries headset state, and emits events.
func (m *Monitor) pollHeadsetStatus() {
	m.mu.Lock()
	var devInfo hid.DeviceInfo
	for _, d := range m.known {
		devInfo = d
		break
	}
	m.mu.Unlock()

	dev, err := hid.OpenPath(devInfo.Path)
	if err != nil {
		return // device busy or permission denied — skip this tick
	}
	status, err := dev.GetStatus()
	dev.Close()

	if err != nil || status == nil {
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

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
	}
}

func (m *Monitor) emit(evt Event) {
	// m.mu is already held
	for _, ch := range m.subs {
		select {
		case ch <- evt:
		default:
			log.Println("[monitor] subscriber channel full, dropping event")
		}
	}
}
