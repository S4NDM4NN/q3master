// Shared helpers for index.html and server.html: canvas sparkline charting
// and Q3/ET name-color + formatting utilities.

function debounce(fn, wait = 150) {
  let t;
  return (...args) => { clearTimeout(t); t = setTimeout(() => fn(...args), wait); };
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

  const xs = points.map(p => p.x), ys = points.map(p => p.y);
  const minX = Math.min(...xs), maxX = Math.max(...xs);
  const maxY = Math.max(1, ...ys);
  const pad = 4;
  const xScale = x => pad + (w - 2 * pad) * (maxX === minX ? 0.5 : (x - minX) / (maxX - minX));
  const yScale = y => (h - pad) - (h - 2 * pad) * (y / maxY);

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
      ctx.lineTo(xScale(segMaxX), h - pad);
      ctx.lineTo(xScale(segMinX), h - pad);
      ctx.closePath();
      ctx.fillStyle = color + '26';
      ctx.fill();
    }
  }

  // Peak marker: a dot on the highest point. The readout showing what the
  // peak actually *is* goes in a fixed top-left corner rather than floating
  // next to the dot -- a floating label would land wherever the peak
  // happens to fall in the series, which frequently collides with the
  // top-right "current value" readout below (e.g. when the count is still
  // climbing and the peak is also the most recent point). A fixed corner
  // mirrors that current-value readout and never collides with it,
  // regardless of chart size, so it works for the small card/network
  // sparklines too, not just the roomier detail-page chart.
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

  // opts.peakLabel adds the peak's timestamp too -- used on the roomier
  // detail-page chart where there's room for it.
  const peakText = opts.peakLabel
    ? `peak ${Math.round(peak.y)} · ${formatLastSeen(peak.x)}`
    : `peak ${Math.round(peak.y)}`;
  ctx.font = '11px monospace';
  const peakTextW = ctx.measureText(peakText).width;
  ctx.fillStyle = '#0a0a0a';
  ctx.fillRect(pad - 3, 1, peakTextW + 6, 14);
  ctx.fillStyle = '#ccc';
  ctx.textAlign = 'left';
  ctx.fillText(peakText, pad, 12);

  const last = points[points.length - 1];
  ctx.fillStyle = '#ccc';
  ctx.font = '11px monospace';
  ctx.textAlign = 'right';
  ctx.fillText(String(Math.round(last.y)), w - 4, 12);
  ctx.textAlign = 'left';
}

// Q3/ET color parsing
const colorMap = {
  '0': '#FFFFFF', '1': '#FF0000', '2': '#00FF00', '3': '#FFFF00',
  '4': '#0000FF', '5': '#00FFFF', '6': '#FF00FF', '7': '#FFFFFF',
  '8': '#808080', '9': '#000000',
  'a': '#FF7F00', 'b': '#7FFF00', 'c': '#007FFF', 'd': '#7F00FF', 'e': '#FF007F', 'f': '#00FF7F',
  'g': '#FFAA00', 'h': '#AAFF00', 'i': '#00AAFF', 'j': '#AA00FF', 'k': '#FF00AA', 'l': '#00FFAA',
  'm': '#DDDDDD', 'n': '#AAAAAA', 'o': '#777777', 'p': '#444444', 'q': '#222222',
  'r': '#FFAACC', 's': '#AAFFCC', 't': '#CCAAFF', 'u': '#FFCCAA', 'v': '#CCFFAA',
  'w': '#AACCAA', 'x': '#AACCFF', 'y': '#CCAACC', 'z': '#AACCBB',
  'A': '#FF7F00', 'B': '#7FFF00', 'C': '#007FFF', 'D': '#7F00FF', 'E': '#FF007F', 'F': '#00FF7F',
  'G': '#FFAA00', 'H': '#AAFF00', 'I': '#00AAFF', 'J': '#AA00FF', 'K': '#FF00AA', 'L': '#00FFAA',
  'M': '#DDDDDD', 'N': '#AAAAAA', 'O': '#777777', 'P': '#444444', 'Q': '#222222',
  'R': '#FFAACC', 'S': '#AAFFCC', 'T': '#CCAAFF', 'U': '#FFCCAA', 'V': '#CCFFAA',
  'W': '#AACCAA', 'X': '#AACCFF', 'Y': '#CCAACC', 'Z': '#AACCBB'
};
function parseNameColors(name) {
  let out = '', color = 'white';
  for (let i = 0; i < name.length; i++) {
    if (name[i] === '^' && i + 1 < name.length && colorMap.hasOwnProperty(name[i + 1])) {
      color = colorMap[name[++i]]; continue;
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
