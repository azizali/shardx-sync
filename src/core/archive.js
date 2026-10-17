'use strict';
// Packs a profile into a .tar.gz, splits it into encrypted parts, and the reverse.
const fs = require('fs');
const fsp = fs.promises;
const path = require('path');
const crypto = require('crypto');
const tar = require('tar');
const { encrypt, decrypt } = require('./crypto');

// Supabase free plan caps each uploaded file at 50 MB. Stay well under it.
const PART_SIZE = 40 * 1024 * 1024;

async function packToFile(root, files, outFile) {
  await tar.c({ gzip: { level: 6 }, file: outFile, cwd: root, portable: true, noDirRecurse: true }, files.map((f) => f.rel));
  const st = await fsp.stat(outFile);
  return { size: st.size, sha256: await sha256File(outFile) };
}

async function sha256File(file) {
  const h = crypto.createHash('sha256');
  for await (const chunk of fs.createReadStream(file)) h.update(chunk);
  return h.digest('hex');
}

const partAad = (id, prefix, i) => `${id}|${prefix}|part|${i}`;
const metaAad = (id, prefix) => `${id}|${prefix}|meta`;

/** Calls onPart(index, encryptedBuffer) for each part; returns the part count. */
async function forEachEncryptedPart(key, file, id, prefix, onPart) {
  const fh = await fsp.open(file, 'r');
  try {
    const { size } = await fh.stat();
    const count = Math.max(1, Math.ceil(size / PART_SIZE));
    for (let i = 0; i < count; i++) {
      const len = Math.min(PART_SIZE, size - i * PART_SIZE);
      const buf = Buffer.alloc(len);
      await fh.read(buf, 0, len, i * PART_SIZE);
      await onPart(i, encrypt(key, buf, partAad(id, prefix, i)), count);
    }
    return count;
  } finally { await fh.close(); }
}

/**
 * Extracts an archive into destDir, accepting only paths that belong to this profile.
 * Protects against a corrupted or hostile archive writing anywhere else.
 */
async function extractProfile(file, destDir, profileId, extensionIds) {
  const extSet = new Set(extensionIds);
  const allowed = (p) => {
    const rel = p.replace(/\\/g, '/').replace(/^\.\//, '');
    if (rel.split('/').includes('..') || path.isAbsolute(rel)) return false;
    if (rel === `profiles/${profileId}.json`) return true;
    if (rel.startsWith(`user-data/${profileId}/`)) return true;
    const m = rel.match(/^extensions\/([A-Za-z0-9_-]+)\//);
    return !!(m && extSet.has(m[1]));
  };
  let rejected = 0;
  await fsp.mkdir(destDir, { recursive: true });
  await tar.x({
    file, cwd: destDir, strict: true,
    filter: (p, entry) => {
      const ok = (entry.type === 'File' || entry.type === 'Directory') && allowed(p);
      if (!ok) rejected++;
      return ok;
    },
  });
  return { rejected };
}

module.exports = { PART_SIZE, packToFile, sha256File, forEachEncryptedPart, extractProfile, partAad, metaAad };
