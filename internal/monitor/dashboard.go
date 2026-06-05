package monitor

const dashboardHTML = `<!doctype html>
<html lang="en" data-theme="dark">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Workyard · Monitor</title>
<style>
:root,
[data-theme="dark"] {
  color-scheme: dark;
  --bg:     #0d1014;
  --panel:  #14181e;
  --line:   #20262f;
  --ink:    #e6eaf0;
  --ink-2:  #b3bac4;
  --muted:  #757f8c;
  --muted-2:#4a525d;
  --accent: #5fd1c3;
  --warn:   #f0b340;
  --bad:    #ef5a5a;
  --link:   #82aaff;
  --hover:  rgba(255,255,255,.025);
}
[data-theme="light"] {
  color-scheme: light;
  --bg:     #f7f5f0;
  --panel:  #ffffff;
  --line:   #e4ddce;
  --ink:    #1c1f25;
  --ink-2:  #41464f;
  --muted:  #6e7480;
  --muted-2:#a8aeb8;
  --accent: #1f8d7d;
  --warn:   #a56500;
  --bad:    #c2392f;
  --link:   #2257d5;
  --hover:  rgba(20,30,50,.03);
}

* { box-sizing: border-box; }
html, body { min-height: 100%; }
body {
  margin: 0;
  background: var(--bg);
  color: var(--ink);
  font-family: ui-sans-serif, system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
  font-size: 14px;
  line-height: 1.45;
  -webkit-font-smoothing: antialiased;
  -moz-osx-font-smoothing: grayscale;
}
button, input { font: inherit; color: inherit; }
::selection { background: var(--accent); color: var(--bg); }

.wrap {
  max-width: 1080px;
  margin: 0 auto;
  padding: 28px 24px 64px;
}

/* ---- Header ----------------------------------------------- */
header {
  display: flex;
  align-items: center;
  gap: 14px;
  padding-bottom: 18px;
  border-bottom: 1px solid var(--line);
  margin-bottom: 22px;
}
.brand {
  display: flex;
  align-items: baseline;
  gap: 8px;
  font-weight: 600;
  font-size: 15px;
  letter-spacing: -0.01em;
}
.brand em {
  font-style: normal;
  font-weight: 400;
  color: var(--muted);
}
.live {
  display: inline-flex;
  align-items: center;
  gap: 7px;
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 11.5px;
  color: var(--muted);
}
.live i {
  width: 6px; height: 6px;
  border-radius: 50%;
  background: var(--accent);
  animation: blink 1.6s ease-in-out infinite;
}
.live.stale i { background: var(--warn); animation: none; }
.live.dead  i { background: var(--bad);  animation: none; }
@keyframes blink {
  0%, 60%, 100% { opacity: 1; }
  80% { opacity: .35; }
}
header .spacer { flex: 1; }
.search {
  position: relative;
}
.search input {
  width: 260px;
  background: var(--panel);
  border: 1px solid var(--line);
  border-radius: 7px;
  padding: 7px 10px 7px 28px;
  color: var(--ink);
  outline: none;
}
.search input::placeholder { color: var(--muted-2); }
.search input:focus { border-color: var(--accent); }
.search .ico {
  position: absolute;
  left: 9px; top: 50%;
  transform: translateY(-50%);
  color: var(--muted);
  pointer-events: none;
}
.iconbtn {
  height: 32px;
  width: 32px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  background: var(--panel);
  border: 1px solid var(--line);
  border-radius: 7px;
  color: var(--ink-2);
  cursor: pointer;
}
.iconbtn:hover { color: var(--ink); }

/* ---- Summary ---------------------------------------------- */
.summary {
  display: flex;
  flex-wrap: wrap;
  gap: 22px;
  margin-bottom: 26px;
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 12.5px;
  color: var(--muted);
}
.summary span b {
  font-weight: 500;
  color: var(--ink);
  margin-right: 6px;
}
.summary .ok b   { color: var(--accent); }
.summary .warn b { color: var(--warn); }
.summary .bad b  { color: var(--bad); }

/* ---- Sections --------------------------------------------- */
section { margin-bottom: 32px; }
.section-head {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  margin-bottom: 10px;
}
.section-head h2 {
  margin: 0;
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 11px;
  font-weight: 500;
  letter-spacing: 0.16em;
  text-transform: uppercase;
  color: var(--muted);
}
.section-head .count {
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 11px;
  color: var(--muted-2);
}

/* ---- Workers ---------------------------------------------- */
.workers {
  border: 1px solid var(--line);
  border-radius: 8px;
  background: var(--panel);
  overflow: hidden;
}
.worker {
  display: grid;
  grid-template-columns: 14px 1fr auto auto;
  align-items: center;
  gap: 14px;
  padding: 11px 14px;
  border-bottom: 1px solid var(--line);
}
.worker:last-child { border-bottom: none; }
.worker:hover { background: var(--hover); }
.dot {
  width: 8px; height: 8px;
  border-radius: 50%;
  background: var(--muted-2);
}
.dot.ok   { background: var(--accent); }
.dot.warn { background: var(--warn); }
.dot.bad  { background: var(--bad); }
.worker .name {
  font-weight: 500;
  font-size: 13.5px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.worker .stats {
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 11.5px;
  color: var(--muted);
  white-space: nowrap;
}
.worker .stats b { color: var(--ink); font-weight: 500; }
.worker .when {
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 11px;
  color: var(--muted-2);
  white-space: nowrap;
}
.worker .err {
  grid-column: 2 / -1;
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 11.5px;
  color: var(--bad);
  margin-top: 4px;
  word-break: break-word;
}

/* ---- Services table --------------------------------------- */
.tbl-wrap {
  border: 1px solid var(--line);
  border-radius: 8px;
  background: var(--panel);
  overflow-x: auto;
}
table {
  width: 100%;
  border-collapse: separate;
  border-spacing: 0;
}
th, td {
  text-align: left;
  padding: 10px 14px;
  border-bottom: 1px solid var(--line);
  vertical-align: middle;
}
tbody tr:last-child td { border-bottom: none; }
tbody tr:hover { background: var(--hover); }
th {
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 10.5px;
  font-weight: 500;
  color: var(--muted);
  letter-spacing: 0.12em;
  text-transform: uppercase;
}
td { font-size: 13px; overflow-wrap: anywhere; }
.svc {
  display: inline-flex;
  align-items: center;
  gap: 10px;
  font-weight: 500;
}
.mono {
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 12px;
}
.sub {
  display: block;
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 11px;
  color: var(--muted);
  margin-top: 2px;
}
.dim { color: var(--muted-2); }
a {
  color: var(--link);
  text-decoration: none;
}
a:hover { text-decoration: underline; }

/* ---- Events ----------------------------------------------- */
.events {
  border: 1px solid var(--line);
  border-radius: 8px;
  background: var(--panel);
  max-height: 420px;
  overflow: auto;
}
.event {
  display: grid;
  grid-template-columns: 90px 1fr;
  gap: 14px;
  padding: 10px 14px;
  border-bottom: 1px solid var(--line);
  font-size: 13px;
}
.event:last-child { border-bottom: none; }
.event .t {
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 11px;
  color: var(--muted);
  padding-top: 1px;
}
.event .body { min-width: 0; }
.event .type {
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 11.5px;
  color: var(--ink-2);
  margin-right: 8px;
}
.event.bad  .type { color: var(--bad); }
.event.warn .type { color: var(--warn); }
.event .msg { color: var(--ink); word-break: break-word; }
.event .loc {
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 11px;
  color: var(--muted);
  margin-top: 2px;
}

.empty {
  color: var(--muted);
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 12px;
  padding: 18px 14px;
  text-align: center;
}

@media (max-width: 720px) {
  .wrap { padding: 18px 14px 40px; }
  .search input { width: 160px; }
  .worker {
    grid-template-columns: 14px 1fr;
    row-gap: 4px;
  }
  .worker .stats, .worker .when { grid-column: 2 / -1; }
  table { min-width: 640px; }
}
</style>
</head>
<body>
<div class="wrap">
  <header>
    <div class="brand">Workyard <em>/ Monitor</em></div>
    <div class="live" id="live"><i></i><span id="live-text">connecting…</span></div>
    <div class="spacer"></div>
    <div class="search">
      <svg class="ico" width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="11" cy="11" r="7"></circle><path d="m21 21-4.3-4.3"></path></svg>
      <input id="filter" type="search" autocomplete="off" placeholder="Filter">
    </div>
    <button class="iconbtn" id="theme" title="Toggle theme" aria-pressed="false">
      <svg id="ico-moon" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M21 12.8A9 9 0 1 1 11.2 3a7 7 0 0 0 9.8 9.8z"></path></svg>
      <svg id="ico-sun" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" style="display:none"><circle cx="12" cy="12" r="4"></circle><path d="M12 2v2M12 20v2M4.93 4.93l1.41 1.41M17.66 17.66l1.41 1.41M2 12h2M20 12h2M4.93 19.07l1.41-1.41M17.66 6.34l1.41-1.41"></path></svg>
    </button>
  </header>

  <div class="summary" id="summary"></div>

  <section>
    <div class="section-head">
      <h2>Workers</h2>
      <span class="count" id="workers-count">0</span>
    </div>
    <div class="workers" id="workers"></div>
  </section>

  <section>
    <div class="section-head">
      <h2>Services</h2>
      <span class="count" id="svc-count">0</span>
    </div>
    <div class="tbl-wrap">
      <table>
        <thead>
          <tr>
            <th style="width:32%">Service</th>
            <th style="width:26%">Worker / Run</th>
            <th style="width:10%">Status</th>
            <th style="width:8%">Port</th>
            <th style="width:24%">URL</th>
          </tr>
        </thead>
        <tbody id="services"></tbody>
      </table>
    </div>
  </section>

  <section>
    <div class="section-head">
      <h2>Events</h2>
      <span class="count" id="events-count">0</span>
    </div>
    <div class="events" id="events"></div>
  </section>
</div>

<script>
const els = {
  workers: document.querySelector("#workers"),
  workersCount: document.querySelector("#workers-count"),
  services: document.querySelector("#services"),
  svcCount: document.querySelector("#svc-count"),
  events: document.querySelector("#events"),
  eventsCount: document.querySelector("#events-count"),
  summary: document.querySelector("#summary"),
  filter: document.querySelector("#filter"),
  theme: document.querySelector("#theme"),
  iconMoon: document.querySelector("#ico-moon"),
  iconSun: document.querySelector("#ico-sun"),
  live: document.querySelector("#live"),
  liveText: document.querySelector("#live-text")
};

let state = null;
let lastPollAt = 0;

function esc(v) {
  return String(v == null ? "" : v).replace(/[&<>"']/g, ch => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[ch]));
}
function relTime(value) {
  if (!value) return "never";
  const d = new Date(value);
  const delta = Math.max(0, Date.now() - d.getTime());
  if (delta < 1000) return "just now";
  if (delta < 60000) return Math.floor(delta / 1000) + "s ago";
  if (delta < 3600000) return Math.floor(delta / 60000) + "m ago";
  if (delta < 86400000) return Math.floor(delta / 3600000) + "h ago";
  return d.toLocaleDateString();
}
function tone(status, healthy) {
  if (healthy === true) return "ok";
  if (healthy === false && status === "running") return "warn";
  if (status === "online" || status === "healthy" || status === "running") return "ok";
  if (status === "offline" || status === "exited" || status === "failed" || status === "error") return "bad";
  return "warn";
}
function matches(item, term) {
  if (!term) return true;
  return JSON.stringify(item).toLowerCase().includes(term);
}

function applyTheme(theme) {
  document.documentElement.setAttribute("data-theme", theme);
  const light = theme === "light";
  els.theme.setAttribute("aria-pressed", String(light));
  els.iconMoon.style.display = light ? "none" : "";
  els.iconSun.style.display  = light ? "" : "none";
  try { localStorage.setItem("workyard-theme", theme); } catch (e) {}
}
function initTheme() {
  let theme = "dark";
  try {
    const saved = localStorage.getItem("workyard-theme");
    if (saved === "light" || saved === "dark") theme = saved;
    else if (window.matchMedia && window.matchMedia("(prefers-color-scheme: light)").matches) theme = "light";
  } catch (e) {}
  applyTheme(theme);
}

function render(next) {
  state = next;
  const term = els.filter.value.trim().toLowerCase();
  const workers  = (next.workers  || []).filter(x => matches(x, term));
  const services = (next.services || []).filter(x => matches(x, term));
  const events   = (next.events   || []).filter(x => matches(x, term)).slice(0, 60);

  lastPollAt = Date.now();
  updateLive(next);

  const wOnline = workers.filter(w => tone(w.status) === "ok").length;
  const healthy = services.filter(s => s.healthy).length;
  const failing = services.filter(s => tone(s.status, s.healthy) === "bad").length;

  els.summary.innerHTML =
    '<span class="ok"><b>' + wOnline + '</b> / ' + workers.length + ' workers online</span>' +
    '<span class="ok"><b>' + healthy + '</b> / ' + services.length + ' services healthy</span>' +
    (failing > 0
      ? '<span class="bad"><b>' + failing + '</b> failing</span>'
      : '<span>no incidents</span>');

  els.workersCount.textContent = workers.length;
  els.svcCount.textContent = services.length;
  els.eventsCount.textContent = events.length;

  els.workers.innerHTML = workers.length ? workers.map(w => {
    const t = tone(w.status);
    return '<div class="worker">' +
      '<span class="dot ' + t + '"></span>' +
      '<div class="name">' + esc(w.name) + '</div>' +
      '<div class="stats">' +
        '<b>' + esc(w.runningCount || 0) + '</b>/' + esc(w.serviceCount || 0) + ' services · ' +
        '<b>' + esc(w.healthyCount || 0) + '</b> healthy' +
      '</div>' +
      (w.lastError ? '<div class="err">' + esc(w.lastError) + '</div>' : '') +
    '</div>';
  }).join("") : '<div class="empty">No workers reporting</div>';

  els.services.innerHTML = services.length ? services.map(s => {
    const t = tone(s.status, s.healthy);
    return '<tr>' +
      '<td>' +
        '<span class="svc"><span class="dot ' + t + '"></span>' + esc(s.name) + '</span>' +
        (s.startCommand ? '<span class="sub">' + esc(s.startCommand) + '</span>' : '') +
      '</td>' +
      '<td class="mono">' + esc(s.worker) +
        '<span class="sub">' + esc(s.project) + ' / ' + esc(s.runId) + '</span>' +
      '</td>' +
      '<td class="mono">' + esc(s.status || "—") + '</td>' +
      '<td class="mono">' + (s.port ? esc(s.port) : '<span class="dim">—</span>') + '</td>' +
      '<td class="mono">' + (s.url ? '<a href="' + esc(s.url) + '" target="_blank" rel="noreferrer">' + esc(s.url) + '</a>' : '<span class="dim">—</span>') + '</td>' +
    '</tr>';
  }).join("") : '<tr><td colspan="5"><div class="empty">No services</div></td></tr>';

  els.events.innerHTML = events.length ? events.map(e => {
    const type = (e.type || "").toLowerCase();
    const t = type.includes("error") || type.includes("fail") ? "bad" :
              type.includes("warn") ? "warn" : "ok";
    return '<div class="event ' + t + '">' +
      '<div class="t">' + esc(relTime(e.time)) + '</div>' +
      '<div class="body">' +
        '<span class="type">' + esc(e.type) + (e.service ? ' · ' + esc(e.service) : '') + '</span>' +
        '<span class="msg">' + esc(e.message || '') + '</span>' +
        '<div class="loc">' + esc(e.worker) + ' / ' + esc(e.project) + ' / ' + esc(e.runId) + '</div>' +
      '</div>' +
    '</div>';
  }).join("") : '<div class="empty">No events</div>';
}

function updateLive(snap) {
  const stale = snap && snap.generatedAt
    ? (Date.now() - new Date(snap.generatedAt).getTime()) > 12000
    : true;
  els.live.classList.toggle("stale", stale);
  els.live.classList.remove("dead");
  els.liveText.textContent = "live · every " + (snap && snap.refreshInterval ? snap.refreshInterval : "—");
}

els.filter.addEventListener("input", () => state && render(state));
els.theme.addEventListener("click", () => {
  const next = document.documentElement.getAttribute("data-theme") === "dark" ? "light" : "dark";
  applyTheme(next);
});
document.addEventListener("keydown", (e) => {
  if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "k") {
    e.preventDefault();
    els.filter.focus();
    els.filter.select();
  }
  if (e.key === "Escape" && document.activeElement === els.filter) {
    els.filter.value = "";
    if (state) render(state);
    els.filter.blur();
  }
});

async function load() {
  try {
    const res = await fetch("/api/state", { cache: "no-store" });
    render(await res.json());
  } catch (err) {
    els.live.classList.add("dead");
    els.liveText.textContent = "offline";
  }
}

initTheme();
load();
setInterval(load, 2000);
setInterval(() => {
  if (lastPollAt && Date.now() - lastPollAt > 12000) {
    els.live.classList.add("stale");
    els.liveText.textContent = "stale · last " + Math.round((Date.now() - lastPollAt) / 1000) + "s ago";
  }
}, 1000);
</script>
</body>
</html>`
