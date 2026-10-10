(function () {
  var C = window.C, host = C.$('#login'), q = new URLSearchParams(location.search);
  var accounts = [
    ['admin@example.com', 'admin-pass-123', 'Admin'], ['operator@example.com', 'operator-pass-123', 'Operator'],
    ['feeder@example.com', 'feeder-pass-123', 'Feeder'], ['replayer@example.com', 'replayer-pass-123', 'Replayer'], ['analyst@example.com', 'analyst-pass-123', 'Analyst']
  ];
  host.className = 'login';
  var note = q.get('changed') ? '<p class="ok-text" role="status">Your password was changed and your other sessions were ended. Sign in with the new one.</p>' : '';

  // The second step: the person signed in with a password, but has a second factor.
  if (q.get('mfa')) {
    host.innerHTML = '<div class="box"><div><h1>Two-step sign-in</h1><p class="muted">Enter the 6-digit code from your authenticator app, or one of your recovery codes.</p></div>' +
      '<form id="m" class="box" novalidate><label>Code<input name="code" inputmode="numeric" autocomplete="one-time-code" required></label>' +
      '<button class="btn primary" type="submit">Verify</button><div id="err" class="err-text" role="alert"></div></form>' +
      '<p class="note">Lost your device? An administrator can turn two-step sign-in off for you.</p><p><a href="/login" id="other">Use a different account</a></p></div>';
    var m = C.$('#m');
    m.addEventListener('submit', function (e) {
      e.preventDefault();
      var btn = m.querySelector('button'); btn.disabled = true;
      C.call('POST', '/login/mfa', { code: m.elements.code.value.trim() }).then(function (r) {
        btn.disabled = false;
        if (r.ok) { location.href = '/'; return; }
        C.$('#err').textContent = r.status === 429 ? 'Too many attempts. Wait a minute and try again.' : (r.body.error && r.body.error.code === 'INVALID_CODE' ? 'That code is not right.' : C.problem(r));
        m.elements.code.select();
      });
    });
    C.$('#other').addEventListener('click', function (e) { e.preventDefault(); C.call('POST', '/logout').then(function () { location.href = '/login'; }); });
    m.elements.code.focus();
    return;
  }

  host.innerHTML = '<div class="box"><div><h1>Pipeline control room</h1><p class="muted">Sign in to see what moved, where it went and what needs attention.</p></div>' + note +
    '<form id="f" class="box" novalidate><label>Email<input name="email" type="email" autocomplete="username" required></label>' +
    '<label>Password<input name="password" type="password" autocomplete="current-password" required></label>' +
    '<button class="btn primary" type="submit">Sign in</button><div id="err" class="err-text" role="alert"></div></form>' +
    '<div class="row" style="justify-content:space-between"><a href="/forgot">Forgot your password?</a><a href="/register">Create an account</a></div>' +
    '<div><p class="note">Development accounts, one click. Each holds different roles, so you can see what each is allowed to do.</p><div class="quick" id="quick"></div></div></div>';
  var f = C.$('#f');
  // Already signed in (for example in another tab): nothing to do here.
  C.whenReady.then(function () { if (C.authenticated) location.href = '/'; });
  function go(email, password) {
    C.call('POST', '/login', { email: email, password: password }).then(function (r) {
      if (r.ok) {
        // With a second factor, reload as this person (the secure session is bound
        // to who is signed in) and ask for the code.
        var claims = (r.body.signed_in || r.body.principal || {}).claims;
        location.href = claims && Number(claims.mfa_enabled) === 1 ? '/login?mfa=1' : '/';
        return;
      }
      C.$('#err').textContent = r.status === 429 ? 'Too many attempts. Wait a minute and try again.' :
        r.status === 401 ? 'That email and password do not match, or the account is waiting for approval or is temporarily locked.' : C.problem(r);
    });
  }
  f.addEventListener('submit', function (e) { e.preventDefault(); go(f.elements.email.value.trim(), f.elements.password.value); });
  accounts.forEach(function (a) {
    var b = document.createElement('button'); b.type = 'button'; b.className = 'btn small'; b.textContent = a[2];
    b.addEventListener('click', function () { go(a[0], a[1]); }); C.$('#quick').appendChild(b);
  });
  f.elements.email.focus();
})();
