(function () {
  var C = window.C, $ = C.$, esc = C.esc, app = $('#app');
  var STAGES = ['Source ingestion and validation', 'Transformation and enrichment', 'Reliable system-to-system transfer', 'Delivery queues and retry paths', 'Access controls and audit trails', 'Monitoring and data lineage'];
  var S = { me: null, tab: 'overview', sub: 'users', sum: null, batches: [], sources: [], dests: [], quarantine: [], audit: [], verify: null, warehouse: [], failures: [], sel: null, detail: null,
    filter: '', panel: null, mon: null, win: '24h', health: null, logs: [], counters: [], logq: { level: 'info', q: '', trace: '' }, users: [], roles: [], perms: [], keys: [], newKey: null, mailbox: [], link: null, mfa: null, mfaSetup: null, mfaCodes: null, history: [] };

  // ---- permissions, as the engine resolved them for this person ----------------
  function can(p, src) { var s = S.me.permissions && S.me.permissions[p]; if (s === undefined) return false; return s === null || !src || s.indexOf(src) >= 0; }
  function canAny(p) { return !!(S.me.permissions && S.me.permissions[p] !== undefined); }
  function pill(status) {
    var m = { delivered: ['Delivered', ''], in_flight: ['In flight', 'info'], retrying: ['Retrying', 'warn'], held: ['Held for replay', 'bad'], failed: ['Failed at intake', 'bad'] }[status] || [status, ''];
    return '<span class="pill ' + m[1] + '">' + m[0] + '</span>';
  }
  function sev(s) { return '<span class="pill ' + ({ critical: 'bad', warning: 'warn', info: 'info', ok: '', degraded: 'warn', down: 'bad', late: 'warn', stale: 'bad', quiet: 'info', none: 'info' }[s] || '') + '">' + esc(s) + '</span>'; }
  function stageLabel(b) { return b.stage > 6 ? 'Complete' : 'Stage ' + b.stage + ' · ' + STAGES[b.stage - 1].split(' ')[0]; }
  function ago(iso) { var s = (Date.now() - new Date(iso)) / 1000; if (isNaN(s)) return ''; if (s < 90) return Math.round(s) + 's ago'; if (s < 5400) return Math.round(s / 60) + 'm ago'; if (s < 129600) return Math.round(s / 3600) + 'h ago'; return Math.round(s / 86400) + 'd ago'; }
  function dur(sec) { return sec < 1 ? Math.round(sec * 1000) + ' ms' : sec < 90 ? sec.toFixed(1) + ' s' : Math.round(sec / 60) + ' min'; }
  function pct(v) { return (v * 100).toFixed(v > 0 && v < .1 ? 1 : 0) + '%'; }

  // ---- loading -------------------------------------------------------------------
  function get(path, key, fallback) { return C.call('GET', path).then(function (r) { if (r.ok) S[key] = (r.body == null && fallback !== undefined) ? fallback : r.body; else if (fallback !== undefined) S[key] = fallback; return r; }); }
  // Permissions are asked for again on every refresh, before anything else: a role
  // changed or an account disabled shows up within seconds (a disabled account is
  // answered with 401 and sent to the sign-in page).
  function load() {
    return C.call('GET', '/ui/me').then(function (r) { if (r.ok) S.me = r.body; return loadData(); });
  }
  function loadData() {
    var jobs = [], t = S.tab;
    if (can('read')) {
      jobs.push(get('/ui/etl/summary', 'sum'), get('/ui/etl/batches?limit=40' + (S.filter ? '&status=' + S.filter : ''), 'batches', []));
      if (t === 'overview') jobs.push(get('/ui/etl/destinations', 'dests', []), get('/ui/etl/warehouse', 'warehouse', []));
      if (t === 'failures') jobs.push(get('/ui/etl/batches?status=held,retrying,failed&limit=100', 'failures', []));
      if (t === 'quarantine') jobs.push(get('/ui/etl/quarantine?limit=100', 'quarantine', []));
      if (S.sel) jobs.push(C.call('GET', '/ui/etl/batches/' + S.sel).then(function (r) { if (r.ok) S.detail = r.body; }));
    }
    if (['overview', 'sources'].indexOf(t) >= 0) jobs.push(get('/ui/etl/sources', 'sources', []));
    if (t === 'monitoring' && can('monitor.read')) jobs.push(get('/ui/etl/monitor?window=' + S.win, 'mon'), get('/ui/etl/alerts/history?limit=30', 'history', []));
    if (t === 'account') jobs.push(get('/ui/me/mfa', 'mfa'));
    if (t === 'overview' && can('monitor.read')) jobs.push(get('/ui/etl/alerts', 'alerts', []));
    if (t === 'observability' && can('monitor.read')) {
      var q = S.logq, qs = '?level=' + q.level + (q.q ? '&q=' + encodeURIComponent(q.q) : '') + (q.trace ? '&trace=' + encodeURIComponent(q.trace) : '') + '&limit=150';
      jobs.push(get('/ui/etl/health', 'health'), get('/ui/etl/logs' + qs, 'logs', []), get('/ui/etl/counters', 'counters', []));
    }
    if (t === 'audit' && can('audit.read')) jobs.push(get('/ui/etl/audit?limit=100', 'audit', []));
    if (t === 'access') {
      if (can('users.manage')) jobs.push(get('/ui/access/users', 'users', []), get('/ui/access/mailbox', 'mailbox', []));
      if (can('users.manage') || can('roles.manage')) jobs.push(get('/ui/access/roles', 'roles', []), get('/ui/access/permissions', 'perms', []), get('/ui/etl/sources', 'sources', []));
    }
    return Promise.all(jobs).then(function () { if (!S.panel && !typing()) render(); });
  }
  function typing() { var a = document.activeElement; return a && /^(INPUT|TEXTAREA|SELECT)$/.test(a.tagName) && a.type !== 'file'; }

  // ---- overview ----------------------------------------------------------------
  function strip() {
    var s = S.sum || { by_status: {}, by_stage: [] }, held = (s.by_status.held || 0);
    return '<div class="strip"><div><b>' + C.num(s.delivered) + '</b><span>rows delivered</span></div>' +
      '<div><b>' + C.num((s.by_status.in_flight || 0) + (s.by_status.retrying || 0)) + '</b><span>batches in flight</span></div>' +
      '<div><b>' + C.num(s.quarantined) + '</b><span>rows quarantined</span></div>' +
      '<div class="' + (held ? 'attn' : '') + '"><b>' + held + '</b><span>held, need attention</span></div>' +
      '<div class="' + (s.rows_lost ? 'attn' : '') + '"><b>' + C.num(s.rows_lost) + '</b><span>rows unaccounted for</span></div></div>';
  }
  function route() {
    var s = S.sum || { by_stage: [] }, held = {};
    S.batches.forEach(function (b) { if (b.status === 'held') held[b.stage] = true; });
    return '<div class="route">' + STAGES.map(function (t, i) {
      var n = s.by_stage[i + 1] || 0, st = held[i + 1] ? ['Held', 'bad'] : n ? ['Working', 'info'] : ['Idle', ''];
      return '<div class="stage ' + (n ? 'busy' : '') + '"><span class="n">0' + (i + 1) + '</span><span class="t">' + t + '</span><span class="c">' + n + '</span><span class="pill ' + st[1] + '">' + st[0] + '</span></div>';
    }).join('') + '</div>';
  }
  function historyCard() {
    if (!S.history.length) return '';
    return '<div class="card"><h2>Alert history</h2>' + S.history.slice(0, 12).map(function (a) {
      var open = !a.cleared_at || a.cleared_at.indexOf('0001') === 0;
      return '<div class="dest"><div><strong>' + esc(a.title) + '</strong><div class="note">' + esc(a.source_id || 'system') + ' · opened ' + C.when(a.opened_at) + (open ? '' : ' · cleared ' + C.when(a.cleared_at)) + (a.acked_by ? ' · acknowledged by ' + esc(a.acked_by) : '') + '</div></div><span class="pill ' + (open ? ({ critical: 'bad', warning: 'warn', info: 'info' }[a.severity] || '') : '') + '">' + (open ? 'open' : 'cleared') + '</span></div>';
    }).join('') + '</div>';
  }
  function circuitsCard(list) {
    if (!list || !list.length) return '';
    return '<div class="card"><h2>Circuit breakers</h2>' + list.map(function (c) {
      return '<div class="dest"><div><strong>' + esc(c.destination) + '</strong><div class="note">' + c.failures + ' failures in a row' + (c.state === 'open' ? ' · paused until ' + C.time(c.open_until) : '') + '</div></div><span class="pill ' + (c.state === 'closed' ? '' : c.state === 'open' ? 'bad' : 'warn') + '">' + c.state.replace('_', ' ') + '</span></div>';
    }).join('') + '<p class="note">While a circuit is open, batches wait for the destination without using their attempts. After the cool-down one call is let through; if it works the circuit closes.</p></div>';
  }
  function countersCard() {
    var rows = S.counters.filter(function (r) { return !/_(bucket|sum|count)$/.test(r.name); });
    if (!rows.length) return '';
    return '<div class="card"><h2>Lifetime counters <span class="note">· stored in the database, they survive restarts</span></h2><div class="tablewrap"><table><thead><tr><th>Counter</th><th>Labels</th><th>Value</th></tr></thead><tbody>' + rows.map(function (r) {
      return '<tr><td class="mono">' + esc(r.name) + '</td><td class="mono note">' + esc(r.labels) + '</td><td>' + C.num(r.value) + '</td></tr>';
    }).join('') + '</tbody></table></div></div>';
  }
  function alertsCard(list, limit) {
    list = list || [];
    if (!list.length) return '<div class="card"><h2>Alerts</h2><div class="empty">Nothing needs attention.</div></div>';
    return '<div class="card"><h2>Alerts <span class="note">· ' + list.length + ' open</span></h2>' + list.slice(0, limit || 50).map(function (a) {
      var acked = a.acked_by && new Date(a.acked_until) > new Date();
      return '<div class="alert ' + (acked ? 'info' : a.severity) + '"><div class="row" style="justify-content:space-between">' + sev(a.severity) + (acked ? '<span class="pill info">Acknowledged</span>' : '') + '<span class="note">' + (a.since ? ago(a.since) : '') + '</span></div><strong>' + esc(a.title) + '</strong>' +
        '<div class="note">' + esc(a.message) + '</div>' + (acked ? '<div class="note">Silenced by ' + esc(a.acked_by) + ' until ' + C.when(a.acked_until) + (a.acked_note ? ': ' + esc(a.acked_note) : '') + '</div>' : '') +
        '<div class="row">' + (a.batch_id ? '<button class="btn small" data-act="open-batch" data-id="' + a.batch_id + '">Open ' + a.batch_id + '</button>' : '') +
        (!acked && can('advance') ? '<button class="btn small" data-act="ack-open" data-id="' + esc(a.id) + '">Acknowledge</button>' : '') + '</div>' +
        (S.ackFor === a.id ? '<div class="form" style="margin-top:8px">' + field('ack_note', 'Why (optional)', '') + '<label class="f">Silence for<select id="ack_min"><option value="60">1 hour</option><option value="240" selected>4 hours</option><option value="1440">1 day</option><option value="10080">1 week</option></select></label></div><div class="row"><button class="btn primary small" data-act="ack-save" data-id="' + esc(a.id) + '">Acknowledge</button><button class="btn small" data-act="ack-cancel">Cancel</button></div>' : '') + '</div>';
    }).join('') + '</div>';
  }
  function destinations() {
    if (!S.dests.length) return '';
    return '<div class="card"><h2>Receiving systems</h2>' + S.dests.map(function (d) {
      return '<div class="dest"><div><strong>' + esc(d.name) + '</strong><div class="note">' + C.num(d.batches) + ' batches, ' + C.num(d.rows) + ' rows' + (d.online ? '' : ' · ' + esc(d.note || 'offline')) + '</div></div>' +
        '<div class="row"><span class="pill ' + (d.online ? '' : 'bad') + '">' + (d.online ? 'Online' : 'Offline') + '</span>' +
        (can('ops.manage') ? '<button class="btn small" data-act="toggle" data-id="' + esc(d.id) + '" data-online="' + (d.online ? 0 : 1) + '">' + (d.online ? 'Take offline' : 'Bring online') + '</button>' : '') + '</div></div>';
    }).join('') + '<p class="note">Take one offline, then send a batch: delivery retries with backoff, then holds the batch at its checkpoint until you replay it.</p></div>';
  }
  function warehouse() {
    if (!S.warehouse.length) return '';
    return '<div class="card"><h2>Landed in destinations</h2><div class="tablewrap"><table><thead><tr><th>Destination</th><th>Batch</th><th>Rows</th><th>When</th></tr></thead><tbody>' + S.warehouse.slice(0, 8).map(function (w) {
      return '<tr><td>' + esc(w.destination) + '</td><td class="mono">' + esc(w.batch_id) + '</td><td>' + C.num(w.row_count) + '</td><td>' + C.when(Number(w.delivered_ms)) + '</td></tr>';
    }).join('') + '</tbody></table></div></div>';
  }
  function overview() {
    return strip() + '<section><h2>Route</h2>' + route() + '</section><div class="grid2"><div class="stack"><div class="card"><h2>Recent batches</h2>' + batchTable(S.batches, 8) + '</div>' +
      (can('monitor.read') ? alertsCard(S.alerts, 4) : '') + (can('ops.manage') || canAny('ops.manage') ? destinations() : '') + '</div><div class="stack">' + detail() + warehouse() + '</div></div>';
  }

  // ---- batches ---------------------------------------------------------------------
  function batchTable(rows, limit) {
    if (!rows.length) return '<div class="empty">No batches yet. Send a sample from the Sources tab.</div>';
    return '<div class="tablewrap"><table><thead><tr><th>Batch</th><th>Source</th><th>Rows</th><th>Quarantined</th><th>Delivered</th><th>Where</th><th>Status</th></tr></thead><tbody>' +
      rows.slice(0, limit || 100).map(function (b) {
        return '<tr tabindex="0" class="click ' + (S.sel === b.id ? 'sel' : '') + '" data-act="open" data-id="' + b.id + '"><td class="mono">' + b.id + '</td><td>' + esc(b.source_id) + '</td><td>' + C.num(b.rows_in) + '</td><td>' + (b.quarantined || '–') + '</td><td>' + (b.status === 'delivered' ? C.num(b.delivered) : '–') + '</td><td>' + stageLabel(b) + '</td><td>' + pill(b.status) + '</td></tr>';
      }).join('') + '</tbody></table></div>';
  }
  function batches() {
    var f = ['', 'in_flight', 'retrying', 'held', 'delivered', 'failed'].map(function (s) { return '<option value="' + s + '"' + (S.filter === s ? ' selected' : '') + '>' + (s ? s.replace('_', ' ') : 'All statuses') + '</option>'; }).join('');
    return '<div class="grid2"><div class="card"><div class="row" style="justify-content:space-between;margin-bottom:8px"><h2 style="margin:0">Batches</h2><select id="filter" aria-label="Filter by status">' + f + '</select></div>' + batchTable(S.batches) + '</div><div class="stack">' + detail() + '</div></div>';
  }
  function waterfall(b) {
    var ev = (b.events || []).filter(function (e) { return e.attempt > 0; });
    if (!ev.length) return '';
    var spans = ev.map(function (e) { var end = new Date(e.at).getTime(), d = Math.max(e.duration_ms || 0, 1); return { s: end - d, d: d, e: e }; });
    var t0 = Math.min.apply(null, spans.map(function (x) { return x.s; })), t1 = Math.max.apply(null, spans.map(function (x) { return x.s + x.d; })), tot = Math.max(t1 - t0, 1);
    return '<h2 style="margin-top:14px">Trace <span class="note">· ' + (tot < 1000 ? tot + ' ms' : (tot / 1000).toFixed(1) + ' s') + ' end to end</span></h2><div class="wf">' + spans.map(function (x) {
      var cls = x.e.kind === 'retry' ? 'retry' : x.e.kind === 'held' ? 'held' : 'ok';
      return '<div class="wfrow"><span class="wflabel">' + x.e.stage + ' ' + esc(STAGES[x.e.stage - 1].split(' ')[0]) + (x.e.attempt > 1 ? ' · try ' + x.e.attempt : '') + '</span><span class="wftrack"><i class="' + cls + '" style="left:' + ((x.s - t0) / tot * 100).toFixed(2) + '%;width:' + Math.max(x.d / tot * 100, 1) + '%"></i></span><span class="wfms mono">' + x.d + ' ms</span></div>';
    }).join('') + '</div>';
  }
  function failurePanel(d) {
    var b = d.batch, f = b.failures || [];
    if (!f.length && b.status !== 'failed') return '';
    var last = f[f.length - 1], html = '<h2 style="margin-top:14px">' + (b.status === 'delivered' ? 'Failures along the way' : 'What went wrong') + '</h2>';
    if (b.status === 'failed') {
      html += '<p class="err-text">' + esc(b.last_error) + '</p><p class="note">The batch was refused at intake. Nothing moved, and every refused row is kept in quarantine with its reason. Fix the data at the source and send it again.</p>';
    } else if (last && b.status !== 'delivered') {
      html += '<dl><dt>Failed at</dt><dd>Stage ' + last.stage + ', ' + esc(last.stage_name) + '</dd><dt>Error</dt><dd class="err-text">' + esc(last.error) + '</dd>' +
        '<dt>Attempt</dt><dd>' + last.attempt + ' of ' + last.max_attempts + '</dd><dt>Decision</dt><dd>' + esc(last.reason) + '</dd>' +
        (b.status === 'retrying' ? '<dt>Next attempt</dt><dd>' + C.when(b.next_attempt_at) + '</dd>' : '') +
        '<dt>Rows at risk</dt><dd>' + C.num(b.rows_in - b.quarantined) + ' rows, safe in the checkpoint; none are lost</dd>' +
        (d.resume ? '<dt>Replay resumes</dt><dd>after stage ' + d.resume.stage + ' (' + C.num(d.resume.rows) + ' rows, hash <span class="mono">' + esc(d.resume.hash.slice(0, 12)) + '</span>, written ' + C.when(d.resume.at) + ')</dd>' : '') +
        '<dt>What to do</dt><dd>' + (b.status === 'held' ? (last.permanent ? 'This error will not clear by itself. Fix the cause, then replay.' : 'Fix the cause (for example bring the receiving system back online), then replay. The same idempotency key is used, so nothing is delivered twice.') : 'Nothing; the engine retries on its own.') + '</dd></dl>';
    }
    if (f.length) html += '<div class="tablewrap"><table><thead><tr><th>#</th><th>When</th><th>Stage</th><th>Error</th><th>Outcome</th></tr></thead><tbody>' + f.map(function (x) {
      return '<tr><td>' + x.attempt + '/' + x.max_attempts + '</td><td>' + C.time(x.at) + '</td><td>' + x.stage + ' ' + esc(x.stage_name.split(' ')[0]) + '</td><td class="wrap">' + esc(x.error) + '</td><td class="wrap"><span class="pill ' + (x.outcome === 'held' ? 'bad' : 'warn') + '">' + (x.outcome === 'held' ? 'Held' : 'Retry') + '</span> <span class="note">' + esc(x.reason) + '</span></td></tr>';
    }).join('') + '</tbody></table></div>';
    return html;
  }
  function detail() {
    if (!S.detail) return '<div class="card"><h2>Lineage trace</h2><p class="note">Select a batch to see its journey, from source to destination.</p></div>';
    var d = S.detail, b = d.batch;
    var html = '<div class="card"><h2>Lineage trace</h2><div class="row" style="justify-content:space-between"><div><span class="mono">' + b.id + '</span> ' + pill(b.status) + '<div class="note">' + esc(b.source_id) + ' · ' + C.num(b.rows_in) + ' rows · key <span class="mono">' + esc(b.key) + '</span></div>' +
      '<div class="note">trace <button class="link mono" data-act="trace" data-id="' + esc(b.trace_id) + '" title="Show the logs of this trace">' + esc(b.trace_id) + '</button> · by ' + esc(b.actor) + '</div></div>' +
      (b.status === 'held' && can('replay', b.source_id) ? '<button class="btn primary" data-act="replay" data-id="' + b.id + '">Replay from checkpoint</button>' : '') + '</div>';
    html += failurePanel(d) + waterfall(b);
    html += '<h2 style="margin-top:14px">Timeline</h2><ol class="trace">' + b.events.map(function (e) { return '<li><time class="mono">' + C.time(e.at) + '</time><i class="pt ' + e.kind + '"></i><span>' + esc(e.message) + '</span></li>'; }).join('') + '</ol>';
    if ((d.lineage || []).length) html += '<h2>Lineage</h2><div class="edges">' + d.lineage.map(function (l) { return '<span class="edge">' + esc(l.parent) + ' → ' + esc(l.via) + ' → ' + esc(l.child) + '</span>'; }).join('') + '</div>';
    if ((d.quarantine || []).length) html += '<h2 style="margin-top:14px">Quarantined rows</h2>' + quarantineTable(d.quarantine.slice(0, 20), true);
    return html + '</div>';
  }
  function failures() {
    var rows = S.failures;
    if (!rows.length) return '<div class="card"><h2>Failures</h2><div class="empty">Nothing is failing. Held, retrying and refused batches appear here.</div></div>';
    return '<div class="grid2"><div class="card"><h2>Failures</h2><div class="tablewrap"><table><thead><tr><th>Batch</th><th>Source</th><th>Status</th><th>Failed at</th><th>Attempts</th><th>Error</th></tr></thead><tbody>' + rows.map(function (b) {
      var f = (b.failures || [])[(b.failures || []).length - 1];
      return '<tr tabindex="0" class="click ' + (S.sel === b.id ? 'sel' : '') + '" data-act="open" data-id="' + b.id + '"><td class="mono">' + b.id + '</td><td>' + esc(b.source_id) + '</td><td>' + pill(b.status) + '</td><td>' + (f ? 'Stage ' + f.stage : 'Intake') + '</td><td>' + (f ? f.attempt + '/' + f.max_attempts : '–') + '</td><td class="wrap">' + esc(b.last_error) + '</td></tr>';
    }).join('') + '</tbody></table></div></div><div class="stack">' + detail() + '</div></div>';
  }
  function quarantineTable(rows, compact) {
    if (!rows || !rows.length) return '<div class="empty">Nothing has been quarantined.</div>';
    return '<div class="tablewrap"><table><thead><tr>' + (compact ? '' : '<th>Batch</th><th>Source</th>') + '<th>Row</th><th>Rule</th><th>Reason</th><th>Data</th></tr></thead><tbody>' + rows.map(function (q) {
      return '<tr>' + (compact ? '' : '<td class="mono">' + q.batch_id + '</td><td>' + esc(q.source_id) + '</td>') + '<td>' + q.row_no + '</td><td>' + esc(q.rule) + '</td><td class="wrap">' + esc(q.reason) + '</td><td class="mono wrap">' + esc(JSON.stringify(q.row)) + '</td></tr>';
    }).join('') + '</tbody></table></div>';
  }

  // ---- sources ------------------------------------------------------------------------
  var BLANK = { id: 'new-source', name: 'New source', owner: 'Your team', format: 'csv', destination: 'billing-api', description: '', expect_every: '1h', max_reject_rate: 0.1, retry: { max_attempts: 4, base: '2s', cap: '30s' }, rules: [{ name: 'id present', column: 'id', check: 'required' }] };
  function sources() {
    var head = '<div class="row" style="justify-content:space-between"><h2 style="margin:0">Sources</h2>' + (can('sources.manage') ? '<button class="btn small" data-act="new-source">New source</button>' : '') + '</div>';
    if (!S.sources.length) return head + panel() + '<div class="empty">No sources you can see.</div>';
    return head + panel() + '<div class="sources">' + S.sources.map(function (s) {
      return '<div class="card"><div class="row" style="justify-content:space-between"><h3>' + esc(s.name) + '</h3>' + (s.paused ? '<span class="pill warn">Paused</span>' : '<span class="pill">Active</span>') + '</div>' +
        '<p class="note" style="margin:2px 0 0">' + esc(s.description || '') + '</p>' +
        '<dl><dt>Source id</dt><dd class="mono">' + esc(s.id) + '</dd><dt>Owner</dt><dd>' + esc(s.owner) + '</dd><dt>Format</dt><dd>' + esc(s.format) + '</dd><dt>Destination</dt><dd>' + esc(s.destination) + '</dd>' +
        '<dt>Expected</dt><dd>' + (s.expect_every && s.expect_every !== '0s' ? 'a batch every ' + esc(s.expect_every) : 'no schedule') + '</dd>' +
        '<dt>Refuse batch above</dt><dd>' + Math.round(s.max_reject_rate * 100) + '% bad rows</dd><dt>Retries</dt><dd>' + s.retry.max_attempts + ' attempts, from ' + esc(s.retry.base) + ' up to ' + esc(s.retry.cap) + '</dd><dt>Version</dt><dd>' + s.version + '</dd></dl>' +
        '<ul class="rules">' + (s.rules || []).map(function (r) { return '<li>' + esc(r.name) + '</li>'; }).join('') + '</ul><div class="row" style="margin-top:12px">' +
        (can('ingest', s.id) ? '<button class="btn primary small" data-act="sample" data-id="' + esc(s.id) + '"' + (s.paused ? ' disabled' : '') + '>Send sample</button><label class="btn small" style="cursor:pointer">Upload file<input type="file" data-act="upload" data-id="' + esc(s.id) + '" hidden></label><button class="btn small" data-act="paste" data-id="' + esc(s.id) + '">Paste data</button>' : '') +
        (can('sources.manage', s.id) ? '<button class="btn small" data-act="edit" data-id="' + esc(s.id) + '">Edit</button><button class="btn small" data-act="pause" data-id="' + esc(s.id) + '" data-paused="' + (s.paused ? 0 : 1) + '">' + (s.paused ? 'Resume' : 'Pause') + '</button>' : '') + '</div></div>';
    }).join('') + '</div>';
  }

  // ---- monitoring ---------------------------------------------------------------------------
  function chart(points) {
    if (!points.length) return '<div class="empty">No data in this window.</div>';
    var W = 900, H = 190, L = 44, B = 24, T = 8, n = points.length, bw = (W - L - 8) / n;
    var max = Math.max.apply(null, points.map(function (p) { return Math.max(p.rows_in, 1); }));
    var nice = Math.pow(10, Math.floor(Math.log10(max))), top = Math.ceil(max / nice) * nice, y = function (v) { return T + (H - T - B) * (1 - v / top); };
    var bars = points.map(function (p, i) {
      var x = L + i * bw + 1, w = Math.max(bw - 2, 1), moving = Math.max(p.rows_in - p.delivered - p.quarantined, 0);
      var d = y(p.delivered), q = y(p.delivered + p.quarantined), m = y(p.delivered + p.quarantined + moving);
      return '<g><title>' + C.when(p.at) + ': ' + p.batches + ' batches, ' + p.rows_in + ' rows, ' + p.delivered + ' delivered, ' + p.quarantined + ' quarantined' + (p.held ? ', ' + p.held + ' held' : '') + '</title>' +
        '<rect class="b-ok" x="' + x + '" y="' + d + '" width="' + w + '" height="' + (y(0) - d) + '"/>' +
        '<rect class="b-q" x="' + x + '" y="' + q + '" width="' + w + '" height="' + (d - q) + '"/>' +
        '<rect class="b-m" x="' + x + '" y="' + m + '" width="' + w + '" height="' + (q - m) + '"/>' +
        (p.held ? '<rect class="b-h" x="' + x + '" y="' + (T) + '" width="' + w + '" height="4"/>' : '') + '</g>';
    }).join('');
    var grid = [0, .5, 1].map(function (f) { var v = top * f; return '<line class="grid" x1="' + L + '" x2="' + (W - 8) + '" y1="' + y(v) + '" y2="' + y(v) + '"/><text class="axis" x="' + (L - 6) + '" y="' + (y(v) + 4) + '" text-anchor="end">' + C.num(v) + '</text>'; }).join('');
    var ticks = [0, Math.floor(n / 2), n - 1].map(function (i, k) { return '<text class="axis" x="' + (L + i * bw + bw / 2) + '" y="' + (H - 6) + '" text-anchor="' + (k === 0 ? 'start' : k === 2 ? 'end' : 'middle') + '">' + C.time(points[i].at) + '</text>'; }).join('');
    return '<svg class="chart" viewBox="0 0 ' + W + ' ' + H + '" role="img" aria-label="Rows per window: delivered, quarantined and still moving">' + grid + bars + ticks + '</svg>' +
      '<div class="legend"><span><i class="b-ok"></i>Delivered</span><span><i class="b-q"></i>Quarantined</span><span><i class="b-m"></i>Still moving or held</span><span><i class="b-h"></i>Held batch in window</span></div>';
  }
  function monitoring() {
    var m = S.mon;
    if (!m) return '<div class="empty">Loading monitoring…</div>';
    var wins = ['1h', '6h', '24h', '168h'].map(function (w) { return '<option value="' + w + '"' + (S.win === w ? ' selected' : '') + '>' + ({ '1h': 'Last hour', '6h': 'Last 6 hours', '24h': 'Last 24 hours', '168h': 'Last 7 days' }[w]) + '</option>'; }).join('');
    var q = m.queue, sw = q.sweeper_age_seconds < 0 ? ['No sweeper', 'bad'] : q.sweeper_age_seconds > 30 ? ['Stalled', 'bad'] : ['Running', ''];
    var maxAvg = Math.max.apply(null, m.stages.map(function (s) { return s.p95_ms; }).concat([1]));
    return '<div class="strip"><div><b>' + m.alerts.filter(function (a) { return a.severity === 'critical'; }).length + '</b><span>critical alerts</span></div><div><b>' + m.alerts.filter(function (a) { return a.severity === 'warning'; }).length + '</b><span>warnings</span></div>' +
      '<div><b>' + q.due + '</b><span>batches waiting to run</span></div><div><b>' + (q.oldest_due_seconds ? dur(q.oldest_due_seconds) : '–') + '</b><span>oldest wait</span></div><div><b><span class="pill ' + sw[1] + '">' + sw[0] + '</span></b><span>sweeper</span></div></div>' +
      '<div class="grid2"><div class="stack"><div class="card"><div class="row" style="justify-content:space-between"><h2 style="margin:0">Throughput</h2><select id="win" aria-label="Window">' + wins + '</select></div>' + chart(m.series) + '</div>' +
      '<div class="card"><h2>Sources</h2><div class="tablewrap"><table><thead><tr><th>Source</th><th>Freshness</th><th>Last batch</th><th>Batches</th><th>Rows</th><th>Rejected</th><th>Held</th><th>Avg</th><th>p95</th></tr></thead><tbody>' + m.sources.map(function (s) {
        return '<tr><td><strong>' + esc(s.name) + '</strong><div class="note">' + esc(s.owner) + ' → ' + esc(s.destination) + '</div></td><td>' + sev(s.paused ? 'info' : s.freshness) + (s.paused ? ' paused' : '') + '</td><td>' + (s.last_batch_at && s.last_batch_at.indexOf('0001') !== 0 ? ago(s.last_batch_at) : '–') + '</td><td>' + s.batches + '</td><td>' + C.num(s.rows_in) + '</td><td>' + (s.rows_in ? pct(s.reject_rate) : '–') + '</td><td>' + (s.held || '–') + '</td><td>' + (s.avg_seconds ? dur(s.avg_seconds) : '–') + '</td><td>' + (s.p95_seconds ? dur(s.p95_seconds) : '–') + '</td></tr>';
      }).join('') + '</tbody></table></div></div>' +
      '<div class="card"><h2>Stages</h2><div class="tablewrap"><table><thead><tr><th>Stage</th><th>Runs</th><th>Failures</th><th>Average</th><th>p95</th><th></th></tr></thead><tbody>' + m.stages.map(function (s) {
        return '<tr><td>' + s.stage + ' ' + esc(s.name) + '</td><td>' + s.runs + '</td><td>' + (s.failures ? '<span class="pill warn">' + s.failures + '</span>' : '–') + '</td><td>' + Math.round(s.avg_ms) + ' ms</td><td>' + Math.round(s.p95_ms) + ' ms</td><td style="width:30%"><span class="meter"><i style="width:' + Math.max(s.p95_ms / maxAvg * 100, s.runs ? 2 : 0) + '%"></i></span></td></tr>';
      }).join('') + '</tbody></table></div></div></div><div class="stack">' + alertsCard(m.alerts) + circuitsCard(m.circuits) + detail() + '</div></div>';
  }

  // ---- observability -----------------------------------------------------------------------
  function observability() {
    var h = S.health || { status: 'ok', checks: [] }, q = S.logq;
    var lv = ['debug', 'info', 'warn', 'error'].map(function (l) { return '<option' + (q.level === l ? ' selected' : '') + '>' + l + '</option>'; }).join('');
    return '<div class="grid2"><div class="card"><div class="row" style="justify-content:space-between;margin-bottom:8px"><h2 style="margin:0">Logs</h2><div class="row">' +
      '<select id="lv" aria-label="Minimum level">' + lv + '</select><input id="lq" type="text" placeholder="Search" value="' + esc(q.q) + '" aria-label="Search logs"><input id="ltrace" type="text" placeholder="Trace id" value="' + esc(q.trace) + '" aria-label="Trace id"><button class="btn small" data-act="logs-go">Apply</button></div></div>' +
      (S.logs.length ? '<div class="tablewrap"><table><thead><tr><th>Time</th><th>Level</th><th>Message</th><th>Batch</th><th>Trace</th><th>Details</th></tr></thead><tbody>' + S.logs.map(function (l) {
        var at = Object.keys(l.attrs || {}).map(function (k) { return k + '=' + l.attrs[k]; }).join('  ');
        return '<tr><td class="mono">' + C.time(l.at) + '</td><td><span class="pill ' + ({ error: 'bad', warn: 'warn', info: 'info', debug: '' }[l.level] || '') + '">' + l.level + '</span></td><td class="wrap">' + esc(l.message) + (l.stage ? ' <span class="note">stage ' + l.stage + '</span>' : '') + '</td><td class="mono">' + (l.batch_id ? '<button class="link" data-act="open-batch" data-id="' + l.batch_id + '">' + l.batch_id + '</button>' : '') + '</td><td class="mono">' + (l.trace_id ? '<button class="link" data-act="trace" data-id="' + esc(l.trace_id) + '">' + esc(l.trace_id) + '</button>' : '') + '</td><td class="mono wrap note">' + esc(at) + '</td></tr>';
      }).join('') + '</tbody></table></div>' : '<div class="empty">No log entries match.</div>') + '</div>' +
      '<div class="stack"><div class="card"><h2>Health <span class="pill ' + ({ ok: '', degraded: 'warn', down: 'bad' }[h.status] || '') + '">' + esc(h.status) + '</span></h2>' + (h.checks || []).map(function (c) {
        return '<div class="dest"><div><strong>' + esc(c.name) + '</strong><div class="note">' + esc(c.detail) + '</div></div><span class="pill ' + ({ ok: '', degraded: 'warn', down: 'bad' }[c.status] || '') + '">' + esc(c.status) + '</span></div>';
      }).join('') + '</div><div class="card"><h2>Scrape and probe</h2><dl><dt>Liveness</dt><dd class="mono">GET /healthz</dd><dt>Metrics</dt><dd class="mono">GET /metrics</dd><dt>Auth</dt><dd>An API key with <span class="mono">monitor.read</span> (the Monitor role). Create one for a service account under Access.</dd></dl>' +
      '<p class="note mono" style="overflow-wrap:anywhere">curl -H "X-API-Key: …" ' + esc(location.origin) + '/metrics</p><p class="note">Every log line carries the batch id and a trace id. Send <span class="mono">X-Trace-Id</span> with an upload and the same id follows the batch through every stage, the audit trail and the logs.</p></div>' + countersCard() + '</div></div>';
  }

  // ---- audit ---------------------------------------------------------------------------------
  function audit() {
    if (!can('audit.read')) return '<div class="empty">Your roles do not include the audit trail.</div>';
    var v = S.verify ? '<span class="pill ' + (S.verify.ok ? '' : 'bad') + '">' + (S.verify.ok ? 'Chain intact · ' + S.verify.total + ' entries' : 'Broken at entry ' + S.verify.bad_seq) + '</span>' : '';
    return '<div class="card"><div class="row" style="justify-content:space-between;margin-bottom:8px"><h2 style="margin:0">Audit trail</h2><div class="row">' + v + '<button class="btn small" data-act="verify">Verify the chain</button></div></div>' +
      (S.audit.length ? '<div class="tablewrap"><table><thead><tr><th>#</th><th>When</th><th>Who</th><th>Action</th><th>Source / batch</th><th>Trace</th><th>Detail</th></tr></thead><tbody>' + S.audit.map(function (a) {
        return '<tr><td class="mono">' + a.seq + '</td><td>' + C.when(a.at) + '</td><td>' + esc(a.actor) + '</td><td>' + esc(a.action) + '</td><td class="mono">' + esc(a.batch_id || a.source_id || '') + '</td><td class="mono">' + esc(a.trace_id || '') + '</td><td class="wrap">' + esc(a.detail) + '</td></tr>';
      }).join('') + '</tbody></table></div>' : '<div class="empty">No entries yet.</div>') + '<p class="note">Each entry carries the hash of the one before it, so an edit or a deletion anywhere shows up as a broken chain. Changes to users, keys, roles and sources are recorded here too.</p></div>';
  }

  // ---- access ----------------------------------------------------------------------------------
  function rolePills(csv) { return String(csv || '').split(',').filter(Boolean).map(function (r) { return '<span class="pill info">' + esc(r) + '</span>'; }).join(' ') || '<span class="note">none</span>'; }
  function access() {
    if (!can('users.manage') && !can('roles.manage')) return '<div class="empty">Your roles do not include user or role management.</div>';
    var subs = [['users', 'Users and service accounts', can('users.manage')], ['roles', 'Roles and permissions', true], ['mailbox', 'Mailbox', can('users.manage')]].filter(function (x) { return x[2]; });
    if (!subs.some(function (x) { return x[0] === S.sub; })) S.sub = subs[0][0];
    return '<div class="tabs sub">' + subs.map(function (x) { return '<button class="tab" aria-selected="' + (S.sub === x[0]) + '" data-act="sub" data-id="' + x[0] + '">' + x[1] + '</button>'; }).join('') + '</div>' + panel() + (S.sub === 'users' ? users() : S.sub === 'mailbox' ? mailbox() : roles());
  }
  function mailbox() {
    return '<div class="card"><h2>Mailbox <span class="note">· development</span></h2><p class="note">Messages the system has queued to send. Connect an email sender to deliver them; until then an administrator reads them here.</p>' +
      (S.mailbox.length ? S.mailbox.map(function (m) { return '<div class="alert info"><div class="row" style="justify-content:space-between"><strong>' + esc(m.subject) + '</strong><span class="note">' + C.when(Number(m.created_ms)) + '</span></div><div class="note">To ' + esc(m.to_email) + ' · ' + (Number(m.sent_ms) > 0 ? 'sent ' + C.when(Number(m.sent_ms)) : Number(m.attempts) >= 10 ? 'gave up after ' + m.attempts + ' tries' : Number(m.attempts) > 0 ? 'not delivered yet (' + m.attempts + ' tries): ' + esc(m.last_error) : 'waiting to be sent') + '</div><div class="mono" style="overflow-wrap:anywhere;user-select:all">' + esc(m.body) + '</div></div>'; }).join('') : '<div class="empty">No messages.</div>') + '</div>';
  }
  function users() {
    var rows = S.users, pending = rows.filter(function (u) { return u.status === 'pending'; });
    return (pending.length ? '<div class="alert warning"><strong>' + pending.length + ' account request' + (pending.length > 1 ? 's' : '') + ' waiting</strong><div class="note">Approve a request to give the person roles and let them sign in.</div></div>' : '') + '<div class="card"><div class="row" style="justify-content:space-between;margin-bottom:8px"><h2 style="margin:0">People and service accounts</h2><div class="row"><button class="btn small" data-act="user-new">New person</button><button class="btn small" data-act="svc-new">New service account</button></div></div>' +
      '<div class="tablewrap"><table><thead><tr><th>Id</th><th>Name</th><th>Type</th><th>Roles</th><th>Status</th><th>Last sign-in</th><th>Keys</th><th></th></tr></thead><tbody>' + rows.map(function (u) {
        return '<tr><td class="mono">' + esc(u.id) + '</td><td>' + esc(u.name) + '<div class="note">' + esc(u.email) + '</div></td><td>' + (u.kind === 'service' ? 'Service' : 'Person') + '</td><td>' + rolePills(u.roles) + '</td><td><span class="pill ' + (u.status === 'active' ? '' : u.status === 'pending' ? 'warn' : 'bad') + '">' + esc(u.status) + '</span>' + (Number(u.locked_until_ms) > Date.now() ? ' <span class="pill bad">locked</span>' : '') + (Number(u.mfa_enabled) === 1 ? ' <span class="pill info">2-step</span>' : '') + '</td><td>' + (u.last_login_ms > 0 ? ago(Number(u.last_login_ms)) : '–') + '</td><td>' + u.keys + '</td>' +
          '<td><div class="row">' + (u.status === 'pending' ? '<button class="btn small primary" data-act="user-edit" data-id="' + esc(u.id) + '" data-approve="1">Approve</button>' : '<button class="btn small" data-act="user-edit" data-id="' + esc(u.id) + '">Edit</button>') + (u.kind === 'human' ? '<button class="btn small" data-act="user-pw" data-id="' + esc(u.id) + '">Password</button>' : '') + (u.kind === 'human' && u.status === 'active' ? '<button class="btn small" data-act="reset-link" data-id="' + esc(u.id) + '">Reset link</button>' : '') + (Number(u.locked_until_ms) > Date.now() ? '<button class="btn small" data-act="unlock" data-id="' + esc(u.id) + '">Unlock</button>' : '') + (Number(u.mfa_enabled) === 1 ? '<button class="btn small" data-act="mfa-reset" data-id="' + esc(u.id) + '">Reset 2-step</button>' : '') + '<button class="btn small" data-act="keys" data-id="' + esc(u.id) + '">Keys</button></div></td></tr>';
      }).join('') + '</tbody></table></div></div>';
  }
  function roles() {
    var cat = S.perms;
    return '<div class="card"><div class="row" style="justify-content:space-between;margin-bottom:8px"><h2 style="margin:0">Roles</h2>' + (can('roles.manage') ? '<button class="btn small" data-act="role-new">New role</button>' : '') + '</div>' +
      '<div class="tablewrap"><table class="matrix"><thead><tr><th>Role</th>' + cat.map(function (p) { return '<th title="' + esc(p.summary) + '"><span class="vt">' + esc(p.id) + '</span></th>'; }).join('') + '<th>Sources</th><th></th></tr></thead><tbody>' + S.roles.map(function (r) {
        return '<tr><td><strong>' + esc(r.name) + '</strong><div class="note mono">' + esc(r.id) + (r.system ? ' · built in' : '') + '</div></td>' + cat.map(function (p) { return '<td class="c">' + (r.permissions.indexOf(p.id) >= 0 ? '<span class="tick" aria-label="allowed">✓</span>' : '<span class="note" aria-label="not allowed">·</span>') + '</td>'; }).join('') +
          '<td>' + (!r.sources || !r.sources.length || r.sources.indexOf('*') >= 0 ? 'All' : r.sources.map(esc).join(', ')) + '</td><td>' + (can('roles.manage') ? '<button class="btn small" data-act="role-edit" data-id="' + esc(r.id) + '">Edit</button>' : '') + '</td></tr>';
      }).join('') + '</tbody></table></div><p class="note">Hover a column title for what the permission allows. A role limited to some sources sees and acts on only those. The administrator role always holds everything.</p></div>';
  }
  function checks(name, items, selected) { return items.map(function (i) { return '<label class="check"><input type="checkbox" name="' + name + '" value="' + esc(i.v) + '"' + (selected.indexOf(i.v) >= 0 ? ' checked' : '') + '> ' + esc(i.l) + (i.n ? '<span class="note"> · ' + esc(i.n) + '</span>' : '') + '</label>'; }).join(''); }
  function picked(name) { return [].slice.call(app.querySelectorAll('input[name="' + name + '"]:checked')).map(function (x) { return x.value; }); }
  function field(id, label, val, type, extra) { return '<label class="f">' + label + '<input id="' + id + '" type="' + (type || 'text') + '" value="' + esc(val || '') + '" ' + (extra || '') + '></label>'; }
  function roleChecks(sel) { return checks('roles', S.roles.map(function (r) { return { v: r.id, l: r.name, n: r.id }; }), sel); }

  function panel() {
    var p = S.panel; if (!p) return '';
    var close = '<button class="btn" data-act="close">Cancel</button>';
    switch (p.type) {
      case 'paste': return '<div class="card"><h2>Paste data into ' + esc(p.id) + '</h2><textarea id="pasted" spellcheck="false" placeholder="Paste the file contents here (the source\'s format applies)"></textarea><div class="row" style="margin-top:10px"><button class="btn primary" data-act="paste-send" data-id="' + esc(p.id) + '">Send batch</button>' + close + '</div></div>';
      case 'source': return '<div class="card"><h2>' + (p.id ? 'Edit source ' + esc(p.id) : 'New source') + '</h2><p class="note">Rules: required, type, regex, range, enum, unique, expr. Saving bumps the version and is recorded in the audit trail.</p><textarea id="srcjson" spellcheck="false">' + esc(p.json) + '</textarea><div class="row" style="margin-top:10px"><button class="btn primary" data-act="save-source">Save source</button>' + close + '</div></div>';
      case 'user-new': return '<div class="card"><h2>New person</h2><div class="form">' + field('u_id', 'Id', '', 'text', 'placeholder="lowercase, e.g. sam"') + field('u_name', 'Name') + field('u_email', 'Email', '', 'email') + field('u_pw', 'Password (12+ characters)', '', 'password', 'autocomplete="new-password"') + field('u_pw2', 'Confirm password', '', 'password', 'autocomplete="new-password"') + '</div><div id="pwmsg" class="note" role="status"></div><h2 style="margin-top:12px">Roles</h2><div class="checks">' + roleChecks([]) + '</div><div class="row" style="margin-top:12px"><button class="btn primary" data-act="user-create">Create</button>' + close + '</div></div>';
      case 'svc-new': return '<div class="card"><h2>New service account</h2><p class="note">Service accounts have no password. Give them an API key afterwards.</p><div class="form">' + field('s_id', 'Id (starts with svc-)', 'svc-', 'text') + field('s_name', 'Name') + '</div><h2 style="margin-top:12px">Roles</h2><div class="checks">' + roleChecks([]) + '</div><div class="row" style="margin-top:12px"><button class="btn primary" data-act="svc-create">Create</button>' + close + '</div></div>';
      case 'user-edit': var u = p.user; return '<div class="card"><h2>Edit ' + esc(u.id) + '</h2><div class="form">' + field('e_name', 'Name', u.name) + (u.kind === 'service' ? '' : field('e_email', 'Email', u.email, 'email')) +
        '<label class="f">Status<select id="e_status"><option' + (u.status === 'active' ? ' selected' : '') + '>active</option><option' + (u.status === 'disabled' ? ' selected' : '') + '>disabled</option><option' + (u.status === 'pending' ? ' selected' : '') + '>pending</option></select></label></div><h2 style="margin-top:12px">Roles</h2><div class="checks">' + roleChecks(String(u.roles).split(',')) + '</div><div class="row" style="margin-top:12px"><button class="btn primary" data-act="user-save" data-id="' + esc(u.id) + '">Save</button>' + close + '</div></div>';
      case 'user-pw': return '<div class="card"><h2>Set a new password for ' + esc(p.id) + '</h2><div class="form">' + field('p_pw', 'New password (12+ characters)', '', 'password', 'autocomplete="new-password"') + field('p_pw2', 'Confirm new password', '', 'password', 'autocomplete="new-password"') + '</div><div id="pwmsg" class="note" role="status"></div><div class="row" style="margin-top:12px"><button class="btn primary" data-act="pw-save" data-id="' + esc(p.id) + '">Set password</button>' + close + '</div></div>';
      case 'link': return '<div class="card"><h2>Reset link for ' + esc(p.id) + '</h2><div class="alert info"><strong>Give this link to the person</strong><div class="mono" style="overflow-wrap:anywhere;user-select:all">' + esc(S.link || '') + '</div><div class="note">It works once and expires in 30 minutes. It is not shown again.</div></div><div class="row" style="margin-top:12px"><button class="btn" data-act="close">Done</button></div></div>';
      case 'keys': return '<div class="card"><h2>API keys for ' + esc(p.id) + '</h2>' + (S.newKey ? '<div class="alert info"><strong>New key (shown once)</strong><div class="mono" style="overflow-wrap:anywhere;user-select:all">' + esc(S.newKey) + '</div><div class="note">Copy it now. Only a digest is stored.</div></div>' : '') +
        (S.keys.length ? '<div class="tablewrap"><table><thead><tr><th>Name</th><th>Prefix</th><th>Created</th><th>By</th><th></th></tr></thead><tbody>' + S.keys.map(function (k) { return '<tr><td>' + esc(k.name) + '</td><td class="mono">' + esc(k.prefix) + '</td><td>' + (k.created_ms > 0 ? C.when(Number(k.created_ms)) : 'seeded') + '</td><td>' + esc(k.created_by) + '</td><td>' + (k.revoked ? '<span class="pill bad">revoked</span>' : '<button class="btn small danger" data-act="key-revoke" data-id="' + esc(k.id) + '" data-user="' + esc(p.id) + '">Revoke</button>') + '</td></tr>'; }).join('') + '</tbody></table></div>' : '<div class="empty">No keys yet.</div>') +
        '<div class="row" style="margin-top:12px">' + field('k_name', 'Name for a new key', '', 'text', 'placeholder="e.g. nightly export"') + '<button class="btn primary" data-act="key-new" data-id="' + esc(p.id) + '">Create key</button><button class="btn" data-act="close">Done</button></div></div>';
      case 'role': var r = p.role, srcAll = !r.sources || !r.sources.length || r.sources.indexOf('*') >= 0, groups = {};
        S.perms.forEach(function (x) { (groups[x.group] = groups[x.group] || []).push(x); });
        return '<div class="card"><h2>' + (p.isNew ? 'New role' : 'Edit role ' + esc(r.id)) + '</h2><div class="form">' + field('r_id', 'Id (lowercase)', r.id, 'text', p.isNew ? '' : 'readonly') + field('r_name', 'Name', r.name) + field('r_desc', 'Description', r.description) + '</div>' +
          (r.id === 'admin' ? '<p class="note">The administrator role always holds every permission on every source.</p>' : Object.keys(groups).map(function (g) { return '<h2 style="margin-top:12px">' + esc(g) + '</h2><div class="checks">' + checks('perm', groups[g].map(function (x) { return { v: x.id, l: x.id, n: x.summary }; }), r.permissions) + '</div>'; }).join('') +
          '<h2 style="margin-top:12px">Applies to sources</h2><label class="check"><input type="radio" name="scope" value="all"' + (srcAll ? ' checked' : '') + '> All sources</label><label class="check"><input type="radio" name="scope" value="some"' + (srcAll ? '' : ' checked') + '> Only these:</label><div class="checks">' + checks('src', S.sources.map(function (s) { return { v: s.id, l: s.name, n: s.id }; }), r.sources || []) + '</div><p class="note">Source limits apply to the data permissions; the rest apply everywhere.</p>') +
          '<div class="row" style="margin-top:12px"><button class="btn primary" data-act="role-save">Save role</button>' + (!p.isNew && !r.system ? '<button class="btn danger" data-act="role-delete" data-id="' + esc(r.id) + '">Delete</button>' : '') + close + '</div></div>';
    }
    return '';
  }

  // ---- account -----------------------------------------------------------------------------------
  function mfaCard() {
    if (!S.mfa) return '';
    if (S.mfaCodes) return '<div class="card"><h2>Two-step sign-in is on</h2><div class="alert warning"><strong>Your recovery codes</strong><div class="note">Keep these somewhere safe. Each works once if you lose your device. They are not shown again.</div><div class="mono" style="user-select:all;line-height:1.9">' + S.mfaCodes.map(esc).join('<br>') + '</div></div><div class="row" style="margin-top:12px"><button class="btn" data-act="mfa-done">I have saved them</button></div></div>';
    if (S.mfa.enabled) return '<div class="card"><h2>Two-step sign-in <span class="pill">on</span></h2><p class="note">Signing in needs a code from your authenticator app as well as your password. To turn it off, give your password and a current code (or a recovery code).</p><div class="form">' + field('mf_pw', 'Password', '', 'password', 'autocomplete="current-password"') + field('mf_code', 'Code', '', 'text', 'autocomplete="one-time-code"') + '</div><div class="row" style="margin-top:12px"><button class="btn danger" data-act="mfa-off">Turn off</button></div></div>';
    if (S.mfaSetup) return '<div class="card"><h2>Set up two-step sign-in</h2><p class="note">In your authenticator app choose <em>enter a setup key</em> and type this key (or open the link on this device), then enter the 6-digit code the app shows.</p><div class="alert info"><strong>Setup key</strong><div class="mono" style="user-select:all;overflow-wrap:anywhere">' + esc(S.mfaSetup.secret) + '</div><div class="note mono" style="overflow-wrap:anywhere;user-select:all">' + esc(S.mfaSetup.uri) + '</div></div><div class="form" style="margin-top:10px">' + field('mf_code', '6-digit code', '', 'text', 'autocomplete="one-time-code" inputmode="numeric"') + '</div><div class="row" style="margin-top:12px"><button class="btn primary" data-act="mfa-on">Turn on</button><button class="btn" data-act="mfa-cancel">Cancel</button></div></div>';
    return '<div class="card"><h2>Two-step sign-in <span class="pill warn">off</span></h2><p class="note">Add a second step to signing in: a code from an authenticator app. Recommended for anyone who can change data or manage people.</p><div class="row"><button class="btn primary" data-act="mfa-start">Set up</button></div></div>';
  }
  function account() {
    var perms = S.me.permissions || {}, cat = S.perms.length ? S.perms : Object.keys(perms).map(function (k) { return { id: k, group: '', summary: '' }; });
    return '<div class="grid2"><div class="card"><h2>Your access</h2><dl><dt>Signed in as</dt><dd>' + esc(S.me.name) + ' <span class="note mono">' + esc(S.me.id) + '</span></dd><dt>Roles</dt><dd>' + rolePills((S.me.roles || []).join(',')) + '</dd></dl>' +
      '<div class="tablewrap"><table><thead><tr><th>Permission</th><th>Applies to</th></tr></thead><tbody>' + (Object.keys(perms).sort().map(function (k) { return '<tr><td class="mono">' + esc(k) + '</td><td>' + (perms[k] === null ? 'everything' : perms[k].map(esc).join(', ') || 'nothing') + '</td></tr>'; }).join('') || '<tr><td colspan="2" class="note">No permissions: ask an administrator for a role.</td></tr>') + '</tbody></table></div></div>' +
      '<div class="stack">' + mfaCard() + '<div class="card"><h2>Change your password</h2><div class="form">' + field('c_cur', 'Current password', '', 'password', 'autocomplete="current-password"') + field('c_new', 'New password (12+ characters)', '', 'password', 'autocomplete="new-password"') + field('c_new2', 'Confirm new password', '', 'password', 'autocomplete="new-password"') + '</div><div id="pwmsg" class="note" role="status"></div><div class="row" style="margin-top:12px"><button class="btn primary" data-act="pw-change">Change password</button></div><p class="note">Changing your password ends all your sessions, this one too, and you sign in again.</p></div></div></div>';
  }

  var VIEWS = { overview: overview, monitoring: monitoring, observability: observability, batches: batches, failures: failures, sources: sources, quarantine: function () { return '<div class="card"><h2>Quarantined rows</h2>' + quarantineTable(S.quarantine) + '</div>'; }, audit: audit, access: access, account: account };

  function render() {
    var active = document.activeElement && document.activeElement.id;
    var tabs = [['overview', 'Overview', can('read')], ['monitoring', 'Monitoring', can('monitor.read')], ['batches', 'Batches', can('read')], ['failures', 'Failures', can('read')],
      ['sources', 'Sources', can('read') || can('ingest') || can('sources.manage') || can('monitor.read')], ['quarantine', 'Quarantine', can('read')], ['observability', 'Observability', can('monitor.read')],
      ['audit', 'Audit trail', can('audit.read')], ['access', 'Access', can('users.manage') || can('roles.manage')], ['account', 'Account', true]].filter(function (t) { return t[2]; });
    if (!tabs.some(function (t) { return t[0] === S.tab; })) S.tab = tabs[0][0];
    app.innerHTML = '<div class="shell"><div class="bar"><div class="brand">Pipeline control room <small>data in motion, under control</small></div>' +
      '<div class="who"><span>' + esc(S.me.name) + ' · ' + esc((S.me.roles || []).join(', ')) + '</span><button class="btn small" data-act="theme">Theme</button><button class="btn small" data-act="logout">Sign out</button></div></div>' +
      '<div class="tabs" role="tablist">' + tabs.map(function (t) { return '<button class="tab" role="tab" aria-selected="' + (S.tab === t[0]) + '" data-act="tab" data-id="' + t[0] + '">' + t[1] + '</button>'; }).join('') + '</div>' + VIEWS[S.tab]() + '</div>';
    if (active) { var el = document.getElementById(active); if (el) el.focus(); }
  }

  // ---- samples ------------------------------------------------------------------------------------
  function stamp() { return new Date().toISOString().replace(/[-:T.Z]/g, '').slice(0, 14) + Math.floor(Math.random() * 90 + 10); }
  var SAMPLES = {
    'orders-sftp': function (n) {
      var rows = ['id,amount,currency,email'], cur = ['NPR', 'USD', 'INR'];
      for (var i = 1; i <= 24; i++) rows.push([n + '-' + i, (Math.random() * 400 + 5).toFixed(2), cur[i % 3], 'buyer' + i + '@example.com'].join(','));
      rows.push(n + '-bad1,-12.00,NPR,refund@example.com'); rows.push(n + '-bad2,19.00,XXX,someone@example.com');
      return rows.join('\n');
    },
    'crm-export': function (n) {
      var tiers = ['free', 'pro', 'enterprise'], out = [];
      for (var i = 1; i <= 18; i++) out.push(JSON.stringify({ id: n + '-c' + i, email: 'person' + i + '@example.com', tier: tiers[i % 3] }));
      out.push(JSON.stringify({ id: n + '-cbad', email: 'not-an-email', tier: 'pro' }));
      return out.join('\n');
    },
    'claims-api': function (n) {
      var out = [];
      for (var i = 1; i <= 12; i++) out.push({ claim_id: n + '-cl' + i, filed_on: '2026-10-0' + (i % 9 + 1), amount: Math.round(Math.random() * 90000) });
      return JSON.stringify(out);
    },
    'ledger-feed': function (n) {
      var rows = ['line,account,debit,credit'];
      for (var i = 1; i <= 15; i++) { var v = (Math.random() * 5000).toFixed(2); rows.push([n + '-l' + i, '4' + (100 + i), v, v].join(',')); }
      return rows.join('\n');
    }
  };
  function send(id, data, label) {
    return C.call('POST', '/ui/etl/sources/' + id + '/batches', { key: id + '-' + stamp(), data: data }).then(function (r) {
      if (!r.ok) { C.toast(C.problem(r), true); return; }
      var b = r.body.batch;
      C.toast(b.status === 'failed' ? 'Refused at intake: ' + b.last_error : (label || 'Sent') + ': ' + b.rows_in + ' rows, ' + b.quarantined + ' quarantined');
      S.sel = b.id; S.detail = null; if (can('read')) S.tab = 'batches'; return load();
    });
  }
  function done(r, ok) { if (r.ok) { C.toast(ok); S.panel = null; } else C.toast(C.problem(r), true); return load(); }
  function val(id) { var e = document.getElementById(id); return e ? e.value : ''; }
  function openKeys(id) { return C.call('GET', '/ui/access/users/' + id + '/keys').then(function (r) { S.keys = r.ok ? r.body : []; S.panel = { type: 'keys', id: id }; render(); }); }

  // ---- actions --------------------------------------------------------------------------------------
  var ACTS = {
    tab: function (el) { S.tab = el.dataset.id; S.panel = null; S.newKey = null; load(); },
    sub: function (el) { S.sub = el.dataset.id; S.panel = null; render(); },
    theme: function () { C.toggleTheme(); },
    logout: function () { C.call('POST', '/logout').then(function () { location.href = '/login'; }); },
    open: function (el) { S.sel = el.dataset.id; S.detail = null; render(); load(); },
    'open-batch': function (el) { S.sel = el.dataset.id; S.detail = null; S.tab = 'batches'; S.panel = null; load(); },
    trace: function (el) { S.logq = { level: 'debug', q: '', trace: el.dataset.id }; S.tab = 'observability'; S.panel = null; load(); },
    'logs-go': function () { S.logq = { level: val('lv'), q: val('lq'), trace: val('ltrace') }; load(); },
    replay: function (el) { C.call('POST', '/ui/etl/batches/' + el.dataset.id + '/replay').then(function (r) { C.toast(r.ok ? 'Replaying from the last checkpoint' : C.problem(r), !r.ok); load(); }); },
    toggle: function (el) {
      var online = el.dataset.online === '1';
      C.call('PUT', '/ui/etl/destinations/' + el.dataset.id, { online: online, note: online ? '' : 'taken offline from the console' }).then(function (r) { C.toast(r.ok ? (online ? 'Back online' : 'Offline: deliveries will retry') : C.problem(r), !r.ok); load(); });
    },
    pause: function (el) { C.call('POST', '/ui/etl/sources/' + el.dataset.id + '/pause', { paused: el.dataset.paused === '1' }).then(function (r) { if (!r.ok) C.toast(C.problem(r), true); load(); }); },
    sample: function (el) { var f = SAMPLES[el.dataset.id]; if (!f) { C.toast('No sample for this source; upload a file instead', true); return; } el.disabled = true; send(el.dataset.id, f(stamp()), 'Sample sent').then(function () { el.disabled = false; }); },
    verify: function () { C.call('GET', '/ui/etl/audit/verify').then(function (r) { S.verify = r.ok ? r.body : null; if (!r.ok) C.toast(C.problem(r), true); render(); }); },
    close: function () { S.panel = null; S.newKey = null; render(); },
    paste: function (el) { S.panel = { type: 'paste', id: el.dataset.id }; render(); },
    'paste-send': function (el) { var t = val('pasted'); if (!t.trim()) { C.toast('Paste some data first', true); return; } send(el.dataset.id, t, 'Sent').then(function () { S.panel = null; }); },
    edit: function (el) { var src = S.sources.filter(function (x) { return x.id === el.dataset.id; })[0]; var o = JSON.parse(JSON.stringify(src)); ['version', 'paused', 'created_at', 'updated_at'].forEach(function (k) { delete o[k]; }); S.panel = { type: 'source', id: src.id, json: JSON.stringify(o, null, 2) }; render(); },
    'new-source': function () { S.panel = { type: 'source', id: '', json: JSON.stringify(BLANK, null, 2) }; render(); },
    'save-source': function () {
      var o; try { o = JSON.parse(val('srcjson')); } catch (e) { C.toast('That is not valid JSON: ' + e.message, true); return; }
      C.call('PUT', '/ui/etl/sources', o).then(function (r) { done(r, r.ok ? 'Saved ' + r.body.id + ' as version ' + r.body.version : ''); });
    },
    'user-new': function () { S.panel = { type: 'user-new' }; render(); },
    'svc-new': function () { S.panel = { type: 'svc-new' }; render(); },
    'user-create': function () {
      var bad = C.passwordProblem(val('u_pw'), val('u_pw2')); if (bad) { C.toast(bad, true); return; }
      C.call('POST', '/ui/access/users', { id: val('u_id'), name: val('u_name'), email: val('u_email'), password: val('u_pw'), confirm: val('u_pw2'), roles: picked('roles').join(',') }).then(function (r) { done(r, 'Person created'); });
    },
    'svc-create': function () { C.call('POST', '/ui/access/services', { id: val('s_id'), name: val('s_name'), roles: picked('roles').join(',') }).then(function (r) { done(r, 'Service account created; now give it a key'); }); },
    'user-edit': function (el) { var u = JSON.parse(JSON.stringify(S.users.filter(function (x) { return x.id === el.dataset.id; })[0])); if (el.dataset.approve) u.status = 'active'; S.panel = { type: 'user-edit', user: u }; render(); },
    'user-save': function (el) {
      var u = S.panel.user;
      C.call('PUT', '/ui/access/users/' + el.dataset.id, { name: val('e_name'), email: u.kind === 'service' ? '-' : val('e_email'), roles: picked('roles').join(','), status: val('e_status') }).then(function (r) { done(r, 'Saved ' + el.dataset.id); });
    },
    'user-pw': function (el) { S.panel = { type: 'user-pw', id: el.dataset.id }; render(); },
    'pw-save': function (el) {
      var bad = C.passwordProblem(val('p_pw'), val('p_pw2')); if (bad) { C.toast(bad, true); return; }
      C.call('PUT', '/ui/access/users/' + el.dataset.id + '/password', { password: val('p_pw'), confirm: val('p_pw2') }).then(function (r) { done(r, 'Password set'); });
    },
    'reset-link': function (el) { C.call('POST', '/ui/access/users/' + el.dataset.id + '/reset-link', {}).then(function (r) { if (r.ok) { S.link = r.body.link; S.panel = { type: 'link', id: el.dataset.id }; render(); } else C.toast(C.problem(r), true); }); },
    'ack-open': function (el) { S.ackFor = el.dataset.id; render(); },
    'ack-cancel': function () { S.ackFor = null; render(); },
    'ack-save': function (el) { C.call('POST', '/ui/etl/alerts/ack', { id: el.dataset.id, note: val('ack_note'), minutes: Number(val('ack_min')) }).then(function (r) { S.ackFor = null; C.toast(r.ok ? 'Acknowledged' : C.problem(r), !r.ok); load(); }); },
    unlock: function (el) { C.call('POST', '/ui/access/users/' + el.dataset.id + '/unlock', {}).then(function (r) { C.toast(r.ok ? 'Unlocked' : C.problem(r), !r.ok); load(); }); },
    'mfa-reset': function (el) { C.call('POST', '/ui/access/users/' + el.dataset.id + '/mfa-reset', {}).then(function (r) { C.toast(r.ok ? 'Two-step sign-in turned off for ' + el.dataset.id : C.problem(r), !r.ok); load(); }); },
    'mfa-start': function () { C.call('POST', '/ui/me/mfa/setup', {}).then(function (r) { if (r.ok) { S.mfaSetup = r.body; render(); } else C.toast(C.problem(r), true); }); },
    'mfa-cancel': function () { S.mfaSetup = null; render(); },
    'mfa-on': function () { C.call('POST', '/ui/me/mfa/enable', { code: val('mf_code') }).then(function (r) { if (r.ok) { S.mfaSetup = null; S.mfaCodes = r.body.recovery_codes; S.mfa = { enabled: true }; render(); } else C.toast(r.body.error && r.body.error.code === 'INVALID_CODE' ? 'That code is not right. Check the app and try the next code.' : C.problem(r), true); }); },
    'mfa-done': function () { S.mfaCodes = null; load(); },
    'mfa-off': function () { C.call('POST', '/ui/me/mfa/disable', { password: val('mf_pw'), code: val('mf_code') }).then(function (r) { if (r.ok) { C.toast('Two-step sign-in is off'); S.mfa = { enabled: false }; load(); } else C.toast(r.status === 401 ? 'The password is wrong' : r.body.error && r.body.error.code === 'INVALID_CODE' ? 'That code is not right.' : C.problem(r), true); }); },
    'pw-change': function () {
      var bad = C.passwordProblem(val('c_new'), val('c_new2')); if (bad) { C.toast(bad, true); return; }
      if (val('c_new') === val('c_cur')) { C.toast('The new password must be different from the current one.', true); return; }
      C.call('PUT', '/ui/me/password', { current: val('c_cur'), password: val('c_new'), confirm: val('c_new2') }).then(function (r) { if (r.ok) { C.toast('Password changed. Signing you out…'); C.call('POST', '/logout').then(function () { location.href = '/login?changed=1'; }); } else C.toast(r.status === 401 ? 'The current password is wrong' : C.problem(r), true); });
    },
    keys: function (el) { S.newKey = null; openKeys(el.dataset.id); },
    'key-new': function (el) { C.call('POST', '/ui/access/users/' + el.dataset.id + '/keys', { name: val('k_name') || 'key' }).then(function (r) { if (r.ok) { S.newKey = r.body.api_key; openKeys(el.dataset.id); load(); } else C.toast(C.problem(r), true); }); },
    'key-revoke': function (el) { C.call('DELETE', '/ui/access/keys/' + el.dataset.id).then(function (r) { C.toast(r.ok ? 'Key revoked' : C.problem(r), !r.ok); S.newKey = null; openKeys(el.dataset.user); load(); }); },
    'role-new': function () { S.panel = { type: 'role', isNew: true, role: { id: '', name: '', description: '', permissions: [], sources: [] } }; render(); },
    'role-edit': function (el) { S.panel = { type: 'role', role: JSON.parse(JSON.stringify(S.roles.filter(function (r) { return r.id === el.dataset.id; })[0])) }; render(); },
    'role-save': function () {
      var p = S.panel, scope = (app.querySelector('input[name="scope"]:checked') || {}).value, id = val('r_id');
      var role = { id: id, name: val('r_name'), description: val('r_desc'), permissions: p.role.id === 'admin' ? [] : picked('perm'), sources: scope === 'some' ? picked('src') : [] };
      if (scope === 'some' && !role.sources.length) { C.toast('Pick at least one source, or choose All sources', true); return; }
      C.call('PUT', '/ui/access/roles', role).then(function (r) { done(r, 'Saved role ' + id); });
    },
    'role-delete': function (el) { C.call('DELETE', '/ui/access/roles/' + el.dataset.id).then(function (r) { done(r, 'Role deleted'); }); }
  };
  app.addEventListener('click', function (e) { var el = e.target.closest('[data-act]'); if (el && ACTS[el.dataset.act] && el.tagName !== 'INPUT') ACTS[el.dataset.act](el); });
  app.addEventListener('change', function (e) {
    if (e.target.id === 'filter') { S.filter = e.target.value; load(); return; }
    if (e.target.id === 'win') { S.win = e.target.value; load(); return; }
    if (e.target.dataset.act === 'upload' && e.target.files[0]) {
      var id = e.target.dataset.id, file = e.target.files[0], rd = new FileReader();
      rd.onload = function () { send(id, String(rd.result), 'Uploaded ' + file.name); }; rd.readAsText(file);
    }
  });
  // While a second password is typed, say whether the two match.
  app.addEventListener('input', function (e) {
    var pairs = { u_pw2: 'u_pw', p_pw2: 'p_pw', c_new2: 'c_new' }, first = pairs[e.target.id], msg = document.getElementById('pwmsg');
    if (!first || !msg) return;
    var same = val(first) === e.target.value;
    msg.className = same ? 'ok-text' : 'err-text'; msg.textContent = e.target.value ? (same ? 'The passwords match.' : 'The passwords do not match.') : '';
  });
  app.addEventListener('keydown', function (e) {
    if (e.key === 'Enter' && e.target.matches('tr.click')) ACTS.open(e.target);
    if (e.key === 'Enter' && e.target.matches('#lq,#ltrace')) ACTS['logs-go']();
  });

  C.call('GET', '/ui/me').then(function (r) {
    S.me = r.body;
    if (S.me.mfa_required) { location.href = '/login?mfa=1'; return new Promise(function () {}); }
    if (!can('read') && can('ingest')) S.tab = 'sources';
    return load();
  }).then(function () {
    render();
    setInterval(function () { if (!document.hidden) load(); }, 2000);
    // Coming back to a tab checks the session at once, so a revoked account is signed out now, not at the next tick.
    document.addEventListener('visibilitychange', function () { if (!document.hidden) load(); });
  });
})();
