'use strict';
const { contextBridge, ipcRenderer } = require('electron');
const call = (ch) => (...a) => ipcRenderer.invoke(ch, ...a);
contextBridge.exposeInMainWorld('api', {
  state: call('app:state'),
  saveSettings: call('settings:save'),
  pickFolder: call('settings:pickFolder'),
  signIn: call('auth:signIn'),
  signOut: call('auth:signOut'),
  list: call('profiles:list'),
  push: call('profiles:push'),
  pull: call('profiles:pull'),
  releaseLock: call('profiles:releaseLock'),
  takeLock: call('profiles:takeLock'),
  deleteRemote: call('profiles:deleteRemote'),
  openBackups: call('open:backups'),
  openShardx: call('open:shardx'),
  openExternal: call('open:external'),
  onProgress: (fn) => ipcRenderer.on('progress', (_e, p) => fn(p)),
});
