'use strict';

// Run with: node --test client/linux/patches_test.js
const test = require('node:test');
const assert = require('node:assert');
const fs = require('fs');
const os = require('os');
const path = require('path');
const { pathToFileURL } = require('url');
const patches = require('./patches');

// The deploy saga and the update function from GG's render chunk (249.js),
// as shipped.
const CHUNK =
  'function So(e){switch(e.layer){case sr.y9.Active:return(0,nr.L1)(e.configData,e.selectionData);case sr.y9.Idle:return(0,nr.EE)(e.configData,e.selectionData);default:throw new Error("layer")}}' +
  'function*Vo(){const e=yield(0,a.Lt)(ar._w),t=yield(0,a.Lt)(ar.ud),i=yield(0,a.Lt)(ar.tP),n=yield(0,a.Lt)(ar.I_),r=yield(0,a.Lt)(ar.YC),o=yield(0,a.Lt)(ar.jM),s=yield(0,a.Lt)(ar.sC),d=yield(0,a.Lt)(ar.Cl),c=yield(0,a.Lt)(Wr.WP),l=yield(0,a.Lt)(Wr.FF),u=yield(0,a.Lt)(ar.SY),g=yield(0,a.Lt)(ar.Xk),f=yield(0,a.Lt)(ar.kf);' +
  'c.length>0&&1===t.length?yield(0,a.yJ)((0,or.jh)({effectType:t[0],layer:e,color:s,colors:o,zoneIds:c,duration:n,size:r,waveDirection:i,presetId:d[0],deviceCenterCoordinates:l,samplingArea:u[0],colorProfileId:g[0],dimFactorOnMonitorSleep:f})):console.info("No zones selected, skipping deployment")}';

// run steps the saga in src with the given store state and returns the
// actions it put and the updates it sent.
function run(src, state) {
  const sent = [];
  const env = {
    sr: { y9: { Active: 'ACTIVE', Idle: 'IDLE' } },
    nr: {
      L1: (configData, selectionData) => (sent.push({ layer: 'base', configData, selectionData }), Promise.resolve()),
      EE: (configData, selectionData) => (sent.push({ layer: 'idle', configData, selectionData }), Promise.resolve()),
    },
    // Lt (select) yields the selector; yJ (put) the action.
    a: { Lt: (sel) => ({ select: sel }), yJ: (action) => ({ put: action }) },
    or: { jh: (x) => x },
    ar: { _w: 'layer', ud: 'effects', tP: '-', I_: '-', YC: '-', jM: '-', sC: '-', Cl: 'list', SY: 'list', Xk: 'list', kf: 'dim' },
    Wr: { WP: 'zones', FF: '-' },
  };
  const Vo = new Function(...Object.keys(env), src + ';return Vo')(...Object.values(env));
  const values = { ...state, list: [], '-': undefined };
  const puts = [];
  const it = Vo();
  for (let r = it.next(); !r.done; ) {
    const v = r.value;
    if (v && 'select' in v) r = it.next(values[v.select]);
    else {
      if (v && 'put' in v) puts.push(v.put);
      r = it.next();
    }
  }
  return { puts, sent };
}

test('the render patch matches GG\'s deploy saga', () => {
  const out = patches.apply(CHUNK, 'render/249.js');
  assert.notStrictEqual(out, CHUNK);
  assert.match(out, /yield So\(\{layer:e,configData:\{dimFactorOnMonitorSleep:f\}/);
});

test('one effect selected deploys it as before', () => {
  const out = patches.apply(CHUNK, 'render/249.js');
  const { puts, sent } = run(out, { layer: 'ACTIVE', effects: ['SINGLE_COLOR'], zones: ['1'], dim: 255 });
  assert.strictEqual(sent.length, 0);
  assert.strictEqual(puts.length, 1);
  assert.strictEqual(puts[0].dimFactorOnMonitorSleep, 255);
  assert.deepStrictEqual(puts[0].zoneIds, ['1']);
});

// The action creator the "When Monitor Sleeps" dropdown dispatches through,
// as shipped (render/index.js).
const ACTIONS = 'const K="SET_DIM_FACTOR_ON_MONITOR_SLEEP";function W(e){return{type:K,dimFactor:e}}';

test('the dropdown\'s action creator notes the change', () => {
  const out = patches.apply(ACTIONS, 'render/index.js');
  const W = new Function(out + ';return W')();
  globalThis.ggLinuxDimSet = false;
  assert.deepStrictEqual(W(255), { type: 'SET_DIM_FACTOR_ON_MONITOR_SLEEP', dimFactor: 255 });
  assert.strictEqual(globalThis.ggLinuxDimSet, true);
});

test('several effects selected send the dimming alone when the dropdown changed it', () => {
  const out = patches.apply(CHUNK, 'render/249.js');
  globalThis.ggLinuxDimSet = true;
  const { sent } = run(out, { layer: 'ACTIVE', effects: ['SINGLE_COLOR', 'OFF'], zones: ['1', '2'], dim: 255 });
  assert.deepStrictEqual(sent, [
    {
      layer: 'base',
      configData: { dimFactorOnMonitorSleep: 255 },
      selectionData: { deviceSelection: { mode: 'crossDevice' }, zoneSelection: { zoneIds: ['1', '2'], mode: 'ids' } },
    },
  ]);
  // Once only, and another edit (a colour) sends nothing: the zones keep
  // their own dimming.
  assert.strictEqual(globalThis.ggLinuxDimSet, false);
  assert.strictEqual(run(out, { layer: 'ACTIVE', effects: ['SINGLE_COLOR', 'OFF'], zones: ['1', '2'], dim: 255 }).sent.length, 0);
  // Unpatched, nothing is sent.
  globalThis.ggLinuxDimSet = true;
  assert.strictEqual(run(CHUNK, { layer: 'ACTIVE', effects: ['SINGLE_COLOR', 'OFF'], zones: ['1', '2'], dim: 255 }).sent.length, 0);
  globalThis.ggLinuxDimSet = false;
});

test('an update function out of the saga\'s reach is not called', () => {
  // A bundle where the matched function is in another module: the saga
  // must not throw (it would stop saving the editor's changes).
  const out = patches.apply(CHUNK, 'render/249.js').replace(/^function So\(e\)\{.*?\}\}/, '');
  globalThis.ggLinuxDimSet = true;
  assert.doesNotThrow(() => run(out, { layer: 'ACTIVE', effects: ['SINGLE_COLOR', 'OFF'], zones: ['1', '2'], dim: 255 }));
  globalThis.ggLinuxDimSet = false;
});

test('no zones or the reactive layer send nothing', () => {
  const out = patches.apply(CHUNK, 'render/249.js');
  assert.strictEqual(run(out, { layer: 'ACTIVE', effects: ['SINGLE_COLOR', 'OFF'], zones: [], dim: 255 }).sent.length, 0);
  assert.strictEqual(run(out, { layer: 'REACTIVE', effects: ['FADE', 'RIPPLE'], zones: ['1'], dim: 255 }).sent.length, 0);
});

test('render scripts load from a patched copy only when a patch changes them', (t) => {
  const app = fs.mkdtempSync(path.join(os.tmpdir(), 'gg-app-'));
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'gg-patched-'));
  t.after(() => {
    fs.rmSync(app, { recursive: true, force: true });
    fs.rmSync(dir, { recursive: true, force: true });
  });
  fs.mkdirSync(path.join(app, 'render'));
  fs.writeFileSync(path.join(app, 'render', '249.js'), CHUNK);
  fs.writeFileSync(path.join(app, 'render', '542.js'), 'var x=1');
  const url = patches.patchedRenderURL(app, pathToFileURL(path.join(app, 'render', '249.js')).href, dir);
  assert.ok(url && url.startsWith('file:'));
  assert.match(fs.readFileSync(new URL(url), 'utf8'), /yield So\(/);
  assert.strictEqual(patches.patchedRenderURL(app, pathToFileURL(path.join(app, 'render', '542.js')).href, dir), null);
  assert.strictEqual(patches.patchedRenderURL(app, pathToFileURL(path.join(app, 'main', 'index.js')).href, dir), null);
  assert.strictEqual(patches.patchedRenderURL(app, 'https://example.com/render/249.js', dir), null);
});
