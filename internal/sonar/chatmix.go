package sonar

import (
	"math"
	"net/http"
	"strconv"

	"ggl/internal/httpx"
)

// ChatMix works when Game and Chat play to the same device. On a headset
// that has ChatMix itself (the Arctis Nova 7's dial), the dial sets it and
// the mixer's slider follows; otherwise the slider sets it. With Game and
// Chat on different devices, or none, ChatMix is off and both play at full
// volume, as in Sonar's ChatMixService.

const (
	chatMixEnabled         = "enabled"
	chatMixFiniteWheel     = "finiteWheel"
	chatMixDifferentDevice = "differentDeviceSelected"
	chatMixNoDevice        = "noDeviceSelected"
)

const nullID = "00000000-0000-0000-0000-000000000000"

// chatMixState says how ChatMix works now and, when a headset's dial sets
// it, that headset's USB id.
func (s *Service) chatMixState() (string, uint32) {
	devs := s.redirectionDevices()
	game, chat := devs["Game"], devs["Chat"]
	switch {
	case game == "" && chat == "":
		return chatMixNoDevice, 0
	case game != chat:
		return chatMixDifferentDevice, 0
	}
	if h, usb, ok := s.headsetOf(game); ok && h.ChatMixDial {
		return chatMixFiniteWheel, usb
	}
	return chatMixEnabled, 0
}

// chatMix is Sonar's ChatMix: balance -1 (all game) .. 1 (all chat).
func (s *Service) chatMix(id string) map[string]any {
	if id == "" {
		id = nullID
	}
	state, _ := s.chatMixState()
	return map[string]any{"balance": s.chatMixBalance(), "state": state, "id": id}
}

func (s *Service) sendChatMix(id string) {
	s.ws.Broadcast("EVENT_SONAR_CHATMIX_DATA", s.chatMix(id))
}

func (s *Service) setChatMix(balance float64) {
	s.setSetting(chatMixKey, strconv.FormatFloat(math.Max(-1, math.Min(1, balance)), 'f', -1, 64))
	for _, c := range channels {
		if c.role == "Game" || c.role == "ChatRender" {
			s.applyChannel(c)
		}
	}
}

// refreshChatMix follows changes of the Game and Chat devices: ChatMix is
// reset when it turns off, and a headset's dial is read when it takes over.
func (s *Service) refreshChatMix() {
	switch state, usb := s.chatMixState(); state {
	case chatMixNoDevice, chatMixDifferentDevice:
		if s.chatMixBalance() != 0 {
			s.setChatMix(0)
		}
	case chatMixFiniteWheel:
		if h := s.headsetSource(); h != nil {
			if game, chat, ok := h.ChatMixLevels(usb); ok {
				if b, ok := dialBalance(game, chat); ok {
					s.setChatMix(b)
				}
			}
		}
	}
	s.sendChatMix("")
}

// HeadsetChatMix applies a move of a headset's ChatMix dial, with game and
// chat levels from 0 to 100.
func (s *Service) HeadsetChatMix(usb uint32, game, chat int) {
	if !s.isActive() {
		return
	}
	state, dial := s.chatMixState()
	if state != chatMixFiniteWheel || dial != usb {
		return
	}
	if b, ok := dialBalance(game, chat); ok {
		s.setChatMix(b)
		s.sendChatMix("")
	}
}

// dialBalance turns a dial position into Sonar's balance (ToBalance): one
// side is always at 100, and the other's attenuation is the balance.
func dialBalance(game, chat int) (float64, bool) {
	g, c := float64(game)/100, float64(chat)/100
	switch {
	case g < 0 || g > 1 || c < 0 || c > 1:
		return 0, false
	case !almostEqual(g, 1):
		if !almostEqual(c, 1) {
			return 0, false
		}
		return 1 - g, true
	}
	return c - 1, true
}

// scaledBalance is how far ChatMix turns the other side down for a slider
// balance (Sonar's ToScaled): halfway to one side leaves the other at a
// third.
func scaledBalance(b float64) float64 {
	if almostEqual(b, 0) {
		return 0
	}
	sign := math.Copysign(1, b)
	return sign * 2 * b / (b + sign)
}

func almostEqual(a, b float64) bool { return math.Abs(a-b) < 0.001 }

func (s *Service) chatMixRoutes() {
	sv := s.srv
	sv.Handle("GET /v1/chatMix", func(w http.ResponseWriter, r *http.Request) { httpx.JSON(w, s.chatMix("")) })
	sv.Handle("PUT /v1/chatMix", func(w http.ResponseWriter, r *http.Request) {
		b, err := strconv.ParseFloat(r.URL.Query().Get("balance"), 64)
		if err != nil {
			reply(w, nil, argErrorf("The balance field is required."))
			return
		}
		if state, _ := s.chatMixState(); state != chatMixEnabled {
			httpx.Error(w, http.StatusForbidden, "ChatMix balance is read-only")
			return
		}
		id := r.URL.Query().Get("correlationId")
		if !almostEqual(b, s.chatMixBalance()) {
			s.setChatMix(b)
			s.sendChatMix(id)
		}
		httpx.JSON(w, s.chatMix(id))
	})
}
