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

EQ bands are `bq_raw` filters whose coefficients ggd computes (RBJ
cookbook), so a band's type, frequency, gain and Q all change live; tone
controls, gain, master volume and ChatMix are further stages of the same
chain, and channel volumes are the virtual devices' own.

ggd links each channel to its device itself (`node.autoconnect = false`)
and keeps it linked as devices come and go. Leaving it to the session
manager would make a missing device either tear the channel down (with
`node.dont-fallback`) or fall back to the default device, which is Sonar's
own Gaming sink.

A filter-chain recreates its filters, at their configured coefficients,
whenever it starts running, so channels are not passive (they keep their
devices open while Sonar is on, as on Windows) and ggd reapplies a
channel's settings when it starts running again.

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
| ClearCast AI noise cancellation, mic and chat | DeepFilterNet LADSPA (mono on the mic, stereo on chat); the mix factor becomes its attenuation limit |
| Mic noise reduction, impact noise reduction | DeepFilterNet, holding noise at most 20 dB down |
| Noise gate, automatic noise gate | LSP Gate; the automatic gate sits 3 dB over the mic's measured noise floor |
| Volume stabilizer (the "Compressor") | LSP Autogain, its level the target loudness (-30 to -12 LUFS) |
| Acoustic echo canceling | not done; the client shows it only when a feature flag is unlocked |
| Virtual surround 7.1, reverb | HRIR convolver (needs an HRIR set), later |
| ChatMix | Game and Chat channel gains, on Sonar's curve; a headset's dial (Arctis Nova 7) sets it through Engine |
| Per-app routing | `target.object` metadata on the app's stream, remembered by WirePlumber |
| Samples, recording | pw-play, looping; pw-record of the raw microphone, played back through the microphone channel |
| Fallback devices | a one-second watch of PipeWire's devices; Engine's headset link for the wireless trigger |

## Phases

1. Service, database and REST API: the Sonar page loads and edits persist.
   No audio changes. Done: configs (with Sonar's schema upgrades, checked
   against Sonar's own code by tools/sonar-oracle), mode, onboarding,
   classic and stream routing and the settings the setup and mixer pages
   read. The client opens Sonar only for a signed-in account; the GG core
   signs in a local profile. The API writes enum values in camelCase
   (Newtonsoft with camelCase naming), as the client compares them.
2. Classic mode audio: the four channels and the microphone with EQ,
   volumes, ChatMix, device selection and default devices. Done and
   tested on a private PipeWire instance; Sonar's tone controls are
   approximations (bass and treble shelves, a presence peak for voice
   clarity).
3. Per-app routing, samples, fallback devices and the ChatMix dial. Done:
   - Apps' PipeWire streams are Sonar's audio sessions (the process comes
     from the stream or its client). Dragging an app sets its stream's
     `target.object` to the device's `object.serial`, which WirePlumber
     follows and remembers for the app, and a one-second watch sends the
     session opened and closed events.
   - Samples loop with pw-play on the channel's device (pausing starts a
     sample over). The microphone sample is a recording of the raw
     microphone, linked into the microphone channel beside the
     microphone while the processed microphone is linked to the chat
     device.
   - Fallback lists live in Sonar's own settings keys. The same watch
     moves channels when devices come and go, puts a device seen for the
     first time at the top, and with the wireless trigger follows
     Engine's link state for SteelSeries wireless headsets. With fallback
     off, a channel keeps its device and plays to it again when it
     returns.
   - Engine records the audio devices headset specs register
     (`register-mm-device-info`) and forwards `chatMixEvent`s. With Game
     and Chat on a headset with a ChatMix dial, the dial sets ChatMix
     (state `finiteWheel`) and the slider is read-only, as on Windows.
   Tested on a private PipeWire with WirePlumber (TestRoutingOnPipeWire,
   TestPhase3OnPipeWire), in the mixer, and with the Arctis Nova 7.
4. Streamer mode. Done:
   - Each playback channel's filter chain splits after its processing
     into a monitoring and a streaming gain; its device stream has four
     ports, the streaming pair named RL/RR because PipeWire drops AUX
     channels in conversion, and ggd links them to the monitoring and
     streaming devices.
   - A second chain copies the processed microphone into either mix
     (Sonar's MicCopy), and the microphone channel records from the
     stream mode microphone.
   - "SteelSeries Sonar - Stream" is a loopback's input side (objects the
     helper process created itself would not reach the session's
     PipeWire); streaming software records its monitor.
   - Mix volumes are linear gains, as in Sonar; a mix's master scales its
     roles and its mute is all roles muted. Stream monitoring links the
     streaming device's monitor to the monitoring device.
   - Entering streamer mode the first time takes the classic Game and Mic
     devices for the monitoring and microphone devices, which Windows
     leaves empty until chosen.
   Tested on a private PipeWire with WirePlumber (TestStreamModeOnPipeWire
   measures each mix) and in the mixer.
5. Device volumes, level meters and mic effects. Done:
   - The client's native audio meter (`audioMeter.node`, Windows endpoint
     and session meters) is client/linux/audiometer.js. Peaks come from
     pw-record streams in PipeWire's peak mode, 25 a second; they are
     passive and marked `stream.monitor`, so they neither keep devices
     running nor show up as apps. A sink is metered through its monitor,
     and a session meter (the GUIDs the mixer builds for Sonar's streams)
     through the Sonar channel's own device, before its volume. One
     `pw-dump -m` follows device volumes while a meter is open; wpctl sets
     them, on the cubic scale desktop volume controls use.
   - Sonar's device volume routes, which the classic mixer's device
     percentages read, give the channels' devices' volumes and send
     `SONAR_EVENT_DEVICE_VOLUMES_UPDATE` when they change.
   - The effects are LADSPA plugins that stay in the graph and are switched
     by their controls: DeepFilterNet costs nothing at an attenuation limit
     of 0 (about a quarter of a core when on), and LSP's plugins have a
     bypass, so changing an effect never restarts the audio. Playback
     graphs have a node per audio channel so chat can run the stereo
     DeepFilterNet (one instance instead of two). PipeWire's builtin
     noisegate is not used: in 1.6 its level input defaults to 0 instead
     of following the signal, so it never opens.
   - The automatic gate's noise floor is the 30th percentile of the raw
     mic's 40 ms peaks over the last ten seconds of sound.
   Tested on a private PipeWire with WirePlumber (TestMicEffectsOnPipeWire
   measures each effect, TestDeviceVolumesOnPipeWire the volume routes)
   and in the mixer.
6. Surround and reverb equivalents.
