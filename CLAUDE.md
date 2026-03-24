# CLAUDE.md

## Project Overview

scapectl is a Go system tray app + CLI for controlling the Fractal Design Scape wireless gaming headset over USB HID. It replaces the browser-only Adjust Pro web app (https://adjust.fractal-design.com) with a native desktop app that also supports script triggers on device connect/disconnect.

**Status: pre-alpha / protocol discovery phase.** The HID command bytes in `internal/hid/protocol.go` are placeholders (`0x00`). They must be filled in by sniffing the Adjust Pro web app's WebHID traffic using `tools/webhid_sniffer.js`.

## Architecture

```
cmd/scapectl/main.go           CLI entry point + tray launcher
internal/
  hid/protocol.go                Wire protocol constants, report builders/parsers
  hid/device.go                  hidapi wrapper (open/send/receive via go-hid)
  monitor/monitor.go             USB bus poller, emits connect/disconnect events
  triggers/triggers.go           Runs user shell scripts on device events
  usbhid/                        Inlined pure Go HID library (from rafaelmartins/usbhid)
  config/config.go               TOML config (~/.config/scapectl/config.toml)
  tray/tray.go                   System tray menu and click handlers
tools/webhid_sniffer.js          Chrome DevTools script for capturing HID traffic
```

## Key Technical Details

- **Vendor ID**: `0x36bc` (Fractal Design). Product IDs are TBD per device — the Adjust Pro Hub is `0x1001`, but the Scape dongle and wired headset have different PIDs.
- **Transport**: The protocol uses either HID output reports (`sendReport`) or feature reports (`sendFeatureReport`). The `Transport` variable in `protocol.go` controls which path `device.go` uses. Determine which by sniffing — the sniffer logs both `TX_OUTPUT` and `TX_FEATURE`.
- **Report size**: Assumed 64 bytes (standard full-speed USB HID). Confirm from `lsusb -v` or the WebHID `collections` object.
- **Monitor**: Polls `hid.Enumerate()` on a timer (default 1s). No hotplug/udev listener — polling is simpler and cross-platform.
- **Triggers**: Config-driven. Scripts get event context via env vars (`SCAPE_EVENT`, `SCAPE_DEVICE`, `SCAPE_JSON`, etc). Executed via `sh -c` on Unix, `cmd /C` on Windows.
- **Tray**: Uses `getlantern/systray`. The tray and trigger runner both consume events from the monitor's channel.

## Build & Run

```bash
# Prerequisites (Linux)
sudo apt install libhidapi-dev
sudo cp 50-fractal.rules /etc/udev/rules.d/
sudo udevadm control --reload-rules && sudo udevadm trigger

make build        # produces ./scapectl
make run          # build + launch tray
./scapectl devices   # list Fractal HID devices
./scapectl sniff     # print incoming HID data
./scapectl raw 01 02 # send arbitrary bytes
```

On macOS: `brew install hidapi`. On Windows: hidapi is bundled by go-hid.

## Conventions

- All `TODO(sniff)` comments mark protocol bytes that need to be filled in from reverse engineering. Search for these when updating the protocol.
- The `internal/hid/protocol.go` file is the single source of truth for the wire format. Report builders return `(reportID, payload)` tuples. Parsers take `[]byte` and return typed structs.
- Keep the CLI subcommands in `main.go` thin — they should call into `internal/` packages.
- Config file format is TOML. The canonical example is `config.example.toml`.
- The `raw` and `sniff` CLI commands exist specifically for protocol discovery. Don't remove them.
- Always run `make build` as its own separate step before committing. Never combine build and git commit in the same command.
- Do not commit or push automatically. Only commit when the user explicitly asks.

## Dependencies

- `internal/usbhid` — Inlined pure Go HID library (from github.com/rafaelmartins/usbhid, BSD-3-Clause). Uses IOKit directly on macOS via purego for async input report callbacks. No CGO required.
- `github.com/ebitengine/purego` — Pure Go syscall bridge (used by usbhid for IOKit on macOS)
- `fyne.io/systray` — Cross-platform system tray (fork of getlantern/systray, no GTK dependency)
- `github.com/pelletier/go-toml/v2` — TOML parser

No CGO required. Cross-compilation does not need platform-specific headers.

## Key Architecture Notes

- Monitor keeps a persistent HID connection and polls f1 21 every 1.5s
- Tray commands use monitor.RunCommand() which locks devMu to prevent interleaving with polls
- Sidetone requires interleaved status polls between f1 34 commands (dongle firmware requirement)
- The dongle sends unsolicited 11 21 reports — SendAndReceive filters by echo bytes to find the correct response
- 11 21 byte 3 (headset present) is unreliable — flaps in a 3-tick cycle. Use f1 21 byte 18 for presence.
- Device is opened non-exclusive so the OS still receives volume/media key reports

## Reverse Engineering Workflow

1. Run `lsusb -d 36bc:` to get product IDs
2. Paste `tools/webhid_sniffer.js` into Chrome DevTools on adjust.fractal-design.com
3. Connect device, exercise features, call `scapeExport()` to download the HID log
4. Update constants in `internal/hid/protocol.go`
5. Test with `./scapectl raw` and `./scapectl sniff`

Alternatively, download the offline Electron app from Adjust Pro's Settings tab and unpack it:
```bash
npx asar extract resources/app.asar unpacked/
grep -rn "sendReport\|sendFeatureReport\|0x36bc" unpacked/
```
