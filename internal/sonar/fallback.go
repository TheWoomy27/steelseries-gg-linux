package sonar

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"

	"ggl/internal/httpx"
)

// Fallback devices. Each channel keeps a priority list of the outputs (for
// the microphone, the inputs) Sonar has seen. With fallback on, a channel
// whose device goes away moves to the next available device in its list,
// and moves back when a device higher in the list returns; a device seen
// for the first time goes to the top of the lists. The wireless trigger
// also counts a SteelSeries wireless headset as gone while it is off,
// though its dongle stays plugged in. This follows Sonar's FallbackService;
// with fallback off a channel keeps its device and plays to it again when
// it returns.

type fallbackRole struct{ role, key, redirect, flow string }

var fallbackRoles = []fallbackRole{
	{"ChatRender", "CHAT_RENDER", "Chat", "render"},
	{"Game", "GAME", "Game", "render"},
	{"ChatCapture", "CHAT_CAPTURE", "Mic", "capture"},
	{"Media", "MEDIA", "Media", "render"},
	{"Aux", "AUX", "Aux", "render"},
}

// fallbackLabelsKey keeps the names of devices that are not plugged in.
const fallbackLabelsKey = "LINUX_FALLBACK_LABELS"

type fallbackDevice struct {
	Label                          string `json:"label"`
	IsActive                       bool   `json:"isActive"`
	IsSteelseriesWirelessSupported bool   `json:"isSteelseriesWirelessSupported"`
	IsSteelseriesWirelessConnected bool   `json:"isSteelseriesWirelessConnected"`
	ID                             string `json:"id"`
	IsExcluded                     bool   `json:"isExcluded"`
	IsPrevious                     bool   `json:"isPrevious"`
	IsNext                         bool   `json:"isNext"`
}

// presentDevice is a device PipeWire has now.
type presentDevice struct {
	label, flow         string
	wireless, connected bool
}

// online says whether a device counts as available: with the wireless
// trigger, a wireless headset only while it is connected.
func (p presentDevice) online(trigger bool) bool { return !trigger || !p.wireless || p.connected }

func (s *Service) presentDevices(objs []pwObject) map[string]presentDevice {
	usb := usbIDs(objs)
	h := s.headsetSource()
	hide := otherProfileNodes(objs)
	s.otherProfiles.Store(&hide)
	out := map[string]presentDevice{}
	for _, d := range devicesIn(objs, "", "") {
		if d.IsVad || hide[d.ID] {
			continue
		}
		p := presentDevice{label: d.FriendlyName, flow: d.DataFlow}
		if id, ok := usb[d.ID]; ok && h != nil {
			if info, ok := h.Headset(id); ok {
				p.wireless, p.connected = info.Wireless, info.Connected
			}
		}
		out[d.ID] = p
	}
	return out
}

func (s *Service) idList(key string) []string {
	var v struct {
		Devices []string `json:"devices"`
	}
	json.Unmarshal([]byte(s.setting(key, "{}")), &v)
	return v.Devices
}

func (s *Service) setIDList(key string, ids []string) {
	if ids == nil {
		ids = []string{}
	}
	s.setSetting(key, jsonText(map[string][]string{"devices": ids}))
}

func (r fallbackRole) listKey() string     { return r.key + "_FALLBACK_DEVICES" }
func (r fallbackRole) excludedKey() string { return r.key + "_FALLBACK_EXCLUDED_DEVICES" }

func (s *Service) fallbackEnabled() bool { return s.boolSetting("IS_FALLBACK_ENABLED") }

func (s *Service) wirelessTrigger() bool {
	return s.fallbackEnabled() && s.boolSetting("IS_STEELSERIES_WIRELESS_TRIGGER_ENABLED")
}

// order is a role's priority list, with devices it has not listed yet at
// the end. The device watch decides where those go (see fallbackTick).
func (s *Service) order(r fallbackRole, present map[string]presentDevice) []string {
	ids := s.idList(r.listKey())
	// Lists made before a card's other-profile copies were hidden still
	// name them.
	if hide := s.otherProfiles.Load(); hide != nil {
		cur := s.redirectionDevices()[r.redirect]
		ids = slices.DeleteFunc(ids, func(id string) bool { return (*hide)[id] && id != cur })
	}
	for _, id := range sortedKeys(present) {
		if present[id].flow == r.flow && !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	labels := map[string]string{}
	json.Unmarshal([]byte(s.setting(fallbackLabelsKey, "{}")), &labels)
	changed := false
	for id, p := range present {
		if labels[id] != p.label {
			labels[id], changed = p.label, true
		}
	}
	if changed {
		s.setSetting(fallbackLabelsKey, jsonText(labels))
	}
	return ids
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// fallbackList is a role's list as Sonar's API shows it, with the devices
// the channel would move to next and previous.
func (s *Service) fallbackList(r fallbackRole, present map[string]presentDevice) []fallbackDevice {
	ids := s.order(r, present)
	excluded := s.idList(r.excludedKey())
	labels := map[string]string{}
	json.Unmarshal([]byte(s.setting(fallbackLabelsKey, "{}")), &labels)
	out := make([]fallbackDevice, 0, len(ids))
	for _, id := range ids {
		p, active := present[id]
		out = append(out, fallbackDevice{Label: labels[id], ID: id, IsActive: active, IsExcluded: slices.Contains(excluded, id),
			IsSteelseriesWirelessSupported: p.wireless, IsSteelseriesWirelessConnected: p.connected})
	}
	if avail := s.available(r, present); len(avail) > 0 {
		out[slices.IndexFunc(out, func(d fallbackDevice) bool { return d.ID == avail[0] })].IsNext = true
		out[slices.IndexFunc(out, func(d fallbackDevice) bool { return d.ID == avail[len(avail)-1] })].IsPrevious = true
	}
	return out
}

// available lists the devices a role can move to, starting after its
// current device and wrapping around (GetAvailableActiveDevices).
func (s *Service) available(r fallbackRole, present map[string]presentDevice) []string {
	excluded := s.idList(r.excludedKey())
	trigger := s.wirelessTrigger()
	var avail []string
	for _, id := range s.order(r, present) {
		if p, ok := present[id]; ok && p.online(trigger) && !slices.Contains(excluded, id) {
			avail = append(avail, id)
		}
	}
	cur := slices.Index(avail, s.redirectionDevices()[r.redirect])
	if cur < 0 || len(avail) == 1 {
		return avail
	}
	return append(append([]string{}, avail[cur+1:]...), avail[:cur]...)
}

// fallbackLists is every role's list. Sonar's API returns a dictionary,
// which serializes as an object keyed by role.
func (s *Service) fallbackLists(present map[string]presentDevice) map[string][]fallbackDevice {
	out := map[string][]fallbackDevice{}
	for _, r := range fallbackRoles {
		out[camel(r.role)] = s.fallbackList(r, present)
	}
	return out
}

func (s *Service) notifyFallback(rs ...fallbackRole) {
	objs, err := pwDump()
	if err != nil {
		return
	}
	present := s.presentDevices(objs)
	for _, r := range rs {
		s.ws.Broadcast("SONAR_EVENT_FALLBACK_UPDATED", map[string]any{camel(r.role): s.fallbackList(r, present)})
	}
}

// setRoleDevice moves a channel to another device.
func (s *Service) setRoleDevice(r fallbackRole, device string) {
	if s.redirectionDevices()[r.redirect] == device {
		return
	}
	s.setRedirectionDevice(r.redirect, device)
	s.retarget(r.redirect)
	s.redirectionChanged(r.redirect)
}

// redirectionChanged tells the client about a channel's new device and
// updates what depends on it.
func (s *Service) redirectionChanged(redirects ...string) {
	s.ws.Broadcast("SONAR_EVENT_REDIRECTION_STATUS_UPDATE", nil)
	var rs []fallbackRole
	for _, r := range fallbackRoles {
		if slices.Contains(redirects, r.redirect) {
			rs = append(rs, r)
		}
	}
	s.notifyFallback(rs...)
	if slices.Contains(redirects, "Game") || slices.Contains(redirects, "Chat") {
		s.refreshChatMix()
	}
}

// fallBack moves a channel off its device, to the next available one.
func (s *Service) fallBack(r fallbackRole, present map[string]presentDevice) {
	next := ""
	if avail := s.available(r, present); len(avail) > 0 {
		next = avail[0]
	}
	s.setRoleDevice(r, next)
}

// deviceArrived handles a device that was plugged in or came online.
func (s *Service) deviceArrived(id string, present map[string]presentDevice) {
	trigger := s.wirelessTrigger()
	for _, r := range fallbackRoles {
		if r.flow != present[id].flow || slices.Contains(s.idList(r.excludedKey()), id) {
			continue
		}
		cur := s.redirectionDevices()[r.redirect]
		order := s.order(r, present)
		if p, ok := present[cur]; !ok || !p.online(trigger) || slices.Index(order, id) <= slices.Index(order, cur) {
			s.setRoleDevice(r, id)
		}
	}
}

// deviceLeft handles a device that was unplugged or went offline.
func (s *Service) deviceLeft(id string, present map[string]presentDevice) {
	for _, r := range fallbackRoles {
		if s.redirectionDevices()[r.redirect] == id {
			s.fallBack(r, present)
		}
	}
}

// fallbackTick follows devices coming and going, given the devices seen on
// the previous tick (nil on the first).
func (s *Service) fallbackTick(present, last map[string]presentDevice) {
	trigger := s.wirelessTrigger()
	if last == nil {
		// Devices already there when Sonar starts go to the end of the lists.
		for _, r := range fallbackRoles {
			s.setIDList(r.listKey(), s.order(r, present))
		}
		if !s.boolSetting("ONBOARDING_FINISHED") {
			return
		}
		if s.fallbackEnabled() && s.boolSetting("IS_FALLBACK_ON_REBOOT_ENABLED") {
			for _, r := range fallbackRoles {
				if p, ok := present[s.redirectionDevices()[r.redirect]]; !ok || !p.online(trigger) {
					s.fallBack(r, present)
				}
			}
		}
		return
	}
	if !s.boolSetting("ONBOARDING_FINISHED") {
		return
	}
	changed := false
	for _, id := range sortedKeys(present) {
		p := present[id]
		old, was := last[id]
		if !was {
			changed = true
			s.listFirstIfNew(id, p.flow)
		}
		if s.fallbackEnabled() && p.online(trigger) && (!was || !old.online(trigger)) {
			s.deviceArrived(id, present)
		}
		if was && (old.connected != p.connected || old.wireless != p.wireless) {
			changed = true
		}
	}
	for _, id := range sortedKeys(last) {
		p, still := present[id]
		if !still {
			changed = true
		}
		if s.fallbackEnabled() && last[id].online(trigger) && (!still || !p.online(trigger)) {
			s.deviceLeft(id, present)
		}
	}
	if changed {
		s.notifyFallback(fallbackRoles...)
	}
}

// listFirstIfNew puts a device seen for the first time at the top of the
// lists for its kind (ExcludeOrSetFirst).
func (s *Service) listFirstIfNew(id, flow string) {
	for _, r := range fallbackRoles {
		if r.flow != flow {
			continue
		}
		ids := s.idList(r.listKey())
		if slices.Contains(ids, id) {
			continue
		}
		s.setIDList(r.listKey(), append([]string{id}, ids...))
	}
}

// moveDevice moves src before dst in a role's list, or to the end without
// dst (SetDevicePosition).
func (s *Service) moveDevice(r fallbackRole, present map[string]presentDevice, src, dst string) error {
	ids := s.order(r, present)
	from := slices.Index(ids, src)
	if from < 0 {
		return fmt.Errorf("device %s is not in the %s fallback list", src, r.role)
	}
	ids = slices.Delete(ids, from, from+1)
	to := len(ids)
	if dst != "" {
		if to = slices.Index(ids, dst); to < 0 {
			return fmt.Errorf("device %s is not in the %s fallback list", dst, r.role)
		}
	}
	s.setIDList(r.listKey(), slices.Insert(ids, to, src))
	return nil
}

func (s *Service) excludeDevice(r fallbackRole, present map[string]presentDevice, id string) error {
	if !slices.Contains(s.order(r, present), id) {
		return fmt.Errorf("device %s is not in the %s fallback list", id, r.role)
	}
	excluded := s.idList(r.excludedKey())
	if !slices.Contains(excluded, id) {
		s.setIDList(r.excludedKey(), append(excluded, id))
	}
	if s.redirectionDevices()[r.redirect] == id && s.fallbackEnabled() {
		s.fallBack(r, present)
	}
	return nil
}

func (s *Service) includeDevice(r fallbackRole, present map[string]presentDevice, src, dst string) error {
	excluded := s.idList(r.excludedKey())
	i := slices.Index(excluded, src)
	if i < 0 {
		return nil
	}
	s.setIDList(r.excludedKey(), slices.Delete(excluded, i, i+1))
	return s.moveDevice(r, present, src, dst)
}

// onboarded puts the devices chosen in onboarding at the top of the lists.
func (s *Service) onboarded(devices map[string]string) {
	objs, err := pwDump()
	if err != nil {
		return
	}
	present := s.presentDevices(objs)
	for _, r := range fallbackRoles {
		if dev := devices[r.redirect]; dev != "" {
			ids := s.order(r, present)
			if slices.Contains(ids, dev) {
				s.moveDevice(r, present, dev, ids[0])
			}
		}
	}
}

func (s *Service) fallbackRoutes() {
	sv := s.srv
	type fallbackOp func(r fallbackRole, present map[string]presentDevice, req *http.Request) error
	current := func() (map[string]presentDevice, error) {
		objs, err := pwDump()
		if err != nil {
			return nil, err
		}
		return s.presentDevices(objs), nil
	}
	// One role: the response is that role's list.
	one := func(op fallbackOp) http.HandlerFunc {
		return func(w http.ResponseWriter, req *http.Request) {
			name, ok := enumName(req.PathValue("role"), roleNames)
			i := slices.IndexFunc(fallbackRoles, func(r fallbackRole) bool { return r.role == name })
			if !ok || i < 0 {
				reply(w, nil, argErrorf("Unhandled device role: %s", req.PathValue("role")))
				return
			}
			s.fallbackMu.Lock()
			defer s.fallbackMu.Unlock()
			present, err := current()
			if err == nil {
				err = op(fallbackRoles[i], present, req)
			}
			if err != nil {
				reply(w, nil, err)
				return
			}
			s.notifyFallback(fallbackRoles[i])
			httpx.JSON(w, s.fallbackList(fallbackRoles[i], present))
		}
	}
	// Every role of the device's kind: the response is all the lists.
	all := func(op fallbackOp) http.HandlerFunc {
		return func(w http.ResponseWriter, req *http.Request) {
			s.fallbackMu.Lock()
			defer s.fallbackMu.Unlock()
			present, err := current()
			if err != nil {
				reply(w, nil, err)
				return
			}
			id := req.PathValue("deviceId") + req.PathValue("sourceDeviceId")
			flow := present[id].flow
			if flow == "" {
				for _, r := range fallbackRoles {
					if slices.Contains(s.idList(r.listKey()), id) {
						flow = r.flow
					}
				}
			}
			for _, r := range fallbackRoles {
				if r.flow == flow {
					if err := op(r, present, req); err != nil {
						reply(w, nil, err)
						return
					}
				}
			}
			s.notifyFallback(fallbackRoles...)
			httpx.JSON(w, s.fallbackLists(present))
		}
	}
	exclude := func(r fallbackRole, present map[string]presentDevice, req *http.Request) error {
		return s.excludeDevice(r, present, req.PathValue("deviceId"))
	}
	include := func(r fallbackRole, present map[string]presentDevice, req *http.Request) error {
		return s.includeDevice(r, present, req.PathValue("sourceDeviceId"), req.PathValue("destinationDeviceId"))
	}
	position := func(r fallbackRole, present map[string]presentDevice, req *http.Request) error {
		return s.moveDevice(r, present, req.PathValue("sourceDeviceId"), req.PathValue("destinationDeviceId"))
	}
	sv.Handle("GET /fallbackSettings/lists", func(w http.ResponseWriter, req *http.Request) {
		s.fallbackMu.Lock()
		defer s.fallbackMu.Unlock()
		present, err := current()
		if err != nil {
			reply(w, nil, err)
			return
		}
		httpx.JSON(w, s.fallbackLists(present))
	})
	sv.Handle("PUT /fallbackSettings/excludeDevice/{role}/{deviceId}", one(exclude))
	sv.Handle("PUT /fallbackSettings/includeDevice/{role}/{sourceDeviceId}", one(include))
	sv.Handle("PUT /fallbackSettings/includeDevice/{role}/{sourceDeviceId}/{destinationDeviceId}", one(include))
	sv.Handle("PUT /fallbackSettings/setDevicePosition/{role}/{sourceDeviceId}", one(position))
	sv.Handle("PUT /fallbackSettings/setDevicePosition/{role}/{sourceDeviceId}/{destinationDeviceId}", one(position))
	sv.Handle("PUT /fallbackSettings/excludeDeviceFromAll/{deviceId}", all(exclude))
	sv.Handle("PUT /fallbackSettings/includeDeviceInAll/{sourceDeviceId}", all(include))
	sv.Handle("PUT /fallbackSettings/includeDeviceInAll/{sourceDeviceId}/{destinationDeviceId}", all(include))
	sv.Handle("PUT /fallbackSettings/setDevicePositionInAll/{sourceDeviceId}", all(position))
	sv.Handle("PUT /fallbackSettings/setDevicePositionInAll/{sourceDeviceId}/{destinationDeviceId}", all(position))
	for _, p := range []struct{ path, key string }{
		{"/fallbackSettings/isEnabled", "IS_FALLBACK_ENABLED"},
		{"/fallbackSettings/isWirelessTriggerEnabled", "IS_STEELSERIES_WIRELESS_TRIGGER_ENABLED"},
		{"/fallbackSettings/isFallbackOnRebootEnabled", "IS_FALLBACK_ON_REBOOT_ENABLED"},
	} {
		key := p.key
		sv.Handle("GET "+p.path, func(w http.ResponseWriter, r *http.Request) { httpx.JSON(w, s.boolSetting(key)) })
		sv.Handle("PUT "+p.path+"/{isEnabled}", func(w http.ResponseWriter, r *http.Request) {
			on, err := parseBool(r.PathValue("isEnabled"))
			if err == nil {
				err = s.setBoolSetting(key, on)
			}
			if err == nil && key != "IS_FALLBACK_ON_REBOOT_ENABLED" {
				go s.notifyFallback(fallbackRoles...)
			}
			reply(w, on, err)
		})
	}
}
