// Client-side interactions for modern auth boilerplate
document.addEventListener('DOMContentLoaded', () => {
  const pwdInput = document.getElementById('password');
  const fill = document.getElementById('strength-fill');
  const text = document.getElementById('strength-text');

  if (pwdInput && fill && text) {
    pwdInput.addEventListener('input', (e) => {
      const val = e.target.value;
      if (!val) {
        fill.style.width = '0%';
        text.innerText = 'Min 8 chars, letters and numbers';
        text.style.color = 'var(--text-dim)';
        return;
      }
      let score = 0;
      if (val.length >= 8) score += 30;
      if (val.length >= 12) score += 20;
      if (/[a-z]/.test(val) && /[A-Z]/.test(val)) score += 25;
      if (/[0-9]/.test(val)) score += 15;
      if (/[^a-zA-Z0-9]/.test(val)) score += 10;

      fill.style.width = score + '%';
      if (score < 40) {
        fill.style.background = '#F43F5E';
        text.innerText = 'Weak password';
        text.style.color = '#FDA4AF';
      } else if (score < 75) {
        fill.style.background = '#F59E0B';
        text.innerText = 'Moderate password';
        text.style.color = '#FCD34D';
      } else {
        fill.style.background = '#10B981';
        text.innerText = 'Strong Argon2id password';
        text.style.color = '#6EE7B7';
      }
    });
  }
});
