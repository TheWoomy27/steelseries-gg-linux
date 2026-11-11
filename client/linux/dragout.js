'use strict';

// Dragging a clip out of GG (Moments' share dialogs). GG starts the drag
// with webContents.startDrag({file}); under Wayland Chromium offers the
// file as text/uri-list but cannot deliver it ("Cannot deliver data of
// type text/uri-list"), so nothing drops. The drag fix
// (packaging/dragfix.c), which the launcher preloads into Electron,
// answers that request itself with the file noted here for the drag.

const fs = require('fs');
const path = require('path');

const note = path.join(process.env.XDG_RUNTIME_DIR || '/tmp', `gg-linux-drag-${process.pid}`);
let timer = null;

// The drag fix is needed in GG's own process only: keep it out of what GG
// starts (apps and scripts launched from bindings).
if (process.env.LD_PRELOAD) {
  const rest = process.env.LD_PRELOAD.split(/[: ]/).filter((p) => p && !p.includes('libgg-dragfix'));
  if (rest.length) process.env.LD_PRELOAD = rest.join(':');
  else delete process.env.LD_PRELOAD;
}

function fixLoaded() {
  try {
    return fs.readFileSync('/proc/self/maps', 'utf8').includes('libgg-dragfix');
  } catch {
    return false;
  }
}

// noteDrag records the file being dragged; the note goes a while after the
// last drag.
function noteDrag(file) {
  try {
    fs.writeFileSync(note, file);
  } catch (err) {
    console.error('[gg-linux] drag out note:', err);
  }
  console.log('[gg-linux] drag out', file, fixLoaded() ? '(drag fix loaded)' : '(no drag fix)');
  clearTimeout(timer);
  timer = setTimeout(() => fs.rm(note, { force: true }, () => {}), 10 * 60 * 1000);
  timer.unref?.();
}

module.exports = { noteDrag };
