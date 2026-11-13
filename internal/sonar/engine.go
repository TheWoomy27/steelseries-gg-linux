package sonar

import (
	"encoding/json"
	"log/slog"
	"math"
	"strconv"
)

// The Sonar service drives the audio engine: it runs while Sonar is enabled
// in GG and onboarding is done, follows config, volume, ChatMix and routing
// changes, and makes Sonar's devices the defaults while it runs, restoring
// the previous defaults afterwards.

// Settings gg-linux keeps for the audio side in key_value; on Windows they
// live in the audio driver or the system.
const (
	volumesKey        = "LINUX_CLASSIC_VOLUMES"
	chatMixKey        = "LINUX_CHATMIX_BALANCE"
	savedDefaultsKey  = "LINUX_SAVED_DEFAULT_DEVICES"
	channelEnabledKey = "LINUX_CHANNEL_ENABLED_" // + role
)

// Media and Aux are optional channels ("Add media channel").
func (s *Service) channelEnabled(c channel) bool {
	if c.role == "Media" || c.role == "Aux" {
		return s.boolSetting(channelEnabledKey + c.role)
	}
	return true
}

func (s *Service) enabledChannels() []channel {
	var out []channel
	for _, c := range channels {
		if s.channelEnabled(c) {
			out = append(out, c)
		}
	}
	return out
}

// classicVolumes are the classic mode volumes: the master and each role's.
type classicVolumes struct {
	Master volume            `json:"master"`
	Roles  map[string]volume `json:"roles"`
}

func (s *Service) classicVolumes() classicVolumes {
	v := classicVolumes{Master: volume{1, false}, Roles: map[string]volume{}}
	json.Unmarshal([]byte(s.setting(volumesKey, "{}")), &v)
	for _, r := range channelRoles {
		if _, ok := v.Roles[r]; !ok {
			v.Roles[r] = volume{1, false}
		}
	}
	return v
}

func (s *Service) saveClassicVolumes(v classicVolumes) error {
	return s.setSetting(volumesKey, jsonText(v))
}

func (s *Service) chatMixBalance() float64 {
	b, _ := strconv.ParseFloat(s.setting(chatMixKey, "0"), 64)
	return math.Max(-1, math.Min(1, b))
}

// mixFactor is a playback channel's share of the master volume and, for
// Game and Chat, ChatMix: turning it toward one side lowers the other.
func (s *Service) mixFactor(c channel, vols classicVolumes) float64 {
	f := 1.0
	if !s.streamMode() {
		if vols.Master.Muted {
			return 0
		}
		f = sliderGain(vols.Master.Volume)
	}
	switch b := scaledBalance(s.chatMixBalance()); c.role {
	case "Game":
		f *= math.Max(0, math.Min(1, 1-b))
	case "ChatRender":
		f *= math.Max(0, math.Min(1, 1+b))
	}
	return f
}

// SetActive is called when Sonar is enabled or disabled in GG.
func (s *Service) SetActive(on bool) {
	s.mu.Lock()
	s.active = on
	switch {
	case on && s.stopWatch == nil:
		s.stopWatch = make(chan struct{})
		go s.watch(s.stopWatch)
	case !on && s.stopWatch != nil:
		close(s.stopWatch)
		s.stopWatch = nil
	}
	s.mu.Unlock()
	s.syncAudio()
	if on {
		s.refreshChatMix()
		s.registerQuickset()
	}
}

// HeadsetLinkChanged is called when a headset connects to or disconnects
// from its dongle: its ChatMix dial may have moved meanwhile.
func (s *Service) HeadsetLinkChanged() {
	if s.isActive() {
		s.refreshChatMix()
	}
}

func (s *Service) isActive() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active
}

// syncAudio starts, restarts or stops the engine to match Sonar's state.
func (s *Service) syncAudio() {
	defer s.sendStatus()
	if !s.isActive() || !s.boolSetting("ONBOARDING_FINISHED") {
		s.stopAudio()
		return
	}
	targets := s.redirectionDevices()
	if s.streamMode() {
		targets = s.streamTargets()
	}
	if err := s.audio.start(s.enabledChannels(), targets, s.streamMode()); err != nil {
		slog.Error("sonar: starting audio", "err", err)
		return
	}
	for _, c := range s.enabledChannels() {
		s.applyChannel(c)
	}
	if s.streamMode() {
		s.applyMicCopy()
	}
	s.makeDefault()
}

// Stop stops Sonar's audio and restores the default devices (ggd exit).
func (s *Service) Stop() { s.stopAudio() }

func (s *Service) stopAudio() {
	s.floor.follow("")
	s.stopSamples()
	s.restoreDefaults()
	s.audio.stop()
}

// applyChannel applies a channel's selected config and volume.
func (s *Service) applyChannel(c channel) {
	if !s.audio.isRunning(c) {
		return
	}
	vols := s.classicVolumes()
	// In streamer mode a playback channel's volumes are its mixes' gains.
	var mixes []any
	stream := s.streamMode() && !c.capture
	if stream {
		sv := s.streamerVolumes()
		mixes = []any{"mon:Mult", s.streamGain(sv, "Monitoring", c.role), "str:Mult", s.streamGain(sv, "Streaming", c.role)}
	}
	if cfgs, err := s.queryConfigs(`WHERE id IN (SELECT config_id FROM selected_config WHERE vad = ?)`, c.vad); err == nil && len(cfgs) > 0 {
		if data, ok := cfgs[0].Data.(*object); ok {
			s.audio.setControls(c, append(channelParams(c, data, s.mixFactor(c, vols), s.floor.get()), mixes...))
			if c.capture {
				s.followNoiseFloor(data)
				s.audio.setAEC(dataBool(data, "acousticEchoCancelingState"))
			}
		}
	} else if stream {
		s.audio.setControls(c, mixes)
	}
	if stream {
		s.audio.setVolume(c, 1, false)
		return
	}
	v := vols.Roles[c.role]
	s.audio.setVolume(c, sliderGain(v.Volume), v.Muted)
}

// followNoiseFloor measures the microphone while the automatic gate is on.
func (s *Service) followNoiseFloor(data *object) {
	dev := ""
	if autoGate(data) {
		dev = s.audio.target("Mic")
	}
	s.floor.follow(dev)
}

func (s *Service) applyVAD(vad int) {
	for _, c := range channels {
		if c.vad == vad {
			s.applyChannel(c)
		}
	}
}

func (s *Service) applyAll() {
	for _, c := range channels {
		s.applyChannel(c)
	}
}

// retarget moves the channels a redirection feeds to its new device.
func (s *Service) retarget(redirect string) {
	s.audio.setTarget(redirect, s.redirectionDevices()[redirect])
}

// makeDefault makes Sonar's Gaming and Microphone devices the defaults,
// remembering the previous ones once.
func (s *Service) makeDefault() {
	sink, source, err := defaultDevices()
	if err != nil {
		slog.Warn("sonar: reading default devices", "err", err)
		return
	}
	game, mic := channels[0].node, channels[len(channels)-1].node
	if s.setting(savedDefaultsKey, "") == "" && sink != game && source != mic {
		s.setSetting(savedDefaultsKey, jsonText(map[string]string{"sink": sink, "source": source}))
	}
	if err := setDefaultDevice("default.configured.audio.sink", game); err != nil {
		slog.Warn("sonar: setting default output", "err", err)
	}
	if err := setDefaultDevice("default.configured.audio.source", mic); err != nil {
		slog.Warn("sonar: setting default input", "err", err)
	}
}

func (s *Service) restoreDefaults() {
	saved := s.setting(savedDefaultsKey, "")
	if saved == "" {
		return
	}
	var d map[string]string
	json.Unmarshal([]byte(saved), &d)
	setDefaultDevice("default.configured.audio.sink", d["sink"])
	setDefaultDevice("default.configured.audio.source", d["source"])
	s.setSetting(savedDefaultsKey, "")
}
