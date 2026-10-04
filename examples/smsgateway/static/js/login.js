UI.page(function () {
  var $ = UI.$, f = $('#loginform');
  function signIn(email, password, btn) {
    var out = $('#result'); out.hidden = true;
    UI.busy(btn, UI.call('POST', '/login', { email: email, password: password }).then(function (r) {
      if (r.ok) { location.href = '/'; return; }
      out.hidden = false; out.className = 'out err'; out.textContent = r.status === 401 ? 'That email and password do not match.' : UI.problem(r);
      f.elements.password.focus();
    }));
  }
  f.addEventListener('submit', function (e) { e.preventDefault(); signIn(f.elements.email.value.trim(), f.elements.password.value, f.querySelector('button[type=submit]')); });
  UI.$$('.quick button').forEach(function (b) { b.addEventListener('click', function () { f.elements.email.value = b.dataset.email; f.elements.password.value = b.dataset.pass; signIn(b.dataset.email, b.dataset.pass, b); }); });
  f.elements.email.focus();
});
