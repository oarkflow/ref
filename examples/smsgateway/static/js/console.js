(function () {
  var $ = function (s) { return document.querySelector(s); };
  function call(method, path, body) {
    return fetch(path, { method: method, credentials: 'same-origin', headers: { 'Content-Type': 'application/json' }, body: body ? JSON.stringify(body) : undefined })
      .then(function (r) {
        if (r.status === 401 && path !== '/login') { location.href = '/login'; return new Promise(function () {}); }
        return r.json().catch(function () { return {}; }).then(function (j) { return { status: r.status, body: j }; });
      });
  }
  function cell(v) { var td = document.createElement('td'); td.textContent = v == null ? '' : v; return td; }
  function fill(sel, rows, cols) {
    var tb = $(sel + ' tbody'); if (!tb) return; tb.innerHTML = '';
    (Array.isArray(rows) ? rows : []).forEach(function (r) {
      var tr = document.createElement('tr');
      cols.forEach(function (c) { tr.appendChild(cell(typeof c === 'function' ? c(r) : r[c])); });
      tb.appendChild(tr);
    });
  }
  function money(v) { return typeof v === 'number' ? v.toFixed(3) : v; }
  function lines(box, rows) {
    box.textContent = '';
    rows.forEach(function (r) {
      var div = document.createElement('div');
      var b = document.createElement('strong'); b.textContent = r[0] + ': ';
      div.appendChild(b); div.appendChild(document.createTextNode(r[1] == null ? '' : r[1]));
      box.appendChild(div);
    });
  }
  function summary(r, explain) {
    var o = $('#result'), b = r.body || {};
    if (r.status >= 400 || b.error) {
      var e = b.error || {};
      lines(o, [['Not sent', (e.message || 'request failed') + (e.code ? ' (' + e.code + ')' : '')]]);
      return;
    }
    var rows = [];
    if (!explain) rows.push(['Accepted', 'message ' + b.id + ' is ' + b.state]);
    rows.push(['To', b.to + ' (' + b.country + ')']);
    rows.push(['Type', b.type + ', ' + b.segments + ' segment' + (b.segments === 1 ? '' : 's') + ', ' + b.encoding]);
    rows.push(['Price', money(b.price) + ' ' + b.currency]);
    var chain = explain ? (b.route || []).map(function (p) { return p.id + ' (' + p.tier + ')'; }) : (b.route || []);
    rows.push(['Route', chain.join(' \u2192 ') || 'none']);
    rows.push(['Mode', b.sandbox ? 'SANDBOX route: nothing leaves this machine, the carrier is a stand-in' : 'live']);
    if (explain && (b.rejected || []).length) {
      rows.push(['Not used', b.rejected.map(function (p) { return p.id + (p.reason ? ': ' + p.reason : ''); }).join('; ')]);
    }
    if (explain) rows.push(['Optimising for', b.objective]);
    lines(o, rows);
  }
  function show(r) { var o = $('#result'); if (o) o.textContent = r.status + ' ' + JSON.stringify(r.body, null, 2); }

  // Sign in
  var lf = $('#loginform');
  if (lf) {
    lf.addEventListener('submit', function (e) {
      e.preventDefault();
      call('POST', '/login', { email: lf.elements.email.value, password: lf.elements.password.value }).then(function (r) {
        if (r.status === 200) { location.href = '/'; } else { show(r); }
      });
    });
    return;
  }

  // Everything else needs a session.
  call('GET', '/ui/me').then(function (r) {
    var roles = r.body.roles || [], admin = roles.indexOf('admin') >= 0;
    $('#who').textContent = r.body.name || r.body.id;
    var out = $('#logout'); out.hidden = false;
    out.addEventListener('click', function () { call('POST', '/logout').then(function () { location.href = '/login'; }); });
    document.querySelectorAll('.admin-only').forEach(function (el) { el.hidden = !admin; });
    window.refreshBell = function () {
      call('GET', '/ui/notifications').then(function (n) {
        var c = $('#bell-count'); if (!c || n.status !== 200) return;
        c.hidden = !(n.body.unread > 0); c.textContent = n.body.unread;
      });
    };
    window.refreshBell(); setInterval(window.refreshBell, 60000);
    page(admin);
  });

  function page(admin) {
    var sf = $('#sendform');
    if (sf) {
      var catalog = {}, current = null, timer = null;
      var key = function (t) { return t.name + '::' + t.lang; };
      var values = function () {
        var v = {};
        document.querySelectorAll('#fieldlist input').forEach(function (i) { v[i.name] = i.value; });
        return v;
      };
      var payload = function () {
        var f = sf.elements, p = { to: f.to.value };
        if (f.reference.value) p.reference = f.reference.value;
        if (current) { p.template = current.name; if (current.lang) p.lang = current.lang; p.vars = values(); }
        else p.text = f.body.value;
        return p;
      };
      var preview = function () {
        if (!current) return;
        call('POST', '/ui/templates/preview', { template: current.name, lang: current.lang || undefined, vars: values() }).then(function (r) {
          var b = r.body || {}, t = $('#preview-text');
          t.textContent = b.error ? ('Cannot render: ' + b.error) : (b.text || '(empty)');
          $('#preview-meta').textContent = b.error ? '' :
            b.characters + ' characters, ' + b.segments + ' segment' + (b.segments === 1 ? '' : 's') + ', ' + b.encoding +
            (b.type ? ', type ' + b.type : '') + (b.sender ? ', sender ' + b.sender : '');
        });
      };
      var later = function () { clearTimeout(timer); timer = setTimeout(preview, 200); };
      var choose = function (k) {
        current = k ? catalog[k] : null;
        var fields = [];
        if (current) { try { fields = JSON.parse(current.fields || '[]'); } catch (e) { fields = []; } }
        $('#plain').hidden = !!current;
        $('#placeholders').hidden = !current;
        $('#preview').hidden = !current;
        $('#template-note').hidden = !current;
        $('#template-note').textContent = current ? current.description : '';
        var list = $('#fieldlist'); list.innerHTML = '';
        fields.forEach(function (fd) {
          var label = document.createElement('label');
          label.appendChild(document.createTextNode(fd.label || fd.name));
          var tag = document.createElement('code'); tag.textContent = ' ${' + fd.name + '}'; tag.className = 'muted'; label.appendChild(tag);
          var input = document.createElement('input'); input.name = fd.name; input.value = fd.default || ''; input.placeholder = fd.hint || '';
          input.addEventListener('input', later); label.appendChild(input);
          list.appendChild(label);
        });
        if (current) preview();
      };
      call('GET', '/ui/templates').then(function (r) {
        var sel = $('#template');
        if (r.status !== 200 || !Array.isArray(r.body) || r.body.length === 0) {
          var note = $('#template-note'); note.hidden = false;
          note.textContent = 'No templates are available' + (r.status !== 200 ? ' (the catalog could not be loaded: ' + r.status + ')' : '') + '. You can still send plain text.';
        }
        (r.body || []).forEach(function (t) {
          catalog[key(t)] = t;
          var o = document.createElement('option'); o.value = key(t); o.textContent = t.label; sel.appendChild(o);
        });
      });
      $('#template').addEventListener('change', function (e) { choose(e.target.value); });
      sf.elements.body.addEventListener('input', function () { $('#plain-count').textContent = sf.elements.body.value.length + ' characters'; });
      var balance = function () { call('GET', '/ui/balance').then(function (r) { $('#balance').textContent = JSON.stringify(r.body, null, 2); }); };
      sf.addEventListener('submit', function (e) { e.preventDefault(); call('POST', '/ui/messages', payload()).then(function (r) { summary(r, false); }).then(balance); });
      $('#explain').addEventListener('click', function () { call('POST', '/ui/route/explain', payload()).then(function (r) { summary(r, true); }); });
      balance();
    }
    if ($('#messages')) {
      call('GET', '/ui/messages').then(function (r) { fill('#messages', r.body, ['id', 'to', 'state', function (m) { return m.provider + (m.sandbox ? ' (sandbox)' : ''); }, 'segments', function (m) { return m.price / 1000000; }]); });
      call('GET', '/ui/ledger').then(function (r) { fill('#ledger', r.body, ['message_id', 'kind', function (l) { return l.amount / 1000000; }]); });
    }
    var guard = function () {
      if (admin) return true;
      document.querySelector('main').innerHTML = '<section class="card"><h1>Operator only</h1><p class="muted">Sign in as the operator to see this page.</p></section>';
      return false;
    };
    var csv = function (v) { return (v || '').split(',').map(function (x) { return x.trim(); }).filter(Boolean); };
    var pipes = function (v) { return (v || '').split('|').filter(Boolean).join(', '); };
    var say = function (sel, ok, text) { var o = $(sel); o.className = 'out ' + (ok ? 'ok' : 'err'); o.textContent = text; };
    var problem = function (r) { var e = (r.body && r.body.error) || {}; return (e.message || ('request failed (' + r.status + ')')) + (e.code ? ' [' + e.code + ']' : ''); };

    // ---- Providers --------------------------------------------------------
    if ($('#provider-table')) {
      if (!guard()) return;
      var form = $('#providerform'), editing = null, channels = [];
      var list = function () {
        call('GET', '/ui/admin/providers').then(function (r) {
          var rows = Array.isArray(r.body) ? r.body : [];
          fill('#provider-table', rows, ['id', 'channel', function (p) { return p.sandbox ? 'sandbox' : 'LIVE'; }, 'state', 'quality', function (p) { return pipes(p.countries) || 'all'; },
            function (p) { return p.assigned_only ? (pipes(p.tenants) + ' ' + pipes(p.users)).trim() || 'nobody' : ''; }, 'successes', 'failures', function () { return 'edit'; }]);
          document.querySelectorAll('#provider-table tbody tr').forEach(function (tr, i) {
            var td = tr.lastChild; td.style.cursor = 'pointer'; td.style.color = 'var(--accent)';
            td.addEventListener('click', function () { edit(rows[i].id); });
          });
        });
      };
      var costs = function () {
        if (!editing) return;
        call('GET', '/ui/admin/providers/' + editing).then(function (r) {
          var cs = (r.body && r.body.costs) || [];
          fill('#costs', cs, ['country', 'per_segment', function () { return 'remove'; }]);
          document.querySelectorAll('#costs tbody tr').forEach(function (tr, i) {
            var td = tr.lastChild; td.style.cursor = 'pointer'; td.style.color = 'var(--accent)';
            td.addEventListener('click', function () { call('DELETE', '/ui/admin/providers/' + editing + '/costs/' + encodeURIComponent(cs[i].country)).then(costs); });
          });
        });
      };
      // ---- settings: the fields come from the provider's channel, so providers of
      // different channels have different forms. Typed inputs, never JSON.
      var settingFields = function () {
        var c = channels.filter(function (x) { return x.name === form.elements.channel.value; })[0];
        try { return JSON.parse((c && c.setting_fields) || '[]'); } catch (e) { return []; }
      };
      var control = function (fd, value) {
        var input;
        if (fd.kind === 'bool') { input = document.createElement('input'); input.type = 'checkbox'; input.checked = value === true || value === 'true'; }
        else if (fd.kind === 'select') {
          input = document.createElement('select');
          (fd.options || []).forEach(function (o) { var op = document.createElement('option'); op.value = o; op.textContent = o; input.appendChild(op); });
          input.value = value == null || value === '' ? (fd.default || '') : value;
        } else {
          input = document.createElement('input');
          input.type = fd.kind === 'number' ? 'number' : fd.kind === 'url' ? 'url' : 'text';
          if (fd.kind === 'number') input.step = 'any';
          input.value = value == null || value === '' ? (fd.default == null ? '' : fd.default) : value;
          input.placeholder = fd.hint ? '' : '';
        }
        input.name = fd.name; if (fd.required && fd.kind !== 'bool') input.required = true;
        return input;
      };
      var renderSettings = function (values) {
        var box = $('#settingfields'); box.innerHTML = '';
        var fields = settingFields();
        $('#settings-note').textContent = fields.length ? 'These apply to every message sent through this provider.' : 'This channel has no settings.';
        fields.forEach(function (fd) {
          var label = document.createElement('label'); label.appendChild(document.createTextNode(fd.label || fd.name));
          if (fd.kind === 'bool') { var cb = control(fd, values[fd.name]); cb.style.width = 'auto'; cb.style.display = 'inline-block'; cb.style.marginLeft = '8px'; label.appendChild(cb); }
          else label.appendChild(control(fd, values[fd.name]));
          if (fd.hint) { var h = document.createElement('small'); h.className = 'muted'; h.style.display = 'block'; h.style.fontWeight = '400'; h.textContent = fd.hint; label.appendChild(h); }
          box.appendChild(label);
        });
      };
      var currentSettings = {};
      $('#settingsform').addEventListener('submit', function (e) {
        e.preventDefault();
        if (!editing) { say('#settings-result', false, 'Save the provider first.'); return; }
        var values = {};
        settingFields().forEach(function (fd) {
          var el = document.querySelector('#settingfields [name="' + fd.name + '"]');
          if (!el) return;
          values[fd.name] = fd.kind === 'bool' ? el.checked : fd.kind === 'number' ? (el.value === '' ? null : Number(el.value)) : el.value;
        });
        call('PUT', '/ui/admin/providers/' + editing + '/settings', { values: values }).then(function (r) {
          if (r.status === 200) { currentSettings = values; say('#settings-result', true, 'Saved. The next message uses it.'); } else say('#settings-result', false, problem(r));
        });
      });

      // ---- credentials: several accounts per provider; secrets are write-only
      var credFields = function () {
        var c = channels.filter(function (x) { return x.name === form.elements.channel.value; })[0];
        try { return JSON.parse((c && c.credential_fields) || '[]'); } catch (e) { return []; }
      };
      var accountForm = $('#accountform'), editingAccount = null, testing = null;
      var renderCredFields = function (account) {
        var box = $('#credfields'); box.innerHTML = '';
        var pub = {}; try { pub = JSON.parse((account && account.public) || '{}'); } catch (e) {}
        credFields().forEach(function (fd) {
          var label = document.createElement('label'); label.appendChild(document.createTextNode(fd.label || fd.name));
          var input = document.createElement('input'); input.name = (fd.secret ? 'secret.' : 'public.') + fd.name;
          if (fd.secret) { input.type = 'password'; input.autocomplete = 'new-password'; input.placeholder = account && account.has_secret ? '(unchanged)' : ''; }
          else input.value = pub[fd.name] == null ? '' : pub[fd.name];
          label.appendChild(input); box.appendChild(label);
        });
      };
      var loadAccounts = function () {
        if (!editing) { fill('#accounts', [], []); return; }
        call('GET', '/ui/admin/providers/' + editing + '/accounts').then(function (r) {
          var rows = Array.isArray(r.body) ? r.body : [];
          fill('#accounts', rows, ['name', 'state', function (a) { try { return Object.entries(JSON.parse(a.public || '{}')).map(function (e) { return e[0] + '=' + e[1]; }).join(', '); } catch (e) { return ''; } },
            function (a) { return a.has_secret ? 'set' : 'none'; }, 'used', 'failures',
            function (a) { return a.cooldown_until_ms > Date.now() ? new Date(a.cooldown_until_ms).toLocaleTimeString() : ''; }, function () { return 'edit / test / remove'; }]);
          document.querySelectorAll('#accounts tbody tr').forEach(function (tr, i) {
            var td = tr.lastChild; td.textContent = '';
            [['edit', function () { editingAccount = rows[i]; accountForm.elements.name.value = rows[i].name; accountForm.elements.name.readOnly = true; accountForm.elements.state.value = rows[i].state; accountForm.elements.note.value = rows[i].note || ''; accountForm.elements.clear_rest.checked = false; renderCredFields(rows[i]); }],
             ['test', function () { testing = rows[i].name; $('#test-panel').hidden = false; $('#test-title').textContent = 'Test ' + editing + ' / ' + testing; $('#test-result').textContent = ''; $('#test-panel').scrollIntoView(); }],
             ['remove', function () { call('DELETE', '/ui/admin/providers/' + editing + '/accounts/' + encodeURIComponent(rows[i].name)).then(loadAccounts); }]].forEach(function (a) {
              var b = document.createElement('a'); b.textContent = a[0]; b.href = '#'; b.style.marginRight = '10px';
              b.addEventListener('click', function (e) { e.preventDefault(); a[1](); }); td.appendChild(b);
            });
          });
        });
      };
      $('#test-close').addEventListener('click', function () { $('#test-panel').hidden = true; });
      $('#testform').addEventListener('submit', function (e) {
        e.preventDefault(); var f = e.target.elements;
        var o = $('#test-result'); o.className = 'out'; o.textContent = 'Sending...';
        call('POST', '/ui/admin/providers/' + editing + '/accounts/' + encodeURIComponent(testing) + '/test', { to: f.to.value, text: f.text.value }).then(function (r) {
          if (r.status !== 200) { say('#test-result', false, problem(r)); return; }
          var b = r.body;
          if (b.ok) say('#test-result', true, 'Accepted by ' + b.channel + (b.sandbox ? ' (SANDBOX: a stand-in, nothing was really sent)' : ' (LIVE: a real message was sent)') + '\nProvider message id: ' + b.provider_message_id + '\nTo ' + b.to + ' as ' + b.from + ', ' + b.latency_ms + ' ms');
          else say('#test-result', false, 'Refused: ' + (b.error || 'no reason given') + '\nThe credentials or settings are wrong, or the provider is unreachable.');
        });
      });
      var newAccount = function () { editingAccount = null; accountForm.reset(); accountForm.elements.name.readOnly = false; renderCredFields(null); };
      $('#new-cred').addEventListener('click', newAccount);
      form.elements.channel.addEventListener('change', function () { renderCredFields(editingAccount); renderSettings(currentSettings); });
      accountForm.addEventListener('submit', function (e) {
        e.preventDefault();
        if (!editing) { say('#account-result', false, 'Save the provider first.'); return; }
        var f = accountForm.elements, pub = {}, sec = {};
        credFields().forEach(function (fd) {
          var v = f[(fd.secret ? 'secret.' : 'public.') + fd.name].value;
          if (fd.secret) { if (v !== '') sec[fd.name] = v; } else pub[fd.name] = v;
        });
        call('PUT', '/ui/admin/providers/' + editing + '/accounts/' + encodeURIComponent(f.name.value.trim()),
          { state: f.state.value, note: f.note.value, public: pub, secret: sec, reset: f.clear_rest.checked }).then(function (r) {
          if (r.status === 200) { say('#account-result', true, 'Saved. The next message may use it.'); newAccount(); loadAccounts(); }
          else say('#account-result', false, problem(r));
        });
      });
      var fillForm = function (p) {
        var f = form.elements;
        f.id.value = p.id || ''; f.id.readOnly = !!p.id;
        f.channel.value = p.channel || ''; f.kind.value = p.kind || 'http'; f.state.value = p.state || 'active';
        f.quality.value = p.quality == null ? 80 : p.quality; f.delivery_rate.value = p.delivery_rate == null ? 0.95 : p.delivery_rate;
        ['countries', 'message_types', 'tenants', 'users', 'sender_countries'].forEach(function (k) { f[k].value = pipes(p[k]); });
        f.owner.value = p.owner || ''; f.prefix_pattern.value = p.prefix_pattern || '';
        f.max_attempts.value = p.max_attempts || 3; f.backoff_initial_s.value = p.backoff_initial_s == null ? 1 : p.backoff_initial_s;
        f.backoff_max_s.value = p.backoff_max_s == null ? 60 : p.backoff_max_s; f.backoff_factor.value = p.backoff_factor || 2;
        f.description.value = p.description || '';
        ['sandbox', 'assigned_only', 'supports_unicode', 'supports_dlr', 'supports_alpha', 'supports_long'].forEach(function (k) {
          f[k].checked = p[k] == null ? k !== 'assigned_only' : !!p[k];
        });
      };
      var open = function (p, title) { $('#editor').hidden = false; $('#editor-title').textContent = title; fillForm(p); renderSettings(currentSettings); $('#provider-result').textContent = ''; costs(); newAccount(); loadAccounts(); $('#editor').scrollIntoView(); };
      var edit = function (id) { editing = id; call('GET', '/ui/admin/providers/' + id).then(function (r) { var p = r.body.provider || {}; try { currentSettings = JSON.parse(p.settings || '{}'); } catch (e) { currentSettings = {}; } open(p, 'Provider ' + id); }); };
      call('GET', '/ui/admin/channels').then(function (r) {
        channels = Array.isArray(r.body) ? r.body : [];
        channels.forEach(function (c) { var o = document.createElement('option'); o.value = c.name; o.textContent = c.name + ' (' + c.kind + ')'; form.elements.channel.appendChild(o); });
        list();
      });
      $('#new-provider').addEventListener('click', function () { editing = null; currentSettings = {}; open({}, 'New provider'); $('#costs tbody').innerHTML = ''; $('#accounts tbody').innerHTML = ''; });
      $('#cancel-provider').addEventListener('click', function () { $('#editor').hidden = true; });
      form.addEventListener('submit', function (e) {
        e.preventDefault();
        var f = form.elements, id = f.id.value.trim();
        var body = {
          channel: f.channel.value, kind: f.kind.value, state: f.state.value, quality: Number(f.quality.value), delivery_rate: Number(f.delivery_rate.value),
          countries: csv(f.countries.value), message_types: csv(f.message_types.value), tenants: csv(f.tenants.value), users: csv(f.users.value),
          sender_countries: csv(f.sender_countries.value), owner: f.owner.value, prefix_pattern: f.prefix_pattern.value,
          sandbox: f.sandbox.checked, assigned_only: f.assigned_only.checked, supports_unicode: f.supports_unicode.checked, supports_dlr: f.supports_dlr.checked,
          supports_alpha: f.supports_alpha.checked, supports_long: f.supports_long.checked,
          max_attempts: Number(f.max_attempts.value), backoff_initial_s: Number(f.backoff_initial_s.value), backoff_max_s: Number(f.backoff_max_s.value),
          backoff_factor: Number(f.backoff_factor.value), description: f.description.value
        };
        call('PUT', '/ui/admin/providers/' + id, body).then(function (r) {
          if (r.status === 200) { editing = id; f.id.readOnly = true; say('#provider-result', true, 'Saved. The next message uses it.'); list(); costs(); }
          else say('#provider-result', false, problem(r));
        });
      });
      $('#costform').addEventListener('submit', function (e) {
        e.preventDefault();
        if (!editing) { say('#provider-result', false, 'Save the provider first.'); return; }
        var f = e.target.elements;
        call('PUT', '/ui/admin/providers/' + editing + '/costs/' + encodeURIComponent(f.country.value.trim()), { per_segment: Number(f.per_segment.value) }).then(function (r) {
          if (r.status === 200) { f.country.value = ''; f.per_segment.value = ''; costs(); } else say('#provider-result', false, problem(r));
        });
      });
      list();
    }

    // ---- Accounts ---------------------------------------------------------
    if ($('#account-table')) {
      if (!guard()) return;
      var aform = $('#accountform'), account = null;
      var users = function () {
        call('GET', '/ui/admin/users').then(function (r) {
          var rows = (r.body && r.body.rows) || [];
          fill('#account-table', rows, ['id', 'name', 'status', function (u) { return money((u.balance || 0) / 1000000); }, 'daily_limit', 'plan', function () { return 'edit'; }]);
          document.querySelectorAll('#account-table tbody tr').forEach(function (tr, i) {
            var td = tr.lastChild; td.style.cursor = 'pointer'; td.style.color = 'var(--accent)';
            td.addEventListener('click', function () { loadAccount(rows[i].id); });
          });
        });
      };
      var showAccount = function (u, title) {
        var f = aform.elements; $('#account-editor').hidden = false; $('#account-title').textContent = title;
        f.id.value = u.id || ''; f.id.readOnly = !!u.id; f.name.value = u.name || ''; f.tenant.value = u.tenant || ''; f.status.value = u.status || 'active';
        f.default_sender.value = u.default_sender || ''; f.senders.value = pipes(u.senders); f.countries.value = pipes(u.countries);
        f.objective.value = u.objective || ''; f.daily_limit.value = u.daily_limit || 0; f.max_price.value = u.max_price || 0;
        f.rate_per_second.value = u.rate_per_second || 0; f.plan.value = u.plan || '';
        $('#account-tools').hidden = !u.id; $('#account-result').textContent = ''; $('#account-rules').href = '/admin/rules?account=' + encodeURIComponent(u.id || '');
        $('#account-editor').scrollIntoView();
      };
      var loadAccount = function (id) { account = id; call('GET', '/ui/admin/users/' + id).then(function (r) { showAccount(r.body, 'Account ' + id); }); };
      $('#new-account').addEventListener('click', function () { account = null; showAccount({}, 'New account'); });
      $('#cancel-account').addEventListener('click', function () { $('#account-editor').hidden = true; });
      aform.addEventListener('submit', function (e) {
        e.preventDefault();
        var f = aform.elements, id = f.id.value.trim();
        call('PUT', '/ui/admin/users/' + id, {
          name: f.name.value, tenant: f.tenant.value, status: f.status.value, default_sender: f.default_sender.value, senders: csv(f.senders.value),
          countries: csv(f.countries.value), objective: f.objective.value, daily_limit: Number(f.daily_limit.value), max_price: Number(f.max_price.value),
          rate_per_second: Number(f.rate_per_second.value), plan: f.plan.value
        }).then(function (r) {
          if (r.status === 200) { account = id; f.id.readOnly = true; $('#account-tools').hidden = false; say('#account-result', true, 'Saved.'); users(); }
          else say('#account-result', false, problem(r));
        });
      });
      $('#topupform').addEventListener('submit', function (e) {
        e.preventDefault(); var f = e.target.elements;
        call('POST', '/ui/admin/users/' + account + '/topup', { amount: Number(f.amount.value), reference: f.reference.value }).then(function (r) {
          if (r.status === 200) { say('#account-result', true, (r.body.applied ? 'Added. ' : 'Already applied (same reference). ') + 'Balance ' + money(r.body.balance)); f.reference.value = ''; users(); }
          else say('#account-result', false, problem(r));
        });
      });
      $('#loginform2').addEventListener('submit', function (e) {
        e.preventDefault(); var f = e.target.elements;
        call('PUT', '/ui/admin/users/' + account + '/password', { email: f.email.value, password: f.password.value }).then(function (r) {
          if (r.status === 200) { say('#account-result', true, 'They can now sign in as ' + f.email.value); f.password.value = ''; } else say('#account-result', false, problem(r));
        });
      });
      $('#issue-key').addEventListener('click', function () {
        call('POST', '/ui/admin/users/' + account + '/key').then(function (r) {
          if (r.status === 200) say('#account-result', true, 'New API key (shown once, the old one stops working): ' + r.body.api_key); else say('#account-result', false, problem(r));
        });
      });
      users();
    }

    if ($('#providers')) {
      if (!admin) { document.querySelector('main').innerHTML = '<section class="card"><h1>Operator only</h1><p class="muted">Sign in as the operator to see this page.</p></section>'; return; }
      var load = function () {
        call('GET', '/ui/admin/providers').then(function (r) {
          fill('#providers', r.body, ['id', 'kind', 'state', 'quality', 'countries', 'successes', 'failures', function (p) { return p.state === 'active' ? 'pause' : 'resume'; }]);
          document.querySelectorAll('#providers tbody tr').forEach(function (tr) {
            var td = tr.lastChild, id = tr.firstChild.textContent, next = td.textContent === 'pause' ? 'paused' : 'active';
            td.style.cursor = 'pointer'; td.style.color = 'var(--accent)';
            td.addEventListener('click', function () { call('PUT', '/ui/admin/providers/' + id + '/state', { state: next }).then(load); });
          });
        });
      };
      var approvals = function () {
        call('GET', '/ui/admin/approvals').then(function (r) {
          fill('#approvals', r.body, [function (t) { return t.title; }, function (t) { return (t.data && t.data.user_id) || ''; }, function () { return 'approve / reject'; }]);
          var rows = document.querySelectorAll('#approvals tbody tr');
          (r.body || []).forEach(function (t, i) {
            var td = rows[i].lastChild; td.textContent = '';
            ['approve', 'reject'].forEach(function (action) {
              var b = document.createElement('button'); b.textContent = action; b.className = action === 'reject' ? 'secondary' : ''; b.style.marginRight = '6px';
              b.addEventListener('click', function () { call('POST', '/ui/admin/approvals/' + t.task_id, { action: action }).then(function () { setTimeout(approvals, 300); }); });
              td.appendChild(b);
            });
          });
        });
      };
      load(); approvals();
      call('GET', '/ui/admin/stats').then(function (r) { $('#stats').textContent = JSON.stringify(r.body, null, 2); });
      call('GET', '/ui/admin/messages').then(function (r) { fill('#allmessages', r.body, ['id', 'user_id', 'state', 'provider', 'to', 'err_code']); });
    }
  }
})();
