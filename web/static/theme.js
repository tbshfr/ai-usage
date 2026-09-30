// Runs in <head> before styles so the resolved theme is set before first paint.
// The login page cannot read saved settings, so it follows the system theme.
(() => {
  const settings = window.dashboardSettings?.settings ??
    { appearance: { theme: 'system', color: document.documentElement.dataset.color } };
  const system = window.matchMedia('(prefers-color-scheme: dark)');
  function apply() {
    const { theme: preference, color } = settings.appearance;
    const theme = preference === 'system' ? (system.matches ? 'dark' : 'light') : preference;
    const root = document.documentElement;
    if (root.dataset.theme === theme && root.dataset.color === color) return;
    root.dataset.theme = theme;
    root.dataset.color = color;
    const meta = document.querySelector('meta[name="theme-color"]');
    if (meta) meta.content = theme === 'dark' ? '#09090b' : '#ffffff';
    document.dispatchEvent(new CustomEvent('appearancechange'));
  }
  function syncControls() {
    document.querySelectorAll('[data-theme-value]').forEach(button => {
      button.setAttribute('aria-pressed', String(button.dataset.themeValue === settings.appearance.theme));
    });
    document.querySelectorAll('[data-color-value]').forEach(button => {
      button.setAttribute('aria-pressed', String(button.dataset.colorValue === settings.appearance.color));
    });
  }
  apply();
  document.addEventListener('click', event => {
    const button = event.target.closest('[data-theme-value], [data-color-value]');
    if (!button) return;
    window.dashboardSettings.save('appearance', {
      theme: button.dataset.themeValue || settings.appearance.theme,
      color: button.dataset.colorValue || settings.appearance.color,
    });
    apply();
    syncControls();
  });
  document.addEventListener('settingschange', event => {
    if (event.detail.section === 'appearance') { apply(); syncControls(); }
  });
  system.addEventListener('change', apply);
})();
