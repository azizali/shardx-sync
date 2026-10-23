'use strict';
const $ = (s) => document.querySelector(s);
const $$ = (s) => document.querySelectorAll(s);
const esc = (s) => String(s ?? '').replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
const STATUS = {
  'synced': 'Synced', 'local-changes': 'Changed here', 'cloud-newer': 'Newer in cloud', 'conflict': 'Conflict',
  'unlinked': 'Not linked', 'local-only': 'Local only', 'cloud-only': 'Cloud only', 'deleted': 'Deleted',
};
const UNFILED = '\u0000unfiled';
let S = {
  items: [], selected: null, picked: new Set(), jobs: {}, state: null, launcherRunning: false,
  role: '', team: null, folder: '', status: '', sort: { key: 'name', dir: 1 },
};
try { S.sort = { ...S.sort, ...JSON.parse(localStorage.getItem('sort') || '{}') }; } catch { /* storage unavailable */ }
const isAdmin = () => S.role === 'admin';
const active = (m) => m.role !== 'admin' && m.status !== 'removed'; // members who can be given access
const plural = (n, one, many) => `${n} ${n === 1 ? one : many}`;
const WORKSPACE_NOTICE = 'Uploading makes this profile part of the workspace. Admins can see it, and it is removed from this computer if your access ends.';
const statusOf = (i) => (i.remote?.deleted ? 'deleted' : i.status);
const byId = (id) => S.items.find((x) => x.id === id);

function show(view) { for (const v of ['setup', 'login', 'main']) $(`#view-${v}`).hidden = v !== view; }
function fmtBytes(n) { if (n == null) return '—'; const u = ['B', 'KB', 'MB', 'GB']; let i = 0; while (n >= 1024 && i < 3) { n /= 1024; i++; } return `${n.toFixed(i ? 1 : 0)} ${u[i]}`; }
function ago(t) {
  if (!t) return 'never'; const s = (Date.now() - new Date(t).getTime()) / 1000;
  if (s < 60) return 'just now'; if (s < 3600) return `${Math.floor(s / 60)}m ago`; if (s < 86400) return `${Math.floor(s / 3600)}h ago`;
  if (s < 86400 * 30) return `${Math.floor(s / 86400)}d ago`; return new Date(t).toLocaleDateString();
}
const osName = (o) => ({ darwin: 'macOS', win32: 'Windows', linux: 'Linux' }[o] || o || '');
const handle = (email) => String(email || '').split('@')[0];
const initials = (email) => handle(email).slice(0, 2).toUpperCase();
// The Wails macOS webview does not implement window.confirm or window.alert: confirm returns false at once
// and alert shows nothing. These show the app's own dialog instead.
function dialog(message, cancelable) {
  return new Promise((resolve) => {
    const ok = $('#modal-ok'); const cancel = $('#modal-cancel');
    $('#modal-msg').textContent = message;
    cancel.hidden = !cancelable;
    $('#modal').hidden = false; ok.focus();
    const done = (v) => { $('#modal').hidden = true; document.removeEventListener('keydown', key, true); resolve(v); };
    const key = (e) => {
      if (e.key === 'Escape') { e.preventDefault(); e.stopPropagation(); done(false); }
      if (e.key === 'Enter') { e.preventDefault(); done(true); }
    };
    ok.onclick = () => done(true); cancel.onclick = () => done(false);
    document.addEventListener('keydown', key, true);
  });
}
const ask = (message) => dialog(message, true);
const tell = (message) => dialog(message, false);
function log(msg, cls = '') { const d = document.createElement('div'); d.className = cls; d.textContent = `${new Date().toLocaleTimeString()}  ${msg}`; $('#log').prepend(d); }

// ---------- boot ----------
async function boot() {
  const r = await api.state(); S.state = r.data;
  if (!S.state.configured) return openSettings(false);
  const off = S.state.offboard;
  if (off) {
    showLogin(false);
    $('#login-err').textContent = off.done
      ? `Your access to this workspace was removed.${off.removed?.length ? ` ${plural(off.removed.length, 'profile was', 'profiles were')} removed from this computer.` : ''}`
      : off.pending?.length
        ? `Your access was removed. Close ${off.pending.join(', ')} in ShardX, then reopen ShardX Sync to finish.`
        : `Your access was removed, but this computer could not finish removing its profiles${off.error ? `: ${off.error}` : '.'}`;
    return;
  }
  if (!S.state.user) return showLogin(false);
  show('main');
  $('#who-email').textContent = S.state.user.email;
  $('#who-machine').textContent = S.state.config.machine;
  S.role = S.state.role || '';
  $('#who-role').hidden = !S.role;
  $('#who-role').textContent = isAdmin() ? 'Admin' : 'Member';
  $('#tab-access').hidden = !isAdmin();
  document.body.classList.toggle('is-member', !isAdmin());
  if (!isAdmin()) showTab('profiles');
  if (isAdmin()) loadTeam();
  refresh();
}

// ---------- settings ----------
function openSettings(cancelable = true) {
  const c = S.state.config; const f = $('#setup-form');
  f.neonUrl.value = c.neonUrl || ''; f.storageUrl.value = c.storageUrl || '';
  f.machine.value = c.machine || ''; f.shardxRoot.value = c.shardxRoot || ''; f.skipCaches.checked = c.skipCaches !== false;
  f.passphrase.value = '';
  $('#pass-hint').textContent = c.hasPassphrase ? 'saved, leave empty to keep' : '';
  f.passphrase.required = !c.hasPassphrase;
  $('#root-hint').textContent = c.shardxRootValid ? c.shardxRootResolved : `not found: ${c.shardxRootResolved}`;
  $('#setup-cancel').hidden = !cancelable;
  $('#setup-err').textContent = '';
  show('setup');
}
$('#setup-form').addEventListener('submit', async (e) => {
  e.preventDefault(); const f = e.target;
  const r = await api.saveSettings({
    neonUrl: f.neonUrl.value, storageUrl: f.storageUrl.value, passphrase: f.passphrase.value,
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
function showLogin(reset) { show('login'); $('#login-form').hidden = !!reset; $('#reset-form').hidden = !reset; }
$('#login-reset').onclick = () => { $('#reset-form').email.value = $('#login-form').email.value; $('#reset-err').textContent = ''; $('#reset-info').textContent = ''; showLogin(true); };
$('#reset-back').onclick = () => showLogin(false);
$('#reset-send').onclick = async () => {
  const f = $('#reset-form'); $('#reset-err').textContent = ''; $('#reset-info').textContent = '';
  if (!f.email.value.trim()) { $('#reset-err').textContent = 'Enter your email first.'; return; }
  const r = await api.sendCode(f.email.value.trim());
  if (!r.ok) { $('#reset-err').textContent = r.error.message; return; }
  $('#reset-info').textContent = 'Code sent if the account exists.';
};
$('#reset-form').addEventListener('submit', async (e) => {
  e.preventDefault(); const f = e.target; $('#reset-err').textContent = '';
  const email = f.email.value.trim();
  let r = await api.resetPassword(email, f.otp.value.trim(), f.password.value);
  if (!r.ok) { $('#reset-err').textContent = r.error.message; return; }
  r = await api.signIn(email, f.password.value);
  f.password.value = ''; f.otp.value = '';
  if (!r.ok) { $('#reset-err').textContent = r.error.message; return; }
  boot();
});
$('#btn-signout').onclick = async () => {
  await api.signOut(); S.items = []; S.selected = null; S.picked.clear(); S.role = ''; S.team = null; boot();
};

// ---------- tabs ----------
function showTab(tab) {
  for (const t of $$('.tab')) t.classList.toggle('on', t.dataset.tab === tab);
  $('#pane-profiles').hidden = tab !== 'profiles';
  $('#pane-access').hidden = tab !== 'access';
  if (tab === 'access') { S.selected = null; renderDetail(); loadTeam(); }
}
for (const t of $$('.tab')) t.onclick = () => showTab(t.dataset.tab);

// ---------- data ----------
async function refresh() {
  const r = await api.list();
  if (!r.ok) {
    log(r.error.message, 'bad');
    if (r.error.code === 'AUTH') return boot();
    if (r.error.code === 'PASSPHRASE' || r.error.code === 'SETUP') { tell(r.error.message); S.state = (await api.state()).data; return openSettings(true); }
    return;
  }
  S.items = r.data.items; S.launcherRunning = r.data.launcherRunning;
  S.picked = new Set([...S.picked].filter(byId));
  $('#banner-launcher').hidden = !S.launcherRunning;
  const { removed = [], pending = [], enforceError } = r.data;
  if (removed.length) log(`Removed ${plural(removed.length, 'profile', 'profiles')} you no longer have access to: ${removed.join(', ')}`, 'bad');
  if (enforceError) log(`Could not check for ended access: ${enforceError}`, 'bad');
  const notes = [];
  if (removed.length) notes.push(`Removed ${plural(removed.length, 'profile', 'profiles')} you no longer have access to.`);
  if (pending.length) notes.push(`Access to ${pending.join(', ')} ended. Close ${pending.length === 1 ? 'it' : 'them'} in ShardX to finish removing.`);
  if (notes.length) { $('#banner-removed').textContent = notes.join(' '); $('#banner-removed').hidden = false; }
  $('#count').title = r.data.root;
  renderAll();
}
$('#btn-refresh').onclick = refresh;
$('#banner-removed').onclick = () => { $('#banner-removed').hidden = true; };

async function loadTeam() {
  if (!isAdmin()) return;
  const r = await api.team();
  if (r.ok) S.team = r.data; else log(r.error.message, 'bad');
  renderAll();
}

function renderAll() { renderFilters(); renderTable(); renderDetail(); renderAccess(); }

// ---------- profiles table ----------
function renderFilters() {
  const folders = [...new Set(S.items.map((i) => i.folder).filter(Boolean))].sort();
  if (S.folder && S.folder !== UNFILED && !folders.includes(S.folder)) S.folder = '';
  $('#f-folder').innerHTML = `<option value="">All folders</option>${folders.map((f) => `<option value="${esc(f)}">${esc(f)}</option>`).join('')}
    <option value="${UNFILED}">Unfiled</option>`;
  $('#f-folder').value = S.folder;
  const present = new Set(S.items.map(statusOf));
  $('#f-status').innerHTML = `<option value="">Any status</option>${Object.keys(STATUS).filter((k) => present.has(k) || k === S.status)
    .map((k) => `<option value="${k}">${STATUS[k]}</option>`).join('')}`;
  $('#f-status').value = S.status;
}
$('#f-folder').onchange = (e) => { S.folder = e.target.value; renderTable(); };
$('#f-status').onchange = (e) => { S.status = e.target.value; renderTable(); };
$('#search').oninput = () => renderTable();

// Sort values per column. null means "no value" and always sorts last, whichever way the column is sorted.
const STATUS_ORDER = Object.keys(STATUS);
const SORT = {
  name: (i) => i.name.toLowerCase(),
  folder: (i) => (i.folder ? i.folder.toLowerCase() : null),
  status: (i) => STATUS_ORDER.indexOf(statusOf(i)),
  cloud: (i) => (i.remote?.updatedAt ? new Date(i.remote.updatedAt).getTime() : null),
  size: (i) => (i.status !== 'cloud-only' ? i.localBytes : i.remote?.sizeBytes) ?? null,
  lock: (i) => (i.remote?.lock ? (i.remote.lock.mine ? '' : i.remote.lock.email.toLowerCase()) : null),
};

function visibleItems() {
  const q = $('#search').value.trim().toLowerCase();
  const rows = S.items.filter((i) => (!q || i.name.toLowerCase().includes(q) || (i.folder || '').toLowerCase().includes(q))
    && (!S.folder || (S.folder === UNFILED ? !i.folder : i.folder === S.folder))
    && (!S.status || statusOf(i) === S.status));
  const val = SORT[S.sort.key] || SORT.name; const dir = S.sort.dir;
  return rows.sort((a, b) => {
    const x = val(a); const y = val(b);
    if (x === null || y === null) { if (x !== y) return x === null ? 1 : -1; }
    else if (x !== y) return (x < y ? -1 : 1) * dir;
    return SORT.name(a) < SORT.name(b) ? -1 : SORT.name(a) > SORT.name(b) ? 1 : 0;
  });
}

$('.profiles thead').addEventListener('click', (e) => {
  const th = e.target.closest('th[data-sort]'); if (!th) return;
  const key = th.dataset.sort;
  S.sort = S.sort.key === key ? { key, dir: -S.sort.dir } : { key, dir: key === 'cloud' || key === 'size' ? -1 : 1 };
  try { localStorage.setItem('sort', JSON.stringify(S.sort)); } catch { /* storage unavailable */ }
  renderTable();
});

function accessCell(i) {
  if (!S.team) return '';
  const emails = (S.team.grants.profiles[i.id] || []).map((u) => S.team.members.find((m) => m.user_id === u)?.email).filter(Boolean);
  const viaFolder = i.folder ? (S.team.grants.folders[i.folder] || []).length : 0;
  const title = [emails.length ? `Shared with ${emails.join(', ')}` : 'Not shared directly', viaFolder ? `+ ${viaFolder} via folder` : ''].filter(Boolean).join(' ');
  const avatars = emails.slice(0, 3).map((e) => `<span class="av">${esc(initials(e))}</span>`).join('');
  const more = emails.length > 3 ? `<span class="muted">+${emails.length - 3}</span>` : '';
  const empty = !emails.length ? `<span class="muted">${viaFolder ? `folder · ${viaFolder}` : 'Share'}</span>` : '';
  return `<button class="access-btn" data-open="${esc(i.id)}" title="${esc(title)}">${avatars}${more}${empty}</button>`;
}

// ---------- jobs: uploads, downloads and lock changes running now, one per profile ----------
// Why an action is unavailable for a profile, or '' when it can run.
function blocked(i, action) {
  const r = i.remote; const lock = r?.lock;
  if (S.jobs[i.id]) return 'Syncing now';
  if (action !== 'push' && action !== 'pull') return '';
  if (i.running) return 'Open in ShardX. Close its window first.';
  if (r?.deleted) return 'Deleted in the cloud';
  if (action === 'push' && i.status === 'cloud-only') return 'Not on this computer';
  if (action === 'push' && lock && !lock.mine) return `Checked out by ${handle(lock.email)} on ${lock.machine}`;
  if (action === 'pull' && !r) return 'Not uploaded yet';
  return '';
}
// Disabled buttons do not show tooltips reliably, so the reason sits on a wrapper.
function btn(i, action, label, attrs) {
  const why = blocked(i, action);
  return `<span title="${esc(why)}"><button class="btn sm" ${attrs} ${why ? 'disabled' : ''}>${label}</button></span>`;
}
const progressText = (j) => (j.pct ? `${j.pct}% · ${j.step}` : j.step);
function actionsCell(i) {
  const j = S.jobs[i.id];
  if (j) {
    return `<div class="job" title="${esc(j.detail || progressText(j))}"><div class="bar"><i style="width:${j.pct || 0}%"></i></div>
      <span>${esc(progressText(j))}</span></div>`;
  }
  return `<div class="acts">${btn(i, 'push', 'Upload', `data-push="${esc(i.id)}"`)}${btn(i, 'pull', 'Download', `data-pull="${esc(i.id)}"`)}</div>`;
}
const progressHtml = (j) => `<div class="bar"><i style="width:${j.pct ?? 0}%"></i></div>
  <div class="ptext">${esc(j.step)}${j.detail ? ' · ' + esc(j.detail) : ''}</div>`;
function renderJobs() {
  const all = Object.values(S.jobs); const queued = all.filter((j) => j.step === 'Queued').length; const active = all.length - queued;
  $('#jobs').hidden = !all.length;
  $('#jobs').innerHTML = `<i class="spin"></i>${[active ? `${active} syncing` : '', queued ? `${queued} queued` : ''].filter(Boolean).join(' · ')}`;
}
// Progress events update just that row and the drawer's bar, so the table does not redraw on every event.
function updateJob(id) {
  renderJobs();
  const i = byId(id); if (!i) return;
  const cell = document.querySelector(`[data-act="${CSS.escape(id)}"]`);
  if (cell) cell.innerHTML = actionsCell(i);
  if (id === S.selected) { const pe = $('#d-progress'); if (pe && S.jobs[id]) pe.innerHTML = progressHtml(S.jobs[id]); else renderDetail(); }
}

function rowHtml(i) {
  const r = i.remote; const lock = r?.lock; const st = statusOf(i);
  const hasLocal = i.status !== 'cloud-only';
  const cloud = !r ? '—' : r.deleted ? 'deleted' : `v${r.version} · ${ago(r.updatedAt)}`;
  const cloudTitle = r && !r.deleted ? `v${r.version} by ${r.updatedBy} on ${r.updatedFrom} (${osName(r.updatedOs)})` : '';
  const lockText = !lock ? '<span class="muted">—</span>' : lock.mine ? 'You' : esc(handle(lock.email));
  return `<tr class="${S.selected === i.id ? 'sel' : ''}">
    <td><input type="checkbox" class="pick" data-id="${esc(i.id)}" ${S.picked.has(i.id) ? 'checked' : ''}></td>
    <td title="${esc(i.name)}"><span class="nm" data-open="${esc(i.id)}">${esc(i.name)}</span>${i.running ? ' <span class="tag">running</span>' : ''}</td>
    <td title="${esc(i.folder || '')}">${i.folder ? esc(i.folder) : '<span class="muted">—</span>'}</td>
    <td><span class="s-${st}"><i class="dot"></i></span>${STATUS[st]}</td>
    <td class="c-cloud muted" title="${esc(cloudTitle)}">${esc(cloud)}</td>
    <td class="c-size muted">${fmtBytes(hasLocal ? i.localBytes : r?.sizeBytes)}</td>
    <td class="c-lock" title="${lock ? esc(`${lock.email} on ${lock.machine}`) : ''}">${lockText}</td>
    <td class="c-access">${accessCell(i)}</td>
    <td data-act="${esc(i.id)}">${actionsCell(i)}</td></tr>`;
}

function renderTable() {
  const rows = visibleItems();
  $('#count').textContent = rows.length === S.items.length ? `${S.items.length} profiles` : `${rows.length} of ${S.items.length}`;
  $('#rows').innerHTML = rows.map(rowHtml).join('') || '<tr><td colspan="9" class="muted empty-cell">No profiles</td></tr>';
  const picked = rows.filter((i) => S.picked.has(i.id)).length;
  $('#check-all').checked = rows.length > 0 && picked === rows.length;
  $('#check-all').indeterminate = picked > 0 && picked < rows.length;
  $('#bulk').hidden = S.picked.size === 0;
  $('#bulk-count').textContent = `${S.picked.size} selected`;
  renderJobs();
  $('[data-bulk="delete"]').textContent = isAdmin() ? 'Delete for good' : 'Delete';
  for (const th of $$('th[data-sort]')) {
    const on = th.dataset.sort === S.sort.key;
    th.classList.toggle('asc', on && S.sort.dir === 1); th.classList.toggle('desc', on && S.sort.dir === -1);
  }
}

$('#rows').addEventListener('change', (e) => {
  const el = e.target; if (!el.classList.contains('pick')) return;
  el.checked ? S.picked.add(el.dataset.id) : S.picked.delete(el.dataset.id);
  renderTable();
});
$('#rows').addEventListener('click', (e) => {
  const el = e.target.closest('[data-open],[data-push],[data-pull]'); if (!el) return;
  if (el.dataset.open) { S.selected = el.dataset.open; renderTable(); renderDetail(); return; }
  const i = byId(el.dataset.push || el.dataset.pull); if (!i) return;
  if (el.dataset.push) doPush(i, { keepLock: !!i.remote?.lock?.mine });
  else doPull(i, { takeLock: true });
});
$('#check-all').onchange = (e) => {
  for (const i of visibleItems()) e.target.checked ? S.picked.add(i.id) : S.picked.delete(i.id);
  renderTable();
};
$('#bulk-clear').onclick = () => { S.picked.clear(); renderTable(); };

// ---------- bulk actions ----------
const BULK = {
  push: { label: 'Upload', fn: (i) => api.push(i.id, { keepLock: !!i.remote?.lock?.mine }) },
  pull: { label: 'Download', fn: (i) => api.pull(i.id, { takeLock: true }) },
  take: { label: 'Check out', fn: (i) => api.takeLock(i.id, false) },
  release: { label: 'Release', fn: (i) => api.releaseLock(i.id, false) },
  delete: { label: 'Delete', fn: (i) => api.deleteRemote(i.id) },
};
for (const b of $$('[data-bulk]')) b.onclick = () => bulk(b.dataset.bulk);
async function bulk(kind) {
  const items = [...S.picked].map(byId).filter(Boolean);
  const n = items.length; if (!n) return;
  if (kind === 'delete' && !await ask(isAdmin() ? `Delete ${n} profiles for good?` : `Delete ${n} profiles for the team? They are also removed from this computer. An admin can restore them.`)) return;
  if (kind === 'push' && !isAdmin() && items.some((i) => !i.remote) && !await ask(WORKSPACE_NOTICE)) return;
  if (kind === 'pull' && S.launcherRunning && !await ask('ShardX Launcher is open. Continue?')) return;
  const { label, fn } = BULK[kind];
  S.picked.clear(); renderTable();
  // All start at once; the app runs three transfers at a time and queues the rest.
  let done = 0; const skipped = [];
  await Promise.all(items.map(async (i) => {
    const r = await run(i, label, () => fn(i));
    if (r.ok) { done++; return; }
    // Anything that needs a decision (overwrite, take over a lock) is left for the row's own button.
    if (r.error.code.startsWith('CONFIRM') || r.error.code === 'LOCKED') skipped.push(i.name);
  }));
  log(`${label}: ${done} of ${n} done${skipped.length ? `, ${skipped.length} need a decision: ${skipped.join(', ')}` : ''}`, done === n ? 'ok' : 'bad');
}

// ---------- detail drawer ----------
function renderDetail() {
  const el = $('#detail'); const i = byId(S.selected);
  el.hidden = !i;
  if (!i) { el.innerHTML = ''; return; }
  const r = i.remote; const lock = r?.lock; const st = statusOf(i);
  const hasLocal = i.status !== 'cloud-only';
  const job = S.jobs[i.id]; const busy = job ? 'disabled title="Syncing now"' : '';
  const pushWhy = blocked(i, 'push'); const pullWhy = blocked(i, 'pull');
  const lockText = !r ? '—' : !lock ? 'Free'
    : lock.mine ? `You · ${esc(ago(lock.at))}` : `${esc(lock.email)} on ${esc(lock.machine)} · ${esc(ago(lock.at))}`;
  el.innerHTML = `
    <div class="dhead"><div><h2 title="${esc(i.name)}">${esc(i.name)}</h2>
      <div class="muted">${i.folder ? esc(i.folder) : 'Unfiled'} · <span class="s-${st}"><i class="dot"></i>${STATUS[st]}</span></div></div>
      <button class="btn sm ghost" id="d-close">Close</button></div>
    ${job ? `<div class="progress" id="d-progress">${progressHtml(job)}</div>` : ''}
    ${i.running ? '<p class="warn">Open in ShardX. Close it to sync.</p>' : ''}
    ${r?.deleted ? '<p class="warn">Deleted in the cloud.</p>' : ''}
    <dl class="kv">
      <dt>Cloud</dt><dd>${r ? `v${r.version} · ${esc(ago(r.updatedAt))} · ${esc(handle(r.updatedBy))} on ${esc(r.updatedFrom)} (${esc(osName(r.updatedOs))})` : 'Not uploaded'}</dd>
      <dt>This computer</dt><dd>${hasLocal ? `${i.localVersion ? `v${i.localVersion}` : 'Not synced'}${i.lastSyncedHere ? ` · ${esc(ago(i.lastSyncedHere))}` : ''}` : 'Not here'}</dd>
      <dt>Size</dt><dd>${hasLocal ? fmtBytes(i.localBytes) : '—'} here · ${r ? fmtBytes(r.sizeBytes) : '—'} cloud</dd>
      <dt>Last opened</dt><dd>${hasLocal ? esc(ago(i.lastLaunchedAt)) : '—'}</dd>
      <dt>Checked out</dt><dd>${lockText}</dd>
    </dl>
    <div class="sec">
      <div class="row"><button class="btn sm primary" id="a-push" ${pushWhy ? 'disabled' : ''}>Upload</button>
        ${pushWhy ? `<span class="why">${esc(pushWhy)}</span>` : `<label class="check"><input type="checkbox" id="a-keep" ${lock?.mine ? 'checked' : ''}> Keep checked out</label>`}</div>
      <div class="row"><button class="btn sm primary" id="a-pull" ${pullWhy ? 'disabled' : ''}>Download</button>
        ${pullWhy ? `<span class="why">${esc(pullWhy)}</span>` : '<label class="check"><input type="checkbox" id="a-lock" checked> Check out to me</label>'}</div>
      <div class="row">
        ${r && !r.deleted && !lock ? `<button class="btn sm" id="a-take" ${busy}>Check out</button>` : ''}
        ${lock?.mine ? `<button class="btn sm" id="a-release" ${busy}>Release</button>` : ''}
        ${lock && !lock.mine ? `<button class="btn sm danger" id="a-force" ${busy}>Force unlock</button>` : ''}
        ${r?.deleted && isAdmin() ? `<button class="btn sm primary" id="a-restore" ${busy}>Restore</button>` : ''}
        ${r && (!r.deleted || isAdmin()) ? `<button class="btn sm danger" id="a-delete" ${busy}>${isAdmin() ? 'Delete for good' : 'Delete'}</button>` : ''}
      </div>
    </div>
    ${isAdmin() && r && !r.deleted && S.team ? accessSection(i) : ''}
    <div class="foot"><button class="link" id="a-backups">Backups folder</button><button class="link" id="a-shardx">ShardX folder</button></div>`;
  const on = (id, fn) => { const b = $(id); if (b) b.onclick = fn; };
  on('#d-close', () => { S.selected = null; renderDetail(); renderTable(); });
  on('#a-push', () => doPush(i, { keepLock: !!$('#a-keep')?.checked }));
  on('#a-pull', () => doPull(i, { takeLock: !!$('#a-lock')?.checked }));
  on('#a-take', () => run(i, 'Check out', () => api.takeLock(i.id, false)));
  on('#a-release', () => run(i, 'Release lock', () => api.releaseLock(i.id, false)));
  on('#a-force', async () => await ask(`Force unlock "${i.name}"? ${lock.email} may still be using it.`)
    && run(i, 'Force unlock', () => api.releaseLock(i.id, true)));
  on('#a-delete', async () => (isAdmin()
    ? await ask(`Delete "${i.name}" for good? The copy on this computer stays.`)
    : await ask(`Delete "${i.name}" for the team? It is also removed from this computer. An admin can restore it.`))
    && run(i, 'Delete', () => api.deleteRemote(i.id)));
  on('#a-restore', () => run(i, 'Restore', () => api.restoreRemote(i.id)));
  on('#a-backups', () => api.openBackups());
  on('#a-shardx', () => api.openShardx());
}

// Admins tick which members can open this profile. Folder access is shown under it, read-only.
function accessSection(i) {
  const direct = S.team.grants.profiles[i.id] || [];
  const plain = S.team.members.filter(active);
  const viaFolder = i.folder ? (S.team.grants.folders[i.folder] || []) : [];
  return `<div class="sec"><h3>Access</h3><div class="checks">
    ${plain.map((m) => `<label class="check"><input type="checkbox" data-profile-user="${esc(m.user_id)}" ${direct.includes(m.user_id) ? 'checked' : ''}>
      ${esc(m.email)}${viaFolder.includes(m.user_id) ? ' <span class="tag">via folder</span>' : ''}</label>`).join('') || '<span class="muted">No members yet</span>'}
  </div></div>`;
}
$('#detail').addEventListener('change', async (e) => {
  const el = e.target; const i = byId(S.selected);
  if (!el.dataset.profileUser || !i) return;
  const current = new Set(S.team.grants.profiles[i.id] || []);
  el.checked ? current.add(el.dataset.profileUser) : current.delete(el.dataset.profileUser);
  const r = await api.setProfileAccess(i.id, [...current]);
  if (!r.ok) tell(r.error.message);
  loadTeam();
});

// ---------- access tab (admins) ----------
function renderAccess() {
  const t = S.team; if (!t || !isAdmin()) return;
  const counts = (uid) => Object.values(t.grants.profiles).filter((a) => a.includes(uid)).length;
  const foldersOf = (uid) => Object.keys(t.grants.folders).filter((f) => t.grants.folders[f].includes(uid)).length;
  const shared = (m) => (m.status === 'removed' ? '—' : m.role === 'admin' ? 'all'
    : `<span title="${plural(counts(m.user_id), 'profile', 'profiles')}, ${plural(foldersOf(m.user_id), 'folder', 'folders')}">${counts(m.user_id)} · ${foldersOf(m.user_id)}</span>`);
  const wipe = (m) => (m.status !== 'removed' ? ''
    : m.wiped_at ? ` <span class="tag" title="Their app deleted its copies">wiped ${esc(ago(m.wiped_at))}</span>` : ' <span class="tag warn-tag">wipe pending</span>');
  $('#access-members').innerHTML = `<table class="t"><colgroup><col><col style="width:118px"><col style="width:60px"><col style="width:124px"></colgroup>
    <thead><tr><th>Member</th><th>Role</th><th class="num">Shared</th><th>Status</th></tr></thead><tbody>
    ${t.members.map((m) => `<tr class="${m.status === 'removed' ? 'gone' : ''}"><td title="${esc(m.email)}">${esc(m.email)}${wipe(m)}</td>
      <td class="fit"><select class="sel" data-role-of="${esc(m.user_id)}" ${m.status === 'removed' ? 'disabled' : ''}><option value="member" ${m.role === 'member' ? 'selected' : ''}>Member</option>
        <option value="admin" ${m.role === 'admin' ? 'selected' : ''}>Admin</option></select></td>
      <td class="num muted">${shared(m)}</td>
      <td class="fit"><select class="sel" data-status-of="${esc(m.user_id)}" data-email="${esc(m.email)}"><option value="active" ${m.status !== 'removed' ? 'selected' : ''}>Active</option>
        <option value="removed" ${m.status === 'removed' ? 'selected' : ''}>Removed</option></select></td></tr>`).join('')
      || '<tr><td colspan="4" class="muted empty-cell">Nobody has signed in yet</td></tr>'}
    </tbody></table>`;
  const plain = t.members.filter(active);
  const folders = [...new Set([...S.items.map((i) => i.folder).filter(Boolean), ...Object.keys(t.grants.folders)])].sort();
  $('#access-folders').innerHTML = !folders.length ? '<p class="muted">No folders yet.</p>' : folders.map((f) => {
    const granted = new Set(t.grants.folders[f] || []);
    const n = S.items.filter((i) => i.folder === f).length;
    return `<div class="frow"><div class="fname" title="${esc(f)}">${esc(f)} <span class="muted">${n}</span></div><div class="tgs">
      ${plain.map((m) => `<button class="tg ${granted.has(m.user_id) ? 'on' : ''}" title="${esc(m.email)}" data-folder="${esc(f)}" data-user="${esc(m.user_id)}">${esc(handle(m.email))}</button>`).join('')
        || '<span class="muted">No members</span>'}
    </div></div>`;
  }).join('');
}
$('#pane-access').addEventListener('change', async (e) => {
  const el = e.target;
  if (el.dataset.roleOf) {
    const r = await api.setMemberRole(el.dataset.roleOf, el.value);
    if (!r.ok) tell(r.error.message);
    return loadTeam();
  }
  if (el.dataset.statusOf) {
    if (el.value === 'removed' && !await ask(`Remove ${el.dataset.email}? They lose access to every profile, and their app deletes its copies of workspace profiles the next time it opens. Delete their account only after the wipe shows as done.`)) return loadTeam();
    const r = await api.setMemberStatus(el.dataset.statusOf, el.value);
    if (!r.ok) tell(r.error.message);
    return loadTeam();
  }
});
$('#pane-access').addEventListener('click', async (e) => {
  const el = e.target.closest('.tg'); if (!el) return;
  const f = el.dataset.folder; const current = new Set(S.team.grants.folders[f] || []);
  current.has(el.dataset.user) ? current.delete(el.dataset.user) : current.add(el.dataset.user);
  el.classList.toggle('on');
  const r = await api.setFolderAccess(f, [...current]);
  if (!r.ok) tell(r.error.message);
  loadTeam();
});

// ---------- single-profile actions ----------
async function run(i, label, fn) {
  if (S.jobs[i.id]) {
    log(`${label}: ${i.name}. Already syncing`, 'bad');
    return { ok: false, error: { code: 'BUSY', message: 'Syncing now' } };
  }
  S.jobs[i.id] = { step: label, pct: 0 };
  updateJob(i.id); if (i.id === S.selected) renderDetail();
  const r = await fn();
  delete S.jobs[i.id];
  if (r.ok) log(`${label}: ${i.name}. Done`, 'ok'); else log(`${label}: ${i.name}. ${r.error.message}`, 'bad');
  updateJob(i.id);
  await refreshSoon();
  return r;
}

// Jobs finishing together share one refresh, plus one more if another finished while it ran.
let refreshing = null; let refreshAgain = false;
function refreshSoon() {
  if (refreshing) { refreshAgain = true; return refreshing; }
  refreshing = (async () => {
    do { refreshAgain = false; await refresh(); } while (refreshAgain);
    refreshing = null;
  })();
  return refreshing;
}

async function doPush(i, opts) {
  if (!i.remote && !isAdmin() && !await ask(WORKSPACE_NOTICE)) return;
  let r = await run(i, 'Upload', () => api.push(i.id, opts));
  if (!r.ok && r.error.code === 'CONFIRM_OVERWRITE_CLOUD' && await ask(`${r.error.message}\n\nUpload anyway and replace the cloud copy?`)) {
    r = await run(i, 'Upload', () => api.push(i.id, { ...opts, overwrite: true }));
  }
  if (!r.ok && !r.error.code.startsWith('CONFIRM')) tell(r.error.message);
}

async function doPull(i, opts) {
  if (S.launcherRunning && !await ask('ShardX Launcher is open. It may not see the downloaded profile until you restart it.\n\nContinue?')) return;
  let o = { ...opts };
  for (let tries = 0; tries < 3; tries++) {
    const r = await run(i, 'Download', () => api.pull(i.id, o));
    if (r.ok) {
      if (r.data.crossOs) tell(`Downloaded from ${osName(r.data.fromOs)}. Saved passwords may not carry across operating systems.`);
      return;
    }
    if (r.error.code === 'CONFIRM_OVERWRITE_LOCAL' && await ask(`${r.error.message}\n\nDownload and replace?`)) { o.overwriteLocal = true; continue; }
    if (r.error.code === 'LOCKED' && await ask(`${r.error.message}\n\nTake it over anyway?`)) { o.forceLock = true; continue; }
    if (!r.error.code.startsWith('CONFIRM') && r.error.code !== 'LOCKED') tell(r.error.message);
    return;
  }
}

document.addEventListener('keydown', (e) => { if (e.key === 'Escape' && S.selected) { S.selected = null; renderDetail(); renderTable(); } });

apiReady.then(() => {
  api.onProgress((p) => {
    const j = S.jobs[p.id]; if (!j) return;
    Object.assign(j, { step: p.step, pct: p.pct, detail: p.detail });
    updateJob(p.id);
  });
  boot();
});
