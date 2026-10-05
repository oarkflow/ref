UI.page(function () {
  var $ = UI.$, el = UI.el, st = { rows: [], filter: '', q: '', timer: null };
  // The account's own status words, from rules/status.bcl. An account is not told
  // which carrier carries its messages, so there is no "via" column and no
  // per-carrier attempt list: the gateway routes, and says how it went.
  var FILTERS = [['', 'All'], ['accepted', 'Accepted'], ['sending', 'Sending'], ['sent', 'Sent'], ['delivered', 'Delivered'], ['failed', 'Failed']];

  function kpis() {
    UI.call('GET', '/ui/balance').then(function (r) {
      if (!r.ok) return; var b = r.body, host = $('#kpis'); host.textContent = '';
      var count = function (s) { return st.rows.filter(function (m) { return m.status === s; }).length; };
      var delivered = count('delivered'), failed = count('failed'), sent = count('sent');
      [['dollar', 'Available balance', b.available, 'ok', 'held ' + UI.money(b.held, 3)],
       ['send', 'Messages (latest 100)', st.rows.length, '', ''],
       ['check', 'Delivered', delivered, 'ok', st.rows.length ? Math.round(100 * delivered / st.rows.length) + '% of these' : ''],
       ['alert', 'Failed', failed, failed ? 'err' : '', failed ? 'refunded automatically' : 'none']]
        .forEach(function (k, i) {
          var v = el('div', { class: 'value' }); host.appendChild(el('div', { class: 'kpi ' + k[3], style: { animationDelay: i * 50 + 'ms' } }, [el('div', { class: 'label', text: k[1] }), v, el('div', { class: 'sub', text: k[4] }), el('div', { class: 'ico-bg' }, [UI.icon(k[0], 20)])]));
          UI.countUp(v, k[2], i === 0 ? function (n) { return UI.money(n, 3); } : UI.num);
        });
    });
  }
  function filters() {
    var host = $('#filters'); host.textContent = '';
    FILTERS.forEach(function (f) {
      var n = st.rows.filter(function (m) { return !f[0] || m.status === f[0]; }).length;
      host.appendChild(el('button', { type: 'button', class: 'chip' + (st.filter === f[0] ? ' on' : ''), onclick: function () { st.filter = f[0]; render(); } }, [f[1] + ' ', el('span', { class: 'faint', text: n })]));
    });
  }
  function visible() {
    return st.rows.filter(function (m) {
      if (st.filter && m.status !== st.filter) return false;
      return !st.q || (m.to + ' ' + m.id + ' ' + (m.status || '') + ' ' + (m.from || '')).toLowerCase().indexOf(st.q) >= 0;
    });
  }
  function render() {
    filters();
    UI.table($('#list'), { rows: visible(), onRow: open, empty: { title: st.rows.length ? 'No messages match' : 'No messages yet', text: st.rows.length ? 'Change the filter or the search.' : 'Send one from the Send page and it appears here.', icon: 'send' }, columns: [
      { label: 'When', render: function (m) { return el('span', { title: UI.when(m.created_ms), text: UI.ago(m.created_ms) }); } },
      { label: 'To', render: function (m) { return el('div', {}, [el('strong', { text: m.to }), el('span', { class: 'sub', text: m.from || '' })]); } },
      { label: 'Status', render: function (m) { return UI.state(m.status); } },
      { label: 'Segments', num: true, key: 'segments' },
      { label: 'Price', num: true, render: function (m) { return UI.micros(m.price) + ' ' + (m.currency || ''); } }
    ] });
  }
  function load() {
    return UI.call('GET', '/ui/messages').then(function (r) { if (r.ok) { st.rows = Array.isArray(r.body) ? r.body : []; render(); kpis(); } });
  }
  // The account's view of one message: its own fields, a status word, and the
  // three timestamps a timeline needs. Nothing about how it was routed.
  function open(m) {
    var d = UI.drawer({ title: 'Message to ' + m.to, subtitle: m.id }), body = d.body; body.appendChild(UI.skeleton(4));
    UI.call('GET', '/ui/messages/' + m.id).then(function (r) {
      body.textContent = ''; if (!r.ok) { body.appendChild(el('div', { class: 'banner errb', text: UI.problem(r) })); return; }
      var x = r.body;
      body.appendChild(el('section', { class: 'card' }, [el('div', { class: 'split' }, [el('h2', { text: 'Summary' }), UI.state(x.status)]),
        el('p', { class: 'muted', text: x.detail || '' }),
        el('div', { class: 'kv' }, [
          el('div', { html: '<span class="muted">To</span> ' + x.to + ' (' + x.country + ')' }), el('div', { html: '<span class="muted">Sender</span> ' + (x.from || '') }), el('div', { html: '<span class="muted">Type</span> ' + x.type }),
          el('div', { html: '<span class="muted">Segments</span> ' + x.segments + ' · ' + x.encoding }), el('div', { html: '<span class="muted">Price</span> ' + UI.micros(x.price) + ' ' + x.currency }),
          el('div', { html: '<span class="muted">Receipt</span> ' + (x.receipt ? 'expected' : 'none') }),
          el('div', { html: '<span class="muted">Accepted</span> ' + UI.when(x.created_ms) }), el('div', { html: '<span class="muted">Sent</span> ' + (x.submitted_ms ? UI.when(x.submitted_ms) : '—') }), el('div', { html: '<span class="muted">Delivered</span> ' + (x.delivered_ms ? UI.when(x.delivered_ms) : '—') })]),
        x.failure ? el('div', { class: 'banner errb' }, [el('strong', { text: x.failure.replace(/_/g, ' ') }), el('div', { text: x.detail || '' })]) : null]));

      var tl = el('div', { class: 'timeline' });
      [[x.created_ms, 'Accepted', 'ok'], [x.submitted_ms, 'Sent to the network', 'ok'], [x.delivered_ms, 'Delivered', 'ok']].forEach(function (e) {
        if (e[0]) tl.appendChild(el('div', { class: 'ev ' + e[2] }, [el('strong', { text: e[1] }), el('div', { class: 'muted', text: UI.when(e[0]) })]));
      });
      if (x.status === 'failed') tl.appendChild(el('div', { class: 'ev err' }, [el('strong', { text: 'Failed' }), el('div', { class: 'muted', text: x.detail || '' })]));
      if (x.status === 'sent' && x.receipt) tl.appendChild(el('div', { class: 'ev warn' }, [el('strong', { text: 'Waiting for a delivery receipt' })]));
      body.appendChild(el('section', { class: 'card' }, [el('h2', { text: 'Progress' }), tl]));
    });
  }
  function ledger() {
    UI.table($('#ledger'), { rows: [], empty: { title: 'Loading…' }, columns: [] });
    UI.call('GET', '/ui/ledger').then(function (r) {
      UI.table($('#ledger'), { rows: Array.isArray(r.body) ? r.body : [], empty: { title: 'No money has moved yet', text: 'Holds and charges appear here.', icon: 'dollar' }, columns: [
        { label: 'When', render: function (l) { return UI.ago(l.at_ms); } }, { label: 'Message', render: function (l) { return el('code', { text: l.message_id }); } },
        { label: 'Entry', render: function (l) { return el('span', { class: 'pill ' + ({ charge: 'info', hold: 'warn', release: 'ok', refund: 'ok' }[l.kind] || ''), text: l.kind }); } }, { label: 'Amount', num: true, render: function (l) { return UI.micros(l.amount); } }] });
    });
  }
  UI.$$('#tabs .tab').forEach(function (t) { t.addEventListener('click', function () { UI.$$('#tabs .tab').forEach(function (x) { x.classList.toggle('active', x === t); }); $('#tab-messages').hidden = t.dataset.tab !== 'messages'; $('#tab-ledger').hidden = t.dataset.tab !== 'ledger'; if (t.dataset.tab === 'ledger') ledger(); }); });
  $('#search').addEventListener('input', function (e) { st.q = e.target.value.trim().toLowerCase(); render(); });
  $('#refresh').addEventListener('click', function (e) { UI.busy(e.currentTarget, load()); });
  $('#list').appendChild(UI.skeleton(6)); load();
  st.timer = setInterval(function () { if (!document.hidden) load(); }, 8000);
});
