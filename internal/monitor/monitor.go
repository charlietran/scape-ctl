// Package monitor polls the USB bus for Fractal device connect/disconnect events.
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
	EventConnected    EventType = iota
	EventDisconnected
)

func (e EventType) String() string {
	switch e {
	case EventConnected:
		return "Connected"
	case EventDisconnected:
		return "Disconnected"
	default:
		return "Unknown"
	}
}

// Event is emitted when a device connects or disconnects.
type Event struct {
	Type      EventType
	Device    hid.DeviceInfo
	Timestamp time.Time
}

// Monitor watches for Fractal HID devices appearing/disappearing.
type Monitor struct {
	interval time.Duration
	stop     chan struct{}
	mu       sync.Mutex
	known    map[string]hid.DeviceInfo // path → info
	subs     []chan Event              // fan-out subscriber channels
	running  bool
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

	// Seed with currently-connected devices
	if devices, err := hid.Enumerate(); err == nil {
		m.mu.Lock()
		for _, d := range devices {
			m.known[d.Path] = d
			log.Printf("[monitor] existing device: %s", d)
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
	defer m.mu.Unlock()

	// Detect new connections
	for path, dev := range currentPaths {
		if _, existed := m.known[path]; !existed {
			log.Printf("[monitor] connected: %s", dev)
			m.known[path] = dev
			m.emit(Event{
				Type:      EventConnected,
				Device:    dev,
				Timestamp: time.Now(),
			})
		}
	}

	// Detect disconnections
	for path, dev := range m.known {
		if _, exists := currentPaths[path]; !exists {
			log.Printf("[monitor] disconnected: %s", dev)
			delete(m.known, path)
			m.emit(Event{
				Type:      EventDisconnected,
				Device:    dev,
				Timestamp: time.Now(),
			})
		}
	}
}

func (m *Monitor) emit(evt Event) {
	// m.mu is already held by scan()
	for _, ch := range m.subs {
		select {
		case ch <- evt:
		default:
			log.Println("[monitor] subscriber channel full, dropping event")
		}
	}
}
