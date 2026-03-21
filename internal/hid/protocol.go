// Package hid implements the Fractal Design Scape USB HID protocol.
//
// Protocol was reverse-engineered from WebHID traffic captured with
// tools/webhid_sniffer.js on adjust.fractal-design.com (2026-03-21).
//
// The device uses vendor-specific HID collection 3 (usagePage 0xFF00)
// with report ID 2. All commands are output reports; responses are
// input reports. Commands use a two-byte family+subcommand prefix.
// Responses echo the first two bytes.
package hid

import (
	"encoding/binary"
	"fmt"
	"math"
	"strings"
)

// ── Device identifiers ──────────────────────────────

const FractalVendorID = 0x36bc

const (
	PIDHub         = 0x1001 // Adjust Pro Hub
	PIDScapeDongle = 0x0001 // Scape wireless dongle
	PIDScapeWired  = 0x0000 // TODO: capture with headset on USB cable
)

// ── Report format ───────────────────────────────────

const (
	ReportID   byte = 0x02 // All Scape commands use report ID 2
	ReportSize      = 63   // Payload size (excludes report ID byte)
)

// Transport method — confirmed as output reports (sendReport).
type TransportMethod int

const (
	TransportOutput  TransportMethod = iota // sendReport → device.Write
	TransportFeature                        // sendFeatureReport → device.SendFeatureReport
)

var Transport = TransportOutput

// ── Command table ───────────────────────────────────
//
// Commands are identified by the first 2-3 bytes of the payload.
// Family 0x11 = dongle queries, 0xF1 = headset queries,
// 0xA4 = config transfer, 0xA5 = EQ preset upload,
// 0xA7 = DSP/audio configuration.

// Dongle queries (family 0x11)
var (
	CmdDongleFWVersion = [2]byte{0x11, 0x01} // → [2]=0, [3]=major, [4]=minor
	CmdDongleSerial    = [2]byte{0x11, 0x02} // → [3..] ASCII serial
	CmdDonglePoll      = [2]byte{0x11, 0x21} // → [3]=headset_present
)

// Headset queries (family 0xF1)
var (
	CmdHeadsetFWVersion = [2]byte{0xF1, 0x01} // → [2]=0, [3]=major, [4]=minor
	CmdHeadsetSerial    = [2]byte{0xF1, 0x02} // → [3..] ASCII serial
	CmdHeadsetPresence  = [2]byte{0xF1, 0x05} // → [2]=present (0/1)
	CmdStatusPoll       = [2]byte{0xF1, 0x21} // → full status blob
)

// Config transfer (family 0xA4)
var (
	CmdConfigWriteBegin = [2]byte{0xA4, 0x01} // + total_len, segments
	CmdConfigWriteData  = [2]byte{0xA4, 0x02} // + chunk_len, data
	CmdConfigWriteEnd   = [1]byte{0xA4}       // sub=0x03
	CmdConfigApply      = [2]byte{0xA4, 0x04} // + 0x01
	CmdConfigRead       = [2]byte{0xA4, 0x05} // + 0x01 0x00 0x00
	CmdKeepalive        = [3]byte{0xA4, 0x0E, 0x99}
)

// DSP/audio (family 0xA7)
var (
	CmdDSPInit          = [2]byte{0xA7, 0x01} // + slot_id, 0x01, preset
	CmdDSPBiquad        = [2]byte{0xA7, 0x02} // + num_drivers, driver, band, 5×float32
	CmdDSPDriverConfig  = [2]byte{0xA7, 0x03} // + driver, 0x02, 0x00, UUID...
	CmdDSPFeatureToggle = [2]byte{0xA7, 0x04} // + driver, 0/1
	CmdDSPParam         = [2]byte{0xA7, 0x05} // + num_drivers, driver, param
	CmdDSPApply         = [2]byte{0xA7, 0x07} // + driver
)

// ── Lighting modes ──────────────────────────────────
// NOTE: lighting commands were not observed in the sniffer log.
// These values are tentative — they may use a different command family.

type LightingMode byte

const (
	LightOff            LightingMode = 0x00
	LightStatic         LightingMode = 0x01
	LightBreathing      LightingMode = 0x02
	LightNorthernLights LightingMode = 0x03
	LightSummerSky      LightingMode = 0x04
	LightRadiantDawn    LightingMode = 0x05
	LightCustom         LightingMode = 0xFF
)

func (m LightingMode) String() string {
	switch m {
	case LightOff:
		return "Off"
	case LightStatic:
		return "Static"
	case LightBreathing:
		return "Breathing"
	case LightNorthernLights:
		return "Northern Lights"
	case LightSummerSky:
		return "Summer Sky"
	case LightRadiantDawn:
		return "Radiant Dawn"
	case LightCustom:
		return "Custom"
	default:
		return "Unknown"
	}
}

// ── Data structures ─────────────────────────────────

type ConnectionMode int

const (
	ConnDisconnected ConnectionMode = iota
	ConnDongle
	ConnUSBCable
)

func (c ConnectionMode) String() string {
	switch c {
	case ConnDongle:
		return "Dongle"
	case ConnUSBCable:
		return "USB Cable"
	default:
		return "Disconnected"
	}
}

type DeviceStatus struct {
	Connected       bool
	Mode            ConnectionMode
	BatteryPercent  int // -1 if unknown
	Charging        bool
	FirmwareVersion string // headset firmware "major.minor"
	DongleFWVersion string // dongle firmware "major.minor"

	// Headset state (from f1 21 getUpdatedDeviceState)
	BoomMicConnected bool
	Muted            bool
	EqSlot           int  // 1-3, active EQ preset slot
	LightSlot        int  // active lighting preset slot
	SidetoneOn       bool
	SidetoneVol      int
	MNCOn            bool // Microphone Noise Cancellation
	PowerOn          bool
	BTMode           int
	HallSensor       int // boom mic position sensor
}

// BiquadCoeffs holds IIR biquad filter coefficients (second-order section).
// The DSP applies these directly — no frequency/gain/Q conversion needed on-device.
type BiquadCoeffs struct {
	B0, A1, B1, A2, B2 float32
}

type EqBand struct {
	FrequencyHz float64
	GainDB      float64
	Q           float64
}

type EqPreset struct {
	Slot  int
	Name  string
	Bands []EqBand
}

type RGB struct {
	R, G, B byte
}

type LightingConfig struct {
	Mode       LightingMode
	Color      RGB
	Brightness byte // 0-100
	Speed      byte // 0-100
}

type MicConfig struct {
	NoiseCancellation bool
	Sidetone          byte // 0-100
}

// ── Report builders ─────────────────────────────────
//
// Each returns (reportID, payload) ready for device.Send().

func buildCmd(cmd []byte) (byte, []byte) {
	buf := make([]byte, ReportSize)
	copy(buf, cmd)
	return ReportID, buf
}

func BuildGetStatus() (byte, []byte) {
	return buildCmd(CmdStatusPoll[:])
}

func BuildGetDongleFW() (byte, []byte) {
	return buildCmd(CmdDongleFWVersion[:])
}

func BuildGetDongleSerial() (byte, []byte) {
	return buildCmd(CmdDongleSerial[:])
}

func BuildGetHeadsetFW() (byte, []byte) {
	return buildCmd(CmdHeadsetFWVersion[:])
}

func BuildGetHeadsetSerial() (byte, []byte) {
	return buildCmd(CmdHeadsetSerial[:])
}

func BuildGetHeadsetPresence() (byte, []byte) {
	return buildCmd(CmdHeadsetPresence[:])
}

func BuildDonglePoll() (byte, []byte) {
	return buildCmd(CmdDonglePoll[:])
}

func BuildKeepalive() (byte, []byte) {
	return buildCmd(CmdKeepalive[:])
}

func BuildGetEqCurve(slot int) (byte, []byte) {
	// TODO: EQ read is done via config read (a4 05), not a direct query
	return buildCmd(CmdStatusPoll[:])
}

func BuildSetActiveEq(slot int) (byte, []byte) {
	// TODO: EQ preset switching uses the a7 command family
	// For now, send a status poll as a no-op
	return buildCmd(CmdStatusPoll[:])
}

// BuildSetBiquad builds a DSP biquad coefficient command for a single band.
// numDrivers is the driver group size (typically 3), driver is the driver
// index (01/02/04), and band is the band index (0-4).
func BuildSetBiquad(numDrivers, driver, band byte, coeffs BiquadCoeffs) (byte, []byte) {
	buf := make([]byte, ReportSize)
	buf[0] = CmdDSPBiquad[0]
	buf[1] = CmdDSPBiquad[1]
	buf[2] = numDrivers
	buf[3] = driver
	buf[4] = band
	binary.LittleEndian.PutUint32(buf[5:], math.Float32bits(coeffs.B0))
	binary.LittleEndian.PutUint32(buf[9:], math.Float32bits(coeffs.A1))
	binary.LittleEndian.PutUint32(buf[13:], math.Float32bits(coeffs.B1))
	binary.LittleEndian.PutUint32(buf[17:], math.Float32bits(coeffs.A2))
	binary.LittleEndian.PutUint32(buf[21:], math.Float32bits(coeffs.B2))
	return ReportID, buf
}

func BuildGetLighting() (byte, []byte) {
	// TODO: lighting commands not yet observed in sniffer log
	return buildCmd(CmdStatusPoll[:])
}

func BuildSetLighting(cfg *LightingConfig) (byte, []byte) {
	// TODO: lighting commands not yet observed in sniffer log
	return buildCmd(CmdStatusPoll[:])
}

func BuildSetMic(cfg *MicConfig) (byte, []byte) {
	// TODO: mic commands not yet observed in sniffer log
	return buildCmd(CmdStatusPoll[:])
}

// ── Report parsers ──────────────────────────────────

// ParseStatus parses the f1 21 status poll response.
//
// Byte layout (from Electron app source: getUpdatedDeviceState):
//
//	[0-1]  f1 21  (echo)
//	[2]    status (0=ok)
//	[3]    hasBoomMic (0=connected, 1=not)
//	[4]    isMuted (0=muted, 1=unmuted)
//	[5]    selectedEqSlot (1-3)
//	[6]    selectedLightSlot
//	[7-8]  chatVol
//	[9-10] gameVol
//	[11]   volumeState
//	[12]   btMode
//	[13]   hallSensor
//	[14]   battery (0-100)
//	[15]   sidetoneState (1=on)
//	[16-17] sidetoneVol
//	[18]   btConnState (1=connected)
//	[19]   encState / MNC (1=on)
//	[20]   powerState (1=on)
//	[33]   dynamicLighting
//	[40]   wdlOptIn
func ParseStatus(data []byte) *DeviceStatus {
	if len(data) < 21 {
		return nil
	}
	if data[0] != 0xF1 || data[1] != 0x21 {
		return nil
	}

	s := &DeviceStatus{
		Connected:      data[18] == 0x01,
		BatteryPercent: -1,
	}

	// Connection mode from btMode
	switch {
	case data[18] == 0x01:
		s.Mode = ConnDongle
	default:
		s.Mode = ConnDisconnected
	}

	if s.Connected {
		s.BatteryPercent = int(data[14])
		s.BoomMicConnected = data[3] != 0x00 // hall sensor: nonzero = boom mic attached
		s.Muted = data[4] == 0x00
		s.EqSlot = int(data[5])
		s.LightSlot = int(data[6])
		s.HallSensor = int(data[13])
		s.SidetoneOn = data[15] == 0x01
		s.SidetoneVol = int(data[16])
		s.MNCOn = data[19] == 0x01
		s.PowerOn = data[20] == 0x01
		s.BTMode = int(data[12])
	}

	return s
}

// ParseFWVersion parses a firmware version response (11 01 or f1 01).
// Returns "major.minor" string.
func ParseFWVersion(data []byte) string {
	if len(data) < 5 {
		return ""
	}
	return fmt.Sprintf("%d.%d", data[3], data[4])
}

// ParseSerial parses a serial number response (11 02 or f1 02).
// Returns the ASCII serial string.
func ParseSerial(data []byte) string {
	if len(data) < 4 {
		return ""
	}
	// Serial starts at byte 3, null-terminated ASCII
	end := len(data)
	for i := 3; i < len(data); i++ {
		if data[i] == 0 {
			end = i
			break
		}
	}
	return strings.TrimSpace(string(data[3:end]))
}

// ParsePresence parses the headset presence response (f1 05).
func ParsePresence(data []byte) bool {
	if len(data) < 3 {
		return false
	}
	return data[2] != 0
}

func ParseLighting(data []byte) *LightingConfig {
	// TODO: lighting response format not yet observed
	if len(data) < 7 {
		return nil
	}
	return &LightingConfig{
		Mode:       LightingMode(data[1]),
		Brightness: data[2],
		Speed:      data[3],
		Color:      RGB{R: data[4], G: data[5], B: data[6]},
	}
}
