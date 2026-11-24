
'use strict';
const API = '/api';
const app = document.getElementById('app');
// ---------- storage (always guarded) ----------
function load(k, d) { try { const v = localStorage.getItem(k); return v == null ? d : JSON.parse(v); } catch (e) { return d; } }
function store(k, v) { try { if (v == null) localStorage.removeItem(k); else localStorage.setItem(k, JSON.stringify(v)); } catch (e) { /* storage unavailable */ } }
let token = load('casekit.token', '') || '';
let keys = load('casekit.keys', {}) || {};
function saveKey(id, key) { keys[id] = key; store('casekit.keys', keys); }

// ---------- DOM helper: text only, never innerHTML ----------
function h(tag, props, ...kids) {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(props || {})) {
    if (v == null || v === false) continue;
    if (k === 'class') el.className = v;
    else if (k.startsWith('on')) el.addEventListener(k.slice(2), v);
    else if (['value', 'checked', 'disabled', 'open'].includes(k)) el[k] = v;
    else el.setAttribute(k, v === true ? '' : String(v));
  }
  return add(el, kids);
}
function add(el, kids) { for (const k of kids.flat(Infinity)) if (k != null && k !== false) el.append(k instanceof Node ? k : String(k)); return el; }

// ---------- dialogs: the app's own confirm, alert and prompt (never the browser's) ----------
// Each returns a promise: confirm -> true|false, alert -> undefined, prompt -> text|null.
const ui = (() => {
  function open({ title, message, kind = 'info', ok = 'OK', cancel = null, input = null, danger = false }) {
    return new Promise(resolve => {
      const prev = document.activeElement, dlg = h('dialog', { class: 'dlg ' + kind, 'aria-labelledby': 'dlg_t', 'aria-describedby': 'dlg_m' });
      const field = input ? h('input', { type: input.type || 'text', id: 'dlg_in', value: input.value || '', placeholder: input.placeholder || '', 'aria-label': input.label || title, readonly: input.readonly ? '' : null }) : null;
      let result = cancel ? (input ? null : false) : undefined;
      const okBtn = h('button', { type: 'submit', class: danger ? 'danger' : 'primary', value: 'ok' }, ok);
      const noBtn = cancel ? h('button', { type: 'button', class: 'ghost', onclick: () => dlg.close() }, cancel) : null;
      const form = h('form', { method: 'dialog', class: 'dlg-in', onsubmit: () => { result = input ? field.value : true; } },
        h('h2', { id: 'dlg_t' }, title || (kind === 'error' ? 'Something went wrong' : kind === 'warn' ? 'Please confirm' : 'Notice')),
        h('div', { id: 'dlg_m', class: 'dlg-msg' }, String(message || '').split('\n').map((l, i) => h('p', { key: i }, l))),
        field ? h('div', { class: 'dlg-field' }, field) : null,
        h('div', { class: 'dlg-actions' }, noBtn, okBtn));
      dlg.append(form);
      dlg.addEventListener('close', () => { dlg.remove(); try { prev && prev.focus && prev.focus(); } catch (e) { /* the opener is gone */ } resolve(result); });
      dlg.addEventListener('cancel', () => { result = cancel ? (input ? null : false) : undefined; });
      document.body.append(dlg); dlg.showModal();
      (field || (danger && noBtn) || okBtn).focus(); if (field && field.select) field.select();
    });
  }
  return {
    confirm: o => open({ kind: o.danger ? 'warn' : 'info', ok: o.confirmLabel || 'Confirm', cancel: o.cancelLabel || 'Cancel', ...o }),
    alert: (message, title, kind) => open({ message, title, kind: kind || 'info', ok: 'OK' }),
    error: (message, title) => open({ message, title: title || 'Something went wrong', kind: 'error', ok: 'OK' }),
    prompt: o => open({ ok: o.confirmLabel || 'OK', cancel: o.cancelLabel || 'Cancel', input: { value: o.value, label: o.label, placeholder: o.placeholder, readonly: o.readonly, type: o.type }, ...o }),
    open
  };
})();
const badgeClass = s => ({ passed: 'b-ok', completed: 'b-ok', approved: 'b-ok', verified: 'b-ok', active: 'b-info', in_progress: 'b-info',
  returned: 'b-warn', flagged: 'b-warn', waived: 'b-warn', failed: 'b-bad', rejected: 'b-bad', withdrawn: 'b-bad',
  on_track: 'b-info', met: 'b-ok', warning: 'b-warn', breached: 'b-bad', approve: 'b-ok', reject: 'b-bad' }[s] || '');
const badge = (s, label) => h('span', { class: 'badge ' + badgeClass(s) }, label || String(s || '').replace(/_/g, ' '));
const when = t => { if (!t) return ''; const d = new Date(t); return isNaN(d) ? String(t) : d.toLocaleString(); };
const safeId = p => 'f_' + String(p).replace(/[^\w-]/g, '_');
const cap = s => s.charAt(0).toUpperCase() + s.slice(1);
// Relative time ("in 3h 5m", "2d 4h ago"); elements carrying data-rel refresh every 30s.
function rel(t) {
  const d = new Date(t); if (!t || isNaN(d)) return '';
  const diff = d - Date.now(), sec = Math.round(Math.abs(diff) / 1000);
  if (sec < 120) return diff < 0 ? sec + 's ago' : 'in ' + sec + 's';
  const m = Math.round(sec / 60);
  const dd = Math.floor(m / 1440), hh = Math.floor((m % 1440) / 60), mm = m % 60;
  const s = dd ? dd + 'd ' + hh + 'h' : hh ? hh + 'h ' + mm + 'm' : mm + 'm';
  return diff < 0 ? s + ' ago' : 'in ' + s;
}
const relEl = t => h('span', { 'data-rel': t }, rel(t));

function lockIcon() {
  const ns = 'http://www.w3.org/2000/svg', mk = (t, a) => { const e = document.createElementNS(ns, t); for (const k in a) e.setAttribute(k, a[k]); return e; };
  const s = mk('svg', { viewBox: '0 0 16 16', width: 14, height: 14, 'aria-hidden': 'true', class: 'ico' });
  s.append(mk('rect', { x: 3, y: 7, width: 10, height: 7, rx: 1.5, fill: 'currentColor' }),
    mk('path', { d: 'M5 7V5a3 3 0 0 1 6 0v2', fill: 'none', stroke: 'currentColor', 'stroke-width': 1.6 }));
  return s;
}
const hours = x => { if (x == null || isNaN(x)) return '—'; if (x < 1) return Math.round(x * 60) + 'm'; return x < 48 ? (Math.round(x * 10) / 10) + 'h' : (Math.round(x / 2.4) / 10) + 'd'; };
const pct = x => x == null || isNaN(x) ? '—' : Math.round(x * 100) + '%';

// ---------- API ----------
// anon: send neither the token nor an access key (external links).
async function api(method, path, body, caseId, anon) {
  const headers = { Accept: 'application/json' };
  if (body !== undefined) headers['Content-Type'] = 'application/json';
  if (token && !anon) headers.Authorization = 'Bearer ' + token;
  if (!anon && caseId && keys[caseId]) headers['X-Access-Key'] = keys[caseId];
  const res = await fetch(API + path, { method, headers, body: body === undefined ? undefined : JSON.stringify(body) });
  let json = null;
  try { json = await res.json(); } catch (e) { /* empty or non-JSON body */ }
  if (!res.ok) {
    // Failures render as {"error": {"code", "message", "details": [{path, rule, message}]}}.
    const e = json && json.error && typeof json.error === 'object' ? json.error : (json || {});
    const msg = e.message || (json && typeof json.error === 'string' ? json.error : '') || (res.status + ' ' + res.statusText);
    if (res.status === 401 && token && !anon) { token = ''; store('casekit.token', null); renderIdent(); const x = new Error('Your sign-in is no longer valid (the server restarted or it expired). Sign in again at the top.'); x.status = 401; x.details = []; throw x; }
    const err = new Error(msg); err.status = res.status; err.details = Array.isArray(e.details) ? e.details : [];
    throw err;
  }
  return json;
}


// ---------- identity ----------
function claims() {
  try { const p = token.split('.')[1], bin = atob(p.replace(/-/g, '+').replace(/_/g, '/')); return JSON.parse(new TextDecoder().decode(Uint8Array.from(bin, ch => ch.charCodeAt(0)))); } catch (e) { return null; }
}
function renderIdent() {
  const c = claims(), who = document.getElementById('who');
  const name = !token ? 'Not signed in' : c ? String(c.name || c.email || c.preferred_username || c.sub || '?').replace(/^demo-/, '') : 'Signed in', rl = c && [].concat(c.roles || []).join(', ');
  who.replaceChildren(h('i', null, name.charAt(0)), h('span', null, name, h('small', null, token ? (rl || 'no role') : 'sign in to work on cases')));
  document.getElementById('signIn').textContent = token ? 'Switch account' : 'Sign in';
  document.getElementById('signIn').className = token ? '' : 'primary';
  document.getElementById('tokClear').hidden = !token;
  document.querySelectorAll('.side nav a').forEach(a => { const p = a.getAttribute('href'); if (p === location.pathname || (p !== '/' && location.pathname.startsWith(p + '/')) || (p === '/work' && location.pathname.startsWith('/tasks'))) a.setAttribute('aria-current', 'page'); else a.removeAttribute('aria-current'); });
}
// The roles the signed-in person holds (their own, and those given for a workspace as "unit:role").
const myRoles = () => { const c = claims() || {}; return [...[].concat(c.roles || []), ...[].concat(c.workspace_roles || []).map(x => String(x).split(':').pop())]; };
function setToken(t) { token = t || ''; store('casekit.token', token || null); renderIdent(); reloadRoute(); }
document.getElementById('tokClear').addEventListener('click', async () => { if (await ui.confirm({ title: 'Sign out?', message: 'You will be signed out on this device.', confirmLabel: 'Sign out' })) setToken(''); });
document.getElementById('signIn').addEventListener('click', () => signIn());

// The sign-in dialog: email and password for people with an account, one-click accounts on a demo
// server, or a pasted token. Failures stay in the dialog, in words.
let demoAccounts = null;
async function loadDemo() {
  if (demoAccounts === null) demoAccounts = await fetch('/demo/accounts', { headers: { Accept: 'application/json' } }).then(r => r.ok ? r.json() : []).catch(() => []);
  return demoAccounts;
}
function signIn(opts = {}) {
  return new Promise(async resolve => {
    const demo = await loadDemo();
    let tab = 'password', error = '', busy = false;
    const dlg = h('dialog', { class: 'dlg info signin', 'aria-labelledby': 'si_t' });
    const finish = tk => { token = tk; store('casekit.token', token); dlg.close(); renderIdent(); resolve(true); if (pageKind() === 'accept') nav('/work'); else reloadRoute(); };
    const fail = m => { error = m; busy = false; draw(); };
    const body = () => {
      if (tab === 'demo') {
        const groups = {}; demo.forEach(a => { (groups[a.group] = groups[a.group] || []).push(a); });
        return h('div', { class: 'stack' }, h('p', { class: 'muted' }, 'Sample accounts on this demo server. Each one holds the role named on it, in the workspace shown.'),
          Object.keys(groups).map(g => h('div', { class: 'stack' }, h('div', { class: 'group-title' }, g), h('div', { class: 'si-grid' },
            groups[g].map(a => h('button', { type: 'button', title: a.hint, onclick: () => finish(a.token) }, h('b', null, a.label), h('small', null, a.hint))))))); }
      if (tab === 'token') {
        const f = h('input', { type: 'password', id: 'si_tok', autocomplete: 'off', placeholder: 'Paste an access token', 'aria-label': 'Access token' });
        return h('form', { class: 'stack', onsubmit: e => { e.preventDefault(); if (!f.value.trim()) return fail('Paste a token first.'); finish(f.value.trim()); } },
          h('p', { class: 'muted' }, 'For service accounts and testing. A token is issued by an administrator and carries its roles.'), f, h('div', { class: 'dlg-actions' }, h('button', { type: 'submit', class: 'primary' }, 'Use token'))); }
      const email = h('input', { type: 'email', id: 'si_email', autocomplete: 'username', required: '', placeholder: 'you@hospital.org' });
      const pw = h('input', { type: 'password', id: 'si_pw', autocomplete: 'current-password', required: '' });
      return h('form', { class: 'stack', onsubmit: async e => {
        e.preventDefault(); if (busy) return; busy = true; error = '';
        try { const r = await api('POST', '/login', { email: email.value.trim(), password: pw.value }, undefined, true); finish(r.token); }
        catch (er) { fail(er.status === 401 || er.status === 403 || /invalid|credential|password/i.test(er.message) ? 'That email and password do not match. Check them and try again, or ask an administrator for an invitation.' : er.message); }
      } }, h('label', { for: 'si_email' }, 'Email', email), h('label', { for: 'si_pw' }, 'Password', pw),
        h('div', { class: 'dlg-actions' }, h('button', { type: 'submit', class: 'primary' }, 'Sign in')),
        h('p', { class: 'muted small' }, 'No account yet? An administrator invites you by sending a link where you choose your password.')); };
    function draw() {
      const tabs = [['password', 'Email and password'], ...(demo.length ? [['demo', 'Demo accounts']] : []), ['token', 'Token']];
      dlg.replaceChildren(h('div', { class: 'dlg-in' }, h('h2', { id: 'si_t' }, opts.title || 'Sign in'),
        opts.reason ? h('p', { class: 'dlg-msg muted' }, opts.reason) : null,
        h('div', { class: 'tabs', role: 'tablist' }, tabs.map(([k, l]) => h('button', { type: 'button', role: 'tab', 'aria-selected': String(tab === k), onclick: () => { tab = k; error = ''; draw(); } }, l))),
        error ? h('div', { class: 'banner error', role: 'alert' }, error) : null, body(),
        h('div', { class: 'dlg-actions' }, h('button', { type: 'button', class: 'ghost', onclick: () => dlg.close() }, 'Cancel'))));
      const first = dlg.querySelector('input'); if (first) first.focus();
    }
    dlg.addEventListener('close', () => { dlg.remove(); resolve(false); });
    document.body.append(dlg); draw(); dlg.showModal();
    const f = dlg.querySelector('input'); if (f) f.focus();
  });
}

// ---------- inbox: what is waiting for me (questions, assignments, overdue answers) ----------
let inboxRows = [];
async function loadInbox() {
  const bell = document.getElementById('bell'); if (!bell) return;
  if (!token) { bell.hidden = true; return; }
  try { inboxRows = await api('GET', '/inbox') || []; bell.hidden = false; }
  catch (e) { bell.hidden = true; return; }
  const n = inboxRows.filter(r => !r.read).length, b = document.getElementById('bellN');
  b.textContent = String(n); b.hidden = !n; bell.classList.toggle('has', n > 0);
}
function inboxDialog() {
  const dlg = h('dialog', { class: 'dlg info inbox', 'aria-labelledby': 'ib_t' });
  const markRead = async r => { if (r.read) return; try { await api('POST', '/inbox/' + encodeURIComponent(r.id) + '/read', { read: 1 }); r.read = 1; } catch (e) { /* it stays unread */ } };
  const draw = () => {
    const unread = inboxRows.filter(r => !r.read);
    dlg.replaceChildren(h('div', { class: 'dlg-in' }, h('h2', { id: 'ib_t' }, 'Inbox', unread.length ? h('span', { class: 'count' }, String(unread.length)) : null),
      inboxRows.length ? h('ul', { class: 'inbox-list' }, inboxRows.map(r => h('li', { class: r.read ? '' : 'unread' },
        h('a', { href: r.case_id ? '/cases/' + encodeURIComponent(r.case_id) : '#', onclick: async e => { e.preventDefault(); await markRead(r); loadInbox(); dlg.close(); if (r.case_id) nav('/cases/' + encodeURIComponent(r.case_id)); } },
          h('strong', null, r.subject), r.body ? h('div', { class: 'muted small' }, r.body) : null, h('div', { class: 'faint small' }, when(r.at)))))) : h('div', { class: 'empty' }, h('b', null, 'Nothing waiting'), 'Questions for you, cases assigned to you and overdue answers show up here.'),
      h('div', { class: 'dlg-actions' }, unread.length ? h('button', { type: 'button', onclick: async () => { for (const r of unread) await markRead(r); await loadInbox(); draw(); } }, 'Mark all as read') : null, h('button', { type: 'button', class: 'primary', onclick: () => dlg.close() }, 'Close'))));
  };
  dlg.addEventListener('close', () => { dlg.remove(); loadInbox(); });
  document.body.append(dlg); draw(); dlg.showModal();
}
document.getElementById('bell').addEventListener('click', async () => { await loadInbox(); inboxDialog(); });
setInterval(() => { if (!document.hidden) loadInbox(); }, 20000);
setTimeout(loadInbox, 300);

// ---------- state ----------
const S = { view: null, vals: {}, reps: {}, dirty: new Set(), errs: {}, notice: null, flash: null, steps: {}, verdicts: {}, nodeIn: {}, comment: '', busy: false, key: '', acks: {}, review: null, panel: '' };
function page(...kids) { app.replaceChildren(...kids.flat().filter(Boolean)); }
function notice() {
  const n = S.notice; if (!n) return null;
  return h('div', { class: 'banner ' + n.kind, role: n.kind === 'error' ? 'alert' : 'status' }, n.text, n.list && n.list.length ? h('ul', null, n.list.map(t => h('li', null, t))) : null);
}
const errList = list => (list || []).map(m => h('div', { class: 'err', role: 'alert' }, m));

// ---------- live: countdowns tick every second; the page follows the server ----------
// Deadlines are kept by the server (warning, breach, reassignment happen there), so besides ticking the
// clock the case pages quietly ask for the current state every few seconds and redraw when it changed.
let liveTimer = null, liveBusy = false, liveSig = '', liveNote = false;
function tickClock() {
  const now = Date.now(); let crossed = false;
  document.querySelectorAll('[data-rel]').forEach(e => { const t = rel(e.getAttribute('data-rel')); if (e.textContent !== t) e.textContent = t; });
  document.querySelectorAll('.sla[data-sla-due]:not([data-sla-held])').forEach(el => {
    const start = +el.dataset.slaStart, due = +el.dataset.slaDue, late = now > due;
    const label = el.querySelector('.sla-label'), fill = el.querySelector('.fill');
    const text = late ? 'Overdue by ' + fmtDur((now - due) / 1000) : fmtDur((due - now) / 1000) + ' left';
    if (label && label.textContent !== text) label.textContent = text;
    if (fill) fill.style.width = Math.max(2, Math.min(100, Math.round(((now - start) / Math.max(1, due - start)) * 100))) + '%';
    if (late && !el.classList.contains('breached')) { el.classList.remove('warning', 'on_track', 'met'); el.classList.add('breached'); crossed = true; }
  });
  document.querySelectorAll('[data-sla-badge-due]').forEach(b => { if (!document.querySelector('[data-sla-held]') && now > +b.dataset.slaBadgeDue) { b.className = 'badge b-bad'; b.textContent = 'SLA missed'; b.removeAttribute('data-sla-badge-due'); crossed = true; } });
  if (crossed) setTimeout(livePoll, 600); // the server decides what a missed deadline does, so ask it
}
setInterval(tickClock, 1000);
const typing = () => { const a = document.activeElement; return a && /^(INPUT|TEXTAREA|SELECT)$/.test(a.tagName) && !a.closest('.side') || document.querySelector('dialog[open]'); };
const sigOf = v => v ? [v.case && v.case.revision, v.case && v.case.status, (v.stage && v.stage.name) || (v.case && v.case.stage), v.work && v.work.sla && v.work.sla.status, v.work && v.work.sla && v.work.sla.due_at, v.work && v.work.assignee, !!(v.work && v.work.suspended)].join('|') : '';
async function livePoll() {
  if (liveBusy || document.hidden || !token && pageKind() !== 'overview' && pageKind() !== 'task') return;
  const kind = pageKind(), parts = location.pathname.split('/').filter(Boolean).map(decodeURIComponent);
  liveBusy = true;
  try {
    if (kind === 'overview') {
      const ov = await api('GET', '/cases/' + encodeURIComponent(parts[1]) + '/overview', undefined, parts[1]), sig = sigOf(ov);
      if (liveSig && sig !== liveSig && !typing()) { S.ov = ov; renderOverview(); }
      if (!liveSig || !typing()) liveSig = sig;
    } else if (kind === 'task') {
      const v = await api('GET', '/cases/' + encodeURIComponent(parts[1]) + (parts[2] ? '/stages/' + encodeURIComponent(parts[2]) : ''), undefined, parts[1]), sig = sigOf(v);
      if (liveSig && sig !== liveSig) {
        const same = v.case.revision === S.view.case.revision && ((v.stage && v.stage.name) === (S.view.stage && S.view.stage.name));
        if (same) { S.view.work = v.work; S.view.stage = v.stage; S.view.case = v.case; if (!typing() && !S.dirty.size && !S.review) renderTask(); }
        else if (!S.dirty.size && !typing() && !S.review) { applyView(v); renderTask(); }
        else if (!liveNote) { liveNote = true; S.notice = { kind: 'warn', text: 'This case was updated by someone or something else. Finish what you are doing, then reload to see the latest.' }; if (!S.review) renderTask(); }
      }
      liveSig = liveSig && typing() ? liveSig : sig;
    } else if (kind === 'queue') {
      await showQueue(new URLSearchParams(location.search).get('scope') || 'queue', true);
    }
  } catch (e) { /* offline or signed out: the next poll tries again */ }
  liveBusy = false;
}
function startLive() {
  clearInterval(liveTimer); liveSig = ''; liveNote = false;
  const kind = pageKind(); if (!['overview', 'task', 'queue'].includes(kind)) return;
  liveTimer = setInterval(livePoll, kind === 'queue' ? 10000 : 4000);
  setTimeout(() => { liveSig = ''; if (S.ov && kind === 'overview') liveSig = sigOf(S.ov); else if (S.view && kind === 'task') liveSig = sigOf(S.view); }, 800);
}
document.addEventListener('visibilitychange', () => { if (!document.hidden) livePoll(); });

// ---------- router ----------
// Pages are server-rendered SPL templates. This script only brings the interactive ones to life.
const nav = to => location.assign(to);
const pageKind = () => { const p = location.pathname; return p === '/' ? 'home' : p.startsWith('/start/') ? 'intro' : p.startsWith('/cases/') ? 'overview' : p.startsWith('/tasks/') ? 'task' : p.startsWith('/links/') ? 'link' : p === '/work' ? 'queue' : p === '/workspace' ? 'workspace' : p === '/accept' ? 'accept' : ''; };
const bounce = (kind, text, to) => { S.flash = { kind, text }; try { sessionStorage.setItem('casekit.flash', JSON.stringify(S.flash)); } catch (e) { /* ignore */ } nav(to); };
async function route() {
  const kind = pageKind();
  if (token && !Object.keys(roster).length) await loadRoster();
  try { const f = sessionStorage.getItem('casekit.flash'); if (f) { S.notice = JSON.parse(f); sessionStorage.removeItem('casekit.flash'); } } catch (e) { /* ignore */ }
  try {
    const parts = location.pathname.split('/').filter(Boolean).map(decodeURIComponent);
    if (kind === 'overview') await showOverview(parts[1]);
    else if (kind === 'task') await showTask(parts[1], parts[2] || '');
    else if (kind === 'link') await showLink(parts[1]);
    else if (kind === 'workspace') await showWorkspace();
    else if (kind === 'accept') showAccept();
    else if (kind === 'queue') await showQueue(new URLSearchParams(location.search).get('scope') || 'queue');
    else if (kind === 'home') homeInit();
    else if (kind === 'intro') introInit();
    startLive();
  } catch (e) {
    screen({ title: 'Something went wrong' }, h('div', { class: 'banner error', role: 'alert' }, e.message),
      e.status === 401 || e.status === 403 || e.status === 404 ? h('p', { class: 'muted' }, 'You may need to sign in, or this case needs its access key.') : null,
      h('div', null, h('a', { class: 'btn', href: '/' }, 'Back home')));
  }
}
// Sign-in changes what a page shows, so reload it (the server-rendered pages are the same for everyone).
function reloadRoute() { if (['overview', 'task', 'queue', 'workspace'].includes(pageKind())) route(); else location.reload(); }

// The workspace tree the caller may see, flattened with its depth, and its units of a level.
async function workspaceTree() {
  if (S.wsTree) return S.wsTree;
  const roots = await api('GET', '/workspaces/tree') || [], out = [];
  const walk = (n, depth) => { out.push({ id: n.id, name: n.name, level: n.level, parent: n.parent_id, depth }); (n.children || []).forEach(c => walk(c, depth + 1)); };
  roots.forEach(n => walk(n, 0));
  return (S.wsTree = out);
}
async function loadWorkspaces(level) { return (await workspaceTree()).filter(n => n.level === level); }
const wsName = id => { const n = (S.wsTree || []).find(x => x.id === id); return n ? n.name : id; };

// The home page lists this device's applications and takes a case id to resume.
function homeInit() {
  const mine = document.getElementById('mine'), ids = Object.keys(keys);
  if (mine && ids.length) { mine.hidden = false; const ul = mine.querySelector('ul'); ids.forEach(id => ul.append(h('li', null, h('a', { href: '/cases/' + encodeURIComponent(id) }, id)))); }
  const f = document.getElementById('resume');
  if (f) f.addEventListener('submit', e => { e.preventDefault(); const id = f.elements.id.value.trim(), k = f.elements.key.value.trim(); if (!id) return; if (k) saveKey(id, k); nav('/cases/' + encodeURIComponent(id)); });
  if (S.notice) { const n = notice(), b = document.querySelector('.body'); if (n && b) b.prepend(n); }
}
// The intro page's button starts a case.
function introInit() {
  const conf = document.getElementById('ui-conf'); if (conf) applyUI({ accent: conf.dataset.accent, density: conf.dataset.density, brand: conf.dataset.brand });
  // The author's HTML runs in a frame with no scripts; same-origin only lets us size it to its content.
  const fr = document.querySelector('iframe.intro-html');
  if (fr) { const fit = () => { try { fr.style.height = (fr.contentDocument.documentElement.scrollHeight + 4) + 'px'; } catch (e) { /* keep the default height */ } }; fr.addEventListener('load', fit); if (fr.contentDocument && fr.contentDocument.readyState === 'complete') fit(); }
  const btn = document.querySelector('[data-start]'); if (!btn) return;
  // Who does what: every step of the workflow and the role that handles it, and whether the person
  // looking at this page can start it. Starting belongs to the first step's role.
  const slot = document.getElementById('ws-pick');
  let canStart = true;
  const gate = h('div', { class: 'stack' }); slot.before(gate);
  api('GET', '/pipelines/' + encodeURIComponent(btn.dataset.start), undefined, undefined, true).then(d => {
    const stages = d.stages || [], first = stages[0] || {}, startRoles = first.public ? [] : (first.roles || []), mine = myRoles();
    const title = d.title || d.name;
    canStart = first.public || !startRoles.length || startRoles.some(r => mine.includes(r));
    const you = !token ? null : (claims() && (claims().name || claims().sub)) || 'you';
    let status;
    if (first.public || !startRoles.length) status = null;
    else if (!token) status = h('div', { class: 'banner warn' }, h('b', null, 'Sign in to start. '), title + ' is started by the ' + startRoles.join(' or ') + ' role.', ' ', h('button', { type: 'button', class: 'primary', onclick: () => signIn({ reason: 'To start ' + title + ' you need an account with the ' + startRoles.join(' or ') + ' role.' }) }, 'Sign in'));
    else if (!canStart) status = h('div', { class: 'banner error', role: 'alert' }, h('b', null, 'You cannot start this. '), 'You are signed in as ' + you + (mine.length ? ' with the ' + mine.join(', ') + ' role' : ' with no role') + '. ' + title + ' is started by the ' + startRoles.join(' or ') + ' role. ',
      h('button', { type: 'button', onclick: () => signIn({ reason: 'Sign in as someone with the ' + startRoles.join(' or ') + ' role.' }) }, 'Switch account'));
    else status = h('div', { class: 'banner ok' }, 'You are signed in with the ' + mine.filter(r => startRoles.includes(r)).join(', ') + ' role, so you can start this.');
    btn.disabled = !canStart && !!token;
    // A workflow can cap how much data one person creates; say where they stand, and stop at the cap.
    if (d.limits && d.limits.per_user && token && canStart) api('GET', '/cases?scope=mine&pipeline=' + encodeURIComponent(d.name)).then(mineRows => {
      const openOnly = d.limits.count === 'open', done = ['approved', 'rejected', 'completed', 'withdrawn'];
      const used = (mineRows || []).filter(r => !openOnly || !done.includes(r.status)).length, left = d.limits.per_user - used, what = (openOnly ? 'unfinished ' : '') + title.toLowerCase();
      if (left <= 0) { btn.disabled = true; gate.querySelector('.banner.ok') && gate.querySelector('.banner.ok').remove(); gate.prepend(h('div', { class: 'banner error', role: 'alert' }, h('b', null, 'Limit reached. '), d.limits.message || 'You have ' + used + ' of ' + d.limits.per_user + ' ' + what + ' you can create. ' + (openOnly ? 'Finish or withdraw one to start another.' : 'Ask an administrator if you need more.'))); }
      else gate.prepend(h('div', { class: 'banner' }, 'You have ' + used + ' of ' + d.limits.per_user + ' ' + what + ' you can create (' + left + ' left).'));
    }).catch(() => {});
    const roleOf = st => st.public ? 'The applicant' : (st.roles || []).length ? (st.roles || []).join(' or ') : 'Assigned people';
    gate.append(status, panel('Who does what', h('ol', { class: 'who-does' }, stages.map((st, i) => h('li', null, h('span', { class: 'n' }, String(i + 1)), h('div', null, h('strong', null, st.title || st.name), st.summary ? h('div', { class: 'muted small' }, st.summary) : null),
      h('span', { class: 'badge ' + (mine.some(r => (st.roles || []).includes(r)) ? 'b-ok' : '') }, roleOf(st) + (i === 0 && !first.public ? ' · starts it' : '')))))));
  }).catch(() => {});
  // A workflow that runs inside a workspace asks where first: the facility decides which providers,
  // nurses, locations and codes every form offers.
  let unit = '';
  if (conf && conf.dataset.wsLevel && token) {
    const level = conf.dataset.wsLevel, label = conf.dataset.wsLabel || level;
    loadWorkspaces(level).then(list => {
      const sel = h('select', { id: 'ws-select', 'aria-label': label }, h('option', { value: '' }, 'Choose a ' + label.toLowerCase() + '…'), list.map(n => h('option', { value: n.id }, n.name)));
      if (list.length === 1) { sel.value = list[0].id; unit = list[0].id; }
      sel.addEventListener('change', () => { unit = sel.value; });
      slot.append(h('div', { class: 'stack', style: 'margin-bottom:12px' }, h('label', { for: 'ws-select' }, label), sel,
        list.length ? null : h('div', { class: 'banner warn' }, 'Your account is not assigned to any ' + label.toLowerCase() + ', so there is nowhere to start. Ask an administrator to place you in one.')));
    }).catch(e => slot.append(h('div', { class: 'banner error' }, e.message)));
  }
  btn.addEventListener('click', async () => {
    if (!token && !canStart) { signIn({ reason: 'Sign in to start.' }); return; }
    if (conf && conf.dataset.wsLevel && conf.dataset.wsRequired === 'true' && !unit) { ui.alert('Choose a ' + (conf.dataset.wsLabel || 'workspace').toLowerCase() + ' first, then start.', 'Choose where to start'); return; }
    btn.disabled = true;
    try {
      const name = btn.dataset.start, v = await api('POST', '/pipelines/' + encodeURIComponent(name) + '/cases', unit ? { data: {}, org_unit: unit } : { data: {} });
      if (v.access_key) saveKey(v.case.id, v.access_key);
      bounce('ok', v.access_key ? 'Started ' + v.case.number + '. It is remembered on this device; keep your case ID ' + v.case.id + ' and access key ' + v.access_key + ' to return from elsewhere.' : 'Started ' + v.case.number + '.', '/cases/' + encodeURIComponent(v.case.id));
    } catch (e) { btn.disabled = false; ui.error(e.message, 'Could not start'); }
  });
}


function forEachInput(fn) {
  for (const g of (S.view.page && S.view.page.groups) || []) for (const f of g.forms || []) for (const inp of f.inputs || []) fn(inp, f, g);
}
function applyView(v) {
  const key = v.case.id + '/' + (v.stage && v.stage.name);
  if (key !== S.key) { S.steps = {}; S.key = key; }
  S.view = v; S.vals = {}; S.reps = {}; S.dirty = new Set(); S.verdicts = {}; S.nodeIn = {}; S.comment = '';
  if (!v.stage) return;
  const seen = {};
  forEachInput((inp, f) => {
    // A form shown in several groups keeps its editable rendering's value.
    if (!seen[inp.path] || inp.editable) { S.vals[inp.path] = inp.value; seen[inp.path] = true; }
    if (f.repeatable && !S.reps[f.name]) S.reps[f.name] = (f.entries || []).map(e => Object.assign({}, e));
    if (inp.verdict) for (const n of v.nodes || []) if ((n.forms || []).includes(f.name)) {
      (S.verdicts[n.name] = S.verdicts[n.name] || {})[inp.path] = { status: inp.verdict.status, comment: inp.verdict.comment || '' };
    }
  });
}

function display(val, inp) {
  if (val == null || val === '' || (Array.isArray(val) && !val.length)) return '—';
  if (typeof val === 'boolean') return val ? 'Yes' : 'No';
  const label = x => { const c = inp && (inp.choices || []).find(o => String(o.value) === String(x)); return c ? (c.label || String(c.value)) : String(x); };
  if (Array.isArray(val)) return val.map(label).join(', ');
  if (typeof val === 'object') return String(val.name || val.id || JSON.stringify(val));
  if (inp && inp.kind === 'datetime') return when(val);
  return label(val);
}
// Like display, but a sealed input never shows a value.
function shown(val, inp) {
  if (inp && inp.sealed) return h('span', { class: 'sealed' }, lockIcon(), 'Sealed until opening');
  return display(val, inp);
}
const calcBadge = inp => inp && inp.computed ? [' ', h('span', { class: 'badge b-info', title: 'Calculated automatically' }, 'calculated')] : null;
function toLocal(iso) {
  const d = new Date(iso); if (!iso || isNaN(d)) return ''; const p = n => String(n).padStart(2, '0');
  return d.getFullYear() + '-' + p(d.getMonth() + 1) + '-' + p(d.getDate()) + 'T' + p(d.getHours()) + ':' + p(d.getMinutes());
}


// ---------- combobox: searchable, clearable, taggable lookups ----------
// A select or multiselect backed by a lookup (or flagged searchable/clearable/taggable, or with many
// choices) is shown as a combobox: type to filter, a clear button, chips for several values, and with
// `taggable` free text becomes a value. A long list asks the server as you type (choices?q=).
const comboWanted = (inp, n) => (inp.kind === 'select' || inp.kind === 'multiselect') && (inp.lookup || inp.searchable || inp.taggable || inp.clearable || n > (inp.kind === 'select' ? 6 : 8));
async function searchChoices(inp, path, q) {
  const v = S.view || S.ov, form = path.split('.')[0], deps = {};
  (inp.depends_on || []).forEach(d => { deps[d] = S.vals[d.includes('.') ? d : form + '.' + d]; });
  const r = await api('POST', '/cases/' + encodeURIComponent(v.case.id) + '/choices/' + encodeURIComponent(form) + '/' + encodeURIComponent(inp.name), { deps, q }, v.case.id);
  return r.choices || [];
}
function combo(inp, path, val, set, choices, base) {
  const multi = inp.kind === 'multiselect', id = base.id, listId = id + '_list';
  let sel = multi ? [].concat(val || []).map(String) : (val == null || val === '' ? [] : [String(val)]);
  let pool = choices.map(c => ({ value: String(c.value), label: c.label || String(c.value) })), query = '', open = false, active = -1, shownList = pool, seq = 0, timer = null;
  const labelOf = v => { const c = pool.concat(shownList).find(o => o.value === v); return c ? c.label : v; };
  const clearable = inp.clearable || !inp.required, remote = pool.length >= 50;
  const root = h('div', { class: 'combo' + (base['aria-invalid'] ? ' invalid' : '') });
  const input = h('input', { type: 'text', id, role: 'combobox', autocomplete: 'off', 'aria-autocomplete': 'list', 'aria-controls': listId, 'aria-expanded': 'false', 'aria-describedby': base['aria-describedby'],
    'aria-required': base['aria-required'], 'aria-invalid': base['aria-invalid'], placeholder: sel.length && multi ? '' : (inp.placeholder || (inp.taggable ? 'Type to search or add…' : 'Search…')) });
  const list = h('ul', { class: 'combo-list', id: listId, role: 'listbox', 'aria-multiselectable': multi ? 'true' : null, hidden: true });
  const commit = () => set(multi ? (sel.length ? sel.slice() : null) : (sel[0] != null ? sel[0] : null));
  const pick = v => { if (v == null || v === '') return; if (multi) { if (!sel.includes(v)) sel.push(v); } else sel = [v]; query = ''; input.value = ''; if (!multi) close(); commit(); draw(); if (multi) { show(); input.focus(); } };
  const unpick = v => { sel = sel.filter(x => x !== v); commit(); draw(); input.focus(); };
  const options = () => {
    const q = query.trim().toLowerCase();
    let out = shownList.filter(o => !q || o.label.toLowerCase().includes(q) || o.value.toLowerCase().includes(q));
    if (multi) out = out.filter(o => !sel.includes(o.value));
    const out2 = out.slice(0, 200).map(o => ({ ...o }));
    const typed = query.trim();
    if (inp.taggable && typed && !shownList.some(o => o.value.toLowerCase() === typed.toLowerCase()) && !sel.includes(typed)) out2.push({ value: typed, label: 'Add “' + typed + '”', tag: true });
    return out2;
  };
  function drawList() {
    const opts = options(); active = Math.min(active, opts.length - 1);
    list.replaceChildren(...(opts.length ? opts.map((o, i) => h('li', { id: id + '_o' + i, role: 'option', class: 'combo-opt' + (i === active ? ' on' : '') + (o.tag ? ' tag' : ''), 'aria-selected': sel.includes(o.value) ? 'true' : 'false',
      onmousedown: e => { e.preventDefault(); pick(o.value); } }, o.label)) : [h('li', { class: 'combo-empty', role: 'presentation' }, query ? 'No match' : 'Nothing to choose from')]));
    input.setAttribute('aria-activedescendant', active >= 0 ? id + '_o' + active : '');
    return opts;
  }
  function show() { open = true; list.hidden = false; input.setAttribute('aria-expanded', 'true'); drawList(); }
  function close() { open = false; list.hidden = true; input.setAttribute('aria-expanded', 'false'); active = -1; }
  function draw() {
    const chips = multi ? sel.map(v => h('span', { class: 'chip' }, labelOf(v), h('button', { type: 'button', class: 'chip-x', 'aria-label': 'Remove ' + labelOf(v), tabindex: '-1', onclick: () => unpick(v) }, '×'))) : [];
    const single = !multi && sel.length ? h('span', { class: 'combo-single', title: labelOf(sel[0]) }, labelOf(sel[0])) : null;
    input.placeholder = sel.length ? (multi ? '' : '') : (inp.placeholder || (inp.taggable ? 'Type to search or add…' : 'Search…'));
    const clear = clearable && sel.length ? h('button', { type: 'button', class: 'combo-clear', 'aria-label': 'Clear ' + (inp.label || inp.name), onclick: () => { sel = []; query = ''; input.value = ''; commit(); draw(); input.focus(); } }, '×') : null;
    root.replaceChildren(h('div', { class: 'combo-box', onclick: () => input.focus() }, ...chips, single, input, clear, h('span', { class: 'combo-caret', 'aria-hidden': 'true' }, '▾')), list);
    if (open) drawList();
  }
  input.addEventListener('focus', () => show());
  input.addEventListener('blur', () => setTimeout(() => { if (document.activeElement === input) return; /* a re-render moved the node and focus came back */ if (inp.taggable && query.trim()) pick(query.trim()); close(); query = ''; input.value = ''; }, 120));
  input.addEventListener('input', () => {
    query = input.value; active = -1; if (!open) show(); else drawList();
    if (remote || (query && pool.length >= 50)) { clearTimeout(timer); const my = ++seq; timer = setTimeout(async () => { try { const r = await searchChoices(inp, path, query.trim()); if (my === seq) { shownList = r.map(c => ({ value: String(c.value), label: c.label || String(c.value) })); drawList(); } } catch (e) { /* keep the local list */ } }, 250); }
  });
  input.addEventListener('keydown', e => {
    const opts = open ? options() : [];
    if (e.key === 'ArrowDown') { e.preventDefault(); if (!open) show(); else { active = (active + 1) % Math.max(1, opts.length); drawList(); } }
    else if (e.key === 'ArrowUp') { e.preventDefault(); if (open) { active = (active - 1 + opts.length) % Math.max(1, opts.length); drawList(); } }
    else if (e.key === 'Enter') { if (open && active >= 0 && opts[active]) { e.preventDefault(); pick(opts[active].value); } else if (inp.taggable && query.trim()) { e.preventDefault(); pick(query.trim()); } }
    else if (e.key === ',' && inp.taggable && query.trim()) { e.preventDefault(); pick(query.trim()); }
    else if (e.key === 'Escape') { if (open) { e.preventDefault(); close(); } }
    else if (e.key === 'Backspace' && !input.value && sel.length) { if (multi) { sel.pop(); commit(); draw(); } else if (clearable) { sel = []; commit(); draw(); } }
  });
  draw();
  return root;
}

function control(inp, path, val, set) {
  const id = safeId(path), desc = id + '_d';
  const base = { id, name: path, 'aria-describedby': desc, 'aria-required': inp.required ? 'true' : null, placeholder: inp.placeholder || null,
    'aria-invalid': S.errs[path] ? 'true' : null };
  const choices = inp.choices && inp.choices.length ? inp.choices : (inp.options || []).map(o => ({ value: o, label: o }));
  const str = val == null ? '' : String(val);
  switch (inp.kind) {
    case 'textarea': return h('textarea', { ...base, rows: 3, maxlength: inp.max_length || null, value: str, oninput: e => set(e.target.value) });
    case 'boolean': return h('input', { ...base, type: 'checkbox', checked: val === true, onchange: e => set(e.target.checked ? true : (inp.required ? null : false)) });
    case 'number': case 'integer': return h('input', { ...base, type: 'number', step: inp.kind === 'integer' ? '1' : 'any', min: inp.min || null, max: inp.max || null,
      value: str, oninput: e => set(e.target.value === '' ? null : Number(e.target.value)) });
    case 'date': return h('input', { ...base, type: 'date', min: inp.min || null, max: inp.max || null, value: str.slice(0, 10), onchange: e => set(e.target.value || null) });
    case 'datetime': return h('input', { ...base, type: 'datetime-local', value: toLocal(val), onchange: e => set(e.target.value ? new Date(e.target.value).toISOString() : null) });
    case 'select': if (comboWanted(inp, choices.length)) return combo(inp, path, val, set, choices, base);
      return h('select', { ...base, placeholder: null, onchange: e => set(e.target.value || null) }, h('option', { value: '' }, inp.placeholder || 'Choose…'),
      choices.map(c => { const o = h('option', { value: String(c.value) }, c.label || String(c.value)); o.selected = String(c.value) === str; return o; }));
    case 'radio': case 'multiselect': {
      if (inp.kind === 'multiselect' && comboWanted(inp, choices.length)) return combo(inp, path, val, set, choices, base);
      const multi = inp.kind === 'multiselect', cur = multi ? [].concat(val || []).map(String) : [];
      return h('div', { class: 'opts', id, role: multi ? 'group' : 'radiogroup', 'aria-labelledby': id + '_l', 'aria-describedby': desc },
        choices.map(c => h('label', { class: 'check' }, h('input', {
          type: multi ? 'checkbox' : 'radio', name: path, value: String(c.value), checked: multi ? cur.includes(String(c.value)) : String(c.value) === str,
          onchange: e => {
            if (!multi) return set(e.target.value);
            const s = new Set(cur); if (e.target.checked) s.add(e.target.value); else s.delete(e.target.value);
            cur.splice(0, cur.length, ...s); set(cur.length ? cur.slice() : null);
          }
        }), c.label || String(c.value))));
    }
    case 'file': return h('input', { ...base, type: 'text', value: val && typeof val === 'object' ? String(val.id || '') : str,
      placeholder: inp.placeholder || 'upload://file-reference', oninput: e => set(e.target.value) });
    default: return h('input', { ...base, type: { email: 'email', phone: 'tel' }[inp.kind] || 'text', maxlength: inp.max_length || null,
      value: str, oninput: e => set(e.target.value) });
  }
}

function fieldEl(inp, path, val, editable, set, lock) {
  const id = safeId(path), errs = S.errs[path];
  if (inp.sealed || inp.computed) editable = false;
  const labelKids = [inp.label, inp.required && editable ? h('span', { class: 'req', title: 'required' }, ' *') : null];
  const extras = [
    inp.verdict ? h('div', { class: 'note' }, 'Review: ', badge(inp.verdict.status), inp.verdict.comment ? ' ' + inp.verdict.comment : '') : null,
    inp.flag ? h('div', { class: 'flag', role: 'note' }, h('strong', null, 'Correction requested: '), inp.flag.comment || 'please check this field') : null,
    errList(errs)];
  const help = h('div', { class: 'help', id: id + '_d' }, inp.help || '',
    editable && inp.kind === 'file' ? (inp.help ? ' ' : '') + 'Enter the reference of an uploaded file' + (inp.accept && inp.accept.length ? ' (' + inp.accept.join(', ') + ')' : '') + '.' : '');
  const span = parseInt(inp.span, 10);
  const style = span > 1 ? 'grid-column:span ' + span : null;
  const cls = 'field' + (['textarea', 'radio', 'multiselect', 'file'].includes(inp.kind) ? ' wide' : '') + (inp.flag ? ' flagged' : '') + (errs ? ' invalid' : '');
  if (!editable) return h('div', { class: cls, style, 'data-path': path }, h('div', { class: 'lbl' }, inp.label, calcBadge(inp), lock && !inp.computed && !inp.sealed ? h('span', { class: 'badge lock', title: inp.disabled ? 'Locked right now by a rule' : 'You cannot change this at this stage' }, inp.disabled ? 'locked' : 'read-only') : null),
    h('div', { class: 'value' }, shown(val, inp), inp.masked && !inp.sealed ? h('span', { class: 'muted' }, ' (masked)') : null), inp.help ? help : null, extras);
  const ctl = control(inp, path, val, set);
  if (inp.kind === 'boolean') return h('div', { class: cls, style, 'data-path': path }, h('label', { class: 'check', for: id }, ctl, labelKids), help, extras);
  const lbl = ['radio', 'multiselect'].includes(inp.kind) && !comboWanted(inp, (inp.choices || (inp.options || [])).length) ? h('div', { class: 'lbl', id: id + '_l' }, labelKids) : h('label', { for: id }, labelKids);
  return h('div', { class: cls, style, 'data-path': path }, lbl, ctl, help, extras);
}

// A lookup that depends on other inputs (providers by specialty, CPT codes by specialty) is
// refreshed from the server whenever one of those inputs changes.
function depChanged(path) {
  forEachInput((inp, f) => {
    if (!inp.depends_on || !inp.depends_on.length || !inp.editable) return;
    const deps = inp.depends_on.map(d => d.includes('.') ? d : f.name + '.' + d);
    if (deps.includes(path)) refreshChoices(inp, f);
  });
}
async function refreshChoices(inp, f) {
  const v = S.view, body = { deps: {} };
  inp.depends_on.forEach(d => { body.deps[d] = S.vals[d.includes('.') ? d : f.name + '.' + d]; });
  try {
    const r = await api('POST', '/cases/' + encodeURIComponent(v.case.id) + '/choices/' + encodeURIComponent(f.name) + '/' + encodeURIComponent(inp.name), body, v.case.id);
    inp.choices = r.choices || [];
    const cur = S.vals[inp.path], ok = x => inp.choices.some(c => String(c.value) === String(x));
    if (Array.isArray(cur) ? cur.some(x => !ok(x)) : cur != null && cur !== '' && !ok(cur)) { S.vals[inp.path] = Array.isArray(cur) ? cur.filter(ok) : null; S.dirty.add(inp.path); }
    renderTask();
  } catch (e) { /* keep the options that are on screen */ }
}

function formEl(f, g) {
  const fixed = parseInt(f.columns || g.columns || 0, 10) || 0, cols = Math.max(1, fixed), editableGroup = g.mode === 'editable';
  const gridProps = fixed ? { class: 'grid fixed', style: '--cols:' + cols } : { class: 'grid' };
  const head = [f.title ? h('h3', null, f.title) : null, f.description ? h('p', { class: 'muted' }, f.description) : null];
  if (g.mode === 'summary') {
    const dl = get => h('dl', { class: 'summary' }, f.inputs.map(inp => [h('dt', null, inp.label), h('dd', null, shown(get(inp), inp), calcBadge(inp))]));
    if (f.repeatable) return h('div', { class: 'form' }, head, (S.reps[f.name] || []).map(e => h('div', { class: 'entry' }, dl(inp => e[inp.name]))));
    return h('div', { class: 'form' }, head, dl(inp => S.vals[inp.path]));
  }
  if (!f.repeatable) return h('div', { class: 'form' }, head, h('div', gridProps,
    f.inputs.map(inp => fieldEl(inp, inp.path, S.vals[inp.path], editableGroup && inp.editable, v => { S.vals[inp.path] = v; S.dirty.add(inp.path); depChanged(inp.path); }, editableGroup && !inp.editable))));
  const list = S.reps[f.name] = S.reps[f.name] || [];
  const canEdit = editableGroup && f.inputs.some(i => i.editable);
  const min = f.min_items || 0, max = f.max_items || Infinity;
  const noun = (f.title || 'entry').toLowerCase();
  return h('div', { class: 'form', 'data-path': f.name }, head, errList(S.errs[f.name]),
    list.length ? null : h('p', { class: 'muted' }, 'No entries yet.'),
    list.map((entry, i) => h('div', { class: 'entry', role: 'group', 'aria-label': (f.title || f.name) + ' ' + (i + 1) },
      h('div', { class: 'row' }, h('strong', null, '#' + (i + 1)), canEdit && list.length > min ? h('button', { type: 'button', class: 'danger', style: 'margin-left:auto',
        'aria-label': 'Remove ' + noun + ' ' + (i + 1), onclick: () => { list.splice(i, 1); S.dirty.add(f.name); renderTask(); } }, 'Remove') : null),
      errList(S.errs[f.name + '.' + i]),
      h('div', gridProps,
        f.inputs.map(inp => fieldEl(inp, f.name + '.' + i + '.' + inp.name, entry[inp.name], canEdit && inp.editable, v => { entry[inp.name] = v; S.dirty.add(f.name); }))))),
    canEdit && list.length < max ? h('button', { type: 'button', onclick: () => { list.push({}); S.dirty.add(f.name); renderTask(); } }, '+ Add ' + noun) : null,
    canEdit && (min || max < Infinity) ? h('div', { class: 'note' }, [min ? 'At least ' + min : '', max < Infinity ? 'at most ' + max : ''].filter(Boolean).join(', ') + ' entries.') : null);
}

// Arranges items ({title, el, forms, flagged, collapsed}) in a layout; used for a page's groups and a group's forms.
function arrange(layout, key, items, footer) {
  const issues = it => it.flagged || Object.keys(S.errs).some(p => it.forms.includes(p.split('.')[0]));
  const mark = it => issues(it) ? h('span', { class: 'dot', title: 'Needs attention' }, ' •', h('span', { class: 'sr' }, ' needs attention')) : null;
  const cur = Math.max(0, Math.min(S.steps[key] || 0, items.length - 1));
  const go = (i, focusTab) => {
    S.steps[key] = i; renderTask();
    const t = document.getElementById(focusTab ? 'tab_' + key + '_' + i : 'ar_' + key);
    if (t) { if (!focusTab) t.scrollIntoView({ block: 'start' }); t.focus({ preventScroll: !focusTab ? true : false }); }
  };
  if (layout === 'wizard') {
    const last = cur === items.length - 1;
    return h('div', null,
      h('ol', { class: 'steps', 'aria-label': 'Steps' }, items.map((it, i) => h('li', { 'aria-current': i === cur ? 'step' : null, class: i < cur ? 'done' : '' },
        (i + 1) + '. ' + it.title, mark(it)))),
      h('section', { id: 'ar_' + key, tabindex: '-1', 'aria-label': 'Step ' + (cur + 1) + ' of ' + items.length },
        h('h3', { style: 'font-size:1.05rem' }, items[cur].title), items[cur].el),
      h('div', { class: 'bar' }, h('div', { class: 'row' },
        h('button', { type: 'button', disabled: cur === 0, onclick: () => go(cur - 1) }, 'Back'),
        last ? null : h('button', { type: 'button', class: 'primary', onclick: () => go(cur + 1) }, 'Next')),
        h('span', { class: 'muted' }, 'Step ' + (cur + 1) + ' of ' + items.length)),
      last ? footer : null);
  }
  let body;
  if (layout === 'tabbed') {
    const tabs = h('div', { class: 'tabs', role: 'tablist' }, items.map((it, i) => h('button', { type: 'button', role: 'tab', id: 'tab_' + key + '_' + i,
      'aria-selected': i === cur ? 'true' : 'false', 'aria-controls': 'ar_' + key, tabindex: i === cur ? '0' : '-1', onclick: () => go(i, true),
      onkeydown: e => { const d = { ArrowRight: 1, ArrowLeft: -1 }[e.key]; if (d) { e.preventDefault(); go((cur + d + items.length) % items.length, true); } } },
      it.title, mark(it))));
    body = h('div', null, tabs, h('div', { role: 'tabpanel', id: 'ar_' + key, tabindex: '-1', 'aria-labelledby': 'tab_' + key + '_' + cur }, items[cur].el));
  } else if (layout === 'accordion') {
    body = h('div', null, items.map((it, i) => {
      const k = key + '_' + i, open = k in S.steps ? S.steps[k] : !it.collapsed;
      return h('details', { class: 'acc', open, ontoggle: e => { S.steps[k] = e.target.open; } }, h('summary', null, it.title, mark(it)), h('div', null, it.el));
    }));
  } else {
    body = h('div', { class: 'stack' }, items.map(it => h('section', null, it.title ? h('h3', { style: 'font-size:1.05rem' }, it.title, mark(it)) : null, it.el)));
  }
  return h('div', null, body, footer);
}

function groupEl(g) {
  const desc = g.description ? h('p', { class: 'muted' }, g.description) : null;
  if (['tabbed', 'accordion', 'wizard'].includes(g.layout) && g.forms.length > 1) {
    const items = g.forms.map(f => ({ title: f.title || f.name, forms: [f.name], flagged: f.inputs.some(i => i.flag), el: formEl({ ...f, title: '' }, g) }));
    return h('div', null, desc, arrange(g.layout, 'g_' + g.name, items));
  }
  return h('div', null, desc, g.forms.map(f => formEl(f, g)));
}

// ---------- collecting & mutating ----------
function collect() {
  const data = {}; let any = false;
  forEachInput((inp, f, g) => {
    if (g.mode !== 'editable' || !inp.editable || inp.computed || inp.sealed) return;
    any = true;
    if (f.repeatable) {
      data[f.name] = (S.reps[f.name] || []).map(e => { const o = {}; for (const i of f.inputs) if (i.editable && !i.computed && !i.sealed && e[i.name] !== undefined) o[i.name] = e[i.name]; return o; });
      return;
    }
    if (inp.masked && !S.dirty.has(inp.path)) return; // never send a masked placeholder back
    const v = S.vals[inp.path];
    (data[f.name] = data[f.name] || {})[inp.name] = v === undefined ? null : v;
  });
  return any ? data : null;
}

function failed(e, prefix) {
  for (const d of e.details) (S.errs[prefix + d.path] = S.errs[prefix + d.path] || []).push(d.message);
  // Problems with fields are shown at the fields, not repeated in a list at the top; only what no field
  // can show (a stale version, a refusal, a missing comment) becomes a banner.
  const onField = d => d.path && d.path !== 'comment';
  if (e.status !== 409 && e.details.length && e.details.every(onField)) { S.notice = null; S.focusError = true; return; }
  S.notice = { kind: 'error', text: e.status === 409 ? e.message + ' Reload the page to get the latest version.' : e.message,
    list: e.details.filter(d => !onField(d)).map(d => d.message) };
}
// Move to the first field with a problem so it can be fixed without hunting.
function focusFirstError() {
  if (!S.focusError) return; S.focusError = false;
  const el = document.querySelector('.body [aria-invalid="true"]'); if (!el) return;
  el.scrollIntoView({ block: 'center', behavior: 'smooth' }); try { el.focus({ preventScroll: true }); } catch (e) { /* not focusable */ }
}
// after(v) runs on success, before the re-render.
async function mutate(method, path, body, prefix, okText, after) {
  if (S.busy) return;
  S.busy = true; S.errs = {}; S.notice = { kind: 'warn', text: 'Working…' }; renderTask();
  const id = S.view.case.id;
  try {
    const v = await api(method, '/cases/' + encodeURIComponent(id) + path, body, id);
    applyView(v); S.history = null;
    S.notice = { kind: 'ok', text: okText + (v.stage ? '' : ' The case moved to stage "' + v.case.stage + '", which you may not view.') };
    if (after) after(v);
  } catch (e) { failed(e, prefix); }
  S.busy = false; renderTask(); focusFirstError();
}
const stagePath = () => '/stages/' + encodeURIComponent(S.view.stage.name);


// Info blocks shown above a group (before) or, with no group, at the top of the page.
function infoEl(before) {
  const pg = S.view.page || {};
  return (pg.info || []).filter(b => (b.before || '') === before).map(b =>
    h('div', { class: 'banner ' + ({ warning: 'warn', danger: 'error', success: 'ok' }[b.style] || 'info'), role: 'note' },
      b.title ? h('strong', null, b.title + ' ') : null, b.body));
}

// Acknowledgements the submitter must accept; each is sent back with the hash of the wording shown.
function acksEl() {
  const list = (S.view.page || {}).acknowledgements || [];
  return list.map(a => h('div', { class: 'field', 'data-path': 'acknowledgements.' + a.name },
    h('label', null, h('input', { type: 'checkbox', checked: !!S.acks[a.name], onchange: e => { S.acks[a.name] = e.target.checked; } }),
      ' ', a.title ? h('strong', null, a.title + ': ') : null, a.text),
    a.accepted ? h('div', { class: 'note' }, 'Accepted ' + when(a.accepted.at) + ' by ' + a.accepted.by) : null,
    errList(S.errs['acknowledgements.' + a.name])));
}
function ackBody() {
  const out = {};
  for (const a of (S.view.page || {}).acknowledgements || []) if (S.acks[a.name]) out[a.name] = a.sha256;
  return out;
}


// ---------- nodes ----------
function reviewInputs(n) {
  const out = [], seen = new Set();
  // Computed inputs are derived, not submitted: the server never asks for a verdict on them.
  const blank = v => v == null || (typeof v === 'string' && !v.trim()) || (Array.isArray(v) && !v.length) || (typeof v === 'object' && !Array.isArray(v) && !Object.keys(v).length);
  // The server only reviews fields that have a value.
  forEachInput((inp, f) => { if ((n.forms || []).includes(f.name) && !inp.computed && !seen.has(inp.path) && (f.repeatable || !blank(S.vals[inp.path]))) { seen.add(inp.path); out.push(inp); } });
  return out;
}
function nodeEl(n) {
  const ops = n.operations || [], st = S.nodeIn[n.name] = S.nodeIn[n.name] || { comment: '', result: '' };
  const pre = 'node:' + n.name + ':', vd = S.verdicts[n.name] = S.verdicts[n.name] || {};
  const rows = n.kind === 'review' && ops.includes('verify') ? reviewInputs(n) : [];
  const send = (verb, extra) => {
    const body = Object.assign({}, extra); if (st.comment.trim()) body.comment = st.comment.trim();
    if (verb === 'complete' && st.result.trim()) {
      try { body.result = JSON.parse(st.result); if (!body.result || typeof body.result !== 'object' || Array.isArray(body.result)) throw 0; }
      catch (e) { S.errs = { [pre + 'result']: ['Result must be a JSON object.'] }; renderTask(); return; }
    }
    mutate('POST', stagePath() + '/nodes/' + encodeURIComponent(n.name) + '/' + encodeURIComponent(verb), body, pre, (n.title || n.name) + ': ' + verb + ' done.');
  };
  const onOp = op => {
    if (op === 'vote:approve' || op === 'vote:reject') {
      const decision = op.slice(5);
      if (decision === 'reject' && !st.comment.trim()) {
        S.errs = { [pre + 'comment']: ['Say why you vote to reject.'] }; renderTask();
        const c = document.getElementById(safeId(pre + 'c')); if (c) c.focus(); return;
      }
      return send('vote', { result: { decision } });
    }
    if (op === 'verify') {
      const verdicts = {};
      for (const [p, x] of Object.entries(vd)) if (x.status) verdicts[p] = x.status === 'flagged' ? { status: 'flagged', comment: x.comment || '' } : { status: 'verified' };
      return send('verify', { verdicts });
    }
    if (op === 'verify_all') {
      const verdicts = {}; rows.forEach(r => { verdicts[r.path] = { status: 'verified' }; });
      return mutate('POST', stagePath() + '/nodes/' + encodeURIComponent(n.name) + '/verify', { verdicts }, pre, 'Fields verified.',
        () => setTimeout(() => { if (S.view.nodes.find(x => x.name === n.name && (x.operations || []).includes('complete'))) send('complete'); }, 0));
    }
    if (['waive', 'reject', 'fail'].includes(op)) { ui.confirm({ title: cap(op) + ' this step?', message: cap(op) + ' "' + (n.title || n.name) + '"? This is recorded in the case history.', confirmLabel: cap(op), danger: true }).then(ok => { if (ok) send(op); }); return; }
    send(op);
  };
  const otherErrs = Object.keys(S.errs).filter(k => k.startsWith(pre) && !['comment', 'result'].includes(k.slice(pre.length)) && !rows.some(r => r.path === k.slice(pre.length)));
  return h('div', { class: 'node' },
    h('div', { class: 'node-head' }, h('h3', null, n.title || n.name), h('span', { class: 'badge' }, n.kind), n.optional ? h('span', { class: 'badge' }, 'optional') : null, badge(n.status)),
    n.description ? h('p', { class: 'muted' }, n.description) : null,
    n.kind === 'approval' ? h('p', { class: 'muted' }, 'Approvals: ' + (n.approvals || []).length + ' of ' + (n.required_approvals || 1)) : null,
    n.kind === 'vote' ? tallyEl(n) : null,
    (n.approvals || []).map(a => h('div', { class: 'note' }, a.decision ? ['Voted ', badge(a.decision), ' by ' + a.by] : 'Approved by ' + a.by,
      ' · ' + when(a.at) + (a.comment ? ' · ' + a.comment : ''))),
    n.comment ? h('p', null, h('strong', null, 'Comment: '), n.comment) : null,
    n.kind === 'liveness' ? livenessEl(n, ops) : null,
    n.result != null && n.kind !== 'vote' && n.kind !== 'liveness' ? h('pre', null, JSON.stringify(n.result, null, 2)) : null,
    rows.length ? h('div', { role: 'group', 'aria-label': 'Field verdicts for ' + (n.title || n.name) }, rows.map(inp => {
      const x = vd[inp.path] = vd[inp.path] || { status: '', comment: '' }, gid = safeId(pre + inp.path);
      return h('div', { class: 'rv' },
        h('div', null, h('div', { id: gid + '_l', style: 'font-weight:600' }, inp.label), h('div', { class: 'muted' }, shown(S.vals[inp.path], inp))),
        h('div', { class: 'seg', role: 'radiogroup', 'aria-labelledby': gid + '_l' }, [['', 'Unset'], ['verified', 'Verified'], ['flagged', 'Flag']].map(([val, l]) =>
          h('label', null, h('input', { type: 'radio', name: gid, value: val, checked: x.status === val, onchange: () => { x.status = val; renderTask(); } }), l))),
        x.status === 'flagged' ? h('div', { class: 'rv-c' }, h('label', { class: 'sr', for: gid + '_c' }, 'What is wrong with ' + inp.label),
          h('input', { type: 'text', id: gid + '_c', placeholder: 'What is wrong? (shown to the applicant)', value: x.comment, oninput: e => { x.comment = e.target.value; } })) : null,
        S.errs[pre + inp.path] ? h('div', { class: 'rv-c' }, errList(S.errs[pre + inp.path])) : null);
    })) : null,
    ops.length ? h('div', { class: 'stack', style: 'margin-top:10px' },
      h('div', { class: 'field' }, h('label', { for: safeId(pre + 'c') }, 'Comment', h('span', { class: 'muted' }, ops.includes('reject') || ops.includes('vote') ? ' (required to reject)' : ' (optional)')),
        h('input', { type: 'text', id: safeId(pre + 'c'), value: st.comment, oninput: e => { st.comment = e.target.value; } }), errList(S.errs[pre + 'comment'])),
      ops.includes('complete') && ['task', 'form'].includes(n.kind) ? h('div', { class: 'field' }, h('label', { for: safeId(pre + 'r') }, 'Result (optional JSON object)'),
        h('textarea', { id: safeId(pre + 'r'), rows: 2, placeholder: '{"station": "K-2"}', value: st.result, oninput: e => { st.result = e.target.value; } }), errList(S.errs[pre + 'result'])) : null,
      otherErrs.map(k => errList(S.errs[k])),
      h('div', { class: 'row' }, (rows.length ? ['verify_all'] : []).concat(ops).filter(op => n.kind !== 'liveness').flatMap(op => op === 'vote' ? ['vote:approve', 'vote:reject'] : [op]).map(op => h('button', { type: 'button', disabled: S.busy, onclick: () => onOp(op),
        class: ['reject', 'fail', 'vote:reject'].includes(op) ? 'danger' : (['approve', 'complete', 'issue', 'run', 'vote:approve', 'verify_all'].includes(op) ? 'primary' : '') },
        op === 'verify_all' ? 'Verify all and complete' : op === 'verify' ? 'Save verdicts' : op.startsWith('vote:') ? 'Vote to ' + op.slice(5) : cap(op))))) : null);
}
// A liveness node: the browser captures webcam frames while the server's
// challenge is shown; the server scores them. Nothing is decided here.
const GESTURES = { blink: 'Blink', turn_left: 'Turn your head left', turn_right: 'Turn your head right', smile: 'Smile', nod: 'Nod', open_mouth: 'Open your mouth' };
function livenessEl(n, ops) {
  const r = n.result && typeof n.result === 'object' ? n.result : {};
  const video = h('video', { playsinline: true, muted: true, width: 320, height: 240, style: 'background:#000;border-radius:8px;max-width:100%', 'aria-label': 'Camera preview' });
  const msg = h('p', { class: 'muted', role: 'status', 'aria-live': 'polite', id: 'live-msg' });
  const mod = h('div', { class: 'stack', 'data-liveness': n.name });
  if (n.status === 'passed') return h('div', { class: 'banner ok' }, 'Liveness verified' + (r.score != null ? ' (score ' + Number(r.score).toFixed(2) + ')' : '') + '.');
  if (n.status === 'failed') return h('div', { class: 'banner error' }, 'Liveness could not be verified. An officer must review this case.');
  if (r.reasons && r.reasons.length) mod.append(h('div', { class: 'banner warn', role: 'alert' }, 'Not verified: ' + r.reasons.join('; ') + (r.attempts_left != null ? ' (' + r.attempts_left + ' attempts left)' : '')));
  if (!ops.includes('challenge')) return mod;
  const btn = h('button', { class: 'primary', type: 'button', id: 'live-start', onclick: async () => {
    btn.disabled = true; S.busy = true;
    try {
      const m = await import('/static/js/liveness-capture.js');
      const out = await m.captureLiveness({ video, node: n.name, headers: Object.assign({ Accept: 'application/json' }, token ? { Authorization: 'Bearer ' + token } : {}, keys[S.view.case.id] ? { 'X-Access-Key': keys[S.view.case.id] } : {}),
        url: verb => API + '/cases/' + encodeURIComponent(S.view.case.id) + stagePath() + '/nodes/' + encodeURIComponent(n.name) + '/' + verb,
        onPrompt: (a, i, c) => { msg.textContent = 'Step ' + (i + 1) + ' of ' + c + ': ' + (GESTURES[a] || a); } });
      applyView(out.response); S.history = null;
      S.notice = { kind: out.node.status === 'passed' ? 'ok' : 'warn', text: out.node.status === 'passed' ? 'Liveness verified.' : 'The check did not pass. You can try again.' };
    } catch (e) { S.notice = { kind: 'error', text: e.message }; }
    S.busy = false; renderTask();
  } }, 'Start camera check');
  mod.append(video, msg, h('p', { class: 'muted' }, 'Your camera is used only for this check; frames are sent to the server and not kept in this page.'), btn);
  return mod;
}
// A vote node's tally: {approve, reject, voters, policy}.
function tallyEl(n) {
  const r = n.result && typeof n.result === 'object' ? n.result : null;
  if (!r) return (n.approvals || []).length ? null : h('p', { class: 'muted' }, 'No votes yet.');
  return h('p', { class: 'row' }, 'Votes:', h('span', { class: 'badge b-ok' }, 'approve ' + (r.approve || 0)), h('span', { class: 'badge b-bad' }, 'reject ' + (r.reject || 0)),
    h('span', { class: 'muted' }, [r.voters != null ? r.voters + ' voter' + (r.voters === 1 ? '' : 's') + ' needed' : '', r.policy ? 'policy: ' + String(r.policy) : ''].filter(Boolean).join(' · ')));
}

// ---------- work: assignment, SLA, holds, routing, links ----------
const WORK_OPS = ['claim', 'release', 'assign', 'delegate', 'suspend', 'resume', 'link'];
const WORK_FORMS = ['assign', 'delegate', 'suspend', 'link'];
const WORK_LABEL = { claim: 'Claim', release: 'Release', assign: 'Assign…', delegate: 'Delegate…', suspend: 'Put on hold…', resume: 'Resume', link: 'External link…' };
const TTLS = [['24h', '1 day'], ['72h', '3 days'], ['168h', '7 days'], ['336h', '14 days']];


// ---------- formatting ----------
function fmtDur(sec) {
  sec = Math.max(0, Math.round(sec || 0)); if (sec < 60) return sec + 's';
  const m = Math.round(sec / 60); if (m < 60) return m + 'm';
  const hh = Math.floor(m / 60), mm = m % 60; if (hh < 24) return hh + 'h' + (mm ? ' ' + mm + 'm' : '');
  const d = Math.floor(hh / 24); return d + 'd' + (hh % 24 ? ' ' + (hh % 24) + 'h' : '');
}
const short = t => { if (!t) return '—'; const d = new Date(t); return isNaN(d) ? '—' : d.toLocaleString([], { day: 'numeric', month: 'short', hour: '2-digit', minute: '2-digit' }); };
const FINISHED = ['approved', 'rejected', 'completed', 'withdrawn'];
// People appear by name when the directory knows them (the roster comes with a signed-in caller's workflow description).
const roster = {};
const person = id => !id ? '—' : roster[id] ? roster[id] : String(id).replace(/^demo-/, '').replace(/^anon_.*/, 'applicant');
async function loadRoster() {
  if (!token) return;
  try {
    const cat = await api('GET', '/pipelines') || [];
    if (!cat.length) return;
    const d = await api('GET', '/pipelines/' + encodeURIComponent(cat[0].name));
    (d.workers || []).forEach(w => { if (w.id && w.name) roster[w.id] = w.name; });
  } catch (e) { /* names are a nicety */ }
}
const stageStatus = s => String(s || '').replace(/_/g, ' ');
const valText = f => f.value == null ? '—' : typeof f.value === 'boolean' ? (f.value ? 'Yes' : 'No') : Array.isArray(f.value) ? f.value.map(x => typeof x === 'object' ? Object.values(x).join(' · ') : x).join('; ') : typeof f.value === 'object' ? (f.value.name || JSON.stringify(f.value)) : String(f.value);

// A deadline meter: how much of the time allowed has gone.
function slaMeter(w, stage) {
  const sla = w && w.sla; if (!sla || !stage.due_at) return null;
  // On hold the clock is stopped: show what was left when it was put on hold, and do not tick.
  // (a hold, or a question waiting for its answer, stops the clock)
  const info = (S.ov && S.ov.information) || (S.view && S.view.information), pausedAt = (w.suspended && w.suspended.at) || (sla.status === 'paused' && info && info.paused_at);
  const held = pausedAt ? new Date(pausedAt).getTime() : 0;
  const start = new Date(sla.started_at).getTime(), due = new Date(sla.due_at).getTime(), now = held || Date.now();
  const pct = Math.max(2, Math.min(100, Math.round(((now - start) / Math.max(1, due - start)) * 100)));
  const late = now > due, label = sla.status === 'breached' || late ? 'Overdue by ' + fmtDur((now - due) / 1000) : fmtDur((due - now) / 1000) + ' left' + (held ? (w.suspended ? ' · on hold' : ' · waiting for an answer') : '');
  return h('div', { class: 'sla ' + (late ? 'breached' : sla.status), 'data-sla-start': String(start), 'data-sla-due': String(due), 'data-sla-status': sla.status, 'data-sla-held': held ? '1' : null }, h('div', { class: 'track' }, h('div', { class: 'fill', style: 'width:' + pct + '%' })),
    h('div', { class: 'meta' }, h('span', { class: 'sla-label' }, label), h('span', null, 'Due ' + short(sla.due_at))));
}
const slaBadge = (w, stage) => { const s = w && w.sla; if (!s) return null; const b = s.status === 'breached' ? badge('failed', 'SLA missed') : s.status === 'warning' ? badge('warning', 'Due soon') : badge('on_track', 'On track'); if (s.due_at && s.status !== 'breached') b.setAttribute('data-sla-badge-due', String(new Date(s.due_at).getTime())); return b; };

// ---------- layout helpers ----------
// A panel: a labelled strip over flat content.
const panel = (title, body, o = {}) => h('section', { class: 'panel' + (o.cls ? ' ' + o.cls : '') + (o.flush ? ' flush' : '') },
  title || o.actions ? h('div', { class: 'ph' }, h('h2', null, title || ''), o.actions || null) : null, h('div', { class: 'pb' }, body));
// A screen: page bar (crumb, title, number, actions), optional tabs, then the body.
function screen(o, ...body) {
  const bar = h('div', { class: 'pagebar' }, h('div', { class: 'titles' }, o.crumb ? h('a', { class: 'crumb', href: o.crumb[0] }, o.crumb[1]) : null,
    h('h1', null, o.title, o.num ? h('span', { class: 'num' }, o.num) : null), o.sub ? h('div', { class: 'sub' }, o.sub) : null), o.actions ? h('div', { class: 'actions' }, o.actions) : null);
  page(bar, o.tabs || null, h('div', { class: 'body' }, notice(), o.after || null, body));
}
const loading = () => page(h('div', { class: 'body' }, h('p', { class: 'muted' }, 'Loading…')));
const flowBar = (ov, cid, done, st) => h('nav', { class: 'flow', 'aria-label': 'Stages' }, (ov.progress || []).map((p, i) => h('a', { class: done && p.status === 'pending' ? 'skipped' : p.status, href: p.name === st.name && ov.can_work ? '/tasks/' + cid : '/cases/' + cid, 'aria-current': p.name === st.name ? 'step' : null },
  h('span', { class: 'n' }, String(i + 1).padStart(2, '0')), h('strong', null, p.title || p.name), h('small', null, done && p.status === 'pending' ? 'skipped' : stageStatus(p.status)))));

// ---------- workflow look: theme and layout come from the workflow's ui block ----------
// Applies the workflow's brand colour, density and brand name; a workflow with no ui gets the basic look.
function applyUI(ui) {
  const r = document.documentElement, props = ['--accent', '--accent-ink', '--accent-2', '--accent-tint'];
  const a = ui && /^#([0-9a-f]{3}|[0-9a-f]{6})$/i.test(ui.accent || '') ? ui.accent : '';
  if (a) {
    const hex = a.length === 4 ? '#' + [...a.slice(1)].map(c => c + c).join('') : a, n = parseInt(hex.slice(1), 16);
    const lum = (0.299 * (n >> 16) + 0.587 * ((n >> 8) & 255) + 0.114 * (n & 255)) / 255, dark = window.matchMedia && matchMedia('(prefers-color-scheme: dark)').matches;
    const base = dark ? 'color-mix(in srgb, ' + hex + ' 62%, white)' : hex;
    r.style.setProperty('--accent', base);
    r.style.setProperty('--accent-ink', (dark ? lum > 0.35 : lum > 0.6) ? '#0b1117' : '#ffffff');
    r.style.setProperty('--accent-2', 'color-mix(in srgb, ' + base + ' 80%, ' + (dark ? 'white' : 'black') + ')');
    r.style.setProperty('--accent-tint', 'color-mix(in srgb, ' + base + ' 16%, var(--panel))');
  } else props.forEach(p => r.style.removeProperty(p));
  r.classList.toggle('compact', !!(ui && ui.density === 'compact'));
  const nm = document.querySelector('.brand .name'); if (nm) { if (!nm.dataset.def) nm.dataset.def = nm.textContent; nm.textContent = (ui && ui.brand) || nm.dataset.def; }
}
// A CSS grid track for a configured width ("1fr" shares the rest).
const track = w => !w ? 'minmax(0,1fr)' : /fr$/.test(w) ? 'minmax(0,' + w + ')' : w;
function retitle(el, title) { const h2 = el.querySelector && el.querySelector('.ph h2'); if (h2) h2.textContent = title; return el; }
// A panel the workflow collapses: the strip becomes the summary of a details element.
function collapsePanel(el) {
  if (!el || !el.querySelector) return el;
  const ph = el.querySelector(':scope > .ph'), pb = el.querySelector(':scope > .pb'); if (!ph || !pb) return el;
  const d = h('details', { class: el.className + ' fold' }), sm = h('summary', { class: 'ph' }); while (ph.firstChild) sm.append(ph.firstChild);
  d.append(sm, pb); return d;
}
// Lays out named panels as the workflow asks: columns, widths, order, hidden and collapsed panels.
// `spec` is the workflow's layout (or null for the basic one); `basic` maps each mode to its default columns.
function layoutEl(spec, basic, makers) {
  spec = spec || {};
  const mode = spec.mode || basic.mode;
  const cols = spec.columns && spec.columns.length ? spec.columns : basic[mode];
  const adj = {}; (spec.panels || []).forEach(p => { adj[p.id] = p; });
  const made = id => {
    const o = adj[id] || {}; if (o.hidden || !makers[id]) return null;
    let el = makers[id](); if (!el) return null;
    if (o.title) el = retitle(el, o.title);
    return o.collapsed ? collapsePanel(el) : el;
  };
  if (mode === 'stacked' && !(spec.columns && spec.columns.length)) return h('div', { class: 'col' }, cols[0].panels.map(made));
  return h('div', { class: 'lay', style: 'grid-template-columns:' + cols.map(c => track(c.width)).join(' ') }, cols.map(c => h('div', { class: 'col' }, c.panels.map(made))));
}
const OV_ALL = ['stand', 'next', 'certificate', 'party', 'entries', 'summary', 'case', 'stage', 'actions', 'log', 'missed'];
const OV_BASIC = { mode: 'console',
  console: [{ width: 'minmax(260px,310px)', panels: ['case', 'stage', 'actions'] }, { width: '1fr', panels: ['stand', 'next', 'certificate', 'party', 'entries', 'summary'] }, { width: 'minmax(300px,360px)', panels: ['log', 'missed'] }],
  split: [{ width: '1fr', panels: ['stand', 'next', 'certificate', 'party', 'entries', 'summary'] }, { width: '340px', panels: ['case', 'stage', 'actions', 'log', 'missed'] }],
  stacked: [{ panels: ['stand', 'next', 'certificate', 'party', 'entries', 'summary', 'case', 'stage', 'actions', 'log', 'missed'] }] };
const TASK_BASIC = { mode: 'split',
  split: [{ width: '1fr', panels: ['form', 'checks'] }, { width: '340px', panels: ['stage', 'case'] }],
  console: [{ width: '1fr', panels: ['form', 'checks'] }, { width: '340px', panels: ['stage', 'case'] }],
  stacked: [{ panels: ['form', 'checks', 'stage', 'case'] }] };

// ---------- case overview: facts | work | record ----------
async function showOverview(id) {
  loading();
  const ov = await api('GET', '/cases/' + encodeURIComponent(id) + '/overview', undefined, id);
  S.ov = ov; S.stand = ''; S.errs = {};
  S.def = null;
  try { S.def = await api('GET', '/pipelines/' + encodeURIComponent(ov.case.pipeline)); } catch (e) { /* the workflow description is optional */ }
  renderOverview();
}
// Who a pending question was put to, and by when, in words.
function askedOf(info) {
  const who = info.to === 'user' ? person(info.assignee) : info.to === 'previous' ? 'the ' + (info.answer_roles || []).join(' or ') + ' role' : 'the person who created the case';
  return who + (info.due_at ? ' (answer due ' + short(info.due_at) + (info.overdue ? ', overdue' : '') + ')' : '');
}
function ovNext(ov) {
  const st = ov.stage, ops = (ov.work && ov.work.operations) || [];
  if (FINISHED.includes(ov.case.status)) return 'This case is ' + ov.case.status + '. Nothing more to do.';
  if (ov.work && ov.work.suspended) return 'On hold: ' + ov.work.suspended.reason + '.' + (ops.includes('resume') ? ' You can resume it from Actions.' : '');
  if (ov.information) return ov.information_can_answer ? 'A question is waiting for you: “' + ov.information.reason + '”. Answer it below.' : 'Waiting for ' + askedOf(ov.information) + ' to answer: “' + ov.information.reason + '”. The deadline clock ' + (ov.work && ov.work.sla && ov.work.sla.status === 'paused' ? 'is stopped meanwhile.' : 'keeps running.');
  if (ov.party && ov.party.status !== 'submitted') return 'Waiting for ' + ov.party.party + ' to complete this step.' + (ov.party.status === 'expired' ? ' The link has expired; issue a new one.' : '');
  if (ov.entries && ov.can_work) return ov.entries.can_complete ? 'Your entries are in. Review them and continue.' : 'Add your entry to continue.';
  if (ov.needs_start) return 'Ready to begin. Press Start to take this stage on; your name and the time are recorded.';
  if (ov.can_work) return 'Open the stage to continue.';
  return 'This stage is with ' + (st.started_by ? person(st.started_by) : ov.work && ov.work.assignee ? person(ov.work.assignee) : 'the team') + '. Nothing is needed from you right now.';
}
function renderOverview() {
  const ov = S.ov, c = ov.case, st = ov.stage, w = ov.work, cid = encodeURIComponent(c.id), ui = ov.ui || {};
  applyUI(ov.ui);
  const done = FINISHED.includes(c.status), wf = (S.def && (S.def.title || S.def.name)) || c.title || 'Case';
  // the primary action lives in the page bar, so it is always in the same place
  let primary = null;
  if (!done) {
    if (ov.needs_start) primary = h('button', { class: 'primary', type: 'button', disabled: S.busy, onclick: () => startStage() }, 'Start ' + (st.title || st.name));
    else if (ov.can_work) primary = h('a', { class: 'btn primary', href: '/tasks/' + cid }, ov.entries ? (ov.entries.mine.length ? 'Open ' : 'Start ') + (st.title || 'entries') : 'Continue ' + (st.title || 'working'));
  }
  const caseFacts = [['Workflow', wf], ['Number', h('span', { class: 'mono' }, c.number)], ['Opened', short(c.created_at)], ['Status', badge(c.status)]];
  if (ov.workspace) caseFacts.splice(1, 0, [ov.workspace.level.charAt(0).toUpperCase() + ov.workspace.level.slice(1), h('span', { title: ov.workspace.path.join(' › ') }, ov.workspace.name)]);
  if (keys[c.id]) caseFacts.push(['Case ID', h('code', null, c.id)]);
  const stageFacts = [['Stage', st.title || st.name], ['State', badge(st.status)]];
  if (st.started_at) stageFacts.push(['Started', short(st.started_at) + (st.started_by ? ' by ' + person(st.started_by) : '')]);
  if (w && w.assignee) stageFacts.push(['Handled by', person(w.assignee)]);
  const kv = rows => h('dl', { class: 'kv' }, rows.map(([k, v]) => [h('dt', null, k), h('dd', null, v)]));
  const groups = [], by = {};
  (ov.fields || []).forEach(f => { const k = f.stage + ' · ' + f.group; if (!by[k]) { by[k] = []; groups.push(k); } by[k].push(f); });
  const log = logCard(ov);
  const makers = {
    case: () => panel('Case', kv(caseFacts)),
    stage: () => panel('Current stage', [kv(stageFacts), done ? null : h('div', { style: 'margin-top:14px' }, slaMeter(w, st))], { actions: slaBadge(w, st) }),
    actions: () => moreCard(ov),
    stand: () => standPanel(ov),
    next: () => panel(done ? 'Outcome' : 'Next step', [h('p', { style: 'margin:0 0 4px;font-size:.95rem' }, ovNext(ov)), answerPanel(ov)], { cls: ov.party && ov.party.status === 'waiting' ? 'wait' : '' }),
    certificate: () => certCard(ov),
    party: () => partyCard(ov),
    entries: () => entriesCard(ov),
    summary: () => panel('Summary of data', groups.length ? groups.map(k => h('div', null, h('div', { class: 'group-title' }, k), h('dl', { class: 'summary' }, by[k].map(f => [h('dt', null, f.label), h('dd', null, valText(f), f.masked ? h('span', { class: 'muted' }, ' (masked)') : null)]))))
      : h('div', { class: 'empty' }, h('b', null, 'Nothing submitted yet'), 'Answers appear here as each stage is completed.')),
    log: () => log[0],
    missed: () => log[1],
  };
  const layout = ui.layout || {};
  screen({ crumb: [token ? '/work' : '/', token ? '← My work' : '← Home'], title: wf, num: c.number, sub: 'Opened ' + short(c.created_at) + ' · ' + (st.title || st.name), actions: [slaBadge(w, st), badge(c.status), primary] },
    layout.flow === 'hide' ? null : flowBar(ov, cid, done, st), layoutEl(layout, OV_BASIC, makers));
}
async function startStage() {
  const ov = S.ov; S.busy = true;
  try { await api('POST', '/cases/' + encodeURIComponent(ov.case.id) + '/stages/' + encodeURIComponent(ov.stage.name) + '/work/start', {}, ov.case.id); nav('/tasks/' + encodeURIComponent(ov.case.id)); }
  catch (e) { S.busy = false; S.notice = { kind: 'error', text: e.message }; renderOverview(); }
}
// The log: who started each stage, how long it waited, how long it took, as a vertical timeline.
function logCard(ov) {
  const rows = ov.log || [], late = ov.breaches || [];
  const item = l => h('li', { class: l.open ? 'open' : l.outcome === 'reset' ? 'reset' : l.breached ? 'late' : 'done' },
    h('div', { class: 't' }, l.title, l.open ? badge('active', 'in progress') : l.outcome === 'reset' ? badge('returned', 'reset') : badge(l.outcome || 'completed'), l.breached ? badge('failed', 'deadline missed') : null),
    h('div', { class: 'meta' }, l.started_at ? 'Started ' + short(l.started_at) + ' by ' + (l.started_by ? person(l.started_by) : 'applicant') : 'Not started'),
    h('div', { class: 'nums' }, h('span', null, 'Waited ', h('b', null, fmtDur(l.wait_seconds))), l.started_at ? h('span', null, 'Worked ', h('b', null, fmtDur(l.work_seconds) + (l.open ? '…' : ''))) : null, l.hold_seconds ? h('span', null, 'Hold ', h('b', null, fmtDur(l.hold_seconds))) : null));
  return [panel('Activity log', rows.length ? h('ol', { class: 'tl' }, rows.map(item)) : h('div', { class: 'empty' }, 'No activity yet.')),
    late.length ? panel('Missed deadlines', late.map(b => h('div', { class: 'banner ' + (b.resolution ? 'info' : 'error'), style: 'flex-direction:column;gap:2px;margin-bottom:8px' },
      h('strong', null, b.stage + ': due ' + short(b.due_at) + ' · ' + (b.resolution ? 'settled by ' + person(b.resolved_by) + ' (' + b.resolution + ')' : 'open')), b.reason,
      b.resolution_comment ? h('em', null, 'Note: ' + b.resolution_comment) : null))) : null];
}

// ---------- admin: take a stand on a missed deadline ----------
const STANDS = {
  reset: ['Reset the stage', 'Restore the data and start state from when the stage opened, and restart the clock.'],
  assign: ['Assign to someone', 'Hand it to another person.'],
  extend: ['Extend the deadline', 'Give the same person more time.'],
  waive: ['Accept the delay', 'Keep everything as is and record why the miss is accepted.'],
};
function standPanel(ov) {
  const ops = (ov.work && ov.work.operations) || [], mine = ['reset', 'assign', 'extend', 'waive'].filter(o => ops.includes(o));
  if (!mine.length) return null;
  const open = (ov.breaches || []).find(b => !b.resolved_at && b.stage === ov.stage.name);
  const st = ov.stage, cur = S.stand;
  const workers = ((S.def && S.def.workers) || []).filter(x => { const sd = (S.def.stages || []).find(z => z.name === st.name); return !sd || !(sd.roles || []).length || (x.roles || []).some(r => sd.roles.includes(r)); }).filter(x => x.id !== (ov.work && ov.work.assignee));
  const text = h('textarea', { id: 'stand_text', rows: 3, placeholder: cur === 'waive' ? 'Why is the miss accepted? (required)' : 'Reason (shown in the log)' });
  const sel = cur === 'assign' ? (workers.length ? h('select', { id: 'stand_to', 'aria-label': 'Assign to' }, h('option', { value: '' }, 'Choose a person…'), workers.map(x => h('option', { value: x.id }, person(x.id) + ' (' + (x.roles || []).join(', ') + ')')))
    : h('input', { type: 'text', id: 'stand_to', placeholder: 'User id' })) : null;
  const by = cur === 'extend' ? h('select', { id: 'stand_by', 'aria-label': 'Extend by' }, [['1h', '1 hour'], ['4h', '4 hours'], ['24h', '1 day'], ['72h', '3 days']].map(([v, l]) => h('option', { value: v }, l))) : null;
  const go = async () => {
    const comment = (document.getElementById('stand_text') || {}).value || '', to = (document.getElementById('stand_to') || {}).value, ext = (document.getElementById('stand_by') || {}).value;
    if ((cur === 'waive' || cur === 'reset') && !comment.trim()) { S.errs = { stand: ['A reason is required.'] }; return renderOverview(); }
    if (cur === 'assign' && !to) { S.errs = { stand: ['Choose who to assign it to.'] }; return renderOverview(); }
    const body = { comment: comment.trim() }; if (cur === 'assign') body.to = to; if (cur === 'extend') body.by = ext;
    S.busy = true;
    try { await api('POST', '/cases/' + encodeURIComponent(ov.case.id) + '/stages/' + encodeURIComponent(st.name) + '/work/' + cur, body, ov.case.id); bounce('ok', STANDS[cur][0] + ': done.', location.pathname); }
    catch (e) { S.busy = false; S.errs = { stand: [e.message] }; renderOverview(); }
  };
  const form = cur ? h('div', { class: 'stack', style: 'margin-top:12px' }, h('strong', null, STANDS[cur][0]), h('p', { class: 'muted', style: 'margin:0' }, STANDS[cur][1]), sel, by, text, errList(S.errs.stand),
    h('div', { class: 'row' }, h('button', { class: 'primary', type: 'button', disabled: S.busy, onclick: go }, 'Confirm'), h('button', { type: 'button', onclick: () => { S.stand = ''; S.errs = {}; renderOverview(); } }, 'Cancel'))) : null;
  const opts = h('div', { class: 'opts-row' }, mine.map(o => h('button', { type: 'button', class: 'opt' + (cur === o ? ' active' : ''), onclick: () => { S.stand = o; S.errs = {}; renderOverview(); } }, h('span', null, STANDS[o][0]), h('small', null, STANDS[o][1]))));
  if (open) return panel('Deadline missed', [h('p', { class: 'why' }, h('strong', null, 'Why: '), open.reason), h('p', { class: 'muted' }, 'Due ' + short(open.due_at) + ' · noticed ' + short(open.breached_at) + '. Choose how to handle it.'), opts, form], { cls: 'alert stand' });
  return panel('Admin actions', [opts, form], { cls: 'stand' });
}

// ---------- hold, resume, ask the applicant, release ----------
const MORE = { suspend: ['Put on hold', 'Why is it on hold?', 'Put on hold'], resume: ['Resume or release', 'Why? (optional, shown in the history)', 'Confirm'], request_information: ['Ask for information', 'What do you need to know?', 'Send question'], release: ['Release to the queue', 'Why? (optional)', 'Release'] };
function moreCard(ov) {
  const ops = ((ov.work && ov.work.operations) || []).filter(o => MORE[o]);
  if (!ops.length) return null;
  const run = async op => {
    const text = (document.getElementById('more_text') || {}).value || '';
    if (op === 'suspend' && !text.trim()) { S.errs = { more: ['This is required.'] }; return renderOverview(); }
    if (op === 'request_information' && !text.trim()) { S.errs = { more: ['Say what you need.'] }; return renderOverview(); }
    const id = encodeURIComponent(ov.case.id);
    if (op === 'request_information' && S.askTo === 'user' && !((document.getElementById('ask_who') || {}).value || '').trim()) { S.errs = { more: ['Choose who to ask.'] }; return renderOverview(); }
    if (op === 'resume' && (((ov.work.resume_targets || []).find(t => t.to === (S.resumeTo || 'same')) || {}).assignee) === 'required' && !((document.getElementById('more_who') || {}).value || '').trim()) { S.errs = { more: ['Choose who takes it.'] }; return renderOverview(); }
    const call = op === 'request_information' ? ['POST', '/cases/' + id + '/information-request', { reason: text.trim(), to: S.askTo || '', assignee: (document.getElementById('ask_who') || {}).value || '', fields: [...(S.askFields || [])] }]
      : ['POST', '/cases/' + id + '/stages/' + encodeURIComponent(ov.stage.name) + '/work/' + op, op === 'suspend' ? { reason: text.trim() } : op === 'resume' ? { comment: text.trim(), to: S.resumeTo || 'same', assignee: (document.getElementById('more_who') || {}).value || '' } : { comment: text.trim() }];
    S.busy = true;
    try { await api(...call, ov.case.id); bounce('ok', MORE[op][0] + ': done.', location.pathname); }
    catch (e) { S.busy = false; S.errs = { more: [e.message] }; renderOverview(); }
  };
  return panel('Actions', [h('div', { class: 'stack' }, ops.map(o => h('button', { type: 'button', class: S.panel === o ? 'primary' : '', onclick: () => { S.panel = S.panel === o ? '' : o; S.errs = {}; renderOverview(); } }, MORE[o][0]))),
    S.panel && MORE[S.panel] ? h('div', { class: 'stack', style: 'margin-top:12px' }, S.panel === 'resume' ? resumeChooser(ov) : S.panel === 'request_information' ? askChooser(ov) : null, h('label', { for: 'more_text' }, MORE[S.panel][1]), h('textarea', { id: 'more_text', rows: 3 }), errList(S.errs.more),
      h('button', { type: 'button', class: 'primary', disabled: S.busy, onclick: () => run(S.panel) }, MORE[S.panel][2])) : null]);
}
// Where released work goes: carry on at this step, or send it back to the step it came from as new work.
// Either way a person can be named; leaving it open keeps the step's own queue or routing.
function resumeChooser(ov) {
  const targets = (ov.work && ov.work.resume_targets) || [{ to: 'same', stage: ov.stage.name, title: ov.stage.title || ov.stage.name }];
  if (!targets.some(t => t.to === S.resumeTo)) S.resumeTo = 'same';
  const cur = targets.find(t => t.to === S.resumeTo) || targets[0];
  const sd = ((S.def && S.def.stages) || []).find(z => z.name === cur.stage);
  const workers = ((S.def && S.def.workers) || []).filter(x => !sd || !(sd.roles || []).length || (x.roles || []).some(r => sd.roles.includes(r)));
  const mode = cur.assignee || 'optional';
  const who = mode === 'none' ? null : workers.length ? h('select', { id: 'more_who', 'aria-label': 'Give it to' }, h('option', { value: '' }, mode === 'required' ? 'Choose a person…' : S.resumeTo === 'same' ? 'Whoever holds it now' : 'Anyone in ' + cur.title + ' (its queue)'),
    workers.map(x => h('option', { value: x.id }, person(x.id) + ' (' + (x.roles || []).join(', ') + ')'))) : h('input', { type: 'text', id: 'more_who', placeholder: 'A user id (optional)' });
  const explain = S.resumeTo === 'previous'
    ? 'It goes back to ' + cur.title + ' as new work: that step must be started again, its deadline starts afresh and nothing is carried over except the data entered so far. This hold is recorded in the history.'
    : 'The hold ends and work carries on in ' + cur.title + ' where it stopped; the deadline is extended by the time it was on hold.';
  return h('div', { class: 'stack' }, h('div', { class: 'opts-row' }, targets.map(t => h('button', { type: 'button', class: 'opt' + (S.resumeTo === t.to ? ' active' : ''), onclick: () => { S.resumeTo = t.to; renderOverview(); } },
    h('span', null, t.to === 'same' ? 'Carry on here' : 'Send back'), h('small', null, t.title)))),
    h('p', { class: 'muted', style: 'margin:0' }, explain), who ? h('label', { for: 'more_who' }, mode === 'required' ? 'Give it to (required)' : 'Give it to (optional)') : null, who);
}
// Every plain input of the workflow, with its form and path, for choosing what a question asks for.
function defInputs() {
  const d = S.def || {}, top = Object.fromEntries((d.inputs || []).map(i => [i.name, i])), out = [];
  (d.forms || []).forEach(f => { if (f.repeatable) return; const list = f.inputs && f.inputs.length ? f.inputs : (f.fields || []).map(n => top[n]).filter(Boolean);
    list.forEach(i => { if (!['file'].includes(i.kind) && !i.compute && !i.computed && !i.sealed) out.push({ path: f.name + '.' + i.name, form: f.title || f.name, input: i }); }); });
  return out;
}
// Optionally ask the person to fill in specific inputs; their answers are written into the case.
function askFields() {
  S.askFields = S.askFields || new Set();
  const all = defInputs(); if (!all.length) return null;
  const groups = {}; all.forEach(x => { (groups[x.form] = groups[x.form] || []).push(x); });
  return h('details', { class: 'ask-fields', open: S.askFields.size ? '' : null }, h('summary', null, 'Ask them to fill in specific fields' + (S.askFields.size ? ' (' + S.askFields.size + ')' : '')),
    h('div', { class: 'stack' }, Object.keys(groups).map(g => h('div', null, h('div', { class: 'group-title' }, g), groups[g].map(x => h('label', { class: 'check' }, h('input', { type: 'checkbox', checked: S.askFields.has(x.path), onchange: e => { if (e.target.checked) S.askFields.add(x.path); else S.askFields.delete(x.path); } }), x.input.label || x.input.name))))));
}
// Who to ask: the options a stage allows (the person who created it, the people of the previous step, a named person).
function askChooser(ov) {
  const targets = (ov.work && ov.work.info_targets) || [];
  if (!targets.length) return null;
  if (!targets.some(t => t.to === S.askTo)) S.askTo = targets[0].to;
  const cur = targets.find(t => t.to === S.askTo);
  const workers = ((S.def && S.def.workers) || []);
  const who = cur.person ? (workers.length ? h('select', { id: 'ask_who', 'aria-label': 'Ask' }, h('option', { value: '' }, 'Choose a person…'), workers.map(x => h('option', { value: x.id }, person(x.id) + ' (' + (x.roles || []).join(', ') + ')')))
    : h('input', { type: 'text', id: 'ask_who', placeholder: 'A user id' })) : null;
  return h('div', { class: 'stack' }, targets.length > 1 ? h('div', { class: 'opts-row' }, targets.map(t => h('button', { type: 'button', class: 'opt' + (S.askTo === t.to ? ' active' : ''), onclick: () => { S.askTo = t.to; renderOverview(); } }, h('span', null, t.to === 'creator' ? 'Creator' : t.to === 'previous' ? 'Earlier step' : 'A person'), h('small', null, t.label)))) : h('p', { class: 'muted', style: 'margin:0' }, 'Asked of: ' + cur.label),
    who, ov.work.info_fields ? askFields() : null, h('p', { class: 'muted', style: 'margin:0' }, 'The deadline clock stops until they answer' + (cur.due ? ', and they have ' + cur.due + '.' : '.')));
}
// The answer box, shown to the person (or role) a question was put to.
// (older comment) The applicant's answer box, shown when staff have asked for more information.
function answerPanel(ov) {
  if (!ov.information || !ov.information_can_answer) return null;
  const info = ov.information, asked = info.fields || [], byPath = Object.fromEntries(defInputs().map(x => [x.path, x.input]));
  S.ansVals = S.ansVals || {}; S.ansChoices = S.ansChoices || {};
  // Start from what the case holds now, so they only confirm or correct it.
  (ov.fields || []).forEach(f => { if (f.path && asked.includes(f.path) && S.ansVals[f.path] === undefined && f.value != null) S.ansVals[f.path] = f.value; });
  // Lookup-backed inputs need their choices (the workflow description lists only fixed options).
  asked.forEach(path => { const inp = byPath[path]; if (inp && inp.lookup && !S.ansChoices[path]) { S.ansChoices[path] = 'loading';
    const [form, name] = path.split('.'); api('POST', '/cases/' + encodeURIComponent(ov.case.id) + '/choices/' + encodeURIComponent(form) + '/' + encodeURIComponent(name), { deps: {} }, ov.case.id).then(r => { S.ansChoices[path] = r.choices || []; renderOverview(); }).catch(() => { S.ansChoices[path] = []; }); } });
  const fields = asked.map(path => { const base = byPath[path]; if (!base) return null;
    const inp = Object.assign({}, base, { editable: true, choices: Array.isArray(S.ansChoices[path]) ? S.ansChoices[path] : base.choices, depends_on: [] });
    return h('div', { class: 'field' }, h('label', { for: safeId(path) }, base.label || base.name, base.required ? ' *' : ''), control(inp, path, S.ansVals[path], v => { S.ansVals[path] = v; }), errList(S.errs[path])); });
  const ans = h('textarea', { rows: 3, id: 'ans', placeholder: asked.length ? 'Anything to add (optional)' : 'Your answer' });
  return h('div', { class: 'stack', style: 'margin-top:14px' }, h('blockquote', null, info.reason), info.due_at ? h('p', { class: 'muted small', style: 'margin:0' }, 'Answer due ' + short(info.due_at) + (info.overdue ? ' (overdue)' : '')) : null,
    asked.length ? h('p', { class: 'muted', style: 'margin:0' }, 'Please fill in what was asked; it is saved into the case.') : null, h('div', { class: 'grid' }, fields),
    h('label', { for: 'ans' }, asked.length ? 'Note' : 'Your answer'), ans, errList(S.errs.answer),
    h('div', null, h('button', { class: 'primary', type: 'button', disabled: S.busy, onclick: async () => {
      if (!asked.length && !ans.value.trim()) { S.errs = { answer: ['Please write an answer.'] }; return renderOverview(); }
      const data = {}; asked.forEach(p => { if (S.ansVals[p] != null) data[p] = S.ansVals[p]; });
      try { await api('POST', '/cases/' + encodeURIComponent(ov.case.id) + '/information-response', { response: { answer: ans.value.trim(), data } }, ov.case.id); S.ansVals = {}; bounce('ok', 'Thank you. Your answer was sent.', location.pathname); }
      catch (e) { S.errs = { answer: asked.length && e.details && e.details.length ? [] : [e.message] }; (e.details || []).forEach(d => { (S.errs[d.path] = S.errs[d.path] || []).push(d.message); }); renderOverview(); }
    } }, 'Send answer')));
}

// ---------- task: the stage's form, checks and buttons ----------
async function showTask(id, stage) {
  loading();
  const entry = new URLSearchParams(location.search).get('entry');
  const v = await api('GET', '/cases/' + encodeURIComponent(id) + (stage ? '/stages/' + encodeURIComponent(stage) : '') + (entry ? '?entry=' + encodeURIComponent(entry) : ''), undefined, id);
  S.errs = {}; S.entryId = entry || ''; applyView(v); renderTask();
}
const DONE = ['passed', 'waived', 'skipped'];
const openNodes = () => ((S.view && S.view.nodes) || []).filter(n => !n.optional && !DONE.includes(n.status));
function labelFor(path) { let l = path; forEachInput(i => { if (i.path === path) l = i.label; }); return l; }
function renderTask() {
  const v = S.view, c = v.case, st = v.stage, cid = encodeURIComponent(c.id);
  applyUI(v.ui);
  if (!st) { screen({ crumb: ['/cases/' + cid, '← Back to the case'], title: 'Case', num: c.number }, panel('', h('p', null, 'Nothing to show for you here.'))); return; }
  if (S.review) return renderSummary();
  const ops = (v.work && v.work.operations) || [], locked = st.require_start && !st.started_at && ops.includes('start');
  const pg = v.page;
  const items = pg && pg.groups ? pg.groups.map(g => ({ title: g.title || g.name, forms: g.forms.map(f => f.name), flagged: g.forms.some(f => f.inputs.some(i => i.flag)),
    collapsed: g.collapsed, el: h('div', null, infoEl(g.name), groupEl(g)) })) : [];
  const en = v.entries, entryMode = !!en && !!S.entryId, listMode = !!en && !S.entryId;
  const bar = entryMode ? entryBar() : actionBar();
  const startBanner = locked ? h('div', { class: 'banner warn' }, h('div', { class: 'grow' }, h('strong', null, 'Start this stage to begin. '), 'Your name and the time are recorded, and the work is yours until you finish or release it.'),
    h('button', { class: 'primary', type: 'button', disabled: S.busy, onclick: async () => { S.busy = true; try { await api('POST', '/cases/' + cid + '/stages/' + encodeURIComponent(st.name) + '/work/start', {}, c.id); location.reload(); } catch (e) { S.busy = false; S.notice = { kind: 'error', text: e.message }; renderTask(); } } }, 'Start')) : null;
  const makers = {
    form: () => listMode ? h('div', { class: 'col' }, entryList()) : items.length ? panel(pg.title || st.title || st.name, [pg.description ? h('p', { class: 'muted' }, pg.description) : null, infoEl(''), arrange(pg.layout, 'page', items, bar)])
      : (bar && !openNodes().length ? panel(st.title || st.name, bar) : null),
    checks: () => (v.nodes || []).length ? panel('Checks', v.nodes.map(nodeEl)) : null,
    stage: () => panel('This stage', [h('dl', { class: 'kv' }, h('dt', null, 'Stage'), h('dd', null, st.title || st.name), h('dt', null, 'State'), h('dd', null, badge(st.status)),
      h('dt', null, 'Started'), h('dd', null, st.started_at ? short(st.started_at) + (st.started_by ? ' by ' + person(st.started_by) : '') : 'Not yet'), v.work && v.work.assignee ? [h('dt', null, 'Handled by'), h('dd', null, person(v.work.assignee))] : null),
      v.work && v.work.sla && st.due_at ? h('div', { style: 'margin-top:14px' }, slaMeter(v.work, st)) : null], { actions: slaBadge(v.work, st) }),
    case: () => panel('Case', [h('dl', { class: 'kv' }, h('dt', null, 'Number'), h('dd', null, h('span', { class: 'mono' }, c.number)), h('dt', null, 'Status'), h('dd', null, badge(c.status))), h('div', { style: 'margin-top:12px' }, h('a', { class: 'btn', href: '/cases/' + cid }, 'Open case overview'))]),
  };
  screen({ crumb: ['/cases/' + cid, '← ' + (c.title || 'Case') + ' ' + c.number], title: st.title || st.name, num: c.number, sub: st.started_at ? 'Started ' + short(st.started_at) + (st.started_by ? ' by ' + person(st.started_by) : '') : 'Not started', actions: [slaBadge(v.work, st), badge(st.status)],
    after: [v.work && v.work.suspended ? h('div', { class: 'banner warn' }, 'On hold: ' + v.work.suspended.reason) : null, startBanner,
      (v.corrections || []).length ? h('div', { class: 'banner warn' }, h('div', null, h('strong', null, 'Please correct: '), v.corrections.map(f => h('div', null, labelFor(f.path), f.comment ? ': ' + f.comment : '')))) : null] },
    layoutEl((v.ui || {}).layout, TASK_BASIC, makers));
}

// ---------- data entries: how many, who, edit and draft rules ----------
const entryRule = en => {
  const bits = [];
  bits.push(en.per_user === 1 ? 'You can make one entry.' : en.per_user > 1 ? 'You can make up to ' + en.per_user + ' entries.' : 'You can make as many entries as you need.');
  if (en.min > 1) bits.push(en.min + ' submitted entries are needed to continue.'); else bits.push('At least one submitted entry is needed to continue.');
  if (en.max) bits.push('The stage takes at most ' + en.max + ' entries in all.');
  bits.push(en.allow_draft ? 'You can save an entry as a draft.' : 'Entries are submitted straight away.');
  bits.push(en.allow_edit ? 'You can change an entry until the stage is finished.' : 'A submitted entry cannot be changed.');
  return bits.join(' ');
};
function entryList() {
  const v = S.view, en = v.entries, c = v.case, cid = encodeURIComponent(c.id), st = v.stage;
  const stageUrl = '/tasks/' + cid + (st.current ? '' : '/' + encodeURIComponent(st.name));
  const ro = ((v.page && v.page.groups) || []).filter(g => g.mode !== 'editable');
  const info = ro.length ? panel('Context', ro.map(g => h('div', null, g.title ? h('div', { class: 'group-title' }, g.title) : null, groupEl(g)))) : null;
  const rows = en.mine.map(e => h('tr', null, h('td', null, h('strong', null, 'Entry ' + e.seq)), h('td', null, e.status === 'submitted' ? badge('completed', 'submitted') : badge('warning', 'draft')),
    h('td', null, short(e.submitted_at || e.updated_at)),
    h('td', null, h('div', { class: 'row' }, h('a', { class: 'btn', href: stageUrl + '?entry=' + encodeURIComponent(e.id) }, (e.status === 'draft' || en.allow_edit) ? (e.status === 'draft' ? 'Continue' : 'Edit') : 'View'),
      (e.status === 'draft' || en.allow_edit) ? h('button', { type: 'button', class: 'ghost danger', onclick: () => delEntry(e) }, 'Delete') : null))));
  const used = en.per_user ? ' (' + en.mine.length + ' of ' + en.per_user + ' used)' : '';
  return [info, panel('Your entries' + used, [h('p', { class: 'muted' }, entryRule(en)),
    en.mine.length ? h('div', { class: 'tbl-wrap' }, h('table', null, h('thead', null, h('tr', null, ['Entry', 'Status', 'When', ''].map(t => h('th', { scope: 'col' }, t)))), h('tbody', null, rows)))
      : h('div', { class: 'empty' }, h('b', null, 'No entries yet'), en.can_add ? 'Add your first entry to continue.' : 'You cannot add entries here.'),
    h('p', { class: 'note', style: 'margin-top:10px' }, en.submitted + ' submitted in all' + (en.min > 1 ? ' · ' + en.min + ' needed' : '') + (en.drafts ? ' · ' + en.drafts + ' draft' + (en.drafts === 1 ? '' : 's') : '') + '.'),
    actionBar()], { actions: en.can_add ? h('a', { class: 'btn primary', href: stageUrl + '?entry=new' }, en.mine.length ? 'Add another entry' : 'Add an entry') : null })];
}
async function delEntry(e) {
  if (!await ui.confirm({ title: 'Delete this entry?', message: 'Entry ' + e.seq + ' will be deleted. This cannot be undone.', confirmLabel: 'Delete', danger: true })) return;
  const v = S.view;
  try { await api('POST', '/cases/' + encodeURIComponent(v.case.id) + '/stages/' + encodeURIComponent(v.stage.name) + '/entries/' + encodeURIComponent(e.id) + '/delete', {}, v.case.id); bounce('ok', 'Entry deleted.', location.pathname); }
  catch (x) { S.notice = { kind: 'error', text: x.message }; renderTask(); }
}
// Save or submit the entry on screen.
async function saveEntry(submit) {
  const v = S.view, id = S.entryId === 'new' ? '' : S.entryId;
  const data = collect() || {};
  S.busy = true; S.errs = {}; S.notice = { kind: 'warn', text: submit ? 'Submitting…' : 'Saving…' }; renderTask();
  try {
    const path = '/cases/' + encodeURIComponent(v.case.id) + '/stages/' + encodeURIComponent(v.stage.name) + '/entries' + (id ? '/' + encodeURIComponent(id) + '/save' : '');
    await api('POST', path, { data, submit }, v.case.id);
    S.busy = false; bounce('ok', submit ? 'Entry submitted.' : 'Draft saved.', location.pathname);
  } catch (e) { S.busy = false; failed(e, ''); renderTask(); }
}
function entryBar() {
  const v = S.view, en = v.entries, cur = en.mine.find(e => e.id === S.entryId), submitted = cur && cur.status === 'submitted';
  const editable = v.can_edit, back = location.pathname;
  return h('div', { class: 'bar' },
    h('a', { class: 'btn', href: back }, '← All entries'),
    h('div', { class: 'row end' },
      editable && en.allow_draft && !submitted ? h('button', { type: 'button', disabled: S.busy, onclick: () => saveEntry(false) }, 'Save draft') : null,
      editable ? h('button', { type: 'button', class: 'primary', disabled: S.busy, onclick: () => saveEntry(true) }, submitted ? 'Save changes' : 'Submit entry') : h('span', { class: 'muted' }, 'This entry is read-only.')));
}

// ---------- actions and the summary page ----------
function actionBar() {
  const v = S.view, acts = v.actions || [];
  if (!v.can_edit && !acts.length) return null;
  const entriesStage = !!v.entries;
  const needs = acts.filter(a => a.comment_required).map(a => a.label), submits = acts.some(a => ['advance', 'approve'].includes(a.outcome));
  const held = !!(v.work && v.work.suspended), short1 = v.entries && !v.entries.can_complete, blocked = a => held || (['advance', 'approve'].includes(a.outcome) && (openNodes().length > 0 || short1));
  return h('div', { class: 'bar' },
    submits ? h('div', { style: 'flex:1 1 100%' }, acksEl()) : null,
    acts.some(a => a.comment_required) ? h('div', { class: 'field', style: 'flex:1 1 100%', 'data-path': 'comment' },
      h('label', { for: 'act_comment' }, 'Comment', h('span', { class: 'muted' }, ' (required to ' + needs.join(', ') + ')')),
      h('textarea', { id: 'act_comment', rows: 2, value: S.comment, oninput: e => { S.comment = e.target.value; } }), errList(S.errs.comment)) : null,
    v.can_edit && !entriesStage ? h('button', { type: 'button', disabled: S.busy, onclick: () => mutate('PUT', stagePath(), { data: collect() || {}, revision: v.case.revision }, '', 'Saved.') }, 'Save draft') : h('span'),
    h('div', { class: 'row end' }, acts.map(a => h('button', { type: 'button', disabled: S.busy || blocked(a), title: held ? 'The case is on hold' : short1 && ['advance', 'approve'].includes(a.outcome) ? 'Submit ' + v.entries.min + (v.entries.min === 1 ? ' entry' : ' entries') + ' first' : blocked(a) ? 'Finish the checks first: ' + openNodes().map(n => n.title || n.name).join(', ') : null,
      class: ['reject', 'withdraw'].includes(a.outcome) ? 'danger' : (a.outcome === 'advance' || a.outcome === 'approve' ? 'primary' : ''),
      onclick: async () => {
        const comment = S.comment.trim();
        if (a.comment_required && !comment) { S.errs = { comment: ['A comment is required to ' + a.label + '.'] }; renderTask(); const el = document.getElementById('act_comment'); if (el) el.focus(); return; }
        if (a.confirm && !a.confirm_submit && !await ui.confirm({ title: a.label + '?', message: a.confirm, confirmLabel: a.label, danger: ['reject', 'withdraw'].includes(a.outcome) })) return;
        const body = { revision: v.case.revision }; if (comment) body.comment = comment;
        const data = v.entries ? null : collect(); if (data) body.data = data;
        if (['advance', 'approve'].includes(a.outcome)) body.acknowledgements = ackBody();
        if (a.confirm_submit) return openSummary(a, body);
        mutate('POST', stagePath() + '/actions/' + encodeURIComponent(a.name), body, '', a.label + ': done.', () => bounce('ok', a.label + ': done.', '/cases/' + encodeURIComponent(v.case.id)));
      } }, a.label))));
}
// First step of a two-step submit: the server checks everything and returns what will be sent.
async function openSummary(a, body) {
  const path = stagePath() + '/actions/' + encodeURIComponent(a.name);
  S.busy = true; S.errs = {}; S.notice = { kind: 'warn', text: 'Checking…' }; renderTask();
  let r;
  try { r = await api('POST', '/cases/' + encodeURIComponent(S.view.case.id) + path, body, S.view.case.id); } catch (e) { failed(e, ''); }
  S.busy = false;
  if (r && r.confirmation_required) { S.notice = null; S.review = { r, a, body, path }; window.scrollTo(0, 0); }
  renderTask(); focusFirstError();
}
function renderSummary() {
  const { r, a, body, path } = S.review, rv = r.review || r, fields = rv.fields || [];
  const groups = [], byGroup = {};
  fields.forEach(f => { const g = f.group || ''; if (!byGroup[g]) { byGroup[g] = []; groups.push(g); } byGroup[g].push(f); });
  const cid = encodeURIComponent(S.view.case.id), c = S.view.case;
  const confirm = () => { S.review = null; mutate('POST', path, { ...body, confirm_token: r.confirm_token }, '', a.label + ': done.', () => bounce('ok', a.label + ': done.', '/cases/' + cid)); };
  screen({ crumb: ['/cases/' + cid, '← Back to the case'], title: rv.title || 'Check before you send', num: c.number, sub: rv.description || rv.prompt },
    h('div', { class: 'split' }, panel('What will be sent', [fields.length ? groups.map(g => h('div', null, g ? h('div', { class: 'group-title' }, g) : null,
      h('dl', { class: 'summary' }, byGroup[g].map(f => [h('dt', null, f.label), h('dd', null, valText(f), f.masked ? h('span', { class: 'muted' }, ' (masked)') : null)])))) : h('div', { class: 'empty' }, 'Nothing to show.'),
      (rv.acknowledgements || []).length ? h('div', null, h('div', { class: 'group-title' }, 'You agreed to'), h('ul', null, rv.acknowledgements.map(x => h('li', null, x.text)))) : null]),
      panel('Send it', [h('p', { class: 'muted' }, 'Once sent you cannot change this stage yourself.'), h('div', { class: 'stack' }, h('button', { type: 'button', class: 'primary big', disabled: S.busy, onclick: confirm }, rv.button || a.label), h('button', { type: 'button', onclick: () => { S.review = null; renderTask(); } }, 'Go back and edit'))])));
}

// ---------- certificates, outside parties, entries on the overview ----------
function certCard(ov) {
  const list = ov.certificates || []; if (!list.length) return null;
  return panel(list.length > 1 ? 'Certificates' : 'Certificate', list.map(ct => {
    const out = h('span', { class: 'muted' });
    return h('div', { class: 'entry' }, h('div', { class: 'row' }, h('strong', null, (ct.title || ct.name) + ' ' + ct.number), h('span', { class: 'grow' }), out),
      h('dl', { class: 'summary', style: 'margin-top:10px' }, h('dt', null, 'Issued'), h('dd', null, short(ct.issued_at)), ct.expires_at ? [h('dt', null, 'Valid until'), h('dd', null, short(ct.expires_at))] : null, ct.code ? [h('dt', null, 'Verification code'), h('dd', null, h('code', null, ct.code))] : null),
      h('div', { class: 'row', style: 'margin-top:10px' }, h('button', { type: 'button', onclick: async () => { out.textContent = 'Checking…'; try { const r = await api('GET', '/certificates/' + encodeURIComponent(ct.code || ct.number), undefined, null, true); out.className = r.valid ? 'badge b-ok' : 'badge b-bad'; out.textContent = r.valid ? 'Signature valid' : 'Not valid'; } catch (e) { out.textContent = e.message; } } }, 'Verify')));
  }));
}
function partyCard(ov) {
  const p = ov.party; if (!p) return null;
  const url = p.token ? location.origin + '/links/' + p.token : '';
  const ops = (ov.work && ov.work.operations) || [];
  const copy = h('button', { type: 'button', onclick: async () => { try { await navigator.clipboard.writeText(url); copy.textContent = 'Copied'; } catch (e) { ui.prompt({ title: 'Copy this link', message: 'Your browser did not allow copying. Select the link and copy it.', value: url, readonly: true, confirmLabel: 'Done', cancelLabel: 'Close' }); } } }, 'Copy link');
  return panel('Outside party', [
    h('dl', { class: 'kv' }, h('dt', null, 'Who'), h('dd', null, p.party), h('dt', null, 'Link sent'), h('dd', null, short(p.issued_at)), h('dt', null, p.status === 'submitted' ? 'Submitted' : 'Expires'), h('dd', null, short(p.submitted_at || p.expires_at))),
    p.status === 'waiting' && !p.token ? h('p', { class: 'muted', style: 'margin:12px 0 0' }, 'They have a private link and need no account. This step moves on as soon as they submit.') : null,
    p.token ? h('div', { class: 'stack', style: 'margin-top:14px' }, h('label', null, 'Private link for ' + p.party), h('input', { type: 'text', readonly: true, value: url, onfocus: e => e.target.select() }), h('div', { class: 'row' }, copy, h('a', { class: 'btn', href: url, target: '_blank', rel: 'noopener' }, 'Open as them'))) : null,
    ops.includes('reissue') ? h('div', { class: 'row', style: 'margin-top:12px' }, h('button', { type: 'button', onclick: async () => { try { await api('POST', '/cases/' + encodeURIComponent(ov.case.id) + '/stages/' + encodeURIComponent(ov.stage.name) + '/work/reissue', {}, ov.case.id); bounce('ok', 'A new link was issued; the old one no longer works.', location.pathname); } catch (e) { S.notice = { kind: 'error', text: e.message }; renderOverview(); } } }, 'Issue a new link')) : null],
    { actions: badge(p.status === 'submitted' ? 'completed' : p.status === 'expired' ? 'failed' : 'active', p.status), cls: p.status === 'waiting' ? 'wait' : '' });
}
function entriesCard(ov) {
  const rows = ov.entry_rows || []; if (!rows.length && !ov.entries) return null;
  return panel('Entries', [ov.entries ? h('p', { class: 'muted' }, entryRule(ov.entries)) : null,
    rows.length ? rows.map(r => h('div', { class: 'entry' }, h('div', { class: 'row' }, h('strong', null, r.stage_title + ' · entry ' + r.seq), r.status === 'submitted' ? badge('completed', 'submitted') : badge('warning', 'draft'),
      h('span', { class: 'muted' }, (r.mine ? 'by you' : r.by ? 'by ' + person(r.by) : '') + (r.submitted_at ? ' · ' + short(r.submitted_at) : ''))),
      r.fields.length ? h('dl', { class: 'summary', style: 'margin-top:10px' }, r.fields.map(f => [h('dt', null, f.label), h('dd', null, valText(f))])) : null)) : h('div', { class: 'empty' }, 'No entries yet.')],
    { actions: ov.entries ? h('span', { class: 'muted' }, ov.entries.submitted + ' submitted' + (ov.entries.min > 1 ? ' of ' + ov.entries.min + ' needed' : '')) : null });
}

// ---------- the outside party's own page ----------
async function showLink(token_) {
  loading();
  let v;
  try { v = await api('GET', '/links/' + encodeURIComponent(token_), undefined, null, true); }
  catch (e) { screen({ title: 'This link cannot be used' }, panel('', h('div', { class: 'empty' }, h('b', null, e.message), 'Ask the person who sent it for a new one.'))); return; }
  S.link = { token: token_, done: false }; S.errs = {}; applyView(v);
  renderLinkPage();
}
function renderLinkPage() {
  const v = S.view, x = v.external || {}, pg = v.page;
  if (S.link.done) { screen({ title: 'Thank you' }, panel('', h('div', { class: 'empty' }, h('b', null, 'Your answer was received'), 'The request moves on. You can close this page.'))); return; }
  const items = pg && pg.groups ? pg.groups.map(g => ({ title: g.title || g.name, forms: g.forms.map(f => f.name), flagged: false, collapsed: false, el: h('div', null, groupEl(g)) })) : [];
  const send = h('button', { type: 'button', class: 'primary big', disabled: S.busy, onclick: async () => {
    S.busy = true; S.errs = {};
    try { await api('POST', '/links/' + encodeURIComponent(S.link.token), { data: collect() || {} }, null, true); S.link.done = true; }
    catch (e) { failed(e, ''); }
    S.busy = false; renderLinkPage();
  } }, 'Submit');
  screen({ title: v.case.title || 'Request', num: v.case.number, sub: 'Requested of ' + (x.party || 'you') + (x.expires ? ' · link valid until ' + short(x.expires) : '') },
    h('div', { class: 'banner' }, 'You were asked to complete this step. You do not need an account, and the link works once.'),
    panel('Your part', arrange(pg && pg.layout, 'page', items, h('div', { class: 'bar' }, h('span'), h('div', { class: 'row end' }, send)))));
}

// ---------- work list ----------
async function showQueue(scope, quiet) {
  const c = claims(), isAdmin = !!(c && (c.roles || []).includes('admin'));
  const tabs = { queue: 'Waiting for my role', assigned: 'Assigned to me', answer: 'Questions for me', mine: 'My own cases' }; if (isAdmin) tabs.attention = 'Needs attention';
  const tabBar = n => h('nav', { class: 'tabs-bar' }, Object.keys(tabs).map(s => h('a', { class: s === scope ? 'on' : '', href: '/work?scope=' + s }, tabs[s], s === 'attention' && n ? h('span', { class: 'count' }, String(n)) : null)));
  if (!token) { screen({ title: 'My work' }, panel('', h('div', { class: 'empty' }, h('b', null, 'Sign in to see your work'), 'Use the demo accounts or the token box in the sidebar.'))); return; }
  if (!quiet) loading();
  // The lists are per workflow; the work list brings them all together.
  try { await workspaceTree(); } catch (e) { /* no workspaces for this account */ }
  const cat = await api('GET', '/pipelines') || [], sc = encodeURIComponent(scope === 'attention' ? 'all' : scope);
  let rows = (await Promise.all(cat.map(p => api('GET', '/cases?scope=' + sc + '&pipeline=' + encodeURIComponent(p.name)).then(rs => (rs || []).map(r => Object.assign({ wf: p.title || p.name }, r))).catch(() => [])))).flat()
    .sort((a, b) => String(b.updated_at || b.created_at).localeCompare(String(a.updated_at || a.created_at)));
  const qsig = JSON.stringify(rows.map(r => [r.id, r.status, r.stage, r.sla_status, r.assignee, r.on_hold, r.due_at, r.overdue, r.can_start, r.started_at])) + scope + (S.qStage || '');
  if (quiet && (qsig === S.queueSig || typing())) return;
  S.queueSig = qsig;
  const all = rows, needs = r => r.sla_status === 'breached' || r.sla_status === 'warning' || r.on_hold || r.overdue;
  const open = r => !['approved', 'rejected', 'completed', 'withdrawn'].includes(r.status);
  const attn = isAdmin ? all.filter(r => needs(r) && open(r)) : [];
  if (scope === 'attention') rows = attn;
  const chip = r => r.info_overdue ? badge('failed', 'answer overdue') : r.on_hold ? h('span', { class: 'badge b-warn' }, 'on hold') : r.sla_status === 'breached' ? badge('failed', 'SLA missed') : r.sla_status === 'warning' ? badge('warning', 'due soon') : badge(r.status);
  const stat = (n, label, cls) => h('div', { class: 'stat ' + (cls || '') }, h('b', null, String(n)), h('span', null, label));
  const stats = h('div', { class: 'stats' }, stat(rows.length, 'Cases'), stat(rows.filter(r => open(r)).length, 'Open', 'info'), stat(rows.filter(r => !r.assignee && open(r)).length, 'Unassigned', 'warn'),
    stat(rows.filter(r => r.sla_status === 'breached').length, 'SLA missed', 'bad'), stat(rows.filter(r => r.on_hold).length, 'On hold', 'warn'));
  // Work waiting at a step that must be started: pick the step, then start one case, the ones ticked, or all
  // of them. A case can also be opened and started from its own page.
  const stageKey = r => r.stage, stageName = r => r.stage_title || r.stage;
  const stageCounts = {}; rows.forEach(r => { const k = stageKey(r); (stageCounts[k] = stageCounts[k] || { name: stageName(r), n: 0 }).n++; });
  if (S.qStage && !stageCounts[S.qStage]) S.qStage = '';
  const shownRows = S.qStage ? rows.filter(r => stageKey(r) === S.qStage) : rows;
  const startable = shownRows.filter(r => r.can_start), picked = new Set();
  const startOne = async r => {
    try { await api('POST', '/cases/' + encodeURIComponent(r.id) + '/stages/' + encodeURIComponent(r.stage) + '/work/start', {}, r.id); S.notice = { kind: 'ok', text: 'Started ' + (r.number || r.id) + '.' }; }
    catch (e) { S.notice = { kind: 'error', text: (r.number || r.id) + ': ' + e.message }; }
    S.queueSig = ''; showQueue(scope);
  };
  const startMany = async list => {
    if (!list.length) return;
    const ok = await ui.confirm({ title: 'Start ' + list.length + ' ' + (list.length === 1 ? 'case' : 'cases') + '?', message: 'Each one is marked as started by you and its deadline keeps running. ' + list.map(r => r.number || r.id).slice(0, 8).join(', ') + (list.length > 8 ? ' and ' + (list.length - 8) + ' more' : '') + '.', confirmLabel: 'Start ' + list.length });
    if (!ok) return;
    let done = 0; const failed = [];
    for (const r of list) {
      try { await api('POST', '/cases/' + encodeURIComponent(r.id) + '/stages/' + encodeURIComponent(r.stage) + '/work/start', {}, r.id); done++; }
      catch (e) { failed.push((r.number || r.id) + ': ' + e.message); }
    }
    S.notice = failed.length ? { kind: 'warn', text: 'Started ' + done + ' of ' + list.length + '. Some could not be started:', list: failed } : { kind: 'ok', text: 'Started ' + done + ' ' + (done === 1 ? 'case' : 'cases') + '.' };
    S.queueSig = ''; showQueue(scope);
  };
  const selBtn = h('button', { type: 'button', disabled: true, onclick: () => startMany(startable.filter(r => picked.has(r.id))) }, 'Start selected');
  const allBtn = h('button', { type: 'button', class: 'primary', onclick: () => startMany(startable) }, 'Start all (' + startable.length + ')');
  const syncSel = () => { selBtn.disabled = !picked.size; selBtn.textContent = picked.size ? 'Start selected (' + picked.size + ')' : 'Start selected'; };
  const stageSel = Object.keys(stageCounts).length > 1 ? h('select', { 'aria-label': 'Show one step', onchange: e => { S.qStage = e.target.value; S.queueSig = ''; showQueue(scope); } },
    h('option', { value: '' }, 'All steps (' + rows.length + ')'), Object.entries(stageCounts).map(([k, v]) => { const o = h('option', { value: k }, v.name + ' (' + v.n + ')'); o.selected = S.qStage === k; return o; })) : null;
  const bar = stageSel || startable.length ? h('div', { class: 'row qbar' }, stageSel, h('span', { class: 'grow' }), startable.length ? [h('span', { class: 'muted small' }, startable.length + ' waiting to be started'), selBtn, allBtn] : null) : null;
  const line = r => h('tr', null,
    h('td', null, r.can_start ? h('input', { type: 'checkbox', 'aria-label': 'Select ' + (r.number || r.id), onchange: e => { if (e.target.checked) picked.add(r.id); else picked.delete(r.id); syncSel(); } }) : null),
    h('td', null, h('a', { href: '/cases/' + encodeURIComponent(r.id) }, h('strong', { class: 'mono' }, r.number || r.id))),
    h('td', null, r.wf, r.org_unit ? h('div', { class: 'small' }, wsName(r.org_unit)) : null), h('td', null, r.stage_title || r.stage),
    h('td', null, r.assignee ? person(r.assignee) : h('span', { class: 'muted' }, 'unassigned')),
    h('td', null, chip(r), r.require_start && !r.started_at && !r.on_hold ? h('div', { class: 'small muted' }, 'not started') : r.started_at ? h('div', { class: 'small muted' }, 'started') : null),
    h('td', { class: 'num' }, r.due_at ? h('span', { class: r.overdue ? 'err' : 'muted' }, rel(r.due_at)) : '—'),
    h('td', null, h('div', { class: 'row' }, r.can_start ? h('button', { type: 'button', class: 'primary', onclick: () => startOne(r) }, 'Start') : null, h('a', { class: 'btn', href: '/cases/' + encodeURIComponent(r.id) }, 'Open')),
      r.breach_reason ? h('div', { class: 'small', style: 'max-width:36ch;margin-top:4px' }, r.breach_reason) : null));
  const table = h('div', { class: 'tbl-wrap' }, h('table', null,
    h('thead', null, h('tr', null, ['', 'Case', 'Workflow', 'Step', 'Handled by', 'Status', 'Due', ''].map(t => h('th', { scope: 'col' }, t)))), h('tbody', null, shownRows.map(line))));
  screen({ title: 'My work', sub: scope === 'attention' ? 'Cases whose deadline is close or missed, or that are on hold.' : 'Cases waiting for you.', tabs: tabBar(attn.length) },
    stats, bar, panel('', shownRows.length ? table : h('div', { class: 'empty' }, h('b', null, 'All clear'), scope === 'attention' ? 'No case needs attention.' : 'Nothing is waiting for you right now.'), { flush: true }));
}

// ---------- workspace administration: reference data and settings ----------
const pluralSet = k => /y$/.test(k) ? k.slice(0, -1) + 'ies' : k + 's';
const attrText = a => Object.entries(a || {}).filter(([k]) => k !== 'disabled').map(([k, v]) => k + '=' + [].concat(v).join('|')).join('  ') || '—';
const parseAttrs = t => { const out = {}; String(t || '').split(/[\n,;]+/).map(x => x.trim()).filter(Boolean).forEach(p => { const [k, ...r] = p.split('='); if (k && r.length) out[k.trim()] = r.join('=').split('|').map(x => x.trim()).filter(Boolean); }); return out; };
async function showWorkspace() {
  if (!token) { screen({ title: 'Workspace' }, panel('', h('div', { class: 'empty' }, h('b', null, 'Sign in to manage a workspace'), 'Use a Workspace admin account in the sidebar.'))); return; }
  loading();
  const tree = await workspaceTree().catch(() => []);
  if (!tree.length) { screen({ title: 'Workspace' }, panel('', h('div', { class: 'empty' }, h('b', null, 'No workspace in your scope'), 'Your account is not assigned to a workspace.'))); return; }
  // Every lookup set the workflows use, and where.
  if (!S.wsSets) {
    const cat = await api('GET', '/pipelines') || [], sets = {}, flows = [];
    for (const p of cat) {
      const d = await api('GET', '/pipelines/' + encodeURIComponent(p.name)).catch(() => null); if (!d) continue;
      if (d.workspace) flows.push({ name: d.name, title: d.title || d.name, stages: d.stages || [], inputs: d.inputs || [], forms: d.forms || [] });
      const where = (d.forms || []).flatMap(f => (f.inputs || (f.fields || []).map(n => (d.inputs || []).find(i => i.name === n)).filter(Boolean) || []).map(i => ({ f: f.name, i })));
      const all = [...(d.inputs || []).map(i => ({ f: '', i })), ...(d.forms || []).flatMap(f => (f.inputs || []).map(i => ({ f: f.name, i })))];
      all.filter(x => x.i.lookup).forEach(x => { (x.i.lookup_where || []).forEach(wh => { const k = wh.split('=')[0].trim(), ps = pluralSet(k), rec = sets[x.i.lookup] = sets[x.i.lookup] || { set: x.i.lookup, uses: [], depends: [] }; rec.depends = [...new Set([...rec.depends, k])]; (sets[ps] = sets[ps] || { set: ps, uses: [], depends: [] }).uses.push((rec.set) + ' › ' + k + ' attribute'); }); (sets[x.i.lookup] = sets[x.i.lookup] || { set: x.i.lookup, uses: [], depends: [] }).uses.push((d.title || d.name) + ' › ' + (x.f ? x.f + '.' : '') + x.i.name); if ((x.i.depends_on || []).length) sets[x.i.lookup].depends = [...new Set([...sets[x.i.lookup].depends, ...x.i.depends_on.map(z => z.split('.').pop())])]; });
    }
    S.wsSets = Object.values(sets); S.wsFlows = flows;
  }
  S.ws = S.ws || {}; const w = S.ws;
  if (!w.unit || !tree.find(n => n.id === w.unit)) w.unit = (tree.find(n => n.level === 'facility') || tree[0]).id;
  w.tab = w.tab || 'facilities'; w.set = w.set || (S.wsSets[0] && S.wsSets[0].set);
  await renderWorkspace();
}
async function renderWorkspace() {
  const w = S.ws, tree = S.wsTree, unitNode = tree.find(n => n.id === w.unit);
  const pick = h('select', { id: 'ws-unit', 'aria-label': 'Workspace', onchange: e => { w.unit = e.target.value; w.entries = null; w.settings = null; renderWorkspace(); } },
    tree.map(n => { const o = h('option', { value: n.id }, '  '.repeat(n.depth) + n.name + ' (' + n.level + ')'); o.selected = n.id === w.unit; return o; }));
  const tabs = h('nav', { class: 'tabs-bar' }, [['facilities', 'Facilities'], ['people', 'People'], ['data', 'Reference data'], ['settings', 'Settings']].map(([k, l]) => h('a', { href: '#', class: w.tab === k ? 'on' : '', onclick: e => { e.preventDefault(); w.tab = k; renderWorkspace(); } }, l)));
  let body;
  try { body = w.tab === 'facilities' ? await facilitiesPanel(unitNode) : w.tab === 'people' ? await peoplePanel(unitNode) : w.tab === 'data' ? await refDataPanel(unitNode) : await settingsPanel(unitNode); }
  catch (e) { body = panel('', h('div', { class: 'banner error' }, e.message)); }
  screen({ title: 'Workspace', num: unitNode.name, sub: 'Providers, nurses, locations, codes and settings that belong to this workspace. Departments inherit what their facility defines.', tabs, actions: h('div', { style: 'min-width:300px' }, pick) }, body);
}

// ---------- facilities: add a facility or department, optionally starting from another one's data ----------
const LEVEL_BELOW = { organization: 'facility', facility: 'department' };
const slug = t => String(t || '').toLowerCase().replace(/[^a-z0-9]+/g, '_').replace(/^_+|_+$/g, '').slice(0, 40);
// ---------- people: who has an account, and inviting someone into a workspace with a role ----------
async function peoplePanel(unitNode) {
  const w = S.ws, tree = S.wsTree;
  const roleSet = new Set(['admin']); (S.wsFlows || []).forEach(f => (f.stages || []).forEach(st => (st.roles || []).forEach(r => roleSet.add(r))));
  const roles = [...roleSet];
  let people = [], listErr = '';
  try { people = await api('GET', '/people') || []; } catch (e) { listErr = e.status === 403 ? 'Only an administrator can see who has an account.' : e.message; }
  const f = {
    email: h('input', { type: 'email', id: 'iv-email', required: '', placeholder: 'person@hospital.org', autocomplete: 'off' }),
    name: h('input', { type: 'text', id: 'iv-name', placeholder: 'Full name (optional)', autocomplete: 'off' }),
    role: h('select', { id: 'iv-role', 'aria-label': 'Role' }, roles.map(r => h('option', { value: r }, r))),
    unit: h('select', { id: 'iv-unit', 'aria-label': 'Workspace' }, tree.map(n => { const o = h('option', { value: n.id }, '  '.repeat(n.depth) + n.name + ' (' + n.level + ')'); o.selected = n.id === w.unit; return o; }))
  };
  const invite = async e => {
    e.preventDefault();
    try {
      const r = await api('POST', '/people/invite', { email: f.email.value.trim(), name: f.name.value.trim(), roles: [f.role.value], org_unit: f.unit.value });
      const url = location.origin + '/accept?token=' + encodeURIComponent(r.token);
      S.notice = { kind: 'ok', text: 'Invitation created for ' + r.email + '.' }; await renderWorkspace();
      const go = await ui.prompt({ title: 'Send this link to ' + r.email, message: 'They open it once to choose a password, then sign in as ' + f.role.value + ' at ' + wsName(f.unit.value) + '. The link expires ' + when(r.expires_at) + '. It is shown only now.', value: url, readonly: true, confirmLabel: 'Copy link', cancelLabel: 'Close' });
      if (go !== null) { try { await navigator.clipboard.writeText(url); } catch (er) { /* the link was shown above */ } }
    } catch (er) { S.notice = { kind: 'error', text: er.message }; await renderWorkspace(); }
  };
  const rows = people.map(m => h('tr', null, h('td', null, h('strong', null, m.name || m.email || m.user_id), h('div', { class: 'small muted' }, m.email || '')),
    h('td', null, [].concat(m.roles || []).map(r => badge('', r))), h('td', null, m.org_unit ? wsName(m.org_unit) : 'Whole group'), h('td', null, badge(m.status === 'active' ? 'active' : m.status === 'suspended' ? 'failed' : 'returned', m.status || 'active'))));
  return h('div', { class: 'split' },
    h('div', { class: 'col' }, panel('People with an account', listErr ? h('div', { class: 'banner error' }, listErr) :
      rows.length ? h('div', { class: 'tbl-wrap' }, h('table', null, h('thead', null, h('tr', null, ['Person', 'Role', 'Workspace', 'Status'].map(t => h('th', { scope: 'col' }, t)))), h('tbody', null, rows))) : h('div', { class: 'empty' }, h('b', null, 'No one yet'), 'Invite the first person.'), { flush: !listErr })),
    h('div', { class: 'col' }, panel('Invite someone', h('form', { class: 'stack', onsubmit: invite },
      h('label', null, 'Email', f.email), h('label', null, 'Name', f.name), h('label', null, 'Role', f.role), h('label', null, 'Works in', f.unit),
      h('p', { class: 'muted' }, 'The role decides which steps they can work: for example a nurse records encounters, a coder assigns CPT codes, a supervisor signs off, an admin manages the workspace. You can invite only into workspaces you manage.'),
      h('button', { type: 'submit', class: 'primary' }, 'Create invitation')))));
}

// ---------- accept an invitation: choose a password ----------
function showAccept() {
  const tk = new URLSearchParams(location.search).get('token') || '';
  if (!tk) return screen({ title: 'Join' }, panel('', h('div', { class: 'empty' }, h('b', null, 'This invitation link is incomplete'), 'Ask the person who invited you to send it again.')));
  const name = h('input', { type: 'text', id: 'ac-name', autocomplete: 'name', placeholder: 'Your full name' });
  const pw = h('input', { type: 'password', id: 'ac-pw', autocomplete: 'new-password', required: '', minlength: '10' });
  const pw2 = h('input', { type: 'password', id: 'ac-pw2', autocomplete: 'new-password', required: '' });
  const err = h('div', { role: 'alert' });
  const form = h('form', { class: 'stack', onsubmit: async e => {
    e.preventDefault(); err.replaceChildren();
    if (pw.value !== pw2.value) return err.append(h('div', { class: 'banner error' }, 'The two passwords are not the same.'));
    try {
      const r = await api('POST', '/people/accept', { token: tk, password: pw.value, name: name.value.trim() }, undefined, true);
      screen({ title: 'You are in' }, panel('', h('div', { class: 'stack' }, h('div', { class: 'banner ok' }, 'Your account is ready' + (r && r.email ? ' for ' + r.email : '') + '.'), h('div', null, h('button', { type: 'button', class: 'primary', onclick: () => signIn({ title: 'Sign in to your new account' }) }, 'Sign in')))));
      signIn({ title: 'Sign in to your new account' });
    } catch (er) { err.append(h('div', { class: 'banner error' }, /expired|invalid|used|not valid/i.test(er.message) ? er.message + ' Ask the person who invited you for a new link.' : er.message)); }
  } }, h('label', { for: 'ac-name' }, 'Name', name), h('label', { for: 'ac-pw' }, 'Choose a password (at least 10 characters)', pw), h('label', { for: 'ac-pw2' }, 'Repeat the password', pw2), err,
    h('div', null, h('button', { type: 'submit', class: 'primary' }, 'Create my account')));
  screen({ title: 'Join', sub: 'Choose a password to finish setting up your account.' }, panel('Your account', form));
}
async function facilitiesPanel(unitNode) {
  const w = S.ws, tree = S.wsTree;
  const done = async (text, kind) => { S.wsTree = null; S.notice = { kind: kind || 'ok', text }; w.entries = null; await workspaceTree(); await renderWorkspace(); };
  const fail = async e => { S.notice = { kind: 'error', text: e.message }; await renderWorkspace(); };
  const parents = tree.filter(n => LEVEL_BELOW[n.level]);
  const defParent = (parents.find(n => n.level === 'organization') || parents[0] || {}).id;
  const f = {
    name: h('input', { type: 'text', id: 'nf-name', placeholder: 'e.g. Lakeside Medical Center' }),
    id: h('input', { type: 'text', id: 'nf-id', placeholder: 'code (from the name)', class: 'mono' }),
    parent: h('select', { id: 'nf-parent', 'aria-label': 'Part of' }, parents.map(n => { const o = h('option', { value: n.id }, '  '.repeat(n.depth) + n.name + ' (' + n.level + ')'); o.selected = n.id === defParent; return o; })),
    copy: h('select', { id: 'nf-copy', 'aria-label': 'Start from' }, [h('option', { value: '' }, 'Start empty (inherit only)'), ...tree.map(n => h('option', { value: n.id }, 'Copy data of ' + n.name))])
  };
  f.name.addEventListener('input', () => { if (!f.id.dataset.touched) f.id.value = slug(f.name.value); });
  f.id.addEventListener('input', () => { f.id.dataset.touched = '1'; });
  const create = async e => {
    e.preventDefault();
    const name = f.name.value.trim(), id = slug(f.id.value || f.name.value), parent = tree.find(n => n.id === f.parent.value);
    if (!name || !id) return fail(new Error('A name is required.'));
    if (!parent) return fail(new Error('Choose where it belongs.'));
    if (tree.some(n => n.id === id)) return fail(new Error('The code "' + id + '" is already used.'));
    try {
      await api('POST', '/workspaces/save', { id, name, parent_id: parent.id, level: LEVEL_BELOW[parent.level] });
      let copied = 0;
      if (f.copy.value) for (const set of S.wsSets) {
        const rows = await api('POST', '/workspace/lookup', { set: set.set, node: f.copy.value }) || [];
        for (const r of rows.filter(r => r.source === f.copy.value)) {
          await api('POST', '/workspace/lookup/save', { set: set.set, node_id: id, code: r.code, label: r.label, sort: r.sort || 0, attrs: r.attrs || {} }); copied++;
        }
      }
      w.unit = id; w.tab = 'data';
      await done(name + ' added' + (copied ? ' with ' + copied + ' reference entries copied.' : '. It inherits everything its parent defines; add its own providers, nurses and locations next.'));
    } catch (er) { await fail(er); }
  };
  const remove = n => async () => {
    if (!await ui.confirm({ title: 'Remove ' + n.name + '?', message: 'Its own providers, nurses, locations and codes are removed with it. Cases already recorded keep their data.', confirmLabel: 'Remove', danger: true })) return;
    try { await api('POST', '/workspaces/delete', { id: n.id }); if (w.unit === n.id) w.unit = n.parent || null; await done(n.name + ' removed.'); } catch (er) { await fail(er); }
  };
  const rows = tree.map(n => h('tr', null,
    h('td', null, h('span', { style: 'padding-left:' + n.depth * 18 + 'px' }, h('strong', null, n.name))), h('td', null, badge('', n.level)), h('td', null, h('span', { class: 'mono' }, n.id)),
    h('td', null, h('div', { class: 'row' },
      h('button', { type: 'button', onclick: () => { S.ws.unit = n.id; S.ws.tab = 'data'; S.ws.entries = null; renderWorkspace(); } }, 'Data'),
      n.level !== 'organization' ? h('button', { type: 'button', class: 'danger', onclick: remove(n) }, 'Remove') : null))));
  return h('div', { class: 'split' },
    h('div', { class: 'col' }, panel('Facilities and departments', h('div', { class: 'tbl-wrap' }, h('table', null, h('thead', null, h('tr', null, ['Name', 'Level', 'Code', ''].map(t => h('th', { scope: 'col' }, t)))), h('tbody', null, rows))), { flush: true })),
    h('div', { class: 'col' }, panel('Add a facility or department',
      h('form', { class: 'stack', onsubmit: create },
        h('label', null, 'Name', f.name), h('label', null, 'Code', f.id), h('label', null, 'Belongs to', f.parent), h('label', null, 'Reference data', f.copy),
        h('p', { class: 'muted' }, 'A facility inherits the group\'s specialties, claim types, provider types and CPT codes. Add its own providers (physician, resident, billing, secondary), nurses and locations under Reference data, and switch off anything it does not use.'),
        h('button', { type: 'submit', class: 'primary' }, 'Add')))));
}
async function refDataPanel(unitNode) {
  const w = S.ws, key = w.unit + '/' + w.set;
  if (!w.entries || w.entriesKey !== key) { w.entries = await api('POST', '/workspace/lookup', { set: w.set, node: w.unit }) || []; w.entriesKey = key; }
  const rows = w.entries, cur = S.wsSets.find(s => s.set === w.set) || { uses: [], depends: [] };
  const act = async (path, body, msg) => { try { await api('POST', path, body); w.entries = null; S.notice = { kind: 'ok', text: msg }; await renderWorkspace(); } catch (e) { S.notice = { kind: 'error', text: e.message }; await renderWorkspace(); } };
  const base = r => ({ set: w.set, code: r.code, label: r.label, sort: r.sort || 0, attrs: Object.fromEntries(Object.entries(r.attrs || {}).filter(([k]) => k !== 'disabled')) });
  const line = r => {
    const off = r.attrs && r.attrs.disabled === true, own = r.source === w.unit, from = own ? 'this workspace' : r.source ? 'inherited from ' + wsName(r.source) : 'whole group';
    return h('tr', { style: off ? 'opacity:.55' : '' }, h('td', null, h('strong', { class: 'mono' }, r.code)), h('td', null, r.label), h('td', { class: 'small' }, attrText(r.attrs)),
      h('td', null, own ? badge('active', 'own') : badge('', from)), h('td', null, off ? badge('failed', 'switched off') : badge('completed', 'in use')),
      h('td', null, h('div', { class: 'row' },
        off && own ? h('button', { type: 'button', onclick: () => act('/workspace/lookup/delete', { set: w.set, code: r.code, node_id: w.unit }, r.code + ' switched on again.') }, 'Switch on') : null,
        !off ? h('button', { type: 'button', class: 'danger', onclick: () => act('/workspace/lookup/save', { ...base(r), node_id: w.unit, disabled: true }, r.code + ' switched off here.') }, 'Switch off') : null,
        own && !off ? h('button', { type: 'button', class: 'ghost', onclick: () => act('/workspace/lookup/delete', { set: w.set, code: r.code, node_id: w.unit }, r.code + ' removed.') }, 'Remove') : null)));
  };
  const f = { code: h('input', { type: 'text', placeholder: 'code (e.g. dr-lama)', id: 'ne-code' }), label: h('input', { type: 'text', placeholder: 'label shown on forms', id: 'ne-label' }),
    sort: h('input', { type: 'number', placeholder: 'order', id: 'ne-sort', style: 'max-width:110px' }), attrs: h('input', { type: 'text', placeholder: cur.depends.length ? 'e.g. ' + cur.depends[0] + '=CARD|GM' : 'attributes, e.g. key=a|b', id: 'ne-attrs' }) };
  // An attribute a form narrows this set by (specialty, provider_type...) is picked from the set of that name when there is one.
  if (w.srcUnit !== w.unit) { w.src = ((await api('POST', '/workspace/sources', { unit: w.unit }).catch(() => null)) || {}).sets || []; w.srcUnit = w.unit; }
  const pickers = {};
  for (const k of cur.depends) {
    const ps = pluralSet(k);
    if (!S.wsSets.find(x => x.set === ps)) continue;
    const dsx = (w.src || []).find(x => x.set === ps);
    const opts = dsx ? dsx.entries : await api('POST', '/workspace/lookup', { set: ps, node: w.unit }).catch(() => []) || [];
    pickers[k] = h('select', { multiple: true, size: Math.min(4, Math.max(2, opts.length)), 'aria-label': k, title: k + ' (hold Ctrl/Cmd for several)' },
      opts.filter(o => !(o.attrs && o.attrs.disabled === true)).map(o => h('option', { value: o.code }, o.label || o.code)));
  }
  const free = cur.depends.filter(k => !pickers[k]);
  f.attrs.placeholder = free.length ? 'e.g. ' + free[0] + '=a|b' : 'other attributes, e.g. key=a|b';
  const collect = () => { const a = parseAttrs(f.attrs.value); Object.entries(pickers).forEach(([k, el]) => { const v = [...el.selectedOptions].map(o => o.value); if (v.length) a[k] = v; }); return a; };
  const add = h('form', { class: 'row', onsubmit: e => { e.preventDefault(); if (!f.code.value.trim() || !f.label.value.trim()) { S.notice = { kind: 'error', text: 'A code and a label are required.' }; return renderWorkspace(); }
    act('/workspace/lookup/save', { set: w.set, node_id: w.unit, code: f.code.value.trim(), label: f.label.value.trim(), sort: Number(f.sort.value) || 0, attrs: collect() }, f.code.value.trim() + ' added to ' + unitNode.name + '.'); } },
    f.code, f.label, f.sort, ...Object.entries(pickers).map(([k, el]) => h('label', { class: 'small' }, k.replace(/_/g, ' '), h('br'), el)), f.attrs, h('button', { type: 'submit', class: 'primary' }, 'Add to ' + unitNode.name));
  const setList = panel('Reference sets', h('div', { class: 'stack' }, S.wsSets.map(s => h('button', { type: 'button', class: w.set === s.set ? 'primary' : '', style: 'justify-content:flex-start', onclick: () => { w.set = s.set; w.entries = null; renderWorkspace(); } }, s.set))));
  const main = [panel(w.set + ' for ' + unitNode.name, [h('p', { class: 'muted' }, 'Used by ' + (cur.uses.join(', ') || 'no form') + (cur.depends.length ? '. Narrowed by ' + cur.depends.join(', ') + ' (give an entry that attribute).' : '.')),
    rows.length ? h('div', { class: 'tbl-wrap' }, h('table', null, h('thead', null, h('tr', null, ['Code', 'Label', 'Attributes', 'Defined at', 'State', ''].map(t => h('th', { scope: 'col' }, t)))), h('tbody', null, rows.map(line)))) : h('div', { class: 'empty' }, h('b', null, 'Nothing here yet'), 'Add the first entry below.')], { flush: true }),
    panel('Add or replace an entry', [h('p', { class: 'muted' }, 'An entry with the same code replaces the inherited one for this workspace and everything below it.'), add])];
  // A set the data source behind the workflows serves (a table or an API): its rows are listed with where
  // each is defined; when the source declares upsert/remove statements an admin can add and remove rows
  // of this workspace, otherwise the set is read-only here.
  const ds = (w.src || []).find(x => x.set === w.set);
  let shown = main;
  if (ds) {
    const actSrc = async (path, body, msg) => { try { await api('POST', path, body); w.srcUnit = null; S.notice = { kind: 'ok', text: msg }; } catch (e) { S.notice = { kind: 'error', text: e.message }; } await renderWorkspace(); };
    const dsLine = o => { const own = o.source === w.unit, from = !o.source ? 'data source' : own ? 'this workspace' : 'inherited from ' + wsName(o.source);
      return h('tr', null, h('td', null, h('strong', { class: 'mono' }, o.code)), h('td', null, o.label), h('td', { class: 'small' }, attrText(o.attrs)), h('td', null, own ? badge('active', 'own') : badge('', from)),
        h('td', null, ds.writable && own ? h('button', { type: 'button', class: 'danger', onclick: () => actSrc('/workspace/source/delete', { set: ds.set, unit: w.unit, code: o.code }, o.code + ' removed.') }, 'Remove') : null)); };
    const parts = [
      h('p', { class: 'muted' }, 'Read from a database or API, not from BCL.' + (ds.writable ? ' You can add and remove rows of ' + unitNode.name + '; rows defined higher up are inherited.' : ' It is read-only here: change it at the source.') + (ds.merge ? ' Offered together with this workspace\'s own entries.' : '')),
      ds.error ? h('div', { class: 'banner error' }, ds.error) : null,
      ds.entries.length ? h('div', { class: 'tbl-wrap' }, h('table', null, h('thead', null, h('tr', null, ['Code', 'Label', 'Attributes', 'Defined at', ''].map(t => h('th', { scope: 'col' }, t)))), h('tbody', null, ds.entries.map(dsLine)))) : h('div', { class: 'empty' }, h('b', null, 'No rows for this workspace'), ds.writable ? 'Add the first one below.' : '')];
    const dsPanel = panel(w.set + ' for ' + unitNode.name + ' (' + ds.kind + ' data source)', parts, { flush: !ds.error });
    const addSrc = ds.writable ? panel('Add or replace a row', [h('p', { class: 'muted' }, 'A row with the same code in this workspace is replaced.'),
      h('form', { class: 'row', onsubmit: e => { e.preventDefault(); if (!f.code.value.trim() || !f.label.value.trim()) { S.notice = { kind: 'error', text: 'A code and a label are required.' }; return renderWorkspace(); }
        actSrc('/workspace/source/save', { set: ds.set, unit: w.unit, code: f.code.value.trim(), label: f.label.value.trim(), sort: Number(f.sort.value) || 0, attrs: collect() }, f.code.value.trim() + ' saved for ' + unitNode.name + '.'); } },
        f.code, f.label, f.sort, ...Object.entries(pickers).map(([k, el]) => h('label', { class: 'small' }, k.replace(/_/g, ' '), h('br'), el)), f.attrs, h('button', { type: 'submit', class: 'primary' }, 'Save to ' + unitNode.name))]) : null;
    shown = ds.merge ? [dsPanel, addSrc, ...main].filter(Boolean) : [dsPanel, addSrc].filter(Boolean);
  }
  return h('div', { class: 'split' }, h('div', { class: 'col' }, shown), h('div', { class: 'col' }, setList));
}
async function settingsPanel(unitNode) {
  const w = S.ws; w.flow = w.flow || (S.wsFlows[0] && S.wsFlows[0].name);
  const flow = S.wsFlows.find(x => x.name === w.flow);
  if (!flow) return panel('', h('div', { class: 'empty' }, h('b', null, 'No workflow runs inside workspaces yet')));
  const key = w.unit + '/' + w.flow;
  if (!w.settings || w.settingsKey !== key) { w.settings = await api('POST', '/workspace/settings', { pipeline: w.flow, unit: w.unit }) || {}; w.settingsKey = key; }
  const own = w.settings.own || {}, eff = w.settings.effective || {};
  const st = JSON.parse(JSON.stringify({ stages: own.stages || {}, fields: own.fields || {} }));
  const stageRows = flow.stages.map(s => { const o = st.stages[s.name] = st.stages[s.name] || {}, e = (eff.stages || {})[s.name] || {};
    const inp = (k, ph) => h('input', { type: 'text', value: o[k] || '', placeholder: e[k] ? e[k] + ' (inherited)' : ph, oninput: ev => { o[k] = ev.target.value.trim(); }, style: 'max-width:150px' });
    return h('tr', null, h('td', null, h('strong', null, s.title || s.name)), h('td', null, inp('due', 'e.g. 12h')), h('td', null, inp('sla_duration', 'e.g. 6h')), h('td', null, inp('sla_warn_before', 'e.g. 1h'))); });
  const fields = flow.forms.flatMap(f => (f.inputs || []).map(i => ({ path: f.name + '.' + i.name, label: i.label || i.name })));
  const fieldRows = fields.map(fl => { const o = st.fields[fl.path] = st.fields[fl.path] || {};
    return h('tr', null, h('td', null, h('strong', null, fl.label), h('div', { class: 'small mono' }, fl.path)), h('td', null, h('input', { type: 'text', value: o.label || '', placeholder: 'relabel for this workspace', oninput: ev => { o.label = ev.target.value; } })),
      h('td', null, ['required', 'hidden', 'disabled'].map(k => h('label', { class: 'check', style: 'display:inline-flex;margin-right:12px' }, h('input', { type: 'checkbox', checked: o[k] === true, onchange: ev => { o[k] = ev.target.checked ? true : undefined; } }), k)))); });
  const clean = () => { const out = { stages: {}, fields: {} };
    Object.entries(st.stages).forEach(([k, v]) => { const x = Object.fromEntries(Object.entries(v).filter(([, y]) => y)); if (Object.keys(x).length) out.stages[k] = x; });
    Object.entries(st.fields).forEach(([k, v]) => { const x = Object.fromEntries(Object.entries(v).filter(([, y]) => y)); if (Object.keys(x).length) out.fields[k] = x; }); return out; };
  const save = async () => { try { await api('POST', '/workspace/settings/save', { pipeline: w.flow, unit: w.unit, overrides: clean() }); w.settings = null; S.notice = { kind: 'ok', text: 'Settings saved for ' + unitNode.name + '.' }; await renderWorkspace(); } catch (e) { S.notice = { kind: 'error', text: e.message }; await renderWorkspace(); } };
  const flowSel = h('select', { 'aria-label': 'Workflow', onchange: e => { w.flow = e.target.value; w.settings = null; renderWorkspace(); } }, S.wsFlows.map(x => { const o = h('option', { value: x.name }, x.title); o.selected = x.name === w.flow; return o; }));
  return h('div', { class: 'col' },
    panel('Workflow', [h('div', { class: 'row' }, flowSel), h('p', { class: 'muted', style: 'margin:10px 0 0' }, 'Settings tune this workflow for ' + unitNode.name + ' and everything below it. They narrow and tune; they never grant access, and an input the workflow requires cannot be hidden.')]),
    panel('Stage deadlines', h('div', { class: 'tbl-wrap' }, h('table', null, h('thead', null, h('tr', null, ['Stage', 'Due', 'SLA duration', 'Warn before'].map(t => h('th', { scope: 'col' }, t)))), h('tbody', null, stageRows))), { flush: true }),
    panel('Fields', h('div', { class: 'tbl-wrap' }, h('table', null, h('thead', null, h('tr', null, ['Field', 'Label here', 'Behaviour'].map(t => h('th', { scope: 'col' }, t)))), h('tbody', null, fieldRows))), { flush: true }),
    h('div', { class: 'row' }, h('button', { type: 'button', class: 'primary big', onclick: save }, 'Save settings for ' + unitNode.name)));
}

renderIdent();
route();
