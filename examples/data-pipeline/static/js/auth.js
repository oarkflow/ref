// Registration, forgotten password and password reset pages. They share one
// small form builder; every request goes through the secure transport (core.js).
(function () {
  var C = window.C, host = C.$('#auth'), page = location.pathname.replace(/\/$/, '');
  host.className = 'login';

  var PAGES = {
    '/register': {
      title: 'Create an account',
      lead: 'An administrator reviews every new account before it can sign in.',
      fields: [['name', 'Your name', 'text', 'name'], ['email', 'Email', 'email', 'username'], ['password', 'Password', 'password', 'new-password'], ['confirm', 'Confirm password', 'password', 'new-password']],
      hint: 'At least 12 characters, with a letter and a digit.',
      button: 'Request an account',
      send: function (v) { return C.call('POST', '/register', { name: v.name, email: v.email, password: v.password, confirm: v.confirm }); },
      check: function (v) { return !v.name.trim() ? 'Enter your name.' : !/^[^@ ]+@[^@ ]+[.][^@ ]+$/.test(v.email) ? 'Enter a valid email address.' : C.passwordProblem(v.password, v.confirm); },
      done: 'Thank you. An administrator will review your request; you can sign in once it is approved.'
    },
    '/forgot': {
      title: 'Forgot your password?',
      lead: 'Enter your email and we will send a link to choose a new one.',
      fields: [['email', 'Email', 'email', 'username']],
      button: 'Send the link',
      send: function (v) { return C.call('POST', '/forgot', { email: v.email }); },
      check: function (v) { return /^[^@ ]+@[^@ ]+[.][^@ ]+$/.test(v.email) ? '' : 'Enter a valid email address.'; },
      done: 'If that email belongs to an active account, a reset link has been sent. It works once and expires in 30 minutes.'
    },
    '/reset': {
      title: 'Choose a new password',
      lead: 'This link works once.',
      fields: [['password', 'New password', 'password', 'new-password'], ['confirm', 'Confirm new password', 'password', 'new-password']],
      hint: 'At least 12 characters, with a letter and a digit.',
      button: 'Change password',
      send: function (v) { return C.call('POST', '/reset', { token: new URLSearchParams(location.search).get('token') || '', password: v.password, confirm: v.confirm }); },
      check: function (v) { return new URLSearchParams(location.search).get('token') ? C.passwordProblem(v.password, v.confirm) : 'This page needs the link from your email.'; },
      done: 'Your password was changed. You can sign in now.'
    }
  };
  var cfg = PAGES[page];
  if (!cfg) { host.textContent = 'Not found.'; return; }

  var html = '<div class="box"><div><h1>' + C.esc(cfg.title) + '</h1><p class="muted">' + C.esc(cfg.lead) + '</p></div><form id="f" class="box" novalidate>';
  cfg.fields.forEach(function (f) { html += '<label>' + C.esc(f[1]) + '<input name="' + f[0] + '" type="' + f[2] + '" autocomplete="' + f[3] + '" required></label>'; });
  if (cfg.fields.some(function (f) { return f[2] === 'password'; })) html += '<label class="check"><input type="checkbox" id="show"> Show passwords</label>';
  if (cfg.hint) html += '<p class="note" id="hint">' + C.esc(cfg.hint) + '</p>';
  html += '<button class="btn primary" type="submit">' + C.esc(cfg.button) + '</button><div id="msg" role="alert"></div></form><p><a href="/login">Back to sign in</a></p></div>';
  host.innerHTML = html;

  var f = C.$('#f'), msg = C.$('#msg');
  function say(text, ok) { msg.className = ok ? 'ok-text' : 'err-text'; msg.textContent = text; }
  var show = C.$('#show');
  if (show) show.addEventListener('change', function () { [].forEach.call(f.querySelectorAll('input[type=password],input[data-pw]'), function (i) { i.type = show.checked ? 'text' : 'password'; i.dataset.pw = '1'; }); });
  // As the second password is typed, say at once whether it matches.
  var confirm = f.elements.confirm;
  if (confirm) f.elements.password.addEventListener('input', live), confirm.addEventListener('input', live);
  function live() { if (!confirm.value) return say(''); say(f.elements.password.value === confirm.value ? 'The passwords match.' : 'The passwords do not match.', f.elements.password.value === confirm.value); }

  f.addEventListener('submit', function (e) {
    e.preventDefault();
    var v = {}; cfg.fields.forEach(function (x) { v[x[0]] = f.elements[x[0]].value; });
    var problem = cfg.check(v);
    if (problem) return say(problem);
    var btn = f.querySelector('button'); btn.disabled = true;
    cfg.send(v).then(function (r) {
      btn.disabled = false;
      if (r.ok) { f.reset(); say(''); f.hidden = true; host.querySelector('.box').insertAdjacentHTML('beforeend', '<p class="ok-text" role="status">' + C.esc(cfg.done) + '</p>'); return; }
      say(r.status === 429 ? 'Too many attempts. Wait a minute and try again.' : C.problem(r));
    });
  });
  f.elements[cfg.fields[0][0]].focus();
})();
