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

  tr.append(
    state,
    cell(a.id),
    cell(a.hostname || '—'),
    cell(a.os && a.arch ? `${a.os}/${a.arch}` : '—'),
    cell(a.online ? a.remote_addr : '—'),
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
  } catch (err) {
    // the SSE indicator already reports that the server is unreachable
  }
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
