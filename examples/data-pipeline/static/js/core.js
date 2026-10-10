// Shared helpers: DOM, requests, toasts, theme.
(function () {
  var C = (window.C = {});
  C.$ = function (s, r) { return (r || document).querySelector(s); };
  C.esc = function (v) { return String(v == null ? '' : v).replace(/[&<>"']/g, function (c) { return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]; }); };
  C.num = function (v) { return (Number(v) || 0).toLocaleString(); };
  C.time = function (iso) { var d = new Date(iso); return isNaN(d) ? '' : d.toLocaleTimeString([], { hour12: false }); };
  C.when = function (iso) { var d = new Date(iso); return isNaN(d) ? '' : d.toLocaleString([], { hour12: false, month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit', second: '2-digit' }); };
  // Every request goes through the secure transport (static/js/secure.js). Until it
  // is ready calls wait; if it cannot start they fail, and nothing is sent in plain.
  var ready = new Promise(function (resolve, reject) {
    C.secureReady = function (secure, cfg) { C.secure = secure; C.authenticated = !!cfg.authenticated; resolve(); };
    C.secureFailed = function (err) {
      var msg = err && err.message ? err.message : String(err);
      C.fatal('The secure connection could not be set up: ' + msg);
      reject(new Error(msg));
    };
  });
  ready.catch(function () {});
  C.whenReady = ready;
  // Makes the next page load register this browser's device again (see secure.js).
  C.forgetDevice = function () { try { localStorage.removeItem('pipeline.device.principal'); } catch (e) { /* ignore */ } };
  C.fatal = function (msg) {
    var b = C.$('#fatal');
    if (!b) { b = document.createElement('div'); b.id = 'fatal'; b.setAttribute('role', 'alert'); b.className = 'fatal'; document.body.prepend(b); }
    b.textContent = msg + ' Nothing was sent. Reload the page; if this continues, ask an administrator.';
  };
  C.call = function (method, path, body) {
    return ready.then(function () {
      return fetch(path, { method: method, credentials: 'same-origin', cache: 'no-store', headers: body ? { 'Content-Type': 'application/json' } : {}, body: body ? JSON.stringify(body) : undefined });
    }).then(function (r) {
      return r.json().catch(function () { return {}; }).then(function (j) {
        // A 401 means the session is gone (signed out, disabled): go and sign in again.
        // A wrong password is also a 401, but it is an answer to show, not a reason to leave.
        var code = j && ((j.error && j.error.code) || j.code), wrongPassword = code === 'INVALID_CREDENTIALS';
        // The secure session was set up for a different sign-in than the one the browser
        // holds now (signed in or out in another tab, or the server restarted). Reload once:
        // the page then registers its device again for who it really is.
        if (r.status === 401 && (code === 'SESSION_BINDING_FAILED' || code === 'DEVICE_REGISTRATION_FORBIDDEN')) {
          var tried = false;
          try { tried = sessionStorage.getItem('pipeline.rebind') === '1'; sessionStorage.setItem('pipeline.rebind', '1'); } catch (e) { /* ignore */ }
          if (!tried) { C.forgetDevice(); location.reload(); return new Promise(function () {}); }
          return { status: 0, ok: false, body: { error: { code: 'TRANSPORT', message: 'The secure session does not match your sign-in. Close other tabs of this site and reload.' } } };
        }
        try { sessionStorage.removeItem('pipeline.rebind'); } catch (e) { /* ignore */ }
        // Signed in with a password but the second factor is still owed: go and give it.
        if (r.status === 401 && code === 'MFA_REQUIRED' && location.search.indexOf('mfa=1') < 0) { location.href = '/login?mfa=1'; return new Promise(function () {}); }
        if (r.status === 401 && !wrongPassword && code !== 'MFA_REQUIRED' && path !== '/login') { location.href = '/login'; return new Promise(function () {}); }
        return { status: r.status, ok: r.status < 400, body: j };
      });
    }, function (err) {
      // The server refused the secure envelope (it was restarted, or the sign-in changed
      // elsewhere): reload once so the page registers its device again.
      if (err && /unprotected response|session|device/i.test(String(err.message))) {
        var again = false;
        try { again = sessionStorage.getItem('pipeline.rebind') === '1'; sessionStorage.setItem('pipeline.rebind', '1'); } catch (e) { /* ignore */ }
        if (!again) { C.forgetDevice(); location.reload(); return new Promise(function () {}); }
      }
      return { status: 0, ok: false, body: { error: { code: 'TRANSPORT', message: err && err.message ? err.message : 'the secure transport failed' } } };
    });
  };
  // The same rules the server enforces, so a mistake is caught before anything is sent.
  C.passwordProblem = function (password, confirm) {
    if (!password || password.length < 12) return 'Use at least 12 characters.';
    if (!/[A-Za-z]/.test(password) || !/[0-9]/.test(password)) return 'Use at least one letter and one digit.';
    if (confirm !== undefined && password !== confirm) return 'The passwords do not match.';
    return '';
  };
  C.problem = function (r) { var e = (r.body && r.body.error) || {}; return e.message || ('Request failed (' + r.status + ')'); };
  C.toast = function (msg, bad) {
    var host = C.$('#toasts');
    if (!host) { host = document.createElement('div'); host.id = 'toasts'; host.className = 'toasts'; host.setAttribute('aria-live', 'polite'); document.body.appendChild(host); }
    var t = document.createElement('div'); t.className = 'toast' + (bad ? ' err' : ''); t.textContent = msg; host.appendChild(t);
    setTimeout(function () { t.remove(); }, 4500);
  };
  C.theme = function () {
    try { var t = localStorage.getItem('theme'); if (t) document.documentElement.setAttribute('data-theme', t); } catch (e) {}
  };
  C.toggleTheme = function () {
    var cur = document.documentElement.getAttribute('data-theme') || (matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light');
    var next = cur === 'dark' ? 'light' : 'dark';
    document.documentElement.setAttribute('data-theme', next);
    try { localStorage.setItem('theme', next); } catch (e) {}
  };
  C.theme();
})();
