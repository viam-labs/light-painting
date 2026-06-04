// Self-contained, dependency-free image -> path tracer.
//
// Pipeline: downscale -> grayscale -> Sobel edge magnitude -> threshold ->
// walk connected edge pixels into polylines -> Ramer-Douglas-Peucker simplify
// -> normalize to [0,1] image space. The result is a set of strokes the arm
// can follow continuously (light on per stroke, lifted between strokes).

export interface Pt {
  u: number;
  v: number;
}

export interface Stroke {
  points: Pt[];
}

export interface TraceOptions {
  maxDim: number; // working resolution (longest side, px)
  threshold: number; // edge magnitude threshold (0-255)
  minStroke: number; // drop strokes with fewer points than this
  simplifyPx: number; // RDP tolerance in working-resolution pixels
}

export interface TraceResult {
  strokes: Stroke[];
  width: number; // working-resolution width
  height: number; // working-resolution height
}

const NEIGHBORS: [number, number][] = [
  [1, 0],
  [1, 1],
  [0, 1],
  [-1, 1],
  [-1, 0],
  [-1, -1],
  [0, -1],
  [1, -1],
];

export function traceImage(
  img: HTMLImageElement,
  opts: TraceOptions,
): TraceResult {
  const scale = opts.maxDim / Math.max(img.width, img.height);
  const w = Math.max(1, Math.round(img.width * scale));
  const h = Math.max(1, Math.round(img.height * scale));

  const canvas = document.createElement("canvas");
  canvas.width = w;
  canvas.height = h;
  const ctx = canvas.getContext("2d");
  if (!ctx) throw new Error("could not get 2d canvas context");
  ctx.drawImage(img, 0, 0, w, h);
  const data = ctx.getImageData(0, 0, w, h).data;

  // Grayscale luminance.
  const gray = new Float32Array(w * h);
  for (let i = 0; i < w * h; i++) {
    const r = data[i * 4];
    const g = data[i * 4 + 1];
    const b = data[i * 4 + 2];
    gray[i] = 0.299 * r + 0.587 * g + 0.114 * b;
  }

  // Sobel gradient magnitude, thresholded to a binary edge map.
  const edge = new Uint8Array(w * h);
  for (let y = 1; y < h - 1; y++) {
    for (let x = 1; x < w - 1; x++) {
      const gx =
        -gray[(y - 1) * w + (x - 1)] +
        gray[(y - 1) * w + (x + 1)] +
        -2 * gray[y * w + (x - 1)] +
        2 * gray[y * w + (x + 1)] +
        -gray[(y + 1) * w + (x - 1)] +
        gray[(y + 1) * w + (x + 1)];
      const gy =
        -gray[(y - 1) * w + (x - 1)] -
        2 * gray[(y - 1) * w + x] -
        gray[(y - 1) * w + (x + 1)] +
        gray[(y + 1) * w + (x - 1)] +
        2 * gray[(y + 1) * w + x] +
        gray[(y + 1) * w + (x + 1)];
      const mag = Math.hypot(gx, gy);
      edge[y * w + x] = mag >= opts.threshold ? 1 : 0;
    }
  }

  // Walk connected edge pixels into polylines.
  const visited = new Uint8Array(w * h);
  const strokes: Stroke[] = [];
  // Normalize each axis independently so (u,v) span [0,1] regardless of aspect
  // ratio (dividing both by max(w,h) squishes the shorter axis). Image aspect is
  // re-applied later by letterbox() when mapping onto the drawing plane.
  const du = Math.max(1, w - 1);
  const dv = Math.max(1, h - 1);

  for (let y = 0; y < h; y++) {
    for (let x = 0; x < w; x++) {
      const start = y * w + x;
      if (!edge[start] || visited[start]) continue;

      const px: [number, number][] = [];
      let cx = x;
      let cy = y;
      while (true) {
        const ci = cy * w + cx;
        visited[ci] = 1;
        px.push([cx, cy]);

        let next: [number, number] | null = null;
        for (const [dx, dy] of NEIGHBORS) {
          const nx = cx + dx;
          const ny = cy + dy;
          if (nx < 0 || ny < 0 || nx >= w || ny >= h) continue;
          const ni = ny * w + nx;
          if (edge[ni] && !visited[ni]) {
            next = [nx, ny];
            break;
          }
        }
        if (!next) break;
        cx = next[0];
        cy = next[1];
      }

      if (px.length < opts.minStroke) continue;
      const simplified = rdp(px, opts.simplifyPx);
      strokes.push({
        points: simplified.map(([sx, sy]) => ({
          u: sx / du,
          v: sy / dv,
        })),
      });
    }
  }

  return { strokes, width: w, height: h };
}

export interface SvgTraceOptions {
  simplifyPx: number; // RDP tolerance (scaled into normalized space)
  minStroke: number; // drop strokes with fewer points
  sampleStep?: number; // sampling step along each path, in viewBox units
  includeRect?: boolean; // include <rect> elements (often backgrounds)
}

// traceSvg samples an SVG's geometry elements directly into stroke polylines —
// near-perfect trajectories, no edge detection. Each path/shape is walked with
// the browser's getPointAtLength()/getCTM() (which honors curves, arcs and
// ancestor transforms), mapped into the viewBox, normalized to [0,1], then
// simplified. Requires a DOM (runs in the browser).
export function traceSvg(svgText: string, opts: SvgTraceOptions): TraceResult {
  const holder = document.createElement("div");
  holder.setAttribute(
    "style",
    "position:absolute;left:-99999px;top:0;width:0;height:0;overflow:hidden",
  );
  holder.innerHTML = svgText;
  const svg = holder.querySelector("svg") as SVGSVGElement | null;
  if (!svg) throw new Error("no <svg> root element");
  document.body.appendChild(holder);

  try {
    // Determine the user-space bounds (viewBox, else width/height, else bbox).
    let vbX = 0, vbY = 0, vbW = 0, vbH = 0;
    const vb = svg.getAttribute("viewBox");
    if (vb) {
      const p = vb.split(/[\s,]+/).map(Number);
      [vbX, vbY, vbW, vbH] = [p[0], p[1], p[2], p[3]];
    } else {
      try {
        const bb = svg.getBBox();
        vbX = bb.x; vbY = bb.y; vbW = bb.width; vbH = bb.height;
      } catch {
        /* ignore */
      }
    }
    if (!vbW || !vbH) throw new Error("could not determine SVG bounds");

    const selector = `path,line,polyline,polygon,circle,ellipse${opts.includeRect ? ",rect" : ""}`;
    const els = svg.querySelectorAll(selector);
    // getPointAtLength is O(path segments) per call, so keep the sample count
    // bounded — RDP recovers the shape from a few hundred samples.
    const diag = Math.max(vbW, vbH);
    const step = opts.sampleStep ?? Math.max(1, diag / 400);
    const eps = opts.simplifyPx * 0.004; // RDP tolerance in normalized units
    const strokes: Stroke[] = [];

    // First pass: sample every element into raw sub-strokes (in viewport coords
    // via getCTM, which includes Inkscape's group transforms), splitting at
    // subpath gaps, and track the overall bounding box.
    const rawStrokes: [number, number][][] = [];
    let minX = Infinity, minY = Infinity, maxX = -Infinity, maxY = -Infinity;

    els.forEach((node) => {
      const geo = node as SVGGeometryElement;
      let len = 0;
      try {
        len = geo.getTotalLength();
      } catch {
        return; // not a geometry element we can sample
      }
      if (!isFinite(len) || len <= 0) return;

      const ctm = geo.getCTM();
      const n = Math.max(16, Math.min(900, Math.ceil(len / step)));
      // A path can contain multiple disconnected subpaths (e.g. a letter's outer
      // and inner contour). getPointAtLength walks them as one continuous length,
      // so a large jump between consecutive samples marks a subpath boundary —
      // split there instead of drawing a straight line across the gap.
      const jumpThresh = (len / n) * 4;

      let sub: [number, number][] = [];
      let px = 0, py = 0, havePrev = false;
      const flush = () => {
        if (sub.length >= 2) rawStrokes.push(sub);
        sub = [];
      };
      for (let i = 0; i <= n; i++) {
        let pt = geo.getPointAtLength((len * i) / n);
        if (ctm) pt = pt.matrixTransform(ctm);
        if (havePrev && Math.hypot(pt.x - px, pt.y - py) > jumpThresh) flush();
        sub.push([pt.x, pt.y]);
        if (pt.x < minX) minX = pt.x;
        if (pt.x > maxX) maxX = pt.x;
        if (pt.y < minY) minY = pt.y;
        if (pt.y > maxY) maxY = pt.y;
        px = pt.x;
        py = pt.y;
        havePrev = true;
      }
      flush();
    });

    // Normalize by the content bounding box so the drawing fills [0,1] with the
    // right aspect, regardless of viewBox/transform quirks.
    const w = maxX - minX;
    const h = maxY - minY;
    if (!(w > 0) || !(h > 0)) throw new Error("traced SVG has no extent");
    for (const sub of rawStrokes) {
      const norm = sub.map(
        ([x, y]) => [(x - minX) / w, (y - minY) / h] as [number, number],
      );
      const simp = rdp(norm, eps);
      if (simp.length >= opts.minStroke) {
        strokes.push({ points: simp.map(([u, v]) => ({ u, v })) });
      }
    }

    return { strokes, width: w, height: h };
  } finally {
    document.body.removeChild(holder);
  }
}

// Ramer-Douglas-Peucker polyline simplification.
function rdp(points: [number, number][], eps: number): [number, number][] {
  if (points.length < 3 || eps <= 0) return points;
  let maxDist = 0;
  let idx = 0;
  const [ax, ay] = points[0];
  const [bx, by] = points[points.length - 1];
  for (let i = 1; i < points.length - 1; i++) {
    const d = perpDist(points[i], ax, ay, bx, by);
    if (d > maxDist) {
      maxDist = d;
      idx = i;
    }
  }
  if (maxDist > eps) {
    const left = rdp(points.slice(0, idx + 1), eps);
    const right = rdp(points.slice(idx), eps);
    return left.slice(0, -1).concat(right);
  }
  return [points[0], points[points.length - 1]];
}

function perpDist(
  p: [number, number],
  ax: number,
  ay: number,
  bx: number,
  by: number,
): number {
  const dx = bx - ax;
  const dy = by - ay;
  const len = Math.hypot(dx, dy);
  if (len === 0) return Math.hypot(p[0] - ax, p[1] - ay);
  return Math.abs((p[0] - ax) * dy - (p[1] - ay) * dx) / len;
}

// Fit normalized image strokes into a plane of the given aspect (w/h),
// preserving image aspect and centering (letterbox). imgAspect = imgW/imgH.
export function letterbox(
  strokes: Stroke[],
  imgAspect: number,
  planeAspect: number,
): Stroke[] {
  let scaleU = 1;
  let scaleV = 1;
  if (imgAspect > planeAspect) {
    // Image is wider than the plane: fit to width, letterbox top/bottom.
    scaleV = planeAspect / imgAspect;
  } else {
    // Image is taller: fit to height, letterbox left/right.
    scaleU = imgAspect / planeAspect;
  }
  const offU = (1 - scaleU) / 2;
  const offV = (1 - scaleV) / 2;
  return strokes.map((s) => ({
    points: s.points.map((p) => ({
      u: offU + p.u * scaleU,
      v: offV + p.v * scaleV,
    })),
  }));
}
