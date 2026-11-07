'use strict';

// Linux implementations for IPC channels whose Windows versions rely on
// native modules or Windows APIs. The client's main process registers its
// own handlers; we wrap ipcMain so ours win for the channels listed here.

const { ipcMain } = require('electron');

const overrides = { handle: new Map(), on: new Map() };

function override(kind, channel, fn) {
  overrides[kind].set(channel, fn);
}

function install() {
  const origHandle = ipcMain.handle.bind(ipcMain);
  const origOn = ipcMain.on.bind(ipcMain);
  ipcMain.handle = (channel, fn) => origHandle(channel, overrides.handle.get(channel) || fn);
  ipcMain.on = (channel, fn) => origOn(channel, overrides.on.get(channel) || fn);
}

module.exports = { install, override };
