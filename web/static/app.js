// Warm harmonic palette: terracotta, amber, sage, dusty rose, plum, sand.
const PALETTE = ['#c96f4a', '#dfa14f', '#7d9b76', '#b06a6a', '#8b7d9b', '#a89f91'];

// Live chart instances; stale ones (htmx-swapped away) are pruned on resize.
const charts = [];

function renderChart(id, d) {
  const el = document.getElementById(id);
  if (!el || !window.uPlot || !d.labels.length) return;
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
