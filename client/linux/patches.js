'use strict';

const fs = require('fs');
const path = require('path');

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
    if (p.file !== file) continue;
    if (!p.find.test(src)) {
      console.warn(`[gg-linux] patch "${p.name}" did not match ${file}; continuing unpatched`);
      continue;
    }
    src = src.replace(p.find, p.replace);
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

module.exports = { apply, patchedFile, PATCHES };
