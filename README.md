# SteelSeries GG for Linux

A Linux port of SteelSeries GG built by reverse engineering the Windows
installer. It runs **your own copy** of the GG client (the original UI) on
Linux Electron and replaces the Windows services behind it:

- `ggd` (Go): the GG core, Engine, PrismSync and GameSense APIs the client
  talks to, plus device I/O over hidraw.
- `internal/devicert`: the Engine's device DSL (structs, APIs, HID chunks)
  on top of SteelSeries' open-source [golisp](https://github.com/steelseries/golisp),
  the same interpreter version the Engine embeds. It runs the 489 decrypted
  device specifications (606 device declarations, all load cleanly).
- `client/linux`: an Electron bootstrap that starts `ggd`, fixes up the
  handful of Windows-only calls, and loads the original client.

Nothing from the installer is committed here. `gg-setup` extracts the client,
device specifications, database migrations and images from an installer you
provide, and recovers the device-spec key from your copy of
`SteelSeriesEngine.exe` at import time.

## Setup

    go build -o bin/ ./cmd/...
    bin/gg-setup SteelSeriesGG<version>Setup.exe      # needs 7z
    sudo cp packaging/70-steelseries-gg.rules /etc/udev/rules.d/   # device access
    GG_LINUX_GGD=$PWD/bin/ggd packaging/steelseries-gg              # needs electron37+

`ggctl` talks to devices directly: `ggctl devices`, `ggctl apis 1852`,
`ggctl read 1852 read_battery_status`, `ggctl call 22a1 get_headset_status`.

## Status

Working:
- Installer import, spec decryption, Engine/PrismSync DB migrations (device catalog).
- Device runtime: all specs load; reads verified on real hardware
  (Aerox 5 Wireless, Apex 7, Arctis Nova 7: firmware, battery, chat mix).
- Client boots on Linux Electron through GG's full startup sequence.

In progress / not done:
- The home page still hits a client-side error on a remaining API shape
  mismatch (`index.js` reads an undefined field after startup); more
  endpoints need their exact response shapes.
- Config deploys/writes are implemented but untested on hardware.
  gg-linux does not write to a device until you change a setting in GG.
- Sonar (PipeWire), Moments (capture), tray, macro recording, GameSense
  lighting effects, PrismSync rendering, headset-specific Go logic
  (Arctis Gen2 EQ), and keyboard binding conversion are not implemented.
- Firmware updates are deliberately disabled.
- SteelSeries account sign-in is not supported (use "skip").
