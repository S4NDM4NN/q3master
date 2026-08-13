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
    if (name[i] === '^' && i + 1 < name.length && name[i + 1] !== '^') {
      color = q3ColorForCode(name[++i]); continue;
    }
    out += `<span style="color:${color}">${name[i]}</span>`;
  }
  return out || '&nbsp;';
}

function getProtocolLabel(p) {
  switch (p) { case 57: return 'RTCW 1.0'; case 60: return 'RTCW 1.4'; case 84: return 'ET 2.60b'; default: return 'Unknown'; }
}

// Same labels as getProtocolLabel, but for the protocol-filter's string
// values ("all", "57", "60", "84") rather than a numeric server.protocol.
function protocolFilterLabel(v) {
  return v === 'all' ? 'All' : getProtocolLabel(parseInt(v, 10));
}

function formatLastSeen(ts) {
  const d = new Date(ts); if (isNaN(d)) return '—';
  return d.toLocaleString(undefined, { hour: '2-digit', minute: '2-digit', month: 'short', day: 'numeric' });
}
