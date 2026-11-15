'use strict';
// tjena Bridge web UI. Talks only to this container's /api (see api.go).
// Same flows as the phone app's screens (bridge_app/lib/screens).

const DEFAULT_HS = 'https://matrix.tjena.eu';
const DEFAULT_GW = 'https://wap.tjena.eu';
const $app = document.getElementById('app');
const $tabs = document.getElementById('tabs');
const $pill = document.getElementById('pill');
const $signout = document.getElementById('signout');

const S = { state: null, tab: 'status', job: null, setUp: null, view: 0 };

// ------------------------------------------------------------------ helpers

function h(tag, attrs, ...kids) {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs || {})) {
    if (v == null || v === false) continue;
    if (k.startsWith('on')) el.addEventListener(k.slice(2), v);
    else if (k === 'class') el.className = v;
    else if (k === 'value') el.value = v;
    else if (k === 'checked') el.checked = !!v;
    else el.setAttribute(k, v === true ? '' : v);
  }
  for (const kid of kids.flat()) {
    if (kid == null || kid === false) continue;
    el.append(kid instanceof Node ? kid : document.createTextNode(String(kid)));
  }
  return el;
}
const card = (...kids) => h('section', { class: 'card' }, ...kids);
const btn = (text, onclick, cls = '') => h('button', { class: 'btn ' + cls, type: 'button', onclick }, text);
const errBox = (text) => text ? h('p', { class: 'err' }, text) : null;
const field = (label, input) => h('div', null, h('label', null, label), input);
const notice = (kind, title, text) => h('div', { class: 'notice ' + kind }, title ? h('b', null, title) : null, text);
const mb = (n) => (n / 1048576).toFixed(1) + ' MB';

function replace(el, ...kids) { el.replaceChildren(...kids.flat().filter((k) => k != null && k !== false)); }

function toast(text) {
  const t = h('div', { class: 'toast', role: 'status' }, text);
  document.body.append(t);
  setTimeout(() => t.remove(), 3500);
}

class ApiError extends Error {
  constructor(msg, status) { super(msg); this.status = status; }
}

async function api(method, path, body) {
  const opt = { method, headers: { 'X-Tjena-UI': '1' }, credentials: 'same-origin' };
  if (body instanceof FormData) opt.body = body;
  else if (body !== undefined) {
    opt.headers['Content-Type'] = 'application/json';
    opt.body = JSON.stringify(body);
  }
  const r = await fetch(path, opt);
  const text = await r.text();
  let j = null;
  try { j = text ? JSON.parse(text) : null; } catch (_) { /* not JSON */ }
  if (r.status === 401 && !path.startsWith('/api/auth')) { boot(); throw new ApiError('Please sign in.', 401); }
  if (!r.ok) throw new ApiError((j && j.error) || text || 'HTTP ' + r.status, r.status);
  return j;
}

async function download(method, path, body, filename) {
  const opt = { method, headers: { 'X-Tjena-UI': '1' } };
  if (body !== undefined) { opt.headers['Content-Type'] = 'application/json'; opt.body = JSON.stringify(body); }
  const r = await fetch(path, opt);
  if (!r.ok) {
    let msg = 'HTTP ' + r.status;
    try { msg = (await r.json()).error || msg; } catch (_) { /* keep */ }
    throw new Error(msg);
  }
  const cd = r.headers.get('Content-Disposition') || '';
  const m = cd.match(/filename="([^"]+)"/);
  const url = URL.createObjectURL(await r.blob());
  const a = h('a', { href: url, download: (m && m[1]) || filename });
  document.body.append(a);
  a.click();
  a.remove();
  setTimeout(() => URL.revokeObjectURL(url), 10000);
}

// Calls into the relay / calls daemon. Returns {status, ok, json, error}.
async function local(kind, method, path, body, timeoutMs) {
  const r = await api('POST', '/api/' + kind, { method, path, body: body ?? null, timeout_ms: timeoutMs || 15000 });
  let json = {};
  try { json = r.body ? JSON.parse(r.body) : {}; } catch (_) { json = {}; }
  const ok = r.status >= 200 && r.status < 300;
  const error = ok ? null : (json.error || json.errcode || (r.status === 0 ? 'not reachable' : 'HTTP ' + r.status));
  return { status: r.status, ok, json, error };
}
const relay = (m, p, b, t) => local('relay', m, p, b, t);
const wacall = (m, p, b, t) => local('wacall', m, p, b, t);

// Runs fn with the button disabled; shows errors in errEl (or a toast).
async function busy(button, errEl, fn) {
  if (button) button.disabled = true;
  if (errEl) replace(errEl);
  try { return await fn(); } catch (e) {
    if (errEl) replace(errEl, errBox(e.message)); else toast(e.message);
  } finally { if (button) button.disabled = false; }
}

// Reads a tjena-bridge:// code from a photo or screenshot (browsers with BarcodeDetector).
function qrFromImage(onCode) {
  if (!('BarcodeDetector' in window)) return null;
  const input = h('input', { type: 'file', accept: 'image/*', hidden: true });
  input.addEventListener('change', async () => {
    const f = input.files[0];
    if (!f) return;
    try {
      const det = new window.BarcodeDetector({ formats: ['qr_code'] });
      const found = await det.detect(await createImageBitmap(f));
      const code = found.map((c) => c.rawValue).find((v) => v.startsWith('tjena-bridge://'));
      if (code) onCode(code); else toast('No tjena code found in this picture.');
    } catch (e) { toast('Could not read the picture: ' + e.message); }
    input.value = '';
  });
  return h('span', null, input, btn('Read from a photo of the code…', () => input.click()));
}

// ------------------------------------------------------------------ boot, sign-in

async function boot() {
  stopPolling();
  $tabs.hidden = true; $pill.hidden = true; $signout.hidden = true;
  let a;
  try { a = await api('GET', '/api/auth'); } catch (e) {
    replace($app, card(errBox('Cannot reach the bridge: ' + e.message), btn('Retry', boot)));
    return;
  }
  if (!a.signed_in) return renderSignin(a);
  $signout.hidden = false;
  S.setUp = null;
  await poll();
  startPolling();
}

$signout.addEventListener('click', async () => {
  await api('POST', '/api/auth/logout', {}).catch(() => {});
  boot();
});

function renderSignin(a) {
  const pw = h('input', { type: 'password', autocomplete: a.configured ? 'current-password' : 'new-password', id: 'pw' });
  const pw2 = h('input', { type: 'password', autocomplete: 'new-password' });
  const err = h('div');
  const go = btn(a.configured ? 'Sign in' : 'Set password', null, 'primary');
  const submit = () => busy(go, err, async () => {
    if (!a.configured && pw.value !== pw2.value) throw new Error('The passwords differ.');
    await api('POST', a.configured ? '/api/auth/login' : '/api/auth/setup', { password: pw.value });
    boot();
  });
  go.addEventListener('click', submit);
  const form = h('form', { onsubmit: (e) => { e.preventDefault(); submit(); } },
    field('Password', pw),
    a.configured ? null : field('Repeat password', pw2),
    errBox(''), err,
    h('div', { class: 'row' }, go));
  replace($app, card(
    h('h1', null, a.configured ? 'Sign in' : 'Choose a password'),
    a.configured
      ? h('p', { class: 'muted' }, 'This page controls your WhatsApp bridge.')
      : h('p', null, 'This page controls your WhatsApp bridge, so it needs a password (at least 10 characters). ',
        'Anyone who can open this page before you could set it: keep the port private until you have. ',
        'You can also set WEBUI_PASSWORD instead.'),
    form));
  pw.focus();
}

// ------------------------------------------------------------------ polling

let pollTimer = null;
function startPolling() { stopPolling(); pollTimer = setInterval(poll, 2500); }
function stopPolling() { if (pollTimer) clearInterval(pollTimer); pollTimer = null; }

async function poll() {
  let st, job;
  try {
    [st, job] = await Promise.all([api('GET', '/api/state'), api('GET', '/api/job')]);
  } catch (_) { return; }
  S.state = st;
  const jobChanged = JSON.stringify(job) !== JSON.stringify(S.job);
  S.job = job;
  updatePill();
  if (S.setUp !== st.set_up) {
    S.setUp = st.set_up;
    st.set_up ? renderMain() : renderSetup();
    return;
  }
  if (!st.set_up) { if (jobChanged) renderSetup(); return; }
  if (S.tab === 'status') renderTabBody();
  if (jobChanged && typeof S.onJob === 'function') S.onJob(job);
}

function health() {
  const st = S.state || {};
  const r = st.relay || {};
  if (!st.set_up) return ['', 'Not set up'];
  switch (st.service_state) {
    case 'running':
      if (r.revoked) return ['bad', 'Revoked'];
      return r.upstream_connected ? ['ok', 'Connected to tjena'] : ['warn', 'Connecting…'];
    case 'starting': case 'restarting': return ['warn', st.service_state === 'starting' ? 'Starting…' : 'Restarting…'];
    case 'halted': case 'error': return ['bad', st.service_state === 'halted' ? 'Stopped by the server' : 'Error'];
    default: return ['', 'Stopped'];
  }
}

function updatePill() {
  const [cls, text] = health();
  $pill.hidden = false;
  $pill.className = 'pill ' + cls;
  $pill.textContent = text;
}

// ------------------------------------------------------------------ setup

function renderSetup() {
  $tabs.hidden = true;
  const st = S.state || {};
  const job = S.job || {};
  const view = ++S.view;
  const parts = [];
  if (job.kind === 'move-out' && job.done && job.phase === 'done') {
    parts.push(card(h('h2', null, 'Moved'), h('p', null, job.progress ||
      'The new device runs your bridge now, and the bridge data was removed here. You can stop this container.')));
  }
  if (job.kind === 'move-in' && job.phase === 'compare' && !job.done) {
    return replace($app, card(h('h2', null, 'Moving from your old device'), sasBox(job)));
  }
  if ((job.kind === 'setup' || job.kind === 'move-in') && (!job.done || job.error)) {
    parts.push(jobCard(job, async () => {
      await api('POST', '/api/job/cancel', {}).catch(() => {});
    }));
    if (!job.done) return replace($app, parts);
  }
  if (st.pending) parts.push(existingCard(st.pending));
  else parts.push(signinCard());
  if (view === S.view) replace($app, parts);
}

function jobCard(job, onCancel) {
  const frac = job.fraction >= 0 ? job.fraction : null;
  return card(
    h('h2', null, job.done ? (job.error ? 'That didn\'t work' : 'Done') : 'Working…'),
    job.done ? null : (frac != null ? h('progress', { value: frac, max: 1 }) : h('progress')),
    job.progress && !job.error ? h('p', null, job.progress) : null,
    job.size > 0 && !job.done ? h('p', { class: 'small muted' }, mb(job.sent) + ' of ' + mb(job.size)) : null,
    errBox(job.error),
    !job.done && onCancel ? h('div', { class: 'row' }, btn('Cancel', onCancel)) : null);
}

// The number both devices show when moving without a code.
function sasBox(job) {
  const err = h('div');
  const answer = (match) => async (e) => busy(e.target, err, async () => {
    await api('POST', '/api/job/confirm', { match });
    await poll();
  });
  const sas = job.sas || '';
  return h('div', null,
    h('p', null, 'Check that the other device shows the same number:'),
    h('div', { class: 'sas', 'aria-label': sas.split('').join(' ') }, sas.slice(0, 3) + ' ' + sas.slice(3)),
    job.confirmed
      ? h('p', null, h('progress'), h('br'), 'Confirmed here. Now confirm on the other device too.')
      : h('div', null,
        h('div', { class: 'row' },
          btn('Same number', answer(true), 'primary'),
          btn('Different', answer(false))),
        h('p', { class: 'small muted' }, 'If the numbers differ, someone may be trying to intercept the move. Nothing has been sent yet.')),
    err,
    h('div', { class: 'row' }, btn('Cancel', async () => { await api('POST', '/api/job/cancel', {}); await poll(); })));
}

function signinCard() {
  const risk = h('input', { type: 'checkbox' });
  const user = h('input', { type: 'text', autocomplete: 'username', placeholder: 'name or @name:tjena.eu' });
  const pass = h('input', { type: 'password', autocomplete: 'current-password' });
  const link = h('input', { type: 'text', placeholder: 'tjena-bridge://login?…' });
  const hs = h('input', { type: 'url', value: DEFAULT_HS });
  const gw = h('input', { type: 'url', value: DEFAULT_GW });
  const err = h('div');
  const go = btn('Sign in', null, 'primary');
  const submit = () => busy(go, err, async () => {
    if (!risk.checked) throw new Error('Please confirm that you understand the risk.');
    const r = await api('POST', '/api/setup/login', {
      homeserver: hs.value, gateway: gw.value, user: user.value, password: pass.value, link: link.value,
    });
    pass.value = '';
    if (!r.existing) await api('POST', '/api/setup/fresh', {});
    await poll(); renderSetup();
  });
  go.addEventListener('click', submit);
  const adv = h('details', null, h('summary', null, 'Advanced: other server, setup link, restore'),
    field('Homeserver (for sign-in)', hs),
    field('Bridge gateway', gw),
    field('Or paste a setup link from the tjena app', link),
    qrFromImage((c) => { link.value = c; }),
    h('h3', null, 'Restore from a backup'),
    restoreForm(false));
  return card(
    h('h1', null, 'Set up your WhatsApp bridge'),
    h('p', null, 'This container will run your personal WhatsApp bridge for tjena. WhatsApp messages are only ',
      'decrypted here and on your own tjena devices; the tjena server only passes encrypted data along.'),
    notice('warn', 'Unofficial WhatsApp client', 'The bridge connects to WhatsApp as a "linked device" using an unofficial client. ' +
      'WhatsApp may restrict or ban accounts that use unofficial clients. Use at your own risk.'),
    h('label', { class: 'check' }, risk, 'I understand the risk'),
    h('form', { onsubmit: (e) => { e.preventDefault(); submit(); } },
      field('tjena username', user), field('Password', pass),
      h('p', { class: 'small muted' }, 'The password goes only to your tjena server, to create this bridge\'s own session.'),
      err, h('div', { class: 'row' }, go)),
    adv);
}

function existingCard(p) {
  const ex = p.existing || {};
  const seen = ex.last_seen ? new Date(ex.last_seen) : null;
  const where = ex.connected ? 'Your other bridge is online right now.'
    : seen && !isNaN(seen) ? 'Your other bridge was last online ' + seen.toLocaleString() + '.'
      : 'Your other bridge is offline.';
  const err = h('div');
  const area = h('div');
  const code = h('textarea', { placeholder: 'tjena-bridge://move?id=…&key=…', rows: 3 });
  const moveBtn = btn('Move here', null);
  moveBtn.addEventListener('click', () => busy(moveBtn, err, async () => {
    await api('POST', '/api/setup/move', { code: code.value });
    await poll(); renderSetup();
  }));
  const connect = btn('Connect without a code', null, 'primary');
  connect.addEventListener('click', () => busy(connect, err, async () => {
    await api('POST', '/api/setup/move', { compare: true });
    await poll(); renderSetup();
  }));
  const showMove = () => replace(area,
    h('h3', null, 'Move from your old device'),
    h('ol', null,
      h('li', null, 'On the old device open ', h('b', null, 'Move to a new phone'),
        ' (phone: tjena Bridge → Settings; Docker: Backup & move → Move to another device).'),
      h('li', null, 'Click ', h('b', null, 'Connect without a code'), ' here.'),
      h('li', null, 'Both devices show a 6-digit number. If they match, confirm on both.')),
    h('div', { class: 'row' }, connect),
    h('details', null, h('summary', null, 'Or use the move code'),
      h('p', null, 'On the old device: "Copy code", and paste it here, or upload a screenshot of its QR code.'),
      code, qrFromImage((c) => { code.value = c; }),
      h('div', { class: 'row' }, moveBtn)));
  const showRestore = () => replace(area, h('h3', null, 'Restore from a backup file'), restoreForm(true));
  const fresh = btn('', async () => {
    if (!confirm('Start fresh?\n\nUse this if the old device is lost or broken. This bridge takes over with an empty setup: ' +
      'you link WhatsApp again, and the old device stops. Chats already in tjena stay, but WhatsApp chats may appear a second time.')) return;
    await busy(fresh, err, async () => { await api('POST', '/api/setup/fresh', {}); await poll(); renderSetup(); });
  }, 'choice');
  fresh.replaceChildren('Start fresh (old device lost)', h('small', null, 'Link WhatsApp again; chats may show up twice.'));
  const choice = (title, sub, fn, cls = '') => {
    const b = btn('', fn, 'choice ' + cls);
    b.replaceChildren(title, h('small', null, sub));
    return b;
  };
  return card(
    h('h1', null, 'You already have a bridge'),
    h('p', null, 'Signed in as ', h('b', null, p.user_id), '. ', where),
    h('p', null, 'Move it here to keep your chats and the WhatsApp link.'),
    choice('Move from my old device', 'Chats, WhatsApp link and keys come along.', showMove, 'primary'),
    choice('Restore from a backup file', 'A .tjbk file from the phone app or this page.', showRestore),
    fresh,
    h('div', { class: 'row' }, btn('Cancel', async () => { await api('POST', '/api/setup/cancel', {}); await poll(); renderSetup(); })),
    err, area);
}

function restoreForm(replacing) {
  const file = h('input', { type: 'file', accept: '.tjbk,application/octet-stream' });
  const pass = h('input', { type: 'password', autocomplete: 'off' });
  const err = h('div');
  const go = btn('Restore', null, replacing ? 'primary' : '');
  go.addEventListener('click', () => busy(go, err, async () => {
    if (!file.files[0]) throw new Error('Choose the backup file.');
    if (S.state && S.state.set_up && !confirm('Replace this bridge with the backup?\n\nThe current bridge data here is overwritten.')) return;
    const fd = new FormData();
    fd.append('passphrase', pass.value);
    fd.append('file', file.files[0]);
    await api('POST', '/api/backup/import', fd);
    pass.value = '';
    toast('Backup restored. The bridge is starting.');
    await poll();
  }));
  return h('div', null,
    h('p', { class: 'small muted' }, 'Only one device may run the bridge: stop the old one first.'),
    field('Backup file', file), field('Passphrase', pass), err, h('div', { class: 'row' }, go));
}

// ------------------------------------------------------------------ main

const TABS = [
  ['status', 'Status'], ['whatsapp', 'WhatsApp'], ['calls', 'Calls'], ['settings', 'Settings'],
  ['backup', 'Backup & move'], ['tools', 'Tools'], ['logs', 'Logs'],
];

function renderMain() {
  $tabs.hidden = false;
  replace($tabs, TABS.map(([id, label]) => h('button', {
    type: 'button', class: S.tab === id ? 'active' : '',
    onclick: () => { S.tab = id; renderMain(); },
  }, label)));
  S.view++;
  S.onJob = null;
  if (S.wa && S.wa.callsTimer) { clearInterval(S.wa.callsTimer); S.wa.callsTimer = null; }
  if (S.logTimer) { clearInterval(S.logTimer); S.logTimer = null; }
  replace($app);
  renderTabBody(true);
}

function renderTabBody(first) {
  switch (S.tab) {
    case 'status': return renderStatus();
    case 'whatsapp': return first && renderWhatsApp();
    case 'calls': return first && renderCalls();
    case 'settings': return first && renderSettings();
    case 'backup': return first && renderBackup();
    case 'tools': return first && renderTools();
    case 'logs': return first && renderLogs();
  }
}

function statusRow(label, value, cls) {
  return h('div', { class: 'status' }, h('span', { class: 'dot ' + (cls || '') }), h('span', null, label), h('span', { class: 'v' }, value));
}

let statusErr = null;
function renderStatus() {
  const st = S.state || {};
  const r = st.relay || {};
  const running = ['running', 'starting', 'restarting'].includes(st.service_state);
  const err = statusErr || (statusErr = h('div'));
  const actions = h('div', { class: 'row' });
  if (running) {
    actions.append(btn('Restart', (e) => busy(e.target, err, async () => { await api('POST', '/api/bridge/restart', {}); await poll(); })));
    actions.append(btn('Stop', (e) => busy(e.target, err, async () => {
      if (!confirm('Stop the bridge? WhatsApp messages stop arriving in tjena until you start it again.')) return;
      await api('POST', '/api/bridge/stop', {}); await poll();
    })));
  } else {
    actions.append(btn('Start', (e) => busy(e.target, err, async () => { await api('POST', '/api/bridge/start', {}); await poll(); }), 'primary'));
  }
  const parts = [];
  if (st.service_state === 'halted') {
    parts.push(card(notice('bad', 'The bridge stopped', st.service_detail || ''),
      h('p', null, 'If this device should run the bridge (and the other one is off), take it back:'),
      h('div', { class: 'row' }, btn('Make this the active bridge', (e) => repair(e.target, err), 'primary'))));
  }
  const [hcls, htext] = health();
  parts.push(card(
    h('h2', null, 'Bridge'),
    statusRow('Service', htext + (st.service_detail && st.service_state !== 'halted' ? ' · ' + st.service_detail : ''), hcls),
    statusRow('Connection to tjena', r.upstream_connected ? 'Connected' : (r.upstream_error || 'Not connected'), r.upstream_connected ? 'ok' : running ? 'warn' : ''),
    statusRow('WhatsApp bridge process', r.bridge_connected ? 'Connected to the relay' : 'Not connected', r.bridge_connected ? 'ok' : running ? 'warn' : ''),
    r.pace && r.pace.rate_limit_hits ? statusRow('Send pacing', `${r.pace.rate_limit_hits} rate limit hits, ${r.pace.sends_waiting || 0} waiting`, 'warn') : null,
    st.last_exit ? statusRow('Last exit', st.last_exit, '') : null,
    actions, err));
  parts.push(card(
    h('h2', null, 'Details'),
    h('dl', { class: 'kv' },
      h('dt', null, 'Owner'), h('dd', null, st.owner || '–'),
      h('dt', null, 'Bridge bot'), h('dd', null, st.bot_mxid || '–'),
      h('dt', null, 'Slot'), h('dd', null, st.slot || '–'),
      h('dt', null, 'Gateway'), h('dd', null, st.gateway_url || '–'),
      h('dt', null, 'Bridge'), h('dd', null, st.bridge_version || '–'),
      h('dt', null, 'This app'), h('dd', null, st.app_version || '–'))));
  replace($app, parts);
}

async function repair(button, err) {
  await busy(button, err, async () => {
    await api('POST', '/api/repair', {});
    toast('This is now the active bridge.');
    await poll();
  });
}

// ------------------------------------------------------------------ WhatsApp

const digits = (s) => String(s || '').replace(/[^0-9]/g, '');

function renderWhatsApp() {
  const wa = S.wa || (S.wa = { logins: [], calls: null, flow: null, error: null, fastImport: true });
  const view = S.view;
  const accounts = h('div');
  const flowEl = h('div');
  const errEl = h('div');
  wa.redraw = () => {
    if (view !== S.view) return;
    replace(errEl, errBox(wa.error));
    replace(accounts, accountsCard(wa));
    replace(flowEl, flowCard(wa));
  };
  const connected = S.state && S.state.relay && S.state.relay.bridge_connected;
  if (!connected && !wa.flow) {
    replace($app, card(h('p', null, 'The bridge is not connected yet. Check the Status tab.'),
      h('div', { class: 'row' }, btn('Retry', renderWhatsApp))));
    return;
  }
  replace($app, errEl, accounts, flowEl);
  wa.redraw();
  loadLogins(wa);
  loadCallsStatus(wa);
  wa.callsTimer = setInterval(() => { if (view === S.view) loadCallsStatus(wa); }, 3000);
}

async function loadLogins(wa) {
  try {
    const r = await relay('GET', '/relay/provision/v3/whoami');
    if (r.ok) { wa.logins = r.json.logins || []; wa.error = null; } else wa.error = r.error;
  } catch (e) { wa.error = e.message; }
  wa.loaded = true;
  wa.redraw();
}

async function loadCallsStatus(wa) {
  try {
    const r = await wacall('GET', '/status', null, 4000);
    const next = r.ok ? r.json : null;
    if (JSON.stringify(next) !== JSON.stringify(wa.calls)) { wa.calls = next; wa.redraw(); }
  } catch (_) { /* ignore */ }
}

function callsInfo(wa, phone) {
  const w = (wa.calls && wa.calls.whatsapp) || {};
  const acct = (w.accounts || []).find((a) => a.phone === phone) || null;
  const pairing = w.pairing || null;
  const mine = !!pairing && pairing.phone === phone;
  return {
    linked: !!acct, online: !!acct && acct.connected,
    error: (acct && acct.last_error) || (mine && pairing.error) || w.last_error || null,
    code: mine ? pairing.code : null, linking: mine,
  };
}

function accountsCard(wa) {
  const list = h('div');
  if (!wa.loaded) list.append(h('progress'));
  else if (!wa.logins.length) list.append(h('p', { class: 'muted' }, 'No WhatsApp account linked yet.'));
  for (const l of wa.logins) {
    const profile = l.profile || {};
    const state = String((l.state && l.state.state_event) || '');
    const phone = profile.phone;
    const name = profile.name || l.name || phone || l.id;
    const cls = state === 'CONNECTED' ? 'ok' : ['CONNECTING', 'BACKFILLING'].includes(state) ? 'warn' : 'bad';
    const calls = callsInfo(wa, digits(l.id));
    list.append(h('div', { class: 'account' },
      h('div', { class: 'head' }, h('span', { class: 'dot ' + cls }), h('span', { class: 'name' }, name),
        btn('Log out', () => logoutAccount(wa, l.id), 'danger')),
      h('div', { class: 'small muted' }, [phone && phone !== name ? phone : null, state.toLowerCase().replace(/_/g, ' ')].filter(Boolean).join(' · ')),
      callsRow(wa, digits(l.id), calls)));
  }
  return card(h('h2', null, 'WhatsApp accounts'), list,
    h('div', { class: 'row' }, btn('Refresh', () => loadLogins(wa))));
}

function callsRow(wa, phone, c) {
  if (c.linking) {
    return h('div', { class: 'notice warn' },
      h('p', null, 'Set up calls: on the phone with this WhatsApp account, open Settings → Linked devices → Link a device → ',
        'Link with phone number instead, and enter:'),
      c.code ? h('div', { class: 'code' }, c.code) : h('progress'),
      errBox(c.error),
      h('div', { class: 'row' }, btn('Cancel', async () => { await wacall('POST', '/link/cancel', {}); loadCallsStatus(wa); })));
  }
  const label = c.linked ? (c.online ? 'Calls from tjena: on' : 'Calls from tjena: connecting…') : 'Calls from tjena: off';
  return h('div', { class: 'row small' },
    h('span', { class: 'dot ' + (c.linked ? (c.online ? 'ok' : 'warn') : '') }), label,
    c.linked && c.error ? h('span', { class: 'err' }, c.error) : null,
    c.linked
      ? btn('Turn off', () => removeCalls(wa, phone, true))
      : btn('Set up calls', (e) => setupCalls(wa, phone, e.target)));
}

async function setupCalls(wa, phone, button) {
  await busy(button, null, async () => {
    if (!wa.calls) {
      await api('POST', '/api/calls', { on: true });
      for (let i = 0; i < 30 && !wa.calls; i++) {
        await new Promise((r) => setTimeout(r, 1000));
        await loadCallsStatus(wa);
      }
      if (!wa.calls) throw new Error('The calls service did not start. Is the bridge running?');
    }
    const r = await wacall('POST', '/link', { phone }, 40000);
    if (!r.ok) throw new Error(r.error || 'Linking failed');
    await loadCallsStatus(wa);
  });
}

async function removeCalls(wa, phone, ask) {
  if (ask && !confirm('Turn off calls for this account?\n\nRemoves "tjena calls" from this account\'s WhatsApp linked devices. Chats are not affected.')) return;
  const r = await wacall('POST', '/unlink', { phone }, 30000);
  if (!r.ok) toast(r.error || 'Failed');
  await loadCallsStatus(wa);
}

async function logoutAccount(wa, id) {
  let deleteChats = true;
  try { const o = await api('GET', '/api/options'); if (o.delete_chats_on_logout === false) deleteChats = false; } catch (_) { /* default */ }
  if (!confirm('Log out of WhatsApp?\n\n' + (deleteChats
    ? 'The bridge stops relaying this WhatsApp account and removes its chats and space from tjena. WhatsApp itself keeps all messages.'
    : 'The bridge stops relaying this WhatsApp account. Its chats stay in tjena but stop updating.'))) return;
  const r = await relay('POST', '/relay/provision/v3/logout/' + encodeURIComponent(id), {});
  if (!r.ok) { wa.error = r.error; wa.redraw(); }
  if (callsInfo(wa, digits(id)).linked) await removeCalls(wa, digits(id), false).catch(() => {});
  await loadLogins(wa);
}

function flowCard(wa) {
  const f = wa.flow;
  if (f) {
    return card(
      h('h2', null, 'Link WhatsApp'),
      f.instructions ? h('p', null, f.instructions) : null,
      f.code ? h('div', null,
        h('p', null, 'Your pairing code:'), h('div', { class: 'code' }, f.code),
        h('p', { class: 'small' }, 'On your phone: WhatsApp → Settings → Linked devices → Link a device → ',
          'Link with phone number instead → enter the code above.')) : null,
      f.qrImg ? h('div', null,
        h('p', null, 'On your phone: WhatsApp → Settings → Linked devices → Link a device, and scan:'),
        h('div', { class: 'qr' }, h('img', { src: f.qrImg, alt: 'WhatsApp QR code' }))) : null,
      !f.code && !f.qrImg ? h('progress') : null,
      h('div', { class: 'row' }, btn('Cancel', () => cancelFlow(wa))));
  }
  const phone = h('input', { type: 'tel', placeholder: '+491701234567', autocomplete: 'tel' });
  const fast = h('input', { type: 'checkbox', checked: wa.fastImport, onchange: (e) => { wa.fastImport = e.target.checked; } });
  return card(
    h('h2', null, wa.logins.length ? 'Add another WhatsApp account' : 'Link WhatsApp'),
    h('p', null, 'With a pairing code: enter the WhatsApp number in international format.'),
    phone,
    h('div', { class: 'row' }, btn('Get pairing code', () => startPhone(wa, phone.value), 'primary'), btn('Show QR code instead', () => startQr(wa))),
    h('label', { class: 'check' }, fast, h('span', null, 'Fast history import',
      h('span', { class: 'small muted' }, ' – the bridge restarts once without your own puppet, so old messages aren\'t slowed down; it comes back by itself.'))));
}

async function prepareImport(wa) {
  if (!wa.fastImport) return true;
  wa.flow.instructions = 'Preparing fast history import…';
  wa.redraw();
  try {
    await api('POST', '/api/double-puppet', { action: 'pause' });
    wa.pausedHere = true;
  } catch (e) { wa.error = 'Could not prepare the fast import: ' + e.message; return false; }
  for (let i = 0; i < 60; i++) {
    await new Promise((r) => setTimeout(r, 1000));
    try {
      const st = await api('GET', '/api/state');
      if (st.relay && st.relay.bridge_connected) { wa.flow.instructions = null; return true; }
    } catch (_) { /* keep waiting */ }
  }
  wa.error = 'The bridge did not come back after restarting. Check the Status tab.';
  return false;
}

async function undoPause(wa) {
  if (!wa.pausedHere) return;
  wa.pausedHere = false;
  await api('POST', '/api/double-puppet', { action: 'resume' }).catch(() => {});
}

async function startPhone(wa, raw) {
  const number = raw.replace(/[\s()-]/g, '');
  if (!/^\+\d{7,15}$/.test(number)) { wa.error = 'Enter your number in international format, e.g. +491701234567'; wa.redraw(); return; }
  wa.error = null;
  wa.flow = { gen: (wa.gen = (wa.gen || 0) + 1) };
  wa.redraw();
  if (!await prepareImport(wa)) { wa.flow = null; wa.redraw(); return; }
  let r = await relay('POST', '/relay/provision/v3/login/start/phone', {});
  if (!r.ok) return flowFail(wa, r);
  wa.flow.loginId = r.json.login_id;
  r = await relay('POST', `/relay/provision/v3/login/step/${wa.flow.loginId}/${r.json.step_id}/user_input`, { phone_number: number }, 60000);
  if (!r.ok) return flowFail(wa, r);
  await handleStep(wa, r.json);
}

async function startQr(wa) {
  wa.error = null;
  wa.flow = { gen: (wa.gen = (wa.gen || 0) + 1) };
  wa.redraw();
  if (!await prepareImport(wa)) { wa.flow = null; wa.redraw(); return; }
  const r = await relay('POST', '/relay/provision/v3/login/start/qr', {}, 60000);
  if (!r.ok) return flowFail(wa, r);
  wa.flow.loginId = r.json.login_id;
  await handleStep(wa, r.json);
}

async function handleStep(wa, step) {
  const gen = wa.flow && wa.flow.gen;
  while (wa.flow && wa.flow.gen === gen) {
    if (step.login_id) wa.flow.loginId = step.login_id;
    if (step.type === 'complete') {
      wa.pausedHere = false; // comes back by itself once the import is done
      wa.flow = null;
      toast('WhatsApp connected');
      await loadLogins(wa);
      return;
    }
    if (step.type !== 'display_and_wait') return flowFail(wa, { status: 0, error: 'Unexpected login step ' + step.type });
    const d = step.display_and_wait || {};
    wa.flow.instructions = step.instructions || null;
    if (d.type === 'code') wa.flow.code = d.data;
    if (d.type === 'qr') {
      try { wa.flow.qrImg = (await api('POST', '/api/qr', { data: d.data })).png; } catch (_) { /* keep old */ }
    }
    wa.redraw();
    const r = await relay('POST', `/relay/provision/v3/login/step/${wa.flow.loginId}/${step.step_id}/display_and_wait`, {}, 9 * 60000);
    if (!wa.flow || wa.flow.gen !== gen) return;
    if (!r.ok) return flowFail(wa, r);
    step = r.json;
  }
}

function flowFail(wa, r) {
  undoPause(wa);
  wa.flow = null;
  wa.error = r.status === 0 ? 'Bridge not reachable: ' + r.error : r.error;
  wa.redraw();
}

async function cancelFlow(wa) {
  const id = wa.flow && wa.flow.loginId;
  wa.flow = null;
  wa.redraw();
  if (id) await relay('POST', '/relay/provision/v3/login/cancel/' + id, {}).catch(() => {});
  await undoPause(wa);
}

// ------------------------------------------------------------------ calls

function renderCalls() {
  const view = S.view;
  const body = h('div');
  const err = h('div');
  const toggle = h('input', { type: 'checkbox', checked: S.state.calls_enabled });
  toggle.addEventListener('change', () => busy(toggle, err, async () => {
    await api('POST', '/api/calls', { on: toggle.checked });
    await poll();
  }));
  replace($app,
    card(h('h2', null, 'WhatsApp calls'),
      h('label', { class: 'check' }, toggle, h('span', null, h('b', null, 'Calls from tjena'), h('br'),
        h('span', { class: 'small muted' }, 'Call your WhatsApp contacts from tjena and answer their calls there. Each WhatsApp ' +
          'account gets a second linked device here, used only for calls. Set it up per account in the WhatsApp tab.'))),
      err, body),
    card(h('h2', null, 'About'), h('p', { class: 'small' },
      'Audio and video go directly between tjena and this container (encrypted WebRTC, through the tjena TURN server if needed), ' +
      'and from here to WhatsApp with WhatsApp\'s own encryption. The tjena server only passes on call setup messages. ' +
      'WhatsApp calls use meowcaller (MIT licence), an independent implementation not made by WhatsApp; ' +
      'WhatsApp may change its call protocol at any time.')));
  const refresh = async () => {
    if (view !== S.view) return;
    let st = null;
    try { const r = await wacall('GET', '/status', null, 4000); st = r.ok ? r.json : null; } catch (_) { /* keep */ }
    if (view !== S.view) return;
    toggle.checked = !!S.state.calls_enabled;
    if (!st) {
      replace(body, S.state.calls_enabled ? h('p', { class: 'muted' }, 'Starting… (the bridge must be running)') : null);
      return;
    }
    const gw = st.gateway || {};
    const accts = (st.whatsapp && st.whatsapp.accounts) || [];
    replace(body, h('h3', null, 'Status' + (st.version ? ' · v' + st.version : '')),
      statusRow('Reachable from tjena', gw.connected ? 'Connected to the tjena server' : (gw.error || 'Connecting…'), gw.connected ? 'ok' : 'warn'),
      accts.length ? null : statusRow('Accounts with calls', 'None yet. WhatsApp tab → an account → Set up calls', 'warn'),
      accts.map((a) => statusRow('WhatsApp +' + a.phone, a.connected ? 'Calls device online' : (a.last_error || 'Connecting…'), a.connected ? 'ok' : 'warn')),
      st.active_calls > 0 ? statusRow('Calls now', st.active_calls + ' active', 'ok') : null);
  };
  refresh();
  S.wa = S.wa || {};
  S.wa.callsTimer = setInterval(refresh, 2500);
}

// ------------------------------------------------------------------ settings

const PRESETS = [
  ['off', 'Off', 'No old messages. Only new messages arrive.',
    { enabled: false, max_initial_messages: 0, max_catchup_messages: 0, max_initial_conversations: 10, request_full_sync: false, full_sync_days: 0 }],
  ['light', 'Light', 'Last 20 messages in your 10 most recent chats.',
    { enabled: true, max_initial_messages: 20, max_catchup_messages: 100, max_initial_conversations: 10, request_full_sync: false, full_sync_days: 0 }],
  ['standard', 'Standard', 'Last 50 messages in your 25 most recent chats.',
    { enabled: true, max_initial_messages: 50, max_catchup_messages: 500, max_initial_conversations: 25, request_full_sync: false, full_sync_days: 0 }],
  ['lots', 'Lots', 'Up to 500 messages in every chat, and a year of history from your phone.',
    { enabled: true, max_initial_messages: 500, max_catchup_messages: 1000, max_initial_conversations: -1, request_full_sync: true, full_sync_days: 365 }],
  ['custom', 'Custom', 'Choose the numbers yourself.', null],
];

function presetFor(bf) {
  for (const [id, , , v] of PRESETS) if (v && Object.keys(v).every((k) => v[k] === bf[k])) return id;
  return 'custom';
}

async function renderSettings() {
  const view = S.view;
  replace($app, card(h('progress')));
  let opts = {};
  try { opts = await api('GET', '/api/options'); } catch (e) { replace($app, card(errBox(e.message))); return; }
  if (view !== S.view) return;
  const st = S.state;
  let bf = Object.assign({}, PRESETS[2][3], opts.backfill || {});
  let preset = presetFor(bf);
  const customBox = h('div');
  const numIn = (key, label, allowAll) => {
    const v = bf[key];
    const i = h('input', { type: 'text', inputmode: 'numeric', value: allowAll && v < 0 ? 'all' : String(v ?? 0) });
    i.addEventListener('input', () => {
      const t = i.value.trim().toLowerCase();
      const n = allowAll && t === 'all' ? -1 : parseInt(t, 10);
      if (!isNaN(n)) bf[key] = n;
    });
    return field(label, i);
  };
  const drawCustom = () => replace(customBox, preset !== 'custom' ? null : h('div', null,
    h('label', { class: 'check' }, h('input', { type: 'checkbox', checked: bf.enabled, onchange: (e) => { bf.enabled = e.target.checked; } }), 'Copy old messages'),
    numIn('max_initial_messages', 'Messages in a new chat'),
    numIn('max_catchup_messages', 'Missed messages per chat after the bridge was offline'),
    numIn('max_initial_conversations', 'Chats created right after linking ("all" for all)', true),
    h('label', { class: 'check' }, h('input', { type: 'checkbox', checked: bf.request_full_sync, onchange: (e) => { bf.request_full_sync = e.target.checked; } }),
      'Ask for a year of history (instead of about 3 months; only when linking)'),
    numIn('full_sync_days', 'Days of history to ask for (0 = WhatsApp default)')));
  const presetRadios = PRESETS.map(([id, title, text, values]) => h('label', { class: 'check' },
    h('input', {
      type: 'radio', name: 'preset', checked: preset === id,
      onchange: () => { preset = id; if (values) bf = Object.assign({}, values); drawCustom(); },
    }),
    h('span', null, h('b', null, title), ' – ', h('span', { class: 'muted' }, text))));
  const status = h('select', null,
    h('option', { value: 'off' }, 'Off (recommended): status updates are not bridged'),
    h('option', { value: 'muted' }, 'Muted chat: bridged into a muted, low-priority chat'),
    h('option', { value: 'on' }, 'Normal chat, with notifications'));
  status.value = opts.status_broadcast || 'off';
  const delOnLogout = h('input', { type: 'checkbox', checked: opts.delete_chats_on_logout !== false });
  const optErr = h('div');
  const saveOpts = btn('Save and restart the bridge', null, 'primary');
  saveOpts.addEventListener('click', () => busy(saveOpts, optErr, async () => {
    await api('POST', '/api/options', Object.assign({}, opts, {
      backfill: bf, status_broadcast: status.value, delete_chats_on_logout: delOnLogout.checked,
    }));
    toast('Saved. The bridge restarts.');
  }));
  drawCustom();

  const extra = h('textarea', { rows: 3, placeholder: '@friend:tjena.eu' }, (st.extra_users || []).join('\n'));
  const extraErr = h('div');
  const saveExtra = btn('Save', null);
  saveExtra.addEventListener('click', () => busy(saveExtra, extraErr, async () => {
    await api('POST', '/api/extra-users', { users: extra.value.split(/[\s,]+/).filter(Boolean) });
    toast('Saved.');
    await poll();
  }));

  const gw = h('input', { type: 'url', value: st.gateway_url || DEFAULT_GW });
  const gwErr = h('div');
  const saveGw = btn('Save', null);
  saveGw.addEventListener('click', () => busy(saveGw, gwErr, async () => {
    if (!confirm('Change the bridge gateway? Only do this if your tjena admin told you to.')) return;
    await api('POST', '/api/gateway', { url: gw.value });
    toast('Saved. The bridge restarts.');
    await poll();
  }));

  const auth = await api('GET', '/api/auth').catch(() => ({}));
  const oldPw = h('input', { type: 'password', autocomplete: 'current-password' });
  const newPw = h('input', { type: 'password', autocomplete: 'new-password' });
  const pwErr = h('div');
  const savePw = btn('Change password', null);
  savePw.addEventListener('click', () => busy(savePw, pwErr, async () => {
    await api('POST', '/api/auth/password', { old: oldPw.value, new: newPw.value });
    oldPw.value = newPw.value = '';
    toast('Password changed. Other browsers are signed out.');
  }));

  const dangerErr = h('div');
  if (view !== S.view) return;
  replace($app,
    card(h('h2', null, 'Chats & history'),
      h('h3', null, 'Old messages when linking WhatsApp'), presetRadios, customBox,
      field('WhatsApp status updates', status),
      h('label', { class: 'check' }, delOnLogout, h('span', null, 'When you log out a WhatsApp account, remove its chats from tjena',
        h('span', { class: 'small muted' }, ' (its space is always removed)'))),
      optErr, h('div', { class: 'row' }, saveOpts)),
    card(h('h2', null, 'Other people who may use this bridge'),
      h('p', { class: 'small muted' }, 'Full Matrix IDs, one per line. They can use the bridge bot but not change its setup. You (', st.owner, ') are always admin.'),
      extra, extraErr, h('div', { class: 'row' }, saveExtra)),
    card(h('h2', null, 'Bridge gateway'), gw, gwErr, h('div', { class: 'row' }, saveGw)),
    card(h('h2', null, 'Web UI password'),
      auth.env_password ? h('p', { class: 'muted' }, 'Set with WEBUI_PASSWORD. Change it in the container\'s environment.')
        : h('div', null, field('Current password', oldPw), field('New password (at least 10 characters)', newPw), pwErr, h('div', { class: 'row' }, savePw))),
    card(h('h2', null, 'Active bridge and removal'),
      h('p', null, 'If another device took over your bridge and you want it back here:'),
      h('div', { class: 'row' }, btn('Make this the active bridge', (e) => repair(e.target, dangerErr))),
      h('p', null, 'Remove the bridge: logs WhatsApp out, removes the bridge from the tjena server, signs this bridge out of ',
        'your tjena account and deletes all bridge data here. Your chats in tjena stay readable.'),
      h('div', { class: 'row' }, btn('Remove this bridge', (e) => busy(e.target, dangerErr, async () => {
        if (!confirm('Remove this bridge?\n\nWhatsApp is logged out and all bridge data here is deleted. This cannot be undone.')) return;
        await api('POST', '/api/remove', {});
        await poll();
      }), 'danger')),
      dangerErr));
}

// ------------------------------------------------------------------ backup & move

function renderBackup() {
  const p1 = h('input', { type: 'password', autocomplete: 'new-password' });
  const p2 = h('input', { type: 'password', autocomplete: 'new-password' });
  const expErr = h('div');
  const exp = btn('Download backup', null, 'primary');
  exp.addEventListener('click', () => busy(exp, expErr, async () => {
    if (p1.value.length < 8) throw new Error('Use a passphrase with at least 8 characters.');
    if (p1.value !== p2.value) throw new Error('The passphrases differ.');
    await download('POST', '/api/backup/export', { passphrase: p1.value }, 'tjena-bridge-backup.tjbk');
    p1.value = p2.value = '';
    toast('Backup downloaded.');
  }));
  const moveArea = h('div');
  const moveErr = h('div');
  const startMove = btn('Move to another device', null, 'primary');
  startMove.addEventListener('click', () => busy(startMove, moveErr, async () => {
    S.job = await api('POST', '/api/move/out', {});
    drawMove(S.job);
  }));
  const drawMove = (job) => {
    if (!job || job.kind !== 'move-out') { replace(moveArea, h('div', { class: 'row' }, startMove), moveErr); return; }
    const copy = () => navigator.clipboard.writeText(job.uri).then(() => toast('Code copied.'), () => toast('Copy failed; select the text instead.'));
    const cancel = btn('Cancel', async () => { await api('POST', '/api/job/cancel', {}); });
    switch (job.phase) {
      case 'waiting':
        return replace(moveArea,
          h('p', null, 'On the new device: sign in with the same tjena account and choose "Move from my old phone". ',
            'A phone with a camera scans this code. Without a camera (or on another Docker bridge) choose ',
            h('b', null, 'Connect without a code'), ' there; both devices then show a number to compare.'),
          h('div', { class: 'qr' }, h('img', { src: job.qr, alt: 'Move code' })),
          h('div', { class: 'mono' }, job.uri),
          h('div', { class: 'row' }, btn('Copy code', copy), cancel),
          h('p', { class: 'small muted' }, 'Your chats, the WhatsApp link and the encryption keys move over, encrypted with a key that is only in this code. ',
            'The tjena server only passes the data on. Don\'t show this code to anyone else. The bridge keeps running here until the new device connects.'));
      case 'compare':
        return replace(moveArea, sasBox(job));
      case 'sending': case 'importing':
        return replace(moveArea,
          h('p', null, h('b', null, job.progress || 'Sending…')),
          job.phase === 'sending' && job.size > 0 ? h('progress', { value: job.sent / job.size, max: 1 }) : h('progress'),
          job.size > 0 ? h('p', { class: 'small muted' }, mb(job.sent) + ' of ' + mb(job.size)) : null,
          h('p', null, 'The bridge is paused here. Keep this page open.'));
      case 'unsure':
        return replace(moveArea,
          notice('warn', 'Did the new device finish?', 'The new device received everything, but the connection ended before it confirmed. ' +
            'Look at the new device. Only one device may run the bridge.'),
          h('div', { class: 'row' },
            btn('It works there: remove it here', async () => { await api('POST', '/api/move/out/resolve', { remove: true }); await poll(); }, 'primary'),
            btn('It failed: keep the bridge here', async () => { await api('POST', '/api/move/out/resolve', { remove: false }); await poll(); })));
      case 'failed':
        return replace(moveArea, errBox(job.error || 'The move failed.'),
          h('p', null, 'The bridge keeps running here.'), h('div', { class: 'row' }, startMove));
      default:
        return replace(moveArea, h('p', null, job.progress || ''));
    }
  };
  S.onJob = drawMove;
  replace($app,
    card(h('h2', null, 'Move to another device'),
      h('p', null, 'To a phone (tjena Bridge app) or another Docker bridge. Same bridge bot, chats stay, WhatsApp stays linked.'),
      moveArea),
    card(h('h2', null, 'Backup'),
      h('p', null, 'An encrypted file with the bridge database and its keys. It works with the phone app too. ',
        'The bridge pauses for a few seconds while the backup is made.'),
      field('Passphrase (at least 8 characters)', p1), field('Repeat passphrase', p2),
      h('p', { class: 'small muted' }, 'Without the passphrase the backup can\'t be opened. Nobody can recover it.'),
      expErr, h('div', { class: 'row' }, exp)),
    card(h('h2', null, 'Restore'), restoreForm(true)));
  drawMove(S.job);
}

// ------------------------------------------------------------------ tools

function renderTools() {
  const out = h('div');
  const err = h('div');
  const show = (title, data) => {
    let text = data;
    try { text = JSON.stringify(typeof data === 'string' ? JSON.parse(data) : data, null, 2); } catch (_) { /* raw */ }
    replace(out, card(h('h2', null, title), h('pre', null, text)));
  };
  const info = (title, tool) => btn(title, (e) => busy(e.target, err, async () => {
    replace(out, card(h('progress')));
    show(title, await api('GET', '/api/tools/' + tool));
  }));
  const action = (title, tool, body, question) => btn(title, (e) => busy(e.target, err, async () => {
    if (!confirm(question)) return;
    replace(out, card(h('progress')));
    show(title, await api('POST', '/api/tools/' + tool, body || {}));
  }));
  replace($app,
    card(h('h2', null, 'Checks (read-only)'),
      h('p', { class: 'small muted' }, 'Read from the bridge\'s own database. Public keys only.'),
      h('div', { class: 'row' }, info('Security & keys', 'security'), info('History import', 'history'), info('Contact & chat names', 'names'))),
    card(h('h2', null, 'Repairs'),
      h('p', { class: 'small muted' }, 'Each one stops the bridge for a moment.'),
      h('div', { class: 'row' },
        action('Finish stopped history imports', 'history-catchup', { mode: 'both' },
          'Let the bridge finish history imports that stopped early, and create chats still waiting for a room?'),
        action('Stop history import', 'history-stop', {}, 'Stop creating and filling chats from history?'),
        action('Fix names', 'names-fix', {}, 'Apply the contact and chat names from the names check?'))),
    err, out);
}

// ------------------------------------------------------------------ logs

function renderLogs() {
  const pre = h('pre', null, 'Loading…');
  const auto = h('input', { type: 'checkbox', checked: true });
  const load = async () => {
    try {
      const r = await api('GET', '/api/logs');
      const atBottom = pre.scrollTop + pre.clientHeight >= pre.scrollHeight - 20;
      pre.textContent = r.text || '(nothing yet)';
      if (atBottom) pre.scrollTop = pre.scrollHeight;
    } catch (_) { /* keep */ }
  };
  replace($app, card(
    h('h2', null, 'Recent output'),
    h('p', { class: 'small muted' }, 'Secrets, phone numbers and message text are removed. Also in `docker logs`.'),
    h('div', { class: 'row' },
      h('label', { class: 'check' }, auto, 'Refresh automatically'),
      btn('Download full log', (e) => busy(e.target, null, () => download('GET', '/api/logs/export', undefined, 'tjena-bridge-log.txt')))),
    pre));
  load().then(() => { pre.scrollTop = pre.scrollHeight; });
  S.logTimer = setInterval(() => { if (auto.checked) load(); }, 3000);
}

boot();
