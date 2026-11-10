'use strict';

// Window placement on tiling compositors. GG's main window tiles like any
// other app, but its secondary windows (device configuration pages and the
// like) are laid out for the size GG opens them at. Hyprland and sway float a
// new toplevel that has a parent or whose minimum and maximum sizes are equal
// when it first maps. Electron only passes window parents to X11, so
// secondary windows map non-resizable (on Linux, Electron pins the minimum
// and maximum size for that) and become resizable once the compositor has
// placed them. Stacking window managers just place them as usual.
//
// The same classes give the client's windows and views the patched copy of
// its preload (see patches.js).

const path = require('path');
const electron = require('electron');
const patches = require('./patches');

// The compositor decides floating when the window first maps (its first
// frame), shortly after show().
const LOCK_MS = 1500;

// withPatchedPreload points a window or view at the patched copy of the
// client's preload (see patches.js).
function withPatchedPreload(options) {
  const preload = options.webPreferences?.preload;
  if (!preload) return options;
  const appDir = electron.app.getAppPath();
  const rel = path.relative(appDir, preload);
  if (rel.startsWith('..')) return options;
  const dir = path.join(electron.app.getPath('userData'), 'gg-linux');
  return { ...options, webPreferences: { ...options.webPreferences, preload: patches.patchedFile(appDir, rel, dir) } };
}

// Named BrowserWindow: BrowserWindow.getAllWindows() and getFocusedWindow()
// only return windows whose constructor has that name.
class BrowserWindow extends electron.BrowserWindow {
  #unlock = null;

  constructor(options = {}) {
    super(withPatchedPreload(options));
    // The client marks its main window through the preload arguments.
    const args = options.webPreferences?.additionalArguments || [];
    if (!args.includes('isMainWindow=true') && this.isResizable()) {
      this.#lockUntilMapped();
      // A window shown again after being hidden maps a new toplevel, which
      // the compositor places afresh.
      this.on('hide', () => {
        if (!this.isDestroyed() && !this.#unlock && this.isResizable()) this.#lockUntilMapped();
      });
    }
  }

  #lockUntilMapped() {
    let timer;
    this.setResizable(false);
    this.#unlock = () => {
      clearTimeout(timer);
      this.#unlock = null;
      if (!this.isDestroyed()) this.setResizable(true);
    };
    const schedule = () => {
      timer = setTimeout(() => this.#unlock?.(), LOCK_MS);
    };
    if (this.isVisible()) schedule();
    else this.once('show', schedule);
  }

  // GG maximizes a window it reopens when the window was maximized when it
  // closed, and tiling compositors report tiled windows as maximized, so a
  // secondary window that was ever tiled would come back tiled. While a
  // new secondary window is being placed, that is left to the compositor.
  maximize() {
    if (this.#unlock) return;
    super.maximize();
  }
}

class WebContentsView extends electron.WebContentsView {
  constructor(options = {}) {
    super(withPatchedPreload(options));
  }
}

// The electron module as the client sees it: the same API with these classes
// replaced (electron's own exports cannot be redefined).
const replaced = { BrowserWindow, WebContentsView };
const clientElectron = new Proxy(electron, {
  get(target, key, receiver) {
    return Object.hasOwn(replaced, key) ? replaced[key] : Reflect.get(target, key, receiver);
  },
});

module.exports = { clientElectron };
