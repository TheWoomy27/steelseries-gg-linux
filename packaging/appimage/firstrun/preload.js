'use strict';

const { contextBridge, ipcRenderer } = require('electron');

contextBridge.exposeInMainWorld('setup', {
  state: () => ipcRenderer.invoke('state'),
  download: () => ipcRenderer.invoke('download'),
  choose: () => ipcRenderer.invoke('choose'),
  import: (installer) => ipcRenderer.invoke('import', installer),
  devices: () => ipcRenderer.invoke('devices'),
  rulesCommand: () => ipcRenderer.invoke('rulesCommand'),
  finish: (options) => ipcRenderer.invoke('finish', options),
  onProgress: (fn) => ipcRenderer.on('progress', (_e, p) => fn(p)),
});
