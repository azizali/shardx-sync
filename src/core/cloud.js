'use strict';
// Thin wrapper around Supabase: auth, profile metadata/locks (Postgres) and encrypted parts (Storage).
const { createClient } = require('@supabase/supabase-js');

const BUCKET = 'profiles';

class SyncError extends Error {
  constructor(code, message, data) { super(message); this.code = code; this.data = data; }
}

function translateRpcError(error) {
  const msg = error?.message || String(error);
  let m = msg.match(/LOCKED:(.*) on (.*)$/);
  if (m) return new SyncError('LOCKED', `In use by ${m[1]} on ${m[2]}.`, { email: m[1], machine: m[2] });
  m = msg.match(/CONFLICT:(\d+)/);
  if (m) return new SyncError('CONFLICT', `Someone uploaded a newer version (v${m[1]}) while you were working.`, { version: Number(m[1]) });
  if (/function .* does not exist|relation .* does not exist|Could not find the function/i.test(msg)) {
    return new SyncError('SETUP', 'Supabase is not set up yet. Run supabase/setup.sql in the SQL Editor.');
  }
  return new SyncError('CLOUD', msg);
}

async function withRetry(fn, label, attempts = 4) {
  let last;
  for (let i = 0; i < attempts; i++) {
    try { return await fn(); } catch (e) {
      last = e;
      if (e instanceof SyncError && e.code !== 'CLOUD') throw e;
      await new Promise((r) => setTimeout(r, 1000 * 2 ** i));
    }
  }
  throw new SyncError('NETWORK', `${label} failed after ${attempts} tries: ${last?.message || last}`);
}

class Cloud {
  /** sessionStore: { getItem, setItem, removeItem } used to keep you logged in. */
  constructor(url, anonKey, sessionStore) {
    this.sb = createClient(url, anonKey, {
      auth: { storage: sessionStore, persistSession: true, autoRefreshToken: true, detectSessionInUrl: false },
    });
  }

  async currentUser() {
    const { data } = await this.sb.auth.getSession();
    return data.session?.user || null;
  }
  async signIn(email, password) {
    const { data, error } = await this.sb.auth.signInWithPassword({ email, password });
    if (error) throw new SyncError('AUTH', error.message);
    return data.user;
  }
  async signOut() { await this.sb.auth.signOut(); }

  async listProfiles() {
    const { data, error } = await this.sb.from('synced_profiles').select('*').order('name');
    if (error) throw translateRpcError(error);
    return data;
  }
  async getProfile(id) {
    const { data, error } = await this.sb.from('synced_profiles').select('*').eq('id', id).maybeSingle();
    if (error) throw translateRpcError(error);
    return data;
  }
  async rpc(fn, args) {
    const { data, error } = await this.sb.rpc(fn, args);
    if (error) throw translateRpcError(error);
    return data;
  }
  claimLock(id, machine, force = false) { return this.rpc('claim_lock', { p_id: id, p_machine: machine, p_force: force }); }
  releaseLock(id, machine, force = false) { return this.rpc('release_lock', { p_id: id, p_machine: machine, p_force: force }); }
  commitVersion(a) {
    return this.rpc('commit_version', {
      p_id: a.id, p_expected: a.expected, p_name: a.name, p_folder: a.folder, p_parts: a.parts, p_size: a.size,
      p_sha: a.sha256, p_prefix: a.prefix, p_machine: a.machine, p_os: a.os, p_keep_lock: a.keepLock,
    });
  }
  async deleteProfileRow(id) {
    const { error } = await this.sb.from('synced_profiles').delete().eq('id', id);
    if (error) throw translateRpcError(error);
  }

  upload(objectPath, buf) {
    return withRetry(async () => {
      const { error } = await this.sb.storage.from(BUCKET).upload(objectPath, buf, { upsert: true, contentType: 'application/octet-stream' });
      if (error) {
        if (/bucket not found/i.test(error.message)) throw new SyncError('SETUP', 'Storage bucket "profiles" is missing. Run supabase/setup.sql.');
        if (/exceeded the maximum allowed size|payload too large/i.test(error.message)) throw new SyncError('SIZE', 'Supabase rejected the file size. Check Storage > Settings upload limit (needs at least 50 MB).');
        throw new SyncError('CLOUD', error.message);
      }
    }, `Upload of ${objectPath}`);
  }
  download(objectPath) {
    return withRetry(async () => {
      const { data, error } = await this.sb.storage.from(BUCKET).download(objectPath);
      if (error) throw new SyncError('CLOUD', error.message || 'download error');
      return Buffer.from(await data.arrayBuffer());
    }, `Download of ${objectPath}`);
  }
  async removeFolder(prefix) {
    const { data, error } = await this.sb.storage.from(BUCKET).list(prefix, { limit: 1000 });
    if (error || !data?.length) return;
    await this.sb.storage.from(BUCKET).remove(data.map((o) => `${prefix}/${o.name}`));
  }
  async listFolders(id) {
    const { data } = await this.sb.storage.from(BUCKET).list(id, { limit: 1000 });
    return (data || []).map((o) => `${id}/${o.name}`);
  }

  async getTeamSetting(key) {
    const { data, error } = await this.sb.from('team_settings').select('value').eq('key', key).maybeSingle();
    if (error) throw translateRpcError(error);
    return data?.value ?? null;
  }
  async insertTeamSetting(key, value) {
    const { error } = await this.sb.from('team_settings').insert({ key, value });
    if (error) throw translateRpcError(error);
  }
}

module.exports = { Cloud, SyncError };
