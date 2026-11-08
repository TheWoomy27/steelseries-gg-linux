# Sonar on Linux

## How Sonar works on Windows

Sonar is a GG sub-app, `apps/sonar/SteelSeriesSonar.exe`: a .NET 8 ASP.NET
Core service (the `SoundStage.*` assemblies) that registers with the GG core
as the `sonar` sub-app and serves a REST API (about 100 routes) and a
websocket over plain HTTP on loopback. Its UI is part of the GG client; the
client finds the API through the sub-app's `webServerAddress`.

The audio side is Windows-only:

- a kernel driver adds virtual devices: SteelSeries Sonar - Gaming, Chat,
  Media, Aux and Microphone;
- an audio processing object (APO, `Sonar.APOAPI`) runs Sonar's DSP inside
  the Windows audio engine on each of those devices;
- Sonar makes its devices the Windows defaults and forwards each one to the
  real headset, speakers or microphone the user picks.

The client shows Sonar only where `window.api.os.platform()` is Windows or
macOS; the Linux bootstrap reports Windows to the pages.

Sonar keeps its state in its own SQLite database, built from
`apps/sonar/db-migrations` (402 presets ship with GG 123). API conventions:
Newtonsoft JSON, camelCase properties, enums as strings, camelCased route
tokens (`[Controller]` and `[Area]`).

## Design

`internal/sonar` is an in-process service in ggd with its own HTTP server,
registered as the `sonar` sub-app while it is enabled in GG settings. It
serves Sonar's API from its own database, built from the installer's
migrations by gg-setup.

Audio runs on PipeWire. ggd starts a PipeWire client (`pipewire -c` with a
generated configuration) that hosts one filter-chain per channel:

- Game, Chat, Media, Aux: a virtual sink named like Sonar's device
  ("SteelSeries Sonar - Gaming"), then the channel's processing, then
  playback to the output device chosen for that channel;
- Microphone: capture from the chosen microphone, then mic processing, then
  a virtual source "SteelSeries Sonar - Microphone";
- Streamer mode adds the Streaming and Monitoring mixes.

Settings that do not change the graph (EQ gains, volumes, ChatMix) are
applied live through the filter nodes' parameters. Changing devices
regenerates the configuration.

While Sonar is on, its Gaming sink and Microphone source are the default
devices, as on Windows. Turning Sonar off restores the previous defaults.

### Feature mapping

| Sonar | Linux |
| --- | --- |
| Parametric EQ, 10 bands: peakingEQ, lowShelving, highShelving, lowPass, highPass, notchFilter | filter-chain builtins `bq_peaking`, `bq_lowshelf`, `bq_highshelf`, `bq_lowpass`, `bq_highpass`, `bq_notch` |
| Bass and treble boost | low and high shelf |
| Voice clarity | presence boost (approximation) |
| General gain | builtin gain |
| Smart volume | compressor and limiter (LSP plugins, when installed) |
| Mic EQ (10-band graphic) | `bq_peaking` at fixed frequencies |
| Mic noise reduction (ClearCast), impact noise reduction | DeepFilterNet LADSPA when installed, otherwise WebRTC noise suppression |
| Noise gate, automatic noise gate | LSP gate |
| Volume stabilizer | WebRTC AGC or LSP compressor |
| Virtual surround 7.1, reverb | HRIR convolver (needs an HRIR set), later |
| ChatMix | Game and Chat sink volumes |
| Per-app routing | stream targets in PipeWire metadata |
| Samples, recording | pw-play, pw-record |
| Fallback devices | follow PipeWire device changes |

## Phases

1. Service, database and REST API: the Sonar page loads and edits persist.
   No audio changes. Done: configs (with Sonar's schema upgrades, checked
   against Sonar's own code by tools/sonar-oracle), mode, onboarding,
   classic routing and the settings the page reads. The client only opens
   Sonar for a signed-in SteelSeries account.
2. Classic mode audio: the four channels and the microphone with EQ,
   volumes, ChatMix, device selection and default devices.
3. Per-app routing, samples and fallback devices.
4. Streamer mode.
5. Mic effects, then surround and reverb equivalents.
