(function () {
  var C = window.C, host = C.$('#login');
  var accounts = [
    ['admin@example.com', 'admin-pass-123', 'Admin'], ['operator@example.com', 'operator-pass-123', 'Operator'],
    ['feeder@example.com', 'feeder-pass-123', 'Feeder'], ['replayer@example.com', 'replayer-pass-123', 'Replayer'], ['analyst@example.com', 'analyst-pass-123', 'Analyst']
  ];
  host.className = 'login';
  host.innerHTML = '<div class="box"><div><h1>Pipeline control room</h1><p class="muted">Sign in to see what moved, where it went and what needs attention.</p></div>' +
    '<form id="f" class="box" novalidate><label>Email<input name="email" type="email" autocomplete="username" required></label>' +
    '<label>Password<input name="password" type="password" autocomplete="current-password" required></label>' +
    '<button class="btn primary" type="submit">Sign in</button><div id="err" class="err-text" role="alert"></div></form>' +
    '<div class="row" style="justify-content:space-between"><a href="/forgot">Forgot your password?</a><a href="/register">Create an account</a></div>' +
    '<div><p class="note">Development accounts, one click. Each holds different roles, so you can see what each is allowed to do.</p><div class="quick" id="quick"></div></div></div>';
  var f = C.$('#f');
  function go(email, password) {
    C.call('POST', '/login', { email: email, password: password }).then(function (r) {
      if (r.ok) { location.href = '/'; return; }
      C.$('#err').textContent = r.status === 401 ? 'That email and password do not match.' : C.problem(r);
    });
  }
  f.addEventListener('submit', function (e) { e.preventDefault(); go(f.elements.email.value.trim(), f.elements.password.value); });
  accounts.forEach(function (a) {
    var b = document.createElement('button'); b.type = 'button'; b.className = 'btn small'; b.textContent = a[2];
    b.addEventListener('click', function () { go(a[0], a[1]); }); C.$('#quick').appendChild(b);
  });
  f.elements.email.focus();
})();
