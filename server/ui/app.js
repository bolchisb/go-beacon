'use strict';

const REFRESH_MS = 2000;
const MAX_EVENTS = 200;

const $ = (sel) => document.querySelector(sel);
const tbody = $('#agents tbody');
const eventList = $('#events');

function humanDuration(sec) {
  sec = Math.max(0, Math.floor(sec));
  const d = Math.floor(sec / 86400);
  const h = Math.floor((sec % 86400) / 3600);
  const m = Math.floor((sec % 3600) / 60);
  const s = sec % 60;
  if (d) return `${d}d ${h}h`;
  if (h) return `${h}h ${m}m`;
  if (m) return `${m}m ${s}s`;
  return `${s}s`;
}

function humanBytes(n) {
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  let i = 0;
  while (n >= 1024 && i < units.length - 1) { n /= 1024; i++; }
  return `${i === 0 ? n : n.toFixed(1)} ${units[i]}`;
}

async function post(path) {
  const res = await fetch(path, { method: 'POST' });
  const body = await res.json().catch(() => ({}));
  if (!res.ok) throw new Error(body.error || `HTTP ${res.status}`);
  return body;
}

function renderAgents(agents) {
  $('#empty').style.display = agents.length ? 'none' : 'block';
  tbody.replaceChildren(...agents.map(buildRow));
}

// transportBadge says how the agent's connection reached the relay.
//
// Three states, not two, and the middle one is the reason this is worth
// drawing. A closed padlock is only shown when the relay terminated TLS itself
// and watched the handshake happen. Behind a proxy the relay sees plain HTTP
// and the only evidence is X-Forwarded-Proto, a header the sender writes -- so
// that gets its own mark and says, on hover, that it is a claim. Painting the
// same padlock for both would be the dashboard vouching for something nobody
// checked.
function transportBadge(a) {
  const span = document.createElement('span');
  span.className = 'lock';

  if (a.transport === 'tls') {
    span.classList.add('ok');
    span.innerHTML = LOCK_CLOSED;
    span.title = a.transport_detail ? `Encrypted — ${a.transport_detail}.` : 'Encrypted.';
    span.setAttribute('aria-label', 'encrypted');
    return span;
  }

  if (a.transport === 'proxy-tls') {
    span.classList.add('claimed');
    span.innerHTML = LOCK_CLOSED;
    span.title = 'Reported as https by X-Forwarded-Proto, from a peer this relay '
      + 'has not been told to trust. Name your proxy in BEACON_TRUSTED_PROXIES '
      + 'and this becomes a padlock; until then it is only what the caller said '
      + 'about itself.';
    span.setAttribute('aria-label', 'encryption reported by a proxy, not verified');
    return span;
  }

  span.classList.add('none');
  span.innerHTML = LOCK_OPEN;
  span.title = 'Not encrypted. The connection this relay accepted was plain HTTP.';
  span.setAttribute('aria-label', 'not encrypted');
  return span;
}

// Inline so they inherit currentColor and need no request of their own.
const LOCK_CLOSED = '<svg viewBox="0 0 12 14" width="10" height="12" aria-hidden="true">'
  + '<path d="M3 6V4a3 3 0 0 1 6 0v2" fill="none" stroke="currentColor" stroke-width="1.4"/>'
  + '<rect x="1.5" y="6" width="9" height="7" rx="1.2" fill="currentColor"/></svg>';

const LOCK_OPEN = '<svg viewBox="0 0 12 14" width="10" height="12" aria-hidden="true">'
  + '<path d="M3 6V4a3 3 0 0 1 5.6-1.5" fill="none" stroke="currentColor" stroke-width="1.4"/>'
  + '<rect x="1.5" y="6" width="9" height="7" rx="1.2" fill="none" stroke="currentColor" stroke-width="1.2"/></svg>';

function buildRow(a) {
  const tr = document.createElement('tr');
  if (!a.online) tr.className = 'offline';

  const cell = (text, cls) => {
    const td = document.createElement('td');
    if (cls) td.className = cls;
    td.textContent = text;
    return td;
  };

  const state = document.createElement('td');
  const dot = document.createElement('span');
  dot.className = `dot ${a.online ? 'up' : 'down'}`;
  state.append(dot, a.online ? 'connected' : 'disconnected');
  if (a.online) state.append(transportBadge(a));

  tr.append(
    state,
    copyCell(a.id),
    copyCell(a.hostname),
    cell(a.os && a.arch ? `${a.os}/${a.arch}` : '—'),
    copyCell(a.online ? a.remote_addr : ''),
    cell(humanDuration(a.since_seconds)),
    cell(a.rtt_ms == null ? '—' : `${a.rtt_ms.toFixed(1)} ms`, 'num'),
    cell(a.online ? a.streams : '—', 'num'),
    cell(a.online ? humanBytes(a.bytes_in) : '—', 'num'),
    cell(a.online ? humanBytes(a.bytes_out) : '—', 'num'),
    cell(a.reconnects, 'num'),
  );

  const actions = document.createElement('td');
  actions.append(
    terminalButton(a),
    actionButton('Test', a, async (btn) => {
      const { rtt_ms } = await post(`/api/agents/${encodeURIComponent(a.id)}/ping`);
      btn.textContent = `${rtt_ms.toFixed(1)} ms`;
    }),
    actionButton('Kick', a, () => post(`/api/agents/${encodeURIComponent(a.id)}/kick`)),
  );
  tr.append(actions);
  return tr;
}

const COPY_ICON =
  '<svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.4" aria-hidden="true">' +
  '<rect x="5.75" y="5.75" width="8.5" height="8.5" rx="1.5"/>' +
  '<path d="M10.25 3.25v-.5a1.5 1.5 0 0 0-1.5-1.5h-5a1.5 1.5 0 0 0-1.5 1.5v5a1.5 1.5 0 0 0 1.5 1.5h.5"/></svg>';

const CHECK_ICON =
  '<svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.8" aria-hidden="true">' +
  '<path d="M3 8.5 6.5 12 13 4.5"/></svg>';

// The dashboard is normally reached over plain HTTP on a private address, which
// is not a secure context, so navigator.clipboard does not exist there. The
// textarea fallback is what actually runs on the deployment we ship; the modern
// path is kept for when the relay sits behind TLS.
async function copyText(text) {
  if (window.isSecureContext && navigator.clipboard) {
    await navigator.clipboard.writeText(text);
    return;
  }
  const ta = document.createElement('textarea');
  ta.value = text;
  ta.setAttribute('readonly', '');
  ta.style.position = 'fixed';
  ta.style.top = '-1000px';
  document.body.appendChild(ta);
  ta.select();
  const ok = document.execCommand('copy');
  ta.remove();
  if (!ok) throw new Error('copy rejected');
}

// copyCell renders a value with a copy button beside it. An empty value gets a
// dash and no button: there is nothing to put on the clipboard.
function copyCell(text) {
  const td = document.createElement('td');
  if (!text) {
    td.textContent = '—';
    return td;
  }
  td.className = 'copyable';

  const label = document.createElement('span');
  label.textContent = text;

  const btn = document.createElement('button');
  btn.type = 'button';
  btn.className = 'copy';
  btn.title = `Copy ${text}`;
  btn.setAttribute('aria-label', `Copy ${text}`);
  btn.innerHTML = COPY_ICON;

  btn.addEventListener('click', async () => {
    try {
      await copyText(text);
      btn.className = 'copy ok';
      btn.innerHTML = CHECK_ICON;
    } catch {
      btn.className = 'copy bad';
    }
    setTimeout(() => { btn.className = 'copy'; btn.innerHTML = COPY_ICON; }, 1200);
  });

  td.append(label, btn);
  return td;
}

// The terminal opens in its own tab, so it deliberately skips actionButton's
// busy-then-restore cycle: there is nothing to wait for here.
function terminalButton(agent) {
  const btn = document.createElement('button');
  btn.textContent = 'SSH';
  btn.disabled = !agent.online;
  btn.addEventListener('click', () => {
    window.open(`terminal.html?agent=${encodeURIComponent(agent.id)}`, '_blank', 'noopener');
  });
  return btn;
}

function actionButton(label, agent, action) {
  const btn = document.createElement('button');
  btn.textContent = label;
  btn.disabled = !agent.online;
  btn.addEventListener('click', async () => {
    btn.disabled = true;
    const original = btn.textContent;
    btn.textContent = '…';
    try {
      await action(btn);
    } catch (err) {
      btn.textContent = 'failed';
      pushEvent({ type: 'error', agent_id: agent.id, message: `${original}: ${err.message}`, at: new Date().toISOString() });
    } finally {
      setTimeout(() => { btn.textContent = original; btn.disabled = false; }, 2000);
    }
  });
  return btn;
}

function pushEvent(e) {
  const li = document.createElement('li');

  const ts = document.createElement('span');
  ts.className = 'ts';
  ts.textContent = new Date(e.at).toLocaleTimeString();

  const who = document.createElement('span');
  who.className = 'who';
  who.textContent = e.agent_id || 'server';

  const msg = document.createElement('span');
  msg.className = e.type;
  msg.textContent = e.message;

  li.append(ts, who, msg);
  eventList.prepend(li);
  while (eventList.childElementCount > MAX_EVENTS) eventList.lastElementChild.remove();
}

async function refresh() {
  try {
    const [agents, info] = await Promise.all([
      fetch('/api/agents').then((r) => r.json()),
      fetch('/api/server').then((r) => r.json()),
    ]);
    renderAgents(agents);
    $('#stat-online').textContent = info.agents_online;
    $('#stat-known').textContent = info.agents_known;
    $('#stat-uptime').textContent = humanDuration(info.uptime_seconds);
    $('#stat-version').textContent = info.version;
    // Empty until an operator account exists, which keeps the header quiet on
    // a relay that has not been set up yet.
    $('#stat-operator').textContent = info.operator || '';
    // No account, nothing to change: the button would only lead to a form that
    // cannot succeed.
    $('#account-open').hidden = !info.operator;
    renderVault(info.vault);
  } catch (err) {
    // the SSE indicator already reports that the server is unreachable
  }
}

// renderVault says whether enrolling an agent will work right now. An operator
// does not need Vault's internals, only whether the door is open.
function renderVault(state) {
  const el = $('#stat-vault');
  const label = {
    unsealed: 'vault open',
    sealed: 'vault sealed',
    unreachable: 'vault unreachable',
    'not configured': 'no vault',
  };
  const cls = {
    unsealed: 'unsealed',
    sealed: 'sealed',
    unreachable: 'unreachable',
    'not configured': 'absent',
  };
  const title = {
    unsealed: 'Vault is open. Agents can be enrolled.',
    sealed: 'Vault is sealed. Connected agents keep working; new ones cannot be enrolled.',
    unreachable: 'The relay cannot reach Vault. Connected agents keep working; new ones cannot be enrolled.',
    'not configured': 'No Vault is configured for this relay.',
  };
  el.textContent = label[state] || 'vault ?';
  el.className = 'vault ' + (cls[state] || '');
  el.title = title[state] || '';
}

function connectEvents() {
  const src = new EventSource('/api/events');
  const link = $('#stat-link');

  src.onopen = () => { link.textContent = 'live'; link.className = 'up'; };
  src.onerror = () => { link.textContent = 'disconnected'; link.className = 'down'; };
  src.onmessage = (msg) => {
    pushEvent(JSON.parse(msg.data));
    refresh(); // an event means the table just changed
  };
}

connectEvents();
refresh();
setInterval(refresh, REFRESH_MS);

// ---- account dialog ----------------------------------------------------
// <dialog> handles centring, the backdrop and Esc. What is left is opening it,
// posting the form, and not closing on failure -- a dialog that vanishes while
// showing why it failed is worse than one that stays.
const accountDialog = document.getElementById('account');
const passwordForm = document.getElementById('password-form');
const passwordNote = document.getElementById('password-note');

function openAccount() {
  passwordForm.reset();
  passwordNote.className = '';
  passwordNote.textContent = '';
  accountDialog.showModal();
}

function closeAccount() {
  accountDialog.close();
}

document.getElementById('account-open').addEventListener('click', openAccount);
document.getElementById('account-close').addEventListener('click', closeAccount);
document.getElementById('account-cancel').addEventListener('click', closeAccount);

// Clicking the backdrop closes it. The dialog element itself covers only the
// panel, so a click landing on it directly means the click was outside.
accountDialog.addEventListener('click', (e) => {
  if (e.target === accountDialog) closeAccount();
});

passwordForm.addEventListener('submit', async (e) => {
  e.preventDefault();
  const submit = document.getElementById('password-submit');
  submit.disabled = true;
  passwordNote.className = '';
  passwordNote.textContent = 'Saving\u2026';

  try {
    const res = await fetch('/api/operator/password', {
      method: 'POST',
      headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
      body: new URLSearchParams(new FormData(passwordForm)),
    });
    const body = await res.json().catch(() => ({}));

    if (res.ok) {
      passwordNote.className = 'ok';
      passwordNote.textContent = 'Changed.';
      passwordForm.reset();
      setTimeout(closeAccount, 900);
    } else {
      passwordNote.className = 'err';
      passwordNote.textContent = body.error || 'Could not change the password.';
    }
  } catch (err) {
    passwordNote.className = 'err';
    passwordNote.textContent = 'The relay did not answer.';
  } finally {
    submit.disabled = false;
  }
});
