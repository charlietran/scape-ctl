// scapectl is a system tray application for controlling the
// Fractal Design Scape wireless gaming headset.
//
// It provides:
//   - Battery status in the tray
//   - Quick-switch EQ presets
//   - Lighting on/off toggle
//   - Script triggers on device connect/disconnect
//
// Usage:
//
//	scapectl              # launch tray app
//	scapectl devices      # list connected Fractal HID devices and exit
//	scapectl status       # print device status and exit
//	scapectl raw <hex>    # send raw HID bytes (for reverse engineering)
//	scapectl sniff        # continuous read mode (print all incoming HID data)
package main

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"log"
	"math"
	"os"
	"strings"
	"time"

	"fyne.io/systray"

	"github.com/charlietran/scapectl/internal/config"
	"github.com/charlietran/scapectl/internal/hid"
	"github.com/charlietran/scapectl/internal/monitor"
	"github.com/charlietran/scapectl/internal/tray"
	"github.com/charlietran/scapectl/internal/triggers"
)

// version is set at build time via -ldflags "-X main.version=..."
var version = "dev"

func main() {
	log.SetFlags(log.Ltime | log.Lshortfile)

	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "devices":
			cmdDevices()
		case "status":
			cmdStatus()
		case "raw":
			cmdRaw(os.Args[2:])
		case "sniff":
			cmdSniff()
		case "eq-code":
			cmdEqCode(os.Args[2:])
		case "help", "-h", "--help":
			cmdHelp()
		default:
			fmt.Fprintf(os.Stderr, "unknown command: %s\n", os.Args[1])
			cmdHelp()
			os.Exit(1)
		}
		return
	}

	// ── Tray mode (default) ──
	cfg := config.Load()
	config.EnsureExists()
	hid.Verbose = cfg.Settings.Verbose

	pollInterval := time.Duration(cfg.Settings.PollIntervalMS) * time.Millisecond
	if pollInterval < 200*time.Millisecond {
		pollInterval = time.Second
	}

	mon := monitor.New(pollInterval)
	triggerEvents := mon.Subscribe()
	trayEvents := mon.Subscribe()
	mon.Start()

	triggerRunner := triggers.New(cfg)
	go triggerRunner.Run(triggerEvents)

	app := tray.New(cfg, mon, triggerRunner, trayEvents, version)
	systray.Run(app.OnReady, app.OnExit)
}

// ── CLI subcommands ─────────────────────────────────

func cmdDevices() {
	fmt.Print(hid.DumpDevices())
}

func cmdStatus() {
	dev, err := hid.OpenFirst()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	defer dev.Close()

	// Query full status via f1 21 (battery, connection, mode)
	status, err := dev.GetStatus()
	if err != nil {
		fmt.Fprintf(os.Stderr, "status error: %v\n", err)
		os.Exit(1)
	}
	if status == nil || !status.Connected {
		fmt.Println("Headset      : not connected")
		return
	}

	// Query FW/serial (these may fail due to unsolicited dongle reports)
	headsetFW, _ := dev.GetHeadsetFW()
	headsetSerial, _ := dev.GetHeadsetSerial()

	eqName := fmt.Sprintf("Slot %d", status.EqSlot)

	fmt.Printf("Headset FW   : %s\n", headsetFW)
	fmt.Printf("Headset Serial: %s\n", headsetSerial)
	fmt.Printf("Mode         : %s\n", status.Mode)
	if status.BatteryPercent >= 0 {
		fmt.Printf("Battery      : %d%%\n", status.BatteryPercent)
	} else {
		fmt.Printf("Battery      : unknown\n")
	}
	micType := "Built-in Omni Mic"
	if status.BoomMicConnected {
		micType = "Detachable Boom Mic"
	}

	fmt.Printf("EQ Preset    : %s\n", eqName)
	fmt.Printf("Current Mic  : %s\n", micType)
	fmt.Printf("Mic Muted    : %v\n", status.Muted)
	fmt.Printf("MNC          : %v\n", status.MNCOn)
	fmt.Printf("Sidetone     : %v (vol: %d)\n", status.SidetoneOn, status.SidetoneVol)
	fmt.Printf("Light Slot   : %d\n", status.LightSlot)
}

func cmdRaw(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: scapectl raw <hex bytes>")
		fmt.Fprintln(os.Stderr, "  e.g.: scapectl raw 01 02 03 ff")
		os.Exit(1)
	}

	dev, err := hid.OpenFirst()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	defer dev.Close()

	// Parse hex bytes
	hexStr := strings.Join(args, "")
	hexStr = strings.ReplaceAll(hexStr, " ", "")
	hexStr = strings.ReplaceAll(hexStr, "0x", "")
	data, err := hex.DecodeString(hexStr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid hex: %v\n", err)
		os.Exit(1)
	}

	reportID := byte(0x00)
	if len(data) > 0 {
		reportID = data[0]
		data = data[1:]
	}

	fmt.Printf("TX report_id=%02x data=%x\n", reportID, data)
	if err := dev.RawSend(reportID, data); err != nil {
		fmt.Fprintf(os.Stderr, "send error: %v\n", err)
		os.Exit(1)
	}

	// Try to read a response
	resp, err := dev.RawRead(1 * time.Second)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read error: %v\n", err)
		os.Exit(1)
	}
	if resp == nil {
		fmt.Println("RX (no response within 1s)")
	} else {
		fmt.Printf("RX %d bytes: %x\n", len(resp), resp)
	}
}

func cmdSniff() {
	dev, err := hid.OpenFirst()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	defer dev.Close()

	fmt.Printf("Sniffing HID input from %s\n", dev.Info)
	fmt.Println("Press Ctrl+C to stop.")

	for {
		data, err := dev.RawRead(100 * time.Millisecond)
		if err != nil {
			fmt.Fprintf(os.Stderr, "read error: %v\n", err)
			time.Sleep(time.Second)
			continue
		}
		if data != nil {
			ts := time.Now().Format("15:04:05.000")
			fmt.Printf("[%s] %d bytes: %x\n", ts, len(data), data)
		}
	}
}

func cmdEqCode(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: scapectl eq-code <decode|encode> [args]")
		fmt.Fprintln(os.Stderr, "  decode <code>   Decode an EQ code string")
		fmt.Fprintln(os.Stderr, "  encode <bands>  Encode bands (not yet implemented)")
		os.Exit(1)
	}

	switch args[0] {
	case "decode":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "usage: scapectl eq-code decode <code>")
			os.Exit(1)
		}
		decodeEqCode(args[1])
	default:
		fmt.Fprintf(os.Stderr, "unknown eq-code subcommand: %s\n", args[0])
		os.Exit(1)
	}
}

func decodeEqCode(code string) {
	data, err := base64.StdEncoding.DecodeString(code)
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid base64: %v\n", err)
		os.Exit(1)
	}

	if len(data) < 3 {
		fmt.Fprintln(os.Stderr, "EQ code too short")
		os.Exit(1)
	}

	version := data[0]
	numBands := int(data[1])

	if version != 1 {
		fmt.Fprintf(os.Stderr, "unsupported EQ code version: %d\n", version)
		os.Exit(1)
	}

	// Verify checksum (XOR of all bytes except last)
	var checksum byte
	for i := 0; i < len(data)-1; i++ {
		checksum ^= data[i]
	}
	if checksum != data[len(data)-1] {
		fmt.Fprintf(os.Stderr, "checksum mismatch: computed 0x%02x, stored 0x%02x\n", checksum, data[len(data)-1])
		os.Exit(1)
	}

	filterNames := map[byte]string{0: "Peaking", 1: "LowShelf", 2: "HighShelf"}

	fmt.Printf("EQ Code: %d bands\n\n", numBands)
	offset := 2
	for i := 0; i < numBands; i++ {
		if offset+11 > len(data) {
			fmt.Fprintf(os.Stderr, "truncated data at band %d\n", i+1)
			os.Exit(1)
		}
		filterType := data[offset]
		gain := math.Float32frombits(binary.LittleEndian.Uint32(data[offset+1:]))
		q := math.Float32frombits(binary.LittleEndian.Uint32(data[offset+5:]))
		freq := binary.LittleEndian.Uint16(data[offset+9:])

		name := filterNames[filterType]
		if name == "" {
			name = fmt.Sprintf("type%d", filterType)
		}

		fmt.Printf("  Band %d: %-10s  freq=%5dHz  gain=%+.1fdB  Q=%.2f\n",
			i+1, name, freq, gain, q)
		offset += 15
	}
}

func cmdHelp() {
	fmt.Println(`scapectl — Fractal Design Scape headset controller

Usage:
  scapectl              Launch system tray app
  scapectl devices      List connected Fractal HID devices
  scapectl status       Print headset status (battery, firmware, etc.)
  scapectl raw <hex>    Send raw HID report (for reverse engineering)
  scapectl sniff        Print all incoming HID data continuously
  scapectl eq-code      Decode/encode EQ preset codes
  scapectl help         Show this help

Config:  ` + config.Path() + `

Triggers:
  Configure scripts in the config file to run on device events.
  See config.example.toml for event types and examples.`)
}
