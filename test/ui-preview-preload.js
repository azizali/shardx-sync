const { contextBridge } = require('electron');
const ok = (data) => Promise.resolve({ ok: true, data });
const now = Date.now(); const iso = (m) => new Date(now - m * 60000).toISOString();
const items = [
  { id: 'a', name: 'NAS Decor Noor', folder: '', status: 'cloud-newer', running: false, localBytes: 41e6, lastLaunchedAt: now - 864e5, localVersion: 3, lastSyncedHere: iso(3000),
    remote: { version: 4, sizeBytes: 23e6, updatedAt: iso(42), updatedBy: 'teammate@team.com', updatedFrom: 'Office-PC', updatedOs: 'win32', lock: null } },
  { id: 'b', name: 'NAS Decor Quratulain', folder: 'Clients', status: 'synced', running: false, localBytes: 1.2e6, lastLaunchedAt: now - 3e8, localVersion: 2, lastSyncedHere: iso(600),
    remote: { version: 2, sizeBytes: 0.6e6, updatedAt: iso(600), updatedBy: 'aziz@team.com', updatedFrom: 'Azizs-MacBook-Pro.local', updatedOs: 'darwin', lock: { email: 'teammate@team.com', machine: 'Office-PC', at: iso(15), mine: false } } },
  { id: 'c', name: 'Shibam', folder: '', status: 'local-changes', running: true, localBytes: 130e6, lastLaunchedAt: now - 6e5, localVersion: 5, lastSyncedHere: iso(1440),
    remote: { version: 5, sizeBytes: 71e6, updatedAt: iso(1440), updatedBy: 'aziz@team.com', updatedFrom: 'Azizs-MacBook-Pro.local', updatedOs: 'darwin', lock: { email: 'aziz@team.com', machine: 'Azizs-MacBook-Pro.local', at: iso(1440), mine: true } } },
  { id: 'd', name: 'Test', folder: '', status: 'local-only', running: false, localBytes: 3e6, lastLaunchedAt: now - 9e7, localVersion: null, lastSyncedHere: null, remote: null },
];
contextBridge.exposeInMainWorld('api', {
  state: () => ok({ configured: true, user: { email: 'aziz@team.com' }, config: { machine: 'Azizs-MacBook-Pro.local' } }),
  list: () => ok({ items, launcherRunning: true, root: '~/Library/Application Support/shardx-launcher' }),
  onProgress: () => {},
});
