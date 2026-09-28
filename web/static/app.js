const PALETTE = ['#c96f4a', '#dfa14f', '#7d9b76', '#b06a6a', '#8b7d9b', '#a89f91'];

// Live chart instances; stale ones (htmx-swapped away) are pruned on resize.
const charts = [];

// Chart data arrives as <script type="application/json" data-chart="id">
// blocks rendered into the page or swapped in by htmx. CSP forbids inline
// scripts, so instead of calling renderChart() directly, the templates
// embed the JSON and this scans for blocks and renders them once each.
function renderDataCharts() {
  for (const block of document.querySelectorAll('script[data-chart]')) {
    if (block.dataset.rendered) continue;
    block.dataset.rendered = 'true';
    let d;
    try {
      d = JSON.parse(block.textContent);
    } catch (e) {
      console.error('chart data parse failed:', block.dataset.chart, e);
      continue;
    }
    try {
      renderChart(block.dataset.chart, d);
    } catch (e) {
      console.error('chart render failed:', block.dataset.chart, e);
    }
  }
}

document.addEventListener('DOMContentLoaded', renderDataCharts);
document.addEventListener('htmx:after:settle', renderDataCharts);

// Keep token choices inside the selected group. Capture runs before htmx's
// form change handler, so a stale token is cleared before requesting data.
function syncTokenFilter(form) {
  const tokens = form.querySelector('select[name="token"]');
  if (!tokens) return;
  const group = form.querySelector('select[name="group"]')?.value || '';
  const ungrouped = form.querySelector('input[name="ungrouped"]')?.checked || false;
  for (const option of tokens.options) {
    const eligible = option.value === '' || (
      option.hasAttribute('data-filter-unauthenticated') ? !group && !ungrouped :
      (!group || option.dataset.filterGroup === group) && (!ungrouped || option.dataset.filterGroup === '')
    );
    option.hidden = !eligible;
    option.disabled = !eligible;
  }
  if (tokens.selectedOptions[0]?.disabled) tokens.value = '';
}

document.addEventListener('DOMContentLoaded', () => {
  const form = document.getElementById('filter-bar');
  if (form) syncTokenFilter(form);
});
document.addEventListener('change', e => {
  const form = e.target.closest('#filter-bar');
  if (!form || !e.target.matches('[name="group"], [name="ungrouped"]')) return;
  const group = form.querySelector('select[name="group"]');
  const ungrouped = form.querySelector('input[name="ungrouped"]');
  if (e.target === group && group.value && ungrouped) ungrouped.checked = false;
  if (e.target === ungrouped && ungrouped.checked && group) group.value = '';
  syncTokenFilter(form);
}, true);

// The Trends fragment changes independently of its range links. Keep the
// chosen podium metric in those links when the selector changes in place.
document.addEventListener('change', e => {
  if (!e.target.matches('#podium-controls select[name="podium_metric"]')) return;
  for (const link of document.querySelectorAll('#filter-bar .presets a, #filter-bar .chip')) {
    const url = new URL(link.getAttribute('href'), window.location.href);
    if (e.target.value === 'tokens') url.searchParams.delete('podium_metric');
    else url.searchParams.set('podium_metric', e.target.value);
    link.href = url.pathname + url.search + url.hash;
  }
});

// CSS starts heatmaps at the right edge, without a visible scroll after paint.
function initializeHeatmapNavigation(root = document) {
  const heatmaps = [];
  if (root instanceof Element && root.matches('[data-scroll-end]')) heatmaps.push(root);
  if (root.querySelectorAll) heatmaps.push(...root.querySelectorAll('[data-scroll-end]'));
  requestAnimationFrame(() => {
    for (const el of heatmaps) {
      updateHeatmapNavigation(el);
    }
  });
}

document.addEventListener('DOMContentLoaded', () => initializeHeatmapNavigation());
document.addEventListener('htmx:after:settle', e => initializeHeatmapNavigation(e.target));

function updateHeatmapNavigation(scroller) {
  const calendar = scroller.closest('.heatmap-calendar');
  if (!calendar) return;
  // RTL scroll containers use zero at the right edge and negative offsets leftward.
  calendar.querySelector('[data-heatmap-direction="-1"]').disabled = scroller.scrollWidth - scroller.clientWidth + scroller.scrollLeft <= 1;
  calendar.querySelector('[data-heatmap-direction="1"]').disabled = scroller.scrollLeft >= -1;
}
document.addEventListener('click', e => {
  const button = e.target.closest('[data-heatmap-direction]');
  if (!button) return;
  const scroller = button.closest('.heatmap-calendar').querySelector('.heatmap-scroll');
  scroller.scrollBy({
    left: Number(button.dataset.heatmapDirection) * scroller.clientWidth * 0.75,
    behavior: window.matchMedia('(prefers-reduced-motion: reduce)').matches ? 'instant' : 'smooth',
  });
});
document.addEventListener('scroll', e => {
  if (e.target instanceof Element && e.target.matches('.heatmap-scroll')) updateHeatmapNavigation(e.target);
}, true);
window.addEventListener('resize', () => {
  document.querySelectorAll('.heatmap-scroll').forEach(updateHeatmapNavigation);
});

// Delegated events also cover heatmaps replaced by live updates.
let activeHeatmapCell = null;
let heatmapTooltip = null;
function hideHeatmapTooltip() {
  activeHeatmapCell?.classList.remove('is-active');
  activeHeatmapCell = null;
  heatmapTooltip?.remove();
  heatmapTooltip = null;
}
function showHeatmapTooltip(cell) {
  hideHeatmapTooltip();
  activeHeatmapCell = cell;
  cell.classList.add('is-active');
  heatmapTooltip = document.createElement('div');
  heatmapTooltip.className = 'heatmap-tooltip';
  // The button's accessible label already includes both values.
  heatmapTooltip.setAttribute('aria-hidden', 'true');
  const date = document.createElement('div');
  date.textContent = cell.dataset.date + ' · UTC';
  const tokens = document.createElement('strong');
  tokens.textContent = cell.dataset.tokens + ' tokens';
  heatmapTooltip.append(date, tokens);
  document.body.append(heatmapTooltip);
  const rect = cell.getBoundingClientRect();
  const tip = heatmapTooltip.getBoundingClientRect();
  const left = Math.max(12, Math.min(rect.left + rect.width / 2 - tip.width / 2, window.innerWidth - tip.width - 12));
  const top = rect.top >= tip.height + 20 ? rect.top - tip.height - 10 : rect.bottom + 10;
  heatmapTooltip.style.left = `${left}px`;
  heatmapTooltip.style.top = `${Math.max(12, Math.min(top, window.innerHeight - tip.height - 12))}px`;
}
document.addEventListener('pointerover', e => {
  const cell = e.target.closest('button.heatmap-cell');
  if (cell && e.pointerType === 'mouse') showHeatmapTooltip(cell);
});
document.addEventListener('pointerout', e => {
  if (e.pointerType === 'mouse' && e.target === activeHeatmapCell) hideHeatmapTooltip();
});
document.addEventListener('focusin', e => {
  if (e.target.matches('button.heatmap-cell')) showHeatmapTooltip(e.target);
});
document.addEventListener('focusout', e => {
  if (e.target === activeHeatmapCell) hideHeatmapTooltip();
});
document.addEventListener('click', e => {
  const cell = e.target.closest('button.heatmap-cell');
  if (cell) showHeatmapTooltip(cell);
  else hideHeatmapTooltip();
});
document.addEventListener('keydown', e => {
  if (e.key === 'Escape') hideHeatmapTooltip();
});
document.addEventListener('scroll', hideHeatmapTooltip, true);
window.addEventListener('resize', hideHeatmapTooltip);
document.addEventListener('htmx:beforeSwap', hideHeatmapTooltip);

function setNavOpen(open) {
  const nav = document.getElementById('site-nav');
  if (!nav) return;
  nav.classList.toggle('open', open);
  const overlay = document.querySelector('.nav-overlay');
  if (overlay) overlay.classList.toggle('open', open);
  const toggle = document.querySelector('.menu-toggle');
  if (toggle) toggle.setAttribute('aria-expanded', String(open));
}

// Detail popovers: htmx swaps fragment content into an empty <dialog>; open
// it once content has settled. Closing (button, backdrop, Esc) is native.
function openSwappedDialog(target) {
  if (!(target instanceof HTMLDialogElement)) return;
  if (target.childElementCount && !target.open) {
    lockPageScroll();
    target.showModal();
  }
}

let lockedScrollY = 0;
function lockPageScroll() {
  if (document.body.classList.contains('dialog-open')) return;
  lockedScrollY = window.scrollY;
  document.body.style.setProperty('--dialog-scroll-top', `-${lockedScrollY}px`);
  document.documentElement.classList.add('dialog-open');
  document.body.classList.add('dialog-open');
}

function unlockPageScroll() {
  if (!document.body.classList.contains('dialog-open') || document.querySelector('dialog[open]')) return;
  document.documentElement.classList.remove('dialog-open');
  document.body.classList.remove('dialog-open');
  document.body.style.removeProperty('--dialog-scroll-top');
  window.scrollTo(0, lockedScrollY);
}

document.addEventListener('close', e => {
  if (e.target instanceof HTMLDialogElement) unlockPageScroll();
}, true);

document.addEventListener('click', e => {
  const toggle = e.target.closest('.menu-toggle');
  if (toggle) {
    setNavOpen(toggle.getAttribute('aria-expanded') !== 'true');
    return;
  }
  if (e.target.classList && e.target.classList.contains('nav-overlay')) {
    setNavOpen(false);
    return;
  }
  if (e.target.closest('#site-nav a')) setNavOpen(false);
  if (e.target.closest('.nav-close')) setNavOpen(false);
  const closer = e.target.closest('[data-close-dialog]');
  if (closer) closer.closest('dialog')?.close();
  // Clicks land on the <dialog> element itself when the backdrop is hit,
  // but also when its own padding band is clicked. Treat a click as a
  // backdrop close only if it falls outside the content's bounding box.
  if (e.target instanceof HTMLDialogElement && e.target.classList.contains('detail-dialog')) {
    const r = (e.target.firstElementChild || e.target).getBoundingClientRect();
    const inContent = e.clientX >= r.left && e.clientX <= r.right &&
      e.clientY >= r.top && e.clientY <= r.bottom;
    if (!inContent) e.target.close();
  }
});

document.addEventListener('keydown', e => {
  if (e.key === 'Escape') setNavOpen(false);
});

document.addEventListener('htmx:after:settle', e => openSwappedDialog(e.target));

function renderChart(id, d) {
  const el = document.getElementById(id);
  if (!el || !window.uPlot || !d || !d.labels || !d.labels.length) return;
  const xs = d.labels.map(ms => ms / 1000);
  const rows = [xs].concat(d.series.map(s => s.values));
  const isPct = d.series.some(s => s.fmt === 'pct');
  const isCost = d.series.some(s => s.fmt === 'cost');
  const series = [{ label: 'Date' }].concat(d.series.map((s, i) => ({
    label: s.name,
    stroke: PALETTE[i % PALETTE.length],
    width: 2,
    spanGaps: s.spanGaps === true,
    points: { show: d.labels.length < 40 || s.values.filter(v => v != null).length === 1 },
    value: (u, v) => v == null ? '' : (s.fmt === 'pct' ? v.toFixed(1) + '%' : s.fmt === 'cost' ? formatCost(v) : v.toLocaleString()),
  })));
  // Keep the time axis meaningful: pad it to at least one bucket span, so
  // single-bucket ranges (e.g. one day of data on week/month buckets) still
  // show a scale that matches the selected granularity.
  const span = d.span || 0;
  const scales = { y: {} };
  if (span) {
    scales.x = {
      range: (u, min, max) => {
        if (max - min >= span) return [min, max];
        const mid = (min + max) / 2;
        return [mid - span / 2, mid + span / 2];
      },
    };
  }
  if (isPct) scales.y = { range: [0, 100] };
  const opts = {
    width: el.clientWidth || 900,
    height: 280,
    scales,
    axes: [
      {
        stroke: '#94836f',
        grid: { stroke: 'rgba(233,220,203,0.6)' },
        ticks: { stroke: 'rgba(233,220,203,0.9)' },
      },
      {
        stroke: '#94836f',
        grid: { stroke: 'rgba(233,220,203,0.6)' },
        ticks: { stroke: 'rgba(233,220,203,0.9)' },
        values: (u, vs) => vs.map(v => v == null ? '' : (isPct ? v + '%' : isCost ? formatCostAxis(v) : abbrev(v))),
      },
    ],
    legend: { show: d.series.length > 1, live: false },
    series,
  };
  el.textContent = '';
  const u = new uPlot(opts, rows, el);
  for (let i = charts.length - 1; i >= 0; i--) {
    if (!charts[i].el.isConnected) charts.splice(i, 1);
  }
  charts.push({ u, el });
}

let resizeRAF = 0;
window.addEventListener('resize', () => {
  cancelAnimationFrame(resizeRAF);
  resizeRAF = requestAnimationFrame(() => {
    for (let i = charts.length - 1; i >= 0; i--) {
      const { u, el } = charts[i];
      if (!el.isConnected) {
        charts.splice(i, 1);
        continue;
      }
      const w = el.clientWidth;
      if (w && w !== u.width) u.setSize({ width: w, height: u.height });
    }
  });
});

function formatCost(v) {
  return '$' + v.toFixed(Math.abs(v) < 10 ? 4 : 2);
}

function formatCostAxis(v) {
  if (Math.abs(v) >= 1000) return '$' + abbrev(v);
  return '$' + Number(v.toFixed(Math.abs(v) < 10 ? 3 : 2));
}

function abbrev(v) {
  if (Math.abs(v) >= 1e9) return (v / 1e9).toFixed(1) + 'B';
  if (Math.abs(v) >= 1e6) return (v / 1e6).toFixed(1) + 'M';
  if (Math.abs(v) >= 1e3) return (v / 1e3).toFixed(1) + 'k';
  return v;
}

document.addEventListener('click', e => {
  const el = e.target.closest('[data-copy]');
  if (!el) return;
  navigator.clipboard.writeText(el.dataset.copy);
});

// Setup page: the receiver form rewrites the endpoint in every snippet and
// shows the bearer-token lines. The endpoint defaults to the dashboard's own
// host on the default OTLP/HTTP port; only a URL the user typed is saved, so
// the default keeps following the host. Storage may be unavailable (private
// mode), so every access is guarded.
const SETUP_KEY = 'ai-usage-setup';

function setupDefaultEndpoint() {
  const { protocol, hostname } = window.location;
  if (!hostname || (protocol !== 'http:' && protocol !== 'https:')) return 'http://127.0.0.1:4318';
  return `${protocol}//${hostname}:4318`;
}

function applySetupReceiver(form) {
  const endpoint = form.endpoint.value.trim().replace(/\/+$/, '') || setupDefaultEndpoint();
  for (const el of document.querySelectorAll('[data-endpoint]')) el.textContent = endpoint;
  for (const el of document.querySelectorAll('[data-auth]')) el.hidden = !form.auth.checked;
}

function initSetupReceiver() {
  const form = document.getElementById('setup-receiver');
  if (!form) return;
  const fallback = setupDefaultEndpoint();
  form.endpoint.placeholder = fallback;
  form.endpoint.value = fallback;
  try {
    const saved = JSON.parse(localStorage.getItem(SETUP_KEY) || '{}');
    if (typeof saved.endpoint === 'string' && saved.endpoint) form.endpoint.value = saved.endpoint;
    form.auth.checked = saved.auth === true;
  } catch (e) { /* storage unavailable or corrupt: keep defaults */ }
  applySetupReceiver(form);
  const save = () => {
    applySetupReceiver(form);
    const endpoint = form.endpoint.value.trim();
    try {
      localStorage.setItem(SETUP_KEY, JSON.stringify({
        endpoint: endpoint === fallback ? '' : endpoint,
        auth: form.auth.checked,
      }));
    } catch (e) { /* not persisted */ }
  };
  form.addEventListener('input', save);
  form.addEventListener('change', save);
  form.addEventListener('submit', e => e.preventDefault());
  form.querySelector('[data-setup-reset]').addEventListener('click', () => {
    try {
      localStorage.removeItem(SETUP_KEY);
    } catch (e) { /* nothing stored */ }
    form.endpoint.value = fallback;
    form.auth.checked = false;
    applySetupReceiver(form);
  });
}

document.addEventListener('DOMContentLoaded', initSetupReceiver);

document.addEventListener('click', e => {
  const button = e.target.closest('[data-copy-snippet]');
  if (!button) return;
  // innerText skips the hidden token lines, matching what is on screen.
  const pre = button.parentElement.querySelector('pre');
  navigator.clipboard.writeText(pre.innerText).then(() => {
    button.textContent = 'Copied';
    setTimeout(() => { button.textContent = 'Copy'; }, 1500);
  });
});

document.addEventListener('click', e => {
  const toggle = e.target.closest('[data-token-group-toggle]');
  if (!toggle) return;
  const rows = document.getElementById(toggle.getAttribute('aria-controls'));
  const expanded = toggle.getAttribute('aria-expanded') !== 'true';
  toggle.setAttribute('aria-expanded', String(expanded));
  rows.hidden = !expanded;
});

document.addEventListener('click', e => {
  const opener = e.target.closest('[data-token-dialog]');
  if (opener) {
    const dialog = document.getElementById(opener.dataset.tokenDialog);
    dialog.querySelector('form').reset();
    dialog.showModal();
  }
  if (e.target instanceof HTMLDialogElement && e.target.classList.contains('token-dialog')) {
    const rect = e.target.getBoundingClientRect();
    if (e.clientX < rect.left || e.clientX > rect.right || e.clientY < rect.top || e.clientY > rect.bottom) e.target.close();
  }
});

function initTokenGroups() {
  const groups = Array.from(document.querySelectorAll('#token-groups option'), option => option.value);
  if (!groups.length || !('showPopover' in HTMLElement.prototype)) return;
  document.querySelectorAll('[data-token-group]').forEach((input, index) => {
    const list = document.createElement('div');
    list.id = `token-group-options-${index}`;
    list.className = 'token-group-options';
    list.setAttribute('popover', 'manual');
    list.setAttribute('role', 'listbox');
    list.setAttribute('aria-label', 'Existing groups');
    input.parentElement.append(list);
    input.setAttribute('role', 'combobox');
    input.setAttribute('aria-autocomplete', 'list');
    input.setAttribute('aria-controls', list.id);
    input.setAttribute('aria-expanded', 'false');
    let selected = -1;
    let matches = [];
    const close = () => {
      if (list.matches(':popover-open')) list.hidePopover();
      input.setAttribute('aria-expanded', 'false');
      input.removeAttribute('aria-activedescendant');
      selected = -1;
    };
    const position = () => {
      const rect = input.getBoundingClientRect();
      const below = window.innerHeight - rect.bottom - 12;
      const above = rect.top - 12;
      const useAbove = below < 160 && above > below;
      list.style.width = `${rect.width}px`;
      list.style.maxHeight = `${Math.max(0, Math.min(192, useAbove ? above : below))}px`;
      list.style.left = `${rect.left}px`;
      list.style.top = `${useAbove ? rect.top - list.getBoundingClientRect().height - 4 : rect.bottom + 4}px`;
    };
    const open = (filter = false) => {
      matches = groups.filter(group => !filter || group.toLocaleLowerCase().includes(input.value.toLocaleLowerCase()));
      selected = -1;
      input.removeAttribute('aria-activedescendant');
      list.replaceChildren(...matches.map((group, i) => {
        const option = document.createElement('div');
        option.id = `${list.id}-${i}`;
        option.className = 'token-group-option';
        option.setAttribute('role', 'option');
        option.setAttribute('aria-selected', 'false');
        option.textContent = group;
        option.addEventListener('pointerdown', e => e.preventDefault());
        option.addEventListener('click', () => choose(i));
        return option;
      }));
      if (!matches.length) return close();
      if (!list.matches(':popover-open')) list.showPopover();
      input.setAttribute('aria-expanded', 'true');
      position();
    };
    const choose = i => {
      input.value = matches[i];
      close();
      input.dispatchEvent(new Event('change', { bubbles: true }));
      input.focus({ preventScroll: true });
    };
    input.addEventListener('click', () => open());
    input.addEventListener('input', () => open(true));
    input.addEventListener('blur', close);
    input.addEventListener('keydown', e => {
      if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
        e.preventDefault();
        if (!list.matches(':popover-open')) open();
        if (!matches.length) return;
        selected = selected < 0 ? (e.key === 'ArrowDown' ? 0 : matches.length - 1) : (selected + (e.key === 'ArrowDown' ? 1 : -1) + matches.length) % matches.length;
        Array.from(list.children).forEach((option, i) => option.setAttribute('aria-selected', String(i === selected)));
        input.setAttribute('aria-activedescendant', list.children[selected].id);
        list.children[selected].scrollIntoView({ block: 'nearest' });
      } else if (e.key === 'Enter' && list.matches(':popover-open') && selected >= 0) {
        e.preventDefault();
        choose(selected);
      } else if (e.key === 'Escape' && list.matches(':popover-open')) {
        e.preventDefault();
        e.stopPropagation();
        close();
      } else if (e.key === 'Tab') close();
    });
    document.addEventListener('pointerdown', e => {
      if (e.target !== input && !list.contains(e.target)) close();
    });
    input.closest('dialog')?.addEventListener('close', close);
    window.addEventListener('resize', close);
    document.addEventListener('scroll', e => { if (e.target !== list) close(); }, true);
  });
}
document.addEventListener('DOMContentLoaded', initTokenGroups);
