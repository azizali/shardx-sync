'use strict';
const test = require('node:test');
const assert = require('node:assert');
const fs = require('fs');
const os = require('os');
const path = require('path');
const crypto = require('crypto');
const { SyncEngine, verifyTeamKey } = require('../src/core/sync');
const { deriveKey } = require('../src/core/crypto');
const A = require('../src/core/archive');
const { FakeBackend, FakeCloud } = require('./fake-cloud');

const ID = '11111111-2222-3333-4444-555555555555';
const EXT = 'abcdef0123456789';
const tmp = () => fs.mkdtempSync(path.join(os.tmpdir(), 'sxs-'));
const w = (root, rel, data) => { const p = path.join(root, rel); fs.mkdirSync(path.dirname(p), { recursive: true }); fs.writeFileSync(p, data); };

function makeShardx(name = 'Alice Shop') {
  const root = tmp();
  w(root, `profiles/${ID}.json`, JSON.stringify({ _meta: { id: ID, folder: 'Clients', proxy_id: 'px1', extensions: [EXT] }, name, navigator: { platform: 'macOS' } }));
  w(root, 'proxies.json', JSON.stringify({ proxies: [{ id: 'px1', host: '1.2.3.4', port: 1080, password: 'secret' }] }));
  w(root, `user-data/${ID}/Default/Cookies`, 'cookie-db');
  w(root, `user-data/${ID}/Default/Bookmarks`, '{"roots":{}}');
  w(root, `user-data/${ID}/Default/Local Storage/leveldb/000003.log`, crypto.randomBytes(5000));
  w(root, `user-data/${ID}/Default/Cache/Cache_Data/data_0`, 'cache-junk');
  w(root, `user-data/${ID}/component_crx_cache/x.crx`, 'junk');
  w(root, `user-data/${ID}/fingerprint.json`, '{"fp":1}');
  w(root, `user-data/${ID}/Local State`, '{}');
  w(root, `extensions/${EXT}/manifest.json`, '{"name":"ext"}');
  return root;
}

function engine(backend, root, user, machine, pass = 'team-pass') {
  const key = deriveKey(pass, 'https://x.supabase.co');
  const cloud = new FakeCloud(backend, user);
  return { e: new SyncEngine({ cloud, key, root, workDir: tmp(), machine, user, skipCaches: true }), cloud, key };
}
const alice = { id: 'u-alice', email: 'alice@team' };
const bob = { id: 'u-bob', email: 'bob@team' };

test('upload on one computer, download on another, round trip is exact', async () => {
  const b = new FakeBackend();
  const rootA = makeShardx(); const rootB = tmp(); fs.mkdirSync(path.join(rootB, 'profiles'));
  const A1 = engine(b, rootA, alice, 'mac-a'); const B1 = engine(b, rootB, bob, 'win-b');
  await verifyTeamKey(A1.cloud, A1.key);
  await verifyTeamKey(B1.cloud, B1.key);

  const row = await A1.e.push(ID);
  assert.equal(row.version, 1);
  assert.equal((await A1.e.overview())[0].status, 'synced');
  // stored data is encrypted
  for (const buf of b.files.values()) assert.ok(!buf.includes(Buffer.from('cookie-db')) && !buf.includes(Buffer.from('secret')));

  const ov = await B1.e.overview();
  assert.equal(ov[0].status, 'cloud-only');
  await B1.e.pull(ID);
  for (const rel of [`profiles/${ID}.json`, `user-data/${ID}/Default/Cookies`, `user-data/${ID}/Default/Bookmarks`,
    `user-data/${ID}/Default/Local Storage/leveldb/000003.log`, `user-data/${ID}/fingerprint.json`, `extensions/${EXT}/manifest.json`]) {
    assert.deepEqual(fs.readFileSync(path.join(rootB, rel)), fs.readFileSync(path.join(rootA, rel)), rel);
  }
  assert.ok(!fs.existsSync(path.join(rootB, `user-data/${ID}/Default/Cache`)), 'caches skipped');
  assert.ok(!fs.existsSync(path.join(rootB, `user-data/${ID}/component_crx_cache`)), 'component cache skipped');
  const proxies = JSON.parse(fs.readFileSync(path.join(rootB, 'proxies.json'), 'utf8')).proxies;
  assert.equal(proxies[0].password, 'secret', 'proxy merged');
  const rowB = await B1.cloud.getProfile(ID);
  assert.equal(rowB.locked_by_email, 'bob@team', 'download checks out to bob');
  assert.equal((await B1.e.overview())[0].status, 'synced');
});

test('locks, conflicts and backups', async () => {
  const b = new FakeBackend();
  const rootA = makeShardx(); const rootB = tmp(); fs.mkdirSync(path.join(rootB, 'profiles'));
  const A1 = engine(b, rootA, alice, 'mac-a'); const B1 = engine(b, rootB, bob, 'win-b');
  await A1.e.push(ID);
  await B1.e.pull(ID); // bob holds the lock

  // Alice cannot upload while Bob has it checked out
  await assert.rejects(A1.e.push(ID, { overwrite: true }), { code: 'LOCKED' });
  // Alice cannot download either without forcing
  await assert.rejects(A1.e.pull(ID), { code: 'LOCKED' });

  // Bob uses it and uploads (releases lock)
  w(rootB, `user-data/${ID}/Default/Bookmarks`, '{"roots":{"bob":1}}');
  assert.equal((await B1.e.overview())[0].status, 'local-changes');
  const v2 = await B1.e.push(ID);
  assert.equal(v2.version, 2); assert.equal(v2.locked_by, null);

  // Alice sees cloud-newer; if she had also changed it, it is a conflict
  assert.equal((await A1.e.overview())[0].status, 'cloud-newer');
  w(rootA, `user-data/${ID}/Default/History`, 'alice-history');
  assert.equal((await A1.e.overview())[0].status, 'conflict');
  await assert.rejects(A1.e.push(ID), { code: 'CONFIRM_OVERWRITE_CLOUD' });
  await assert.rejects(A1.e.pull(ID), { code: 'CONFIRM_OVERWRITE_LOCAL' });

  // Alice takes Bob's version; her copy goes to backups
  await A1.e.pull(ID, { overwriteLocal: true });
  assert.equal(fs.readFileSync(path.join(rootA, `user-data/${ID}/Default/Bookmarks`), 'utf8'), '{"roots":{"bob":1}}');
  assert.ok(!fs.existsSync(path.join(rootA, `user-data/${ID}/Default/History`)));
  const bdir = path.join(A1.e.backupDir, ID);
  const [snap] = fs.readdirSync(bdir);
  assert.equal(fs.readFileSync(path.join(bdir, snap, `user-data/${ID}/Default/History`), 'utf8'), 'alice-history');

  // Old cloud versions are cleaned up: only one version folder remains
  assert.equal((await A1.cloud.listFolders(ID)).length, 1);
});

test('concurrent uploads: second one loses cleanly with CONFLICT and leaves no garbage', async () => {
  const b = new FakeBackend();
  const rootA = makeShardx(); const A1 = engine(b, rootA, alice, 'mac-a');
  await A1.e.push(ID);
  const B1 = engine(b, makeShardx(), bob, 'win-b');
  // Simulate: bob's engine thinks v1 is base, but alice publishes v2 between bob's check and commit.
  const realCommit = B1.cloud.commitVersion.bind(B1.cloud);
  B1.cloud.commitVersion = async (a) => {
    w(rootA, `user-data/${ID}/Default/Bookmarks`, 'changed'); await A1.e.push(ID);
    return realCommit(a);
  };
  B1.e.state.set(ID, { version: 1, fingerprint: 'x', at: '' });
  await assert.rejects(B1.e.push(ID), { code: 'CONFLICT' });
  assert.equal((await A1.cloud.listFolders(ID)).length, 1, 'bob\'s partial upload removed');
});

test('wrong team passphrase is rejected', async () => {
  const b = new FakeBackend();
  const A1 = engine(b, makeShardx(), alice, 'mac-a', 'right');
  await verifyTeamKey(A1.cloud, A1.key);
  const B1 = engine(b, tmp(), bob, 'win-b', 'wrong');
  await assert.rejects(verifyTeamKey(B1.cloud, B1.key), { code: 'PASSPHRASE' });
});

test('large profile is split into multiple encrypted parts', async () => {
  const b = new FakeBackend();
  const root = makeShardx();
  w(root, `user-data/${ID}/Default/IndexedDB/big.blob`, crypto.randomBytes(A.PART_SIZE + 3 * 1024 * 1024)); // incompressible
  const A1 = engine(b, root, alice, 'mac-a');
  const row = await A1.e.push(ID);
  assert.equal(row.parts, 2);
  const root2 = tmp(); fs.mkdirSync(path.join(root2, 'profiles'));
  const B1 = engine(b, root2, bob, 'b');
  await B1.e.pull(ID);
  assert.deepEqual(fs.readFileSync(path.join(root2, `user-data/${ID}/Default/IndexedDB/big.blob`)), fs.readFileSync(path.join(root, `user-data/${ID}/Default/IndexedDB/big.blob`)));
});

test('archive extraction ignores paths outside the profile', async () => {
  const src = tmp();
  w(src, `profiles/${ID}.json`, '{}'); w(src, 'proxies.json', 'evil'); w(src, 'user-data/other-id/x', 'evil');
  const file = path.join(tmp(), 'a.tar.gz');
  await A.packToFile(src, [{ rel: `profiles/${ID}.json` }, { rel: 'proxies.json' }, { rel: 'user-data/other-id/x' }], file);
  const dest = tmp();
  const { rejected } = await A.extractProfile(file, dest, ID, []);
  assert.equal(rejected, 2);
  assert.ok(fs.existsSync(path.join(dest, `profiles/${ID}.json`)));
  assert.ok(!fs.existsSync(path.join(dest, 'proxies.json')));
});
