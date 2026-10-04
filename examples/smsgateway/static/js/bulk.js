// Campaigns and audiences: choose a source (numbers, CSV, saved audience), see
// which numbers are valid, preview the first message, and follow a campaign.
(function () {
  var $ = function (s, r) { return (r || document).querySelector(s); };
  var all = function (s, r) { return [].slice.call((r || document).querySelectorAll(s)); };
  function el(tag, attrs, kids) {
    var e = document.createElement(tag);
    Object.keys(attrs || {}).forEach(function (k) {
      if (k === 'text') e.textContent = attrs[k]; else if (k === 'class') e.className = attrs[k];
      else if (k.slice(0, 2) === 'on') e.addEventListener(k.slice(2), attrs[k]); else if (attrs[k] !== false && attrs[k] != null) e.setAttribute(k, attrs[k] === true ? '' : attrs[k]);
    });
    (kids || []).forEach(function (c) { if (c != null) e.appendChild(typeof c === 'string' ? document.createTextNode(c) : c); });
    return e;
  }
  function call(method, path, body) {
    return fetch(path, { method: method, credentials: 'same-origin', headers: { 'Content-Type': 'application/json' }, body: body ? JSON.stringify(body) : undefined })
      .then(function (r) {
        if (r.status === 401) { location.href = '/login'; return new Promise(function () {}); }
        return r.json().catch(function () { return {}; }).then(function (j) { return { status: r.status, body: j }; });
      });
  }
  function problem(r) { var e = (r.body && r.body.error) || {}; return (e.message || ('request failed (' + r.status + ')')) + (e.code ? ' [' + e.code + ']' : ''); }
  function say(node, ok, text) { node.hidden = false; node.className = 'out ' + (ok ? 'ok' : 'err'); node.textContent = text; }
  var REASONS = { empty: 'no number', unparsable: 'not a phone number', too_short: 'too short', too_long: 'too long', not_possible: 'not a possible number', not_valid: 'not a valid number', type_not_allowed: 'a number type that cannot receive SMS' };
  function why(r) { return REASONS[r] || r || ''; }
  function cell(v) { return el('td', { text: v == null ? '' : String(v) }); }
  function table(sel, rows, cols) {
    var tb = $(sel + ' tbody'); tb.textContent = '';
    rows.forEach(function (r) { var tr = el('tr'); cols.forEach(function (c) { tr.appendChild(typeof c === 'function' ? c(r) : cell(r[c])); }); tb.appendChild(tr); });
  }

  var form = $('#campaignform') || $('#audienceform');
  if (!form) return;
  var isCampaign = !!$('#campaignform');
  var state = { src: 'list', msg: 'text', columns: [], catalog: {}, current: null, rows: [] };

  // ---------------------------------------------------------------- source
  function setTab(group, attr, name, paneAttr) {
    all('[' + attr + ']', group).forEach(function (t) { t.classList.toggle('active', t.getAttribute(attr) === name); });
    all('[' + paneAttr + ']').forEach(function (p) { p.hidden = p.getAttribute(paneAttr) !== name; });
  }
  all('#src-tabs .tab').forEach(function (t) { t.addEventListener('click', function () { state.src = t.dataset.src; setTab($('#src-tabs'), 'data-src', state.src, 'data-pane'); }); });
  function csvColumns(text) {
    var first = (text || '').replace(/^﻿/, '').split(/\r?\n/)[0] || '';
    var d = first.indexOf(',') >= 0 ? ',' : first.indexOf(';') >= 0 ? ';' : first.indexOf('\t') >= 0 ? '\t' : ',';
    var out = [], cur = '', q = false;
    for (var i = 0; i < first.length; i++) {
      var c = first[i];
      if (c === '"') { if (q && first[i + 1] === '"') { cur += '"'; i++; } else q = !q; }
      else if (c === d && !q) { out.push(cur.trim()); cur = ''; } else cur += c;
    }
    out.push(cur.trim());
    return out.filter(Boolean);
  }
  function updateColumns() {
    var csv = form.elements.csv ? form.elements.csv.value : '';
    state.columns = csvColumns(csv);
    var sel = form.elements.phone_field; if (!sel) return;
    var keep = sel.value; sel.textContent = '';
    var cols = state.columns.length ? state.columns : ['phone'];
    cols.forEach(function (c) { sel.appendChild(el('option', { value: c, text: c })); });
    var guess = cols.filter(function (c) { return c === keep; })[0] || cols.filter(function (c) { return /phone|mobile|msisdn|number|contact|cell/i.test(c); })[0] || cols[0];
    sel.value = guess;
    renderChips(); renderTemplateFields();
  }
  if (form.elements.csv) form.elements.csv.addEventListener('input', updateColumns);
  var file = $('#csvfile');
  if (file) file.addEventListener('change', function () {
    var f = file.files[0]; if (!f) return;
    var reader = new FileReader();
    reader.onload = function () { form.elements.csv.value = String(reader.result || ''); state.src = 'csv'; setTab($('#src-tabs'), 'data-src', 'csv', 'data-pane'); updateColumns(); };
    reader.readAsText(f);
  });
  var sample = $('#csvsample');
  if (sample) sample.addEventListener('click', function () {
    form.elements.csv.value = 'phone,name,amount,due\n9841234567,Asha,1250,5 Oct\n+977 984-123-4568,Bimal,300,6 Oct\n12345,Short,1,7 Oct\n';
    form.elements.text.value = form.elements.text.value || 'Dear {{ name }}, NPR {{ amount }} is due on {{ due }}.';
    updateColumns();
  });

  // --------------------------------------------------------------- message
  function renderChips() {
    var box = $('#chips'); if (!box) return; box.textContent = '';
    state.columns.forEach(function (c) {
      box.appendChild(el('button', { type: 'button', class: 'chip', text: c, title: 'Insert {{ ' + c + ' }}', onclick: function () {
        var ta = form.elements.text, pos = ta.selectionStart == null ? ta.value.length : ta.selectionStart, tag = '{{ ' + c + ' }}';
        ta.value = ta.value.slice(0, pos) + tag + ta.value.slice(ta.selectionEnd == null ? pos : ta.selectionEnd); ta.focus(); ta.selectionStart = ta.selectionEnd = pos + tag.length;
      } }));
    });
  }
  if ($('#msg-tabs')) all('#msg-tabs .tab').forEach(function (t) { t.addEventListener('click', function () { state.msg = t.dataset.msg; setTab($('#msg-tabs'), 'data-msg', state.msg, 'data-mpane'); }); });
  function fieldsOf(t) { try { return JSON.parse(t.fields || '[]'); } catch (e) { return []; } }
  function renderTemplateFields() {
    var box = $('#tpl-list'); if (!box) return; box.textContent = '';
    var t = state.current; $('#tpl-fields').hidden = !t; $('#tpl-note').textContent = t ? t.description : '';
    if (!t) return;
    fieldsOf(t).forEach(function (f) {
      var matched = state.columns.indexOf(f.name) >= 0;
      var input = el('input', { name: 'var.' + f.name, value: f.default || '', placeholder: f.hint || '' });
      box.appendChild(el('label', { class: 'f' }, [(f.label || f.name) + ' ', el('code', { class: 'muted', text: '${' + f.name + '}' }), ' ',
        el('span', { class: 'badge ' + (matched ? 'use' : 'only'), text: matched ? 'from the "' + f.name + '" column' : 'same for everyone' }), input,
        el('small', { class: 'muted', text: matched ? 'Each row\'s value is used; this is the fallback for an empty one.' : (f.hint || 'No column has this name, so this value is used for every recipient.') })]));
    });
  }
  if (isCampaign) {
    call('GET', '/ui/templates').then(function (r) {
      var sel = form.elements.template, seen = {};
      (Array.isArray(r.body) ? r.body : []).forEach(function (t) { if (seen[t.name]) return; seen[t.name] = 1; state.catalog[t.name] = t; sel.appendChild(el('option', { value: t.name, text: t.label + ' (' + t.name + ')' })); });
    });
    form.elements.template.addEventListener('change', function (e) { state.current = state.catalog[e.target.value] || null; renderTemplateFields(); });
    call('GET', '/ui/audiences').then(function (r) {
      var sel = form.elements.audience;
      (Array.isArray(r.body) ? r.body : []).forEach(function (a) { sel.appendChild(el('option', { value: a.id, text: a.name + ' (' + a.valid + ' valid of ' + a.total + ')' })); });
    });
  }

  // ------------------------------------------------------------ request body
  function source() {
    var b = {}, region = (form.elements.region.value || 'NP').trim().toUpperCase();
    if (region) b.region = region;
    if (state.src === 'list') b.recipients = form.elements.numbers.value.split('\n').map(function (s) { return s.trim(); }).filter(Boolean);
    else if (state.src === 'csv') { b.csv = form.elements.csv.value; b.phone_field = form.elements.phone_field.value; }
    else if (state.src === 'audience') b.audience = form.elements.audience.value;
    return b;
  }
  function campaign() {
    var b = source(); b.name = form.elements.name.value.trim();
    if (state.msg === 'template' && form.elements.template.value) {
      b.template = form.elements.template.value; b.vars = {};
      all('#tpl-list input').forEach(function (i) { if (i.value !== '') b.vars[i.name.replace(/^var\./, '')] = i.value; });
    } else b.text = form.elements.text.value;
    if (form.elements.from.value.trim()) b.from = form.elements.from.value.trim();
    if (form.elements.type.value) b.type = form.elements.type.value;
    if (form.elements.on_invalid && form.elements.on_invalid.value) b.on_invalid = form.elements.on_invalid.value;
    return b;
  }
  function checkBody() {
    var b = isCampaign ? campaign() : source();
    if (!isCampaign) b.name = form.elements.name.value.trim();
    return b;
  }

  // --------------------------------------------------------------- preview
  $('#preview').addEventListener('click', function () {
    var out = $('#preview-out'); out.hidden = false; out.className = 'out'; out.textContent = 'Checking...';
    var b = checkBody();
    if (b.audience) { say(out, true, 'This audience was checked when it was saved; open it on the Audiences page to see its rows.'); return; }
    call('POST', '/ui/campaigns/preview', b).then(function (r) {
      if (r.status !== 200) { say(out, false, problem(r)); return; }
      var p = r.body; out.className = 'out'; out.textContent = '';
      out.appendChild(el('div', { class: 'stats' }, [
        el('span', { class: 'stat', text: p.total + ' recipients' }), el('span', { class: 'stat okc', text: p.valid + ' valid' }),
        el('span', { class: 'stat ' + (p.skipped ? 'errc' : ''), text: p.skipped + ' will be skipped' })]));
      var pol = p.policy || {};
      if (p.skipped > 0 && pol.effect === 'deny') out.appendChild(el('div', { class: 'banner errb', text: 'The rules would discard this whole list: ' + (pol.reason || 'it has invalid numbers') + ' (rule ' + pol.rule + ')' }));
      else if (p.skipped > 0) out.appendChild(el('div', { class: 'banner warnb', text: 'The ' + p.valid + ' valid number' + (p.valid === 1 ? '' : 's') + ' will be sent; the ' + p.skipped + ' invalid number' + (p.skipped === 1 ? ' is' : 's are') + ' skipped and counted' + (pol.notify ? ', and you will be notified' : '') + ' (rule ' + pol.rule + ').' }));
      if (p.columns && p.columns.length) out.appendChild(el('div', { class: 'muted', text: 'Columns: ' + p.columns.join(', ') }));
      if (isCampaign && p.sample) out.appendChild(el('div', {}, [el('strong', { text: 'The first message: ' }), p.sample.error ? el('span', { class: 'err', text: p.sample.error }) : el('span', { text: p.sample.text })]));
      var bad = p.skipped_rows || [];
      if (bad.length) {
        var t = el('table', {}, [el('thead', {}, [el('tr', {}, [el('th', { text: '#' }), el('th', { text: 'Number' }), el('th', { text: 'Why it is skipped' })])]), el('tbody')]);
        bad.slice(0, 50).forEach(function (r) { t.lastChild.appendChild(el('tr', {}, [cell(r.n + 1), cell(r.phone), cell(why(r.reason))])); });
        out.appendChild(t); if (bad.length > 50) out.appendChild(el('div', { class: 'muted', text: '... and ' + (bad.length - 50) + ' more' }));
      }
    });
  });

  // ---------------------------------------------------------------- submit
  form.addEventListener('submit', function (e) {
    e.preventDefault();
    var out = $('#result'), b = checkBody();
    if (isCampaign && state.msg === 'template' && !b.template) { say(out, false, 'Choose a template or switch to Text.'); return; }
    call('POST', isCampaign ? '/ui/campaigns' : '/ui/audiences', b).then(function (r) {
      if (r.status !== 202 && r.status !== 201 && r.status !== 200) { say(out, false, problem(r)); return; }
      var d = r.body;
      if (isCampaign && d.notice) refreshBell();
      say(out, true, (isCampaign ? 'Submitted for approval: ' : 'Saved: ') + d.valid + ' valid recipient' + (d.valid === 1 ? '' : 's') + (d.skipped ? ', ' + d.skipped + ' skipped (invalid number)' : '') + (isCampaign ? '. Nothing is sent until an operator approves it.' : '.'));
      refresh();
    });
  });

  // ------------------------------------------------------------------ lists
  function download(name, text) {
    var a = el('a', { href: URL.createObjectURL(new Blob([text], { type: 'text/csv' })), download: name }); document.body.appendChild(a); a.click(); a.remove();
  }
  function csvCell(v) { v = v == null ? '' : String(v); return /[",\n]/.test(v) ? '"' + v.replace(/"/g, '""') + '"' : v; }
  function openDetail(kind, id) {
    call('GET', '/ui/' + kind + '/' + id).then(function (r) {
      if (r.status !== 200) return;
      var head = r.body.campaign || r.body.audience, rows = r.body.recipients || r.body.members || [];
      state.rows = rows; state.head = head;
      $('#detail').hidden = false; $('#detail-title').textContent = head.name;
      var s = $('#detail-summary');
      if (s) s.textContent = head.total + ' recipients: ' + (head.sent || 0) + ' sent, ' + (head.failed || 0) + ' failed, ' + (head.skipped || 0) + ' skipped (invalid number), state ' + head.state + (head.note ? ', note: ' + head.note : '');
      renderDetail(); $('#detail').scrollIntoView();
    });
  }
  function renderDetail() {
    var f = $('#detail-filter').value, rows = state.rows.filter(function (r) { return !f || r.state === f; });
    var withFields = !!$('#detail-table th:last-child') && $('#detail-table th:last-child').textContent === 'Fields';
    table('#detail-table', rows, [function (r) { return cell(r.n + 1); }, 'to_number', 'e164', function (r) { return el('td', {}, [el('span', { class: 'badge ' + (r.state === 'sent' ? 'use' : r.state === 'skipped' || r.state === 'failed' ? 'avoid' : 'only'), text: r.state })]); },
      function (r) { return cell(r.state === 'skipped' ? why(r.reason) : r.reason); }].concat(withFields ? [function (r) { try { var o = JSON.parse(r.fields || '{}'); delete o[state.head.phone_field || 'phone']; return cell(Object.keys(o).map(function (k) { return k + '=' + o[k]; }).join(', ')); } catch (e) { return cell(''); } }] : []));
  }
  if ($('#detail-filter')) $('#detail-filter').addEventListener('change', renderDetail);
  if ($('#detail-close')) $('#detail-close').addEventListener('click', function () { $('#detail').hidden = true; });
  if ($('#detail-csv')) $('#detail-csv').addEventListener('click', function () {
    var cols = {}; state.rows.forEach(function (r) { try { Object.keys(JSON.parse(r.fields || '{}')).forEach(function (k) { cols[k] = 1; }); } catch (e) {} });
    var names = Object.keys(cols).sort(), lines = [['n', 'number', 'dialled_as', 'state', 'reason', 'message_id'].concat(names).map(csvCell).join(',')];
    state.rows.forEach(function (r) { var o = {}; try { o = JSON.parse(r.fields || '{}'); } catch (e) {} lines.push([r.n + 1, r.to_number, r.e164, r.state, r.reason, r.message_id].concat(names.map(function (k) { return o[k]; })).map(csvCell).join(',')); });
    download((state.head && state.head.name || 'campaign').replace(/[^A-Za-z0-9_-]+/g, '_') + '-report.csv', lines.join('\n') + '\n');
  });
  function linkCell(label, fn) { return el('td', {}, [el('a', { href: '#', text: label, onclick: function (e) { e.preventDefault(); fn(); } })]); }
  function refresh() {
    if (isCampaign) {
      call('GET', '/ui/campaigns').then(function (r) {
        table('#campaigns', Array.isArray(r.body) ? r.body : [], ['name', 'state', 'source', 'total', 'valid', 'skipped', 'sent', 'failed', function (c) { return linkCell('open', function () { openDetail('campaigns', c.id); }); }]);
      });
    } else {
      call('GET', '/ui/audiences').then(function (r) {
        table('#audiences', Array.isArray(r.body) ? r.body : [], ['name', 'total', 'valid', 'skipped', 'region', function (a) {
          var td = el('td'), del = el('a', { href: '#', text: 'delete' }), ask = el('span', { hidden: true }, [' Delete? ']);
          td.appendChild(el('a', { href: '#', text: 'open', onclick: function (e) { e.preventDefault(); openDetail('audiences', a.id); } })); td.appendChild(document.createTextNode(' '));
          del.addEventListener('click', function (e) { e.preventDefault(); del.hidden = true; ask.hidden = false; });
          ask.appendChild(el('a', { href: '#', text: 'yes', onclick: function (e) { e.preventDefault(); call('DELETE', '/ui/audiences/' + a.id).then(function () { $('#detail').hidden = true; refresh(); }); } }));
          ask.appendChild(document.createTextNode(' / ')); ask.appendChild(el('a', { href: '#', text: 'no', onclick: function (e) { e.preventDefault(); ask.hidden = true; del.hidden = false; } }));
          td.appendChild(del); td.appendChild(ask); return td;
        }]);
      });
    }
  }
  function refreshBell() { if (window.refreshBell) window.refreshBell(); }
  updateColumns(); refresh();
})();
