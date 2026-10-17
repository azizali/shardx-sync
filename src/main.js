'use strict';
const { app, BrowserWindow, ipcMain, dialog, shell, safeStorage } = require('electron');
const fs = require('fs');
const os = require('os');
const path = require('path');
const { Cloud, SyncError } = require('./core/cloud');
const { SyncEngine, verifyTeamKey } = require('./core/sync');
const { deriveKey } = require('./core/crypto');
const DEFAULTS = require('./core/defaults');
const { resolveShardxRoot, isValidShardxRoot } = require('./core/paths');
const { isLauncherRunning } = require('./core/profiles');

const WORK = app.getPath('userData');
const CONFIG_FILE = path.join(WORK, 'config.json');
const SECRET_FILE = path.join(WORK, 'secrets.bin');

// ---------- local config & secrets (secrets encrypted with the OS keychain/DPAPI/libsecret) ----------
const readJson = (f, d) => { try { return JSON.parse(fs.readFileSync(f, 'utf8')); } catch { return d; } };
const loadConfig = () => {
  const c = { neonUrl: '', storageUrl: '', shardxRoot: '', machine: os.hostname(), skipCaches: true, ...readJson(CONFIG_FILE, {}) };
  return { ...c, neonUrl: c.neonUrl || DEFAULTS.neonUrl, storageUrl: c.storageUrl || DEFAULTS.storageUrl, authOrigin: c.authOrigin || DEFAULTS.authOrigin };
};
const saveConfig = (c) => { fs.mkdirSync(WORK, { recursive: true }); fs.writeFileSync(CONFIG_FILE, JSON.stringify(c, null, 2)); };

function loadSecrets() {
  try {
    const raw = fs.readFileSync(SECRET_FILE);
    const text = safeStorage.isEncryptionAvailable() ? safeStorage.decryptString(raw) : raw.toString('utf8');
    return JSON.parse(text);
  } catch { return {}; }
}
function saveSecrets(s) {
  const text = JSON.stringify(s);
  fs.writeFileSync(SECRET_FILE, safeStorage.isEncryptionAvailable() ? safeStorage.encryptString(text) : text, { mode: 0o600 });
}
// Keeps the Neon Auth session across restarts.
const sessionStore = {
  getItem: (k) => loadSecrets().session?.[k] ?? null,
  setItem: (k, v) => { const s = loadSecrets(); s.session = { ...(s.session || {}), [k]: v }; saveSecrets(s); },
  removeItem: (k) => { const s = loadSecrets(); if (s.session) delete s.session[k]; saveSecrets(s); },
};

// ---------- runtime ----------
let win; let cloud = null; let engine = null; let busy = false;

function buildCloud() {
  const c = loadConfig();
  try { cloud = c.neonUrl && c.storageUrl ? new Cloud(c, sessionStore) : null; } catch { cloud = null; }
  engine = null;
}

async function getEngine() {
  if (engine) return engine;
  const c = loadConfig();
  if (!cloud) throw new SyncError('SETUP', 'Open Settings and check the Neon URLs.');
  const user = await cloud.currentUser();
  if (!user) throw new SyncError('AUTH', 'Please sign in.');
  const pass = loadSecrets().passphrase;
  if (!pass) throw new SyncError('SETUP', 'Open Settings and enter the team passphrase.');
  const root = resolveShardxRoot(c.shardxRoot);
  if (!isValidShardxRoot(root)) throw new SyncError('SETUP', `ShardX data folder not found at ${root}. Set it in Settings.`);
  const key = deriveKey(pass, c.neonUrl);
  await verifyTeamKey(cloud, key);
  engine = new SyncEngine({
    cloud, key, root, workDir: WORK, machine: c.machine || os.hostname(), user, skipCaches: c.skipCaches !== false,
    onProgress: (p) => win?.webContents.send('progress', p),
  });
  await engine.cleanTmp();
  return engine;
}

// Wraps every IPC call: one sync at a time, errors returned as data (not thrown) so the UI can react.
function handle(channel, fn, { exclusive = false } = {}) {
  ipcMain.handle(channel, async (_e, ...args) => {
    if (exclusive && busy) return { ok: false, error: { code: 'BUSY', message: 'Another sync is running. Please wait.' } };
    if (exclusive) busy = true;
    try { return { ok: true, data: await fn(...args) }; } catch (e) {
      return { ok: false, error: { code: e.code || 'ERROR', message: e.message, data: e.data } };
    } finally { if (exclusive) busy = false; }
  });
}

handle('app:state', async () => {
  const c = loadConfig();
  const user = cloud ? await cloud.currentUser().catch(() => null) : null;
  const root = resolveShardxRoot(c.shardxRoot);
  return {
    configured: !!(c.neonUrl && c.storageUrl && loadSecrets().passphrase),
    user: user ? { id: user.id, email: user.email } : null,
    config: { ...c, shardxRootResolved: root, shardxRootValid: isValidShardxRoot(root), hasPassphrase: !!loadSecrets().passphrase },
    platform: process.platform, version: app.getVersion(),
  };
});

handle('settings:save', async (s) => {
  const c = loadConfig();
  const next = {
    ...readJson(CONFIG_FILE, {}),
    neonUrl: String(s.neonUrl || '').trim().replace(/\/+$/, ''),
    storageUrl: String(s.storageUrl || '').trim().replace(/\/+$/, ''),
    shardxRoot: String(s.shardxRoot || '').trim(),
    machine: String(s.machine || '').trim() || os.hostname(),
    skipCaches: s.skipCaches !== false,
  };
  for (const k of ['neonUrl', 'storageUrl']) {
    if (next[k] && !/^https:\/\/[^/]+/.test(next[k])) throw new SyncError('SETUP', `${k} must be an https URL.`);
    if (next[k] === DEFAULTS[k]) next[k] = ''; // keep following the built-in default
  }
  const changedProject = (next.neonUrl || DEFAULTS.neonUrl) !== c.neonUrl;
  saveConfig(next);
  if (s.passphrase) { const sec = loadSecrets(); sec.passphrase = String(s.passphrase); saveSecrets(sec); }
  if (changedProject) { const sec = loadSecrets(); delete sec.session; saveSecrets(sec); }
  buildCloud();
  return true;
});

handle('settings:pickFolder', async () => {
  const r = await dialog.showOpenDialog(win, { properties: ['openDirectory'], title: 'Select the ShardX data folder (contains "profiles")' });
  return r.canceled ? null : r.filePaths[0];
});

handle('auth:signIn', async (email, password) => {
  if (!cloud) throw new SyncError('SETUP', 'Check the Neon URLs in Settings first.');
  const u = await cloud.signIn(email, password);
  engine = null;
  return { id: u.id, email: u.email };
});
handle('auth:sendCode', async (email) => {
  if (!cloud) throw new SyncError('SETUP', 'Check the Neon URLs in Settings first.');
  await cloud.sendPasswordCode(email); return true;
});
handle('auth:resetPassword', async (email, otp, password) => {
  if (!cloud) throw new SyncError('SETUP', 'Check the Neon URLs in Settings first.');
  await cloud.resetPassword(email, otp, password); return true;
});
handle('auth:signOut', async () => { await cloud?.signOut(); engine = null; return true; });

handle('profiles:list', async () => {
  const e = await getEngine();
  return { items: await e.overview(), launcherRunning: await isLauncherRunning(), machine: e.machine, root: e.root };
});
handle('profiles:push', async (id, opts) => (await getEngine()).push(id, opts || {}), { exclusive: true });
handle('profiles:pull', async (id, opts) => (await getEngine()).pull(id, opts || {}), { exclusive: true });
handle('profiles:releaseLock', async (id, force) => (await getEngine()).releaseLock(id, !!force), { exclusive: true });
handle('profiles:takeLock', async (id, force) => (await getEngine()).takeLock(id, !!force), { exclusive: true });
handle('profiles:deleteRemote', async (id) => (await getEngine()).deleteFromCloud(id), { exclusive: true });
handle('open:backups', async () => { fs.mkdirSync(path.join(WORK, 'backups'), { recursive: true }); return shell.openPath(path.join(WORK, 'backups')); });
handle('open:shardx', async () => shell.openPath(resolveShardxRoot(loadConfig().shardxRoot)));
handle('open:external', async (url) => { if (/^https:\/\//.test(url)) await shell.openExternal(url); return true; });

function createWindow() {
  win = new BrowserWindow({
    width: 1120, height: 720, minWidth: 860, minHeight: 560,
    backgroundColor: '#0f1115', title: 'ShardX Sync',
    webPreferences: { preload: path.join(__dirname, 'preload.js'), contextIsolation: true, nodeIntegration: false, sandbox: true },
  });
  win.setMenuBarVisibility(false);
  win.loadFile(path.join(__dirname, 'ui', 'index.html'));
  win.webContents.setWindowOpenHandler(() => ({ action: 'deny' }));
  win.webContents.on('will-navigate', (e) => e.preventDefault());
}

app.whenReady().then(() => { buildCloud(); createWindow(); });
app.on('window-all-closed', () => app.quit());
app.on('before-quit', (e) => {
  if (busy) {
    const r = dialog.showMessageBoxSync(win, { type: 'warning', buttons: ['Keep running', 'Quit anyway'], defaultId: 0, message: 'A sync is in progress. Quitting now cancels it (nothing on disk is damaged).' });
    if (r === 0) e.preventDefault();
  }
});
