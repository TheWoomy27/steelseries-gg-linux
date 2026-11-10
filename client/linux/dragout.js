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

// noteDrag records the file being dragged; the note goes a while after the
// last drag.
function noteDrag(file) {
  try {
    fs.writeFileSync(note, file);
  } catch (err) {
    console.error('[gg-linux] drag out note:', err);
  }
  console.log('[gg-linux] drag out', file, process.env.LD_PRELOAD ? '(drag fix loaded)' : '(no drag fix)');
  clearTimeout(timer);
  timer = setTimeout(() => fs.rm(note, { force: true }, () => {}), 10 * 60 * 1000);
  timer.unref?.();
}

module.exports = { noteDrag };
