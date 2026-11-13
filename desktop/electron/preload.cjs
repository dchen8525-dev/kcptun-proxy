'use strict';
const { contextBridge, ipcRenderer } = require('electron');

contextBridge.exposeInMainWorld('kcp', {
  start: (cfg) => ipcRenderer.invoke('kcp:start', cfg),
  stop: () => ipcRenderer.invoke('kcp:stop'),
  state: () => ipcRenderer.invoke('kcp:state'),
  loadSettings: () => ipcRenderer.invoke('kcp:settings:load'),
  saveSettings: (patch) => ipcRenderer.invoke('kcp:settings:save', patch),
  speedStart: (cfg) => ipcRenderer.invoke('kcp:speed:start', cfg),
  speedStop: () => ipcRenderer.invoke('kcp:speed:stop'),
  openLogDir: () => ipcRenderer.invoke('kcp:open-log-dir'),
  pickUrl: () => ipcRenderer.invoke('kcp:pick-url'),
  onStats: (fn) => ipcRenderer.on('kcp:stats', (_e, p) => fn(p)),
  onLog: (fn) => ipcRenderer.on('kcp:log', (_e, p) => fn(p)),
  onState: (fn) => ipcRenderer.on('kcp:state', (_e, p) => fn(p)),
  onSpeed: (fn) => ipcRenderer.on('kcp:speed', (_e, p) => fn(p)),
});
