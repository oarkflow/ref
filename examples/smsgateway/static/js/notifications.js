(function () {
  var $ = function (s) { return document.querySelector(s); };
  function call(method, path, body) {
    return fetch(path, { method: method, credentials: 'same-origin', headers: { 'Content-Type': 'application/json' }, body: body ? JSON.stringify(body) : undefined })
      .then(function (r) { if (r.status === 401) { location.href = '/login'; return new Promise(function () {}); } return r.json().catch(function () { return {}; }).then(function (j) { return { status: r.status, body: j }; }); });
  }
  function el(tag, cls, text) { var e = document.createElement(tag); if (cls) e.className = cls; if (text != null) e.textContent = text; return e; }
  function render() {
    call('GET', '/ui/notifications').then(function (r) {
      var box = $('#notes'); box.textContent = '';
      var list = (r.body && r.body.notifications) || [];
      if (!list.length) { box.appendChild(el('p', 'muted', 'Nothing yet.')); return; }
      list.forEach(function (n) {
        var row = el('div', 'note' + (n.read_ms ? '' : ' unread'));
        var head = el('div', '', n.title);
        if (n.level === 'warn') { var b = el('span', 'badge avoid', 'attention'); b.style.marginLeft = '8px'; head.appendChild(b); }
        row.appendChild(head); row.appendChild(el('div', 'muted', n.body));
        row.appendChild(el('small', 'muted', new Date(n.created_ms).toLocaleString()));
        if (!n.read_ms) { var a = el('a', '', 'mark as read'); a.href = '#'; a.addEventListener('click', function (e) { e.preventDefault(); call('POST', '/ui/notifications/read', { ids: [n.id] }).then(function () { render(); if (window.refreshBell) window.refreshBell(); }); }); row.appendChild(a); }
        box.appendChild(row);
      });
    });
  }
  $('#read-all').addEventListener('click', function () { call('POST', '/ui/notifications/read', { everything: true }).then(function () { render(); if (window.refreshBell) window.refreshBell(); }); });
  render();
})();
