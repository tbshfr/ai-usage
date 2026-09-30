(() => {
  const settings = JSON.parse(document.getElementById('dashboard-settings').textContent);
  const confirmed = structuredClone(settings);
  const queues = {};
  const revisions = {};
  let toastTimer;
  const SAVE_TIMEOUT_MS = 10000;
  const channel = 'BroadcastChannel' in window ? new BroadcastChannel('dashboard-settings') : null;

  function toast(message, { tone = '', hideAfter = 0 } = {}) {
    const el = document.querySelector('[data-settings-toast]');
    if (!el) return;
    clearTimeout(toastTimer);
    el.textContent = message;
    el.dataset.tone = tone;
    el.toggleAttribute('data-visible', true);
    if (hideAfter) toastTimer = setTimeout(hideToast, hideAfter);
  }

  function hideToast() {
    clearTimeout(toastTimer);
    document.querySelector('[data-settings-toast]')?.removeAttribute('data-visible');
  }

  document.addEventListener('click', event => {
    if (event.target.closest('[data-settings-toast]')) hideToast();
  });

  function receive(section, value) {
    if (!(section in settings)) return;
    confirmed[section] = structuredClone(value);
    if (JSON.stringify(settings[section]) === JSON.stringify(value)) return;
    settings[section] = structuredClone(value);
    document.dispatchEvent(new CustomEvent('settingschange', { detail: { section } }));
  }

  channel?.addEventListener('message', ({ data }) => receive(data.section, data.value));

  window.addEventListener('pageshow', async event => {
    if (!event.persisted) return;
    try {
      const response = await fetch('/settings/preferences', { redirect: 'manual' });
      if (response.status !== 200) return;
      const latest = await response.json();
      Object.keys(settings).forEach(section => receive(section, latest[section]));
    } catch { /* Keep the restored settings; the next page load renders current ones. */ }
  });

  function failure(reason) {
    if (reason?.name === 'TimeoutError') return 'The server did not respond. Try again.';
    if (reason instanceof TypeError) return 'The server could not be reached. Try again.';
    // An expired session redirects to the login page.
    if (reason?.type === 'opaqueredirect') return 'Your session expired. Sign in again to save settings.';
    if (reason?.status === 400) return 'This value was rejected. Check it and try again.';
    return 'Settings could not be saved. Try again.';
  }

  window.dashboardSettings = {
    settings,
    save(section, value) {
      const body = JSON.stringify(value);
      if (body === JSON.stringify(settings[section])) return;
      settings[section] = JSON.parse(body);
      const revision = revisions[section] = (revisions[section] || 0) + 1;
      // Fast saves skip the in-progress message so the toast does not flicker
      clearTimeout(toastTimer);
      toastTimer = setTimeout(() => toast('Saving…'), 500);
      // Writes to one section run in order so the last change wins on the server
      const request = (queues[section] || Promise.resolve()).catch(() => {}).then(async () => {
        const response = await fetch(`/settings/preferences/${section}`, {
          method: 'PUT', headers: { 'Content-Type': 'application/json' }, body, keepalive: true, redirect: 'manual',
          // An unresponsive server otherwise leaves the save pending indefinitely.
          signal: AbortSignal.timeout(SAVE_TIMEOUT_MS),
        });
        if (response.status !== 204) throw response;
      });
      queues[section] = request;
      request.then(() => {
        confirmed[section] = JSON.parse(body);
        channel?.postMessage({ section, value: JSON.parse(body) });
        if (revisions[section] !== revision) return;
        toast('Saved', { tone: 'success', hideAfter: 2000 });
      }, reason => {
        if (revisions[section] !== revision) return;
        settings[section] = structuredClone(confirmed[section]);
        toast(failure(reason), { tone: 'error' });
      });
    },
  };
})();
