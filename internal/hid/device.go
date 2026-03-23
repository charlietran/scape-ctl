package hid

import (
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/charlietran/scape-ctl/internal/usbhid"
)

// Verbose controls whether debug-level log messages are printed.
var Verbose bool

// UsagePageVendor is the HID usage page for the vendor-specific control
// collection (collection 3). This is the only collection we can send
// commands to — the others are consumer control, telephony, etc.
const UsagePageVendor = 0xFF00

// DeviceInfo describes a discovered Fractal device (not yet opened).
type DeviceInfo struct {
	VendorID    uint16
	ProductID   uint16
	ProductName string
	Serial      string
	Path        string
	Interface   int
	UsagePage   uint16
}

func (d DeviceInfo) String() string {
	return fmt.Sprintf("%s [%04x:%04x] @ %s", d.ProductName, d.VendorID, d.ProductID, d.Path)
}

func (d DeviceInfo) ShortString() string {
	return fmt.Sprintf("%s [%04x:%04x]", d.ProductName, d.VendorID, d.ProductID)
}

// inputReport is a report received from the device's persistent reader goroutine.
type inputReport struct {
	data []byte
	err  error
}

// Device is an open connection to a Fractal Scape HID device.
type Device struct {
	dev     *usbhid.Device
	Info    DeviceInfo
	reports chan inputReport // persistent reader goroutine feeds this
}

// Enumerate finds Fractal Design HID devices, returning only the
// vendor-specific control interface (usagePage 0xFF00) for each device.
func Enumerate() ([]DeviceInfo, error) {
	devices, err := usbhid.Enumerate(func(d *usbhid.Device) bool {
		return d.VendorId() == FractalVendorID && d.UsagePage() == UsagePageVendor
	})
	if err != nil {
		return nil, fmt.Errorf("hid enumerate: %w", err)
	}

	var result []DeviceInfo
	for _, d := range devices {
		product := d.Product()
		if product == "" {
			product = "(unknown)"
		}
		result = append(result, DeviceInfo{
			VendorID:    d.VendorId(),
			ProductID:   d.ProductId(),
			ProductName: product,
			Serial:      d.SerialNumber(),
			Path:        d.Path(),
			UsagePage:   d.UsagePage(),
		})
	}
	return result, nil
}

// OpenPath opens a device by its OS device path.
func OpenPath(path string) (*Device, error) {
	// Find the device by path
	devices, err := usbhid.Enumerate(func(d *usbhid.Device) bool {
		return d.Path() == path
	})
	if err != nil {
		return nil, fmt.Errorf("hid enumerate for open: %w", err)
	}
	if len(devices) == 0 {
		return nil, fmt.Errorf("hid device not found: %s", path)
	}

	dev := devices[0]
	// Open non-exclusive so OS still receives volume/media key reports
	if err := dev.Open(false); err != nil {
		return nil, fmt.Errorf("hid open %s: %w", path, err)
	}

	info := DeviceInfo{
		VendorID:    dev.VendorId(),
		ProductID:   dev.ProductId(),
		ProductName: dev.Product(),
		Serial:      dev.SerialNumber(),
		Path:        dev.Path(),
		UsagePage:   dev.UsagePage(),
	}
	if info.ProductName == "" {
		info.ProductName = "(unknown)"
	}

	if Verbose {
		log.Printf("[hid] opened device: %s", info)
	}

	d := &Device{
		dev:     dev,
		Info:    info,
		reports: make(chan inputReport, 16),
	}
	go d.readLoop()
	return d, nil
}

// OpenFirst opens the first Fractal device found.
func OpenFirst() (*Device, error) {
	devices, err := Enumerate()
	if err != nil {
		return nil, err
	}
	if len(devices) == 0 {
		return nil, fmt.Errorf("no Fractal Design devices found (VID %04x)", FractalVendorID)
	}
	return OpenPath(devices[0].Path)
}

// Close releases the device.
func (d *Device) Close() {
	if d.dev != nil {
		d.dev.Close()
		d.dev = nil
	}
}

// ── Low-level I/O ───────────────────────────────────

// readLoop runs in a goroutine for the lifetime of the device connection.
// It reads input reports and sends them to the reports channel.
// Exits when the device is closed (GetInputReport returns an error).
func (d *Device) readLoop() {
	for {
		_, data, err := d.dev.GetInputReport()
		d.reports <- inputReport{data, err}
		if err != nil {
			return
		}
	}
}

// Send transmits an output report to the device.
func (d *Device) Send(reportID byte, payload []byte) error {
	return d.dev.SetOutputReport(reportID, payload)
}

// Read reads an input report from the device with a timeout.
// Returns nil if no data available within the timeout.
func (d *Device) Read(timeout time.Duration) ([]byte, error) {
	select {
	case r := <-d.reports:
		if r.err != nil {
			return nil, r.err
		}
		return r.data, nil
	case <-time.After(timeout):
		return nil, nil
	}
}

// SendAndReceive sends a command and waits for a matching response.
// Responses are matched by the first 2 bytes (command echo). Unrelated
// input reports (e.g. unsolicited dongle reports) are discarded.
func (d *Device) SendAndReceive(reportID byte, payload []byte, timeout time.Duration) ([]byte, error) {
	if err := d.Send(reportID, payload); err != nil {
		return nil, fmt.Errorf("send: %w", err)
	}

	deadline := time.After(timeout)
	for {
		select {
		case r := <-d.reports:
			if r.err != nil {
				return nil, fmt.Errorf("read: %w", r.err)
			}
			// Match by echo bytes
			if len(r.data) >= 2 && len(payload) >= 2 && r.data[0] == payload[0] && r.data[1] == payload[1] {
				return r.data, nil
			}
			// Not our response — discard and keep waiting
		case <-deadline:
			return nil, errors.New("timeout")
		}
	}
}

// ── High-level operations ───────────────────────────

const defaultTimeout = 500 * time.Millisecond

// GetStatus sends f1 21 and parses the status blob (battery, connection, etc.).
// A timeout means the headset is unreachable (off/out of range) — returned as
// a disconnected status, not an error.
func (d *Device) GetStatus() (*DeviceStatus, error) {
	rid, payload := BuildGetStatus()
	resp, err := d.SendAndReceive(rid, payload, defaultTimeout)
	if err != nil {
		if err.Error() == "timeout" {
			return &DeviceStatus{Connected: false, BatteryPercent: -1}, nil
		}
		return nil, err
	}
	if resp == nil {
		return &DeviceStatus{Connected: false, BatteryPercent: -1}, nil
	}
	return ParseStatus(resp), nil
}

// GetDongleFW queries the dongle firmware version (11 01).
func (d *Device) GetDongleFW() (string, error) {
	rid, payload := BuildGetDongleFW()
	resp, err := d.SendAndReceive(rid, payload, defaultTimeout)
	if err != nil {
		return "", err
	}
	return ParseFWVersion(resp), nil
}

// GetDongleSerial queries the dongle serial number (11 02).
func (d *Device) GetDongleSerial() (string, error) {
	rid, payload := BuildGetDongleSerial()
	resp, err := d.SendAndReceive(rid, payload, defaultTimeout)
	if err != nil {
		return "", err
	}
	return ParseSerial(resp), nil
}

// GetHeadsetFW queries the headset firmware version (f1 01).
func (d *Device) GetHeadsetFW() (string, error) {
	rid, payload := BuildGetHeadsetFW()
	resp, err := d.SendAndReceive(rid, payload, defaultTimeout)
	if err != nil {
		return "", err
	}
	return ParseFWVersion(resp), nil
}

// GetHeadsetSerial queries the headset serial number (f1 02).
func (d *Device) GetHeadsetSerial() (string, error) {
	rid, payload := BuildGetHeadsetSerial()
	resp, err := d.SendAndReceive(rid, payload, defaultTimeout)
	if err != nil {
		return "", err
	}
	return ParseSerial(resp), nil
}

// IsHeadsetPresent checks if the headset is reachable through the dongle (f1 05).
func (d *Device) IsHeadsetPresent() (bool, error) {
	rid, payload := BuildGetHeadsetPresence()
	resp, err := d.SendAndReceive(rid, payload, defaultTimeout)
	if err != nil {
		return false, err
	}
	return ParsePresence(resp), nil
}

// SendKeepalive sends the a4 0e 99 heartbeat.
func (d *Device) SendKeepalive() error {
	rid, payload := BuildKeepalive()
	return d.Send(rid, payload)
}

// SetActiveEq switches which of the 3 EQ slots is active.
func (d *Device) SetActiveEq(slot int) error {
	rid, payload := BuildSetActiveEq(slot)
	return d.Send(rid, payload)
}

// RawSend sends arbitrary bytes (for protocol discovery).
func (d *Device) RawSend(reportID byte, data []byte) error {
	payload := make([]byte, ReportSize)
	copy(payload, data)
	return d.Send(reportID, payload)
}

// RawRead reads raw bytes with a timeout (for protocol discovery).
func (d *Device) RawRead(timeout time.Duration) ([]byte, error) {
	return d.Read(timeout)
}

// ── Diagnostics ─────────────────────────────────────

// DumpDevices returns a human-readable list of all Fractal HID devices.
func DumpDevices() string {
	devices, err := Enumerate()
	if err != nil {
		return fmt.Sprintf("enumeration error: %v", err)
	}
	if len(devices) == 0 {
		return "No Fractal Design devices found.\n" +
			"Ensure the dongle or cable is connected.\n" +
			"On Linux, add a udev rule:\n" +
			"  SUBSYSTEMS==\"usb*\", ATTRS{idVendor}==\"36bc\", MODE=\"0666\""
	}

	var b strings.Builder
	for _, d := range devices {
		fmt.Fprintf(&b, "─── Fractal Device ───\n")
		fmt.Fprintf(&b, "  VID:PID    : %04x:%04x\n", d.VendorID, d.ProductID)
		fmt.Fprintf(&b, "  Product    : %s\n", d.ProductName)
		fmt.Fprintf(&b, "  Serial     : %s\n", d.Serial)
		fmt.Fprintf(&b, "  Path       : %s\n", d.Path)
		fmt.Fprintf(&b, "  Interface  : %d\n\n", d.Interface)
	}
	return b.String()
}
