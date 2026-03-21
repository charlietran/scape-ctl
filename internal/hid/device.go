package hid

import (
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	usbhid "github.com/sstallion/go-hid"
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

// Device is an open connection to a Fractal Scape HID device.
type Device struct {
	dev         *usbhid.Device
	Info        DeviceInfo
	nonBlocking bool
}

// Enumerate finds Fractal Design HID devices, returning only the
// vendor-specific control interface (usagePage 0xFF00) for each device.
func Enumerate() ([]DeviceInfo, error) {
	var devices []DeviceInfo

	err := usbhid.Enumerate(FractalVendorID, 0x0000, func(info *usbhid.DeviceInfo) error {
		// Only return the vendor protocol collection (usagePage 0xFF00).
		// Each physical device exposes multiple HID collections; we only
		// want the one we can send commands to.
		if info.UsagePage != UsagePageVendor {
			return nil
		}
		product := "(unknown)"
		if info.ProductStr != "" {
			product = info.ProductStr
		}
		devices = append(devices, DeviceInfo{
			VendorID:    uint16(info.VendorID),
			ProductID:   uint16(info.ProductID),
			ProductName: product,
			Serial:      info.SerialNbr,
			Path:        info.Path,
			Interface:   info.InterfaceNbr,
			UsagePage:   info.UsagePage,
		})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("hid enumerate: %w", err)
	}
	return devices, nil
}

// OpenPath opens a device by its OS device path.
func OpenPath(path string) (*Device, error) {
	dev, err := usbhid.OpenPath(path)
	if err != nil {
		return nil, fmt.Errorf("hid open %s: %w", path, err)
	}

	// Get device info
	info := DeviceInfo{Path: path}
	if product, err := dev.GetProductStr(); err == nil {
		info.ProductName = product
	}
	if serial, err := dev.GetSerialNbr(); err == nil {
		info.Serial = serial
	}
	devInfo, _ := dev.GetDeviceInfo()
	if devInfo != nil {
		info.VendorID = uint16(devInfo.VendorID)
		info.ProductID = uint16(devInfo.ProductID)
	}

	d := &Device{dev: dev, Info: info}

	// Non-blocking so reads don't hang
	if err := dev.SetNonblock(true); err != nil {
		log.Printf("warning: failed to set non-blocking: %v", err)
	} else {
		d.nonBlocking = true
	}

	if Verbose {
		log.Printf("[hid] opened device: %s", info)
	}
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

// Send transmits a report to the device using the configured transport method.
func (d *Device) Send(reportID byte, payload []byte) error {
	switch Transport {
	case TransportOutput:
		// go-hid Write: first byte is report ID
		buf := make([]byte, 0, 1+len(payload))
		buf = append(buf, reportID)
		buf = append(buf, payload...)
		_, err := d.dev.Write(buf)
		return err

	case TransportFeature:
		buf := make([]byte, 0, 1+len(payload))
		buf = append(buf, reportID)
		buf = append(buf, payload...)
		_, err := d.dev.SendFeatureReport(buf)
		return err

	default:
		return fmt.Errorf("unknown transport method: %d", Transport)
	}
}

// Read reads a response from the device. Returns nil if no data available.
// The report ID byte is stripped so returned data aligns with the WebHID
// sniffer log offsets (byte 0 = first command byte, not report ID).
func (d *Device) Read(timeout time.Duration) ([]byte, error) {
	buf := make([]byte, ReportSize+1) // +1 for report ID prefix

	if timeout > 0 {
		if timeout < time.Millisecond {
			timeout = time.Millisecond
		}
		n, err := d.dev.ReadWithTimeout(buf, timeout)
		if err != nil {
			return nil, err
		}
		if n <= 1 {
			return nil, nil
		}
		return buf[1:n], nil // strip report ID
	}

	n, err := d.dev.Read(buf)
	if err != nil {
		return nil, err
	}
	if n <= 1 {
		return nil, nil
	}
	return buf[1:n], nil // strip report ID
}

// SendAndReceive sends a command and waits for a response.
func (d *Device) SendAndReceive(reportID byte, payload []byte, timeout time.Duration) ([]byte, error) {
	if err := d.Send(reportID, payload); err != nil {
		return nil, fmt.Errorf("send: %w", err)
	}
	resp, err := d.Read(timeout)
	if err != nil {
		return nil, fmt.Errorf("read: %w", err)
	}
	return resp, nil
}

// ── High-level operations ───────────────────────────

const defaultTimeout = 100 * time.Millisecond

// GetStatus sends f1 21 and parses the status blob (battery, connection, etc.).
// A timeout means the headset is unreachable (off/out of range) — returned as
// a disconnected status, not an error.
func (d *Device) GetStatus() (*DeviceStatus, error) {
	rid, payload := BuildGetStatus()
	resp, err := d.SendAndReceive(rid, payload, defaultTimeout)
	if err != nil {
		if errors.Is(err, usbhid.ErrTimeout) {
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
