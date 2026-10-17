'use strict';
// Upload / download orchestration. All safety rules live here.
const fs = require('fs');
const fsp = fs.promises;
const os = require('os');
const path = require('path');
const crypto = require('crypto');
const P = require('./profiles');
const A = require('./archive');
const { encrypt, decrypt } = require('./crypto');
const { SyncError } = require('./cloud');

const KEEP_BACKUPS = 3;
const rand = () => crypto.randomBytes(4).toString('hex');

async function move(src, dst) {
  await fsp.mkdir(path.dirname(dst), { recursive: true });
  try { await fsp.rename(src, dst); } catch (e) {
    if (e.code !== 'EXDEV') throw e;
    await fsp.cp(src, dst, { recursive: true, preserveTimestamps: true });
    await fsp.rm(src, { recursive: true, force: true });
  }
}
const exists = (p) => fsp.access(p).then(() => true, () => false);

async function writeJsonAtomic(file, obj) {
  const tmp = `${file}.${rand()}.tmp`;
  await fsp.writeFile(tmp, JSON.stringify(obj, null, 2));
  await fsp.rename(tmp, file);
}

class StateStore {
  constructor(file) { this.file = file; try { this.data = JSON.parse(fs.readFileSync(file, 'utf8')); } catch { this.data = {}; } }
  get(id) { return this.data[id] || null; }
  set(id, v) { if (v) this.data[id] = v; else delete this.data[id]; fs.writeFileSync(this.file, JSON.stringify(this.data, null, 2)); }
}

class SyncEngine {
  /**
   * opts: { cloud, key, root, workDir, machine, user, skipCaches, onProgress }
   */
  constructor(opts) {
    Object.assign(this, opts);
    this.state = new StateStore(path.join(this.workDir, 'sync-state.json'));
    this.tmpDir = path.join(this.workDir, 'tmp');
    this.backupDir = path.join(this.workDir, 'backups');
  }

  progress(id, step, pct = null, detail = '') { this.onProgress?.({ id, step, pct, detail }); }
  isMine(row) { return row?.locked_by === this.user.id && row?.locked_from === this.machine; }

  async localFingerprint(summary) {
    const files = await P.collectFiles(this.root, summary, this.skipCaches);
    return { files, fingerprint: P.fingerprintOf(files), bytes: files.reduce((s, f) => s + f.size, 0) };
  }

  /** Combined local + cloud view used by the UI list. */
  async overview() {
    const [locals, remotes] = await Promise.all([P.listLocalProfiles(this.root), this.cloud.listProfiles()]);
    const byId = new Map();
    for (const l of locals) byId.set(l.id, { id: l.id, local: l, remote: null });
    for (const r of remotes) byId.set(r.id, { ...(byId.get(r.id) || { id: r.id, local: null }), remote: r });
    const items = [];
    for (const it of byId.values()) {
      const st = this.state.get(it.id);
      let status; let localBytes = null; let running = false;
      if (it.local) {
        const fp = await this.localFingerprint(it.local);
        localBytes = fp.bytes;
        running = await P.isProfileRunning(this.root, it.id);
        const changed = !st || fp.fingerprint !== st.fingerprint;
        if (!it.remote) status = 'local-only';
        else if (!st) status = 'unlinked';
        else {
          const newer = it.remote.version > st.version;
          status = changed && newer ? 'conflict' : changed ? 'local-changes' : newer ? 'cloud-newer' : 'synced';
        }
      } else status = 'cloud-only';
      const r = it.remote;
      items.push({
        id: it.id,
        name: it.local?.name || r?.name || it.id,
        folder: it.local?.folder ?? r?.folder ?? '',
        status, running, localBytes,
        lastLaunchedAt: it.local?.lastLaunchedAt || null,
        localVersion: st?.version ?? null,
        lastSyncedHere: st?.at ?? null,
        remote: r && {
          version: r.version, sizeBytes: r.size_bytes, updatedAt: r.updated_at, updatedBy: r.updated_by_email,
          updatedFrom: r.updated_from, updatedOs: r.updated_os,
          lock: r.locked_by ? { email: r.locked_by_email, machine: r.locked_from, at: r.locked_at, mine: this.isMine(r) } : null,
        },
      });
    }
    items.sort((a, b) => a.name.localeCompare(b.name));
    return items;
  }

  async push(id, { keepLock = false, overwrite = false } = {}) {
    this.progress(id, 'Checking', 0);
    const summaryPath = path.join(this.root, 'profiles', `${id}.json`);
    if (!(await exists(summaryPath))) throw new SyncError('MISSING', 'This profile is not on this computer.');
    const summary = P.summarize(id, P.readProfileJson(this.root, id));
    if (await P.isProfileRunning(this.root, id)) throw new SyncError('RUNNING', `Close the "${summary.name}" browser window first, then upload.`);

    const remote = await this.cloud.getProfile(id);
    const st = this.state.get(id);
    if (remote) {
      if (remote.locked_by && !this.isMine(remote)) {
        throw new SyncError('LOCKED', `In use by ${remote.locked_by_email} on ${remote.locked_from}. They must upload or release it first.`);
      }
      if ((!st || remote.version > st.version) && !overwrite) {
        throw new SyncError('CONFIRM_OVERWRITE_CLOUD',
          `The cloud has version ${remote.version} from ${remote.updated_by_email} (${remote.updated_from}) that you have not downloaded. Uploading will replace it.`);
      }
    }
    const expected = remote?.version ?? 0;

    this.progress(id, 'Scanning files', 2);
    const { files, fingerprint, bytes } = await this.localFingerprint(summary);
    await fsp.mkdir(this.tmpDir, { recursive: true });
    const archive = path.join(this.tmpDir, `${id}-${rand()}.tar.gz`);
    const prefix = `${id}/${Date.now().toString(36)}-${rand()}`;
    let committed = false;
    try {
      this.progress(id, `Compressing ${files.length} files`, 5, fmtBytes(bytes));
      const packed = await A.packToFile(this.root, files, archive);

      let proxy = null;
      if (summary.proxyId) {
        try { proxy = JSON.parse(await fsp.readFile(path.join(this.root, 'proxies.json'), 'utf8')).proxies?.find((p) => p.id === summary.proxyId) || null; } catch { /* none */ }
      }
      const extensions = summary.extensions.filter((e) => files.some((f) => f.rel.startsWith(`extensions/${e}/`)));
      const meta = {
        format: 1, id, name: summary.name, extensions, proxy, fileCount: files.length, rawBytes: bytes,
        archiveBytes: packed.size, sha256: packed.sha256, os: process.platform, machine: this.machine, createdAt: new Date().toISOString(),
      };
      await this.cloud.upload(`${prefix}/meta`, encrypt(this.key, Buffer.from(JSON.stringify(meta)), A.metaAad(id, prefix)));

      const parts = await A.forEachEncryptedPart(this.key, archive, id, prefix, async (i, buf, count) => {
        this.progress(id, `Uploading part ${i + 1} of ${count}`, 10 + Math.round((85 * i) / count), fmtBytes(packed.size));
        await this.cloud.upload(`${prefix}/part-${String(i).padStart(4, '0')}`, buf);
      });

      this.progress(id, 'Publishing', 96);
      const row = await this.cloud.commitVersion({
        id, expected, name: summary.name, folder: summary.folder, parts, size: packed.size, sha256: packed.sha256,
        prefix, machine: this.machine, os: process.platform, keepLock,
      });
      committed = true;
      this.state.set(id, { version: row.version, fingerprint, at: new Date().toISOString() });

      this.progress(id, 'Cleaning up old versions', 98);
      for (const folder of await this.cloud.listFolders(id)) if (folder !== prefix) await this.cloud.removeFolder(folder).catch(() => {});
      this.progress(id, `Uploaded version ${row.version}`, 100);
      return row;
    } finally {
      await fsp.rm(archive, { force: true });
      if (!committed) await this.cloud.removeFolder(prefix).catch(() => {});
    }
  }

  async pull(id, { takeLock = true, forceLock = false, overwriteLocal = false } = {}) {
    this.progress(id, 'Checking', 0);
    const remote = await this.cloud.getProfile(id);
    if (!remote || !remote.blob_prefix) throw new SyncError('MISSING', 'This profile is not in the cloud.');
    if (remote.locked_by && !this.isMine(remote) && !forceLock) {
      throw new SyncError('LOCKED', `In use by ${remote.locked_by_email} on ${remote.locked_from}. Downloading now would give you an outdated copy.`,
        { email: remote.locked_by_email, machine: remote.locked_from });
    }
    const localJson = path.join(this.root, 'profiles', `${id}.json`);
    const hasLocal = await exists(localJson);
    if (hasLocal) {
      if (await P.isProfileRunning(this.root, id)) throw new SyncError('RUNNING', 'Close this profile\'s browser window first, then download.');
      if (!overwriteLocal) {
        const st = this.state.get(id);
        const { fingerprint } = await this.localFingerprint(P.summarize(id, P.readProfileJson(this.root, id)));
        if (!st) throw new SyncError('CONFIRM_OVERWRITE_LOCAL', 'This profile already exists here and was never synced. Downloading replaces it (a backup is kept).');
        if (st.fingerprint !== fingerprint) throw new SyncError('CONFIRM_OVERWRITE_LOCAL', 'This profile was used here since the last sync. Downloading replaces those changes (a backup is kept).');
      }
    }
    let claimedNow = false;
    if (takeLock && !this.isMine(remote)) { await this.cloud.claimLock(id, this.machine, forceLock); claimedNow = true; }
    let done = false;

    const prefix = remote.blob_prefix;
    await fsp.mkdir(this.tmpDir, { recursive: true });
    const archive = path.join(this.tmpDir, `${id}-${rand()}.tar.gz`);
    const staging = path.join(this.tmpDir, `stage-${id}-${rand()}`);
    try {
      this.progress(id, 'Downloading', 3);
      const meta = JSON.parse(decrypt(this.key, await this.cloud.download(`${prefix}/meta`), A.metaAad(id, prefix)).toString('utf8'));
      const out = await fsp.open(archive, 'w');
      try {
        for (let i = 0; i < remote.parts; i++) {
          this.progress(id, `Downloading part ${i + 1} of ${remote.parts}`, 5 + Math.round((70 * i) / remote.parts), fmtBytes(remote.size_bytes));
          const buf = await this.cloud.download(`${prefix}/part-${String(i).padStart(4, '0')}`);
          await out.write(decrypt(this.key, buf, A.partAad(id, prefix, i)));
        }
      } finally { await out.close(); }
      if ((await A.sha256File(archive)) !== remote.sha256) throw new SyncError('CORRUPT', 'Downloaded data failed its integrity check. Nothing was changed. Try again.');

      this.progress(id, 'Unpacking', 78);
      await A.extractProfile(archive, staging, id, meta.extensions || []);
      if (!(await exists(path.join(staging, 'profiles', `${id}.json`)))) throw new SyncError('CORRUPT', 'Archive has no profile file. Nothing was changed.');

      this.progress(id, 'Backing up current copy', 85);
      await this.installFromStaging(id, staging, meta);

      const summary = P.summarize(id, P.readProfileJson(this.root, id));
      const { fingerprint } = await this.localFingerprint(summary);
      this.state.set(id, { version: remote.version, fingerprint, at: new Date().toISOString() });
      await this.pruneBackups(id);
      const crossOs = meta.os && meta.os !== process.platform;
      this.progress(id, `Downloaded version ${remote.version}`, 100, crossOs ? 'Uploaded from a different OS. Some site logins may need to be redone.' : '');
      done = true;
      return { version: remote.version, crossOs, fromOs: meta.os };
    } finally {
      if (!done && claimedNow) await this.cloud.releaseLock(id, this.machine, false).catch(() => {});
      await fsp.rm(archive, { force: true });
      await fsp.rm(staging, { recursive: true, force: true });
    }
  }

  /** Swap the staged profile into the ShardX folder. Rolls back if anything fails. */
  async installFromStaging(id, staging, meta) {
    const stamp = new Date().toISOString().replace(/[:.]/g, '-');
    const bdir = path.join(this.backupDir, id, stamp);
    const moved = []; // [livePath, backupPath]
    const backup = async (live, rel) => {
      if (await exists(live)) { const b = path.join(bdir, rel); await move(live, b); moved.push([live, b]); }
    };
    const installed = [];
    try {
      await backup(path.join(this.root, 'profiles', `${id}.json`), `profiles/${id}.json`);
      await backup(path.join(this.root, 'user-data', id), `user-data/${id}`);
      const stagedExt = path.join(staging, 'extensions');
      const extIds = (await exists(stagedExt)) ? await fsp.readdir(stagedExt) : [];
      for (const ext of extIds) await backup(path.join(this.root, 'extensions', ext), `extensions/${ext}`);
      const proxiesFile = path.join(this.root, 'proxies.json');
      if (meta.proxy && (await exists(proxiesFile))) {
        await fsp.mkdir(bdir, { recursive: true });
        await fsp.copyFile(proxiesFile, path.join(bdir, 'proxies.json'));
      }

      this.progress(id, 'Installing', 92);
      const installs = [[`profiles/${id}.json`], [`user-data/${id}`], ...extIds.map((e) => [`extensions/${e}`])];
      for (const [rel] of installs) {
        const src = path.join(staging, rel);
        if (await exists(src)) { const dst = path.join(this.root, rel); await move(src, dst); installed.push(dst); }
      }
      if (meta.proxy) {
        let store = { proxies: [] };
        try { store = JSON.parse(await fsp.readFile(proxiesFile, 'utf8')); } catch { /* new file */ }
        store.proxies = (store.proxies || []).filter((p) => p.id !== meta.proxy.id).concat([meta.proxy]);
        await writeJsonAtomic(proxiesFile, store);
      }
    } catch (e) {
      for (const dst of installed.reverse()) await fsp.rm(dst, { recursive: true, force: true }).catch(() => {});
      for (const [live, b] of moved.reverse()) await move(b, live).catch(() => {});
      throw new SyncError('INSTALL', `Could not install the profile, your previous copy was restored: ${e.message}`);
    }
  }

  async pruneBackups(id) {
    const dir = path.join(this.backupDir, id);
    const all = (await fsp.readdir(dir).catch(() => [])).sort();
    for (const old of all.slice(0, Math.max(0, all.length - KEEP_BACKUPS))) await fsp.rm(path.join(dir, old), { recursive: true, force: true });
  }

  async releaseLock(id, force = false) { return this.cloud.releaseLock(id, this.machine, force); }
  async takeLock(id, force = false) { return this.cloud.claimLock(id, this.machine, force); }

  async deleteFromCloud(id) {
    const remote = await this.cloud.getProfile(id);
    if (!remote) return;
    if (remote.locked_by && !this.isMine(remote)) throw new SyncError('LOCKED', `In use by ${remote.locked_by_email}. Ask them to release it first.`);
    for (const folder of await this.cloud.listFolders(id)) await this.cloud.removeFolder(folder);
    await this.cloud.deleteProfileRow(id);
    this.state.set(id, null);
  }

  async cleanTmp() { await fsp.rm(this.tmpDir, { recursive: true, force: true }).catch(() => {}); }
}

function fmtBytes(n) {
  if (n == null) return '';
  const u = ['B', 'KB', 'MB', 'GB']; let i = 0;
  while (n >= 1024 && i < u.length - 1) { n /= 1024; i++; }
  return `${n.toFixed(i ? 1 : 0)} ${u[i]}`;
}

const KEY_CHECK = 'shardx-sync-key-check-v1';
async function verifyTeamKey(cloud, key) {
  const existing = await cloud.getTeamSetting('key_check');
  if (!existing) {
    await cloud.insertTeamSetting('key_check', encrypt(key, Buffer.from(KEY_CHECK), 'keycheck').toString('base64'));
    return 'created';
  }
  try {
    if (decrypt(key, Buffer.from(existing, 'base64'), 'keycheck').toString() === KEY_CHECK) return 'ok';
  } catch { /* fallthrough */ }
  throw new SyncError('PASSPHRASE', 'Wrong team passphrase. It must match the one your team set up first.');
}

module.exports = { SyncEngine, verifyTeamKey, fmtBytes, os };
