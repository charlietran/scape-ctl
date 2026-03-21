// Package monitor polls the USB bus for Fractal device connect/disconnect events
// and maintains a persistent HID connection for status polling.
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
	dev            *hid.Device // persistent HID connection
	headsetOnline  bool        // last known headset power state
	headsetChecked bool        // true after first status poll
	pollCount      int         // tick counter for staggered operations
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
// The caller must not close the device.
func (m *Monitor) Device() *hid.Device {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.dev
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
	// USB bus scan for dongle connect/disconnect (every tick)
	m.scanBus()

	m.mu.Lock()
	hasDongle := len(m.known) > 0
	m.mu.Unlock()

	if !hasDongle {
		return
	}

	// Ensure persistent connection is open
	m.ensureConnected()

	m.mu.Lock()
	dev := m.dev
	m.mu.Unlock()

	if dev == nil {
		return
	}

	// Quick dongle poll (11 21) for fast headset presence detection,
	// then full status (f1 21) only if headset is present, then keepalive.
	m.pollHeadset(dev)
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

	// Detect new dongle connections
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

	// Detect dongle disconnections
	for path, dev := range m.known {
		if _, exists := currentPaths[path]; !exists {
			log.Printf("[monitor] dongle disconnected: %s", dev)
			delete(m.known, path)
			// Close persistent connection
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
		return // permission denied or busy — retry next tick
	}
	m.dev = dev
}

// pollHeadset does a fast dongle poll (11 21) for presence detection,
// then a full status poll (f1 21) only if the headset is present.
func (m *Monitor) pollHeadset(dev *hid.Device) {
	// Fast presence check via dongle (responds instantly)
	rid, payload := hid.BuildDonglePoll()
	resp, err := dev.SendAndReceive(rid, payload, 100*time.Millisecond)
	if err != nil {
		m.mu.Lock()
		if m.dev == dev {
			m.dev.Close()
			m.dev = nil
		}
		m.mu.Unlock()
		return
	}

	// 11 21 response: byte 3 = headset present (0/1)
	online := len(resp) > 3 && resp[3] != 0

	m.mu.Lock()
	var devInfo hid.DeviceInfo
	for _, d := range m.known {
		devInfo = d
		break
	}

	// Emit power state change (including first poll)
	if !m.headsetChecked || online != m.headsetOnline {
		m.headsetChecked = true
		m.headsetOnline = online
		evtType := EventHeadsetPowerOn
		if !online {
			evtType = EventHeadsetPowerOff
		}
		m.emit(Event{
			Type:      evtType,
			Device:    devInfo,
			Timestamp: time.Now(),
		})
	}
	m.mu.Unlock()

	// Full status poll + keepalive only if headset is online
	if online {
		status, err := dev.GetStatus()
		if err == nil && status != nil && status.Connected {
			m.mu.Lock()
			m.emit(Event{
				Type:      EventHeadsetStatus,
				Device:    devInfo,
				Status:    status,
				Timestamp: time.Now(),
			})
			m.mu.Unlock()
		}
	}

	dev.SendKeepalive()
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
