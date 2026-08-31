function renderTokenChart(id, d) {
  const el = document.getElementById(id);
  if (!el || !window.uPlot || !d.labels.length) return;
  const xs = d.labels.map(ms => ms / 1000);
  const rows = [xs].concat(d.series.map(s => s.values));
  const colors = ['#5aa9e6', '#f28f3b', '#8ac926', '#c77dff', '#ff9770', '#e0e0e0'];
  const hasCost = d.series.some(s => s.scale === 'cost');
  const series = [{ label: 'Day' }].concat(d.series.map((s, i) => ({
    label: s.name,
    scale: s.scale || 'y',
    stroke: colors[i % colors.length],
    width: s.scale ? 2 : 1.5,
    spanGaps: false,
    points: { show: d.labels.length < 40 },
    value: (u, v) => s.scale === 'cost' && v != null ? '$' + v.toFixed(4) : v,
  })));
  const opts = {
    width: el.clientWidth || 900,
    height: 300,
    scales: { y: {}, cost: { auto: true, side: 'right' } },
    axes: [{}, {
      scale: 'cost', show: hasCost,
      values: (u, vs) => vs.map(v => v == null ? '' : '$' + v.toFixed(2)),
    }],
    series,
  };
  el.textContent = '';
  new uPlot(opts, rows, el);
}

document.addEventListener('click', e => {
  const el = e.target.closest('[data-copy]');
  if (!el) return;
  navigator.clipboard.writeText(el.dataset.copy);
});
