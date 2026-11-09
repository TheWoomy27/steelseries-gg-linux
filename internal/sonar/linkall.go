package sonar

import (
	"net/http"
	"slices"

	"ggl/internal/httpx"
)

// Link All uses one device for every output channel. Linking copies a
// channel's device, priority list and excluded devices to the other output
// channels, as Sonar's LinkAllService does; while linked, the client sets
// every output channel's device at once (PUT
// /classicRedirections/render/deviceId). Sonar also links device change
// shortcuts, which gg-linux does not have.

const linkAllKey = "IS_RENDER_LINK_ALL_ENABLED"

type linkAllData struct {
	IsEnabled           bool                        `json:"isEnabled"`
	ClassicRedirections []classicRedirection        `json:"classicRedirections"`
	Devices             map[string][]fallbackDevice `json:"devices"`
	Shortcuts           []any                       `json:"shortcuts"`
}

func (s *Service) linkAllRoutes() {
	sv := s.srv
	sv.Handle("GET /linkAll/isRenderLinkAllEnabled", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, s.boolSetting(linkAllKey))
	})
	sv.Handle("PUT /linkAll/link/{sourceRole}", func(w http.ResponseWriter, r *http.Request) {
		name, ok := enumName(r.PathValue("sourceRole"), roleNames)
		if !ok {
			reply(w, nil, argErrorf("The value '%s' is not valid.", r.PathValue("sourceRole")))
			return
		}
		data, err := s.linkAll(name)
		reply(w, data, err)
	})
	sv.Handle("PUT /linkAll/unlink", func(w http.ResponseWriter, r *http.Request) {
		reply(w, false, s.setBoolSetting(linkAllKey, false))
	})
}

// linkAll copies the source role's device and fallback lists to the other
// output channels. Linking from the microphone only turns Link All on.
func (s *Service) linkAll(source string) (linkAllData, error) {
	if err := s.setBoolSetting(linkAllKey, true); err != nil {
		return linkAllData{}, err
	}
	s.fallbackMu.Lock()
	defer s.fallbackMu.Unlock()
	objs, err := pwDump()
	if err != nil {
		return linkAllData{}, err
	}
	present := s.presentDevices(objs)
	i := slices.IndexFunc(fallbackRoles, func(r fallbackRole) bool { return r.role == source })
	if i >= 0 && fallbackRoles[i].flow == "render" {
		src := fallbackRoles[i]
		var others []fallbackRole
		for _, r := range fallbackRoles {
			if r.flow == "render" && r != src {
				others = append(others, r)
			}
		}
		list, excluded := s.idList(src.listKey()), s.idList(src.excludedKey())
		for _, r := range others {
			s.setIDList(r.listKey(), list)
			s.setIDList(r.excludedKey(), excluded)
		}
		// The source's device, if it is there, plays every output channel.
		if device := s.redirectionDevices()[src.redirect]; device != "" {
			if _, ok := present[device]; ok {
				var changed []string
				for _, r := range others {
					if s.redirectionDevices()[r.redirect] != device {
						s.setRedirectionDevice(r.redirect, device)
						s.retarget(r.redirect)
						changed = append(changed, r.redirect)
					}
				}
				if len(changed) > 0 {
					s.redirectionChanged(changed...)
				}
			}
		}
		// A channel left on a device the copied list excludes moves on.
		for _, r := range others {
			if slices.Contains(excluded, s.redirectionDevices()[r.redirect]) {
				s.fallBack(r, present)
			}
		}
		s.notifyFallback(fallbackRoles...)
	}
	return linkAllData{
		IsEnabled:           true,
		ClassicRedirections: s.classicRedirections(classicRedirectionIDs),
		Devices:             s.fallbackLists(present),
		Shortcuts:           []any{},
	}, nil
}
