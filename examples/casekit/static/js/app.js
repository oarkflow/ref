
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
  const diff = d - Date.now(), m = Math.round(Math.abs(diff) / 60000);
  const dd = Math.floor(m / 1440), hh = Math.floor((m % 1440) / 60), mm = m % 60;
  const s = dd ? dd + 'd ' + hh + 'h' : hh ? hh + 'h ' + mm + 'm' : mm + 'm';
  return diff < 0 ? s + ' ago' : 'in ' + s;
}
const relEl = t => h('span', { 'data-rel': t }, rel(t));
setInterval(() => document.querySelectorAll('[data-rel]').forEach(e => { e.textContent = rel(e.getAttribute('data-rel')); }), 30000);
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
  try { const p = token.split('.')[1]; return JSON.parse(atob(p.replace(/-/g, '+').replace(/_/g, '/'))); } catch (e) { return null; }
}
function renderIdent() {
  const c = claims(), who = document.getElementById('who');
  const name = !token ? 'Applicant' : c ? String(c.sub || '?').replace(/^demo-/, '') : 'Signed in', roles = c && c.roles && c.roles.length && [].concat(c.roles).join(', ') !== name ? ' · ' + [].concat(c.roles).join(', ') : '';
  who.replaceChildren(h('i', null, name.charAt(0)), name + roles);
  document.getElementById('tokClear').hidden = !token;
  document.querySelectorAll('.top nav a').forEach(a => { const p = a.getAttribute('href'); if (p === location.pathname || (p !== '/' && location.pathname.startsWith(p)) || (p === '/work' && location.pathname.startsWith('/tasks'))) a.setAttribute('aria-current', 'page'); else a.removeAttribute('aria-current'); });
}
function setToken(t) { token = t || ''; store('casekit.token', token || null); renderIdent(); reloadRoute(); }
document.getElementById('tokSave').addEventListener('click', () => { const i = document.getElementById('tok'); setToken(i.value.trim()); i.value = ''; });
document.getElementById('tokClear').addEventListener('click', () => setToken(''));
// Demo servers (-demo) offer one-click staff sign-in; other servers return 404.
fetch('/demo/tokens', { headers: { Accept: 'application/json' } }).then(r => r.ok ? r.json() : null).then(t => {
  if (!t) return;
  window.DEMO = t;
  const box = document.getElementById('demoBtns');
  Object.keys(t).forEach(role => { const b = h('button', { type: 'button' }, role); b.addEventListener('click', () => { token = t[role]; store('casekit.token', token); nav('/work'); }); box.appendChild(b); });
  document.getElementById('demo').hidden = false;
}).catch(() => {});

// ---------- state ----------
const S = { view: null, vals: {}, reps: {}, dirty: new Set(), errs: {}, notice: null, flash: null, steps: {}, verdicts: {}, nodeIn: {}, comment: '', busy: false, key: '', acks: {}, review: null, panel: '' };
function page(...kids) { app.replaceChildren(...kids.flat().filter(Boolean)); }
function notice() {
  const n = S.notice; if (!n) return null;
  return h('div', { class: 'banner ' + n.kind, role: n.kind === 'error' ? 'alert' : 'status' }, n.text, n.list && n.list.length ? h('ul', null, n.list.map(t => h('li', null, t))) : null);
}
const errList = list => (list || []).map(m => h('div', { class: 'err', role: 'alert' }, m));

// ---------- router ----------
// Pages are server-rendered SPL templates. This script only brings the interactive ones to life.
const nav = to => location.assign(to);
const pageKind = () => { const p = location.pathname; return p === '/' ? 'home' : p.startsWith('/start/') ? 'intro' : p.startsWith('/cases/') ? 'overview' : p.startsWith('/tasks/') ? 'task' : p.startsWith('/links/') ? 'link' : p === '/work' ? 'queue' : ''; };
const bounce = (kind, text, to) => { S.flash = { kind, text }; try { sessionStorage.setItem('casekit.flash', JSON.stringify(S.flash)); } catch (e) { /* ignore */ } nav(to); };
async function route() {
  const kind = pageKind();
  try { const f = sessionStorage.getItem('casekit.flash'); if (f) { S.notice = JSON.parse(f); sessionStorage.removeItem('casekit.flash'); } } catch (e) { /* ignore */ }
  try {
    const parts = location.pathname.split('/').filter(Boolean).map(decodeURIComponent);
    if (kind === 'overview') await showOverview(parts[1]);
    else if (kind === 'task') await showTask(parts[1], parts[2] || '');
    else if (kind === 'link') await showLink(parts[1]);
    else if (kind === 'queue') await showQueue(new URLSearchParams(location.search).get('scope') || 'queue');
    else if (kind === 'home') homeInit();
    else if (kind === 'intro') introInit();
  } catch (e) {
    page(h('h1', null, 'Something went wrong'), h('div', { class: 'banner error', role: 'alert' }, e.message),
      e.status === 401 || e.status === 403 || e.status === 404 ? h('p', { class: 'muted' }, 'You may need to sign in, or this case needs its access key.') : null,
      h('a', { class: 'btn', href: '/' }, 'Back home'));
  }
}
// Sign-in changes what a page shows, so reload it (the server-rendered pages are the same for everyone).
function reloadRoute() { if (['overview', 'task', 'queue'].includes(pageKind())) route(); else location.reload(); }

// The home page lists this device's applications and takes a case id to resume.
function homeInit() {
  const mine = document.getElementById('mine'), ids = Object.keys(keys);
  if (mine && ids.length) { mine.hidden = false; const ul = mine.querySelector('ul'); ids.forEach(id => ul.append(h('li', null, h('a', { href: '/cases/' + encodeURIComponent(id) }, id)))); }
  const f = document.getElementById('resume');
  if (f) f.addEventListener('submit', e => { e.preventDefault(); const id = f.elements.id.value.trim(), k = f.elements.key.value.trim(); if (!id) return; if (k) saveKey(id, k); nav('/cases/' + encodeURIComponent(id)); });
  if (S.notice) { const n = notice(); if (n) app.prepend(n); }
}
// The intro page's button starts a case.
function introInit() {
  const btn = document.querySelector('[data-start]'); if (!btn) return;
  btn.addEventListener('click', async () => {
    btn.disabled = true;
    try {
      const name = btn.dataset.start, v = await api('POST', '/pipelines/' + encodeURIComponent(name) + '/cases', { data: {} });
      if (v.access_key) saveKey(v.case.id, v.access_key);
      bounce('ok', v.access_key ? 'Started ' + v.case.number + '. It is remembered on this device; keep your case ID ' + v.case.id + ' and access key ' + v.access_key + ' to return from elsewhere.' : 'Started ' + v.case.number + '.', '/cases/' + encodeURIComponent(v.case.id));
    } catch (e) { btn.disabled = false; alert(e.message); }
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
    case 'select': return h('select', { ...base, placeholder: null, onchange: e => set(e.target.value || null) }, h('option', { value: '' }, inp.placeholder || 'Choose…'),
      choices.map(c => { const o = h('option', { value: String(c.value) }, c.label || String(c.value)); o.selected = String(c.value) === str; return o; }));
    case 'radio': case 'multiselect': {
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

function fieldEl(inp, path, val, editable, set) {
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
  const cls = 'field' + (inp.flag ? ' flagged' : '') + (errs ? ' invalid' : '');
  if (!editable) return h('div', { class: cls, style, 'data-path': path }, h('div', { class: 'lbl' }, inp.label, calcBadge(inp)),
    h('div', { class: 'value' }, shown(val, inp), inp.masked && !inp.sealed ? h('span', { class: 'muted' }, ' (masked)') : null), inp.help ? help : null, extras);
  const ctl = control(inp, path, val, set);
  if (inp.kind === 'boolean') return h('div', { class: cls, style, 'data-path': path }, h('label', { class: 'check', for: id }, ctl, labelKids), help, extras);
  const lbl = ['radio', 'multiselect'].includes(inp.kind) ? h('div', { class: 'lbl', id: id + '_l' }, labelKids) : h('label', { for: id }, labelKids);
  return h('div', { class: cls, style, 'data-path': path }, lbl, ctl, help, extras);
}

function formEl(f, g) {
  const cols = Math.max(1, parseInt(f.columns || g.columns || 1, 10) || 1), editableGroup = g.mode === 'editable';
  const head = [f.title ? h('h3', null, f.title) : null, f.description ? h('p', { class: 'muted' }, f.description) : null];
  if (g.mode === 'summary') {
    const dl = get => h('dl', { class: 'summary' }, f.inputs.map(inp => [h('dt', null, inp.label), h('dd', null, shown(get(inp), inp), calcBadge(inp))]));
    if (f.repeatable) return h('div', { class: 'form' }, head, (S.reps[f.name] || []).map(e => h('div', { class: 'entry' }, dl(inp => e[inp.name]))));
    return h('div', { class: 'form' }, head, dl(inp => S.vals[inp.path]));
  }
  if (!f.repeatable) return h('div', { class: 'form' }, head, h('div', { class: 'grid', style: '--cols:' + cols },
    f.inputs.map(inp => fieldEl(inp, inp.path, S.vals[inp.path], editableGroup && inp.editable, v => { S.vals[inp.path] = v; S.dirty.add(inp.path); }))));
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
      h('div', { class: 'grid', style: '--cols:' + cols },
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
  S.notice = { kind: 'error', text: e.status === 409 ? e.message + ' Reload the page to get the latest version.' : e.message,
    list: e.details.map(d => (d.path && d.path !== 'comment' ? labelFor(d.path) + ': ' : '') + d.message) };
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
  S.busy = false; renderTask();
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
    if (['waive', 'reject', 'fail'].includes(op) && !window.confirm(cap(op) + ' "' + (n.title || n.name) + '"?')) return;
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
const person = id => id ? String(id).replace(/^demo-/, '').replace(/^anon_.*/, 'applicant') : '—';
const stageStatus = s => String(s || '').replace(/_/g, ' ');
// A deadline meter: how much of the time allowed has gone.
function slaMeter(w, stage) {
  const sla = w && w.sla; if (!sla || !stage.due_at) return null;
  const start = new Date(sla.started_at).getTime(), due = new Date(sla.due_at).getTime(), now = Date.now();
  const pct = Math.max(2, Math.min(100, Math.round(((now - start) / Math.max(1, due - start)) * 100)));
  const late = now > due, label = sla.status === 'breached' || late ? 'Overdue by ' + fmtDur((now - due) / 1000) : fmtDur((due - now) / 1000) + ' left';
  return h('div', { class: 'sla ' + (late ? 'breached' : sla.status) }, h('div', { class: 'bar-track' }, h('div', { class: 'bar-fill', style: 'width:' + pct + '%' })),
    h('div', { class: 'meta' }, h('span', null, label), h('span', null, 'Due ' + short(sla.due_at))));
}
const slaBadge = (w, stage) => { const s = w && w.sla; if (!s) return null; return s.status === 'breached' ? badge('failed', 'SLA missed') : s.status === 'warning' ? badge('warning', 'Due soon') : badge('on_track', 'On track'); };

// ---------- case overview: where it stands, the data, the log ----------
async function showOverview(id) {
  page(h('p', { class: 'muted' }, 'Loading…'));
  const ov = await api('GET', '/cases/' + encodeURIComponent(id) + '/overview', undefined, id);
  S.ov = ov; S.stand = ''; S.errs = {};
  S.def = null;
  if ((ov.work && ov.work.operations || []).some(o => ['assign', 'reset', 'extend', 'waive'].includes(o))) { try { S.def = await api('GET', '/pipelines/' + encodeURIComponent(ov.case.pipeline)); } catch (e) { /* the worker list is optional */ } }
  renderOverview();
}
function ovNext(ov) {
  const st = ov.stage, ops = (ov.work && ov.work.operations) || [];
  if (FINISHED.includes(ov.case.status)) return 'This case is ' + ov.case.status + '. Nothing more to do.';
  if (ov.work && ov.work.suspended) return 'On hold: ' + ov.work.suspended.reason + '.' + (ops.includes('resume') ? ' You can resume it below.' : '');
  if (ov.information) return ops.length ? 'Waiting for the applicant to answer: “' + ov.information.reason + '”' : 'A reviewer asked you a question. Please answer it below.';
  if (ov.party && ov.party.status !== 'submitted') return 'Waiting for ' + ov.party.party + ' to complete this step.' + (ov.party.status === 'expired' ? ' The link has expired; reissue it.' : '');
  if (ov.entries && ov.can_work) return ov.entries.can_complete ? 'Your entries are in. Review them and continue.' : 'Add your entry to continue.';
  if (ov.needs_start) return 'Ready to begin. Press Start to take this stage on; your name and the time are recorded.';
  if (ov.can_work) return 'Open the stage to continue.';
  return 'This stage is with ' + (st.started_by ? person(st.started_by) : ov.work && ov.work.assignee ? person(ov.work.assignee) : 'the team') + '. Nothing is needed from you right now.';
}
function renderOverview() {
  const ov = S.ov, c = ov.case, st = ov.stage, w = ov.work, cid = encodeURIComponent(c.id), ops = (w && w.operations) || [];
  const done = FINISHED.includes(c.status);
  const facts = [['Status', badge(c.status)], ['Stage', st.title || st.name], ['Stage status', badge(st.status)]];
  if (st.started_at) facts.push(['Started', short(st.started_at) + (st.started_by ? ' by ' + person(st.started_by) : '')]);
  if (w && w.assignee) facts.push(['Handled by', person(w.assignee)]);
  const primary = [];
  if (!done) {
    if (ov.needs_start) primary.push(h('button', { class: 'primary big', type: 'button', disabled: S.busy, onclick: () => startStage() }, 'Start ' + (st.title || st.name)));
    else if (ov.can_work) primary.push(h('a', { class: 'btn primary big', href: '/tasks/' + cid }, ov.entries ? (ov.entries.mine.length ? 'Open ' : 'Start ') + (st.title || 'entries') : 'Continue ' + (st.title || 'working')));
  }
  const nextCard = h('section', { class: 'card' },
    h('div', { class: 'card-head' }, h('h2', null, done ? 'Outcome' : 'Where it stands'), slaBadge(w, st), badge(st.status)),
    h('dl', { class: 'kv' }, facts.map(([k, v]) => [h('dt', null, k), h('dd', null, v)])),
    done ? null : h('div', { style: 'margin-top:14px' }, slaMeter(w, st)),
    h('p', { class: 'muted', style: 'margin:16px 0 12px' }, ovNext(ov)),
    primary.length ? h('div', { class: 'row' }, primary) : null,
    answerPanel(ov));
  // Summary of the data gathered so far, grouped by where it was asked.
  const groups = [], by = {};
  (ov.fields || []).forEach(f => { const k = f.stage + ' · ' + f.group; if (!by[k]) { by[k] = []; groups.push(k); } by[k].push(f); });
  const val = f => f.value == null ? '—' : typeof f.value === 'boolean' ? (f.value ? 'Yes' : 'No') : Array.isArray(f.value) ? f.value.map(x => typeof x === 'object' ? Object.values(x).join(' · ') : x).join('; ') : typeof f.value === 'object' ? (f.value.name || JSON.stringify(f.value)) : String(f.value);
  const summary = h('section', { class: 'card' }, h('div', { class: 'card-head' }, h('h2', null, 'Summary of data')),
    groups.length ? groups.map(k => h('div', null, h('div', { class: 'group-title' }, k), h('dl', { class: 'summary' }, by[k].map(f => [h('dt', null, f.label), h('dd', null, val(f), f.masked ? h('span', { class: 'muted' }, ' (masked)') : null)]))))
      : h('div', { class: 'empty' }, h('b', null, 'Nothing submitted yet'), 'Answers appear here as each stage is completed.'));
  const extra = [certCard(ov), partyCard(ov), entriesCard(ov)];
  page(h('a', { class: 'crumb', href: token ? '/work' : '/' }, token ? '← My work' : '← Home'),
    h('div', { class: 'page-head' }, h('div', null, h('h1', null, (c.title || 'Case') + ' ' + c.number), h('div', { class: 'sub' }, 'Opened ' + short(c.created_at) + (keys[c.id] ? ' · case ID ' : ''), keys[c.id] ? h('code', null, c.id) : null)),
      h('div', { class: 'actions' }, badge(c.status))),
    notice(),
    h('div', { class: 'layout' },
      h('div', null, standPanel(ov), nextCard, extra, summary, logCard(ov)),
      h('aside', null, h('section', { class: 'card' }, h('h2', null, 'Progress'), h('ol', { class: 'vsteps' }, (ov.progress || []).map((p, i) => h('li', { class: done && p.status === 'pending' ? 'skipped' : p.status },
        h('span', { class: 'n' }, p.status === 'completed' ? '✓' : String(i + 1)), h('a', { href: p.name === st.name && ov.can_work ? '/tasks/' + cid : '/cases/' + cid }, p.title || p.name),
        h('small', null, done && p.status === 'pending' ? 'skipped' : stageStatus(p.status)))))),
        moreCard(ov))));
}
async function startStage() {
  const ov = S.ov; S.busy = true;
  try { await api('POST', '/cases/' + encodeURIComponent(ov.case.id) + '/stages/' + encodeURIComponent(ov.stage.name) + '/work/start', {}, ov.case.id); nav('/tasks/' + encodeURIComponent(ov.case.id)); }
  catch (e) { S.busy = false; S.notice = { kind: 'error', text: e.message }; renderOverview(); }
}
// The log: who started each stage, how long it waited, how long it took.
function logCard(ov) {
  const rows = ov.log || [];
  const late = (ov.breaches || []);
  return h('section', { class: 'card' }, h('div', { class: 'card-head' }, h('h2', null, 'Activity log')),
    rows.length ? h('div', { class: 'tbl-wrap' }, h('table', null,
      h('thead', null, h('tr', null, ['Stage', 'Started by', 'Started', 'Waited', 'Worked', 'Result', 'Deadline'].map(t => h('th', { scope: 'col' }, t)))),
      h('tbody', null, rows.map(l => h('tr', null, h('td', null, h('strong', null, l.title)), h('td', null, l.started_by ? person(l.started_by) : (l.started_at ? 'applicant' : '—')),
        h('td', null, short(l.started_at)), h('td', { class: 'dur' }, fmtDur(l.wait_seconds)), h('td', { class: 'dur' }, l.started_at ? fmtDur(l.work_seconds) + (l.open ? ' so far' : '') : '—'),
        h('td', null, l.open ? badge('active', 'in progress') : badge(l.outcome === 'reset' ? 'returned' : l.outcome || 'completed', l.outcome === 'reset' ? 'reset' : null)),
        h('td', null, l.due_at ? (l.breached ? badge('failed', 'missed') : h('span', { class: 'muted' }, 'due ' + short(l.due_at))) : '—')))))) : h('div', { class: 'empty' }, 'No activity yet.'),
    late.length ? h('div', null, h('div', { class: 'group-title' }, 'Missed deadlines'), late.map(b => h('div', { class: 'banner ' + (b.resolution ? 'info' : 'error'), style: 'flex-direction:column;gap:2px' },
      h('strong', null, b.stage + ': due ' + short(b.due_at) + ' · ' + (b.resolution ? 'settled by ' + person(b.resolved_by) + ' (' + b.resolution + ')' : 'open')), b.reason,
      b.resolution_comment ? h('em', null, 'Note: ' + b.resolution_comment) : null))) : null);
}

// ---------- admin: take a stand on a missed deadline ----------
const STANDS = {
  reset: ['Reset the stage', 'Put the data and the start back to how it was when the stage opened, and restart the clock.'],
  assign: ['Assign to someone', 'Hand it to another person. Their SLA starts from now if you also extend.'],
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
  const form = cur ? h('div', { class: 'stack', style: 'margin-top:14px' }, h('strong', null, STANDS[cur][0]), h('p', { class: 'muted', style: 'margin:0' }, STANDS[cur][1]), sel, by, text, errList(S.errs.stand),
    h('div', { class: 'row' }, h('button', { class: 'primary', type: 'button', disabled: S.busy, onclick: go }, 'Confirm'), h('button', { type: 'button', onclick: () => { S.stand = ''; S.errs = {}; renderOverview(); } }, 'Cancel'))) : null;
  const opts = h('div', { class: 'opts-row' }, mine.map(o => h('button', { type: 'button', class: 'opt' + (cur === o ? ' active' : ''), onclick: () => { S.stand = o; S.errs = {}; renderOverview(); } }, h('span', null, STANDS[o][0]), h('small', null, STANDS[o][1]))));
  if (open) return h('section', { class: 'stand', role: 'alert' }, h('h2', null, '⚠ The deadline was missed'),
    h('p', { class: 'why' }, h('strong', null, 'Why: '), open.reason), h('p', { class: 'muted' }, 'Due ' + short(open.due_at) + ' · noticed ' + short(open.breached_at) + '. Choose how to handle it:'), opts, form);
  return h('details', { class: 'card more', open: !!cur }, h('summary', null, 'Admin actions'), h('div', { style: 'margin-top:14px' }, opts, form));
}

// ---------- hold, resume, ask the applicant, release ----------
const MORE = { suspend: ['Put on hold', 'Why is it on hold?', 'Put on hold'], resume: ['Resume', 'Anything to add? (optional)', 'Resume'], request_information: ['Ask the applicant', 'What do you need from them?', 'Send request'], release: ['Release to the queue', 'Why? (optional)', 'Release'] };
function moreCard(ov) {
  const ops = ((ov.work && ov.work.operations) || []).filter(o => MORE[o]);
  if (!ops.length) return null;
  const run = async op => {
    const text = (document.getElementById('more_text') || {}).value || '';
    if (op === 'suspend' && !text.trim()) { S.errs = { more: ['This is required.'] }; return renderOverview(); }
    if (op === 'request_information' && !text.trim()) { S.errs = { more: ['Say what you need.'] }; return renderOverview(); }
    const id = encodeURIComponent(ov.case.id);
    const call = op === 'request_information' ? ['POST', '/cases/' + id + '/information-request', { reason: text.trim() }]
      : ['POST', '/cases/' + id + '/stages/' + encodeURIComponent(ov.stage.name) + '/work/' + op, op === 'suspend' ? { reason: text.trim() } : { comment: text.trim() }];
    S.busy = true;
    try { await api(...call, ov.case.id); bounce('ok', MORE[op][0] + ': done.', location.pathname); }
    catch (e) { S.busy = false; S.errs = { more: [e.message] }; renderOverview(); }
  };
  return h('section', { class: 'card' }, h('h2', null, 'Actions'),
    h('div', { class: 'stack' }, ops.map(o => h('button', { type: 'button', class: S.panel === o ? 'primary' : '', onclick: () => { S.panel = S.panel === o ? '' : o; S.errs = {}; renderOverview(); } }, MORE[o][0]))),
    S.panel && MORE[S.panel] ? h('div', { class: 'stack', style: 'margin-top:12px' }, h('label', { for: 'more_text' }, MORE[S.panel][1]), h('textarea', { id: 'more_text', rows: 3 }), errList(S.errs.more),
      h('button', { type: 'button', class: 'primary', disabled: S.busy, onclick: () => run(S.panel) }, MORE[S.panel][2])) : null);
}
// The applicant's answer box, shown when staff have asked for more information.
function answerPanel(ov) {
  if (!ov.information || ((ov.work && ov.work.operations) || []).length) return null;
  const ans = h('textarea', { rows: 4, id: 'ans', placeholder: 'Your answer' });
  return h('div', { class: 'stack', style: 'margin-top:16px' }, h('h3', null, 'We need more information'), h('blockquote', null, ov.information.reason), h('label', { for: 'ans' }, 'Your answer'), ans, errList(S.errs.answer),
    h('button', { class: 'primary', type: 'button', disabled: S.busy, onclick: async () => {
      if (!ans.value.trim()) { S.errs = { answer: ['Please write an answer.'] }; return renderOverview(); }
      try { await api('POST', '/cases/' + encodeURIComponent(ov.case.id) + '/information-response', { response: { answer: ans.value.trim() } }, ov.case.id); bounce('ok', 'Thank you. Your answer was sent.', location.pathname); }
      catch (e) { S.errs = { answer: [e.message] }; renderOverview(); }
    } }, 'Send answer'));
}

// ---------- task: just the stage's form, checks and buttons ----------
async function showTask(id, stage) {
  page(h('p', { class: 'muted' }, 'Loading…'));
  const entry = new URLSearchParams(location.search).get('entry');
  const v = await api('GET', '/cases/' + encodeURIComponent(id) + (stage ? '/stages/' + encodeURIComponent(stage) : '') + (entry ? '?entry=' + encodeURIComponent(entry) : ''), undefined, id);
  S.errs = {}; S.entryId = entry || ''; applyView(v); renderTask();
}
const DONE = ['passed', 'waived', 'skipped'];
const openNodes = () => ((S.view && S.view.nodes) || []).filter(n => !n.optional && !DONE.includes(n.status));
function labelFor(path) { let l = path; forEachInput(i => { if (i.path === path) l = i.label; }); return l; }
function renderTask() {
  const v = S.view, c = v.case, st = v.stage, cid = encodeURIComponent(c.id);
  if (!st) { page(h('h1', null, 'Case ' + (c.number || c.id)), notice(), h('a', { class: 'btn', href: '/cases/' + cid }, 'Back to the case')); return; }
  if (S.review) return renderSummary();
  const ops = (v.work && v.work.operations) || [], locked = st.require_start && !st.started_at && ops.includes('start');
  const pg = v.page;
  const items = pg && pg.groups ? pg.groups.map(g => ({ title: g.title || g.name, forms: g.forms.map(f => f.name), flagged: g.forms.some(f => f.inputs.some(i => i.flag)),
    collapsed: g.collapsed, el: h('div', null, infoEl(g.name), groupEl(g)) })) : [];
  const en = v.entries, entryMode = !!en && !!S.entryId, listMode = !!en && !S.entryId;
  const bar = entryMode ? entryBar() : actionBar();
  const form = listMode ? entryList() : items.length ? h('section', { class: 'card', 'aria-labelledby': 'pg-title' }, h('h2', { id: 'pg-title' }, pg.title || st.title || st.name), pg.description ? h('p', { class: 'muted' }, pg.description) : null, infoEl(''),
    arrange(pg.layout, 'page', items, bar)) : (bar && !openNodes().length ? h('section', { class: 'card' }, h('h2', null, st.title || st.name), bar) : null);
  const checks = (v.nodes || []).length ? h('section', { class: 'card', 'aria-labelledby': 'nodes-h' }, h('h2', { id: 'nodes-h' }, 'Checks'), v.nodes.map(nodeEl)) : null;
  const startBanner = locked ? h('div', { class: 'banner warn' }, h('div', { class: 'grow' }, h('strong', null, 'Start this stage to begin. '), 'Your name and the time are recorded, and the work is yours until you finish or release it.'),
    h('button', { class: 'primary', type: 'button', disabled: S.busy, onclick: async () => { S.busy = true; try { await api('POST', '/cases/' + cid + '/stages/' + encodeURIComponent(st.name) + '/work/start', {}, c.id); location.reload(); } catch (e) { S.busy = false; S.notice = { kind: 'error', text: e.message }; renderTask(); } } }, 'Start')) : null;
  page(h('a', { class: 'crumb', href: '/cases/' + cid }, '← ' + (c.title || 'Case') + ' ' + c.number),
    h('div', { class: 'page-head' }, h('div', null, h('h1', null, st.title || st.name), h('div', { class: 'sub' }, st.started_at ? 'Started ' + short(st.started_at) + (st.started_by ? ' by ' + person(st.started_by) : '') : 'Not started')),
      h('div', { class: 'actions' }, slaBadge(v.work, st), badge(st.status))),
    notice(), v.work && v.work.suspended ? h('div', { class: 'banner warn' }, 'On hold: ' + v.work.suspended.reason) : null, startBanner,
    (v.corrections || []).length ? h('div', { class: 'banner warn' }, h('div', null, h('strong', null, 'Please correct: '), v.corrections.map(f => h('div', null, labelFor(f.path), f.comment ? ': ' + f.comment : '')))) : null,
    v.work && v.work.sla && st.due_at ? h('div', { class: 'card', style: 'padding:14px 18px' }, slaMeter(v.work, st)) : null, form, checks);
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
  const info = ro.length ? h('section', { class: 'card' }, ro.map(g => h('div', null, g.title ? h('div', { class: 'group-title' }, g.title) : null, groupEl(g)))) : null;
  const rows = en.mine.map(e => h('tr', null, h('td', null, h('strong', null, 'Entry ' + e.seq)), h('td', null, e.status === 'submitted' ? badge('completed', 'submitted') : badge('warning', 'draft')),
    h('td', null, short(e.submitted_at || e.updated_at)),
    h('td', null, h('div', { class: 'row' }, (e.status === 'draft' || en.allow_edit) ? h('a', { class: 'btn', href: stageUrl + '?entry=' + encodeURIComponent(e.id) }, e.status === 'draft' ? 'Continue' : 'Edit') : h('a', { class: 'btn', href: stageUrl + '?entry=' + encodeURIComponent(e.id) }, 'View'),
      (e.status === 'draft' || en.allow_edit) ? h('button', { type: 'button', class: 'ghost danger', onclick: () => delEntry(e) }, 'Delete') : null))));
  const used = en.per_user ? ' (' + en.mine.length + ' of ' + en.per_user + ' used)' : '';
  return h('div', null, info, h('section', { class: 'card' }, h('div', { class: 'card-head' }, h('h2', null, 'Your entries' + used),
    en.can_add ? h('a', { class: 'btn primary', href: stageUrl + '?entry=new' }, en.mine.length ? 'Add another entry' : 'Add an entry') : null),
    h('p', { class: 'muted' }, entryRule(en)),
    en.mine.length ? h('div', { class: 'tbl-wrap' }, h('table', null, h('thead', null, h('tr', null, ['Entry', 'Status', 'When', ''].map(t => h('th', { scope: 'col' }, t)))), h('tbody', null, rows)))
      : h('div', { class: 'empty' }, h('b', null, 'No entries yet'), en.can_add ? 'Add your first entry to continue.' : 'You cannot add entries here.'),
    h('p', { class: 'note', style: 'margin-top:12px' }, en.submitted + ' submitted in all' + (en.min > 1 ? ' · ' + en.min + ' needed' : '') + (en.drafts ? ' · ' + en.drafts + ' draft' + (en.drafts === 1 ? '' : 's') : '') + '.'),
    actionBar()));
}
async function delEntry(e) {
  if (!window.confirm('Delete entry ' + e.seq + '?')) return;
  const v = S.view;
  try { await api('POST', '/cases/' + encodeURIComponent(v.case.id) + '/stages/' + encodeURIComponent(v.stage.name) + '/entries/' + encodeURIComponent(e.id) + '/delete', {}, v.case.id); bounce('ok', 'Entry deleted.', location.pathname); }
  catch (x) { S.notice = { kind: 'error', text: x.message }; renderTask(); }
}
// Save or submit the entry on screen.
async function saveEntry(submit) {
  const v = S.view, en = v.entries, id = S.entryId === 'new' ? '' : S.entryId;
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
      onclick: () => {
        const comment = S.comment.trim();
        if (a.comment_required && !comment) { S.errs = { comment: ['A comment is required to ' + a.label + '.'] }; renderTask(); const el = document.getElementById('act_comment'); if (el) el.focus(); return; }
        if (a.confirm && !a.confirm_submit && !window.confirm(a.confirm)) return;
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
  renderTask();
}
function renderSummary() {
  const { r, a, body, path } = S.review, rv = r.review || r, fields = rv.fields || [];
  const val = f => f.value == null ? '—' : typeof f.value === 'boolean' ? (f.value ? 'Yes' : 'No') : Array.isArray(f.value) ? f.value.map(x => typeof x === 'object' ? Object.values(x).join(' · ') : x).join('; ') : typeof f.value === 'object' ? (f.value.name || JSON.stringify(f.value)) : String(f.value);
  const groups = [], byGroup = {};
  fields.forEach(f => { const g = f.group || ''; if (!byGroup[g]) { byGroup[g] = []; groups.push(g); } byGroup[g].push(f); });
  const cid = encodeURIComponent(S.view.case.id);
  page(h('a', { class: 'crumb', href: '/cases/' + cid }, '← Back to the case'), h('div', { class: 'page-head' }, h('div', null, h('h1', null, rv.title || 'Check before you send'), rv.description || rv.prompt ? h('div', { class: 'sub' }, rv.description || rv.prompt) : null)), notice(),
    h('section', { class: 'card' }, fields.length ? groups.map(g => h('div', null, g ? h('div', { class: 'group-title' }, g) : null,
      h('dl', { class: 'summary' }, byGroup[g].map(f => [h('dt', null, f.label), h('dd', null, val(f), f.masked ? h('span', { class: 'muted' }, ' (masked)') : null)])))) : h('div', { class: 'empty' }, 'Nothing to show.'),
      (rv.acknowledgements || []).length ? h('div', null, h('div', { class: 'group-title' }, 'You agreed to'), h('ul', null, rv.acknowledgements.map(x => h('li', null, x.text)))) : null,
      h('div', { class: 'bar' }, h('button', { type: 'button', onclick: () => { S.review = null; renderTask(); } }, 'Go back and edit'),
        h('div', { class: 'row end' }, h('button', { type: 'button', class: 'primary big', disabled: S.busy, onclick: () => { S.review = null; mutate('POST', path, { ...body, confirm_token: r.confirm_token }, '', a.label + ': done.', () => bounce('ok', a.label + ': done.', '/cases/' + cid)); } }, rv.button || a.label)))));
}

// ---------- work list ----------
async function showQueue(scope) {
  const c = claims(), isAdmin = !!(c && (c.roles || []).includes('admin'));
  const tabs = { queue: 'Waiting for my role', assigned: 'Assigned to me', mine: 'My own cases' }; if (isAdmin) tabs.attention = 'Needs attention';
  const nav2 = counts => h('div', { class: 'seg-tabs' }, Object.keys(tabs).map(s => h('a', { class: s === scope ? 'on' : '', href: '/work?scope=' + s }, tabs[s], s === 'attention' && counts ? h('span', { class: 'count' }, String(counts)) : null)));
  if (!token) { page(h('div', { class: 'page-head' }, h('h1', null, 'My work')), h('div', { class: 'card' }, h('div', { class: 'empty' }, h('b', null, 'Sign in to see your work'), 'Use the sign-in buttons at the top right.'))); return; }
  page(h('div', { class: 'page-head' }, h('h1', null, 'My work')), nav2(0), h('p', { class: 'muted' }, 'Loading…'));
  // The lists are per workflow; the work list brings them all together.
  const cat = await api('GET', '/pipelines') || [], sc = encodeURIComponent(scope === 'attention' ? 'all' : scope);
  let rows = (await Promise.all(cat.map(p => api('GET', '/cases?scope=' + sc + '&pipeline=' + encodeURIComponent(p.name)).then(rs => (rs || []).map(r => Object.assign({ wf: p.title || p.name }, r))).catch(() => [])))).flat()
    .sort((a, b) => String(b.updated_at || b.created_at).localeCompare(String(a.updated_at || a.created_at)));
  const all = rows || [], needs = r => r.sla_status === 'breached' || r.sla_status === 'warning' || r.on_hold || r.overdue;
  const attn = isAdmin ? all.filter(r => needs(r) && !['approved', 'rejected', 'completed', 'withdrawn'].includes(r.status)) : [];
  if (scope === 'attention') rows = attn;
  const chip = r => r.on_hold ? h('span', { class: 'badge b-warn' }, 'on hold') : r.sla_status === 'breached' ? badge('failed', 'SLA missed') : r.sla_status === 'warning' ? badge('warning', 'due soon') : badge(r.status);
  const head = h('div', { class: 'page-head' }, h('div', null, h('h1', null, 'My work'), h('div', { class: 'sub' }, scope === 'attention' ? 'Cases whose deadline is close or missed, or that are on hold.' : 'Cases waiting for you.')));
  const line = r => h('tr', null,
    h('td', null, h('a', { href: '/cases/' + encodeURIComponent(r.id) }, h('strong', null, r.number || r.id))),
    h('td', null, h('div', null, r.stage_title || r.stage), h('div', { class: 'faint', style: 'font-size:.78rem' }, r.wf)),
    h('td', null, r.assignee ? person(r.assignee) : h('span', { class: 'muted' }, 'unassigned')),
    h('td', null, chip(r)),
    h('td', null, r.due_at ? h('span', { class: r.overdue ? 'err' : 'muted' }, rel(r.due_at)) : '—'),
    h('td', null, h('a', { class: 'btn', href: '/cases/' + encodeURIComponent(r.id) }, 'Open'),
      r.breach_reason ? h('div', { class: 'faint', style: 'font-size:.78rem;max-width:34ch;margin-top:4px' }, r.breach_reason) : null));
  const empty = h('div', { class: 'empty' }, h('b', null, 'All clear'), scope === 'attention' ? 'No case needs attention.' : 'Nothing is waiting for you right now.');
  const table = h('div', { class: 'tbl-wrap' }, h('table', null,
    h('thead', null, h('tr', null, ['Case', 'Stage', 'Handled by', 'Status', 'Due', ''].map(t => h('th', { scope: 'col' }, t)))),
    h('tbody', null, (rows || []).map(line))));
  page(head, nav2(attn.length), notice(), h('div', { class: 'card' }, rows && rows.length ? table : empty));
}

// An issued certificate, with a live check of its signature.
function certCard(ov) {
  const list = ov.certificates || []; if (!list.length) return null;
  return h('section', { class: 'card' }, h('div', { class: 'card-head' }, h('h2', null, list.length > 1 ? 'Certificates' : 'Certificate')), list.map(ct => {
    const out = h('span', { class: 'muted' });
    return h('div', { class: 'entry' }, h('div', { class: 'row' }, h('strong', null, (ct.title || ct.name) + ' ' + ct.number), h('span', { class: 'grow' }), out),
      h('dl', { class: 'summary', style: 'margin-top:10px' }, h('dt', null, 'Issued'), h('dd', null, short(ct.issued_at)), ct.expires_at ? [h('dt', null, 'Valid until'), h('dd', null, short(ct.expires_at))] : null, ct.code ? [h('dt', null, 'Verification code'), h('dd', null, h('code', null, ct.code))] : null),
      h('div', { class: 'row', style: 'margin-top:10px' }, h('button', { type: 'button', onclick: async () => { out.textContent = 'Checking…'; try { const r = await api('GET', '/certificates/' + encodeURIComponent(ct.code || ct.number), undefined, null, true); out.className = r.valid ? 'badge b-ok' : 'badge b-bad'; out.textContent = r.valid ? 'Signature valid' : 'Not valid'; } catch (e) { out.textContent = e.message; } } }, 'Verify')));
  }));
}

// ---------- outside party (a stage handled by someone who is not a workflow user) ----------
function partyCard(ov) {
  const p = ov.party; if (!p) return null;
  const url = p.token ? location.origin + '/links/' + p.token : '';
  const ops = (ov.work && ov.work.operations) || [];
  const copy = h('button', { type: 'button', onclick: async () => { try { await navigator.clipboard.writeText(url); copy.textContent = 'Copied'; } catch (e) { window.prompt('Copy this link', url); } } }, 'Copy link');
  return h('section', { class: 'card' }, h('div', { class: 'card-head' }, h('h2', null, 'Waiting for an outside party'), badge(p.status === 'submitted' ? 'completed' : p.status === 'expired' ? 'failed' : 'active', p.status)),
    h('dl', { class: 'kv' }, h('dt', null, 'Who'), h('dd', null, p.party), h('dt', null, 'Link sent'), h('dd', null, short(p.issued_at)), h('dt', null, p.status === 'submitted' ? 'Submitted' : 'Expires'), h('dd', null, short(p.submitted_at || p.expires_at))),
    p.status === 'waiting' && !p.token ? h('p', { class: 'muted', style: 'margin-top:12px' }, 'They have a private link and need no account. This step moves on as soon as they submit.') : null,
    p.token ? h('div', { class: 'stack', style: 'margin-top:14px' }, h('label', null, 'Private link for ' + p.party), h('input', { type: 'text', readonly: true, value: url, onfocus: e => e.target.select() }), h('div', { class: 'row' }, copy, h('a', { class: 'btn', href: url, target: '_blank', rel: 'noopener' }, 'Open as them'))) : null,
    ops.includes('reissue') ? h('div', { class: 'row', style: 'margin-top:12px' }, h('button', { type: 'button', onclick: async () => { try { await api('POST', '/cases/' + encodeURIComponent(ov.case.id) + '/stages/' + encodeURIComponent(ov.stage.name) + '/work/reissue', {}, ov.case.id); bounce('ok', 'A new link was issued; the old one no longer works.', location.pathname); } catch (e) { S.notice = { kind: 'error', text: e.message }; renderOverview(); } } }, 'Issue a new link')) : null);
}
function entriesCard(ov) {
  const rows = ov.entry_rows || []; if (!rows.length && !ov.entries) return null;
  const val = f => f.value == null ? '—' : typeof f.value === 'boolean' ? (f.value ? 'Yes' : 'No') : String(f.value);
  return h('section', { class: 'card' }, h('div', { class: 'card-head' }, h('h2', null, 'Entries'), ov.entries ? h('span', { class: 'muted' }, ov.entries.submitted + ' submitted' + (ov.entries.min > 1 ? ' of ' + ov.entries.min + ' needed' : '')) : null),
    ov.entries ? h('p', { class: 'muted' }, entryRule(ov.entries)) : null,
    rows.length ? rows.map(r => h('div', { class: 'entry' }, h('div', { class: 'row' }, h('strong', null, r.stage_title + ' · entry ' + r.seq), r.status === 'submitted' ? badge('completed', 'submitted') : badge('warning', 'draft'),
      h('span', { class: 'muted' }, (r.mine ? 'by you' : r.by ? 'by ' + person(r.by) : '') + (r.submitted_at ? ' · ' + short(r.submitted_at) : ''))),
      r.fields.length ? h('dl', { class: 'summary', style: 'margin-top:10px' }, r.fields.map(f => [h('dt', null, f.label), h('dd', null, val(f))])) : null)) : h('div', { class: 'empty' }, 'No entries yet.'));
}

// ---------- the outside party's own page ----------
async function showLink(token_) {
  page(h('p', { class: 'muted' }, 'Loading…'));
  let v;
  try { v = await api('GET', '/links/' + encodeURIComponent(token_), undefined, null, true); }
  catch (e) { page(h('div', { class: 'card' }, h('div', { class: 'empty' }, h('b', null, 'This link cannot be used'), e.message + ' Ask the person who sent it for a new one.'))); return; }
  S.link = { token: token_, done: false }; S.errs = {}; applyView(v);
  renderLinkPage();
}
function renderLinkPage() {
  const v = S.view, x = v.external || {}, pg = v.page;
  if (S.link.done) { page(h('div', { class: 'card' }, h('div', { class: 'empty' }, h('b', null, 'Thank you'), 'Your answer was received and the request moves on. You can close this page.'))); return; }
  const items = pg && pg.groups ? pg.groups.map(g => ({ title: g.title || g.name, forms: g.forms.map(f => f.name), flagged: false, collapsed: false, el: h('div', null, groupEl(g)) })) : [];
  const send = h('button', { type: 'button', class: 'primary big', disabled: S.busy, onclick: async () => {
    S.busy = true; S.errs = {};
    try { await api('POST', '/links/' + encodeURIComponent(S.link.token), { data: collect() || {} }, null, true); S.link.done = true; }
    catch (e) { failed(e, ''); }
    S.busy = false; renderLinkPage();
  } }, 'Submit');
  page(h('div', { class: 'page-head' }, h('div', null, h('h1', null, (v.case.title || 'Request') + ' ' + (v.case.number || '')), h('div', { class: 'sub' }, 'Requested of ' + (x.party || 'you') + (x.expires ? ' · link valid until ' + short(x.expires) : '')))),
    notice(), h('div', { class: 'banner' }, 'You were asked to complete this step. You do not need an account; the link works once.'),
    h('section', { class: 'card' }, arrange(pg && pg.layout, 'page', items, h('div', { class: 'bar' }, h('span'), h('div', { class: 'row end' }, send)))));
}

renderIdent();
route();
