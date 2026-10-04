UI.page(function () {
  var $ = UI.$, el = UI.el, st = { rows: [], filter: '', q: '', timer: null };
  var FILTERS = [['', 'All'], ['queued', 'In flight'], ['submitted', 'Submitted'], ['delivered', 'Delivered'], ['failed', 'Failed']];

  function kpis() {
    UI.call('GET', '/ui/balance').then(function (r) {
      if (!r.ok) return; var b = r.body, host = $('#kpis'); host.textContent = '';
      var delivered = st.rows.filter(function (m) { return m.state === 'delivered'; }).length, failed = st.rows.filter(function (m) { return m.state === 'failed'; }).length;
      [['dollar', 'Available balance', b.available, 'ok', 'held ' + UI.money(b.held, 3)], ['send', 'Messages (latest 100)', st.rows.length, '', ''], ['check', 'Delivered', delivered, 'ok', st.rows.length ? Math.round(100 * delivered / st.rows.length) + '% of these' : ''], ['alert', 'Failed', failed, failed ? 'err' : '', failed ? 'refunded automatically' : 'none']]
        .forEach(function (k, i) {
          var v = el('div', { class: 'value' }); host.appendChild(el('div', { class: 'kpi ' + k[3], style: { animationDelay: i * 50 + 'ms' } }, [el('div', { class: 'label', text: k[1] }), v, el('div', { class: 'sub', text: k[4] }), el('div', { class: 'ico-bg' }, [UI.icon(k[0], 20)])]));
          UI.countUp(v, k[2], i === 0 ? function (n) { return UI.money(n, 3); } : UI.num);
        });
    });
  }
  function filters() {
    var host = $('#filters'); host.textContent = '';
    FILTERS.forEach(function (f) {
      var n = st.rows.filter(function (m) { return !f[0] || (f[0] === 'queued' ? ['queued', 'dispatching'].indexOf(m.state) >= 0 : m.state === f[0]); }).length;
      host.appendChild(el('button', { type: 'button', class: 'chip' + (st.filter === f[0] ? ' on' : ''), onclick: function () { st.filter = f[0]; render(); } }, [f[1] + ' ', el('span', { class: 'faint', text: n })]));
    });
  }
  function visible() {
    return st.rows.filter(function (m) {
      if (st.filter === 'queued' && ['queued', 'dispatching'].indexOf(m.state) < 0) return false; if (st.filter && st.filter !== 'queued' && m.state !== st.filter) return false;
      return !st.q || (m.to + ' ' + m.id + ' ' + m.provider).toLowerCase().indexOf(st.q) >= 0;
    });
  }
  function render() {
    filters();
    UI.table($('#list'), { rows: visible(), onRow: open, empty: { title: st.rows.length ? 'No messages match' : 'No messages yet', text: st.rows.length ? 'Change the filter or the search.' : 'Send one from the Send page and it appears here.', icon: 'send' }, columns: [
      { label: 'When', render: function (m) { return el('span', { title: UI.when(m.created_ms), text: UI.ago(m.created_ms) }); } },
      { label: 'To', render: function (m) { return el('div', {}, [el('strong', { text: m.to }), el('span', { class: 'sub', text: m.from || '' })]); } },
      { label: 'State', render: function (m) { return UI.state(m.state); } },
      { label: 'Via', render: function (m) { return el('span', {}, [m.provider || '', ' ', m.sandbox ? el('span', { class: 'pill warn', text: 'sandbox' }) : m.provider ? el('span', { class: 'pill ok', text: 'live' }) : '']); } },
      { label: 'Segments', num: true, key: 'segments' },
      { label: 'Price', num: true, render: function (m) { return UI.micros(m.price) + ' ' + (m.currency || ''); } }
    ] });
  }
  function load() {
    return UI.call('GET', '/ui/messages').then(function (r) { if (r.ok) { st.rows = Array.isArray(r.body) ? r.body : []; render(); kpis(); } });
  }
  function open(m) {
    var d = UI.drawer({ title: 'Message to ' + m.to, subtitle: m.id }), body = d.body; body.appendChild(UI.skeleton(5));
    UI.call('GET', '/ui/messages/' + m.id).then(function (r) {
      body.textContent = ''; if (!r.ok) { body.appendChild(el('div', { class: 'banner errb', text: UI.problem(r) })); return; }
      var x = r.body.message, att = r.body.attempts || [];
      body.appendChild(el('section', { class: 'card' }, [el('div', { class: 'split' }, [el('h2', { text: 'Summary' }), UI.state(x.state)]), el('div', { class: 'kv' }, [
        el('div', { html: '<span class="muted">To</span> ' + x.to + ' (' + x.country + ')' }), el('div', { html: '<span class="muted">Sender</span> ' + (x.from || '') }), el('div', { html: '<span class="muted">Type</span> ' + x.type }),
        el('div', { html: '<span class="muted">Segments</span> ' + x.segments + ' · ' + x.encoding }), el('div', { html: '<span class="muted">Provider</span> ' + (x.provider || '') }), el('div', { html: '<span class="muted">Price</span> ' + UI.micros(x.price) + ' ' + x.currency }),
        el('div', { html: '<span class="muted">Accepted</span> ' + UI.when(x.created_ms) }), el('div', { html: '<span class="muted">Delivered</span> ' + (x.delivered_ms ? UI.when(x.delivered_ms) : '—') })]),
        x.err_text ? el('div', { class: 'banner errb', text: x.err_text }) : null]));
      var tl = el('div', { class: 'timeline' }); att.forEach(function (a) { tl.appendChild(el('div', { class: 'ev ' + (a.outcome === 'accepted' ? 'ok' : a.outcome === 'failed' ? 'err' : 'warn') }, [el('strong', { text: 'Attempt ' + a.n + ' via ' + a.provider + ': ' + a.outcome }), el('div', { class: 'muted', text: UI.when(a.at_ms) + (a.latency_ms ? ' · ' + a.latency_ms + ' ms' : '') + (a.err_text ? ' · ' + a.err_text : '') })])); });
      body.appendChild(el('section', { class: 'card' }, [el('h2', { text: 'Delivery attempts' }), att.length ? tl : el('p', { class: 'muted', text: 'No attempt has been made yet.' })]));
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
