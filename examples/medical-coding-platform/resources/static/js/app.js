/**
 * CLEAR Medical Coding Platform — Core Application JavaScript
 * Handles global UI state, navigation, alert dismissal, and SLA countdown timers.
 */
document.addEventListener('DOMContentLoaded', () => {
  // Mobile sidebar toggle
  const sidebarToggle = document.getElementById('sidebar-toggle');
  const sidebar = document.querySelector('.sidebar');
  if (sidebarToggle && sidebar) {
    sidebarToggle.addEventListener('click', () => {
      sidebar.classList.toggle('open');
    });
  }

  // Auto-dismissible alerts
  document.querySelectorAll('.alert-dismiss').forEach(btn => {
    btn.addEventListener('click', (e) => {
      const alert = e.target.closest('.alert');
      if (alert) {
        alert.style.opacity = '0';
        alert.style.transform = 'translateY(-8px)';
        setTimeout(() => alert.remove(), 250);
      }
    });
  });

  // Global SLA deadline countdown calculation
  updateSLATimers();
  setInterval(updateSLATimers, 30000); // refresh every 30 seconds
});

function updateSLATimers() {
  const now = new Date().getTime();
  document.querySelectorAll('[data-sla-deadline]').forEach(el => {
    const deadlineStr = el.getAttribute('data-sla-deadline');
    if (!deadlineStr || isNaN(new Date(deadlineStr).getTime())) return;

    const deadline = new Date(deadlineStr).getTime();
    const diff = deadline - now;

    // Write into the inner .sla-text span when present so badge icons survive.
    const target = el.querySelector('.sla-text') || el;

    const hours = Math.floor(Math.abs(diff) / (1000 * 60 * 60));
    const minutes = Math.floor((Math.abs(diff) % (1000 * 60 * 60)) / (1000 * 60));

    if (diff < 0) {
      el.classList.remove('sla-ok', 'sla-warn');
      el.classList.add('sla-breach');
      target.textContent = `Breached by ${hours}h ${minutes}m`;
    } else if (diff < 4 * 60 * 60 * 1000) { // less than 4 hours
      el.classList.remove('sla-ok', 'sla-breach');
      el.classList.add('sla-warn');
      target.textContent = `${hours}h ${minutes}m left`;
    } else {
      el.classList.remove('sla-warn', 'sla-breach');
      el.classList.add('sla-ok');
      target.textContent = `${hours}h ${minutes}m left`;
    }
  });
}
