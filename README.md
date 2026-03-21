# scape-ctl

System tray controller + CLI for the **Fractal Design Scape** wireless headset.

Replaces the browser-only [Adjust Pro](https://adjust.fractal-design.com) web app
with a native, always-running desktop app that can also trigger scripts when the
headset connects or disconnects.

```
┌──────────────────────────────────────┐
│          System Tray Menu            │
│  ● Scape Dark                        │
│  🔋 Battery: 78%                     │
│  ─────────────────                   │
│  EQ Preset  ▸ ① Balance              │
│               ② Clarity              │
│               ③ Depth                │
│  Lighting   ▸ Off / On               │
│  ─────────────────                   │
│  List Devices                        │
│  Edit Config                         │
│  ─────────────────                   │
│  Quit                                │
└──────────────────────────────────────┘
```

## Status

**Pre-alpha / protocol discovery phase.** The HID command bytes in
`internal/hid/protocol.go` are all `0x00` placeholders. You need to sniff the
real protocol from the Adjust Pro web app to fill them in. See the
[Reverse Engineering](#reverse-engineering) section below.

## Architecture

```
cmd/scape-ctl/main.go          Entry point: CLI subcommands + tray launch
internal/
  hid/
    protocol.go                 Wire protocol: constants, report builders/parsers
    device.go                   hidapi wrapper: open, send, receive
  monitor/
    monitor.go                  USB bus poller: detects connect/disconnect
  triggers/
    triggers.go                 Runs user scripts on device events
  config/
    config.go                   TOML config: ~/.config/scape-ctl/config.toml
  tray/
    tray.go                     System tray menu and click handlers
tools/
  webhid_sniffer.js             Chrome DevTools script for capturing HID traffic
```

## Prerequisites

- **Go 1.22+**
- **hidapi** system library:
  - Debian/Ubuntu: `sudo apt install libhidapi-dev`
  - Fedora: `sudo dnf install hidapi-devel`
  - macOS: `brew install hidapi`
  - Windows: bundled with go-hid
- **Linux only**: udev rule for non-root access (see below)

## Install

```bash
# Clone and build
git clone https://github.com/charlietran/scape-ctl
cd scape-ctl
make build

# (Linux) Install udev rule for non-root HID access
make udev
# OR manually:
sudo cp 50-fractal.rules /etc/udev/rules.d/
sudo udevadm control --reload-rules && sudo udevadm trigger

# Install binary
sudo make install
```

## Usage

### System tray app (default)

```bash
scape-ctl
```

Runs in the system tray with battery status, EQ switching, and lighting
controls. Also starts the device monitor and trigger system.

### CLI commands

```bash
scape-ctl devices      # List all connected Fractal HID devices
scape-ctl status       # Print battery, firmware, connection info
scape-ctl sniff        # Continuously print all incoming HID data
scape-ctl raw 01 02 ff # Send arbitrary HID bytes (for protocol discovery)
```

## Triggers

Configure scripts in `~/.config/scape-ctl/config.toml` to run when the headset
connects or disconnects:

```toml
[[triggers]]
event   = "Connected"
script  = "pactl set-default-sink alsa_output.usb-Fractal_Design_Scape"
label   = "Switch audio to Scape"
enabled = true

[[triggers]]
event   = "Disconnected"
script  = "pactl set-default-sink alsa_output.pci-0000_00_1f.3.analog-stereo"
label   = "Switch to speakers"
enabled = true
```

Scripts receive environment variables:

| Variable          | Example                          |
|-------------------|----------------------------------|
| `SCAPE_EVENT`     | `Connected`                      |
| `SCAPE_DEVICE`    | `Fractal Scape Dongle`           |
| `SCAPE_VID`       | `36bc`                           |
| `SCAPE_PID`       | `1002`                           |
| `SCAPE_PATH`      | `/dev/hidraw3`                   |
| `SCAPE_TIMESTAMP` | `2026-03-21T14:30:00Z`           |
| `SCAPE_JSON`      | Full event as JSON               |

See `config.example.toml` for more examples.

## Reverse Engineering

The protocol bytes need to be captured from the official Adjust Pro web app.
Here's the workflow:

### Step 1: Identify your device

```bash
lsusb -d 36bc:
# Example output:
# Bus 003 Device 005: ID 36bc:1002 Fractal Scape Dongle
```

Note the product ID and update `PIDScapeDongle` in `internal/hid/protocol.go`.

### Step 2: Sniff WebHID traffic

1. Open <https://adjust.fractal-design.com> in Chrome
2. Open DevTools (F12) → Console
3. Paste `tools/webhid_sniffer.js` and press Enter
4. Click "Add Fractal USB Device" and connect
5. Exercise each feature one at a time:
   - Switch each EQ preset → note which bytes change
   - Adjust each EQ band → note the encoding
   - Toggle each lighting mode → note mode bytes
   - Change colors / brightness / speed
   - Check the battery display vs. incoming reports
6. Add annotations: `scapeNote("switched to EQ slot 2")`
7. Export: `scapeExport()` downloads the full log as JSON

### Step 3: Read the JavaScript

The web app's JS bundle contains the complete protocol:

1. DevTools → Sources → find `adjust.fractal-design.com` JS chunks
2. Pretty-print (click `{}`) and search for:
   - `sendReport` / `sendFeatureReport` → reveals transport method
   - `0x36bc` or `36bc` → reveals VID/PID filters
   - Array literals with hex values → command payloads
3. OR grab the offline Electron app (Settings tab in Adjust Pro):
   ```bash
   npx asar extract resources/app.asar unpacked/
   grep -rn "sendReport\|sendFeatureReport\|0x36bc" unpacked/
   ```

### Step 4: Fill in protocol.go

Update the `TODO(sniff)` constants in `internal/hid/protocol.go` with
discovered values. The report builders and parsers already have the right
structure — you just need the actual byte values.

### Step 5: Test with the CLI

```bash
# Verify communication works
make build
./scape-ctl raw 00 01 02   # send bytes, see response
./scape-ctl sniff           # watch incoming data
./scape-ctl status          # test status parser
```

## Tips

- The **dongle** and **wired headset** may present as different USB devices with
  different PIDs and possibly different HID interfaces. The dongle relays
  commands to the headset wirelessly.
- **Feature reports** vs **output reports**: the sniffer logs both. Check whether
  the web app uses `sendReport` (output) or `sendFeatureReport` (feature) and
  update the `Transport` variable in `protocol.go`.
- **Don't send firmware commands** — look for them in the sniff log and avoid
  those report IDs / command bytes during testing.
- The headset stores settings in internal memory. Writes persist across reboots.

## License

MIT
