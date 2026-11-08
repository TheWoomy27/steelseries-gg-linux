'use strict';

// Window placement on tiling compositors. GG's main window tiles like any
// other app, but its secondary windows (device configuration pages and the
// like) are laid out for the size GG opens them at. Hyprland and sway float a
// new toplevel that has a parent or whose minimum and maximum sizes are equal
// when it first maps. Electron only passes window parents to X11, so
// secondary windows map non-resizable (on Linux, Electron pins the minimum
// and maximum size for that) and become resizable once the compositor has
// placed them. Stacking window managers just place them as usual.

const electron = require('electron');

// The compositor decides floating when the window first maps (its first
// frame), shortly after show().
const LOCK_MS = 1500;

// Named BrowserWindow: BrowserWindow.getAllWindows() and getFocusedWindow()
// only return windows whose constructor has that name.
class BrowserWindow extends electron.BrowserWindow {
  #unlock = null;

  constructor(options = {}) {
    super(options);
    // The client marks its main window through the preload arguments.
    const args = options.webPreferences?.additionalArguments || [];
    if (!args.includes('isMainWindow=true') && this.isResizable()) this.#lockUntilMapped();
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

  maximize() {
    this.#unlock?.();
    super.maximize();
  }
}

// The electron module as the client sees it: the same API with BrowserWindow
// replaced (electron's own export cannot be redefined).
const clientElectron = new Proxy(electron, {
  get(target, key, receiver) {
    return key === 'BrowserWindow' ? BrowserWindow : Reflect.get(target, key, receiver);
  },
});

module.exports = { clientElectron };
