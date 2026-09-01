# Vendored assets

Downloaded once and committed; served from the embedded binary. No CDN
references at runtime.

| File | Version | Source |
|------|---------|--------|
| `htmx.min.js` | 4.0.0 | https://unpkg.com/htmx.org@4.0.0/dist/htmx.min.js |
| `hx-sse.min.js` | 4.0.0 | https://unpkg.com/htmx.org@4.0.0/dist/ext/hx-sse.min.js |
| `uplot.min.js` | 1.6.32 (IIFE build, exposes global `uPlot`) | https://unpkg.com/uplot@1.6.32/dist/uPlot.iife.min.js |
| `uplot.min.css` | 1.6.32 | https://unpkg.com/uplot@1.6.32/dist/uPlot.min.css |

htmx 4's official `hx-sse` extension drives the `/events` stream. Unlike
the htmx 2 extension it replaces, it is fetch-based (no `EventSource`),
dispatches named events as bubbling DOM events, and defaults to
`pauseOnBackground` for `hx-sse:connect` — the stream closes while the
page is hidden, so background tabs never hold one of the browser's six
per-host connections (the htmx 2 extension leaked one stream per tab
switch and exhausted the limit on the sixth).
