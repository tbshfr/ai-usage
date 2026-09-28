(() => {
  const key = 'ai-usage.appearance';
  const themes = ['system', 'light', 'dark'];
  const colors = ['green', 'blue', 'violet', 'rose', 'orange'];
  const system = window.matchMedia('(prefers-color-scheme: dark)');
  let preference = { theme: 'system', color: 'green' };
  let storageAvailable = true;
  function read() {
    let saved;
    try {
      const raw = localStorage.getItem(key);
      storageAvailable = true;
      try { saved = JSON.parse(raw || '{}'); } catch { /* invalid data: use defaults */ }
    } catch { storageAvailable = false; }
    preference = {
      theme: themes.includes(saved?.theme) ? saved.theme : 'system',
      color: colors.includes(saved?.color) ? saved.color : 'green',
    };
  }
  function syncControls() {
    document.querySelectorAll('[data-theme-value]').forEach(button => {
      button.setAttribute('aria-pressed', String(button.dataset.themeValue === preference.theme));
    });
    document.querySelectorAll('[data-color-value]').forEach(button => {
      button.setAttribute('aria-pressed', String(button.dataset.colorValue === preference.color));
    });
    document.querySelectorAll('[data-appearance-status]').forEach(el => {
      el.textContent = storageAvailable ? 'Saved automatically in this browser.' : 'Applied for this visit. Browser storage is unavailable.';
    });
  }
  function apply() {
    const theme = preference.theme === 'system' ? (system.matches ? 'dark' : 'light') : preference.theme;
    document.documentElement.dataset.theme = theme;
    document.documentElement.dataset.color = preference.color;
    const meta = document.querySelector('meta[name="theme-color"]');
    if (meta) meta.content = theme === 'dark' ? '#09090b' : '#ffffff';
    syncControls();
    document.dispatchEvent(new CustomEvent('appearancechange'));
  }
  read();
  apply();
  document.addEventListener('DOMContentLoaded', syncControls);
  document.addEventListener('click', event => {
    const button = event.target.closest('[data-theme-value], [data-color-value]');
    if (!button) return;
    if (themes.includes(button.dataset.themeValue)) preference.theme = button.dataset.themeValue;
    if (colors.includes(button.dataset.colorValue)) preference.color = button.dataset.colorValue;
    try { localStorage.setItem(key, JSON.stringify(preference)); storageAvailable = true; }
    catch { storageAvailable = false; }
    apply();
  });
  system.addEventListener('change', () => { if (preference.theme === 'system') apply(); });
  window.addEventListener('storage', event => {
    if (event.key === key || event.key === null) { read(); apply(); }
  });
})();
