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
// derived from the median spacing (not a fixed constant) so it adapts
// whether points are raw samples (~minutes apart), hourly, or daily rollups.
function splitOnGaps(points) {
  if (points.length < 3) return [points];

  const deltas = [];
  for (let i = 1; i < points.length; i++) deltas.push(points[i].x - points[i - 1].x);
  const sorted = [...deltas].sort((a, b) => a - b);
  const median = sorted[Math.floor(sorted.length / 2)];
  const floorMs = 10 * 60 * 1000;
  const threshold = Math.max(3 * median, floorMs);

  const segments = [];
  let current = [points[0]];
  for (let i = 1; i < points.length; i++) {
    if (points[i].x - points[i - 1].x > threshold) {
      segments.push(current);
      current = [];
    }
    current.push(points[i]);
  }
  segments.push(current);
  return segments;
}

function drawSparkline(canvas, points, opts = {}) {
  if (!canvas) return;
  const { ctx, w, h } = fitCanvas(canvas);
  ctx.clearRect(0, 0, w, h);

  if (!points || points.length === 0) {
    ctx.fillStyle = '#777';
    ctx.font = '11px monospace';
    ctx.fillText('No data yet', 8, h / 2 + 4);
    return;
  }

  // The peak/current-value readouts get their own header strip above the
  // plot instead of floating on top of it -- text drawn inside the plot
  // area inevitably collides with the line/fill whenever a value is near
  // the top of the chart's range, which is often (that's what "peak"
  // means). Reserving space up front means the chart itself never has
  // anything drawn under those labels, so there's nothing to collide with.
  const headerH = 14;
  const xs = points.map(p => p.x), ys = points.map(p => p.y);
  const minX = Math.min(...xs), maxX = Math.max(...xs);
  const maxY = Math.max(1, ...ys);
  const pad = 4;
  const plotTop = headerH + 3;
  const plotBottom = h - pad;
  const xScale = x => pad + (w - 2 * pad) * (maxX === minX ? 0.5 : (x - minX) / (maxX - minX));
  const yScale = y => plotBottom - (plotBottom - plotTop) * (y / maxY);

  const color = opts.color || '#0dcaf0';

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
      const segMaxX = segment[segment.length - 1].x, segMinX = segment[0].x;
      ctx.lineTo(xScale(segMaxX), plotBottom);
      ctx.lineTo(xScale(segMinX), plotBottom);
      ctx.closePath();
      ctx.fillStyle = color + '26';
      ctx.fill();
    }
  }

  // Peak marker: a dot on the highest point.
  let peak = points[0];
  for (const p of points) if (p.y > peak.y) peak = p;
  const px = xScale(peak.x), py = yScale(peak.y);
  ctx.beginPath();
  ctx.arc(px, py, 3, 0, Math.PI * 2);
  ctx.fillStyle = '#fff';
  ctx.strokeStyle = color;
  ctx.lineWidth = 1.5;
  ctx.fill();
  ctx.stroke();

  // Header strip: peak on the left (with timestamp too when opts.peakLabel
  // is set -- used on the roomier detail-page chart), current value on the
  // right.
  const peakText = opts.peakLabel
    ? `peak ${Math.round(peak.y)} · ${formatLastSeen(peak.x)}`
    : `peak ${Math.round(peak.y)}`;
  const last = points[points.length - 1];
  ctx.font = '11px monospace';
  ctx.fillStyle = '#999';
  ctx.textAlign = 'left';
  ctx.fillText(peakText, pad, 11);
  ctx.fillStyle = '#ccc';
  ctx.textAlign = 'right';
  ctx.fillText(String(Math.round(last.y)), w - pad, 11);
  ctx.textAlign = 'left';
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

// Lists other addresses (see ServerEntry.AlsoKnownAs) detected reporting
// identical map/player content to this server -- almost certainly the same
// physical server broadcasting under multiple ports/IPs, sometimes under a
// different hostname too (e.g. region-labeled mirrors of one live match).
// Each entry is {address, hostname} -- hostname is a one-time snapshot from
// when it was folded in (alias addresses aren't polled again afterward), so
// it's shown alongside the address rather than replacing it. Shown as small
// pill badges, same visual language as formatSources, so it reads as
// "here's the honest picture" rather than a callout.
function formatAlsoKnownAs(aka) {
  if (!aka || aka.length === 0) return '';
  return aka.map(e => {
    const name = e.hostname ? parseNameColors(e.hostname) : '';
    return `<span class="source-badge">${e.address}${name ? ` <span class="text-light-emphasis">(${name})</span>` : ''}</span>`;
  }).join('');
}

function formatLastSeen(ts) {
  const d = new Date(ts); if (isNaN(d)) return '—';
  return d.toLocaleString(undefined, { hour: '2-digit', minute: '2-digit', month: 'short', day: 'numeric' });
}
