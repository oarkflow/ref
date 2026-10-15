/**
 * CLEAR Medical Coding Platform - workflow pages.
 *
 * One script drives three pages: the work queue (start a case), a case (the
 * stage form built from the engine's view) and workspace settings. The server
 * decides everything: which forms and fields appear, which are editable or
 * disabled, which are required, what a dependent select offers. This script
 * only renders what it is told and sends back what the user typed.
 *
 * Values from the server go into the DOM through textContent / value, never
 * innerHTML.
 */
(function () {
  'use strict';

  // --- helpers ---------------------------------------------------------------

  function api(method, path, body) {
    return fetch(path, {
      method: method,
      credentials: 'same-origin',
      headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
      body: body === undefined ? undefined : JSON.stringify(body)
    }).then(function (res) {
      return res.text().then(function (text) {
        var data = null;
        try { data = text ? JSON.parse(text) : null; } catch (e) { data = null; }
        if (!res.ok) {
          var failure = data && data.error ? data.error : {};
          var err = new Error(failure.message || res.statusText || 'Request failed');
          err.status = res.status;
          err.details = failure.details || [];
          throw err;
        }
        return data;
      });
    });
  }

  function el(tag, attrs) {
    var node = document.createElement(tag);
    if (attrs) {
      Object.keys(attrs).forEach(function (k) {
        var v = attrs[k];
        if (v === undefined || v === null || v === false) return;
        if (k === 'class') node.className = v;
        else if (k === 'text') node.textContent = v;
        else if (k === 'value') node.value = v;
        else if (k.slice(0, 2) === 'on' && typeof v === 'function') node.addEventListener(k.slice(2), v);
        else node.setAttribute(k, v === true ? '' : v);
      });
    }
    for (var i = 2; i < arguments.length; i++) {
      var child = arguments[i];
      if (child === undefined || child === null || child === false) continue;
      node.appendChild(typeof child === 'string' ? document.createTextNode(child) : child);
    }
    return node;
  }

  function clear(node) {
    while (node.firstChild) node.removeChild(node.firstChild);
  }

  function formatTime(iso) {
    if (!iso) return '';
    var d = new Date(iso);
    return isNaN(d.getTime()) ? iso : d.toLocaleString();
  }

  function notify(root, kind, message) {
    var box = root.querySelector('.wf-notice');
    if (!box) return;
    box.className = 'wf-notice wf-notice-' + kind;
    box.textContent = message;
    box.hidden = !message;
  }

  // --- case page -----------------------------------------------------------------

  function CasePage(root) {
    this.root = root;
    this.id = root.getAttribute('data-case') || location.pathname.split('/').filter(Boolean).pop();
    this.view = null;
    this.controls = {}; // path -> element (single forms) or list for repeatable rows
  }

  CasePage.prototype.load = function () {
    var self = this;
    return api('GET', '/workflow/api/cases/' + encodeURIComponent(this.id))
      .then(function (view) { self.render(view); })
      .catch(function (err) {
        clear(self.root);
        self.root.appendChild(el('div', { class: 'alert alert-error' },
          el('div', { class: 'alert-content' },
            el('span', { class: 'alert-title', text: err.status === 404 ? 'Case not found' : 'Could not open the case' }),
            el('p', { class: 'alert-message', text: err.message }))));
      });
  };

  CasePage.prototype.render = function (view) {
    var self = this;
    this.view = view;
    this.controls = {};
    clear(this.root);

    var c = view.case || {};
    var stage = view.stage || {};

    this.root.appendChild(el('div', { class: 'wf-notice', hidden: true, role: 'status' }));

    // Header: number, stage, status, SLA.
    var chips = el('div', { class: 'wf-chips' });
    chips.appendChild(el('span', { class: 'wf-chip', text: 'Stage: ' + (stage.title || stage.name || '') }));
    chips.appendChild(el('span', { class: 'wf-chip wf-chip-' + (c.status || ''), text: (c.status || '').replace(/_/g, ' ') }));
    var sla = view.work && view.work.sla;
    if (stage.due_at) {
      var overdue = new Date(stage.due_at).getTime() < Date.now() && stage.status === 'active';
      chips.appendChild(el('span', { class: 'wf-chip ' + (overdue ? 'wf-chip-danger' : 'wf-chip-ok'),
        text: 'Due ' + formatTime(stage.due_at) + (sla && sla.status ? ' - ' + sla.status.replace(/_/g, ' ') : '') }));
    }
    if (view.work && view.work.assignee) {
      chips.appendChild(el('span', { class: 'wf-chip', text: 'Assigned to ' + view.work.assignee }));
    }
    if (view.work && view.work.suspended) {
      chips.appendChild(el('span', { class: 'wf-chip wf-chip-warn', text: 'On hold: ' + (view.work.suspended.reason || '') }));
    }
    this.root.appendChild(el('div', { class: 'panel wf-header' },
      el('div', { class: 'wf-title-row' },
        el('div', null,
          el('span', { class: 'panel-label', text: c.title || 'Case' }),
          el('h1', { class: 'page-title', text: c.number || c.id || '' })),
        this.workButtons(view)),
      chips,
      this.progress(view.progress || [])));

    (view.corrections || []).forEach(function (flag) {
      self.root.appendChild(el('div', { class: 'alert alert-warning' },
        el('div', { class: 'alert-content' },
          el('span', { class: 'alert-title', text: 'Correction requested' }),
          el('p', { class: 'alert-message', text: (flag.path || '') + (flag.note ? ' - ' + flag.note : '') }))));
    });

    var page = view.page;
    if (!page) {
      this.root.appendChild(el('div', { class: 'panel' }, el('p', { text: 'This stage has no form.' })));
      return;
    }

    var form = el('form', { class: 'wf-form', novalidate: true,
      onsubmit: function (ev) { ev.preventDefault(); } });
    (page.info || []).forEach(function (block) {
      form.appendChild(el('div', { class: 'alert alert-' + (block.style === 'danger' ? 'error' : block.style || 'info') },
        el('div', { class: 'alert-content' },
          block.title ? el('span', { class: 'alert-title', text: block.title }) : null,
          block.body ? el('p', { class: 'alert-message', text: block.body }) : null)));
    });
    (page.groups || []).forEach(function (group) { form.appendChild(self.group(group)); });

    var bar = el('div', { class: 'panel wf-actions' });
    if (view.can_edit) {
      bar.appendChild(el('button', { type: 'button', class: 'btn btn-secondary', text: 'Save draft',
        onclick: function () { self.save(); } }));
    }
    (view.actions || []).forEach(function (action) {
      var kind = action.outcome === 'advance' || action.outcome === 'approve' ? 'btn-primary' : 'btn-secondary';
      bar.appendChild(el('button', { type: 'button', class: 'btn ' + kind, text: action.label || action.name,
        onclick: function () { self.act(action); } }));
    });
    if (!(view.actions || []).length && !view.can_edit) {
      bar.appendChild(el('span', { class: 'wf-muted', text: 'You can look at this stage but not act on it.' }));
    }
    form.appendChild(bar);
    this.root.appendChild(form);
    this.wireDependencies();
  };

  CasePage.prototype.progress = function (steps) {
    var list = el('ol', { class: 'wf-progress' });
    steps.forEach(function (s, i) {
      list.appendChild(el('li', { class: 'wf-step wf-step-' + (s.status || 'pending') },
        el('span', { class: 'wf-step-marker', text: String(i + 1) }),
        el('span', { class: 'wf-step-name', text: s.title || s.name })));
    });
    return list;
  };

  CasePage.prototype.workButtons = function (view) {
    var self = this;
    var wrap = el('div', { class: 'wf-work' });
    var ops = (view.work && view.work.operations) || [];
    var work = view.work || {};
    var offered = [];
    if (ops.indexOf('claim') >= 0) offered.push(['claim', 'Claim']);
    if (ops.indexOf('release') >= 0) offered.push(['release', 'Release']);
    if (ops.indexOf('suspend') >= 0) offered.push(['suspend', 'Put on hold']);
    if (ops.indexOf('resume') >= 0) offered.push(['resume', 'Resume']);
    offered.forEach(function (pair) {
      wrap.appendChild(el('button', { type: 'button', class: 'btn btn-sm btn-secondary', text: pair[1],
        onclick: function () { self.work(pair[0]); } }));
    });
    return wrap;
  };

  CasePage.prototype.work = function (op) {
    var self = this;
    var body = {};
    if (op === 'suspend') {
      var reason = window.prompt('Reason for the hold (for example MISSING_DOC or QUERY_PENDING):');
      if (!reason) return;
      body = { reason: reason, note: reason };
    }
    api('POST', '/workflow/api/cases/' + encodeURIComponent(this.id) + '/work/' + op, body)
      .then(function (view) { self.render(view); notify(self.root, 'ok', 'Done.'); })
      .catch(function (err) { notify(self.root, 'error', err.message); });
  };

  CasePage.prototype.group = function (group) {
    var self = this;
    var body = el('div', { class: 'wf-group-body' });
    (group.forms || []).forEach(function (form) { body.appendChild(self.formBlock(form, group)); });
    var title = group.title || group.name;
    if (group.layout === 'accordion' || group.collapsed) {
      var details = el('details', { class: 'panel wf-group' }, el('summary', { class: 'panel-title', text: title }), body);
      if (!group.collapsed) details.open = true;
      return details;
    }
    return el('section', { class: 'panel wf-group' },
      el('div', { class: 'panel-header-flex' }, el('h2', { class: 'panel-title', text: title })),
      group.description ? el('p', { class: 'wf-muted', text: group.description }) : null,
      body);
  };

  CasePage.prototype.formBlock = function (form, group) {
    var self = this;
    var block = el('div', { class: 'wf-formblock' });
    if (form.title && (group.forms || []).length > 1) {
      block.appendChild(el('h3', { class: 'wf-form-title', text: form.title }));
    }
    if (!form.repeatable) {
      var grid = el('div', { class: 'wf-grid' });
      (form.inputs || []).forEach(function (input) {
        grid.appendChild(self.field(form, input, null, input.value));
      });
      block.appendChild(grid);
      return block;
    }

    var editable = (form.inputs || []).some(function (i) { return i.editable; });
    var rows = el('div', { class: 'wf-rows', 'data-form': form.name });
    var entries = form.entries && form.entries.length ? form.entries : (editable ? [{}] : []);
    entries.forEach(function (entry) { rows.appendChild(self.row(form, entry, editable)); });
    block.appendChild(rows);
    if (editable) {
      block.appendChild(el('button', { type: 'button', class: 'btn btn-sm btn-secondary', text: 'Add ' + (form.title || form.name),
        onclick: function () {
          if (form.max_items && rows.children.length >= form.max_items) return;
          rows.appendChild(self.row(form, {}, true));
          self.wireDependencies();
        } }));
    }
    return block;
  };

  CasePage.prototype.row = function (form, entry, editable) {
    var self = this;
    var row = el('div', { class: 'wf-row', 'data-form': form.name });
    var grid = el('div', { class: 'wf-grid' });
    (form.inputs || []).forEach(function (input) {
      grid.appendChild(self.field(form, input, row, entry[input.name]));
    });
    row.appendChild(grid);
    if (editable) {
      row.appendChild(el('button', { type: 'button', class: 'btn-icon wf-remove', title: 'Remove', 'aria-label': 'Remove row', text: '×',
        onclick: function () {
          var rows = row.parentNode;
          if (rows.children.length > Math.max(form.min_items || 1, 1)) rows.removeChild(row);
          else Array.prototype.forEach.call(row.querySelectorAll('input,select,textarea'), function (c) {
            if (c.type === 'checkbox') c.checked = false; else c.value = '';
          });
        } }));
    }
    return row;
  };

  CasePage.prototype.field = function (form, input, row, value) {
    var id = 'wf_' + form.name + '_' + input.name + '_' + Math.random().toString(36).slice(2, 7);
    var control = this.control(input, id, value);
    control.setAttribute('data-form', form.name);
    control.setAttribute('data-input', input.name);
    control.setAttribute('data-path', input.path);
    if (input.depends_on && input.depends_on.length) control.setAttribute('data-depends', input.depends_on.join(','));
    var label = el('label', { class: 'form-label', for: id },
      input.label || input.name,
      input.required ? el('span', { class: 'wf-required', 'aria-hidden': 'true', text: ' *' }) : null);
    var wrap = el('div', { class: 'form-field wf-field' + (input.span ? ' wf-span-' + input.span : '') + (input.disabled ? ' wf-disabled' : '') },
      label, control,
      input.help ? el('span', { class: 'wf-help', text: input.help }) : null,
      el('span', { class: 'wf-error', 'data-error-for': input.path, role: 'alert' }));
    if (input.disabled) {
      wrap.appendChild(el('span', { class: 'wf-help', text: 'Not editable right now.' }));
    }
    if (row) control.setAttribute('data-row', '1');
    return wrap;
  };

  CasePage.prototype.control = function (input, id, value) {
    var editable = !!input.editable;
    var common = { id: id, name: input.path };
    var node;
    var empty = value === undefined || value === null;

    if (input.masked) {
      return el('input', Object.assign({ type: 'text', class: 'form-control', value: '••••••', disabled: true }, common));
    }
    switch (input.kind) {
      case 'textarea':
        node = el('textarea', Object.assign({ class: 'form-control', rows: '3', maxlength: input.max_length || false,
          placeholder: input.placeholder || false }, common));
        node.value = empty ? '' : String(value);
        break;
      case 'select':
      case 'radio':
        node = el('select', Object.assign({ class: 'form-control' }, common));
        this.fillOptions(node, input.choices || [], empty ? '' : String(value));
        break;
      case 'multiselect':
        node = el('select', Object.assign({ class: 'form-control', multiple: true }, common));
        this.fillOptions(node, input.choices || [], null);
        (Array.isArray(value) ? value : []).forEach(function (v) {
          Array.prototype.forEach.call(node.options, function (o) { if (o.value === String(v)) o.selected = true; });
        });
        break;
      case 'boolean':
        node = el('input', Object.assign({ type: 'checkbox', class: 'wf-check' }, common));
        node.checked = value === true || value === 'true';
        break;
      case 'number':
      case 'integer':
        node = el('input', Object.assign({ type: 'number', class: 'form-control', step: input.kind === 'integer' ? '1' : 'any',
          min: input.min || false, max: input.max || false }, common));
        node.value = empty ? '' : String(value);
        break;
      case 'date':
        node = el('input', Object.assign({ type: 'date', class: 'form-control' }, common));
        node.value = empty ? '' : String(value).slice(0, 10);
        break;
      case 'datetime':
        node = el('input', Object.assign({ type: 'datetime-local', class: 'form-control' }, common));
        node.value = empty ? '' : String(value).slice(0, 16);
        break;
      case 'file':
        node = el('input', Object.assign({ type: 'text', class: 'form-control', value: empty ? '' : 'Uploaded file', disabled: true }, common));
        return node;
      default:
        node = el('input', Object.assign({ type: input.kind === 'email' ? 'email' : (input.kind === 'phone' ? 'tel' : 'text'),
          class: 'form-control', maxlength: input.max_length || false, placeholder: input.placeholder || false }, common));
        node.value = empty ? '' : String(value);
    }
    if (!editable) {
      node.disabled = true;
      node.setAttribute('data-readonly', '1');
    }
    return node;
  };

  CasePage.prototype.fillOptions = function (select, choices, selected) {
    clear(select);
    select.appendChild(el('option', { value: '', text: '' }));
    choices.forEach(function (o) {
      var opt = el('option', { value: o.value, text: o.label || o.value });
      if (selected !== null && o.value === selected) opt.selected = true;
      select.appendChild(opt);
    });
  };

  // A dependent select refreshes when the inputs it depends on change.
  CasePage.prototype.wireDependencies = function () {
    var self = this;
    Array.prototype.forEach.call(this.root.querySelectorAll('[data-depends]'), function (control) {
      if (control.getAttribute('data-wired')) return;
      control.setAttribute('data-wired', '1');
      var scope = control.closest('.wf-row') || control.closest('.wf-formblock') || self.root;
      var deps = control.getAttribute('data-depends').split(',');
      deps.forEach(function (name) {
        var parent = scope.querySelector('[data-input="' + name + '"]');
        if (!parent) return;
        parent.addEventListener('change', function () { self.refreshChoices(control, scope, deps); });
      });
    });
  };

  CasePage.prototype.refreshChoices = function (control, scope, deps) {
    var self = this;
    var values = {};
    deps.forEach(function (name) {
      var parent = scope.querySelector('[data-input="' + name + '"]');
      values[name] = parent ? (parent.type === 'checkbox' ? parent.checked : parent.value) : '';
    });
    api('POST', '/workflow/api/cases/' + encodeURIComponent(this.id) + '/choices/' +
      encodeURIComponent(control.getAttribute('data-form')) + '/' + encodeURIComponent(control.getAttribute('data-input')),
      { deps: values })
      .then(function (res) {
        var keep = control.value;
        var choices = (res && res.choices) || [];
        var still = choices.some(function (o) { return o.value === keep; });
        self.fillOptions(control, choices, still ? keep : '');
      })
      .catch(function (err) { notify(self.root, 'error', err.message); });
  };

  // What the user typed, shaped as the engine expects: {form: {input: value}},
  // and a list of entries for a repeatable form. Disabled controls are left out.
  CasePage.prototype.collect = function () {
    var data = {};
    function read(control) {
      if (control.type === 'checkbox') return control.checked;
      if (control.multiple) {
        return Array.prototype.filter.call(control.options, function (o) { return o.selected; }).map(function (o) { return o.value; });
      }
      var raw = control.value;
      if (raw === '') return null;
      if (control.type === 'number') { var n = Number(raw); return isNaN(n) ? raw : n; }
      return raw;
    }
    Array.prototype.forEach.call(this.root.querySelectorAll('.wf-rows'), function (rows) {
      var form = rows.getAttribute('data-form');
      var list = [];
      Array.prototype.forEach.call(rows.querySelectorAll('.wf-row'), function (row) {
        var entry = {};
        var any = false;
        Array.prototype.forEach.call(row.querySelectorAll('[data-input]'), function (c) {
          if (c.disabled) return;
          var v = read(c);
          if (v !== null && v !== '' && !(Array.isArray(v) && !v.length)) any = true;
          if (v !== null) entry[c.getAttribute('data-input')] = v;
        });
        if (any) list.push(entry);
      });
      if (rows.querySelector('[data-input]:not([disabled])')) data[form] = list;
    });
    Array.prototype.forEach.call(this.root.querySelectorAll('[data-input]'), function (c) {
      if (c.disabled || c.closest('.wf-row')) return;
      var form = c.getAttribute('data-form');
      data[form] = data[form] || {};
      data[form][c.getAttribute('data-input')] = read(c);
    });
    return data;
  };

  CasePage.prototype.showErrors = function (err) {
    var self = this;
    Array.prototype.forEach.call(this.root.querySelectorAll('.wf-error'), function (n) { n.textContent = ''; });
    var shown = false;
    (err.details || []).forEach(function (d) {
      // repeatable paths look like form.0.input; the first row's message is shown
      var path = (d.path || '').replace(/\.(\d+)\./, '.');
      var target = self.root.querySelector('[data-error-for="' + path + '"]');
      if (target) { target.textContent = d.message; shown = true; }
    });
    notify(this.root, 'error', err.message + (shown ? '' : ((err.details || []).length ? ': ' + err.details.map(function (d) { return d.message; }).join('; ') : '')));
  };

  CasePage.prototype.save = function () {
    var self = this;
    var stage = this.view.stage.name;
    return api('PUT', '/workflow/api/cases/' + encodeURIComponent(this.id) + '/stages/' + encodeURIComponent(stage), { data: this.collect() })
      .then(function (view) { self.render(view); notify(self.root, 'ok', 'Saved.'); return view; })
      .catch(function (err) { self.showErrors(err); throw err; });
  };

  CasePage.prototype.act = function (action) {
    var self = this;
    if (action.confirm && !window.confirm(action.confirm)) return;
    var comment = '';
    if (action.outcome === 'return' || action.outcome === 'reject') {
      comment = window.prompt((action.label || action.name) + ' - add a comment:');
      if (comment === null || comment.trim() === '') return;
    }
    var stage = this.view.stage.name;
    var run = this.view.can_edit ? this.save() : Promise.resolve(this.view);
    run.then(function () {
      return api('POST', '/workflow/api/cases/' + encodeURIComponent(self.id) + '/stages/' + encodeURIComponent(stage) +
        '/actions/' + encodeURIComponent(action.name), comment ? { comment: comment } : {});
    }).then(function (view) {
      self.render(view);
      var next = view.stage && view.stage.name !== stage ? 'Moved to ' + (view.stage.title || view.stage.name) + '.' : 'Done.';
      notify(self.root, 'ok', view.case && view.case.status === 'completed' ? 'The case is complete.' : next);
    }).catch(function (err) {
      if (err && err.details) self.showErrors(err);
    });
  };

  // --- queue page: start a case ------------------------------------------------------

  function StartPanel(root) { this.root = root; }

  StartPanel.prototype.init = function () {
    var self = this;
    var select = this.root.querySelector('[data-role="unit"]');
    var note = this.root.querySelector('.wf-notice');
    api('GET', '/workflow/api/units').then(function (res) {
      var units = (res && res.units) || [];
      clear(select);
      if (!units.length) {
        select.appendChild(el('option', { value: '', text: 'No workspace available' }));
        self.root.querySelector('button').disabled = true;
        return;
      }
      units.forEach(function (u) {
        select.appendChild(el('option', { value: u.id, text: (u.facility_name ? u.facility_name + ' / ' : '') + u.name }));
      });
    }).catch(function (err) { notify(self.root, 'error', err.message); });

    this.root.querySelector('button').addEventListener('click', function () {
      var data = { patient: {
        patient_name: self.root.querySelector('[data-role="name"]').value.trim(),
        mrn: self.root.querySelector('[data-role="mrn"]').value.trim(),
        dos: self.root.querySelector('[data-role="dos"]').value
      } };
      Object.keys(data.patient).forEach(function (k) { if (data.patient[k] === '') delete data.patient[k]; });
      api('POST', '/workflow/api/cases', { org_unit: select.value, data: data }).then(function (view) {
        location.href = '/workflow/cases/' + encodeURIComponent(view.case.id);
      }).catch(function (err) {
        var detail = (err.details || []).map(function (d) { return d.message; }).join('; ');
        notify(self.root, 'error', err.message + (detail ? ': ' + detail : ''));
      });
    });
    if (note) note.hidden = true;
  };

  // --- settings page -------------------------------------------------------------------

  function SettingsPage(root) { this.root = root; }

  SettingsPage.prototype.init = function () {
    var self = this;
    var select = this.root.querySelector('[data-role="unit"]');
    var editor = this.root.querySelector('[data-role="editor"]');
    var effective = this.root.querySelector('[data-role="effective"]');

    function load(message) {
      if (!select.value) return;
      api('GET', '/workflow/api/workspaces/' + encodeURIComponent(select.value) + '/settings').then(function (res) {
        editor.value = JSON.stringify(res.own || { stages: {}, fields: {} }, null, 2);
        effective.textContent = JSON.stringify(res.effective || {}, null, 2);
        notify(self.root, 'ok', message || '');
      }).catch(function (err) {
        editor.value = '';
        effective.textContent = '';
        notify(self.root, 'error', err.status === 403 || err.status === 404
          ? 'You do not administer this workspace.' : err.message);
      });
    }

    api('GET', '/workflow/api/units').then(function (res) {
      var seen = {};
      var options = [];
      ((res && res.units) || []).forEach(function (u) {
        [[u.company_id, 'Company ' + u.company_id], [u.facility_id, 'Facility ' + (u.facility_name || u.facility_id)],
          [u.id, 'Work item ' + u.name]].forEach(function (pair) {
          if (pair[0] && !seen[pair[0]]) { seen[pair[0]] = true; options.push(pair); }
        });
      });
      clear(select);
      options.forEach(function (pair) { select.appendChild(el('option', { value: pair[0], text: pair[1] })); });
      load();
    }).catch(function (err) { notify(self.root, 'error', err.message); });

    select.addEventListener('change', load);
    this.root.querySelector('[data-role="save"]').addEventListener('click', function () {
      var parsed;
      try { parsed = JSON.parse(editor.value || '{}'); } catch (e) { notify(self.root, 'error', 'The settings are not valid JSON: ' + e.message); return; }
      api('PUT', '/workflow/api/workspaces/' + encodeURIComponent(select.value) + '/settings', { overrides: parsed })
        .then(function () { load('Settings saved. New cases in this workspace use them.'); })
        .catch(function (err) { notify(self.root, 'error', err.message); });
    });
  };

  // --- boot -------------------------------------------------------------------------------

  document.addEventListener('DOMContentLoaded', function () {
    var caseRoot = document.getElementById('wf-case');
    if (caseRoot) new CasePage(caseRoot).load();
    var start = document.getElementById('wf-start');
    if (start) new StartPanel(start).init();
    var settings = document.getElementById('wf-settings');
    if (settings) new SettingsPage(settings).init();
  });
}());
