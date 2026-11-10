'use strict';

// Dragging a clip out of GG (Moments' share dialogs). GG starts the drag
// with webContents.startDrag({file}); on Wayland Chromium offers the file
// as text/uri-list but then cannot deliver it ("Cannot deliver data of
// type text/uri-list"), so the drop never lands. Instead the page's own
// drag carries the file: a script in the page takes the drag of GG's
// draggable file components before GG cancels it, and puts the file's
// URL on it (text/uri-list), which Chromium delivers and the receiving
// app (Discord, a browser) turns into a file.

const { app } = require('electron');

// PAGE_SCRIPT runs in GG's pages. React keeps the component's props on
// its fiber: the draggable element's component has `file` and
// `onDragStartFunc`.
const PAGE_SCRIPT = `(() => {
  if (window.__ggLinuxDragOut) return;
  window.__ggLinuxDragOut = true;
  const fileFor = (el) => {
    for (; el && el !== document; el = el.parentElement) {
      if (el.getAttribute && el.getAttribute('draggable') !== 'true') continue;
      const key = Object.keys(el).find((k) => k.startsWith('__reactFiber$'));
      for (let f = key && el[key]; f; f = f.return) {
        const p = f.memoizedProps;
        if (p && typeof p.file === 'string' && p.file && typeof p.onDragStartFunc === 'function') return p.file;
      }
    }
    return '';
  };
  window.addEventListener('dragstart', (ev) => {
    const file = fileFor(ev.target);
    if (!file || !ev.dataTransfer) return;
    const url = 'file://' + file.split('/').map(encodeURIComponent).join('/');
    ev.dataTransfer.setData('text/uri-list', url + '\\r\\n');
    ev.dataTransfer.setData('text/plain', file);
    ev.dataTransfer.effectAllowed = 'copy';
    // GG's own handler would cancel this drag for its native one.
    ev.stopImmediatePropagation();
    console.log('[gg-linux] drag out', url);
  }, true);
})();`;

function install() {
  app.on('web-contents-created', (_, wc) => {
    wc.on('dom-ready', () => {
      if (!wc.getURL().includes('/render/')) return;
      wc.executeJavaScript(PAGE_SCRIPT).catch((err) => console.error('[gg-linux] drag out script:', err));
    });
  });
}

module.exports = { install, PAGE_SCRIPT };
