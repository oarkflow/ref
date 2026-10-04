UI.page(function (ctx) {
  var $ = UI.$, el = UI.el;
  if (!ctx.admin) { $('#main').textContent = ''; $('#main').appendChild(UI.empty('Operator only', 'Sign in as the operator to see this page.', 'shield')); return; }
  var channels = [];
  var pipes = function (v) { return (v || '').split('|').filter(Boolean); };
  var csv = function (v) { return (v || '').split(',').map(function (x) { return x.trim(); }).filter(Boolean); };

  // ------------------------------------------------------------------ list
  function load() {
    return UI.call('GET', '/ui/admin/providers').then(function (r) {
      var rows = Array.isArray(r.body) ? r.body : [];
      UI.table($('#list'), { rows: rows, onRow: function (p) { edit(p.id); }, empty: { title: 'No providers yet', text: 'Create one to start routing messages.', icon: 'server' }, columns: [
        { label: 'Provider', render: function (p) { return el('div', {}, [el('span', { class: 'dot ' + (p.state === 'active' ? 'ok' : p.state === 'paused' ? 'warn' : 'err') + (p.state === 'active' ? ' pulse' : '') }), ' ', el('strong', { text: p.id }), el('span', { class: 'sub', text: p.description || '' })]); } },
        { label: 'Channel', render: function (p) { return el('div', {}, [p.channel, ' ', el('span', { class: 'pill ' + (p.sandbox ? 'warn' : 'ok'), text: p.sandbox ? 'sandbox' : 'LIVE' })]); } },
        { label: 'State', render: function (p) { return UI.state(p.state); } },
        { label: 'Quality', render: function (p) { return el('div', { style: { minWidth: '90px' } }, [el('span', { text: p.quality }), el('div', { class: 'gauge' }, [el('span', { style: { width: Math.min(100, p.quality) + '%' } })])]); } },
        { label: 'Serves', render: function (p) { var c = pipes(p.countries); return c.length ? el('span', {}, c.map(function (x) { return el('span', { class: 'pill', text: x, style: { marginRight: '4px' } }); })) : el('span', { class: 'muted', text: 'every country' }); } },
        { label: 'Assigned to', render: function (p) { var a = pipes(p.tenants).map(function (x) { return 'tenant ' + x; }).concat(pipes(p.users)); return p.assigned_only ? (a.length ? a.join(', ') : 'nobody') : el('span', { class: 'muted', text: 'anyone' }); } },
        { label: 'Sent', num: true, render: function (p) { return UI.num(p.successes); } }, { label: 'Failed', num: true, render: function (p) { return el('span', { class: p.failures ? 'pill err' : '', text: UI.num(p.failures) }); } },
        { label: '', render: function (p) { return el('button', { type: 'button', class: 'small secondary', onclick: function () { edit(p.id); } }, [UI.icon('edit', 14), 'Edit']); } }] });
    });
  }
  $('#list').appendChild(UI.skeleton(5));
  UI.call('GET', '/ui/admin/channels').then(function (r) { channels = Array.isArray(r.body) ? r.body : []; load(); });
  $('#new-provider').addEventListener('click', function () { openEditor(null, {}, {}); });
  function edit(id) { UI.call('GET', '/ui/admin/providers/' + id).then(function (r) { if (!r.ok) { UI.fail(UI.problem(r)); return; } var p = r.body.provider || {}, s = {}; try { s = JSON.parse(p.settings || '{}'); } catch (e) {} openEditor(id, p, s); }); }

  // --------------------------------------------------------------- helpers
  function field(label, input, hint) { return el('label', {}, [label, input, hint ? el('small', { text: hint }) : null]); }
  function check(label, name, on, hint) { var i = el('input', { type: 'checkbox', name: name }); i.checked = !!on; return el('label', { class: 'cb', title: hint || '' }, [i, label]); }
  function section(title, hint, kids) { return el('section', { class: 'card' }, [el('h2', { text: title }), hint ? el('p', { class: 'muted', text: hint }) : null, el('div', { class: 'grid2' }, kids)]); }
  function control(fd, value) {
    var i;
    if (fd.kind === 'bool') { i = el('input', { type: 'checkbox', name: fd.name }); i.checked = value === true || value === 'true'; return i; }
    if (fd.kind === 'select') { i = el('select', { name: fd.name }, (fd.options || []).map(function (o) { return el('option', { value: o, text: o }); })); i.value = value == null || value === '' ? (fd.default || '') : value; return i; }
    i = el('input', { name: fd.name, type: fd.kind === 'number' ? 'number' : fd.kind === 'url' ? 'url' : 'text', step: fd.kind === 'number' ? 'any' : null, value: value == null || value === '' ? (fd.default == null ? '' : fd.default) : value });
    if (fd.required) i.required = true; return i;
  }
  function fieldsOf(channel, which) { var c = channels.filter(function (x) { return x.name === channel; })[0]; try { return JSON.parse((c && c[which]) || '[]'); } catch (e) { return []; } }

  // ---------------------------------------------------------------- editor
  function openEditor(id, p, settings) {
    var d = UI.drawer({ title: id ? 'Provider ' + id : 'New provider', subtitle: id ? (p.description || p.channel) : 'Create a routing entity on one of the configured channels', onclose: load });
    var saved = !!id, current = id, tabs = {}, panes = {}, active = 'general';
    var bar = el('div', { class: 'tabs', role: 'tablist' }); d.body.appendChild(bar);
    [['general', 'General'], ['settings', 'Settings'], ['credentials', 'Credentials'], ['prices', 'Prices']].forEach(function (t) {
      var b = el('button', { type: 'button', class: 'tab' + (t[0] === active ? ' active' : ''), role: 'tab', onclick: function () { if (!saved && t[0] !== 'general') { UI.toast('Save the provider first, then set its ' + t[1].toLowerCase() + '.', 'warn'); return; } show(t[0]); } }, [t[1]]);
      tabs[t[0]] = b; bar.appendChild(b); panes[t[0]] = el('div', { class: 'pane-in' }); d.body.appendChild(panes[t[0]]); panes[t[0]].hidden = t[0] !== active;
    });
    function show(name) { active = name; Object.keys(tabs).forEach(function (k) { tabs[k].classList.toggle('active', k === name); panes[k].hidden = k !== name; }); }

    // ---- General
    var f = el('form', { novalidate: true }), gen = panes.general; gen.appendChild(f);
    var idIn = el('input', { name: 'id', required: true, pattern: '[a-z0-9_]+', value: id || '', placeholder: 'np_gold' }); if (id) idIn.readOnly = true;
    var chan = el('select', { name: 'channel', required: true }, [el('option', { value: '', text: 'Choose a channel' })].concat(channels.map(function (c) { return el('option', { value: c.name, text: c.name + ' (' + c.kind + ')' }); }))); chan.value = p.channel || '';
    var kind = el('select', { name: 'kind' }, ['http', 'smpp'].map(function (k) { return el('option', { value: k, text: k }); })); kind.value = p.kind || 'http';
    var state = el('select', { name: 'state' }, ['active', 'paused', 'disabled'].map(function (k) { return el('option', { value: k, text: k }); })); state.value = p.state || 'active';
    f.appendChild(section('Identity', null, [field('Id', idIn, 'Lowercase letters, digits and underscores. Cannot change later.'), field('Channel', chan, 'The connection it sends over; a channel is configured in BCL.'), field('Kind', kind), field('State', state), el('div', { class: 'wide' }, [field('Description', el('input', { name: 'description', value: p.description || '' }))])]));
    f.appendChild(section('Where it applies', 'Leave a list empty to mean everyone.', [
      field('Countries', el('input', { name: 'countries', value: pipes(p.countries).join(', '), placeholder: 'NP, IN' }), 'ISO codes, separated by commas'), field('Message types', el('input', { name: 'message_types', value: pipes(p.message_types).join(', '), placeholder: 'otp, promotional' })),
      field('Tenants', el('input', { name: 'tenants', value: pipes(p.tenants).join(', ') })), field('Accounts', el('input', { name: 'users', value: pipes(p.users).join(', ') })), field('Owner account', el('input', { name: 'owner', value: p.owner || '' }), 'Exclusive to one account'),
      field('Sender countries', el('input', { name: 'sender_countries', value: pipes(p.sender_countries).join(', ') })), field('Number prefix pattern', el('input', { name: 'prefix_pattern', value: p.prefix_pattern || '', placeholder: 'regular expression' })),
      el('div', { class: 'checks' }, [check('Only for the tenants and accounts above', 'assigned_only', p.assigned_only)])]));
    f.appendChild(section('Quality and capabilities', null, [field('Quality (0-100)', el('input', { name: 'quality', type: 'number', min: 0, max: 100, step: 'any', value: p.quality == null ? 80 : p.quality })), field('Delivery rate (0-1)', el('input', { name: 'delivery_rate', type: 'number', min: 0, max: 1, step: 'any', value: p.delivery_rate == null ? 0.95 : p.delivery_rate })),
      el('div', { class: 'checks' }, [check('Unicode', 'supports_unicode', p.supports_unicode == null || p.supports_unicode), check('Delivery receipts', 'supports_dlr', p.supports_dlr == null || p.supports_dlr), check('Alphanumeric sender', 'supports_alpha', p.supports_alpha == null || p.supports_alpha), check('Long messages', 'supports_long', p.supports_long == null || p.supports_long)])]));
    f.appendChild(section('Retries', 'How a failure is retried on this provider before the next one is tried.', [field('Max attempts', el('input', { name: 'max_attempts', type: 'number', min: 1, max: 12, value: p.max_attempts || 3 })), field('Backoff start (s)', el('input', { name: 'backoff_initial_s', type: 'number', step: 'any', value: p.backoff_initial_s == null ? 1 : p.backoff_initial_s })),
      field('Backoff max (s)', el('input', { name: 'backoff_max_s', type: 'number', step: 'any', value: p.backoff_max_s == null ? 60 : p.backoff_max_s })), field('Backoff factor', el('input', { name: 'backoff_factor', type: 'number', step: 'any', value: p.backoff_factor || 2 }))]));
    f.appendChild(el('section', { class: 'card' }, [el('div', { class: 'checks' }, [check('Sandbox: this provider talks to a stand-in and nothing leaves this machine', 'sandbox', p.sandbox == null || p.sandbox)]), el('p', { class: 'muted', text: 'A label shown on the Send page and on messages. It does not change where the provider sends.' })]));
    var save = el('button', { type: 'submit' }, [UI.icon('check', 16), 'Save provider']); f.appendChild(el('div', { class: 'row' }, [save]));
    f.addEventListener('submit', function (e) {
      e.preventDefault(); var x = f.elements, pid = x.id.value.trim();
      if (!/^[a-z0-9_]+$/.test(pid)) { UI.toast('The id may hold lowercase letters, digits and underscores.', 'warn'); x.id.focus(); return; } if (!x.channel.value) { UI.toast('Choose a channel.', 'warn'); x.channel.focus(); return; }
      var body = { channel: x.channel.value, kind: x.kind.value, state: x.state.value, quality: +x.quality.value, delivery_rate: +x.delivery_rate.value, countries: csv(x.countries.value), message_types: csv(x.message_types.value), tenants: csv(x.tenants.value), users: csv(x.users.value),
        sender_countries: csv(x.sender_countries.value), owner: x.owner.value, prefix_pattern: x.prefix_pattern.value, sandbox: x.sandbox.checked, assigned_only: x.assigned_only.checked, supports_unicode: x.supports_unicode.checked, supports_dlr: x.supports_dlr.checked,
        supports_alpha: x.supports_alpha.checked, supports_long: x.supports_long.checked, max_attempts: +x.max_attempts.value, backoff_initial_s: +x.backoff_initial_s.value, backoff_max_s: +x.backoff_max_s.value, backoff_factor: +x.backoff_factor.value, description: x.description.value };
      UI.busy(save, UI.call('PUT', '/ui/admin/providers/' + pid, body).then(function (r) {
        if (!r.ok) { UI.fail(UI.problem(r)); return; } UI.toast('The next message uses these settings.', 'ok', 'Provider saved');
        if (!saved) { saved = true; current = pid; idIn.readOnly = true; d.title.textContent = 'Provider ' + pid; settingsTab(); credsTab(); pricesTab(); show('settings'); } else { settingsTab(); credsTab(); }
      }));
    });
    chan.addEventListener('change', function () { if (saved) { settingsTab(); credsTab(); } });

    // ---- Settings (typed fields declared by the channel)
    function settingsTab() {
      var pane = panes.settings; pane.textContent = ''; var fields = fieldsOf(chan.value, 'setting_fields');
      var form = el('form', { novalidate: true }), grid = el('div', { class: 'grid2' });
      fields.forEach(function (fd) { var c = control(fd, settings[fd.name]); grid.appendChild(fd.kind === 'bool' ? el('div', { class: 'checks' }, [el('label', { class: 'cb' }, [c, fd.label || fd.name])]) : field(fd.label || fd.name, c, fd.hint)); });
      var sv = el('button', { type: 'submit' }, [UI.icon('check', 16), 'Save settings']);
      pane.appendChild(el('section', { class: 'card' }, [el('h2', { text: 'Settings' }), el('p', { class: 'muted', text: fields.length ? 'These apply to every message sent through this provider. The fields depend on its channel.' : 'This channel has no provider settings.' }), form]));
      form.appendChild(grid); if (fields.length) form.appendChild(el('div', { class: 'row' }, [sv]));
      form.addEventListener('submit', function (e) {
        e.preventDefault(); var values = {};
        fields.forEach(function (fd) { var c = form.elements[fd.name]; values[fd.name] = fd.kind === 'bool' ? c.checked : fd.kind === 'number' ? (c.value === '' ? null : +c.value) : c.value; });
        UI.busy(sv, UI.call('PUT', '/ui/admin/providers/' + current + '/settings', { values: values }).then(function (r) { if (r.ok) { settings = values; UI.ok('Settings saved. The next message uses them.'); } else UI.fail(UI.problem(r)); }));
      });
    }

    // ---- Credentials
    function credsTab() {
      var pane = panes.credentials; pane.textContent = ''; var fields = fieldsOf(chan.value, 'credential_fields');
      var head = el('div', { class: 'split' }, [el('div', {}, [el('h2', { text: 'Accounts', style: { margin: 0 } }), el('p', { class: 'muted', text: 'One active account is used per message, the least recently used first. If the provider refuses one, it rests for five minutes and the next is tried at once. Secrets are stored sealed and never shown again.' })]),
        el('button', { type: 'button', onclick: function () { accountForm(null); } }, [UI.icon('plus', 16), 'Add account'])]);
      var host = el('div'); var formHost = el('div'); pane.appendChild(head); pane.appendChild(formHost); pane.appendChild(host); host.appendChild(UI.skeleton(2));
      function refresh() {
        UI.call('GET', '/ui/admin/providers/' + current + '/accounts').then(function (r) {
          var rows = Array.isArray(r.body) ? r.body : []; host.textContent = ''; if (!rows.length) host.appendChild(el('section', { class: 'card' }, [UI.empty('No accounts', 'Without an account this provider uses whatever its channel holds.', 'key')]));
          rows.forEach(function (a) {
            var pub = {}; try { pub = JSON.parse(a.public || '{}'); } catch (e) {} var resting = a.cooldown_until_ms > Date.now(), testHost = el('div');
            host.appendChild(el('section', { class: 'card lift' }, [
              el('div', { class: 'split' }, [el('div', {}, [el('strong', { text: a.name }), ' ', UI.state(a.state), resting ? el('span', { class: 'pill warn', text: 'resting until ' + new Date(a.cooldown_until_ms).toLocaleTimeString(), style: { marginLeft: '6px' } }) : null,
                el('div', { class: 'muted', text: (Object.keys(pub).map(function (k) { return k + ': ' + pub[k]; }).join(' · ') || 'no public settings') + (a.has_secret ? ' · secret set' : ' · no secret') + (a.note ? ' · ' + a.note : '') })]),
                el('div', { class: 'row', style: { margin: 0 } }, [el('span', { class: 'pill', text: a.used + ' used' }), el('span', { class: 'pill ' + (a.failures ? 'err' : ''), text: a.failures + ' refused' }),
                  el('button', { type: 'button', class: 'small secondary', onclick: function () { testPanel(testHost, a); } }, [UI.icon('play', 14), 'Test']), el('button', { type: 'button', class: 'small secondary', onclick: function () { accountForm(a); } }, [UI.icon('edit', 14), 'Edit']),
                  el('button', { type: 'button', class: 'small secondary danger-text', onclick: function () { UI.confirm({ title: 'Remove account ' + a.name + '?', text: 'Messages will stop using these credentials.', confirm: 'Remove', danger: true }).then(function (y) { if (y) UI.call('DELETE', '/ui/admin/providers/' + current + '/accounts/' + encodeURIComponent(a.name)).then(function (r) { if (r.ok) { UI.ok('Account removed.'); refresh(); } else UI.fail(UI.problem(r)); }); }); } }, [UI.icon('trash', 14)])])]), testHost]));
          });
        });
      }
      function testPanel(host, a) {
        host.textContent = ''; var to = el('input', { name: 'to', placeholder: '+977 984 123 4567', required: true }), text = el('input', { name: 'text', value: 'Credential test from the SMS gateway', maxlength: 160 }), out = el('div');
        var go = el('button', { type: 'submit', class: 'small' }, [UI.icon('send', 14), 'Send test message']);
        host.appendChild(el('form', { class: 'banner infob', style: { display: 'block', marginTop: '12px' }, onsubmit: function (e) {
          e.preventDefault(); out.textContent = '';
          UI.busy(go, UI.call('POST', '/ui/admin/providers/' + current + '/accounts/' + encodeURIComponent(a.name) + '/test', { to: to.value, text: text.value }).then(function (r) {
            if (!r.ok) { out.appendChild(el('div', { class: 'banner errb', text: UI.problem(r) })); return; } var b = r.body;
            out.appendChild(b.ok ? el('div', { class: 'banner okb' }, [UI.icon('check', 18), el('div', {}, [el('strong', { text: 'Accepted by ' + b.channel + (b.sandbox ? ' (sandbox: a stand-in, nothing was really sent)' : ' (LIVE: a real message was sent)') }), el('div', { text: 'Provider message id ' + b.provider_message_id + ' · to ' + b.to + ' as ' + b.from + ' · ' + b.latency_ms + ' ms' })])])
              : el('div', { class: 'banner errb' }, [UI.icon('alert', 18), el('div', {}, [el('strong', { text: 'Refused' }), el('div', { text: b.error || 'No reason given.' }), el('small', { text: 'The credentials or settings are wrong, or the provider is unreachable.' })])]));
          }));
        } }, [el('strong', { text: 'Test ' + a.name }), el('p', { class: 'muted', text: 'Sends one message through this account only: no routing, no payment, no record. For a live provider it is a real message, so use your own number.' }), el('div', { class: 'grid2' }, [field('To', to), field('Text', text)]), el('div', { class: 'row' }, [go, el('button', { type: 'button', class: 'secondary small', onclick: function () { host.textContent = ''; } }, ['Close'])]), out]));
        to.focus();
      }
      function accountForm(a) {
        formHost.textContent = ''; var pub = {}; try { pub = JSON.parse((a && a.public) || '{}'); } catch (e) {}
        var name = el('input', { name: 'name', required: true, pattern: '[a-zA-Z0-9_-]+', value: a ? a.name : '', placeholder: 'primary' }); if (a) name.readOnly = true;
        var st = el('select', { name: 'state' }, ['active', 'disabled'].map(function (k) { return el('option', { value: k, text: k }); })); st.value = a ? a.state : 'active';
        var note = el('input', { name: 'note', value: a ? a.note || '' : '' }), clear = el('input', { type: 'checkbox', name: 'clear' }), grid = el('div', { class: 'grid2' }, [field('Account name', name), field('State', st)]);
        fields.forEach(function (fd) {
          var i = el('input', { name: (fd.secret ? 'secret.' : 'public.') + fd.name, type: fd.secret ? 'password' : 'text', autocomplete: fd.secret ? 'new-password' : 'off', placeholder: fd.secret ? (a && a.has_secret ? '(unchanged)' : '') : '' });
          if (!fd.secret) i.value = pub[fd.name] == null ? '' : pub[fd.name]; grid.appendChild(field((fd.label || fd.name) + (fd.secret ? ' (secret)' : ''), i, fd.hint));
        });
        grid.appendChild(field('Note', note)); var sv = el('button', { type: 'submit' }, [UI.icon('check', 16), a ? 'Save account' : 'Add account']);
        formHost.appendChild(el('form', { class: 'card', novalidate: true, onsubmit: function (e) {
          e.preventDefault(); if (!name.value.trim()) { UI.toast('Name the account.', 'warn'); name.focus(); return; } var pubv = {}, sec = {};
          fields.forEach(function (fd) { var v = e.target.elements[(fd.secret ? 'secret.' : 'public.') + fd.name].value; if (fd.secret) { if (v !== '') sec[fd.name] = v; } else pubv[fd.name] = v; });
          UI.busy(sv, UI.call('PUT', '/ui/admin/providers/' + current + '/accounts/' + encodeURIComponent(name.value.trim()), { state: st.value, note: note.value, public: pubv, secret: sec, reset: clear.checked }).then(function (r) {
            if (r.ok) { UI.ok('Account saved. The next message may use it.'); formHost.textContent = ''; refresh(); } else UI.fail(UI.problem(r));
          }));
        } }, [el('h2', { text: a ? 'Edit ' + a.name : 'New account' }), grid, el('div', { class: 'checks' }, [el('label', { class: 'cb' }, [clear, 'Clear its failures and rest period'])]), el('div', { class: 'row' }, [sv, el('button', { type: 'button', class: 'secondary', onclick: function () { formHost.textContent = ''; } }, ['Cancel'])])]));
        name.focus();
      }
      refresh();
    }

    // ---- Prices
    function pricesTab() {
      var pane = panes.prices; pane.textContent = ''; var host = el('div'), country = el('input', { name: 'country', placeholder: 'NP or *', required: true, style: { maxWidth: '140px' } }), price = el('input', { name: 'per_segment', type: 'number', step: 'any', min: 0, placeholder: '0.011', required: true, style: { maxWidth: '160px' } });
      function refresh() {
        UI.call('GET', '/ui/admin/providers/' + current).then(function (r) {
          UI.table(host, { rows: (r.body && r.body.costs) || [], empty: { title: 'No prices', text: 'Without a price this provider costs nothing in routing.', icon: 'dollar' }, columns: [{ label: 'Country', render: function (c) { return c.country === '*' ? el('span', {}, [el('strong', { text: '*' }), ' every other country']) : c.country; } },
            { label: 'Per segment', num: true, render: function (c) { return UI.money(c.per_segment, 4); } },
            { label: '', render: function (c) { return el('button', { type: 'button', class: 'small secondary danger-text', onclick: function () { UI.call('DELETE', '/ui/admin/providers/' + current + '/costs/' + encodeURIComponent(c.country)).then(function (r) { if (r.ok) refresh(); else UI.fail(UI.problem(r)); }); } }, [UI.icon('trash', 14)]); } }] });
        });
      }
      var add = el('button', { type: 'submit' }, [UI.icon('plus', 16), 'Set price']);
      pane.appendChild(el('section', { class: 'card' }, [el('h2', { text: 'Price per segment' }), el('p', { class: 'muted', text: 'By destination country; * is the price for every country not listed.' }), host,
        el('form', { class: 'row', onsubmit: function (e) { e.preventDefault(); UI.busy(add, UI.call('PUT', '/ui/admin/providers/' + current + '/costs/' + encodeURIComponent(country.value.trim()), { per_segment: +price.value }).then(function (r) { if (r.ok) { country.value = ''; price.value = ''; UI.ok('Price saved.'); refresh(); } else UI.fail(UI.problem(r)); })); } }, [country, price, add])]));
      refresh();
    }
    if (saved) { settingsTab(); credsTab(); pricesTab(); } else { [['settings', 'Save the provider first.'], ['credentials', 'Save the provider first.'], ['prices', 'Save the provider first.']].forEach(function (t) { panes[t[0]].appendChild(el('section', { class: 'card' }, [UI.empty(t[1], 'These need the provider to exist.', 'server')])); }); }
  }
});
