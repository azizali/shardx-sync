'use strict';
const $ = (s) => document.querySelector(s);
const esc = (s) => String(s ?? '').replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
const STATUS = {
  'synced': 'In sync', 'local-changes': 'Changed here', 'cloud-newer': 'Newer in cloud', 'conflict': 'Changed on both',
  'unlinked': 'Not linked yet', 'local-only': 'Not uploaded', 'cloud-only': 'Only in cloud',
};
const STATUS_HELP = {
  'synced': 'This computer has the latest cloud version.',
  'local-changes': 'You used this profile here since the last sync. Upload to share it.',
  'cloud-newer': 'Your teammate uploaded a newer version. Download to get it.',
  'conflict': 'Changed here and in the cloud. Pick which copy wins: upload yours or download theirs.',
  'unlinked': 'Exists here and in the cloud but was never synced on this computer. Pick which copy wins.',
  'local-only': 'Only on this computer. Upload to share it with your team.',
  'cloud-only': 'Not on this computer yet. Download to add it to ShardX.',
};
let S = { items: [], selected: null, busy: null, progress: null, state: null, launcherRunning: false };

function show(view) { for (const v of ['setup', 'login', 'main']) $(`#view-${v}`).hidden = v !== view; }
function fmtBytes(n) { if (n == null) return '—'; const u = ['B', 'KB', 'MB', 'GB']; let i = 0; while (n >= 1024 && i < 3) { n /= 1024; i++; } return `${n.toFixed(i ? 1 : 0)} ${u[i]}`; }
function ago(t) {
  if (!t) return 'never'; const s = (Date.now() - new Date(t).getTime()) / 1000;
  if (s < 60) return 'just now'; if (s < 3600) return `${Math.floor(s / 60)} min ago`; if (s < 86400) return `${Math.floor(s / 3600)} h ago`;
  if (s < 86400 * 30) { const d = Math.floor(s / 86400); return `${d} day${d === 1 ? '' : 's'} ago`; } return new Date(t).toLocaleDateString();
}
const when = (t) => (t ? new Date(t).toLocaleString() : '');
const osName = (o) => ({ darwin: 'macOS', win32: 'Windows', linux: 'Linux' }[o] || o || '');
function log(msg, cls = '') { const d = document.createElement('div'); d.className = cls; d.textContent = `${new Date().toLocaleTimeString()}  ${msg}`; $('#log').prepend(d); }

// ---------- boot ----------
async function boot() {
  const r = await api.state(); S.state = r.data;
  if (!S.state.configured) return openSettings(false);
  if (!S.state.user) return show('login');
  show('main');
  $('#who-email').textContent = S.state.user.email;
  $('#who-machine').textContent = S.state.config.machine;
  refresh();
}

// ---------- settings ----------
function openSettings(cancelable = true) {
  const c = S.state.config; const f = $('#setup-form');
  f.supabaseUrl.value = c.supabaseUrl || ''; f.supabaseAnonKey.value = c.supabaseAnonKey || '';
  f.machine.value = c.machine || ''; f.shardxRoot.value = c.shardxRoot || ''; f.skipCaches.checked = c.skipCaches !== false;
  f.passphrase.value = '';
  $('#pass-hint').textContent = c.hasPassphrase ? '(saved. Leave empty to keep it)' : '';
  f.passphrase.required = !c.hasPassphrase;
  $('#root-hint').textContent = c.shardxRootValid ? `Using ${c.shardxRootResolved}` : `Not found: ${c.shardxRootResolved}`;
  $('#setup-cancel').hidden = !cancelable;
  $('#setup-err').textContent = '';
  show('setup');
}
$('#setup-form').addEventListener('submit', async (e) => {
  e.preventDefault(); const f = e.target;
  const r = await api.saveSettings({
    supabaseUrl: f.supabaseUrl.value, supabaseAnonKey: f.supabaseAnonKey.value, passphrase: f.passphrase.value,
    machine: f.machine.value, shardxRoot: f.shardxRoot.value, skipCaches: f.skipCaches.checked,
  });
  if (!r.ok) { $('#setup-err').textContent = r.error.message; return; }
  boot();
});
$('#setup-cancel').onclick = () => boot();
$('#pick-folder').onclick = async () => { const r = await api.pickFolder(); if (r.ok && r.data) $('#setup-form').shardxRoot.value = r.data; };
$('#btn-settings').onclick = async () => { S.state = (await api.state()).data; openSettings(true); };
$('#login-settings').onclick = () => openSettings(true);

// ---------- auth ----------
$('#login-form').addEventListener('submit', async (e) => {
  e.preventDefault(); const f = e.target; $('#login-err').textContent = '';
  const r = await api.signIn(f.email.value.trim(), f.password.value);
  if (!r.ok) { $('#login-err').textContent = r.error.message; return; }
  f.password.value = ''; boot();
});
$('#btn-signout').onclick = async () => { await api.signOut(); S.items = []; S.selected = null; boot(); };

// ---------- list ----------
async function refresh() {
  $('#list-foot').textContent = 'Loading…';
  const r = await api.list();
  if (!r.ok) {
    $('#list-foot').textContent = '';
    log(r.error.message, 'bad');
    if (r.error.code === 'AUTH') return boot();
    if (r.error.code === 'PASSPHRASE' || r.error.code === 'SETUP') { alert(r.error.message); S.state = (await api.state()).data; return openSettings(true); }
    return;
  }
  S.items = r.data.items; S.launcherRunning = r.data.launcherRunning;
  $('#banner-launcher').hidden = !S.launcherRunning;
  $('#list-foot').textContent = `${S.items.length} profiles · ${r.data.root}`;
  renderList(); renderDetail();
}
$('#btn-refresh').onclick = refresh;
$('#search').oninput = renderList;

function renderList() {
  const q = $('#search').value.toLowerCase();
  const items = S.items.filter((i) => !q || i.name.toLowerCase().includes(q) || (i.folder || '').toLowerCase().includes(q));
  $('#list').innerHTML = items.map((i) => {
    const lock = i.remote?.lock ? (i.remote.lock.mine ? ' · checked out by you' : ` · in use by ${i.remote.lock.email}`) : '';
    const sync = i.remote ? `synced ${ago(i.remote.updatedAt)}` : 'not in cloud';
    return `<div class="item ${S.selected === i.id ? 'sel' : ''}" data-id="${esc(i.id)}">
      <div class="name">${esc(i.name)}</div><span class="chip s-${i.status}">${STATUS[i.status]}</span>
      <div class="sub">${i.folder ? esc(i.folder) + ' · ' : ''}${esc(sync)}${esc(lock)}${i.running ? ' · running' : ''}</div></div>`;
  }).join('') || '<div class="muted" style="padding:12px">No profiles</div>';
  for (const el of document.querySelectorAll('.item')) el.onclick = () => { S.selected = el.dataset.id; renderList(); renderDetail(); };
}

// ---------- detail ----------
function renderDetail() {
  const i = S.items.find((x) => x.id === S.selected);
  if (!i) { $('#detail').innerHTML = '<div class="empty muted">Select a profile</div>'; return; }
  const r = i.remote; const lock = r?.lock; const busy = !!S.busy;
  const hasLocal = i.status !== 'cloud-only';
  const lockHtml = !r ? '<span class="muted">Not in cloud yet</span>'
    : !lock ? '<span class="dot"></span><span>Free. Nobody has it checked out.</span>'
    : lock.mine ? `<span class="dot mine"></span><span>Checked out by you on this computer · ${esc(ago(lock.at))}</span>`
    : `<span class="dot other"></span><span>In use by <b>${esc(lock.email)}</b> on ${esc(lock.machine)} · ${esc(ago(lock.at))}</span>`;
  const p = S.progress && S.progress.id === i.id ? S.progress : null;
  $('#detail').innerHTML = `
    <div class="dhead"><h2>${esc(i.name)}</h2><span class="chip s-${i.status}">${STATUS[i.status]}</span></div>
    <div class="dsub">${esc(STATUS_HELP[i.status])}</div>
    ${p ? `<div class="progress"><div class="bar"><i style="width:${p.pct ?? 0}%"></i></div><div class="ptext">${esc(p.step)}${p.detail ? ' · ' + esc(p.detail) : ''}</div></div>` : ''}
    ${i.running ? '<p class="warn">This profile is open in ShardX right now. Close its browser window before syncing.</p>' : ''}
    <div class="grid">
      <div class="stat"><div class="k">Last sync (cloud)</div><div class="v">${r ? esc(ago(r.updatedAt)) : 'Never'}</div>
        <div class="v2">${r ? `v${r.version} by ${esc(r.updatedBy)} on ${esc(r.updatedFrom)} (${esc(osName(r.updatedOs))})<br>${esc(when(r.updatedAt))}` : ''}</div></div>
      <div class="stat"><div class="k">Last sync on this computer</div><div class="v">${esc(ago(i.lastSyncedHere))}</div>
        <div class="v2">${i.localVersion ? `Has v${i.localVersion}` : ''}${i.lastSyncedHere ? ' · ' + esc(when(i.lastSyncedHere)) : ''}</div></div>
      <div class="stat"><div class="k">Size</div><div class="v">${hasLocal ? fmtBytes(i.localBytes) : '—'} <span class="muted" style="font-size:12px">on disk</span></div>
        <div class="v2">${r ? fmtBytes(r.sizeBytes) + ' compressed in cloud' : ''}</div></div>
      <div class="stat"><div class="k">Last opened here</div><div class="v">${hasLocal ? esc(ago(i.lastLaunchedAt)) : '—'}</div><div class="v2">${esc(i.folder || '')}</div></div>
    </div>
    <div class="box"><h3>Upload to cloud</h3><p>Sends everything in this profile from this computer: fingerprint, cookies, logins, bookmarks, history, extensions, storage and proxy.${lock && !lock.mine ? ' <b>Blocked while your teammate has it checked out.</b>' : ''}</p>
      <div class="row"><button class="btn primary" id="a-push" ${!hasLocal || busy || i.running || (lock && !lock.mine) ? 'disabled' : ''}>Upload</button>
      <label class="check"><input type="checkbox" id="a-keep" ${lock?.mine ? 'checked' : ''}> Keep it checked out to me</label></div></div>
    <div class="box"><h3>Download from cloud</h3><p>Replaces this computer's copy with the cloud version. Your current copy is backed up first.</p>
      <div class="row"><button class="btn primary" id="a-pull" ${!r || busy || i.running ? 'disabled' : ''}>Download</button>
      <label class="check"><input type="checkbox" id="a-lock" checked> Check it out to me so my teammate knows I'm using it</label></div></div>
    <div class="box"><h3>Check-out lock</h3><div class="lock" style="margin-bottom:12px">${lockHtml}</div>
      <div class="row">
        ${r && !lock ? `<button class="btn" id="a-take" ${busy ? 'disabled' : ''}>Check out to me</button>` : ''}
        ${lock?.mine ? `<button class="btn" id="a-release" ${busy ? 'disabled' : ''}>Release without uploading</button>` : ''}
        ${lock && !lock.mine ? `<button class="btn danger" id="a-force" ${busy ? 'disabled' : ''}>Force unlock</button>` : ''}
      </div></div>
    <div class="row" style="margin-top:20px">
      <button class="btn ghost" id="a-backups">Open backups folder</button>
      <button class="btn ghost" id="a-shardx">Open ShardX folder</button>
      ${r ? `<button class="btn danger" id="a-delete" ${busy ? 'disabled' : ''}>Remove from cloud</button>` : ''}
    </div>`;
  const on = (id, fn) => { const el = $(id); if (el) el.onclick = fn; };
  on('#a-push', () => doPush(i, { keepLock: $('#a-keep').checked }));
  on('#a-pull', () => doPull(i, { takeLock: $('#a-lock').checked }));
  on('#a-take', () => run(i, 'Check out', () => api.takeLock(i.id, false)));
  on('#a-release', () => run(i, 'Release lock', () => api.releaseLock(i.id, false)));
  on('#a-force', () => confirm(`Force unlock "${i.name}"?\n\n${lock.email} may still be using it. If they upload later, their upload will be blocked until they download again.`)
    && run(i, 'Force unlock', () => api.releaseLock(i.id, true)));
  on('#a-delete', () => confirm(`Remove "${i.name}" from the cloud?\n\nThe copy on this computer stays. Your teammate will no longer be able to download it.`)
    && run(i, 'Remove from cloud', () => api.deleteRemote(i.id)));
  on('#a-backups', () => api.openBackups());
  on('#a-shardx', () => api.openShardx());
}

async function run(i, label, fn) {
  S.busy = i.id; S.progress = { id: i.id, step: label, pct: 0 }; renderDetail();
  const r = await fn();
  S.busy = null;
  if (r.ok) log(`${label}: ${i.name}. Done`, 'ok'); else log(`${label}: ${i.name}. ${r.error.message}`, 'bad');
  S.progress = null;
  await refresh();
  return r;
}

async function doPush(i, opts) {
  let r = await run(i, 'Upload', () => api.push(i.id, opts));
  if (!r.ok && r.error.code === 'CONFIRM_OVERWRITE_CLOUD' && confirm(`${r.error.message}\n\nUpload anyway and replace the cloud copy?`)) {
    r = await run(i, 'Upload', () => api.push(i.id, { ...opts, overwrite: true }));
  }
  if (!r.ok && !r.error.code.startsWith('CONFIRM')) alert(r.error.message);
}

async function doPull(i, opts) {
  if (S.launcherRunning && !confirm('ShardX Launcher is open. It may not see the downloaded profile until you restart it.\n\nContinue?')) return;
  let o = { ...opts };
  for (let tries = 0; tries < 3; tries++) {
    const r = await run(i, 'Download', () => api.pull(i.id, o));
    if (r.ok) {
      if (r.data.crossOs) alert(`Downloaded. This copy was uploaded from ${osName(r.data.fromOs)}. Fingerprint, bookmarks, history and storage carry over, but saved cookies and passwords may not decrypt on a different OS, so some sites may ask you to log in again.`);
      return;
    }
    if (r.error.code === 'CONFIRM_OVERWRITE_LOCAL' && confirm(`${r.error.message}\n\nDownload and replace?`)) { o.overwriteLocal = true; continue; }
    if (r.error.code === 'LOCKED' && confirm(`${r.error.message}\n\nTake it over anyway? Only do this if they are not using it.`)) { o.forceLock = true; continue; }
    if (!r.error.code.startsWith('CONFIRM') && r.error.code !== 'LOCKED') alert(r.error.message);
    return;
  }
}

api.onProgress((p) => { S.progress = p; if (p.id === S.selected) renderDetail(); });
boot();
