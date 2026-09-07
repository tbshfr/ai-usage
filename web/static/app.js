// Warm harmonic palette: terracotta, amber, sage, dusty rose, plum, sand.
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

// The heatmap is chronological from left to right. On phones, start at the
// newest days while leaving the strip freely swipeable toward older history.
function scrollMobileHeatmapsToEnd(root = document) {
  if (!window.matchMedia('(max-width: 640px)').matches) return;
  const heatmaps = [];
  if (root instanceof Element && root.matches('[data-scroll-end]')) heatmaps.push(root);
  if (root.querySelectorAll) heatmaps.push(...root.querySelectorAll('[data-scroll-end]'));
  requestAnimationFrame(() => {
    for (const el of heatmaps) el.scrollLeft = el.scrollWidth;
  });
}

document.addEventListener('DOMContentLoaded', () => scrollMobileHeatmapsToEnd());
document.addEventListener('htmx:after:settle', e => scrollMobileHeatmapsToEnd(e.target));

// Slide-over menu (mobile): the topbar nav turns into a right-hand drawer,
// toggled by the hamburger and closed by the backdrop, any nav link, or Esc.
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
    target.showModal();
    lockPageScroll();
  }
}

let lockedScrollY = 0;
function lockPageScroll() {
  if (document.body.classList.contains('dialog-open')) return;
  lockedScrollY = window.scrollY;
  document.documentElement.classList.add('dialog-open');
  document.body.classList.add('dialog-open');
}

function unlockPageScroll() {
  if (document.querySelector('dialog[open]')) return;
  document.documentElement.classList.remove('dialog-open');
  document.body.classList.remove('dialog-open');
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
  const series = [{ label: 'Date' }].concat(d.series.map((s, i) => ({
    label: s.name,
    stroke: PALETTE[i % PALETTE.length],
    width: 2,
    spanGaps: false,
    points: { show: d.labels.length < 40 },
    value: (u, v) => v == null ? '' : (s.fmt === 'pct' ? v.toFixed(1) + '%' : v.toLocaleString()),
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
        values: (u, vs) => vs.map(v => v == null ? '' : (isPct ? v + '%' : abbrev(v))),
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

// Keep charts fitted to the viewport (rotation, window resize).
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
