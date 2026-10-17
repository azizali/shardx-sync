'use strict';
// Reads ShardX profiles from disk and decides which files belong in a sync.
const fs = require('fs');
const fsp = fs.promises;
const os = require('os');
const path = require('path');
const crypto = require('crypto');
const { execFile } = require('child_process');

// Top-level folders in a Chromium user-data dir that are pure caches or
// component downloads. Chromium re-creates them; skipping them makes syncs ~2-4x smaller.
const SKIP_ROOT_DIRS = new Set([
  'component_crx_cache', 'extensions_crx_cache', 'GraphiteDawnCache', 'GPUPersistentCache', 'GrShaderCache',
  'ShaderCache', 'WidevineCdm', 'WasmTtsEngine', 'OnDeviceHeadSuggestModel', 'OptimizationHints',
  'optimization_guide_model_store', 'Safe Browsing', 'Subresource Filter', 'ZxcvbnData', 'ActorSafetyLists',
  'CertificateRevocation', 'PKIMetadata', 'Crowd Deny', 'FileTypePolicies', 'MEIPreload', 'SSLErrorAssistant',
  'TrustTokenKeyCommitments', 'FirstPartySetsPreloaded', 'OriginTrials', 'PrivacySandboxAttestationsPreloaded',
  'SafetyTips', 'AmountExtractionHeuristicRegexes', 'CaptchaProviders', 'download_cache', 'Crashpad',
  'BrowserMetrics', 'Webstore Downloads', 'screen_ai', 'hyphen-data',
]);
// Cache folders inside a browser profile folder (Default, Profile 1, ...).
const SKIP_PROFILE_DIRS = new Set([
  'Cache', 'Code Cache', 'GPUCache', 'DawnGraphiteCache', 'DawnWebGPUCache', 'GrShaderCache', 'ShaderCache',
]);
// Never sync these: lock files of a running browser and OS junk.
const SKIP_FILES = new Set(['SingletonLock', 'SingletonCookie', 'SingletonSocket', 'lockfile', '.DS_Store', 'Thumbs.db', 'desktop.ini']);

const isBrowserProfileDir = (name) => name === 'Default' || /^Profile \d+$/.test(name) || name === 'Guest Profile' || name === 'System Profile';

/**
 * Decide whether a path (relative to the ShardX root, "/"-separated) is part of the sync.
 * Only called for paths under user-data/<id>; profile json and extension folders are always included.
 */
function includeUserDataPath(rel, isDir, skipCaches) {
  const parts = rel.split('/');
  const name = parts[parts.length - 1];
  if (SKIP_FILES.has(name)) return false;
  if (!skipCaches) return true;
  // parts: ['user-data', '<id>', ...]
  if (parts.length === 3) {
    if (isDir && SKIP_ROOT_DIRS.has(name)) return false;
    if (name.startsWith('BrowserMetrics')) return false;
  }
  if (parts.length === 4 && isDir && isBrowserProfileDir(parts[2]) && SKIP_PROFILE_DIRS.has(name)) return false;
  return true;
}

function readProfileJson(root, id) {
  return JSON.parse(fs.readFileSync(path.join(root, 'profiles', `${id}.json`), 'utf8'));
}

function summarize(id, json) {
  const meta = json._meta || {};
  const launched = typeof meta.last_launched_at === 'string' ? Number(meta.last_launched_at.replace('@', '')) : null;
  return {
    id,
    name: json.name || id,
    folder: meta.folder || '',
    proxyId: meta.proxy_id || null,
    extensions: Array.isArray(meta.extensions) ? meta.extensions : [],
    lastLaunchedAt: launched ? launched * 1000 : null,
    platform: json.navigator?.platform || null,
  };
}

async function listLocalProfiles(root) {
  const dir = path.join(root, 'profiles');
  let files = [];
  try { files = await fsp.readdir(dir); } catch { return []; }
  const out = [];
  for (const f of files) {
    if (!f.endsWith('.json')) continue;
    const id = f.slice(0, -5);
    try { out.push(summarize(id, readProfileJson(root, id))); } catch { /* skip broken file */ }
  }
  return out;
}

/** Every file that goes into the archive, as "/"-separated paths relative to the ShardX root. */
async function collectFiles(root, profile, skipCaches) {
  const files = [];
  const add = async (rel) => {
    try {
      const st = await fsp.lstat(path.join(root, rel));
      if (st.isFile()) files.push({ rel, size: st.size, mtime: Math.floor(st.mtimeMs) });
    } catch { /* vanished */ }
  };
  async function walk(rel, filter) {
    let entries;
    try { entries = await fsp.readdir(path.join(root, rel), { withFileTypes: true }); } catch { return; }
    for (const e of entries) {
      const child = `${rel}/${e.name}`;
      if (e.isSymbolicLink()) continue;
      if (filter && !filter(child, e.isDirectory())) continue;
      if (e.isDirectory()) await walk(child, filter);
      else if (e.isFile()) await add(child);
    }
  }
  await add(`profiles/${profile.id}.json`);
  await walk(`user-data/${profile.id}`, (rel, isDir) => includeUserDataPath(rel, isDir, skipCaches));
  for (const ext of profile.extensions) {
    if (/^[A-Za-z0-9_-]+$/.test(ext)) await walk(`extensions/${ext}`, (rel) => !SKIP_FILES.has(path.posix.basename(rel)));
  }
  files.sort((a, b) => (a.rel < b.rel ? -1 : a.rel > b.rel ? 1 : 0));
  return files;
}

/** Cheap change detector: hash of every synced file's path, size and modified time. */
function fingerprintOf(files) {
  const h = crypto.createHash('sha1');
  for (const f of files) h.update(`${f.rel}|${f.size}|${f.mtime}\n`);
  return h.digest('hex');
}

/** True if this profile's browser window is open right now. */
async function isProfileRunning(root, id) {
  const dir = path.join(root, 'user-data', id);
  if (process.platform === 'win32') {
    const lock = path.join(dir, 'lockfile');
    if (!fs.existsSync(lock)) return false;
    try { fs.closeSync(fs.openSync(lock, 'r+')); return false; } catch { return true; }
  }
  try {
    const target = await fsp.readlink(path.join(dir, 'SingletonLock')); // "hostname-pid"
    const i = target.lastIndexOf('-');
    const host = target.slice(0, i);
    const pid = Number(target.slice(i + 1));
    if (host !== os.hostname() || !pid) return true; // be safe
    try { process.kill(pid, 0); return true; } catch (e) { return e.code === 'EPERM'; }
  } catch { return false; }
}

/** True if the ShardX Launcher app itself is open. */
function isLauncherRunning() {
  return new Promise((resolve) => {
    const cmd = process.platform === 'win32' ? ['tasklist', ['/FO', 'CSV', '/NH']] : ['ps', ['-A', '-o', 'comm=']];
    execFile(cmd[0], cmd[1], { windowsHide: true, maxBuffer: 8 * 1024 * 1024 }, (err, stdout) => {
      if (err) return resolve(false);
      resolve(/shardx[- ]launcher/i.test(stdout));
    });
  });
}

module.exports = {
  listLocalProfiles, readProfileJson, summarize, collectFiles, fingerprintOf,
  includeUserDataPath, isProfileRunning, isLauncherRunning, SKIP_FILES,
};
