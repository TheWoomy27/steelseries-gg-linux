package engine

import (
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/steelseries/golisp"

	"ggl/internal/devicert"
	"ggl/internal/hid"
)

var deviceTypeNames = []string{"keyboard", "mouse", "voice", "headset", "controller", "other", "application",
	"mousepad", "motherboard", "otherRGB", "monitor", "speakers", "microphone"}

func typeName(t int) string {
	if t >= 0 && t < len(deviceTypeNames) {
		return deviceTypeNames[t]
	}
	return "other"
}

// liveDevice is a catalog device with at least one physical product
// (dongle, wired cable, bluetooth...) plugged in.
type liveDevice struct {
	mu        sync.Mutex
	dbID      int
	cat       *catalogDevice
	physical  map[uint32]*devicert.Decl
	firmware  map[uint32]string
	connected time.Time
	ready     bool
	status    map[string]any // generic status properties (battery, link...)
}

func (l *liveDevice) decls() []*devicert.Decl {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]*devicert.Decl, 0, len(l.physical))
	for _, d := range l.physical {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// primary is the physical device configs are written through.
func (l *liveDevice) primary() *devicert.Decl {
	ds := l.decls()
	if len(ds) == 0 {
		return nil
	}
	// Prefer the physical whose spec declares the most APIs: for wireless
	// mice that is the dongle/mouse spec rather than a bootloader.
	best := ds[0]
	for _, d := range ds[1:] {
		if len(d.ApiNames()) > len(best.ApiNames()) {
			best = d
		}
	}
	return best
}

func (e *Engine) liveByProduct(pid uint32) *liveDevice {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, l := range e.live {
		if uint32(l.cat.ProductID) == pid {
			return l
		}
		l.mu.Lock()
		_, ok := l.physical[pid]
		l.mu.Unlock()
		if ok {
			return l
		}
	}
	return nil
}

func (e *Engine) liveByID(id int) *liveDevice {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.live[id]
}

// onHotplug reconciles live devices with the hidraw nodes present.
func (e *Engine) onHotplug(added, removed []hid.Info) {
	present := e.hid.Products()
	for pid := range present {
		phys := uint32(hid.SteelSeriesVID)<<16 | uint32(pid)
		if e.liveByProduct(phys) != nil {
			continue
		}
		e.connectPhysical(phys)
	}
	e.mu.Lock()
	var gone []*liveDevice
	for id, l := range e.live {
		l.mu.Lock()
		for phys := range l.physical {
			if _, ok := present[uint16(phys&0xffff)]; !ok {
				delete(l.physical, phys)
			}
		}
		empty := len(l.physical) == 0
		l.mu.Unlock()
		if empty {
			delete(e.live, id)
			gone = append(gone, l)
		}
	}
	e.mu.Unlock()
	for _, l := range gone {
		slog.Info("device disconnected", "device", l.cat.FullName)
		e.emit("deviceDisconnected", map[string]any{"id": l.dbID, "product_id": l.cat.ProductID})
		e.emit("EVENT_DEVICE_STATUS_CHANGED", map[string]any{"deviceId": l.dbID, "connected": false})
	}
}

func (e *Engine) connectPhysical(phys uint32) {
	if !e.rt.KnownDevice(phys) {
		slog.Debug("no specification for device", "pid", fmt.Sprintf("%#x", phys))
		return
	}
	main := e.mainProductFor(phys)
	cat, err := e.catalogByProduct(main)
	if err != nil {
		if cat, err = e.catalogByProduct(phys); err != nil {
			slog.Warn("device not in catalog", "pid", fmt.Sprintf("%#x", phys), "main", fmt.Sprintf("%#x", main))
			return
		}
	}
	decl, err := e.rt.Device(phys)
	if err != nil {
		slog.Warn("device spec", "pid", fmt.Sprintf("%#x", phys), "err", err)
		return
	}
	e.mu.Lock()
	l, existing := e.live[cat.ID]
	if !existing {
		l = &liveDevice{dbID: cat.ID, cat: cat, physical: map[uint32]*devicert.Decl{}, firmware: map[uint32]string{}, connected: time.Now()}
		e.live[cat.ID] = l
	}
	l.mu.Lock()
	l.physical[phys] = decl
	l.mu.Unlock()
	e.mu.Unlock()
	slog.Info("device connected", "device", cat.FullName, "physical", fmt.Sprintf("%#x", phys), "spec", decl.Name)
	e.userDevice(cat.ID, true)
	e.ensureConfig(cat)
	go e.initDevice(l, decl)
}

// initDevice runs the spec's (init ...) and reads firmware, then tells the
// client the device is ready.
func (e *Engine) initDevice(l *liveDevice, d *devicert.Decl) {
	if d.StartDelayMs > 0 {
		time.Sleep(time.Duration(d.StartDelayMs) * time.Millisecond)
	}
	if err := withTimeout(15*time.Second, d.RunInit); err != nil {
		slog.Warn("device init", "spec", d.Name, "err", err)
	}
	if v, err := d.Call("get_fw_version"); err == nil && golisp.StringP(v) {
		fw := golisp.StringValue(v)
		l.mu.Lock()
		l.firmware[d.ID] = fw
		l.mu.Unlock()
		e.db.Exec(`UPDATE physical_devices SET firmware_version = ? WHERE product_id = ?`, fw, d.ID)
	}
	l.mu.Lock()
	l.ready = true
	l.mu.Unlock()
	e.emit("deviceConnected", e.deviceJSON(l.cat, l))
	e.emit("EVENT_DEVICE_STATUS_CHANGED", map[string]any{"deviceId": l.dbID, "connected": true})

	// gg-linux only writes settings it owns: once a configuration has been
	// deployed from GG, reconnecting the device re-applies it, as the
	// Windows Engine does. A device GG has never configured keeps its
	// onboard settings untouched.
	if cfg, err := e.activeConfig(l.dbID); err == nil && cfg.LastDeployed > 0 {
		if err := e.deploy(l, cfg.settingsMap(), false); err != nil {
			slog.Warn("deploy on connect", "device", l.cat.Name, "err", err)
		}
	}
}

func withTimeout(d time.Duration, fn func() error) error {
	ch := make(chan error, 1)
	go func() { ch <- fn() }()
	select {
	case err := <-ch:
		return err
	case <-time.After(d):
		return fmt.Errorf("timed out after %s", d)
	}
}

// deviceJSON renders domains.Device.
func (e *Engine) deviceJSON(cat *catalogDevice, l *liveDevice) map[string]any {
	ud := e.userDevice(cat.ID, false)
	connected := 0
	connectedPID := uint32(0)
	var fwSupported = true
	firmware := map[uint32]string{}
	var primary *devicert.Decl
	if l != nil {
		connected = 1
		l.mu.Lock()
		for k, v := range l.firmware {
			firmware[k] = v
		}
		l.mu.Unlock()
		if primary = l.primary(); primary != nil {
			connectedPID = primary.ID
			fwSupported = primary.FirmwareSupported()
		}
	}
	phys, _ := e.physicalDevices(cat.ProductID)
	physJSON := []map[string]any{}
	for _, p := range phys {
		fw := p.Firmware
		if v, ok := firmware[p.ProductID]; ok {
			fw = v
		}
		physJSON = append(physJSON, map[string]any{
			"product_id":                p.ProductID,
			"hexProductId":              fmt.Sprintf("%04X", p.ProductID&0xffff),
			"name":                      p.Name,
			"full_name":                 p.FullName,
			"firmware":                  map[string]any{"version": fw, "incompatible": false, "should_update": false, "update_in_progress": false, "re_pair_required": false, "excluded_from_update": false},
			"bootloader_product_id":     p.BootloaderPID,
			"bootloader_mode":           p.BootloaderMode,
			"is_exclusive":              p.IsExclusive,
			"displayed_counterfeit_msg": false,
			"priority":                  p.Priority,
		})
	}
	cfgs := e.configMetadata(cat.ID)
	m := map[string]any{
		"id":                               cat.ID,
		"product_id":                       cat.ProductID,
		"connected_product_id":             connectedPID,
		"secondary_connected_product_id":   0,
		"name":                             cat.Name,
		"full_name":                        cat.FullName,
		"display_name":                     cat.FullName,
		"type":                             cat.Type,
		"deviceTypeName":                   typeName(cat.Type),
		"connected":                        connected,
		"configuration_metadata":           cfgs,
		"optimized_for":                    ud.OptimizedFor,
		"child_devices":                    e.childDevices(cat.ID),
		"physical_devices":                 physJSON,
		"lighting_templates":               e.lightingTemplates(cat.ID),
		"hidden":                           ud.Hidden,
		"persist_changes":                  cat.PersistChanges,
		"software_update_url":              "",
		"device_card_app":                  "",
		"about_page_disabled":              cat.AboutPageDisabled,
		"hide_device_card":                 cat.HideDeviceCard,
		"has_onboard_macros":               cat.HasOnboardMacros,
		"first_plugin":                     ud.FirstPlugin,
		"is_application_disabling_allowed": false,
		"is_application_deploy_disabled":   cat.DeploymentOff,
		"background_software_id":           "",
		"background_software_version":      "",
		"supports_prism_sync":              false,
		"sequence_step_ms":                 33,
		"is_counterfeit":                   false,
		"firmware_version_is_supported":    fwSupported,
		"prismsync_v2_whitelisted":         false,
		"supportedSubAppFeatures":          []any{},
		"genericDevicePropertiesRead":      []any{},
		"genericDevicePropertiesWrite":     []any{},
		"genericDevicePropertiesStatus":    []any{},
		"imageUrl":                         "",
		"wireless_device_information":      map[string]any{"transmitter_product_id": 0, "transmitter_name": "", "receiver_product_id": 0, "receiver_id": 0, "receiver_name": ""},
		"firstConnectionTime":              ud.FirstConnect,
	}
	if primary != nil {
		m["supports_prism_sync"] = primary.SupportsPrismSync
		m["prismsync_v2_whitelisted"] = primary.PrismSyncV2Whitelisted
		m["sequence_step_ms"] = primary.SequenceStepMs
		m["device_card_app"] = primary.DeviceCardApp
	}
	return m
}

// devicesJSON lists the user's devices: everything connected plus
// devices seen before.
func (e *Engine) devicesJSON(includeDisconnected bool) []map[string]any {
	e.mu.Lock()
	live := make(map[int]*liveDevice, len(e.live))
	for k, v := range e.live {
		live[k] = v
	}
	e.mu.Unlock()
	out := []map[string]any{}
	seen := map[int]bool{}
	for id, l := range live {
		seen[id] = true
		out = append(out, e.deviceJSON(l.cat, l))
	}
	if includeDisconnected {
		rows, err := e.db.Query(`SELECT device_id FROM users_devices WHERE user_id = ?`, e.userID)
		if err == nil {
			var ids []int
			for rows.Next() {
				var id int
				rows.Scan(&id)
				ids = append(ids, id)
			}
			rows.Close()
			for _, id := range ids {
				if seen[id] {
					continue
				}
				if cat, err := e.catalogByID(id); err == nil {
					out = append(out, e.deviceJSON(cat, nil))
				}
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i]["id"].(int) < out[j]["id"].(int) })
	return out
}

// DeviceFamilies backs the GG core's /v1/loadouts/devicesFamily.
func (e *Engine) DeviceFamilies() []map[string]any {
	out := []map[string]any{}
	for _, d := range e.devicesJSON(true) {
		out = append(out, map[string]any{"id": fmt.Sprint(d["id"]), "deviceFamily": d["deviceTypeName"], "name": d["name"]})
	}
	return out
}

// PrismDevices lists connected devices that take part in PrismSync.
func (e *Engine) PrismDevices() []map[string]any {
	out := []map[string]any{}
	for _, d := range e.devicesJSON(false) {
		if d["supports_prism_sync"] == true || d["prismsync_v2_whitelisted"] == true {
			out = append(out, map[string]any{"id": d["id"], "name": d["name"], "full_name": d["full_name"],
				"type": d["type"], "deviceTypeName": d["deviceTypeName"], "product_id": d["product_id"], "isEnabled": true})
		}
	}
	return out
}

// loadoutDevicesJSON renders loadouts.LoadoutDevice (camelCase), used by
// the QuickSet / loadout views.
func (e *Engine) loadoutDevicesJSON(connectedOnly bool) []map[string]any {
	out := []map[string]any{}
	for _, d := range e.devicesJSON(!connectedOnly) {
		meta := []map[string]any{}
		for _, c := range d["configuration_metadata"].([]map[string]any) {
			meta = append(meta, map[string]any{
				"id": c["id"], "name": c["name"], "deviceId": c["device_id"], "lastDeployed": c["last_deployed"],
				"active": c["active"], "isQuickSetAutoDeletionOk": c["is_quickset_auto_deletion_ok"] == 1,
				"quickSetAutoCreatedForProfile": c["quickset_auto_created_for_profile"],
			})
		}
		out = append(out, map[string]any{
			"id": d["id"], "productId": d["product_id"], "name": d["name"], "fullName": d["full_name"],
			"displayName": d["display_name"], "type": d["type"], "deviceTypeName": d["deviceTypeName"],
			"connected": d["connected"], "configurationMetadata": meta, "imageUrl": d["imageUrl"],
			"hideDeviceCard": d["hide_device_card"], "hideDeviceConfigLink": false, "needsFirmwareUpdate": false,
			"childDeviceName": "", "firstConnectionTime": d["firstConnectionTime"],
			"prismSyncV2Whitelisted": d["prismsync_v2_whitelisted"],
		})
	}
	return out
}
