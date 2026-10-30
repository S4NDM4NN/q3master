// Shared helpers for index.html, server.html, and masters.html: canvas
// sparkline charting, the master-server uptime bar, and Q3/ET name-color +
// formatting utilities.

function debounce(fn, wait = 150) {
  let t;
  return (...args) => { clearTimeout(t); t = setTimeout(() => fn(...args), wait); };
}

// Status-page-style day-by-day uptime bar for a master server: one colored
// segment per calendar day for the trailing N days. N is picked from
// viewport width rather than fixed at 90: each bar has a CSS min-width (see
// .uptime-bar), so cramming 90 of them into a narrow phone-portrait width
// would either overflow the page horizontally or force bars thinner than
// the min-width allows -- shrinking the day count instead keeps every bar
// legible and tappable. Days with no recorded checks (before this feature
// existed for that host, or not caught up yet) render as the neutral
// "no data" gray.
function uptimeBarDayCount(){
  const w = window.innerWidth;
  if (w < 420) return 30;
  if (w < 700) return 60;
  return 90;
}
function dayKeyUTC(d){
  return d.getUTCFullYear() + '-' + String(d.getUTCMonth()+1).padStart(2,'0') + '-' + String(d.getUTCDate()).padStart(2,'0');
}
// opts: { host, containerId, summaryId (optional), rangeStartId (optional) }
function refreshUptimeBar(opts){
  const days = uptimeBarDayCount();
  $.getJSON('/api/history/master/daily?host=' + encodeURIComponent(opts.host) + '&days=' + days, function(points){
    const byDay = {};
    (points || []).forEach(p => { byDay[dayKeyUTC(new Date(p.ts))] = p; });

    const today = new Date();
    const container = $('#' + opts.containerId).empty();
    let trackedDays = 0, upDays = 0;

    for (let i = days - 1; i >= 0; i--){
      const d = new Date(Date.UTC(today.getUTCFullYear(), today.getUTCMonth(), today.getUTCDate()));
      d.setUTCDate(d.getUTCDate() - i);
      const label = d.toLocaleDateString(undefined, {month:'short', day:'numeric'});
      const p = byDay[dayKeyUTC(d)];

      let cls = '', title = `${label}: no data`;
      if (p){
        trackedDays++;
        if (p.uptime_pct >= 99.9){ cls = 'up'; upDays++; }
        else if (p.uptime_pct > 0){ cls = 'degraded'; }
        else { cls = 'down'; }
        title = `${label}: ${Math.round(p.uptime_pct)}% uptime (${p.sample_count} check${p.sample_count===1?'':'s'})`;
      }
      container.append(`<div class="uptime-bar ${cls}" title="${title}"></div>`);
    }

    if (opts.summaryId) {
      $('#' + opts.summaryId).text(
        trackedDays ? `${((upDays/trackedDays)*100).toFixed(1)}% of tracked days fully up` : 'No data yet'
      );
    }
    if (opts.rangeStartId) {
      const rangeStart = new Date(Date.UTC(today.getUTCFullYear(), today.getUTCMonth(), today.getUTCDate()));
      rangeStart.setUTCDate(rangeStart.getUTCDate() - (days - 1));
      $('#' + opts.rangeStartId).text(rangeStart.toLocaleDateString(undefined, {month:'short', day:'numeric'}));
    }
  });
}

// --- Lightweight canvas line charts (no charting library) ---
function fitCanvas(canvas) {
  const rect = canvas.getBoundingClientRect();
  const dpr = window.devicePixelRatio || 1;
  const w = Math.max(100, rect.width), h = Math.max(30, rect.height);
  canvas.width = w * dpr;
  canvas.height = h * dpr;
  const ctx = canvas.getContext('2d');
  ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
  return { ctx, w, h };
}

// Splits points into segments, breaking wherever the gap between
// consecutive samples is much larger than the series' typical spacing.
// Missed/failed polls simply don't write a sample (see history.RecordSample
// call sites), so a stretch of the server being unreachable for hours shows
// up as one big gap between two ordinary-looking points -- without this,
// drawSparkline would connect straight across it and an outage would render
// as an innocuous flat plateau instead of a visible break. The threshold is
// derived from *local* median spacing -- a window of nearby deltas, not one
// global median across the whole series -- so it adapts within whichever
// resolution tier a given stretch of points came from (raw samples ~a
// minute apart, hourly or daily rollups further back; GetNetworkHistory /
// GetServerHistory serve exactly this kind of tiered mix for any range
// wider than a few hours). A single global median breaks down the moment a
// series mixes tiers: a week-long chart's tail (raw, ~60s apart) vastly
// outnumbers its older hourly-resolution stretch (~3600s apart), so the
// global median lands in the raw tier and every ordinary hourly gap looks
// like a multi-hour outage -- fragmenting a perfectly continuous week into
// a scatter of single-point segments.
function splitOnGaps(points) {
  if (points.length < 3) return [points];

  const deltas = [];
  for (let i = 1; i < points.length; i++) deltas.push(points[i].x - points[i - 1].x);
  const floorMs = 10 * 60 * 1000;
  const windowRadius = 5;

  const localThreshold = i => {
    const lo = Math.max(0, i - windowRadius);
    const hi = Math.min(deltas.length, i + windowRadius + 1);
    const window = [...deltas.slice(lo, hi)].sort((a, b) => a - b);
    const median = window[Math.floor(window.length / 2)];
    return Math.max(3 * median, floorMs);
  };

  const segments = [];
  let current = [points[0]];
  for (let i = 1; i < points.length; i++) {
    if (points[i].x - points[i - 1].x > localThreshold(i - 1)) {
      segments.push(current);
      current = [];
    }
    current.push(points[i]);
  }
  segments.push(current);
  return segments;
}

// "3d ago" / "5h ago" / "12m ago" for a millisecond age.
function formatAgo(ms) {
  const m = Math.round(ms / 60000);
  if (m < 1) return 'now';
  if (m < 60) return m + 'm ago';
  const h = Math.round(m / 60);
  if (h < 48) return h + 'h ago';
  return Math.round(h / 24) + 'd ago';
}

// X-axis tick label: clock time for spans under ~2 days (where the date
// would repeat on every tick), otherwise the date.
function formatAxisTime(ts, spanMs) {
  const d = new Date(ts);
  if (spanMs <= 48 * 3600 * 1000) return d.toLocaleTimeString(undefined, { hour: 'numeric', minute: '2-digit' });
  return d.toLocaleDateString(undefined, { month: 'short', day: 'numeric' });
}

// Draws a sparkline chart and wires up hover/touch readout. Layout, top to
// bottom: a header strip ("latest" value on the left, "peak" and when it
// happened on the right), the plot, and a time-axis strip. The latest value
// is marked by a solid dot on the end of the line, so the number in the
// header and the dot are visibly the same thing; the peak gets a hollow
// marker only when it is a different point (otherwise the two would stack).
// Taller charts (>= 120px, the server detail page) also get y gridlines
// with labels and several x ticks; compact ones show just the range ends.
//
// opts: { color, peakLabel (include peak timestamp even when narrow) }
function drawSparkline(canvas, points, opts = {}) {
  if (!canvas) return;
  canvas._spark = { points, opts, hover: -1 };

  if (!canvas._sparkBound) {
    canvas._sparkBound = true;
    // pan-y: keep vertical page scroll working when a finger lands on a chart.
    canvas.style.touchAction = 'pan-y';
    const onMove = e => {
      const st = canvas._spark;
      if (!st || !st.layout || !st.points.length) return;
      const rect = canvas.getBoundingClientRect();
      const px = e.clientX - rect.left;
      const { minX, maxX, pad, w } = st.layout;
      const frac = (px - pad) / (w - 2 * pad);
      const target = minX + Math.min(1, Math.max(0, frac)) * (maxX - minX);
      let best = 0, bestD = Infinity;
      st.points.forEach((p, i) => {
        const d = Math.abs(p.x - target);
        if (d < bestD) { bestD = d; best = i; }
      });
      if (st.hover !== best) { st.hover = best; renderSparkline(canvas); }
    };
    const onLeave = () => {
      const st = canvas._spark;
      if (st && st.hover !== -1) { st.hover = -1; renderSparkline(canvas); }
    };
    canvas.addEventListener('pointermove', onMove);
    canvas.addEventListener('pointerdown', onMove);
    canvas.addEventListener('pointerleave', onLeave);
    canvas.addEventListener('pointercancel', onLeave);
  }
  renderSparkline(canvas);
}

function renderSparkline(canvas) {
  const { points, opts, hover } = canvas._spark;
  const { ctx, w, h } = fitCanvas(canvas);
  ctx.clearRect(0, 0, w, h);

  if (!points || points.length === 0) {
    ctx.fillStyle = '#777';
    ctx.font = '11px monospace';
    ctx.fillText('No data yet', 8, h / 2 + 4);
    return;
  }

  const color = opts.color || '#0dcaf0';
  const tall = h >= 120;
  const headerH = 14, footerH = 13;
  const xs = points.map(p => p.x), ys = points.map(p => p.y);
  const minX = Math.min(...xs), maxX = Math.max(...xs);
  const maxY = Math.max(1, ...ys);
  const pad = 4;
  const plotTop = headerH + 5;
  const plotBottom = h - footerH - 2;
  const xScale = x => pad + (w - 2 * pad) * (maxX === minX ? 0.5 : (x - minX) / (maxX - minX));
  const yScale = y => plotBottom - (plotBottom - plotTop) * (y / maxY);
  canvas._spark.layout = { minX, maxX, pad, w };

  const last = points[points.length - 1];
  let peak = points[0];
  for (const p of points) if (p.y > peak.y) peak = p;
  const showPeakMarker = peak.y > last.y;

  // Gridlines: baseline always (anchors "zero"), plus labeled max/half
  // lines on tall charts so the y scale can actually be read.
  ctx.lineWidth = 1;
  ctx.font = '10px monospace';
  ctx.textAlign = 'left';
  const grid = tall ? [0, 0.5, 1] : [0];
  for (const f of grid) {
    const y = Math.round(plotBottom - (plotBottom - plotTop) * f) + 0.5;
    ctx.strokeStyle = f === 0 ? '#444' : '#2c2c2c';
    ctx.setLineDash(f === 0 ? [] : [3, 3]);
    ctx.beginPath(); ctx.moveTo(pad, y); ctx.lineTo(w - pad, y); ctx.stroke();
    if (tall) {
      ctx.fillStyle = '#777';
      ctx.fillText(String(Math.round(maxY * f)), pad + 2, y - 3);
    }
  }
  ctx.setLineDash([]);

  for (const segment of splitOnGaps(points)) {
    if (segment.length === 0) continue;

    ctx.beginPath();
    segment.forEach((p, i) => {
      const x = xScale(p.x), y = yScale(p.y);
      if (i === 0) ctx.moveTo(x, y); else ctx.lineTo(x, y);
    });
    ctx.strokeStyle = color;
    ctx.lineWidth = 1.5;
    ctx.stroke();

    if (segment.length > 1) {
      ctx.lineTo(xScale(segment[segment.length - 1].x), plotBottom);
      ctx.lineTo(xScale(segment[0].x), plotBottom);
      ctx.closePath();
      ctx.fillStyle = color + '26';
      ctx.fill();
    }
  }

  const dot = (p, filled) => {
    ctx.beginPath();
    ctx.arc(xScale(p.x), yScale(p.y), 3, 0, Math.PI * 2);
    ctx.fillStyle = filled ? color : '#111';
    ctx.strokeStyle = color;
    ctx.lineWidth = 1.5;
    ctx.fill();
    ctx.stroke();
  };
  if (showPeakMarker) dot(peak, false);
  dot(last, true);

  // Header: latest (matches the solid end dot) on the left, peak (matches
  // the hollow dot) on the right.
  ctx.font = '11px monospace';
  ctx.textAlign = 'left';
  ctx.fillStyle = color;
  const latestText = 'latest ' + Math.round(last.y);
  ctx.fillText(latestText, pad, 11);
  const latestW = ctx.measureText(latestText).width;

  const now = Date.now();
  let peakText = 'peak ' + Math.round(peak.y);
  const withWhen = peakText + ' · ' + (opts.peakLabel ? formatLastSeen(peak.x) : formatAgo(now - peak.x));
  ctx.fillStyle = '#999';
  ctx.textAlign = 'right';
  if (latestW + ctx.measureText(withWhen).width + 16 <= w - 2 * pad) peakText = withWhen;
  ctx.fillText(peakText, w - pad, 11);

  // X axis. Last tick is "now"; the rest are dates/times across the span.
  const span = maxX - minX;
  const nTicks = tall ? Math.max(2, Math.min(6, Math.floor(w / 110))) : 2;
  ctx.font = '10px monospace';
  ctx.fillStyle = '#777';
  for (let i = 0; i < nTicks; i++) {
    const f = nTicks === 1 ? 0 : i / (nTicks - 1);
    const t = minX + span * f;
    ctx.textAlign = i === 0 ? 'left' : (i === nTicks - 1 ? 'right' : 'center');
    const label = i === nTicks - 1 ? 'now' : formatAxisTime(t, span);
    ctx.fillText(label, i === 0 ? pad : (i === nTicks - 1 ? w - pad : xScale(t)), h - 2);
  }
  ctx.textAlign = 'left';

  // Hover readout: guide line, dot on the point, and a small box with the
  // value and its time (clamped so it never runs off either edge).
  if (hover >= 0 && hover < points.length) {
    const p = points[hover];
    const x = xScale(p.x);
    ctx.strokeStyle = '#666';
    ctx.lineWidth = 1;
    ctx.beginPath(); ctx.moveTo(Math.round(x) + 0.5, plotTop); ctx.lineTo(Math.round(x) + 0.5, plotBottom); ctx.stroke();
    dot(p, true);

    const v = Math.round(p.y * 10) / 10;
    const text = `${v} · ${formatLastSeen(p.x)}`;
    ctx.font = '11px monospace';
    const tw = ctx.measureText(text).width + 10;
    let bx = x - tw / 2;
    bx = Math.max(pad, Math.min(w - pad - tw, bx));
    const by = plotTop;
    ctx.fillStyle = '#000000d9';
    ctx.strokeStyle = '#555';
    ctx.fillRect(bx, by, tw, 16);
    ctx.strokeRect(bx + 0.5, by + 0.5, tw - 1, 15);
    ctx.fillStyle = '#eee';
    ctx.textAlign = 'left';
    ctx.fillText(text, bx + 5, by + 12);
  }
}

// Q3/ET extended color codes. s4ndmod26's engine fork extends stock
// Quake3's 10 colors ('^0'-'^9') to a 32-entry table spanning '0'-'O'
// (ASCII 0x30-0x4F: digits, then punctuation ':;<=>?@', then uppercase
// 'A'-'O'), indexed via (charCode - 0x30) & 31 -- see
// Q_IsColorString/ColorIndex/g_color_table in
// ~/s4ndmod26/mod/src/game/q_shared.h and q_math.c. Values below are
// g_color_table's floats converted to hex. Because the engine masks with
// & 31 rather than validating against a fixed set, *any* character after
// '^' (other than another '^', which prints a literal caret) resolves to
// some table entry via wraparound -- so this applies unconditionally
// rather than gating on a lookup, matching the real client instead of an
// invented substitute palette.
const Q3_COLOR_TABLE = [
  '#000000', '#FF0000', '#00FF00', '#FFFF00', '#0000FF', '#00FFFF', '#FF00FF', '#FFFFFF',
  '#FF8000', '#808080', '#BFBFBF', '#BFBFBF', '#008000', '#808000', '#000080', '#800000',
  '#804000', '#FF991A', '#008080', '#800080', '#0080FF', '#8000FF', '#3399CC', '#CCFFCC',
  '#006633', '#FF0033', '#B21A1A', '#993300', '#CC9933', '#999933', '#FFFFBF', '#FFFF80',
];
function q3ColorForCode(ch) {
  return Q3_COLOR_TABLE[(ch.charCodeAt(0) - 0x30) & 31];
}
function parseNameColors(name) {
  let out = '', color = 'white';
  for (let i = 0; i < name.length; i++) {
    if (name[i] === '^' && i + 1 < name.length) {
      if (name[i + 1] === '^') {
        // "^^XY": the real client shows this as a literal '^' (the first
        // caret isn't a valid escape target, since the next char is
        // another caret) followed by whatever the second caret's pair
        // produces -- X becomes an invisible color change and Y then just
        // prints as an ordinary character. In practice this whole 4-char
        // unit reads as "a literal caret, plus one stray leftover
        // character" that was never meant to be part of the display --
        // widespread across real server names (e.g. "^^77Q3RETRO...",
        // "^^44GENESIS...", the "fpsclasico" network's "^^&&FreeFUn...")
        // wherever an operator apparently discovered ^^ as "how to show a
        // literal caret" without realizing it leaves this artifact behind.
        // So it's collapsed to just the literal caret here -- X still
        // updates the running color (matching what the real client would
        // tint everything after it), but neither X nor Y is drawn.
        out += `<span style="color:${color}">^</span>`;
        i++; // second '^'
        if (i + 1 < name.length) { color = q3ColorForCode(name[i + 1]); i++; } // X
        if (i + 1 < name.length) { i++; } // Y, dropped
        continue;
      }
      color = q3ColorForCode(name[++i]); continue;
    }
    out += `<span style="color:${color}">${name[i]}</span>`;
  }
  return out || '&nbsp;';
}

// Renders one real-player name, flagging it if it's in the server's
// suspected_bots list (see ServerEntry.SuspectedBots -- a name that's
// stayed connected suspiciously long without ever leaving, heuristically
// suggesting it's a spoofed fake-player entry rather than a real human).
// Matching is on the raw name string, same as what the server reported.
function formatPlayerName(name, suspectedBots) {
  const flagged = (suspectedBots || []).includes(name);
  const warn = flagged
    ? ' <i class="bi bi-exclamation-triangle-fill suspected-bot-icon" title="Suspected bot: connected continuously for an unusually long time without ever leaving"></i>'
    : '';
  return parseNameColors(name) + warn;
}

// Game taxonomy. Several protocol numbers can belong to the same game
// (RTCW 1.0 and 1.4 are both "RTCW", just different client builds), so
// this maps each protocol to a game key for the primary filter/badge,
// merging those builds together.
const PROTOCOL_INFO = {
  57: { game: 'rtcw' },
  60: { game: 'rtcw' },
  61: { game: 'rtcw' },
  84: { game: 'et' },
  82: { game: 'et' },
  68: { game: 'q3a' },
  71: { game: 'oa' },
};
const GAME_LABELS = { rtcw: 'RTCW', et: 'ET', q3a: 'Quake 3 Arena', oa: 'OpenArena' };
const GAME_ORDER = ['rtcw', 'et', 'q3a', 'oa'];

// Inverse of PROTOCOL_INFO (game -> its protocol numbers), derived rather
// than hand-maintained separately so the two can't drift apart. Used by
// index.html/quake3.html to sum per-protocol network-history series for a
// game that spans more than one protocol, and to scope each page's fetched
// data down to its own subset of games (see PAGE_GAMES on each page).
const GAME_PROTOCOLS = {};
Object.entries(PROTOCOL_INFO).forEach(([proto, info]) => {
  (GAME_PROTOCOLS[info.game] || (GAME_PROTOCOLS[info.game] = [])).push(Number(proto));
});

// Fallback for servers whose protocol number isn't in PROTOCOL_INFO --
// which turned out to include not just genuinely-new protocols, but also
// older/nonstandard RTCW-family builds (plain "Wolf 1.41-MP", several
// pre-1.5x iortcw releases) that apparently never send a "protocol" key in
// their getstatus reply at all, so ServerEntry.Protocol stays 0 forever no
// matter how many times they're successfully polled -- confirmed by
// checking the real production list, where these have full hostname/version
// data but protocol:0. No protocol number will ever fix that, so as a last
// resort this pattern-matches the engine name off the server's own
// self-reported version string instead. Order matters: OpenArena's
// "ioq3+oa..." string starts with "ioq3", so it's checked before the
// generic ioq3/Q3/CNQ3 patterns that would otherwise misclassify it as
// plain Quake 3 Arena.
function guessGameFromVersion(raw) {
  if (!raw) return null;
  const v = raw.toLowerCase();
  if (v.startsWith('iortcw') || v.startsWith('wolf ') || v.startsWith('rtcw')) return 'rtcw';
  if (v.startsWith('et ') || v.startsWith('etlegacy') || v.startsWith('et legacy')) return 'et';
  if (v.startsWith('ioq3+oa') || v.startsWith('openarena')) return 'oa';
  if (v.startsWith('q3 ') || v.startsWith('ioq3') || v.startsWith('cnq3')) return 'q3a';
  return null;
}

function getGameKey(server) {
  const info = PROTOCOL_INFO[server.protocol];
  if (info) return info.game;
  return guessGameFromVersion(server.version) || 'unknown';
}
function getGameLabel(server) {
  return GAME_LABELS[getGameKey(server)] || 'Unknown';
}

// RTCW's protocol number maps 1:1 to a real, distinct client build, so it
// gets a clean static label. Every other game here turned out to have real
// version diversity *within* one protocol number once actual live servers
// were polled directly: ET 2.60b/2.60d retail and ET Legacy (v2.83/2.84)
// all share stock protocol 84; ET-mod stacks (silEnT/Jaymod/etc, all
// self-reporting "ET 3.00") share protocol 82; vanilla Quake 3 1.32c/1.32e,
// ioquake3 1.36 builds, and CNQ3 1.53 all share protocol 68; various
// ioquake3+OpenArena builds share protocol 71. So rather than guess one
// label per protocol, every game except RTCW gets its version parsed from
// each server's own self-reported getstatus "version" string.
const STATIC_VERSIONS = { 57: '1.0', 60: '1.4' };

// Parses a concise "engine version" out of a raw getstatus version string,
// e.g. "Q3 1.32e linux-x86_64 Aug 12 2026" -> "Q3 1.32e", "ioq3
// 1.36_gca3c155c linux-x86_64 ..." -> "ioq3 1.36". Distro-packaged builds
// (Debian/Ubuntu ioquake3) tack on a "+u<date>.<hash>[+~]dfsg-<rev>/<Distro>"
// suffix instead of the git-build "_<hash>" form -- splitting on the first
// of _, +, ~, or / (whichever comes first) catches both, so e.g.
// "ioq3 1.36+u20240217.7d711f8+dfsg-1build2/Ubuntu" and "ioq3
// 1.36_gca3c155c" both collapse to "ioq3 1.36" instead of the filter
// showing a pile of near-duplicate one-off entries. Telling engine forks
// apart (Q3 vs ioq3 vs CNQ3 vs ET Legacy) is the point; exact build
// hashes/packaging metadata aren't useful in a compact badge/filter.
function parseVersionFromRaw(raw) {
  if (!raw) return null;
  const parts = raw.trim().split(/\s+/);
  if (parts.length === 0 || !parts[0]) return null;
  if (parts.length === 1) return parts[0];
  return parts[0] + ' ' + parts[1].split(/[_+~/]/)[0];
}

// Version label for a server: static for RTCW, parsed from the server's
// own reported version otherwise (see STATIC_VERSIONS comment above).
function getVersionLabel(server) {
  if (STATIC_VERSIONS[server.protocol]) return STATIC_VERSIONS[server.protocol];
  return parseVersionFromRaw(server.version) || ('protocol ' + server.protocol);
}
// Combined "Game Version" string, for places that want one label rather
// than separate game/version badges (tooltips, the per-server detail page).
function getProtocolLabel(server) {
  const game = getGameLabel(server);
  return game === 'Unknown' ? 'Unknown' : game + ' ' + getVersionLabel(server);
}

// Same labels as getGameLabel, but for the game-filter's string values
// ("all", "rtcw", "et", "q3a", "oa") rather than a numeric server.protocol.
function gameFilterLabel(v) {
  return v === 'all' ? 'All' : (GAME_LABELS[v] || v);
}

// Builds a <select>'s options from live server data: groups servers by
// keyFn (scoped to the selected game if one is), counts occurrences, most
// common first. Used for both the version and mod filters, since neither
// has a fixed, curatable list of possible values -- versions and mods are
// both effectively open-ended, self-reported strings.
function computeFilterOptions(data, game, keyFn, labelFn) {
  const counts = new Map(); // key -> { label, count }
  data.forEach(s => {
    if (game !== 'all' && getGameKey(s) !== game) return;
    const key = keyFn(s);
    if (key === null || key === undefined || key === '') return;
    const entry = counts.get(key) || { label: labelFn(s), count: 0 };
    entry.count++;
    counts.set(key, entry);
  });
  return [...counts.entries()]
    .map(([key, v]) => ({ key, label: v.label, count: v.count }))
    .sort((a, b) => b.count - a.count);
}

// Lists sources (master labels) a server has been reported by, as small
// pill badges (most recently updated first) rather than a flat comma
// string, so more-than-one source reads as an actual list. The
// "wolfmaster.s4ndmod.com (direct heartbeat)" source -- the server
// heartbeating straight to our own UDP master instead of/alongside being
// found via discovery -- gets its own highlighted style and icon so it
// stands out from "found via querying master X" rather than blending into
// the list. sources is ServerEntry.sources: {label: ISO timestamp}.
function formatSources(sources) {
  const entries = Object.entries(sources || {});
  if (entries.length === 0) return '<em>unknown</em>';
  entries.sort((a, b) => new Date(b[1]) - new Date(a[1]));
  return entries.map(([label, ts]) => {
    const isHeartbeat = label.includes('direct heartbeat');
    const cls = 'source-badge' + (isHeartbeat ? ' source-badge-heartbeat' : '');
    const icon = isHeartbeat ? '<i class="bi bi-broadcast me-1"></i>' : '';
    return `<span class="${cls}" title="last seen: ${formatLastSeen(ts)}">${icon}${label}</span>`;
  }).join('');
}

// Strips the ":port" off an "ip:port" address -- mirrors the Go-side ipOf
// (internal/servers/util.go), used the same way here: port-independent
// same-host comparisons for port-padding classification.
function ipOf(addr) {
  const i = (addr || '').lastIndexOf(':');
  return i === -1 ? (addr || '') : addr.slice(0, i);
}

// Groups server.address + server.also_known_as by IP, returning only the
// IPs with 2+ addresses from this one clone group -- i.e. this specific
// detected "cloned server"'s own port-padding footprint, computed the same
// way GetPortPaddingGroups (clones.go) computes it server-side: every
// address in the group checked against every other, not just aliases
// against the primary (a group's aliases can cluster heavily on some OTHER
// IP than the primary's own -- see clones.go's PortPaddingView doc comment).
// Returns [{ip, addrs: [address, ...]}], biggest cluster first.
function portPaddingIPClusters(server) {
  const all = [server.address].concat((server.also_known_as || []).map(e => e.address));
  const byIP = new Map();
  all.forEach(addr => {
    const ip = ipOf(addr);
    if (!byIP.has(ip)) byIP.set(ip, []);
    byIP.get(ip).push(addr);
  });
  return [...byIP.entries()]
    .filter(([, addrs]) => addrs.length >= 2)
    .map(([ip, addrs]) => ({ ip, addrs }))
    .sort((a, b) => b.addrs.length - a.addrs.length);
}

// Warning-icon badge for a server with at least one same-clone-group IP
// cluster (see portPaddingIPClusters) -- '' if there are none. Links to the
// port-padding dashboard; the native title= tooltip (this project doesn't
// initialize Bootstrap's JS tooltip component anywhere, so plain title= is
// the established pattern) names every clustered IP and its port count.
function portPaddingBadge(server) {
  const clusters = portPaddingIPClusters(server);
  if (clusters.length === 0) return '';
  const totalPadded = clusters.reduce((sum, c) => sum + c.addrs.length, 0);
  const breakdown = clusters.map(c => `${c.ip} (${c.addrs.length})`).join(', ');
  const title = `Port padding: ${totalPadded} ports across ${clusters.length} IP${clusters.length === 1 ? '' : 's'} detected as this same server (${breakdown}) — collapsed into this one entry.`;
  return `<a href="port-padding.html" title="${title}"><i class="bi bi-exclamation-triangle-fill padding-icon ms-1"></i></a>`;
}

// Lists other addresses (see ServerEntry.AlsoKnownAs) detected reporting
// identical map/player content to this server -- almost certainly the same
// physical server broadcasting under multiple ports/IPs, sometimes under a
// different hostname too (e.g. region-labeled mirrors of one live match).
// Each entry is {address, hostname} -- hostname is a one-time snapshot from
// when it was folded in (alias addresses aren't polled again afterward), so
// it's shown alongside the address rather than replacing it. Shown as small
// pill badges, same visual language as formatSources, so it reads as
// "here's the honest picture" rather than a callout. When primaryAddress is
// given, aliases in a padding IP cluster (see portPaddingIPClusters) get the
// amber "padding" style instead of the neutral one, so the distinction
// visible via the card's warning icon carries through to this row too.
function formatAlsoKnownAs(aka, primaryAddress) {
  if (!aka || aka.length === 0) return '';
  let paddingIPs = null;
  if (primaryAddress) {
    paddingIPs = new Set(portPaddingIPClusters({ address: primaryAddress, also_known_as: aka }).map(c => c.ip));
  }
  return aka.map(e => {
    const name = e.hostname ? parseNameColors(e.hostname) : '';
    const isPadding = paddingIPs !== null && paddingIPs.has(ipOf(e.address));
    const cls = 'source-badge' + (isPadding ? ' source-badge-padding' : '');
    return `<span class="${cls}">${e.address}${name ? ` <span class="text-light-emphasis">(${name})</span>` : ''}</span>`;
  }).join('');
}

function formatLastSeen(ts) {
  const d = new Date(ts); if (isNaN(d)) return '—';
  return d.toLocaleString(undefined, { hour: '2-digit', minute: '2-digit', month: 'short', day: 'numeric' });
}
