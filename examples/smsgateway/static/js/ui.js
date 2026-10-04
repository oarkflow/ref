// Shared UI: elements, icons, requests, toasts, dialogs, drawers, tables, charts
// and the shell (sidebar, bell, user menu, theme). Page scripts call UI.page(fn).
(function () {
  var UI = (window.UI = {});

  // ------------------------------------------------------------------ basics
  UI.$ = function (s, r) { return (r || document).querySelector(s); };
  UI.$$ = function (s, r) { return [].slice.call((r || document).querySelectorAll(s)); };
  UI.el = function (tag, attrs, kids) {
    var e = document.createElement(tag);
    Object.keys(attrs || {}).forEach(function (k) {
      var v = attrs[k];
      if (k === 'text') e.textContent = v; else if (k === 'html') e.innerHTML = v; else if (k === 'class') e.className = v;
      else if (k.slice(0, 2) === 'on') e.addEventListener(k.slice(2), v);
      else if (k === 'style' && typeof v === 'object') Object.assign(e.style, v);
      else if (v !== false && v != null) e.setAttribute(k, v === true ? '' : v);
    });
    (kids || []).forEach(function (c) { if (c != null) e.appendChild(typeof c === 'string' ? document.createTextNode(c) : c); });
    return e;
  };
  var ICONS = {
    send: 'M22 2 11 13M22 2l-7 20-4-9-9-4 20-7z', list: 'M8 6h13M8 12h13M8 18h13M3 6h.01M3 12h.01M3 18h.01', megaphone: 'M3 11v2a1 1 0 0 0 1 1h2l5 4V6L6 10H4a1 1 0 0 0-1 1zM15 9a4 4 0 0 1 0 6M18 6a8 8 0 0 1 0 12',
    users: 'M17 21v-2a4 4 0 0 0-4-4H5a4 4 0 0 0-4 4v2M9 11a4 4 0 1 0 0-8 4 4 0 0 0 0 8zM23 21v-2a4 4 0 0 0-3-3.87M16 3.13a4 4 0 0 1 0 7.75',
    bell: 'M18 8a6 6 0 0 0-12 0c0 7-3 9-3 9h18s-3-2-3-9M13.73 21a2 2 0 0 1-3.46 0', grid: 'M3 3h7v7H3zM14 3h7v7h-7zM14 14h7v7h-7zM3 14h7v7H3z',
    server: 'M2 4h20v6H2zM2 14h20v6H2zM6 7h.01M6 17h.01', sliders: 'M4 21v-7M4 10V3M12 21v-9M12 8V3M20 21v-5M20 12V3M1 14h6M9 8h6M17 16h6',
    user: 'M20 21v-2a4 4 0 0 0-4-4H8a4 4 0 0 0-4 4v2M12 11a4 4 0 1 0 0-8 4 4 0 0 0 0 8z', sun: 'M12 17a5 5 0 1 0 0-10 5 5 0 0 0 0 10zM12 1v2M12 21v2M4.22 4.22l1.42 1.42M18.36 18.36l1.42 1.42M1 12h2M21 12h2M4.22 19.78l1.42-1.42M18.36 5.64l1.42-1.42',
    moon: 'M21 12.79A9 9 0 1 1 11.21 3 7 7 0 0 0 21 12.79z', menu: 'M3 12h18M3 6h18M3 18h18', x: 'M18 6 6 18M6 6l12 12', chevron: 'M9 18l6-6-6-6', check: 'M20 6 9 17l-5-5',
    alert: 'M10.29 3.86 1.82 18a2 2 0 0 0 1.71 3h16.94a2 2 0 0 0 1.71-3L13.71 3.86a2 2 0 0 0-3.42 0zM12 9v4M12 17h.01', info: 'M12 22a10 10 0 1 0 0-20 10 10 0 0 0 0 20zM12 16v-4M12 8h.01',
    search: 'M11 19a8 8 0 1 0 0-16 8 8 0 0 0 0 16zM21 21l-4.35-4.35', plus: 'M12 5v14M5 12h14', trash: 'M3 6h18M8 6V4h8v2M19 6l-1 14H6L5 6', edit: 'M12 20h9M16.5 3.5a2.1 2.1 0 0 1 3 3L7 19l-4 1 1-4z',
    refresh: 'M23 4v6h-6M1 20v-6h6M3.51 9a9 9 0 0 1 14.85-3.36L23 10M1 14l4.64 4.36A9 9 0 0 0 20.49 15', download: 'M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4M7 10l5 5 5-5M12 15V3',
    upload: 'M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4M17 8l-5-5-5 5M12 3v12', pause: 'M6 4h4v16H6zM14 4h4v16h-4z', play: 'M5 3l14 9-14 9z', shield: 'M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10z',
    key: 'M21 2l-2 2m-7.61 7.61a5.5 5.5 0 1 1-7.78 7.78 5.5 5.5 0 0 1 7.78-7.78zm0 0L15.5 7.5m0 0 3 3L22 7l-3-3', activity: 'M22 12h-4l-3 9L9 3l-3 9H2', dollar: 'M12 1v22M17 5H9.5a3.5 3.5 0 0 0 0 7h5a3.5 3.5 0 0 1 0 7H6',
    inbox: 'M22 12h-6l-2 3h-4l-2-3H2M5.45 5.11 2 12v6a2 2 0 0 0 2 2h16a2 2 0 0 0 2-2v-6l-3.45-6.89A2 2 0 0 0 16.76 4H7.24a2 2 0 0 0-1.79 1.11z', logout: 'M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4M16 17l5-5-5-5M21 12H9',
    chat: 'M21 15a2 2 0 0 1-2 2H7l-4 4V5a2 2 0 0 1 2-2h14a2 2 0 0 1 2 2z', clock: 'M12 22a10 10 0 1 0 0-20 10 10 0 0 0 0 20zM12 6v6l4 2', cpu: 'M4 4h16v16H4zM9 9h6v6H9zM9 1v3M15 1v3M9 20v3M15 20v3M20 9h3M20 14h3M1 9h3M1 14h3',
    layers: 'M12 2 2 7l10 5 10-5-10-5zM2 17l10 5 10-5M2 12l10 5 10-5', copy: 'M9 9h13v13H9zM5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1', eye: 'M1 12s4-8 11-8 11 8 11 8-4 8-11 8-11-8-11-8zM12 15a3 3 0 1 0 0-6 3 3 0 0 0 0 6z'
  };
  UI.icon = function (name, size) {
    var s = size || 20, ns = 'http://www.w3.org/2000/svg', svg = document.createElementNS(ns, 'svg');
    svg.setAttribute('viewBox', '0 0 24 24'); svg.setAttribute('width', s); svg.setAttribute('height', s); svg.setAttribute('fill', 'none');
    svg.setAttribute('stroke', 'currentColor'); svg.setAttribute('stroke-width', '2'); svg.setAttribute('stroke-linecap', 'round'); svg.setAttribute('stroke-linejoin', 'round'); svg.setAttribute('aria-hidden', 'true'); svg.setAttribute('class', 'ico');
    var p = document.createElementNS(ns, 'path'); p.setAttribute('d', ICONS[name] || ICONS.info); svg.appendChild(p); return svg;
  };
  UI.icons = function (root) { UI.$$('[data-icon]', root).forEach(function (n) { if (!n.firstChild || !n.querySelector('svg')) n.insertBefore(UI.icon(n.dataset.icon, +n.dataset.size || 20), n.firstChild); }); };

  // ---------------------------------------------------------------- requests
  UI.call = function (method, path, body) {
    return fetch(path, { method: method, credentials: 'same-origin', headers: { 'Content-Type': 'application/json' }, body: body ? JSON.stringify(body) : undefined })
      .then(function (r) {
        if (r.status === 401 && path !== '/login' && !/^\/login/.test(location.pathname)) { location.href = '/login'; return new Promise(function () {}); }
        return r.json().catch(function () { return {}; }).then(function (j) { return { status: r.status, ok: r.status < 400, body: j }; });
      });
  };
  UI.problem = function (r) { var e = (r.body && r.body.error) || {}; return (e.message || ('Request failed (' + r.status + ')')) + (e.code ? ' [' + e.code + ']' : ''); };
  // busy: shows a spinner on a button while a promise runs.
  UI.busy = function (btn, promise) { if (!btn) return promise; btn.classList.add('busy'); btn.disabled = true; var done = function () { btn.classList.remove('busy'); btn.disabled = false; }; promise.then(done, done); return promise; };

  // ------------------------------------------------------------------ format
  UI.money = function (v, digits) { return (typeof v === 'number' ? v : Number(v) || 0).toLocaleString(undefined, { minimumFractionDigits: digits == null ? 2 : digits, maximumFractionDigits: digits == null ? 3 : digits }); };
  UI.micros = function (v) { return UI.money((Number(v) || 0) / 1e6, 3); };
  UI.num = function (v) { return (Number(v) || 0).toLocaleString(); };
  UI.ago = function (ms) {
    if (!ms) return ''; var s = Math.max(0, Math.round((Date.now() - ms) / 1000));
    if (s < 5) return 'just now'; if (s < 60) return s + 's ago'; if (s < 3600) return Math.floor(s / 60) + ' min ago'; if (s < 86400) return Math.floor(s / 3600) + ' h ago';
    return new Date(ms).toLocaleDateString(undefined, { month: 'short', day: 'numeric' });
  };
  UI.when = function (ms) { return ms ? new Date(ms).toLocaleString() : ''; };
  UI.countUp = function (node, to, fmt) {
    var from = 0, start = performance.now(), dur = 650; fmt = fmt || UI.num;
    if (window.matchMedia && matchMedia('(prefers-reduced-motion: reduce)').matches) { node.textContent = fmt(to); return; }
    (function step(t) { var k = Math.min(1, (t - start) / dur), e = 1 - Math.pow(1 - k, 3); node.textContent = fmt(from + (to - from) * e); if (k < 1) requestAnimationFrame(step); })(start);
  };
  var STATE = { delivered: 'ok', sent: 'ok', submitted: 'info', queued: 'info', dispatching: 'info', pending: 'info', failed: 'err', rejected: 'err', skipped: 'warn', pending_approval: 'warn', sending: 'info', active: 'ok', paused: 'warn', disabled: 'err', suspended: 'err' };
  UI.state = function (s) { return UI.el('span', { class: 'pill ' + (STATE[s] || ''), text: String(s || '').replace(/_/g, ' ') }); };

  // ------------------------------------------------------------------ toasts
  UI.toast = function (message, kind, title) {
    var box = UI.$('#toasts'); if (!box) { box = UI.el('div', { class: 'toasts', id: 'toasts', 'aria-live': 'polite' }); document.body.appendChild(box); }
    var t = UI.el('div', { class: 'toast ' + (kind || ''), role: kind === 'err' ? 'alert' : 'status' }, [
      UI.icon(kind === 'err' ? 'alert' : kind === 'ok' ? 'check' : kind === 'warn' ? 'alert' : 'info', 20),
      UI.el('div', { class: 't' }, [title ? UI.el('strong', { text: title }) : null, UI.el('span', { text: message })]),
      UI.el('button', { class: 'iconbtn', type: 'button', 'aria-label': 'Dismiss', onclick: function () { gone(); } }, [UI.icon('x', 16)])
    ]);
    var gone = function () { t.classList.add('out-anim'); setTimeout(function () { t.remove(); }, 250); };
    box.appendChild(t); setTimeout(gone, kind === 'err' ? 8000 : 4500); return t;
  };
  UI.ok = function (m) { return UI.toast(m, 'ok'); }; UI.fail = function (m) { return UI.toast(m, 'err'); };

  // ----------------------------------------------------------------- dialogs
  function overlay(onclick) { var o = UI.el('div', { class: 'overlay', onclick: onclick }); document.body.appendChild(o); return o; }
  UI.confirm = function (opts) {
    return new Promise(function (resolve) {
      var prev = document.activeElement, o = overlay(function () { close(false); });
      var yes = UI.el('button', { type: 'button', class: opts.danger ? 'danger' : '', text: opts.confirm || 'Confirm', onclick: function () { close(true); } });
      var m = UI.el('div', { class: 'modal', role: 'dialog', 'aria-modal': 'true', 'aria-label': opts.title }, [
        UI.el('h2', { text: opts.title }), UI.el('p', { class: 'muted', text: opts.text || '' }),
        UI.el('div', { class: 'row' }, [UI.el('button', { type: 'button', class: 'secondary', text: 'Cancel', onclick: function () { close(false); } }), yes])
      ]);
      document.body.appendChild(m); yes.focus();
      var key = function (e) { if (e.key === 'Escape') close(false); }; document.addEventListener('keydown', key);
      function close(v) { document.removeEventListener('keydown', key); m.remove(); o.remove(); if (prev && prev.focus) prev.focus(); resolve(v); }
    });
  };
  // drawer: a panel that slides in from the right. Returns { body, head, close }.
  UI.drawer = function (opts) {
    var prev = document.activeElement, leaving = false;
    var o = overlay(function () { close(); });
    var title = UI.el('div', {}, [UI.el('h2', { text: opts.title || '', style: { margin: 0 } }), opts.subtitle ? UI.el('div', { class: 'muted', text: opts.subtitle }) : null]);
    var actions = UI.el('div', { class: 'row', style: { margin: 0 } });
    var d = UI.el('aside', { class: 'drawer', role: 'dialog', 'aria-modal': 'true', 'aria-label': opts.title || 'Details' }, [
      UI.el('div', { class: 'dhead' }, [title, UI.el('div', { class: 'row', style: { margin: 0 } }, [actions, UI.el('button', { class: 'iconbtn', type: 'button', 'aria-label': 'Close', onclick: function () { close(); } }, [UI.icon('x', 20)])])]),
      UI.el('div', { class: 'dbody' })
    ]);
    document.body.appendChild(d); document.body.style.overflow = 'hidden';
    var key = function (e) { if (e.key === 'Escape') close(); }; document.addEventListener('keydown', key);
    function close() {
      if (leaving) return; leaving = true; document.removeEventListener('keydown', key);
      d.classList.add('leaving'); o.classList.add('leaving');
      setTimeout(function () { d.remove(); o.remove(); document.body.style.overflow = ''; if (prev && prev.focus) prev.focus(); if (opts.onclose) opts.onclose(); }, 220);
    }
    return { body: UI.$('.dbody', d), actions: actions, title: title.firstChild, close: close, el: d };
  };

  // ------------------------------------------------------------------ tables
  UI.skeleton = function (rows, cols) {
    var t = UI.el('div', { style: { padding: '8px 0' } });
    for (var i = 0; i < (rows || 4); i++) t.appendChild(UI.el('div', { class: 'skel', style: { height: '16px', margin: '14px 0', width: (60 + (i * 13) % 35) + '%' } }));
    return t;
  };
  UI.empty = function (title, text, icon, action) {
    return UI.el('div', { class: 'empty' }, [UI.icon(icon || 'inbox', 44), UI.el('strong', { text: title }), UI.el('div', { text: text || '' }), action || null]);
  };
  // table: columns [{ label, key|render(row)->Node|string, num, class }], rows, empty {title,text,icon}, onRow(row)
  UI.table = function (host, o) {
    host.textContent = '';
    if (!o.rows || !o.rows.length) { host.appendChild(UI.empty((o.empty || {}).title || 'Nothing here yet', (o.empty || {}).text, (o.empty || {}).icon, (o.empty || {}).action)); return; }
    var head = UI.el('tr', {}, o.columns.map(function (c) { return UI.el('th', { class: c.num ? 'num' : '', text: c.label }); }));
    var body = UI.el('tbody');
    o.rows.forEach(function (row, i) {
      var tr = UI.el('tr', { class: o.onRow ? 'clickable' : '', style: { animationDelay: Math.min(i, 14) * 25 + 'ms' } });
      o.columns.forEach(function (c) {
        var v = c.render ? c.render(row) : row[c.key], td = UI.el('td', { class: (c.num ? 'num ' : '') + (c.class || '') });
        if (v instanceof Node) td.appendChild(v); else td.textContent = v == null ? '' : String(v);
        tr.appendChild(td);
      });
      if (o.onRow) { tr.tabIndex = 0; tr.addEventListener('click', function (e) { if (e.target.closest('a,button,input,select,label')) return; o.onRow(row); }); tr.addEventListener('keydown', function (e) { if (e.key === 'Enter') o.onRow(row); }); }
      body.appendChild(tr);
    });
    host.appendChild(UI.el('div', { class: 'tablewrap' }, [UI.el('table', {}, [UI.el('thead', {}, [head]), body])]));
  };

  // ------------------------------------------------------------------ charts
  var NS = 'http://www.w3.org/2000/svg';
  function svg(tag, attrs) { var e = document.createElementNS(NS, tag); Object.keys(attrs || {}).forEach(function (k) { e.setAttribute(k, attrs[k]); }); return e; }
  UI.palette = ['#6366f1', '#06b6d4', '#16a34a', '#d97706', '#dc2626', '#a855f7', '#0ea5e9', '#f43f5e'];
  UI.bars = function (data, o) {
    o = o || {}; var w = o.width || 640, h = o.height || 190, pad = { l: 30, r: 8, t: 10, b: 26 }, max = Math.max.apply(null, data.map(function (d) { return d.value; }).concat([1]));
    var s = svg('svg', { viewBox: '0 0 ' + w + ' ' + h, class: 'chart', role: 'img', 'aria-label': o.label || 'Bar chart' });
    [0, .5, 1].forEach(function (k) { var y = pad.t + (h - pad.t - pad.b) * (1 - k); s.appendChild(svg('line', { x1: pad.l, x2: w - pad.r, y1: y, y2: y, class: 'grid' })); var t = svg('text', { x: 0, y: y + 4 }); t.textContent = Math.round(max * k); s.appendChild(t); });
    var bw = (w - pad.l - pad.r) / data.length;
    data.forEach(function (d, i) {
      var bh = (h - pad.t - pad.b) * d.value / max, x = pad.l + i * bw + bw * .18, y = h - pad.b - bh;
      var r = svg('rect', { x: x, y: h - pad.b, width: bw * .64, height: 0, rx: 4, class: 'bar-fill' }); r.appendChild(svg('title')).textContent = d.label + ': ' + d.value;
      s.appendChild(r); requestAnimationFrame(function () { setTimeout(function () { r.setAttribute('y', y); r.setAttribute('height', Math.max(bh, d.value ? 2 : 0)); }, 30 + i * 20); });
      if (!(data.length > 12 && i % 2)) { var t = svg('text', { x: x + bw * .32, y: h - 8, 'text-anchor': 'middle' }); t.textContent = d.label; s.appendChild(t); }
    });
    return s;
  };
  UI.donut = function (segments, centerText, sub) {
    var total = segments.reduce(function (a, s) { return a + s.value; }, 0), r = 56, c = 2 * Math.PI * r, off = 0;
    var s = svg('svg', { viewBox: '0 0 150 150', class: 'donut', role: 'img', 'aria-label': 'Share by state' });
    s.appendChild(svg('circle', { cx: 75, cy: 75, r: r, fill: 'none', stroke: 'var(--surface-2)', 'stroke-width': 18 }));
    if (total) segments.forEach(function (g, i) {
      var len = c * g.value / total, ring = svg('circle', { cx: 75, cy: 75, r: r, fill: 'none', stroke: g.color || UI.palette[i % UI.palette.length], 'stroke-width': 18, 'stroke-dasharray': '0 ' + c, 'stroke-dashoffset': -off, transform: 'rotate(-90 75 75)', class: 'seg' });
      ring.appendChild(svg('title')).textContent = g.label + ': ' + g.value; s.appendChild(ring); off += len;
      setTimeout(function () { ring.setAttribute('stroke-dasharray', len + ' ' + (c - len)); }, 40 + i * 90);
    });
    var t1 = svg('text', { x: 75, y: 78, 'text-anchor': 'middle', style: 'font-size:22px;font-weight:700;fill:var(--fg)' }); t1.textContent = centerText; s.appendChild(t1);
    var t2 = svg('text', { x: 75, y: 96, 'text-anchor': 'middle', style: 'font-size:11px' }); t2.textContent = sub || ''; s.appendChild(t2);
    return s;
  };
  UI.spark = function (values, o) {
    o = o || {}; var w = o.width || 240, h = o.height || 56, max = Math.max.apply(null, values.concat([1])), step = values.length > 1 ? w / (values.length - 1) : w;
    var pts = values.map(function (v, i) { return [i * step, h - 4 - (h - 8) * v / max]; });
    var d = pts.map(function (p, i) { return (i ? 'L' : 'M') + p[0].toFixed(1) + ' ' + p[1].toFixed(1); }).join(' ');
    var s = svg('svg', { viewBox: '0 0 ' + w + ' ' + h, class: 'chart', preserveAspectRatio: 'none', role: 'img', 'aria-label': o.label || 'Trend' });
    s.appendChild(svg('path', { d: d + ' L' + w + ' ' + h + ' L0 ' + h + ' Z', class: 'area' }));
    var line = svg('path', { d: d, class: 'line' }); line.style.setProperty('--len', Math.ceil(w * 2)); s.appendChild(line); return s;
  };

  // ------------------------------------------------------------------- shell
  var pageFns = [];
  UI.page = function (fn) { pageFns.push(fn); };
  UI.me = null;
  function theme(set) {
    var root = document.documentElement;
    if (set) { root.dataset.theme = set; try { localStorage.setItem('sms_theme', set); } catch (e) {} }
    var dark = root.dataset.theme === 'dark' || (!root.dataset.theme && matchMedia('(prefers-color-scheme: dark)').matches); return dark;
  }
  try { var saved = localStorage.getItem('sms_theme'); if (saved) document.documentElement.dataset.theme = saved; } catch (e) {}
  function menu(anchor, node) {
    var close = function () { node.remove(); document.removeEventListener('click', away); }; var away = function (e) { if (!node.contains(e.target) && !anchor.contains(e.target)) close(); };
    document.body.appendChild(node); setTimeout(function () { document.addEventListener('click', away); }, 0); return close;
  }
  function boot() {
    UI.icons(document);
    var shell = UI.$('#shell');
    if (!shell) { pageFns.forEach(function (f) { f({ me: null, admin: false }); }); return; }
    var path = location.pathname.replace(/\/$/, '') || '/';
    var best = null; UI.$$('.navlink').forEach(function (a) { var p = a.getAttribute('href'); if (p === path || (p !== '/' && path.indexOf(p) === 0)) { if (!best || p.length > best.getAttribute('href').length) best = a; } });
    if (best) { best.classList.add('active'); best.setAttribute('aria-current', 'page'); }
    var label = best ? best.textContent.trim() : ''; UI.$('#crumb').textContent = label; if (label) document.title = label + ' · ' + document.title.split(' - ').slice(-1)[0];
    try { if (localStorage.getItem('sms_collapsed') === '1') shell.classList.add('collapsed'); } catch (e) {}
    UI.$('#collapse').addEventListener('click', function () { shell.classList.toggle('collapsed'); try { localStorage.setItem('sms_collapsed', shell.classList.contains('collapsed') ? '1' : '0'); } catch (e) {} });
    UI.$('#burger').addEventListener('click', function () { shell.classList.add('open'); var s = UI.el('div', { class: 'scrim', onclick: function () { shell.classList.remove('open'); s.remove(); } }); document.body.appendChild(s); });
    UI.$$('.navlink').forEach(function (a) { a.addEventListener('click', function () { shell.classList.remove('open'); var s = UI.$('.scrim'); if (s) s.remove(); }); });
    var tb = UI.$('#theme'); var paint = function () { tb.textContent = ''; tb.appendChild(UI.icon(theme() ? 'sun' : 'moon', 18)); tb.setAttribute('aria-label', theme() ? 'Switch to light mode' : 'Switch to dark mode'); };
    tb.addEventListener('click', function () { theme(theme() ? 'light' : 'dark'); paint(); }); paint();
    UI.call('GET', '/ui/me').then(function (r) {
      var roles = (r.body && r.body.roles) || [], admin = roles.indexOf('admin') >= 0; UI.me = r.body; UI.admin = admin;
      var name = r.body.name || r.body.id || ''; UI.$('#avatar').textContent = name.charAt(0);
      UI.$('#who').textContent = name; UI.$$('.admin-only').forEach(function (n) { n.hidden = !admin; });
      var chip = UI.$('#userchip');
      chip.addEventListener('click', function () {
        var m = UI.el('div', { class: 'menu', role: 'menu' }, [
          UI.el('div', { class: 'head' }, [UI.el('strong', { text: name }), UI.el('small', { text: admin ? 'Operator' : 'Account ' + (r.body.id || '') })]), UI.el('div', { class: 'sep' }),
          UI.el('button', { class: 'mi', type: 'button', onclick: function () { UI.call('POST', '/logout').then(function () { location.href = '/login'; }); } }, [UI.icon('logout', 18), 'Sign out'])
        ]); menu(chip, m);
      });
      bell(); pageFns.forEach(function (f) { f({ me: r.body, admin: admin }); });
    });
  }
  // The bell: unread count, and a panel with the latest notices.
  function bell() {
    var btn = UI.$('#bell'), dot = UI.$('#bell-count');
    function count() { return UI.call('GET', '/ui/notifications').then(function (n) { if (n.status !== 200) return n; dot.hidden = !(n.body.unread > 0); dot.textContent = n.body.unread; return n; }); }
    window.refreshBell = count; count(); setInterval(count, 45000);
    btn.addEventListener('click', function () {
      count().then(function (n) {
        var list = (n.body && n.body.notifications || []).slice(0, 8), panel = UI.el('div', { class: 'menu panel-notes' });
        var close = menu(btn, panel);
        panel.appendChild(UI.el('div', { class: 'nhead' }, [UI.el('strong', { text: 'Notifications' }), UI.el('a', { href: '/notifications', text: 'See all', onclick: close })]));
        if (!list.length) panel.appendChild(UI.empty('You are all caught up', 'Campaign results and warnings appear here.', 'bell'));
        list.forEach(function (x) {
          panel.appendChild(UI.el('div', { class: 'nitem' + (x.read_ms ? '' : ' unread') }, [UI.el('span', { class: 'pill ' + (x.level === 'warn' ? 'warn' : 'info') }, [UI.icon(x.level === 'warn' ? 'alert' : 'info', 14)]),
            UI.el('div', {}, [UI.el('div', { text: x.title, style: { fontWeight: 600 } }), UI.el('small', { class: 'muted', text: x.body }), UI.el('div', { class: 'faint', style: { fontSize: '12px' }, text: UI.ago(x.created_ms) })])]));
        });
      });
    });
  }
  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', boot); else boot();
})();
