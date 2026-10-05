UI.page(function () {
  var $ = UI.$, el = UI.el, f = $('#sendform').elements;
  var state = { mode: 'text', catalog: {}, current: null, timer: null, seq: 0 };
  var GSM = /^[A-Za-z0-9 @£$¥èéùìòÇ\nØø\rÅåΔ_ΦΓΛΩΠΨΣΘΞÆæßÉ!"#¤%&'()*+,\-.\/:;<=>?¡ÄÖÑÜ§¿äöñüà^{}\\\[~\]|€]*$/;

  // ---- balance
  function balance() { UI.call('GET', '/ui/balance').then(function (r) { if (r.ok) $('#balance-mini').textContent = 'Balance ' + UI.money(r.body.available, 3) + (r.body.held ? ' · held ' + UI.money(r.body.held, 3) : ''); }); }
  balance();

  // ---- mode tabs
  UI.$$('#mode .tab').forEach(function (t) { t.addEventListener('click', function () {
    state.mode = t.dataset.mode; UI.$$('#mode .tab').forEach(function (x) { x.classList.toggle('active', x === t); });
    $('#pane-text').hidden = state.mode !== 'text'; $('#pane-template').hidden = state.mode !== 'template'; $('#pane-template').classList.add('pane-in'); later();
  }); });

  // ---- plain text counter
  function count() {
    var text = f.body.value, unicode = !GSM.test(text), n = text.length, single = unicode ? 70 : 160, multi = unicode ? 67 : 153, segs = n === 0 ? 0 : n <= single ? 1 : Math.ceil(n / multi);
    $('#count').textContent = n + ' character' + (n === 1 ? '' : 's') + (segs ? ' · ' + segs + ' segment' + (segs === 1 ? '' : 's') : '');
    $('#encoding').textContent = n ? (unicode ? 'Unicode (UCS-2): 70 per segment' : 'GSM-7: 160 per segment') : '';
    var g = $('#gauge'); g.classList.toggle('warn', segs > 1); g.firstChild.style.width = Math.min(100, n === 0 ? 0 : (n <= single ? n / single : ((n - single) % multi || multi) / multi) * 100) + '%';
  }
  f.body.addEventListener('input', function () { count(); later(); });

  // ---- templates
  UI.call('GET', '/ui/templates').then(function (r) {
    var seen = {}; (Array.isArray(r.body) ? r.body : []).forEach(function (t) {
      var k = t.name + '::' + t.lang; state.catalog[k] = t; var o = el('option', { value: k, text: t.label }); $('#template').appendChild(o);
    });
  });
  function fields(t) { try { return JSON.parse(t.fields || '[]'); } catch (e) { return []; } }
  $('#template').addEventListener('change', function (e) {
    state.current = state.catalog[e.target.value] || null; var t = state.current, box = $('#fieldlist'); box.textContent = '';
    $('#placeholders').hidden = !t; $('#template-note').textContent = t ? t.description : '';
    if (t) fields(t).forEach(function (fd) {
      var input = el('input', { name: fd.name, value: fd.default || '', placeholder: fd.hint || '' }); input.addEventListener('input', later);
      box.appendChild(el('label', {}, [fd.label || fd.name, ' ', el('code', { text: '${' + fd.name + '}' }), input]));
    }); later();
  });
  function vars() { var v = {}; UI.$$('#fieldlist input').forEach(function (i) { v[i.name] = i.value; }); return v; }

  // ---- request
  function body() {
    var b = { to: f.to.value.trim() };
    if (f.reference.value.trim()) b.reference = f.reference.value.trim();
    if (f.from.value.trim()) b.from = f.from.value.trim();
    if (state.mode === 'template' && state.current) { b.template = state.current.name; if (state.current.lang) b.lang = state.current.lang; b.vars = vars(); } else b.text = f.body.value;
    return b;
  }
  function ready() { var b = body(); return b.to.length >= 3 && (b.template || (b.text && b.text.trim())); }

  // ---- route preview (live)
  function later() { clearTimeout(state.timer); if (!ready()) { idle(); return; } $('#route-status').textContent = 'Checking…'; state.timer = setTimeout(explain, 450); }
  function idle() { $('#route-status').textContent = ''; }
  function explain() {
    var mine = ++state.seq;
    return UI.call('POST', '/ui/route/explain', body()).then(function (r) {
      if (mine !== state.seq) return; $('#route-status').textContent = '';
      renderRoute(r);
      // For a template, the explain does not return text; ask the preview endpoint for it.
      if (state.mode === 'template' && state.current) preview();
    });
  }
  function preview() {
    UI.call('POST', '/ui/templates/preview', { template: state.current.name, lang: state.current.lang || undefined, vars: vars() }).then(function (r) {
      var b = r.body || {}, box = $('#preview'); box.hidden = false; box.className = 'banner ' + (b.error ? 'errb' : 'infob');
      $('#preview-text').textContent = b.error ? 'Cannot render: ' + b.error : (b.text || '(empty)');
      $('#preview-meta').textContent = b.error ? '' : b.characters + ' characters · ' + b.segments + ' segment' + (b.segments === 1 ? '' : 's') + ' · ' + b.encoding + (b.type ? ' · type ' + b.type : '') + (b.sender ? ' · sender ' + b.sender : '');
    });
  }
  // The account's dry run: how the number was read, what it costs, and whether a
  // receipt would come. Which carrier would carry it is deliberately not in the
  // answer — the gateway routes, and an account asked to choose would be coupled
  // to this gateway's supplier list and failover order.
  function renderRoute(r) {
    var box = $('#route'); box.textContent = '';
    if (!r.ok) { var e = (r.body && r.body.error) || {}; box.appendChild(el('div', { class: 'banner errb' }, [UI.icon('alert', 18), el('div', {}, [el('strong', { text: e.code === 'INSUFFICIENT_FUNDS' ? 'Not enough balance' : 'This message cannot be sent' }), el('div', { text: e.message || 'Request failed' })])])); return; }
    var b = r.body;
    box.appendChild(el('div', { class: 'stats' }, [el('span', { class: 'stat', text: b.to + ' · ' + b.country }), el('span', { class: 'stat', text: b.segments + ' segment' + (b.segments === 1 ? '' : 's') + ' · ' + b.encoding }),
      el('span', { class: 'stat okc', text: 'Price ' + UI.money(b.price, 3) + ' ' + b.currency }),
      el('span', { class: 'stat', text: b.receipt ? 'Receipt expected' : 'No receipt' })]));
    box.appendChild(el('div', { class: 'banner okb' }, [UI.icon('check', 18), el('div', { text: 'This message can be sent. It will be routed when you send it.' })]));
  }
  f.to.addEventListener('input', function () { later(); });
  [f.from, f.reference].forEach(function (i) { i.addEventListener('input', later); });
  $('#explain').addEventListener('click', function (e) { if (!ready()) { UI.toast('Enter a number and a message first.', 'warn'); return; } UI.busy(e.currentTarget, explain()); });

  // ---- send, then follow the message
  $('#sendform').addEventListener('submit', function (e) {
    e.preventDefault(); if (!ready()) { UI.toast('Enter a number and a message first.', 'warn'); return; }
    UI.busy($('#send'), UI.call('POST', '/ui/messages', body()).then(function (r) {
      if (!r.ok) { var er = (r.body && r.body.error) || {}; UI.toast(er.message || 'The message was not sent', 'err', er.code === 'INSUFFICIENT_FUNDS' ? 'Not enough balance' : 'Not sent'); return; }
      UI.toast('Accepted and queued for delivery.', 'ok', 'Message sent'); balance(); window.refreshBell && window.refreshBell(); follow(r.body);
    }));
  });
  // Follow the message by its status word, and stop when the API says it is
  // terminal rather than guessing from a fixed number of polls.
  function follow(m) {
    var card = $('#sent'); card.hidden = false; card.classList.remove('pane-in'); void card.offsetWidth; card.classList.add('pane-in');
    var stateHost = $('#sent-state'), body = $('#sent-body'); body.textContent = '';
    body.appendChild(el('div', { class: 'kv' }, [el('div', { html: '<span class="muted">Id</span> <code>' + m.id + '</code>' }), el('div', { html: '<span class="muted">To</span> ' + m.to + ' (' + m.country + ')' }),
      el('div', { html: '<span class="muted">Status</span> ' + (m.status || 'accepted') }), el('div', { html: '<span class="muted">Price</span> ' + UI.money(m.price, 3) + ' ' + m.currency }),
      el('div', { html: '<span class="muted">Receipt</span> ' + (m.receipt ? 'expected' : 'none') })]));
    var tl = el('div', { class: 'timeline' }); body.appendChild(tl);
    var tries = 0;
    (function poll() {
      UI.call('GET', '/ui/messages/' + m.id).then(function (r) {
        if (!r.ok) return; var msg = r.body;
        stateHost.textContent = ''; stateHost.appendChild(UI.state(msg.status));
        tl.textContent = '';
        [[msg.created_ms, 'Accepted', 'ok'], [msg.submitted_ms, 'Sent to the network', 'ok'], [msg.delivered_ms, 'Delivered', 'ok']].forEach(function (e) {
          if (e[0]) tl.appendChild(el('div', { class: 'ev ' + e[2] }, [el('strong', { text: e[1] }), el('div', { class: 'muted', text: UI.when(e[0]) })]));
        });
        if (msg.status === 'failed') tl.appendChild(el('div', { class: 'ev err' }, [el('strong', { text: 'Failed' }), el('div', { class: 'muted', text: msg.detail || '' })]));
        if (msg.status === 'sent' && msg.receipt) tl.appendChild(el('div', { class: 'ev warn' }, [el('strong', { text: 'Waiting for a delivery receipt' })]));
        if (msg.terminal || tries++ >= 60) balance(); else setTimeout(poll, 1500);
      });
    })();
  }
  count();
});
