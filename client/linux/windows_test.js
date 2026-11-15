'use strict';

// Run with: node --test client/linux/windows_test.js
const test = require('node:test');
const assert = require('node:assert');
const Module = require('module');

// A stand-in for electron's BrowserWindow that records setResizable calls.
class FakeBrowserWindow {
  constructor() {
    this.resizable = true;
    this.calls = [];
  }
  isResizable() {
    return this.resizable;
  }
  setResizable(v) {
    this.calls.push(v);
    this.resizable = v;
  }
  isVisible() {
    return false;
  }
  isDestroyed() {
    return false;
  }
  on() {}
  once() {}
  maximize() {}
}
const fakeElectron = { BrowserWindow: FakeBrowserWindow, WebContentsView: class {}, app: {} };

const load = Module._load;
Module._load = function (request, ...rest) {
  return request === 'electron' ? fakeElectron : load.call(this, request, ...rest);
};
const { clientElectron, floatsFixedSize } = require('./windows');
Module._load = load;

test('floatsFixedSize', () => {
  assert.equal(floatsFixedSize({ HYPRLAND_INSTANCE_SIGNATURE: 'x', XDG_CURRENT_DESKTOP: 'Hyprland' }), true);
  assert.equal(floatsFixedSize({ SWAYSOCK: '/run/user/1000/sway-ipc.sock' }), true);
  assert.equal(floatsFixedSize({ XDG_CURRENT_DESKTOP: 'sway' }), true);
  assert.equal(floatsFixedSize({ XDG_CURRENT_DESKTOP: 'KDE' }), false);
  assert.equal(floatsFixedSize({ XDG_CURRENT_DESKTOP: 'ubuntu:GNOME' }), false);
  assert.equal(floatsFixedSize({}), false);
});

function withEnv(env, fn) {
  const keys = ['HYPRLAND_INSTANCE_SIGNATURE', 'SWAYSOCK', 'XDG_CURRENT_DESKTOP'];
  const saved = Object.fromEntries(keys.map((k) => [k, process.env[k]]));
  for (const k of keys) delete process.env[k];
  Object.assign(process.env, env);
  try {
    return fn();
  } finally {
    for (const k of keys) {
      if (saved[k] === undefined) delete process.env[k];
      else process.env[k] = saved[k];
    }
  }
}

test('secondary windows stay resizable on KDE', () => {
  const w = withEnv({ XDG_CURRENT_DESKTOP: 'KDE' }, () => new clientElectron.BrowserWindow({}));
  assert.deepEqual(w.calls, []);
  assert.equal(w.isResizable(), true);
});

test('secondary windows map pinned on Hyprland', () => {
  const w = withEnv({ HYPRLAND_INSTANCE_SIGNATURE: 'x' }, () => new clientElectron.BrowserWindow({}));
  assert.deepEqual(w.calls, [false]);
});

test('the main window is never pinned', () => {
  const opts = { webPreferences: { additionalArguments: ['isMainWindow=true'] } };
  const w = withEnv({ HYPRLAND_INSTANCE_SIGNATURE: 'x' }, () => new clientElectron.BrowserWindow(opts));
  assert.deepEqual(w.calls, []);
});
