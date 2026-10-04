UI.page(function (ctx) {
  var $ = UI.$, el = UI.el;
  if (!ctx.admin) { $('#main').textContent = ''; $('#main').appendChild(UI.empty('Operator only', 'Sign in as the operator to see this page.', 'shield')); return; }
  var COLORS = { delivered: '#16a34a', submitted: '#06b6d4', queued: '#6366f1', dispatching: '#6366f1', failed: '#dc2626' };
  var first = true;

  function kpis(s, providers, approvals) {
    var day = (s.day && s.day[0]) || {}, money = (s.money && s.money[0]) || {}, active = providers.filter(function (p) { return p.state === 'active'; }).length;
    var rate = day.total ? Math.round(100 * day.delivered / day.total) : null, host = $('#kpis'); host.textContent = '';
    var items = [
      ['send', 'Messages, 24 h', day.total || 0, '', (day.failed || 0) + ' failed', UI.num, ''], ['check', 'Delivered', rate == null ? 0 : rate, rate == null || rate >= 90 ? 'ok' : 'warn', rate == null ? 'no traffic yet' : 'of the last 24 h', function (n) { return Math.round(n) + '%'; }, ''],
      ['dollar', 'Revenue, 24 h', (day.revenue || 0) / 1e6, 'info', 'held ' + UI.money((money.held || 0) / 1e6, 3) + ' of ' + UI.money((money.balance || 0) / 1e6, 2) + ' balances', function (n) { return UI.money(n, 3); }, ''],
      ['server', 'Providers active', active, active === providers.length ? 'ok' : 'warn', providers.length - active ? (providers.length - active) + ' paused or disabled' : 'all running', UI.num, ''],
      ['inbox', 'Waiting for approval', approvals.length, approvals.length ? 'warn' : '', approvals.length ? 'campaigns need a decision' : 'nothing waiting', UI.num, '']
    ];
    items.forEach(function (k, i) {
      var v = el('div', { class: 'value' }, [first ? '0' : k[5](k[2])]);
      host.appendChild(el('div', { class: 'kpi ' + k[3], style: { animationDelay: i * 50 + 'ms' } }, [el('div', { class: 'label', text: k[1] }), v, el('div', { class: 'sub', text: k[4] }), el('div', { class: 'ico-bg' }, [UI.icon(k[0], 20)])]));
      if (first) UI.countUp(v, k[2], k[5]);
    });
  }
  function hourly(rows) {
    var nowH = Math.floor(Date.now() / 3600000), byHour = {}; (rows || []).forEach(function (r) { byHour[r.hour] = r; });
    var data = [], total = 0; for (var h = nowH - 23; h <= nowH; h++) { var r = byHour[h]; total += r ? r.n : 0; data.push({ label: new Date(h * 3600000).getHours() + 'h', value: r ? r.n : 0 }); }
    var host = $('#hourly'); host.textContent = ''; $('#hourly-note').textContent = total + ' accepted';
    if (!total) { host.appendChild(UI.empty('No traffic in the last 24 hours', 'Messages appear here as they are accepted.', 'activity')); return; }
    host.appendChild(UI.bars(data, { label: 'Messages accepted per hour' }));
  }
  function states(rows) {
    var host = $('#states'); host.textContent = ''; var tot = {}; (rows || []).forEach(function (r) { tot[r.state] = (tot[r.state] || 0) + r.n; });
    var segs = Object.keys(tot).map(function (k, i) { return { label: k, value: tot[k], color: COLORS[k] || UI.palette[(i + 3) % 8] }; }), sum = segs.reduce(function (a, s) { return a + s.value; }, 0);
    if (!sum) { host.appendChild(UI.empty('No messages yet', '', 'send')); return; }
    host.appendChild(UI.donut(segs, String(sum), 'messages'));
    var lg = el('div', { class: 'legend', style: { flexDirection: 'column' } }); segs.forEach(function (g) { lg.appendChild(el('span', {}, [el('i', { style: { background: g.color } }), g.label + ' ', el('strong', { text: g.value })])); }); host.appendChild(lg);
  }
  function providers(list) {
    UI.table($('#providers'), { rows: list, empty: { title: 'No providers', icon: 'server' }, columns: [
      { label: 'Provider', render: function (p) { return el('div', {}, [el('span', { class: 'dot ' + (p.state === 'active' ? 'ok' : p.state === 'paused' ? 'warn' : 'err') + (p.state === 'active' ? ' pulse' : '') }), ' ', el('strong', { text: p.id }), el('span', { class: 'sub', text: p.channel + (p.sandbox ? ' · sandbox' : ' · live') })]); } },
      { label: 'Sent', num: true, render: function (p) { return UI.num(p.successes); } }, { label: 'Failed', num: true, render: function (p) { return el('span', { class: p.failures ? 'pill err' : '', text: UI.num(p.failures) }); } },
      { label: 'On', render: function (p) {
        var cb = el('input', { type: 'checkbox', checked: p.state === 'active', 'aria-label': (p.state === 'active' ? 'Pause ' : 'Resume ') + p.id });
        cb.addEventListener('change', function () {
          var to = cb.checked ? 'active' : 'paused';
          UI.confirm({ title: (cb.checked ? 'Resume ' : 'Pause ') + p.id + '?', text: cb.checked ? 'It will carry messages again right away.' : 'New messages will use the next provider. Messages already queued for it fail over.', confirm: cb.checked ? 'Resume' : 'Pause', danger: !cb.checked }).then(function (yes) {
            if (!yes) { cb.checked = !cb.checked; return; }
            UI.call('PUT', '/ui/admin/providers/' + p.id + '/state', { state: to }).then(function (r) { if (r.ok) { UI.toast(p.id + ' is now ' + to, 'ok'); load(); } else { cb.checked = !cb.checked; UI.fail(UI.problem(r)); } });
          });
        });
        return el('label', { class: 'switch' }, [cb, el('span', { class: 'slider' })]);
      } }] });
  }
  function approvals(list) {
    var host = $('#approvals'), pill = $('#approval-count'); pill.hidden = !list.length; pill.textContent = list.length + ' open';
    host.textContent = ''; if (!list.length) { host.appendChild(UI.empty('Nothing is waiting', 'Campaigns submitted for approval show up here.', 'check')); return; }
    list.forEach(function (t, i) {
      var d = t.data || {}, row = el('div', { class: 'nitem', style: { animation: 'rise .3s var(--ease) both', animationDelay: i * 50 + 'ms', alignItems: 'center' } }, [
        el('div', { style: { flex: 1 } }, [el('strong', { text: t.title }), el('div', { class: 'muted', text: 'Submitted by ' + (d.user_id || 'an account') + (d.skipped ? ' · ' + d.skipped + ' invalid numbers will be skipped' : '') })]),
        el('button', { type: 'button', class: 'small', onclick: function (e) { decide(t, 'approve', e.currentTarget); } }, ['Approve']),
        el('button', { type: 'button', class: 'small secondary danger-text', onclick: function (e) { decide(t, 'reject', e.currentTarget); } }, ['Reject'])]);
      host.appendChild(row);
    });
  }
  function decide(t, action, btn) {
    UI.confirm({ title: (action === 'approve' ? 'Approve' : 'Reject') + ' this campaign?', text: action === 'approve' ? 'Every valid recipient will be sent the message and charged to the account.' : 'Nothing will be sent. The account is told it was rejected.', confirm: action === 'approve' ? 'Approve and send' : 'Reject', danger: action === 'reject' }).then(function (yes) {
      if (!yes) return; UI.busy(btn, UI.call('POST', '/ui/admin/approvals/' + t.task_id, { action: action }).then(function (r) { if (r.ok) { UI.toast(action === 'approve' ? 'Approved. Sending has started.' : 'Rejected.', 'ok'); setTimeout(load, 400); } else UI.fail(UI.problem(r)); }));
    });
  }
  function recent(list) {
    UI.table($('#recent'), { rows: list.slice(0, 12), empty: { title: 'No messages yet', icon: 'send' }, columns: [
      { label: 'When', render: function (m) { return UI.ago(m.created_ms); } }, { label: 'Account', key: 'user_id' }, { label: 'To', key: 'to' }, { label: 'State', render: function (m) { return UI.state(m.state); } },
      { label: 'Provider', key: 'provider' }, { label: 'Problem', render: function (m) { return m.err_code ? el('span', { class: 'pill err', text: m.err_code }) : ''; } }] });
  }
  function load() {
    return Promise.all([UI.call('GET', '/ui/admin/stats'), UI.call('GET', '/ui/admin/providers'), UI.call('GET', '/ui/admin/approvals'), UI.call('GET', '/ui/admin/messages')]).then(function (rs) {
      var s = rs[0].body || {}, p = Array.isArray(rs[1].body) ? rs[1].body : [], a = Array.isArray(rs[2].body) ? rs[2].body : [], m = Array.isArray(rs[3].body) ? rs[3].body : [];
      kpis(s, p, a); hourly(s.hourly); states(s.states); providers(p); approvals(a); recent(m); first = false; $('#updated').textContent = 'Updated ' + new Date().toLocaleTimeString();
    });
  }
  $('#refresh').addEventListener('click', function (e) { UI.busy(e.currentTarget, load()); });
  ['#kpis', '#hourly', '#providers', '#approvals', '#recent'].forEach(function (s) { $(s).appendChild(UI.skeleton(3)); });
  load(); setInterval(function () { if (!document.hidden) load(); }, 10000);
});
