'use strict';
// In-memory stand-in for Supabase that mirrors the SQL functions' rules.
const { SyncError } = require('../src/core/cloud');

class FakeBackend { constructor() { this.rows = new Map(); this.files = new Map(); this.settings = new Map(); } }

class FakeCloud {
  constructor(backend, user) { this.b = backend; this.user = user; }
  async currentUser() { return this.user; }
  async listProfiles() { return [...this.b.rows.values()].map((r) => ({ ...r })); }
  async getProfile(id) { const r = this.b.rows.get(id); return r ? { ...r } : null; }
  holder(r, machine) { return r.locked_by && !(r.locked_by === this.user.id && r.locked_from === machine); }
  async claimLock(id, machine, force) {
    const r = this.b.rows.get(id); if (!r) throw new SyncError('CLOUD', 'missing');
    if (this.holder(r, machine) && !force) throw new SyncError('LOCKED', `In use by ${r.locked_by_email} on ${r.locked_from}.`);
    Object.assign(r, { locked_by: this.user.id, locked_by_email: this.user.email, locked_from: machine, locked_at: new Date().toISOString() });
    return { ...r };
  }
  async releaseLock(id, machine, force) {
    const r = this.b.rows.get(id);
    if (this.holder(r, machine) && !force) throw new SyncError('LOCKED', 'locked');
    Object.assign(r, { locked_by: null, locked_by_email: null, locked_from: null, locked_at: null }); return { ...r };
  }
  async commitVersion(a) {
    let r = this.b.rows.get(a.id);
    if (!r) { if (a.expected !== 0) throw new SyncError('CONFLICT', 'conflict'); r = { id: a.id, version: 0 }; this.b.rows.set(a.id, r); }
    else {
      if (r.version !== a.expected) throw new SyncError('CONFLICT', 'conflict');
      if (this.holder(r, a.machine)) throw new SyncError('LOCKED', 'locked');
    }
    Object.assign(r, {
      version: r.version + 1, name: a.name, folder: a.folder, parts: a.parts, size_bytes: a.size, sha256: a.sha256, blob_prefix: a.prefix,
      updated_at: new Date().toISOString(), updated_by: this.user.id, updated_by_email: this.user.email, updated_from: a.machine, updated_os: a.os,
      locked_by: a.keepLock ? this.user.id : null, locked_by_email: a.keepLock ? this.user.email : null, locked_from: a.keepLock ? a.machine : null,
    });
    return { ...r };
  }
  async deleteProfileRow(id) { this.b.rows.delete(id); }
  async upload(p, buf) { this.b.files.set(p, Buffer.from(buf)); }
  async download(p) { if (!this.b.files.has(p)) throw new SyncError('CLOUD', 'not found'); return this.b.files.get(p); }
  async removeFolder(prefix) { for (const k of [...this.b.files.keys()]) if (k.startsWith(prefix + '/')) this.b.files.delete(k); }
  async listFolders(id) { return [...new Set([...this.b.files.keys()].filter((k) => k.startsWith(id + '/')).map((k) => k.split('/').slice(0, 2).join('/')))]; }
  async getTeamSetting(k) { return this.b.settings.get(k) ?? null; }
  async insertTeamSetting(k, v) { this.b.settings.set(k, v); }
}
module.exports = { FakeBackend, FakeCloud };
