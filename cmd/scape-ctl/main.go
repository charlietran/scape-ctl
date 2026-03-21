// scape-ctl is a system tray application for controlling the
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
//	scape-ctl              # launch tray app
//	scape-ctl devices      # list connected Fractal HID devices and exit
//	scape-ctl status       # print device status and exit
//	scape-ctl raw <hex>    # send raw HID bytes (for reverse engineering)
//	scape-ctl sniff        # continuous read mode (print all incoming HID data)
package main

import (
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/getlantern/systray"

	"github.com/charlietran/scape-ctl/internal/config"
	"github.com/charlietran/scape-ctl/internal/hid"
	"github.com/charlietran/scape-ctl/internal/monitor"
	"github.com/charlietran/scape-ctl/internal/tray"
	"github.com/charlietran/scape-ctl/internal/triggers"
)

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

	app := tray.New(cfg, mon, triggerRunner, trayEvents)
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

	// Query dongle info
	dongleFW, _ := dev.GetDongleFW()
	dongleSerial, _ := dev.GetDongleSerial()

	fmt.Printf("Dongle FW    : %s\n", dongleFW)
	fmt.Printf("Dongle Serial: %s\n", dongleSerial)
	fmt.Println()

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

	// Headset is connected — query its FW/serial
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
		fmt.Fprintln(os.Stderr, "usage: scape-ctl raw <hex bytes>")
		fmt.Fprintln(os.Stderr, "  e.g.: scape-ctl raw 01 02 03 ff")
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
	fmt.Println("Press Ctrl+C to stop.\n")

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

func cmdHelp() {
	fmt.Println(`scape-ctl — Fractal Design Scape headset controller

Usage:
  scape-ctl              Launch system tray app
  scape-ctl devices      List connected Fractal HID devices
  scape-ctl status       Print headset status (battery, firmware, etc.)
  scape-ctl raw <hex>    Send raw HID report (for reverse engineering)
  scape-ctl sniff        Print all incoming HID data continuously
  scape-ctl help         Show this help

Config:  ` + config.Path() + `
Sniffer: tools/webhid_sniffer.js (paste into Chrome DevTools)

Triggers:
  Configure scripts in the config file to run on connect/disconnect.
  Scripts receive SCAPE_EVENT, SCAPE_DEVICE, SCAPE_VID, SCAPE_PID,
  SCAPE_PATH, SCAPE_TIMESTAMP, and SCAPE_JSON environment variables.`)
}
