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

Needs Go, 7z and electron37 or newer.

    go build -o bin/ ./cmd/...
    bin/gg-setup SteelSeriesGG<version>Setup.exe      # import into ~/.local/share/steelseries-gg
    packaging/install.sh                               # install for your user
    sudo cp packaging/70-steelseries-gg.rules /etc/udev/rules.d/   # device access
    systemctl --user enable --now steelseries-ggd.service          # start ggd at login

`install.sh` puts `ggd`, `ggctl`, `gg-setup` and the `steelseries-gg`
launcher in `~/.local/bin` (`PREFIX` changes that), adds a "SteelSeries
GG" desktop entry with GG's icon, and installs a systemd user service that
starts `ggd` with your graphical session. Run it again after updating
gg-linux: it also refreshes the Linux bootstrap in the imported client
(`gg-setup --bootstrap`). The launcher starts `ggd` itself when it is not
running.

`ggd` puts GG's tray icon in your notification area (StatusNotifierItem:
KDE, waybar and most Wayland bars, GNOME with the AppIndicator extension);
its "Open SteelSeries GG" runs the launcher installed beside `ggd`,
`steelseries-gg` from your PATH, or `$GG_LINUX_CLIENT`.

To run a source checkout without installing, point the launcher at it:
`GG_LINUX_GGD=$PWD/bin/ggd GG_LINUX_BOOTSTRAP=$PWD/client/linux/bootstrap.js packaging/steelseries-gg`.

`ggctl` talks to devices directly: `ggctl devices`, `ggctl apis 1852`,
`ggctl read 1852 read_battery_status`, `ggctl call 22a1 get_headset_status`.
It also drives Moments in the running ggd: `ggctl moments clip` saves a
clip, and `ggctl moments toggle|mic|desktop|status` switch capture, the
microphone and desktop capture.

## Status

Working:
- Installer import, spec decryption, Engine/PrismSync DB migrations (device catalog).
- Device runtime: all specs load; reads verified on real hardware
  (Aerox 5 Wireless, Apex 7, Arctis Nova 7: firmware, battery, chat mix).
- Client boots on Linux Electron through GG's full startup sequence; the
  home page, Engine gear page and device configuration pages render with
  the original UI (checked for Aerox 5 Wireless, Apex 7, Alias and Arctis
  Nova 7 Gen 2), including the Engine's configuration migrations.
- Parametric EQ presets: the game preset list and custom presets
  (create, edit, save, revert, rename, remove).
- On tiling compositors (Hyprland, sway) device configuration and other
  secondary windows float at GG's size; the main window tiles.
- GPU rendering on machines with two kinds of GPU (an Intel iGPU and an
  NVIDIA card): the launcher points Chromium at the GPU driving the
  displays, which Electron 37 does not do by itself.
- Tray icon (from ggd, with GG's own icons): "Open SteelSeries GG", an
  "Engine configs" menu to switch each connected device's configuration
  (with battery levels), menus sub-apps add, and "Exit", which stops ggd
  and the client.

In progress / not done:
- Config deploys run on the Apex 7 (settings, key mappings and the meta
  layer), the Aerox 5 Wireless, the Alias and the Arctis Nova 7 Gen 2,
  re-applied on connect; settings a wireless mouse misses while asleep
  are written when it wakes. gg-linux does not write to a device until
  you change a setting in GG.
- Key bindings: a configuration keeps its keys bound to macros, OS
  shortcuts (copy, paste...) and other keys. Devices with onboard macro
  memory (the Apex 7, Apex Pro and Apex 9 keyboards, the Prime mice) play
  them themselves: they are written with the key mappings, in the format
  Windows GG uses. ggd runs the rest: macros on devices without macro
  memory (most mice), launching an application (the picker offers the
  desktop's applications, .desktop files, AppImages and shell scripts),
  switching
  configurations, text macros (pasted from the clipboard with
  wl-copy, as Windows GG pastes them), GameSense game events and
  switching Engine apps (PrismSync, ImageSync and GameSense games) on,
  off or over. A key or button bound that way is
  remapped to send a spare key code the desktop has no use for (an
  unknown key), unless the binding is set to pass the input through;
  ggd reads the device's reports and plays macros through a virtual
  keyboard and mouse (/dev/uinput; the udev rules give the logged-in
  user access to it). Cooldown timers, game events and the record and engine
  app toggles are not run yet.
- Macro recording: GG's macro editor, its Quick Record fields and its
  shortcut settings record from every keyboard and mouse through
  /dev/input, so your user has to be in the `input` group. ggd only reads
  while GG records, and never grabs the devices.
- Sonar: setup, mixer and settings pages, and classic mode audio on
  PipeWire (channels, EQ, volumes, ChatMix, device routing, default
  devices), per-app routing, samples, fallback devices, the Arctis Nova
  7's ChatMix dial, streamer mode, the mixer's device volumes and level
  meters, the mic effects (ClearCast on the mic and chat, noise
  reduction, noise gate, compressor), smart volume and spatial audio
  (virtual 7.1 for headphones or speakers, with reverb); see
  docs/sonar.md. The effects need DeepFilterNet's LADSPA plugin and LSP
  plugins, and spatial audio PipeWire's SOFA plugin and libmysofa's HRTF
  set; without them they are left out. Echo canceling (WebRTC, through
  PipeWire's echo canceller) is offered while Sonar plays to speakers,
  any brand, where Windows offers it for SteelSeries Arena speakers only.
- QuickSet (GG's game profiles) has a sidebar entry. A profile bound to a
  game is deployed while the game runs (Steam games by their app id,
  others by install folder; a running game counts as in front, since
  Wayland has no portable foreground window), and Default comes back when
  it exits. Device configurations and the Sonar Game preset follow the
  profile; a game with a Sonar preset (Fortnite, Overwatch...) gets a
  profile made from it. Profiles are off until turned on in QuickSet.
- Headset EQ: the Arctis Nova 7 Gen 2's on-device EQ deploys with its
  configuration, and with Sonar on GG's Auto-Flatten EQ banner reports
  a headset EQ that is not flat and flattens it on request (not on its
  own).
- Moments: the clip library, the editor (trim, 9:16 crop, texts, audio
  tracks, screenshots, montages), exports and the settings pages. Capture
  is off until you turn it on. Then Moments keeps the last minutes of
  the game being played (or the desktop, in desktop mode) in a replay
  buffer in RAM, and a shortcut saves it as a clip in
  ~/Videos/SteelSeries Moments. Recording uses gpu-screen-recorder when
  it is installed (recommended: it encodes on the GPU and keeps sound and
  picture in sync), else wf-recorder on Wayland or ffmpeg on X11, with
  the game sound, chat and microphone each on a track of their own.
  On Hyprland, GG's Moments shortcuts (Alt+S saves a clip) are bound
  while capture is on, except keys your configuration already uses; on
  other desktops, bind a key to `ggctl moments clip`. Sharing to
  YouTube (Windows GG uploads through SteelSeries' cloud) exports the
  edited clip into the Shared Clips folder, shows it in the file manager
  with its path on the clipboard, and opens YouTube's upload page, to drag
  it in or pick it with the page's file dialog (Ctrl+L, Ctrl+V); the
  other services encode the clip to drag onto their site, as on Windows.
  Toggling the microphone restarts the recording, which empties the
  buffer.
- Prism (PrismSync V2, the Engine page's Prism tab): configurations,
  presets, the effects Off, Single color, Breathe and Color shift on any
  selection of keys and zones, the device canvas, and switching devices
  in or out. ggd renders the lighting and sends it to the devices
  SteelSeries made for it (Apex 7, Alias, the Aerox 3/5 Wireless...),
  which follow Prism as soon as they connect, as on Windows; a device
  switched out gets its own configuration's lighting back. The idle
  layer (after a time without input) and the reactive one (Line, Ripple
  and Fade on key presses) follow the SteelSeries keyboards and mice
  themselves; ggd reads their key reports only while the configuration
  uses one of those layers, and only to light the keys. On Hyprland the
  lighting dims or goes off while the monitors sleep, as each
  configuration's "When Monitor Sleeps" says. The Audio Visualizer
  listens to the default output (or the one picked in its audio source
  menu) through PipeWire while a configuration uses it, and lights the
  keyboard's columns as frequency bands with Prism's colour profiles and
  amplitude settings; GG's previews of it show made-up sound. Reflect
  copies the focused monitor through wlr-screencopy (Hyprland, sway and
  other wlroots compositors) while a configuration uses it, when the
  screen changes and at most 10 times a second, and lights each zone
  with the colour of its sampling area as Windows Prism does; GG's
  previews of it use a made-up picture. The copies cost the compositor
  some CPU (Hyprland: about 10% of a core on a 2560x1440 monitor).
  ImageSync (Engine > Apps) plays a GIF on per-key keyboards as Windows
  GG does, through GameSense. GG's PrismSync switch turns Prism's
  lighting off and on; while it is off, devices keep their own lighting,
  except where a game or ImageSync lights them.
- GameSense lighting: games that use the GameSense SDK light
  keys and zones with their bound handlers, as the Windows Engine runs
  them: static, gradient and ranged colours, flashing, percent and count
  bars, context colours and 22x6 bitmaps, on the zones each device's
  specification names. Prism draws the games' colours over its own
  lighting on the zones they use, until the game stops or sends nothing
  for its time-out (15 s by default). Games stay registered across
  restarts; GG's Apps page lists them with their switch and shows their
  effects on each device. Native games find ggd through
  `SteelSeries Engine 3/coreProps.json` in the data folder. Windows games
  under Proton or Wine read it from their prefix's
  `C:\ProgramData\SteelSeries\SteelSeries Engine 3`, so ggd writes it
  into every prefix it finds: Steam's in each library (new ones as soon
  as Proton makes them), `~/.wine`, Lutris and umu prefixes in `~/Games`,
  Heroic's and Bottles'. The Engine and GameSense keep their ports across
  restarts, so those copies stay right. Not done yet: saving effect
  changes made on that page (ggd applies them, but the page does not
  send them), GameSense screens (OLED) and vibration.
- Cooldown timers are a lighting effect of GG's older per-key lighting
  editor (Apex M750/M800, Apex 3 and MSI keyboards); they are not
  implemented, and GG does not offer them for the Apex 7 or the Aerox
  mice.
- A device whose battery runs low (5%, as its spec says) gets a desktop
  notification where Windows GG shows an overlay, following GG's overlay,
  notification and low battery sound switches.
- Hyprland ignores the minimize button, as it has no minimized state.
- Firmware updates are deliberately disabled.
- GG runs with a local profile (your Linux user, signed in) so Sonar and
  Moments open offline; SteelSeries account sign-in is not supported.
