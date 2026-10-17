// Live integration test against a real Neon project. Not part of `npm test`.
// Usage: LIVE_A_EMAIL=.. LIVE_A_PASS=.. LIVE_B_EMAIL=.. LIVE_B_PASS=.. node test/live-neon.js
// Uses the built-in defaults (src/core/defaults.js). Cleans up the cloud rows and files it creates.
'use strict';
const assert = require('node:assert');
const fs = require('fs'); const os = require('os'); const path = require('path'); const crypto = require('crypto');
const { Cloud } = require('../src/core/cloud');
const { SyncEngine, verifyTeamKey } = require('../src/core/sync');
const { deriveKey } = require('../src/core/crypto');
const DEFAULTS = require('../src/core/defaults');

const ID = crypto.randomUUID();
const tmp = () => fs.mkdtempSync(path.join(os.tmpdir(), 'sxs-live-'));
const w = (root, rel, data) => { const p = path.join(root, rel); fs.mkdirSync(path.dirname(p), { recursive: true }); fs.writeFileSync(p, data); };
const memStore = () => { const m = new Map(); return { getItem: (k) => m.get(k) ?? null, setItem: (k, v) => m.set(k, v), removeItem: (k) => m.delete(k) }; };
const step = (s) => console.log(`\n▶ ${s}`);

async function engineFor(email, pass, machine, root, key) {
  const cloud = new Cloud(DEFAULTS, memStore());
  const user = await cloud.signIn(email, pass);
  await verifyTeamKey(cloud, key);
  return new SyncEngine({ cloud, key, root, workDir: tmp(), machine, user, skipCaches: true, onProgress: (p) => p.pct === 100 && console.log('  ', p.step) });
}

(async () => {
  const key = deriveKey('live-test-passphrase-' + ID, DEFAULTS.neonUrl);
  const rootA = tmp(); const rootB = tmp(); fs.mkdirSync(path.join(rootB, 'profiles'));
  w(rootA, `profiles/${ID}.json`, JSON.stringify({ _meta: { id: ID, folder: 'Live' }, name: 'Live Test Profile' }));
  w(rootA, `user-data/${ID}/Default/Cookies`, 'cookie-db');
  w(rootA, `user-data/${ID}/Default/IndexedDB/big.blob`, crypto.randomBytes(45 * 1024 * 1024)); // forces 2 parts

  // The team key check row is per passphrase; use a throwaway key name so the real team's row is untouched.
  const realGet = Cloud.prototype.getTeamSetting; const realIns = Cloud.prototype.insertTeamSetting;
  Cloud.prototype.getTeamSetting = function (k) { return realGet.call(this, `${k}-live-${ID}`); };
  Cloud.prototype.insertTeamSetting = function (k, v) { return realIns.call(this, `${k}-live-${ID}`, v); };

  const A = await engineFor(process.env.LIVE_A_EMAIL, process.env.LIVE_A_PASS, 'live-mac', rootA, key);
  const B = await engineFor(process.env.LIVE_B_EMAIL, process.env.LIVE_B_PASS, 'live-win', rootB, key);
  try {
    step('A uploads (2 parts)'); let t = Date.now();
    const row = await A.push(ID);
    assert.equal(row.version, 1); assert.equal(row.parts, 2);
    console.log(`   ${((Date.now() - t) / 1000).toFixed(1)}s`);

    step('B sees it and downloads it'); t = Date.now();
    assert.equal((await B.overview()).find((i) => i.id === ID).status, 'cloud-only');
    await B.pull(ID);
    console.log(`   ${((Date.now() - t) / 1000).toFixed(1)}s`);
    for (const rel of [`profiles/${ID}.json`, `user-data/${ID}/Default/Cookies`, `user-data/${ID}/Default/IndexedDB/big.blob`]) {
      assert.ok(fs.readFileSync(path.join(rootA, rel)).equals(fs.readFileSync(path.join(rootB, rel))), rel);
    }
    const ovB = (await B.overview()).find((i) => i.id === ID);
    assert.equal(ovB.status, 'synced'); assert.equal(ovB.remote.lock.mine, true);

    step('A is blocked while B has it checked out');
    await assert.rejects(A.push(ID, { overwrite: true }), { code: 'LOCKED' });
    await assert.rejects(A.pull(ID), { code: 'LOCKED' });

    step('B changes and uploads v2, releasing the lock');
    w(rootB, `user-data/${ID}/Default/Bookmarks`, '{"bob":1}');
    const v2 = await B.push(ID);
    assert.equal(v2.version, 2); assert.equal(v2.locked_by, null);
    assert.equal((await A.cloud.listFolders(ID)).length, 1, 'old version cleaned up');

    step('A sees cloud-newer and downloads');
    assert.equal((await A.overview()).find((i) => i.id === ID).status, 'cloud-newer');
    await A.pull(ID, { takeLock: false });
    assert.equal(fs.readFileSync(path.join(rootA, `user-data/${ID}/Default/Bookmarks`), 'utf8'), '{"bob":1}');

    step('Wrong passphrase is rejected');
    const bad = new Cloud(DEFAULTS, memStore()); await bad.signIn(process.env.LIVE_A_EMAIL, process.env.LIVE_A_PASS);
    await assert.rejects(verifyTeamKey(bad, deriveKey('wrong', DEFAULTS.neonUrl)), { code: 'PASSPHRASE' });

    console.log('\n✔ LIVE TEST PASSED');
  } finally {
    step('Cleanup');
    await A.deleteFromCloud(ID).catch((e) => console.log('   cleanup error', e.message));
    assert.equal(await A.cloud.getProfile(ID), null);
    assert.equal((await A.cloud.listFolders(ID)).length, 0);
    console.log('   cloud row and files removed');
    await A.cloud.signOut(); await B.cloud.signOut();
  }
})().catch((e) => { console.error('\n✖ LIVE TEST FAILED:', e.code || '', e.message); process.exit(1); });
