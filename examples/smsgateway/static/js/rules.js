// Rules page: routing rules one by one (navigator, collapsible groups and
// cards, a form per rule), the generated definition, and a source editor.
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

  var state = { rules: [], users: [], providers: [], templates: [], filter: { kind: 'all', value: '' }, open: {}, tree: { Accounts: true, Countries: false, Providers: false }, search: '', draft: null };

  // ---------------------------------------------------------------- helpers
  function split(s) { return (s || '').split('|').filter(Boolean); }
  function csv(v) { return (v || '').split(',').map(function (x) { return x.trim(); }).filter(Boolean); }
  function specificity(r) {
    var n = 0;
    if (r.account) n += 40;
    if (r.tenant) n += 15;
    if (r.recipients || r.recipient_prefix || r.recipient_pattern) n += 30;
    if (r.content_words || r.content_pattern || r.templates) n += 20;
    if (r.countries) n += 10;
    if (r.types) n += 5;
    if (r.sender_pattern) n += 5;
    if (r.min_segments || r.max_segments) n += 5;
    return n;
  }
  function kindOf(r) {
    if (r.recipients || r.recipient_prefix || r.recipient_pattern) return 'Recipient';
    if (r.content_words || r.content_pattern || r.templates) return 'Content';
    if (r.types || r.sender_pattern || r.min_segments || r.max_segments) return 'Size, type and sender';
    if (r.countries) return 'Country';
    return 'Default';
  }
  var MODES = { use: 'Use first', only: 'Use only', avoid: 'Never use' };
  function summary(r) {
    var who = r.account ? 'account ' + r.account : 'any account';
    if (r.tenant) who += ' of tenant ' + r.tenant;
    var parts = [];
    if (r.countries) parts.push('to ' + split(r.countries).join(', '));
    if (r.recipients) parts.push('number ' + split(r.recipients).join(' or '));
    if (r.recipient_prefix) parts.push('numbers starting ' + r.recipient_prefix);
    if (r.templates) parts.push('template ' + split(r.templates).join(' or '));
    if (r.recipient_pattern) parts.push('numbers matching /' + r.recipient_pattern + '/');
    if (r.content_words) parts.push('text containing ' + split(r.content_words).join(' or '));
    if (r.content_pattern) parts.push('text matching /' + r.content_pattern + '/');
    if (r.types) parts.push('type ' + split(r.types).join(' or '));
    if (r.sender_pattern) parts.push('sender matching /' + r.sender_pattern + '/');
    if (r.min_segments || r.max_segments) parts.push((r.min_segments || 1) + (r.max_segments ? '-' + r.max_segments : '+') + ' segments');
    return who + (parts.length ? ', ' + parts.join(', ') : ', everything');
  }
  function action(r) {
    return { use: 'use ' + r.provider + ' first', only: 'use only ' + r.provider, avoid: 'never use ' + r.provider }[r.mode];
  }

  // ------------------------------------------------------------- data load
  function loadAll() {
    return Promise.all([call('GET', '/ui/admin/routing-rules'), call('GET', '/ui/admin/users'), call('GET', '/ui/admin/providers'), call('GET', '/ui/templates')]).then(function (rs) {
      state.rules = Array.isArray(rs[0].body) ? rs[0].body : [];
      state.users = (rs[1].body && rs[1].body.rows) || [];
      state.providers = Array.isArray(rs[2].body) ? rs[2].body : [];
      var seen = {}; state.templates = (Array.isArray(rs[3].body) ? rs[3].body : []).filter(function (t) { if (seen[t.name]) return false; seen[t.name] = true; return true; }).map(function (t) { return { name: t.name, label: t.label }; });
      renderTree(); renderList(); fillTryForm();
    });
  }

  // ----------------------------------------------------------------- tree
  function matches(r) {
    var f = state.filter;
    if (f.kind === 'platform' && r.account) return false;
    if (f.kind === 'account' && r.account !== f.value) return false;
    if (f.kind === 'country' && split(r.countries).indexOf(f.value) < 0) return false;
    if (f.kind === 'provider' && r.provider !== f.value) return false;
    if (f.kind === 'disabled' && r.enabled) return false;
    if (state.search) {
      var hay = (r.name + ' ' + summary(r) + ' ' + action(r) + ' ' + (r.note || '')).toLowerCase();
      if (hay.indexOf(state.search.toLowerCase()) < 0) return false;
    }
    return true;
  }
  function renderTree() {
    var box = $('#rr-tree'); box.textContent = '';
    var count = function (fn) { return state.rules.filter(fn).length; };
    var node = function (label, n, kind, value, depth) {
      var sel = state.filter.kind === kind && state.filter.value === (value || '');
      return el('div', { class: 'tnode' + (sel ? ' sel' : ''), style: 'padding-left:' + (8 + (depth || 0) * 14) + 'px', role: 'treeitem', tabindex: 0,
        onclick: function () { state.filter = { kind: kind, value: value || '' }; renderTree(); renderList(); },
        onkeydown: function (e) { if (e.key === 'Enter') e.target.click(); } }, [el('span', { text: label }), el('span', { class: 'n', text: String(n) })]);
    };
    var group = function (label, children) {
      var open = state.tree[label];
      box.appendChild(el('div', { class: 'tgroup', role: 'treeitem', 'aria-expanded': open ? 'true' : 'false', tabindex: 0,
        onclick: function () { state.tree[label] = !state.tree[label]; renderTree(); },
        onkeydown: function (e) { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); state.tree[label] = !state.tree[label]; renderTree(); } } },
        [el('span', { class: 'chev', text: open ? '▾' : '▸' }), el('span', { text: label })]));
      if (open) children.forEach(function (c) { box.appendChild(c); });
    };
    box.appendChild(node('All rules', state.rules.length, 'all', ''));
    box.appendChild(node('Platform (any account)', count(function (r) { return !r.account; }), 'platform', ''));
    var accounts = {}; state.users.forEach(function (u) { accounts[u.id] = 0; });
    state.rules.forEach(function (r) { if (r.account) accounts[r.account] = (accounts[r.account] || 0) + 1; });
    group('Accounts', Object.keys(accounts).sort().map(function (a) { return node(a, accounts[a], 'account', a, 1); }));
    var countries = {};
    state.rules.forEach(function (r) { split(r.countries).forEach(function (c) { countries[c] = (countries[c] || 0) + 1; }); });
    group('Countries', Object.keys(countries).sort().map(function (c) { return node(c, countries[c], 'country', c, 1); }));
    var providers = {};
    state.rules.forEach(function (r) { providers[r.provider] = (providers[r.provider] || 0) + 1; });
    group('Providers', Object.keys(providers).sort().map(function (p) { return node(p, providers[p], 'provider', p, 1); }));
    box.appendChild(node('Switched off', count(function (r) { return !r.enabled; }), 'disabled', ''));
  }
  function filterTitle() {
    var f = state.filter;
    return { all: 'All rules', platform: 'Platform rules (any account)', account: 'Rules for account ' + f.value, country: 'Rules for ' + f.value, provider: 'Rules about ' + f.value, disabled: 'Switched off' }[f.kind];
  }

  // ----------------------------------------------------------------- list
  function renderList() {
    $('#rr-title').textContent = filterTitle();
    var box = $('#rr-list'); box.textContent = '';
    if (state.draft) box.appendChild(card(state.draft, true));
    var rules = state.rules.filter(matches).sort(function (a, b) { return b.priority - a.priority || (a.name < b.name ? -1 : 1); });
    if (!rules.length && !state.draft) { box.appendChild(el('div', { class: 'card muted', text: 'No rules here yet. Use Add rule.' })); return; }
    var groups = {}, order = ['Recipient', 'Content', 'Size, type and sender', 'Country', 'Default'];
    rules.forEach(function (r) { (groups[kindOf(r)] = groups[kindOf(r)] || []).push(r); });
    order.forEach(function (g) {
      if (!groups[g]) return;
      var key = 'g:' + g, open = state.open[key] !== false;
      var section = el('section', { class: 'card rgroup' });
      section.appendChild(el('div', { class: 'rghead', role: 'button', tabindex: 0, 'aria-expanded': open ? 'true' : 'false',
        onclick: function () { state.open[key] = !open; renderList(); },
        onkeydown: function (e) { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); state.open[key] = !open; renderList(); } } },
        [el('span', { class: 'chev', text: open ? '▾' : '▸' }), el('strong', { text: g }), el('span', { class: 'n', text: String(groups[g].length) })]));
      if (open) groups[g].forEach(function (r) { section.appendChild(card(r, false)); });
      box.appendChild(section);
    });
  }

  function card(r, isDraft) {
    var open = isDraft || state.open[r.id] === true;
    var c = el('div', { class: 'rcard' + (r.enabled === 0 || r.enabled === false ? ' off' : '') + (open ? ' open' : '') });
    var enabled = r.enabled !== 0 && r.enabled !== false;
    var head = el('div', { class: 'rhead', role: 'button', tabindex: 0, 'aria-expanded': open ? 'true' : 'false',
      onclick: function (e) { if (e.target.closest('.switch')) return; if (!isDraft) { state.open[r.id] = !open; renderList(); } },
      onkeydown: function (e) { if ((e.key === 'Enter' || e.key === ' ') && !isDraft && !e.target.closest('.switch')) { e.preventDefault(); state.open[r.id] = !open; renderList(); } } });
    head.appendChild(el('span', { class: 'chev', text: open ? '▾' : '▸' }));
    var title = el('div', { class: 'rtitle' }, [el('strong', { text: r.name || (isDraft ? 'New rule' : '(unnamed)') }),
      el('div', { class: 'muted rsum', text: summary(r) + ' → ' + action(r) })]);
    head.appendChild(title);
    head.appendChild(el('span', { class: 'badge ' + r.mode, text: MODES[r.mode] || r.mode }));
    head.appendChild(el('span', { class: 'prio', title: 'Priority', text: 'P' + (r.priority || 0) }));
    if (!isDraft) {
      var sw = el('label', { class: 'switch', title: enabled ? 'On: click to switch off' : 'Off: click to switch on' }, [
        el('input', { type: 'checkbox', checked: enabled, 'aria-label': 'Enabled',
          onchange: function (e) {
            call('PUT', '/ui/admin/routing-rules/' + r.id + '/enabled', { enabled: e.target.checked }).then(function (res) {
              if (res.status === 200) loadAll(); else { e.target.checked = !e.target.checked; say($('#rr-result'), false, problem(res)); }
            });
          } }), el('span', { class: 'slider' })]);
      head.appendChild(sw);
    }
    c.appendChild(head);
    if (open) c.appendChild(form(r, isDraft));
    return c;
  }

  // ----------------------------------------------------------------- form
  function opt(value, label, selected) { var o = el('option', { value: value, text: label || value }); if (selected) o.selected = true; return o; }
  function field(label, input, hint) {
    return el('label', { class: 'f' }, [label, input, hint ? el('small', { class: 'muted', text: hint }) : null]);
  }
  function section(title, open, kids) {
    var d = el('details', { class: 'fsec' }); if (open) d.setAttribute('open', '');
    d.appendChild(el('summary', { text: title })); var g = el('div', { class: 'grid2' }, kids); d.appendChild(g); return d;
  }
  var TYPES = ['transactional', 'otp', 'promotional'];

  function form(r, isDraft) {
    var f = el('form', { class: 'rform' });
    var preset = (state.draft && isDraft && state.draft._preset) || null;
    var name = el('input', { name: 'name', value: r.name || '', required: true, maxlength: 120 });
    var account = el('select', { name: 'account' }, [opt('', 'Any account', !r.account)].concat(state.users.map(function (u) { return opt(u.id, u.id + (u.name ? ' (' + u.name + ')' : ''), r.account === u.id); })));
    var tenant = el('input', { name: 'tenant', value: r.tenant || '', placeholder: 'empty: any' });
    var countries = el('input', { name: 'countries', value: split(r.countries).join(', '), placeholder: 'NP, IN (empty: any)' });
    var recipients = el('input', { name: 'recipients', value: split(r.recipients).join(', '), placeholder: '9856034616, +977 9856034616' });
    var tplBox = el('div', { class: 'checks' }, state.templates.map(function (t) {
      return el('label', { class: 'cb' }, [el('input', { type: 'checkbox', name: 'template', value: t.name, checked: split(r.templates).indexOf(t.name) >= 0 }), ' ' + t.label + ' (' + t.name + ')']);
    }));
    var prefix = el('input', { name: 'recipient_prefix', value: r.recipient_prefix || '', placeholder: '9779801', inputmode: 'numeric', pattern: '[0-9]*' });
    var rpat = el('input', { name: 'recipient_pattern', value: r.recipient_pattern || '', placeholder: 'regular expression on the number' });
    var words = el('input', { name: 'content_words', value: split(r.content_words).join(', '), placeholder: 'otp, verification code' });
    var cpat = el('input', { name: 'content_pattern', value: r.content_pattern || '', placeholder: 'regular expression on the text' });
    var types = el('div', { class: 'checks' }, TYPES.map(function (t) {
      return el('label', { class: 'cb' }, [el('input', { type: 'checkbox', name: 'type', value: t, checked: split(r.types).indexOf(t) >= 0 }), ' ' + t]);
    }));
    var sender = el('input', { name: 'sender_pattern', value: r.sender_pattern || '', placeholder: '^ACME' });
    var minS = el('input', { name: 'min_segments', type: 'number', min: 0, max: 100, value: r.min_segments || 0 });
    var maxS = el('input', { name: 'max_segments', type: 'number', min: 0, max: 100, value: r.max_segments || 0 });
    var mode = el('select', { name: 'mode' }, Object.keys(MODES).map(function (k) { return opt(k, { use: 'Use this provider first (the others stay as fall-backs)', only: 'Use only this provider', avoid: 'Never use this provider' }[k], r.mode === k); }));
    var provider = el('select', { name: 'provider', required: true }, [opt('', 'Choose a provider', !r.provider)].concat(state.providers.map(function (p) { return opt(p.id, p.id + ' (' + p.channel + (p.state !== 'active' ? ', ' + p.state : '') + ')', r.provider === p.id); })));
    var prio = el('input', { name: 'priority', type: 'number', min: 0, max: 9999, value: r.priority || 0 });
    var auto = el('input', { type: 'checkbox', name: 'auto', checked: !!isDraft });
    var sug = el('small', { class: 'muted' });
    var note = el('input', { name: 'note', value: r.note || '', placeholder: 'why this rule exists' });
    var enabled = el('input', { type: 'checkbox', name: 'enabled', checked: r.enabled !== 0 && r.enabled !== false });
    var live = el('div', { class: 'muted livesum' });

    var read = function () {
      var b = {
        name: name.value.trim(), enabled: enabled.checked, account: account.value, tenant: tenant.value.trim(),
        recipients: csv(recipients.value), templates: all('input[name=template]', tplBox).filter(function (i) { return i.checked; }).map(function (i) { return i.value; }),
        countries: csv(countries.value).map(function (c) { return c.toUpperCase(); }), recipient_prefix: prefix.value.trim(), recipient_pattern: rpat.value.trim(),
        sender_pattern: sender.value.trim(), content_words: csv(words.value), content_pattern: cpat.value.trim(),
        types: all('input[name=type]', types).filter(function (i) { return i.checked; }).map(function (i) { return i.value; }),
        min_segments: Number(minS.value) || 0, max_segments: Number(maxS.value) || 0, mode: mode.value, provider: provider.value,
        priority: Number(prio.value) || 0, note: note.value.trim()
      };
      return b;
    };
    var asRow = function (b) {
      return { name: b.name, account: b.account, tenant: b.tenant, countries: b.countries.join('|'), recipients: b.recipients.join('|'), templates: b.templates.join('|'), recipient_prefix: b.recipient_prefix, recipient_pattern: b.recipient_pattern,
        sender_pattern: b.sender_pattern, content_words: b.content_words.join('|'), content_pattern: b.content_pattern, types: b.types.join('|'),
        min_segments: b.min_segments, max_segments: b.max_segments, mode: b.mode, provider: b.provider, priority: b.priority };
    };
    var refresh = function () {
      var b = read(), row = asRow(b), s = specificity(row);
      sug.textContent = 'Suggested priority from how specific the rule is: ' + s;
      if (auto.checked) prio.value = s;
      row.priority = Number(prio.value) || 0;
      live.textContent = 'In words: for ' + summary(row) + ', ' + (row.provider ? action(row) : 'choose a provider') + '.';
    };
    [name, account, tenant, countries, recipients, prefix, rpat, words, cpat, sender, minS, maxS, mode, provider, prio, note].forEach(function (i) { i.addEventListener('input', refresh); i.addEventListener('change', refresh); });
    all('input', types).forEach(function (i) { i.addEventListener('change', refresh); });
    all('input', tplBox).forEach(function (i) { i.addEventListener('change', refresh); });
    prio.addEventListener('input', function () { auto.checked = false; });
    auto.addEventListener('change', refresh);

    var openScope = !preset || ['user', 'usercountry', 'userrecipient', 'usercontent', 'usertemplate', 'custom'].indexOf(preset) >= 0;
    f.appendChild(el('div', { class: 'grid2' }, [field('Name', name)]));
    f.appendChild(section('Who: account and tenant', openScope || !!r.account || !!r.tenant, [field('Account', account), field('Tenant', tenant)]));
    f.appendChild(section('Where: destination country and recipient', !preset ? (!!r.countries || !!r.recipients || !!r.recipient_prefix || !!r.recipient_pattern) : ['country', 'usercountry', 'userrecipient', 'custom'].indexOf(preset) >= 0,
      [field('Countries', countries, 'ISO codes, separated by commas'), field('Recipient number(s)', recipients, 'any written form: 9856034616, +977 9856034616, 009779856034616 (Nepal is the default country)'), field('Number starts with', prefix, 'digits, with the country code'), field('Number matches', rpat, 'regular expression')]));
    f.appendChild(section('What: the message', !preset ? (!!r.content_words || !!r.content_pattern || !!r.templates || !!r.types || !!r.sender_pattern) : ['content', 'usercontent', 'template', 'usertemplate', 'custom'].indexOf(preset) >= 0,
      [field('Message template', tplBox, 'the rule applies to messages sent with these templates'), field('Text contains any of', words, 'words or phrases, separated by commas; case does not matter'), field('Text matches', cpat, 'regular expression'), field('Message type', types), field('Sender id matches', sender, 'regular expression')]));
    f.appendChild(section('How big: segments', !preset ? (!!r.min_segments || !!r.max_segments) : ['quantity', 'custom'].indexOf(preset) >= 0, [field('At least', minS, '0: no minimum'), field('At most', maxS, '0: no maximum')]));
    f.appendChild(el('div', { class: 'fsec act' }, [el('div', { class: 'grid2' }, [field('Action', mode), field('Provider', provider)])]));
    f.appendChild(el('div', { class: 'grid2' }, [field('Priority', prio, null), el('label', { class: 'f' }, [el('span', { text: 'Priority is' }), el('label', { class: 'cb' }, [auto, ' set from specificity']), sug]), field('Note', note),
      el('label', { class: 'cb f' }, [enabled, ' Enabled'])]));
    f.appendChild(live);
    var result = el('div', { class: 'out', hidden: true });
    var buttons = el('div', { class: 'row' });
    buttons.appendChild(el('button', { type: 'submit', text: isDraft ? 'Add rule' : 'Save rule' }));
    if (!isDraft) {
      buttons.appendChild(el('button', { type: 'button', class: 'secondary', text: 'Duplicate', onclick: function () {
        var copy = JSON.parse(JSON.stringify(r)); delete copy.id; copy.name = r.name + ' (copy)'; copy._preset = 'custom'; state.draft = copy; renderList(); window.scrollTo(0, 0);
      } }));
      var confirmBox = el('span', { class: 'confirm', hidden: true }, [' Delete this rule? ',
        el('button', { type: 'button', class: 'danger small', text: 'Yes, delete', onclick: function () {
          call('DELETE', '/ui/admin/routing-rules/' + r.id).then(function (res) { if (res.status === 200) { delete state.open[r.id]; loadAll(); say($('#rr-result'), true, 'Deleted. The change is live.'); } else say(result, false, problem(res)); });
        } }), el('button', { type: 'button', class: 'secondary small', text: 'No', onclick: function () { confirmBox.hidden = true; del.hidden = false; } })]);
      var del = el('button', { type: 'button', class: 'secondary danger-text', text: 'Delete', onclick: function () { del.hidden = true; confirmBox.hidden = false; } });
      buttons.appendChild(del); buttons.appendChild(confirmBox);
    } else {
      buttons.appendChild(el('button', { type: 'button', class: 'secondary', text: 'Cancel', onclick: function () { state.draft = null; renderList(); } }));
    }
    f.appendChild(buttons); f.appendChild(result);
    f.addEventListener('submit', function (e) {
      e.preventDefault();
      var b = read();
      if (['user', 'usercountry', 'userrecipient', 'usercontent', 'usertemplate'].indexOf(preset) >= 0 && !b.account) { say(result, false, 'Choose the account this rule is for.'); return; }
      call(isDraft ? 'POST' : 'PUT', '/ui/admin/routing-rules' + (isDraft ? '' : '/' + r.id), b).then(function (res) {
        if (res.status === 200 || res.status === 201) { state.draft = null; if (!isDraft) delete state.open[r.id]; loadAll().then(function () { say($('#rr-result'), true, 'Saved. The next message uses it.'); }); }
        else say(result, false, problem(res));
      });
    });
    refresh();
    return f;
  }

  // ------------------------------------------------------------ try a route
  var SAMPLE_VARS = { otp_login: { brand: 'Acme', code: '123456', minutes: '5' }, payment_received: { currency: 'NPR', amount: '100', date: 'today', receipt: 'R-1' }, spring_sale: { discount: '10', ends: 'Sunday', url: 'https://example.com' } };
  function fillTryForm() {
    var f = $('#rr-tryform'); if (!f) return;
    var acct = f.elements.account, keep = acct.value; acct.textContent = '';
    state.users.forEach(function (u) { acct.appendChild(opt(u.id, u.id + (u.name ? ' (' + u.name + ')' : ''), u.id === keep)); });
    if (state.filter.kind === 'account') acct.value = state.filter.value;
    var tpl = f.elements.template, tk = tpl.value; tpl.textContent = ''; tpl.appendChild(opt('', '(plain text)'));
    state.templates.forEach(function (t) { tpl.appendChild(opt(t.name, t.label + ' (' + t.name + ')', t.name === tk)); });
  }
  function ruleName(id) { var r = state.rules.filter(function (x) { return x.id === id; })[0]; return r ? r.name : id; }
  function runTry(e) {
    e.preventDefault();
    var f = e.target.elements, body = { account: f.account.value, to: f.to.value.trim(), dlr: false };
    if (f.template.value) { body.template = f.template.value; body.vars = SAMPLE_VARS[f.template.value] || {}; } else body.text = f.text.value;
    if (f.type.value) body.type = f.type.value;
    if (f.from.value.trim()) body.from = f.from.value.trim();
    call('POST', '/ui/admin/route/explain', body).then(function (r) {
      var out = $('#rr-tryout'); out.hidden = false; out.textContent = '';
      if (r.status !== 200) { out.className = 'out err'; out.textContent = problem(r); return; }
      out.className = 'out';
      var b = r.body, chain = b.route || [];
      out.appendChild(el('div', {}, [el('strong', { text: 'To ' + b.to + ' (' + b.country + '), ' + b.type + ', ' + b.segments + ' segment' + (b.segments === 1 ? '' : 's') + ', price ' + (b.price || 0).toFixed(3) + ' ' + b.currency + ', ranked for ' + (b.objective || '') })]));
      var ol = el('ol', { class: 'chain' });
      chain.forEach(function (p) {
        var why = p.custom_rule ? 'rule: ' + ruleName(p.custom_rule) : 'tier: ' + (p.tier || '');
        ol.appendChild(el('li', {}, [el('strong', { text: p.id }), ' ', el('span', { class: 'badge ' + (p.sandbox ? 'avoid' : 'use'), text: p.sandbox ? 'sandbox' : 'LIVE' }), el('span', { class: 'muted', text: '  ' + why + ', channel ' + p.channel })]));
      });
      out.appendChild(chain.length ? ol : el('div', { class: 'err', text: 'No provider can carry this message.' }));
      var rej = b.rejected || [];
      if (rej.length) {
        var ul = el('ul', { class: 'chain' });
        rej.forEach(function (p) { ul.appendChild(el('li', {}, [el('strong', { text: p.id }), el('span', { class: 'muted', text: '  ' + (p.rule && p.rule.indexOf('rr_') === 0 ? 'rule: ' + ruleName(p.rule) : (p.rule || '')) + (p.reason ? ' \u2013 ' + p.reason : '') })])); });
        out.appendChild(el('div', { class: 'muted', text: 'Not used:' })); out.appendChild(ul);
      }
    });
  }

  // -------------------------------------------------------------- presets
  var PRESETS = [
    ['platform', 'Platform default provider', 'Every account, every destination.'],
    ['country', 'Country default provider', 'Every account, for numbers in a country.'],
    ['user', 'User default provider', 'One account, every destination.'],
    ['usercountry', 'User and country provider', 'One account, for numbers in a country.'],
    ['userrecipient', 'User and recipient provider', 'One account, for a number or a number range.'],
    ['content', 'Content based provider', 'Every account, when the text says something.'],
    ['usercontent', 'User content based provider', 'One account, when the text says something.'],
    ['template', 'Template based provider', 'Every account, for messages sent with a template (a login code, a receipt).'],
    ['usertemplate', 'User template based provider', 'One account, for messages sent with a template.'],
    ['quantity', 'Size based provider', 'Messages of a number of segments.'],
    ['custom', 'Custom rule', 'Any combination of conditions.']
  ];
  function showPresets() {
    var box = $('#rr-preset'); box.hidden = false; box.textContent = '';
    box.appendChild(el('div', { class: 'muted', text: 'What kind of rule?' }));
    var grid = el('div', { class: 'pgrid' });
    PRESETS.forEach(function (p) {
      grid.appendChild(el('button', { type: 'button', class: 'pbtn', onclick: function () {
        box.hidden = true;
        var d = { name: p[1], enabled: 1, account: '', tenant: '', countries: '', recipients: '', templates: '', recipient_prefix: '', recipient_pattern: '', sender_pattern: '', content_words: '', content_pattern: '', types: '', min_segments: 0, max_segments: 0, mode: 'use', provider: '', priority: 0, note: '', _preset: p[0] };
        if (state.filter.kind === 'account' && ['user', 'usercountry', 'userrecipient', 'usercontent', 'usertemplate', 'custom'].indexOf(p[0]) >= 0) d.account = state.filter.value;
        if (state.filter.kind === 'country' && ['country', 'usercountry', 'custom'].indexOf(p[0]) >= 0) d.countries = state.filter.value;
        if (state.filter.kind === 'provider') d.provider = state.filter.value;
        state.draft = d; renderList(); window.scrollTo(0, 0);
      } }, [el('strong', { text: p[1] }), el('small', { class: 'muted', text: p[2] })]));
    });
    grid.appendChild(el('button', { type: 'button', class: 'pbtn cancel', text: 'Cancel', onclick: function () { box.hidden = true; } }));
    box.appendChild(grid);
  }

  // --------------------------------------------------------------- editor
  var monacoReady = null;
  function registerBcl(monaco) {
    monaco.languages.register({ id: 'bcl' });
    monaco.languages.setLanguageConfiguration('bcl', { comments: { lineComment: '#' }, brackets: [['{', '}'], ['[', ']'], ['(', ')']], autoClosingPairs: [{ open: '{', close: '}' }, { open: '[', close: ']' }, { open: '"', close: '"' }, { open: '(', close: ')' }], folding: { markers: { start: /\{\s*$/, end: /^\s*\}/ } } });
    monaco.languages.setMonarchTokensProvider('bcl', {
      keywords: ['module', 'version', 'environment', 'decision_schema', 'decision_table', 'ranking', 'default', 'hit_policy', 'strategy', 'effects', 'row', 'priority', 'when', 'then', 'outcome', 'decision', 'reason', 'attributes', 'all', 'any', 'not', 'rules', 'rule', 'score', 'scores', 'priority_path', 'candidates', 'bcl', 'import', 'allow', 'deny', 'first', 'unique', 'collect', 'first_match', 'true', 'false', 'nil', 'matches', 'in'],
      tokenizer: { root: [
        [/#.*$/, 'comment'], [/"([^"\\]|\\.)*"/, 'string'], [/\b\d+(\.\d+)?\b/, 'number'],
        [/[a-z_][a-zA-Z0-9_]*(\.[a-z_][a-zA-Z0-9_]*)+/, 'variable'],
        [/[a-z_][a-zA-Z0-9_]*/, { cases: { '@keywords': 'keyword', '@default': 'identifier' } }],
        [/==|!=|<=|>=|<|>|&&|\|\|/, 'operator'], [/[{}()\[\]]/, '@brackets']
      ] }
    });
  }
  function loadMonaco() {
    if (monacoReady) return monacoReady;
    monacoReady = new Promise(function (resolve, reject) {
      var base = 'https://cdn.jsdelivr.net/npm/monaco-editor@0.52.0/min';
      window.MonacoEnvironment = { getWorkerUrl: function () { return URL.createObjectURL(new Blob(["self.MonacoEnvironment={baseUrl:'" + base + "/'};importScripts('" + base + "/vs/base/worker/workerMain.js');"], { type: 'text/javascript' })); } };
      var s = document.createElement('script'); s.src = base + '/vs/loader.js';
      s.onload = function () { window.require.config({ paths: { vs: base + '/vs' } }); window.require(['vs/editor/editor.main'], function () { registerBcl(window.monaco); resolve(window.monaco); }); };
      s.onerror = function () { reject(new Error('The code editor could not be loaded from cdn.jsdelivr.net; a plain text box is used instead.')); };
      document.head.appendChild(s);
    });
    return monacoReady;
  }
  // makeEditor: Monaco when it loads, a text area otherwise; the same small interface.
  function makeEditor(container, readOnly) {
    container.textContent = '';
    var api = { get: function () { return ''; }, set: function () {}, mark: function () {}, layout: function () {} };
    return loadMonaco().then(function (monaco) {
      var dark = window.matchMedia && window.matchMedia('(prefers-color-scheme: dark)').matches;
      var ed = monaco.editor.create(container, { value: '', language: 'bcl', theme: dark ? 'vs-dark' : 'vs', readOnly: !!readOnly, automaticLayout: true, minimap: { enabled: false }, scrollBeyondLastLine: false, fontSize: 13, tabSize: 2, folding: true, renderWhitespace: 'selection', wordWrap: 'off' });
      api.get = function () { return ed.getValue(); };
      api.set = function (v) { ed.setValue(v || ''); monaco.editor.setModelMarkers(ed.getModel(), 'bcl', []); };
      api.mark = function (diags) {
        monaco.editor.setModelMarkers(ed.getModel(), 'bcl', (diags || []).map(function (d) {
          var line = Math.max(1, d.line || 1), col = Math.max(1, d.column || 1);
          return { severity: d.severity === 'warning' ? monaco.MarkerSeverity.Warning : monaco.MarkerSeverity.Error, message: d.message + (d.code ? ' [' + d.code + ']' : ''), startLineNumber: line, startColumn: col, endLineNumber: line, endColumn: col + 1 };
        }));
      };
      api.layout = function () { ed.layout(); };
      api.goto = function (line) { ed.revealLineInCenter(line); ed.setPosition({ lineNumber: line, column: 1 }); ed.focus(); };
      return api;
    }).catch(function (err) {
      var ta = el('textarea', { class: 'code', rows: readOnly ? 24 : 28, spellcheck: 'false', readonly: !!readOnly }); container.appendChild(ta);
      container.appendChild(el('small', { class: 'muted', text: err.message }));
      api.get = function () { return ta.value; }; api.set = function (v) { ta.value = v || ''; };
      return api;
    });
  }

  // ------------------------------------------------------------ generated
  var generated = null;
  function loadGenerated() {
    var ready = generated ? Promise.resolve(generated) : makeEditor($('#generated-editor'), true).then(function (e) { generated = e; return e; });
    ready.then(function (e) { call('GET', '/ui/admin/rules/custom_routing').then(function (r) { e.set(r.body.source || ''); e.layout(); }); });
  }

  // ---------------------------------------------------------- definitions
  var editor = null, current = null, tryFacts = [];
  function loadDefinitions() {
    call('GET', '/ui/admin/rules').then(function (r) {
      var rows = Array.isArray(r.body) ? r.body : [], box = $('#defs'); box.textContent = '';
      rows.forEach(function (d) {
        var gen = d.name === 'custom_routing';
        box.appendChild(el('button', { type: 'button', class: 'def' + (current && current.name === d.name ? ' sel' : ''), onclick: function () { if (gen) { activate('generated'); } else openDef(d); } }, [
          el('strong', { text: d.name }), el('small', { class: 'muted', text: (d.decisions || []).concat(d.rankings || []).join(', ') }),
          el('small', { class: 'muted', text: gen ? 'generated from routing rules' : d.version + (d.overridden ? ' (edited)' : '') })]));
      });
    });
  }
  function openDef(d) {
    current = d; $('#def-editor').hidden = false; $('#def-title').textContent = d.name; $('#def-result').textContent = ''; $('#def-result').className = 'out';
    var ready = editor ? Promise.resolve(editor) : makeEditor($('#source-editor'), false).then(function (e) { editor = e; return e; });
    ready.then(function (ed) {
      call('GET', '/ui/admin/rules/' + d.name).then(function (r) {
        ed.set(r.body.source || ''); ed.layout();
        $('#def-title').textContent = d.name + ' (version ' + r.body.version + (r.body.overridden ? ', edited' : '') + ')';
        var sel = $('#try-decision'); sel.textContent = '';
        (d.decisions || []).concat(d.rankings || []).forEach(function (n) { sel.appendChild(el('option', { text: n })); });
        $('#def-reset').disabled = !d.resettable;
        renderFacts(r.body.facts); loadDefinitions();
        $('#def-editor').scrollIntoView();
      });
    });
  }
  function report(diags, okText) {
    var out = $('#def-result');
    if (!diags.length) { say(out, true, okText); return; }
    out.hidden = false; out.className = 'out err'; out.textContent = '';
    diags.forEach(function (d) {
      var line = el('div', {}, [el('a', { href: '#', text: 'line ' + d.line + ':' + d.column, onclick: function (e) { e.preventDefault(); if (editor && editor.goto) editor.goto(d.line); } }), ' ' + d.message + (d.code ? ' [' + d.code + ']' : '')]);
      out.appendChild(line);
    });
  }
  function check(after) {
    call('POST', '/ui/admin/rules/' + current.name + '/check', { source: editor.get() }).then(function (r) {
      var diags = (r.body && r.body.diagnostics) || [];
      editor.mark(diags);
      var errors = diags.filter(function (d) { return d.severity !== 'warning'; });
      report(diags, 'No problems found.');
      if (after && !errors.length) after();
    });
  }
  function renderFacts(facts) {
    tryFacts = facts || []; var box = $('#try-fields'); box.textContent = '';
    if (!tryFacts.length) { box.textContent = 'This definition reads no facts.'; return; }
    tryFacts.forEach(function (f) {
      var input = el('input', { name: f.path });
      if (f.kind === 'bool') { input.type = 'checkbox'; input.checked = f.sample === 'true'; input.className = 'inl'; }
      else if (f.kind === 'number') { input.type = 'number'; input.step = 'any'; input.value = f.sample || '0'; }
      else input.value = f.sample || '';
      box.appendChild(el('label', {}, [f.path, input]));
    });
  }
  function gather() {
    var facts = {};
    tryFacts.forEach(function (f) {
      var e = $('#try-fields [name="' + f.path + '"]');
      var v = f.kind === 'bool' ? e.checked : f.kind === 'number' ? Number(e.value) : e.value, o = facts, parts = f.path.split('.');
      parts.slice(0, -1).forEach(function (k) { o[k] = o[k] || {}; o = o[k]; });
      o[parts[parts.length - 1]] = v;
    });
    return facts;
  }

  // ----------------------------------------------------------------- tabs
  function activate(name) {
    all('#rr-tabs .tab').forEach(function (t) { t.classList.toggle('active', t.dataset.tab === name); });
    ['routing', 'generated', 'definitions'].forEach(function (n) { $('#tab-' + n).hidden = n !== name; });
    if (name === 'generated') loadGenerated();
    if (name === 'definitions') loadDefinitions();
    try { history.replaceState(null, '', location.pathname + location.search + '#' + name); } catch (e) {}
  }

  // ----------------------------------------------------------------- init
  call('GET', '/ui/me').then(function (me) {
    var roles = (me.body && me.body.roles) || [];
    if (roles.indexOf('admin') < 0) { $('main').textContent = ''; $('main').appendChild(el('section', { class: 'card' }, [el('h1', { text: 'Operator only' }), el('p', { class: 'muted', text: 'Sign in as the operator to see this page.' })])); return; }
    all('#rr-tabs .tab').forEach(function (t) { t.addEventListener('click', function () { activate(t.dataset.tab); }); });
    $('#rr-new').addEventListener('click', showPresets);
    $('#rr-tryform').addEventListener('submit', runTry);
    $('#rr-search').addEventListener('input', function (e) { state.search = e.target.value; renderList(); });
    $('#rr-expand').addEventListener('click', function () { state.tree.Accounts = state.tree.Countries = state.tree.Providers = true; state.rules.forEach(function (r) { state.open[r.id] = true; }); ['Recipient', 'Content', 'Size, type and sender', 'Country', 'Default'].forEach(function (g) { state.open['g:' + g] = true; }); renderTree(); renderList(); });
    $('#rr-collapse').addEventListener('click', function () { state.tree.Accounts = state.tree.Countries = state.tree.Providers = false; state.open = {}; ['Recipient', 'Content', 'Size, type and sender', 'Country', 'Default'].forEach(function (g) { state.open['g:' + g] = false; }); renderTree(); renderList(); });
    $('#generated-refresh').addEventListener('click', loadGenerated);
    $('#def-check').addEventListener('click', function () { check(); });
    $('#def-save').addEventListener('click', function () {
      check(function () {
        call('PUT', '/ui/admin/rules/' + current.name, { source: editor.get() }).then(function (r) {
          if (r.status === 200) { say($('#def-result'), true, 'Saved as version ' + r.body.version + '. The next message uses it.'); loadDefinitions(); } else say($('#def-result'), false, problem(r));
        });
      });
    });
    $('#def-reset').addEventListener('click', function () {
      call('POST', '/ui/admin/rules/' + current.name + '/reset').then(function (r) {
        if (r.status === 200) { say($('#def-result'), true, 'Back to the file version.'); openDef(current); } else say($('#def-result'), false, problem(r));
      });
    });
    $('#try-run').addEventListener('click', function () {
      call('POST', '/ui/admin/rules/' + current.name + '/try', { decision: $('#try-decision').value, facts: gather() }).then(function (r) {
        var o = $('#try-result');
        if (r.status !== 200) { say(o, false, problem(r)); return; }
        var b = r.body; o.hidden = false; o.className = 'out';
        o.textContent = 'Effect: ' + b.effect + (b.rule ? '   Rule: ' + b.rule : '') + (b.reason ? '\nReason: ' + b.reason : '') +
          '\n' + Object.keys(b.attributes || {}).map(function (k) { return k + ' = ' + b.attributes[k]; }).join('\n') + (b.trace && b.trace.length ? '\n\nTrace:\n  ' + b.trace.join('\n  ') : '');
      });
    });
    var q = new URLSearchParams(location.search);
    if (q.get('account')) { state.filter = { kind: 'account', value: q.get('account') }; state.tree.Accounts = true; }
    loadAll().then(function () {
      var tab = (location.hash || '').replace('#', '');
      if (['generated', 'definitions'].indexOf(tab) >= 0) activate(tab);
    });
  });
})();
