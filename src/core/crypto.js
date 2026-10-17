'use strict';
// AES-256-GCM with a key derived from the team passphrase. The cloud only ever sees ciphertext.
const crypto = require('crypto');

const MAGIC = Buffer.from('SXS1');

function deriveKey(passphrase, salt) {
  return crypto.scryptSync(String(passphrase), `shardx-sync:${salt}`, 32, { N: 1 << 15, r: 8, p: 1, maxmem: 128 * 1024 * 1024 });
}

function encrypt(key, plain, aad) {
  const iv = crypto.randomBytes(12);
  const c = crypto.createCipheriv('aes-256-gcm', key, iv);
  c.setAAD(Buffer.from(aad));
  const body = Buffer.concat([c.update(plain), c.final()]);
  return Buffer.concat([MAGIC, iv, body, c.getAuthTag()]);
}

function decrypt(key, blob, aad) {
  if (blob.length < 32 || !blob.subarray(0, 4).equals(MAGIC)) throw new Error('Not a ShardX Sync file');
  const iv = blob.subarray(4, 16);
  const tag = blob.subarray(blob.length - 16);
  const d = crypto.createDecipheriv('aes-256-gcm', key, iv);
  d.setAAD(Buffer.from(aad));
  d.setAuthTag(tag);
  try {
    return Buffer.concat([d.update(blob.subarray(16, blob.length - 16)), d.final()]);
  } catch {
    throw new Error('Could not decrypt. The team passphrase does not match the one used to upload.');
  }
}

module.exports = { deriveKey, encrypt, decrypt };
