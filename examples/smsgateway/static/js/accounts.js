UI.page(function (ctx) {
  var $ = UI.$, el = UI.el, rows = [];
  if (!ctx.admin) { $('#main').textContent = ''; $('#main').appendChild(UI.empty('Operator only', 'Sign in as the operator to see this page.', 'shield')); return; }
  var pipes = function (v) { return (v || '').split('|').filter(Boolean); }, csv = function (v) { return (v || '').split(',').map(function (x) { return x.trim(); }).filter(Boolean); };
  function field(label, input, hint) { return el('label', {}, [label, input, hint ? el('small', { text: hint }) : null]); }

  function render() {
    var q = ($('#search').value || '').trim().toLowerCase(), list = rows.filter(function (u) { return !q || (u.id + ' ' + (u.name || '') + ' ' + (u.plan || '')).toLowerCase().indexOf(q) >= 0; });
    $('#count').textContent = list.length + ' of ' + rows.length;
    UI.table($('#list'), { rows: list, onRow: function (u) { open(u.id); }, empty: { title: rows.length ? 'No account matches' : 'No accounts yet', text: rows.length ? 'Try another search.' : 'Create the first account.', icon: 'users' }, columns: [
      { label: 'Account', render: function (u) { return el('div', { class: 'row tight', style: { margin: 0, flexWrap: 'nowrap' } }, [el('span', { class: 'avatar', text: (u.name || u.id).charAt(0) }), el('div', {}, [el('strong', { text: u.name || u.id }), el('span', { class: 'sub', text: u.id })])]); } },
      { label: 'Status', render: function (u) { return UI.state(u.status); } }, { label: 'Balance', num: true, render: function (u) { return UI.money((u.balance || 0) / 1e6, 3); } },
      { label: 'Daily limit', num: true, render: function (u) { return u.daily_limit ? UI.num(u.daily_limit) : el('span', { class: 'muted', text: 'none' }); } }, { label: 'Plan', key: 'plan' },
      { label: '', render: function (u) { return el('button', { type: 'button', class: 'small secondary', onclick: function () { open(u.id); } }, [UI.icon('edit', 14), 'Open']); } }] });
  }
  function load() { return UI.call('GET', '/ui/admin/users').then(function (r) { rows = (r.body && r.body.rows) || []; render(); }); }
  $('#list').appendChild(UI.skeleton(5)); load(); $('#search').addEventListener('input', render);
  $('#new-account').addEventListener('click', function () { editor(null, {}); });
  function open(id) { UI.call('GET', '/ui/admin/users/' + id).then(function (r) { if (r.ok) editor(id, r.body); else UI.fail(UI.problem(r)); }); }

  function editor(id, u) {
    var d = UI.drawer({ title: id ? (u.name || id) : 'New account', subtitle: id || 'Create an account that can send messages', onclose: load }), saved = !!id, current = id;
    var bar = el('div', { class: 'tabs' }), tabs = {}, panes = {}; d.body.appendChild(bar);
    [['details', 'Details'], ['access', 'Money and access']].forEach(function (t, i) {
      tabs[t[0]] = el('button', { type: 'button', class: 'tab' + (i ? '' : ' active'), onclick: function () { if (!saved && t[0] !== 'details') { UI.toast('Save the account first.', 'warn'); return; } show(t[0]); } }, [t[1]]);
      panes[t[0]] = el('div', { class: 'pane-in' }); panes[t[0]].hidden = !!i; bar.appendChild(tabs[t[0]]); d.body.appendChild(panes[t[0]]);
    });
    function show(n) { Object.keys(tabs).forEach(function (k) { tabs[k].classList.toggle('active', k === n); panes[k].hidden = k !== n; }); }

    var idIn = el('input', { name: 'id', required: true, pattern: '[a-z0-9_]+', value: id || '' }); if (id) idIn.readOnly = true;
    var status = el('select', { name: 'status' }, ['active', 'suspended'].map(function (s) { return el('option', { value: s, text: s }); })); status.value = u.status || 'active';
    var obj = el('select', { name: 'objective' }, [['', '(rules decide)'], ['highest_delivery', 'best delivery'], ['lowest_cost', 'lowest cost'], ['balanced', 'balanced']].map(function (o) { return el('option', { value: o[0], text: o[1] }); })); obj.value = u.objective || '';
    var inputs = {
      name: el('input', { name: 'name', value: u.name || '' }), tenant: el('input', { name: 'tenant', value: u.tenant || '', placeholder: 'defaults to the id' }), default_sender: el('input', { name: 'default_sender', value: u.default_sender || '' }),
      senders: el('input', { name: 'senders', value: pipes(u.senders).join(', '), placeholder: 'empty: any' }), countries: el('input', { name: 'countries', value: pipes(u.countries).join(', '), placeholder: 'NP, IN (empty: any)' }),
      daily_limit: el('input', { name: 'daily_limit', type: 'number', min: 0, value: u.daily_limit || 0 }), max_price: el('input', { name: 'max_price', type: 'number', step: 'any', min: 0, value: u.max_price || 0 }),
      rate_per_second: el('input', { name: 'rate_per_second', type: 'number', min: 0, value: u.rate_per_second || 0 }), plan: el('input', { name: 'plan', value: u.plan || '' })
    };
    var save = el('button', { type: 'submit' }, [UI.icon('check', 16), 'Save account']);
    var form = el('form', { novalidate: true, onsubmit: function (e) {
      e.preventDefault(); var pid = idIn.value.trim(); if (!/^[a-z0-9_]+$/.test(pid)) { UI.toast('The id may hold lowercase letters, digits and underscores.', 'warn'); idIn.focus(); return; }
      UI.busy(save, UI.call('PUT', '/ui/admin/users/' + pid, { name: inputs.name.value, tenant: inputs.tenant.value, status: status.value, default_sender: inputs.default_sender.value, senders: csv(inputs.senders.value), countries: csv(inputs.countries.value), objective: obj.value,
        daily_limit: +inputs.daily_limit.value, max_price: +inputs.max_price.value, rate_per_second: +inputs.rate_per_second.value, plan: inputs.plan.value }).then(function (r) {
        if (!r.ok) { UI.fail(UI.problem(r)); return; } UI.toast('Account saved.', 'ok'); if (!saved) { saved = true; current = pid; idIn.readOnly = true; d.title.textContent = inputs.name.value || pid; access(); show('access'); }
      }));
    } }, [
      el('section', { class: 'card' }, [el('h2', { text: 'Identity' }), el('div', { class: 'grid2' }, [field('Id', idIn, 'Lowercase letters, digits and underscores. Cannot change later.'), field('Name', inputs.name), field('Tenant', inputs.tenant), field('Status', status, 'A suspended account cannot send.'), field('Plan', inputs.plan)])]),
      el('section', { class: 'card' }, [el('h2', { text: 'What it may send' }), el('div', { class: 'grid2' }, [field('Default sender', inputs.default_sender), field('Allowed senders', inputs.senders), field('Allowed countries', inputs.countries), field('Routing objective', obj)])]),
      el('section', { class: 'card' }, [el('h2', { text: 'Limits' }), el('p', { class: 'muted', text: '0 means no limit.' }), el('div', { class: 'grid2' }, [field('Messages per day', inputs.daily_limit), field('Price cap per message', inputs.max_price), field('Messages per second', inputs.rate_per_second)])]),
      el('div', { class: 'row' }, [save])]);
    panes.details.appendChild(form);

    function access() {
      var p = panes.access; p.textContent = ''; var bal = el('div', { class: 'big', text: '—' });
      function balance() { UI.call('GET', '/ui/admin/users/' + current).then(function (r) { if (r.ok) UI.countUp(bal, ((r.body.balance || 0) / 1e6), function (n) { return UI.money(n, 3); }); }); } balance();
      var amount = el('input', { name: 'amount', type: 'number', step: 'any', min: 0.01, placeholder: 'Amount', required: true }), ref = el('input', { name: 'reference', placeholder: 'Unique reference', required: true }), top = el('button', { type: 'submit' }, [UI.icon('plus', 16), 'Top up']);
      p.appendChild(el('section', { class: 'card' }, [el('div', { class: 'split' }, [el('div', {}, [el('h2', { text: 'Balance' }), bal]), el('a', { class: 'btnlink', href: '/admin/rules?account=' + encodeURIComponent(current) }, ['Routing rules for this account'])]),
        el('form', { class: 'grid2', onsubmit: function (e) { e.preventDefault(); UI.busy(top, UI.call('POST', '/ui/admin/users/' + current + '/topup', { amount: +amount.value, reference: ref.value }).then(function (r) { if (!r.ok) { UI.fail(UI.problem(r)); return; }
          UI.toast(r.body.applied ? 'Added. Balance ' + UI.money(r.body.balance, 3) : 'That reference was already applied, so nothing changed.', r.body.applied ? 'ok' : 'warn'); ref.value = ''; amount.value = ''; balance(); })); } }, [field('Amount', amount), field('Reference', ref, 'The same reference is never applied twice.'), el('div', { class: 'row' }, [top])])]));
      var em = el('input', { name: 'email', type: 'email', required: true, placeholder: 'person@company.com' }), pw = el('input', { name: 'password', type: 'password', minlength: 10, required: true, autocomplete: 'new-password', placeholder: '10 characters or more' }), si = el('button', { type: 'submit' }, ['Set sign-in']);
      p.appendChild(el('section', { class: 'card' }, [el('h2', { text: 'Sign-in' }), el('form', { class: 'grid2', onsubmit: function (e) { e.preventDefault(); UI.busy(si, UI.call('PUT', '/ui/admin/users/' + current + '/password', { email: em.value, password: pw.value }).then(function (r) { if (r.ok) { UI.toast('They can now sign in as ' + em.value, 'ok'); pw.value = ''; } else UI.fail(UI.problem(r)); })); } }, [field('Email', em), field('New password', pw), el('div', { class: 'row' }, [si])])]));
      var keyOut = el('div'), issue = el('button', { type: 'button', class: 'secondary' }, [UI.icon('key', 16), 'Issue a new API key']);
      issue.addEventListener('click', function () { UI.confirm({ title: 'Issue a new API key?', text: 'The old key stops working immediately.', confirm: 'Issue key', danger: true }).then(function (y) { if (!y) return; UI.busy(issue, UI.call('POST', '/ui/admin/users/' + current + '/key').then(function (r) {
        keyOut.textContent = ''; if (!r.ok) { UI.fail(UI.problem(r)); return; } var code = el('code', { text: r.body.api_key, style: { wordBreak: 'break-all' } });
        keyOut.appendChild(el('div', { class: 'banner warnb', style: { display: 'block' } }, [el('strong', { text: 'Copy it now: it is shown once.' }), el('div', { style: { margin: '8px 0' } }, [code]), el('button', { type: 'button', class: 'small secondary', onclick: function () { navigator.clipboard && navigator.clipboard.writeText(r.body.api_key).then(function () { UI.ok('Copied.'); }); } }, [UI.icon('copy', 14), 'Copy'])]));
      })); }); });
      p.appendChild(el('section', { class: 'card' }, [el('h2', { text: 'API key' }), el('p', { class: 'muted', text: 'For programs that send over the API with the x-api-key header.' }), issue, keyOut]));
    }
    if (saved) access(); else panes.access.appendChild(el('section', { class: 'card' }, [UI.empty('Save the account first', 'Then you can top it up and set its sign-in.', 'user')]));
  }
});
