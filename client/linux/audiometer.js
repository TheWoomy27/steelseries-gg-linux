'use strict';

// Linux stand-in for GG's native audio meter module (audioMeter.node), which
// reads and sets Windows endpoint volumes and peak meters. The client's main
// process builds its AudioMeter and CaptureAudioMeter from here instead.
//
// Device ids are PipeWire node names, as Sonar's API gives them.
//
// - Volumes are the nodes' own (channelVolumes and mute) on the cubic scale
//   desktop volume controls show. One `pw-dump -m` follows them while a meter
//   is open, and wpctl sets them.
// - Peaks come from pw-record streams in PipeWire's peak mode, as
//   pavucontrol's meters: 25 frames a second, each the peak of its 40 ms.
//   Sinks are metered through their monitors. The streams are passive, so a
//   meter neither wakes a suspended device nor keeps it running.
// - A session meter (one of Sonar's channel streams on a device, by the
//   session GUIDs the mixer builds) meters that Sonar channel's own device,
//   before its volume, as the mixer scales the peak by the channel volume.

const { spawn, execFile } = require('child_process');

const PEAK_RATE = 25;
// A peak older than this is from a stream that stopped running.
const PEAK_STALE_MS = 150;
const DUMP_IDLE_MS = 5000;

// Sonar's channel devices by the role numbers in session GUIDs.
const SESSION_ROLES = { 1: 'sonar_chat', 2: 'sonar_game', 3: 'sonar_mic', 5: 'sonar_media', 6: 'sonar_aux' };
const SESSION_GUID = /^1e8a0([0-9a-f]+)0\d-a28b-41c0-9baf-328f7bef863a$/i;

function sessionDevice(sessionId) {
  const m = SESSION_GUID.exec(sessionId || '');
  return m ? SESSION_ROLES[parseInt(m[1], 16)] : undefined;
}

// Nodes by name, from pw-dump: { id, sink, volume, muted }.
const nodes = new Map();
const namesById = new Map();
const meters = new Set();
let dump = null;
let dumpReady = false;
let dumpStop = null;

function nodeVolume(params) {
  for (const p of (params && params.Props) || []) {
    if (Array.isArray(p.channelVolumes) && p.channelVolumes.length) {
      const avg = p.channelVolumes.reduce((a, b) => a + b, 0) / p.channelVolumes.length;
      return { volume: Math.cbrt(avg), muted: !!p.mute };
    }
  }
  return undefined;
}

function update(o) {
  if (!o.info) {
    const name = namesById.get(o.id);
    namesById.delete(o.id);
    if (name && nodes.get(name)?.id === o.id) {
      nodes.delete(name);
      // PipeWire reuses ids, so the device may return under the same one.
      const s = peaks.get(name);
      if (s) s.failedId = undefined;
    }
    return;
  }
  if (o.type !== 'PipeWire:Interface:Node') return;
  const props = o.info.props || {};
  const name = props['node.name'] || namesById.get(o.id);
  if (!name) return;
  namesById.set(o.id, name);
  const old = nodes.get(name);
  const node = { id: o.id, sink: old?.sink, volume: old?.volume, muted: old?.muted };
  if (props['media.class']) node.sink = props['media.class'].startsWith('Audio/Sink');
  const v = nodeVolume(o.info.params);
  if (v) Object.assign(node, v);
  nodes.set(name, node);
  if (dumpReady && v && (!old || old.volume !== v.volume || old.muted !== v.muted)) {
    for (const m of meters) if (m.device === name) m.volumeChanged();
  }
}

function startDump() {
  if (dumpStop) {
    clearTimeout(dumpStop);
    dumpStop = null;
  }
  if (dump) return;
  const p = spawn('pw-dump', ['-m', '-N'], { stdio: ['ignore', 'pipe', 'ignore'] });
  dump = p;
  let buf = '';
  p.stdout.setEncoding('utf8');
  p.stdout.on('data', (chunk) => {
    buf += chunk;
    // pw-dump prints each batch of changes as a JSON array whose closing
    // bracket is alone on its line.
    for (let end; (end = buf.indexOf('\n]\n')) >= 0; ) {
      const text = buf.slice(0, end + 2);
      buf = buf.slice(end + 3);
      let objs;
      try {
        objs = JSON.parse(text);
      } catch (err) {
        console.warn('[gg-linux] audio meter: reading pw-dump:', err.message);
        continue;
      }
      objs.forEach(update);
      if (!dumpReady) {
        dumpReady = true;
        for (const m of meters) m.volumeChanged();
      }
      for (const m of meters) m.peak?.connect();
    }
  });
  p.on('error', (err) => console.warn('[gg-linux] audio meter: pw-dump:', err.message));
  p.on('exit', () => {
    if (dump !== p) return;
    dump = null;
    dumpReady = false;
    nodes.clear();
    namesById.clear();
    // Restart unless the last meter closed.
    if (meters.size) setTimeout(() => meters.size && startDump(), 1000);
  });
}

function releaseDump() {
  if (meters.size || dumpStop) return;
  dumpStop = setTimeout(() => {
    dumpStop = null;
    if (!meters.size && dump) {
      const p = dump;
      dump = null;
      dumpReady = false;
      nodes.clear();
      namesById.clear();
      p.kill();
    }
  }, DUMP_IDLE_MS);
}

// Peak streams, shared by the meters on a device.
const peaks = new Map();

class PeakStream {
  constructor(device) {
    this.device = device;
    this.users = 0;
    this.value = 0;
    this.at = 0;
    this.proc = null;
    this.nodeId = undefined;
    this.failedId = undefined;
  }

  // connect starts the stream once the device is known, and again after it
  // returns. pw-record falls back to the default device for a missing
  // target, so it is never started without one.
  connect() {
    const node = nodes.get(this.device);
    if (this.proc || !this.users || !node || node.sink === undefined || node.id === this.failedId) return;
    this.nodeId = node.id;
    const props = [
      'resample.peaks = true',
      'node.passive = true',
      'node.dont-reconnect = true',
      'stream.monitor = true',
      'node.name = gg_audio_meter',
      'media.name = "GG audio meter"',
    ];
    if (node.sink) props.push('stream.capture.sink = true');
    // pw-record's output is fully buffered on a pipe; stdbuf makes each
    // frame arrive as it is measured.
    const p = spawn(
      'stdbuf',
      ['-o0', 'pw-record', '-a', '--rate', String(PEAK_RATE), '--channels', '2', '--format', 'f32', '--target', this.device, '-P', `{ ${props.join(' ')} }`, '-'],
      { stdio: ['ignore', 'pipe', 'ignore'] },
    );
    this.proc = p;
    let rest = Buffer.alloc(0);
    p.stdout.on('data', (chunk) => {
      const b = rest.length ? Buffer.concat([rest, chunk]) : chunk;
      const frames = Math.floor(b.length / 8);
      for (let i = 0; i < frames; i++) {
        this.value = Math.max(Math.abs(b.readFloatLE(i * 8)), Math.abs(b.readFloatLE(i * 8 + 4)));
      }
      if (frames) this.at = Date.now();
      rest = b.subarray(frames * 8);
    });
    p.on('error', (err) => console.warn('[gg-linux] audio meter: pw-record:', err.message));
    p.on('exit', () => {
      if (this.proc !== p) return;
      this.proc = null;
      this.value = 0;
      // The device went away (or the stream failed on it): connect() starts
      // again once the device is a new node.
      this.failedId = this.nodeId;
    });
  }

  peak() {
    return Date.now() - this.at < PEAK_STALE_MS ? this.value : 0;
  }

  close() {
    if (--this.users > 0) return;
    peaks.delete(this.device);
    if (this.proc) {
      const p = this.proc;
      this.proc = null;
      p.kill();
    }
  }
}

function openPeak(device) {
  let s = peaks.get(device);
  if (!s) {
    s = new PeakStream(device);
    peaks.set(device, s);
  }
  s.users++;
  s.connect();
  return s;
}

// wpctl runs one at a time per node; a slider's stream of changes collapses
// to the latest value.
const pending = new Map();

function wpctl(device, verb, value) {
  const key = `${device} ${verb}`;
  const busy = pending.has(key);
  pending.set(key, value);
  if (busy) return;
  const run = () => {
    const v = pending.get(key);
    const node = nodes.get(device);
    if (!node) {
      pending.delete(key);
      return;
    }
    execFile('wpctl', [verb, String(node.id), String(v)], (err) => {
      if (err) console.warn(`[gg-linux] audio meter: wpctl ${verb}:`, err.message);
      if (pending.get(key) === v) pending.delete(key);
      else run();
    });
  };
  run();
}

class AudioMeter {
  constructor(deviceId, onVolume, sessionId) {
    const session = sessionDevice(sessionId);
    this.session = session !== undefined;
    this.device = session || deviceId;
    this.onVolume = onVolume;
    meters.add(this);
    startDump();
    this.peak = openPeak(this.device);
  }

  volumeChanged() {
    if (!this.session && this.onVolume) this.onVolume(this.getVolume());
  }

  getPeak() {
    return this.peak ? this.peak.peak() : 0;
  }

  // A session meter's volume would be Sonar's stream on the device, which
  // is always at full volume; the mixer does not read it.
  getVolume() {
    if (this.session) return { volume: 1, muted: false };
    const node = nodes.get(this.device);
    return node && node.volume !== undefined ? { volume: node.volume, muted: node.muted } : { volume: 0, muted: false };
  }

  setVolume({ volume, muted }) {
    if (this.session) return;
    if (typeof volume === 'number') wpctl(this.device, 'set-volume', Math.max(0, Math.min(1, volume)).toFixed(4));
    if (typeof muted === 'boolean') wpctl(this.device, 'set-mute', muted ? 1 : 0);
  }

  close() {
    if (!meters.delete(this)) return;
    this.peak.close();
    this.peak = null;
    releaseDump();
  }
}

class CaptureAudioMeter extends AudioMeter {
  constructor(deviceId) {
    super(deviceId);
  }
}

module.exports = { AudioMeter, CaptureAudioMeter, sessionDevice };
