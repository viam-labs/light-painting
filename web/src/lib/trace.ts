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
  const denom = Math.max(1, Math.max(w, h) - 1);

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
          u: sx / denom,
          v: sy / denom,
        })),
      });
    }
  }

  return { strokes, width: w, height: h };
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
