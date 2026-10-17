'use strict';
// Neon backend: Managed Better Auth (sign-in), Data API (profile rows + lock functions)
// and Object Storage through the storage Function (presigned URLs).
const SESSION_KEY = 'neon-session';

class SyncError extends Error {
  constructor(code, message, data) { super(message); this.code = code; this.data = data; }
}

/** https://ep-x.c-6.us-east-2.aws.neon.tech/neondb -> auth + data API base URLs */
function deriveUrls(neonUrl) {
  const u = new URL(neonUrl);
  const [first, ...rest] = u.hostname.split('.');
  const db = u.pathname.replace(/\/+$/, '') || '/neondb';
  return {
    auth: `https://${[first, 'neonauth', ...rest].join('.')}${db}/auth`,
    rest: `https://${[first, 'apirest', ...rest].join('.')}${db}/rest/v1`,
  };
}

function translateDbError(status, body) {
  const msg = body?.message || `HTTP ${status}`;
  let m = msg.match(/LOCKED:(.*) on (.*)$/);
  if (m) return new SyncError('LOCKED', `In use by ${m[1]} on ${m[2]}.`, { email: m[1], machine: m[2] });
  m = msg.match(/CONFLICT:(\d+)/);
  if (m) return new SyncError('CONFLICT', `Someone uploaded a newer version (v${m[1]}) while you were working.`, { version: Number(m[1]) });
  if (body?.code === '42P01' || body?.code === 'PGRST202' || body?.code === 'PGRST205' || /does not exist|Could not find/i.test(msg)) {
    return new SyncError('SETUP', 'The Neon database is not set up yet. Run neon/setup.sql on it.');
  }
  if (status === 401) return new SyncError('AUTH', 'Your session expired. Please sign in again.');
  return new SyncError('CLOUD', msg);
}

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
async function withRetry(fn, label, attempts = 4) {
  let last;
  for (let i = 0; i < attempts; i++) {
    try { return await fn(); } catch (e) {
      last = e;
      if (e instanceof SyncError && e.code !== 'CLOUD' && e.code !== 'RETRY') throw e;
      await sleep(1000 * 2 ** i);
    }
  }
  throw new SyncError('NETWORK', `${label} failed after ${attempts} tries: ${last?.message || last}`);
}

class Cloud {
  /**
   * cfg: { neonUrl, storageUrl, authOrigin }
   * sessionStore: { getItem, setItem, removeItem } (encrypted on disk by the caller)
   */
  constructor(cfg, sessionStore) {
    const urls = deriveUrls(cfg.neonUrl);
    this.authUrl = urls.auth;
    this.restUrl = urls.rest;
    this.fnUrl = cfg.storageUrl.replace(/\/+$/, '');
    this.origin = cfg.authOrigin;
    this.store = sessionStore;
    this.jwt = null; this.jwtExp = 0;
  }

  // ---------- auth (Better Auth REST) ----------
  session() { try { return JSON.parse(this.store.getItem(SESSION_KEY) || 'null'); } catch { return null; } }
  saveSession(s) { if (s) this.store.setItem(SESSION_KEY, JSON.stringify(s)); else this.store.removeItem(SESSION_KEY); }

  static cookiesFrom(res, prev = '') {
    const jar = new Map(prev.split('; ').filter(Boolean).map((c) => [c.slice(0, c.indexOf('=')), c]));
    for (const line of res.headers.getSetCookie?.() || []) {
      const pair = line.split(';')[0];
      const name = pair.slice(0, pair.indexOf('='));
      if (/max-age=0|expires=thu, 01 jan 1970/i.test(line)) jar.delete(name); else jar.set(name, pair);
    }
    return [...jar.values()].join('; ');
  }

  async authPost(path, body, withCookie = false) {
    const headers = { 'content-type': 'application/json', origin: this.origin };
    if (withCookie && this.session()?.cookie) headers.cookie = this.session().cookie;
    let res;
    try { res = await fetch(this.authUrl + path, { method: 'POST', headers, body: JSON.stringify(body) }); } catch (e) {
      throw new SyncError('NETWORK', `Cannot reach Neon Auth: ${e.message}`);
    }
    const json = await res.json().catch(() => ({}));
    if (!res.ok) {
      const map = {
        INVALID_EMAIL_OR_PASSWORD: 'Wrong email or password.',
        INVALID_OTP: 'That code is wrong. Check the email or request a new code.',
        OTP_EXPIRED: 'That code expired. Request a new one.',
        TOO_MANY_ATTEMPTS: 'Too many attempts. Request a new code.',
        INVALID_ORIGIN: 'Neon Auth rejected this app. Add the app origin as a trusted domain (see README).',
        PASSWORD_TOO_SHORT: 'Password is too short (minimum 8 characters).',
      };
      throw new SyncError('AUTH', map[json.code] || json.message || `Sign-in failed (HTTP ${res.status})`);
    }
    return { res, json };
  }

  async currentUser() { return this.session()?.user || null; }

  async signIn(email, password) {
    const { res, json } = await this.authPost('/sign-in/email', { email, password });
    const cookie = Cloud.cookiesFrom(res);
    if (!cookie) throw new SyncError('AUTH', 'Sign-in returned no session.');
    const user = { id: json.user.id, email: json.user.email };
    this.saveSession({ cookie, user });
    this.jwt = null;
    await this.token(); // proves the session works end to end
    return user;
  }

  async signOut() {
    try { await this.authPost('/sign-out', {}, true); } catch { /* already gone */ }
    this.saveSession(null); this.jwt = null;
  }

  /** Sends a 6-digit code by email. Works for new team accounts that have no password yet. */
  async sendPasswordCode(email) {
    await this.authPost('/email-otp/send-verification-otp', { email, type: 'forget-password' });
  }
  async resetPassword(email, otp, password) {
    await this.authPost('/email-otp/reset-password', { email, otp, password });
  }

  /** Short-lived (15 min) JWT for the Data API and storage Function, refreshed from the session. */
  async token() {
    if (this.jwt && Date.now() < this.jwtExp - 60_000) return this.jwt;
    const s = this.session();
    if (!s) throw new SyncError('AUTH', 'Please sign in.');
    let res;
    try { res = await fetch(this.authUrl + '/token', { headers: { cookie: s.cookie, origin: this.origin } }); } catch (e) {
      throw new SyncError('NETWORK', `Cannot reach Neon Auth: ${e.message}`);
    }
    if (res.status === 401 || res.status === 403) { this.saveSession(null); throw new SyncError('AUTH', 'Your session expired. Please sign in again.'); }
    if (!res.ok) throw new SyncError('CLOUD', `Token refresh failed (HTTP ${res.status})`);
    const updated = Cloud.cookiesFrom(res, s.cookie);
    if (updated !== s.cookie) this.saveSession({ ...s, cookie: updated });
    const { token } = await res.json();
    const claims = JSON.parse(Buffer.from(token.split('.')[1], 'base64url').toString());
    this.jwt = token; this.jwtExp = claims.exp * 1000;
    return token;
  }

  // ---------- Data API ----------
  async db(method, path, body, extraHeaders = {}) {
    return withRetry(async () => {
      const res = await fetch(this.restUrl + path, {
        method,
        headers: { 'content-type': 'application/json', authorization: `Bearer ${await this.token()}`, ...extraHeaders },
        body: body === undefined ? undefined : JSON.stringify(body),
      });
      const text = await res.text();
      const json = text ? JSON.parse(text) : null;
      if (res.ok) return json;
      // A cold database connection occasionally misses the JWT on the first call. Retry it.
      if (json?.message === 'not signed in') throw new SyncError('RETRY', 'not signed in');
      if (res.status >= 500) throw new SyncError('CLOUD', json?.message || `HTTP ${res.status}`);
      throw translateDbError(res.status, json);
    }, `Database ${method} ${path.split('?')[0]}`, 3);
  }

  listProfiles() { return this.db('GET', '/synced_profiles?select=*&order=name.asc'); }
  async getProfile(id) { return (await this.db('GET', `/synced_profiles?id=eq.${encodeURIComponent(id)}&select=*`))[0] || null; }
  rpc(fn, args) { return this.db('POST', `/rpc/${fn}`, args); }
  claimLock(id, machine, force = false) { return this.rpc('claim_lock', { p_id: id, p_machine: machine, p_force: force }); }
  releaseLock(id, machine, force = false) { return this.rpc('release_lock', { p_id: id, p_machine: machine, p_force: force }); }
  commitVersion(a) {
    return this.rpc('commit_version', {
      p_id: a.id, p_expected: a.expected, p_name: a.name, p_folder: a.folder, p_parts: a.parts, p_size: a.size,
      p_sha: a.sha256, p_prefix: a.prefix, p_machine: a.machine, p_os: a.os, p_keep_lock: a.keepLock,
    });
  }
  deleteProfileRow(id) { return this.db('DELETE', `/synced_profiles?id=eq.${encodeURIComponent(id)}`); }
  async getTeamSetting(key) { return (await this.db('GET', `/team_settings?key=eq.${encodeURIComponent(key)}&select=value`))[0]?.value ?? null; }
  insertTeamSetting(key, value) { return this.db('POST', '/team_settings', { key, value }); }

  // ---------- Object Storage via the storage Function ----------
  async fn(path, body) {
    let res;
    try {
      res = await fetch(this.fnUrl + path, {
        method: 'POST', headers: { 'content-type': 'application/json', authorization: `Bearer ${await this.token()}` }, body: JSON.stringify(body),
      });
    } catch (e) { throw new SyncError('CLOUD', `Storage service unreachable: ${e.message}`); }
    const json = await res.json().catch(() => ({}));
    if (res.status === 401) { this.jwt = null; throw new SyncError('CLOUD', 'storage token rejected'); }
    if (!res.ok) throw new SyncError(res.status >= 500 ? 'CLOUD' : 'STORAGE', json.error || `Storage error (HTTP ${res.status})`);
    return json;
  }

  upload(key, buf) {
    return withRetry(async () => {
      const { urls } = await this.fn('/sign', { items: [{ op: 'put', key }] });
      const res = await fetch(urls[0], { method: 'PUT', headers: { 'content-type': 'application/octet-stream' }, body: buf });
      if (!res.ok) throw new SyncError('CLOUD', `Upload rejected (HTTP ${res.status}): ${(await res.text()).slice(0, 200)}`);
    }, `Upload of ${key}`);
  }
  download(key) {
    return withRetry(async () => {
      const { urls } = await this.fn('/sign', { items: [{ op: 'get', key }] });
      const res = await fetch(urls[0]);
      if (!res.ok) throw new SyncError('CLOUD', `Download failed (HTTP ${res.status})`);
      return Buffer.from(await res.arrayBuffer());
    }, `Download of ${key}`);
  }
  async removeFolder(prefix) {
    const { keys } = await withRetry(() => this.fn('/list', { prefix: `${prefix}/` }), `List ${prefix}`);
    for (let i = 0; i < keys.length; i += 1000) {
      const batch = keys.slice(i, i + 1000);
      await withRetry(() => this.fn('/delete', { keys: batch }), `Delete in ${prefix}`);
    }
  }
  async listFolders(id) {
    const { keys } = await withRetry(() => this.fn('/list', { prefix: `${id}/` }), `List ${id}`);
    return [...new Set(keys.map((k) => k.split('/').slice(0, 2).join('/')))];
  }
}

module.exports = { Cloud, SyncError, deriveUrls };
