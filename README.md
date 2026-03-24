# scapectl

Native desktop controller and CLI for the **Fractal Design Scape** wireless gaming headset.

<img align="right"  height="300" alt="image" src="https://github.com/user-attachments/assets/7c6ec708-9fda-461a-a63e-31a04396f66d" />

Features:

- Real-time headset connection/disconnection detection
- Realtime status of headset features (battery level, status of mic, mute, RGB, noise cancellation, sidetone)
- Trigger scripts on headset power on/off and 2.4GHz receiver connect/disconnect (e.g. switch audio output)
- Send commands: switch EQ preset, toggle RGB on/off, toggle mic noise cancellation

Planned, not yet implemented:

- Customize headset button controls
- EQ code import/export
- Full lighting theme control

Built for macOS, Windows and Linux, but so far only tested on macOS and Windows.

- [Install](#install)
- [Security and Privacy](#security-and-privacy)
- [Usage](#usage)
- [Configuration](#configuration)
- [Triggers](#triggers)
- [Building from source](#building-from-source)
- [USB HID Protocol Reference](#usb-hid-protocol-reference)
- [Credits](#credits)
- [License](#license)

This is an unofficial app made for my own purposes, freely shared without any guarantees. I am not affiliated with or endorsed by Fractal Design in any way. The USB communication protocol was observed and documented from Fractal's [Adjust Pro](https://adjust.fractal-design.com) web app.

## Install

Grab the latest release for your platform from the [Releases page](https://github.com/charlietran/scapectl/releases). If you wish to compile yourself, see [Building from source](#building-from-source)

## Security and Privacy

This app is **not macOS notarized** and **not Windows signed**. Your OS will flag it as untrusted on first run. If this raises one or both of your eyebrows, it should! Stop running random programs from strangers on the internet! But this is a hobby project and I'm not going to bother paying for Apple and Microsoft developer accounts.

This app does not contain any telemetry or otherwise make network calls in the background. It does make a network request to check for the latest version if you click on the "Check for update" menu item.

### macOS

macOS will block the app with _"ScapeCtl is damaged and can't be opened"_ or _"cannot be opened because the developer cannot be verified"_. Remove the quarantine attribute:

```bash
xattr -cr ScapeCtl.app
```

macOS also requires explicit permission for the app to read USB data from the headphones. On first run you may see a "not permitted" error. Go to **System Settings** → **Privacy & Security** → **Input Monitoring**, click **+**, add the ScapeCtl app, and toggle it **on**.

### Windows

Windows will show a **"Windows protected your PC"** SmartScreen warning. Click **More info** → **Run anyway**.

### Linux

A udev Required for non-root HID access:

```bash
make udev
# OR manually:
sudo cp 50-fractal.rules /etc/udev/rules.d/
sudo udevadm control --reload-rules && sudo udevadm trigger
```

## Usage

The app is meant to be simple menu interface for seeing the status of the headphones and easily toggling some of its features. You may also configure it to run scripts upon certain events, to automate things like switching the audio device when the headphones are docked and undocked.

## Configuration

Config file location:

- **macOS**: `~/Library/Application Support/scapectl/config.toml`
- **Linux**: `~/.config/scapectl/config.toml`

A default config with comments is created on first run. See `config.example.toml` for all options.

## Triggers

Run scripts automatically on device events. Add `[[triggers]]` entries to your config file (see `config.example.toml` for all options).

### Notifications

```toml
[[triggers]]
event   = "HeadsetPowerOn"
script  = "osascript -e 'display notification \"Headset connected\" with title \"Scape\"'"
enabled = true

[[triggers]]
event   = "HeadsetPowerOff"
script  = "osascript -e 'display notification \"Headset disconnected\" with title \"Scape\"'"
enabled = true
```

### Audio Switching

The app cannot switch audio devices for you, but the trigger functiona allows you to execute any script you like. Here are config examples for how to automatically switch your default audio output when the headset powers on/off, with separate command-line audio switching tools:

> **Note:** Trigger scripts run without a login shell, so tools may not be on `PATH`. Use the full path to the executable in your trigger scripts. Run `which SwitchAudioSource` (macOS) or `where nircmd` (Windows) to find it.

**macOS** — install [SwitchAudioSource](https://github.com/deweller/switchaudio-osx):

```bash
brew install switchaudio-osx
which SwitchAudioSource  # e.g. /opt/homebrew/bin/SwitchAudioSource
SwitchAudioSource -a     # list available devices
```

> [!NOTE]  
> The full path to SwitchAudioSource may differ on your system, run `which SwitchAudioSource` to see your actual path

```toml
[[triggers]]
event    = "HeadsetPowerOn"
script   = "/opt/homebrew/bin/SwitchAudioSource -s 'Fractal Design Scape'"
enabled  = true
cooldown = 5

[[triggers]]
event    = "HeadsetPowerOff"
script   = "/opt/homebrew/bin/SwitchAudioSource -s 'MacBook Pro Speakers'"
enabled  = true
cooldown = 5
```

**Windows** — download [NirCmd](https://www.nirsoft.net/utils/nircmd.html) (free, single `.exe`, no install required). Place it somewhere permanent and use the full path:

> [!NOTE]  
> Replace `C:\Tools\nircmd.exe` below with the actual location of where you put nircmd.exe

```toml
[[triggers]]
event    = "HeadsetPowerOn"
script   = 'C:\Tools\nircmd.exe setdefaultsounddevice "Fractal Design Scape" 1'
enabled  = true
cooldown = 5

[[triggers]]
event    = "HeadsetPowerOff"
script   = 'C:\Tools\nircmd.exe setdefaultsounddevice "Speakers" 1'
enabled  = true
cooldown = 5
```

**Linux** — uses `pactl` (PulseAudio) or `wpctl` (PipeWire), usually pre-installed:

```bash
pactl list short sinks   # list devices (PulseAudio)
wpctl status             # list devices (PipeWire)
```

```toml
[[triggers]]
event    = "HeadsetPowerOn"
script   = "pactl set-default-sink alsa_output.usb-Fractal_Design_Scape"
enabled  = true
cooldown = 5

[[triggers]]
event    = "HeadsetPowerOff"
script   = "pactl set-default-sink alsa_output.pci-0000_00_1f.3.analog-stereo"
enabled  = true
cooldown = 5
```

> Replace device names in the examples above with your actual device names.

### Trigger fields

| Field      | Required | Description                                                                                                                            |
| ---------- | -------- | -------------------------------------------------------------------------------------------------------------------------------------- |
| `event`    | Yes      | Event name (see table below)                                                                                                           |
| `script`   | Yes      | Shell command to run                                                                                                                   |
| `enabled`  | Yes      | `true` or `false`                                                                                                                      |
| `cooldown` | No       | Minimum seconds between firings (default: 0). Prevents the same script from running repeatedly if the event fires in quick succession. |
| `battery`  | No       | For `BatteryLevel` only: fire when battery <= this % (default: 20)                                                                     |

### Available events

| Event                | Description                                        |
| -------------------- | -------------------------------------------------- |
| `DongleConnected`    | USB receiver plugged in                            |
| `DongleDisconnected` | USB receiver unplugged                             |
| `HeadsetPowerOn`     | Headset turned on (detected via dongle)            |
| `HeadsetPowerOff`    | Headset turned off or out of range                 |
| `BatteryLevel`       | Battery update (use `battery` field for threshold) |
| `MicMuted`           | Mic muted (boom flipped up)                        |
| `MicUnmuted`         | Mic unmuted (boom flipped down)                    |
| `EqChanged`          | EQ preset slot changed                             |
| `RgbOn`              | RGB lighting turned on                             |
| `RgbOff`             | RGB lighting turned off                            |
| `MncOn`              | Mic Noise Cancellation enabled                     |
| `MncOff`             | Mic Noise Cancellation disabled                    |

### Environment variables

Scripts receive these environment variables:

| Variable          | Example                              |
| ----------------- | ------------------------------------ |
| `SCAPE_EVENT`     | `HeadsetPowerOn`                     |
| `SCAPE_DEVICE`    | `Fractal Scape Dongle`               |
| `SCAPE_VID`       | `36bc`                               |
| `SCAPE_PID`       | `0001`                               |
| `SCAPE_PATH`      | `DevSrvsID:4295080900`               |
| `SCAPE_TIMESTAMP` | `2026-03-21T14:30:00-07:00`          |
| `SCAPE_JSON`      | Full event as JSON                   |
| `SCAPE_BATTERY`   | Battery % (BatteryLevel events only) |

### CLI

You may optionally use the CLI for your own scripting or tinkering. Running `scapectl` with no arguments launches the system tray app. Pass a subcommand for CLI mode:

```bash
scapectl status       # Print battery, firmware, EQ slot, mic, connection info
scapectl devices      # List connected Fractal HID devices
scapectl sniff        # Continuously print incoming HID data
scapectl raw 02 f1 21 # Send arbitrary HID bytes
```

On **macOS**, the binary is inside the app bundle. You can run CLI commands from it directly:

```bash
ScapeCtl.app/Contents/MacOS/scapectl status
```

On **Windows** and **Linux**, run the binary directly:

```bash
# Windows
scapectl.exe status

# Linux
./scapectl status
```

## Building from source

```bash
git clone https://github.com/charlietran/scapectl
cd scapectl
make build
./scapectl
```

Requires **Go 1.22+**. No other system dependencies on any platform.

### Cross-compilation

All builds are pure Go (no CGO) except macOS, which uses CGO for IOKit bindings. You can compile for all 3 platforms from macOS, or just for Linux & Windows on either of those platforms. ## USB HID Protocol Reference

Reverse-engineered from WebHID sniffer captures and the Fractal Adjust Pro Electron app source. This section documents the protocol for anyone building their own tools.

### Reverse Engineering Tools

- `tools/webhid_sniffer.js` — Paste into Chrome DevTools on adjust.fractal-design.com to capture all HID traffic with annotations
- The offline Electron app can be unpacked with `npx asar extract resources/app.asar unpacked/` for browseable JS source
## USB HID Protocol Reference

### Device Identifiers

| Device                | VID      | PID      | Notes                                  |
| --------------------- | -------- | -------- | -------------------------------------- |
| Fractal Scape Dongle  | `0x36BC` | `0x0001` | 2.4 GHz USB wireless receiver          |
| Fractal Scape (wired) | `0x36BC` | TBD      | USB-C direct connection                |
| Adjust Pro Hub        | `0x36BC` | `0x1001` | Fan/RGB controller (different product) |

### HID Transport

- **Report ID**: 2 (all commands use report ID 2)
- **Payload size**: 63 bytes (report ID excluded)
- **Transport**: Output reports (`sendReport`, not feature reports)
- **Collection**: usagePage `0xFF00`, usage 1 (vendor-specific, collection index 3)
- **Response pattern**: all responses echo the first 2 command bytes

The dongle exposes 4 HID collections. Only collection 3 (usagePage `0xFF00`) is used for the control protocol. The others are Consumer Control (media keys), vendor `0xFF13` (unknown), and Telephony (call buttons).

### Architecture: Dongle vs Headset

The dongle acts as a wireless relay. Commands prefixed `0x11` are handled by the dongle directly (instant response). Commands prefixed `0xF1` are relayed to the headset (slower, may timeout if headset is off).

The Adjust Pro web app treats these as two separate "devices" internally:

- **Dongle controller** (`DEVICE_TYPE_FACTORY_DONGLE = 0x11`): queries dongle state, checks headset presence
- **Headset delegate** (`DEVICE_TYPE_FACTORY_HEADSET = 0xF1`): queries headset status, controls EQ/mic/lighting

### Command Reference

#### 0x11 — Dongle Commands

| Command | Description             | Response                                       |
| ------- | ----------------------- | ---------------------------------------------- |
| `11 01` | Dongle firmware version | `11 01 00 <major> <minor>`                     |
| `11 02` | Dongle serial number    | `11 02 00 <ASCII string, null-terminated>`     |
| `11 21` | Dongle state poll       | `11 21 00 <headset_present> ...` (byte 3: 0/1) |

`11 21` byte 3 is not reliable for headset presence detection — it can report false values tied to the dongle's 2.4 GHz radio polling cycle. Use `f1 21` byte 18 (`btConnState`) instead for accurate connection state.

#### 0xF1 — Headset Commands

| Command       | Description              | Response                                   |
| ------------- | ------------------------ | ------------------------------------------ |
| `f1 01`       | Headset firmware version | `f1 01 00 <major> <minor>`                 |
| `f1 02`       | Headset serial number    | `f1 02 00 <ASCII string, null-terminated>` |
| `f1 05`       | Headset info             | `f1 05 <data...>`                          |
| `f1 21`       | Full status poll         | 63-byte status blob (see below)            |
| `f1 34 XX YY` | Set mic parameter        | `XX`=parameter, `YY`=value                 |
| `f1 36 01`    | Enable MNC               | Mic Noise Cancellation on                  |
| `f1 36 00`    | Disable MNC              | Mic Noise Cancellation off                 |

**`f1 21` Status Blob** (from Electron app `getUpdatedDeviceState`):

| Byte  | Field            | Values                                                  |
| ----- | ---------------- | ------------------------------------------------------- |
| 0-1   | Echo             | `f1 21`                                                 |
| 2     | Status           | 0 = OK                                                  |
| 3     | Boom mic         | 0 = boom attached, 1 = detached (using built-in mic) \* |
| 4     | Muted            | 0 = unmuted, 1 = muted (boom flipped up) \*\*           |
| 5     | EQ slot          | 1-3                                                     |
| 6     | Lighting slot    | 0 = off, 1+ = active                                    |
| 7-8   | Chat volume      |                                                         |
| 9-10  | Game volume      |                                                         |
| 11    | Volume state     |                                                         |
| 12    | BT mode          |                                                         |
| 13    | Hall sensor      | Boom mic position sensor                                |
| 14    | Battery          | 0-100 (%)                                               |
| 15    | Sidetone state   | 1 = on                                                  |
| 16-17 | Sidetone volume  |                                                         |
| 18    | BT connection    | 1 = connected                                           |
| 19    | MNC (ENC)        | 1 = on                                                  |
| 20    | Power state      | 1 = on                                                  |
| 33    | Dynamic lighting |                                                         |
| 40    | WDL opt-in       | Windows Dynamic Lighting                                |

_\* Electron source says `0 = byte3` means attached, but actual device behavior is inverted_
_\*\* Electron source says `0 = byte4` means muted, but actual device sends 1 = muted_

#### 0xA4 — Config Transfer & Lighting

| Command               | Description                           |
| --------------------- | ------------------------------------- |
| `a4 01 01 NN SS CC`   | Begin config write (length, segments) |
| `a4 02 3c <60 bytes>` | Config data chunk (0x3c = 60 bytes)   |
| `a4 03`               | End config write                      |
| `a4 04 00`            | Select effect slot 0 (RGB off)        |
| `a4 04 01`            | Select effect slot 1 (RGB on)         |
| `a4 05 01 00 00`      | Read current config/state             |
| `a4 0e 99`            | Keepalive heartbeat                   |

Lighting themes are uploaded as bulk data via `a4 01`/`a4 02`/`a4 03`. Simple on/off uses `a4 04`.

#### 0xA5 — Lighting Brightness/Color Upload

| Command              | Description                   |
| -------------------- | ----------------------------- |
| `a5 f0 00 ff`        | Begin brightness/color upload |
| `a5 01 00 ff <data>` | Data chunk                    |
| `a5 f1 00 ff`        | End upload                    |

#### 0xA7 — DSP / Audio (EQ)

| Command                  | Description                                           |
| ------------------------ | ----------------------------------------------------- |
| `a7 01 XX 01 YY`         | Init DSP slot (`XX`: 0x17/0x27/0x37 for driver 1/2/3) |
| `a7 02 NN DD PP <5×f32>` | Set biquad coefficients (LE float32)                  |
| `a7 03 DD 02 00 <UUID>`  | Set driver config with preset UUID                    |
| `a7 04 DD 00/01`         | Enable/disable feature per driver                     |
| `a7 05 NN DD PP`         | Set parameter per driver/band                         |
| `a7 07 DD`               | Select EQ slot / apply driver config                  |

**EQ Architecture:**

- 3 preset slots, each with up to 5 parametric EQ bands
- Each band: frequency (Hz), Q factor, gain (dB), filter type
- Filter types: 0 = peaking, 1 = low shelf, 2 = high shelf
- Biquad coefficients (IIR second-order sections) are pre-computed per sampling rate
- Driver IDs: 1, 2, 4 (corresponding to different audio paths/sample rates)
- Coefficients per band: `EqCoefA = [a1, a2]`, `EqCoefB = [b0, b1, b2]`
- Switching EQ slots re-uploads all coefficients — `a7 07 <slot>` selects which slot is active

**EQ Code Format** (shareable preset strings from the web app):

- Base64-encoded binary: `[version=1][numPoints][15 bytes per band][XOR checksum]`
- Per band (15 bytes): `[filterType:u8][gain:f32 LE][Q:f32 LE][freq:u16 LE][4 bytes padding]`
- Checksum: XOR of all preceding bytes

### Polling & Connection Detection

The Adjust Pro web app polls every **1500ms** with a **4200ms** response timeout:

```
11 21  →  dongle state (instant response)
f1 21  →  headset status (relayed, ~60ms when online, times out when offline)
a4 0e 99  →  keepalive
```

**Connection detection architecture** (from Electron app source):

The web app's dongle controller (`updateDeviceState`) sends `11 21` and reads byte 3. If `headsetConnected` transitions to true, it creates a "headset delegate" via `M.factory()`, which performs a multi-step handshake:

1. `readFWVersionFromDevice` (f1 01)
2. `readSerialNumberFromDevice` (f1 02)
3. `readEditionIdFromDevice`
4. `readBTAddrFromDevice`
5. `updateDeviceState` (f1 21 — full status)

All 5 steps run sequentially under a `deviceMutex` via `runCancellableExclusiveGroup`. This multi-step handshake takes several seconds. If `headsetConnected` goes false during the handshake, the `CancellableExclusiveGroup` cancels the in-progress factory creation, so no false "connected" event is emitted.

**Practical recommendation:** For simpler implementations, skip `11 21` for presence detection entirely and use only `f1 21` byte 18 (`btConnState`). The tradeoff is slower disconnect detection (~5s for the dongle's internal relay timeout) but zero false positives. This is the approach scapectl uses.

## Credits

- USB HID implementation based on [rafaelmartins/usbhid](https://github.com/rafaelmartins/usbhid) — pure Go USB HID via native OS APIs (IOKit, hidraw, WinAPI). BSD-3-Clause.
- System tray via [fyne-io/systray](https://github.com/fyne-io/systray) — cross-platform system tray library. BSD-3-Clause.
- [ebitengine/purego](https://github.com/ebitengine/purego) — pure Go syscall bridge for calling C libraries without CGO. Apache-2.0.
- Claude Code - huge help with figuring out the USB protocol and implementing the cross-platform Golang app

## License

GNU General Public License v3.0
