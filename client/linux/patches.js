'use strict';

const fs = require('fs');
const path = require('path');
const { fileURLToPath, pathToFileURL } = require('url');

// Source patches for the client's webpack bundles, applied in memory at load
// time. Each patch is optional: if the pattern is not found (for example
// after a GG update) we log and keep going with the unpatched code.

const PATCHES = [
  {
    file: 'main/index.js',
    name: 'data directory',
    // getDataDir(): `${PROGRAMDATA}\SteelSeries\GG` on Windows.
    find: /:process\.env\.PROGRAMDATA\?`\$\{process\.env\.PROGRAMDATA\}\\\\SteelSeries\\\\GG`:`[^`]*`/,
    replace: ':process.env.GG_LINUX_DATA_DIR',
  },
  {
    file: 'main/index.js',
    name: 'files under the data directory',
    // e.g. `${process.env.PROGRAMDATA}\SteelSeries\GG\windowProps.json`
    find: /`\$\{process\.env\.PROGRAMDATA\}\\\\SteelSeries\\\\GG\\\\([A-Za-z0-9_.-]+)`/g,
    replace: '(process.env.GG_LINUX_DATA_DIR+"/$1")',
  },
  {
    file: 'main/index.js',
    name: 'audio meter',
    // The native AudioMeter loads only on Windows; Linux has audiometer.js.
    find: /if\("win32"!==process\.platform\)throw new Error\("unsupported platform for audio meter"\)/,
    replace: 'if(global.ggLinuxAudioMeter)return global.ggLinuxAudioMeter.AudioMeter;$&',
  },
  {
    file: 'main/index.js',
    name: 'capture audio meter',
    find: /if\("win32"!==process\.platform\)throw new Error\("unsupported platform for capture audio meter"\)/,
    replace: 'if(global.ggLinuxAudioMeter)return global.ggLinuxAudioMeter.CaptureAudioMeter;$&',
  },
  {
    file: 'main/index.js',
    name: 'drag out',
    // Moments' share dialogs drag a clip out with webContents.startDrag.
    // Chromium cannot deliver the file to the app it is dropped on under
    // Wayland; the drag fix (packaging/dragfix.c, preloaded by the
    // launcher) delivers it, from this note of which file is dragged.
    find: /e\.sender\.startDrag\(\{file:n,icon:i\}\)/,
    replace: '(global.ggLinuxDragOut&&global.ggLinuxDragOut(n)),e.sender.startDrag({file:n,icon:i})',
  },
  {
    file: 'main/preload.js',
    name: 'platform',
    // The pages check window.api.os.platform() for "win32" or "darwin" and
    // hide what neither supports, Sonar included. gg-linux stands in for the
    // Windows services, so the pages see Windows.
    find: /platform:\(\)=>process\.platform/,
    replace: 'platform:()=>"linux"===process.platform?"win32":process.platform',
  },
  {
    file: 'render',
    marker: 'SET_DIM_FACTOR_ON_MONITOR_SLEEP',
    name: 'monitor sleep dimming: note the dropdown changed',
    // The deploy saga below runs for every edit in the lighting palette;
    // the dimming alone is sent for zones with different effects only when
    // the "When Monitor Sleeps" dropdown changed it (its action creator
    // notes that), so another edit doesn't overwrite zones' own dimming.
    find: /const ([\w$]+)="SET_DIM_FACTOR_ON_MONITOR_SLEEP";function ([\w$]+)\(e\)\{return\{type:\1,dimFactor:e\}\}/,
    replace: 'const $1="SET_DIM_FACTOR_ON_MONITOR_SLEEP";function $2(e){return globalThis.ggLinuxDimSet=!0,{type:$1,dimFactor:e}}',
  },
  {
    file: 'render',
    marker: 'No zones selected, skipping deployment',
    name: 'monitor sleep dimming for zones with different effects',
    // Prism's "When Monitor Sleeps" is saved with an effect: the deploy
    // saga sends it only when the selected zones share one effect and
    // otherwise drops the change ("No zones selected, skipping
    // deployment"), so a configuration with several effects (all zones
    // selected, as the page opens) never keeps it. In that case send the
    // dimming alone for the selected zones; ggd keeps each zone's effect.
    find: /([\w$]+)\.length>0&&1===([\w$]+)\.length\?(yield\(0,[\w$]+\.[\w$]+\)\(\(0,[\w$]+\.[\w$]+\)\(\{effectType:\2\[0\],layer:([\w$]+),[^}]*?zoneIds:\1,[^}]*?dimFactorOnMonitorSleep:([\w$]+)\}\)\)):(console\.info\("No zones selected, skipping deployment"\))/,
    replace: (src, all, zones, effects, deploy, layer, dim, skip) => {
      // The function that PUTs a configuration update on a layer.
      const send = /function ([\w$]+)\(e\)\{switch\(e\.layer\)\{case [\w$]+\.[\w$]+\.Active:return\(0,[\w$]+\.[\w$]+\)\(e\.configData,e\.selectionData\)/.exec(src);
      if (!send) {
        console.warn('[gg-linux] patch "monitor sleep dimming" found no update function; continuing unpatched');
        return all;
      }
      // The update function must be the saga's own (typeof, should a
      // future bundle put it out of the saga's reach), and the dimming is
      // sent only when the dropdown set it (see the patch above).
      return (
        `${zones}.length>0&&1===${effects}.length?(globalThis.ggLinuxDimSet=!1,${deploy}):` +
        `${zones}.length>0&&${effects}.length>1&&("ACTIVE"===${layer}||"IDLE"===${layer})&&` +
        `globalThis.ggLinuxDimSet&&"function"==typeof ${send[1]}?(globalThis.ggLinuxDimSet=!1,` +
        `yield ${send[1]}({layer:${layer},configData:{dimFactorOnMonitorSleep:${dim}},` +
        `selectionData:{deviceSelection:{mode:"crossDevice"},zoneSelection:{zoneIds:${zones},mode:"ids"}}})` +
        `.catch((x)=>console.error("[gg-linux] monitor sleep dimming not saved",x))):${skip}`
      );
    },
  },
];

// Code added to the client's preload, which runs in each of its pages.
const PRELOAD_ADDITIONS = `
// gg-linux: the PrismSync tile in Engine > Apps opens PrismSync's older
// editor, which the Engine on Linux does not run. The Prism tab is its
// editor here, so "Configure" goes there.
window.addEventListener('click', (e) => {
  const tile = '.app-prismSync';
  if (!(e.target instanceof Element) || !e.target.closest(tile + ' .appConfigButton, ' + tile + ' .appHeaderClickArea')) return;
  e.stopImmediatePropagation();
  e.preventDefault();
  location.hash = '#/gg/bloom';
}, true);
`;

function apply(src, file) {
  for (const p of PATCHES) {
    // Render patches find their webpack chunk, whose name changes between
    // GG builds, by a marker in it.
    if (p.file === 'render' ? !file.startsWith('render/') || !src.includes(p.marker) : p.file !== file) continue;
    if (!p.find.test(src)) {
      console.warn(`[gg-linux] patch "${p.name}" did not match ${file}; continuing unpatched`);
      continue;
    }
    p.find.lastIndex = 0;
    const whole = src;
    src = src.replace(p.find, typeof p.replace === 'function' ? (...m) => p.replace(whole, ...m) : p.replace);
  }
  if (file === 'main/preload.js') src += PRELOAD_ADDITIONS;
  return src;
}

// patchedFile writes a patched copy of a client file the client loads by
// path (the preload) into dir and returns the copy's path.
const written = new Map();
function patchedFile(appDir, file, dir) {
  if (!written.has(file)) {
    const src = apply(fs.readFileSync(path.join(appDir, file), 'utf8'), file);
    const out = path.join(dir, 'patched-' + path.basename(file));
    fs.mkdirSync(dir, { recursive: true });
    fs.writeFileSync(out, src);
    written.set(file, out);
  }
  return written.get(file);
}

// patchedRenderURL returns the file: URL of a patched copy of the render
// script at url (written into dir), or null when no patch changes it. The
// pages load their scripts from file: URLs, which are sent there instead.
const renderCopies = new Map();
function patchedRenderURL(appDir, url, dir) {
  if (!url.startsWith('file:')) return null;
  let file;
  try {
    file = path.relative(appDir, fileURLToPath(url));
  } catch {
    return null;
  }
  if (!file.startsWith('render' + path.sep) || !file.endsWith('.js') || file.includes('..')) return null;
  file = file.split(path.sep).join('/');
  if (!renderCopies.has(file)) {
    let copy = null;
    try {
      const src = fs.readFileSync(path.join(appDir, file), 'utf8');
      const out = apply(src, file);
      if (out !== src) {
        copy = path.join(dir, 'patched-' + file.replace(/\//g, '-'));
        fs.mkdirSync(dir, { recursive: true });
        fs.writeFileSync(copy, out);
        copy = pathToFileURL(copy).href;
      }
    } catch (err) {
      console.warn(`[gg-linux] could not patch ${file}:`, err.message);
    }
    renderCopies.set(file, copy);
  }
  return renderCopies.get(file);
}

module.exports = { apply, patchedFile, patchedRenderURL, PATCHES };
