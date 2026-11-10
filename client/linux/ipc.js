'use strict';

// Linux implementations for IPC channels whose Windows versions rely on
// native modules or Windows APIs. The client's main process registers its
// own handlers; we wrap ipcMain so ours win for the channels listed here.
// A wrapper gets the client's handler and decides when to call it.

const { ipcMain } = require('electron');

const overrides = { handle: new Map(), on: new Map() };
const wrappers = { handle: new Map(), on: new Map() };

function override(kind, channel, fn) {
  overrides[kind].set(channel, fn);
}

// wrap replaces the client's handler for channel with make(handler).
function wrap(kind, channel, make) {
  wrappers[kind].set(channel, make);
}

function pick(kind, channel, fn) {
  if (overrides[kind].has(channel)) return overrides[kind].get(channel);
  if (wrappers[kind].has(channel)) return wrappers[kind].get(channel)(fn);
  return fn;
}

function install() {
  const origHandle = ipcMain.handle.bind(ipcMain);
  const origOn = ipcMain.on.bind(ipcMain);
  ipcMain.handle = (channel, fn) => origHandle(channel, pick('handle', channel, fn));
  ipcMain.on = (channel, fn) => origOn(channel, pick('on', channel, fn));
  require('./apps').install({ wrap });
}

module.exports = { install, override, wrap };
