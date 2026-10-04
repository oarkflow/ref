UI.page(function () {
  var $ = UI.$, el = UI.el;
  function render() {
    return UI.call('GET', '/ui/notifications').then(function (r) {
      var box = $('#notes'); box.textContent = ''; var list = (r.body && r.body.notifications) || [];
      if (!list.length) { box.appendChild(UI.empty('You are all caught up', 'Campaign results and warnings appear here.', 'bell')); return; }
      list.forEach(function (n, i) {
        var warn = n.level === 'warn', row = el('div', { class: 'nitem' + (n.read_ms ? '' : ' unread'), style: { animation: 'rise .3s var(--ease) both', animationDelay: Math.min(i, 12) * 30 + 'ms', padding: '14px 20px' } }, [
          el('span', { class: 'pill ' + (warn ? 'warn' : 'info') }, [UI.icon(warn ? 'alert' : 'info', 16)]),
          el('div', { style: { flex: 1 } }, [el('div', { style: { fontWeight: 600 } }, [n.title, warn ? el('span', { class: 'pill warn', text: 'attention', style: { marginLeft: '8px' } }) : null]), el('div', { class: 'muted', text: n.body }), el('small', { class: 'faint', title: UI.when(n.created_ms), text: UI.ago(n.created_ms) })]),
          n.read_ms ? null : el('button', { type: 'button', class: 'small ghost', onclick: function () { UI.call('POST', '/ui/notifications/read', { ids: [n.id] }).then(function () { render(); window.refreshBell && window.refreshBell(); }); } }, ['Mark as read'])]);
        box.appendChild(row);
      });
    });
  }
  $('#read-all').addEventListener('click', function (e) { UI.busy(e.currentTarget, UI.call('POST', '/ui/notifications/read', { everything: true }).then(function () { UI.ok('All marked as read.'); render(); window.refreshBell && window.refreshBell(); })); });
  $('#notes').appendChild(UI.skeleton(4)); render();
});
