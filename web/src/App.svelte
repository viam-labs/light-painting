<script lang="ts">
  import { getCookie, setCookie } from "typescript-cookie";
  import type { RobotClient } from "@viamrobotics/sdk";
  import { connect, Painter, type Credentials } from "./lib/viam";
  import { traceImage, traceSvg, letterbox, type Stroke } from "./lib/trace";

  const VERSION = __APP_VERSION__;

  // ---- connection ----
  let host = $state("");
  let machineId = $state("");
  let client = $state<RobotClient | null>(null);
  let painter = $state<Painter | null>(null);
  let serviceName = $state("painter");
  let connecting = $state(false);
  let error = $state("");
  let log = $state<string[]>([]);

  let machineConfigUrl = $derived(
    machineId ? `https://app.viam.com/machine/${machineId}` : "",
  );

  let formHost = $state("");
  let formKeyId = $state("");
  let formKey = $state("");

  function addLog(msg: string) {
    log = [`${new Date().toLocaleTimeString()}  ${msg}`, ...log].slice(0, 30);
  }

  function discover(): [string, Credentials] | null {
    const parts = window.location.pathname.split("/");
    if (parts.length >= 3 && parts[1] === "machine") {
      machineId = parts[2];
      const cookieValue = getCookie(parts[2]);
      if (cookieValue) {
        const v = JSON.parse(cookieValue);
        return [v.hostname, { type: "api-key", payload: v.key, authEntity: v.id }];
      }
    }
    const saved = getCookie("light-painting-host");
    if (saved) {
      const v = JSON.parse(saved);
      return [v.hostname, { type: "api-key", payload: v.key, authEntity: v.id }];
    }
    return null;
  }

  async function doConnect(h: string, creds: Credentials) {
    connecting = true;
    error = "";
    try {
      const c = await connect(h, creds);
      client = c;
      host = h;
      painter = new Painter(c, serviceName);
      addLog(`connected to ${h}`);
      await refreshPlane();
    } catch (e) {
      error = `connection failed: ${e}`;
    } finally {
      connecting = false;
    }
  }

  function saveAndConnect() {
    if (!formHost || !formKeyId || !formKey) {
      error = "host, key id and key are all required";
      return;
    }
    setCookie("light-painting-host", JSON.stringify({ hostname: formHost, key: formKey, id: formKeyId }));
    doConnect(formHost, { type: "api-key", payload: formKey, authEntity: formKeyId });
  }

  const found = discover();
  if (found) doConnect(found[0], found[1]);

  // ---- plane ----
  let plane = $state({
    origin: { x: 300, y: 150, z: 500 },
    width_mm: 300,
    height_mm: 300,
    approach: { x: 1, y: 0, z: 0 },
    up: { x: 0, y: 0, z: 1 },
    mirror: false,
  });
  let planeAspect = $derived(plane.width_mm / plane.height_mm);

  async function refreshPlane() {
    if (!painter) return;
    try {
      const p = (await painter.getPlane()) as any;
      plane = {
        origin: p.origin,
        width_mm: p.width_mm,
        height_mm: p.height_mm,
        approach: p.approach ?? { x: 1, y: 0, z: 0 },
        up: p.up ?? { x: 0, y: 0, z: 1 },
        mirror: p.mirror ?? false,
      };
      addLog(`plane ${plane.width_mm}×${plane.height_mm}mm`);
    } catch (e) {
      addLog(`get_plane failed: ${e}`);
    }
  }

  async function applyPlane() {
    if (!painter) return;
    try {
      await painter.setPlane(plane);
      addLog("plane updated");
    } catch (e) {
      addLog(`set_plane failed: ${e}`);
    }
  }

  // ---- image ----
  let img = $state<HTMLImageElement | null>(null);
  let imgName = $state("");
  let imgAspect = $state(1);
  let isSvg = $state(false);
  let svgText = $state("");
  let previewCanvas: HTMLCanvasElement | undefined = $state();

  function onFile(e: Event) {
    const file = (e.target as HTMLInputElement).files?.[0];
    if (!file) return;
    imgName = file.name;
    isSvg = file.name.toLowerCase().endsWith(".svg") || file.type === "image/svg+xml";
    const image = new Image();
    image.onload = () => {
      img = image;
      if (!isSvg) imgAspect = image.width / image.height;
    };
    if (isSvg) {
      // Keep the raw SVG for vector tracing; load a rendered copy for the preview.
      file.text().then((t) => {
        svgText = t;
        image.src = URL.createObjectURL(file);
      });
    } else {
      svgText = "";
      image.src = URL.createObjectURL(file);
    }
  }

  // ---- layers ----
  type ColorMode = "solid" | "rainbow";
  interface Layer {
    id: number;
    name: string;
    enabled: boolean;
    threshold: number;
    maxDim: number;
    simplifyPx: number;
    minStroke: number;
    mode: ColorMode;
    color: string;
    intensity: number;
    strokes: Stroke[];
  }

  let nextId = 1;
  function makeLayer(over: Partial<Layer> = {}): Layer {
    return {
      id: nextId++,
      name: `layer ${nextId - 1}`,
      enabled: true,
      threshold: 80,
      maxDim: 180,
      simplifyPx: 1.5,
      minStroke: 6,
      mode: "solid",
      color: "#ff36c2",
      intensity: 1,
      strokes: [],
      ...over,
    };
  }

  let layers = $state<Layer[]>([makeLayer({ name: "layer 1" })]);

  function addLayer() {
    layers = [...layers, makeLayer()];
  }
  function removeLayer(id: number) {
    layers = layers.filter((l) => l.id !== id);
  }

  // Live-trace every layer (debounced) whenever the image or any trace control
  // changes — no Trace button.
  let traceTimer: ReturnType<typeof setTimeout> | undefined;
  $effect(() => {
    // Track image/svg + each layer's trace settings.
    void [img, svgText];
    for (const l of layers) void [l.threshold, l.maxDim, l.simplifyPx, l.minStroke];
    clearTimeout(traceTimer);
    traceTimer = setTimeout(traceAll, 120);
    return () => clearTimeout(traceTimer);
  });

  // Redraw the preview when colors/modes/intensity/enable change.
  $effect(() => {
    for (const l of layers) void [l.color, l.mode, l.intensity, l.enabled];
    drawPreview();
  });

  function traceAll() {
    if (isSvg) {
      if (!svgText) return;
      let aspectSet = false;
      for (const l of layers) {
        const res = traceSvg(svgText, { simplifyPx: l.simplifyPx, minStroke: l.minStroke });
        l.strokes = res.strokes;
        if (!aspectSet && res.height > 0) {
          imgAspect = res.width / res.height;
          aspectSet = true;
        }
      }
      addLog(`traced SVG · ${layers.length} layer(s)`);
      drawPreview();
      return;
    }
    if (!img) return;
    for (const l of layers) {
      l.strokes = traceImage(img, {
        maxDim: l.maxDim,
        threshold: l.threshold,
        minStroke: l.minStroke,
        simplifyPx: l.simplifyPx,
      }).strokes;
    }
    addLog(`traced ${layers.length} layer(s)`);
    drawPreview();
  }

  // ---- color helpers ----
  function hexToRgb(hex: string) {
    const n = parseInt(hex.replace("#", ""), 16);
    return { r: (n >> 16) & 255, g: (n >> 8) & 255, b: n & 255 };
  }
  function scaleRgb(c: { r: number; g: number; b: number }, k: number) {
    return { r: Math.round(c.r * k), g: Math.round(c.g * k), b: Math.round(c.b * k) };
  }
  // h in [0,1], s/v in [0,1] -> 0-255 rgb
  function hsvToRgb(h: number, s: number, v: number) {
    const i = Math.floor(h * 6);
    const f = h * 6 - i;
    const p = v * (1 - s);
    const q = v * (1 - f * s);
    const t = v * (1 - (1 - f) * s);
    let r = 0, g = 0, b = 0;
    switch (i % 6) {
      case 0: r = v; g = t; b = p; break;
      case 1: r = q; g = v; b = p; break;
      case 2: r = p; g = v; b = t; break;
      case 3: r = p; g = q; b = v; break;
      case 4: r = t; g = p; b = v; break;
      case 5: r = v; g = p; b = q; break;
    }
    return { r: Math.round(r * 255), g: Math.round(g * 255), b: Math.round(b * 255) };
  }
  const rgbCss = (c: { r: number; g: number; b: number }) => `rgb(${c.r},${c.g},${c.b})`;

  function pointColor(layer: Layer, i: number, n: number) {
    if (layer.mode === "rainbow") {
      return hsvToRgb(n > 1 ? i / (n - 1) : 0, 1, layer.intensity);
    }
    return scaleRgb(hexToRgb(layer.color), layer.intensity);
  }

  // ---- preview ----
  function drawPreview() {
    const c = previewCanvas;
    if (!c) return;
    const W = 520;
    const H = img ? Math.round(W / imgAspect) : 300;
    c.width = W;
    c.height = H;
    const ctx = c.getContext("2d");
    if (!ctx) return;
    ctx.clearRect(0, 0, W, H);
    if (img) {
      ctx.globalAlpha = 0.38;
      ctx.drawImage(img, 0, 0, W, H);
      ctx.globalAlpha = 1;
    }
    ctx.lineWidth = 1.4;
    ctx.lineJoin = "round";
    for (const layer of layers) {
      if (!layer.enabled) continue;
      for (const s of layer.strokes) {
        const n = s.points.length;
        for (let i = 1; i < n; i++) {
          const a = s.points[i - 1], b = s.points[i];
          ctx.strokeStyle = rgbCss(pointColor(layer, i, n));
          ctx.shadowColor = ctx.strokeStyle;
          ctx.shadowBlur = 6;
          ctx.beginPath();
          ctx.moveTo(a.u * W, a.v * H);
          ctx.lineTo(b.u * W, b.v * H);
          ctx.stroke();
        }
      }
    }
    ctx.shadowBlur = 0;
  }

  let totalStrokes = $derived(
    layers.filter((l) => l.enabled).reduce((n, l) => n + l.strokes.length, 0),
  );

  // ---- paint ----
  let painting = $state(false);

  async function paint() {
    if (!painter || totalStrokes === 0) return;
    painting = true;
    try {
      const payload: any[] = [];
      for (const layer of layers) {
        if (!layer.enabled || layer.strokes.length === 0) continue;
        const mapped = letterbox(layer.strokes, imgAspect, planeAspect);
        for (const s of mapped) {
          const n = s.points.length;
          if (layer.mode === "rainbow") {
            payload.push({
              points: s.points.map((p, i) => ({ u: p.u, v: p.v, color: pointColor(layer, i, n) })),
            });
          } else {
            payload.push({ points: s.points, color: pointColor(layer, 0, n) });
          }
        }
      }
      addLog(`exposing ${payload.length} stroke(s) across ${layers.filter((l) => l.enabled).length} layer(s)…`);
      const res = (await painter.paintPath(payload)) as any;
      addLog(`done · ${res.strokes} strokes / ${res.points} points`);
    } catch (e) {
      addLog(`paint failed: ${e}`);
    } finally {
      painting = false;
    }
  }

  async function home() {
    if (!painter) return;
    try { await painter.home(); addLog("homed"); } catch (e) { addLog(`home failed: ${e}`); }
  }
  async function stop() {
    if (!painter) return;
    try { await painter.stop(); addLog("stop sent"); } catch (e) { addLog(`stop failed: ${e}`); }
  }
  async function clearVisuals() {
    if (!painter) return;
    try { await painter.clearVisuals(); addLog("cleared visuals"); } catch (e) { addLog(`clear failed: ${e}`); }
  }

  // ---- import / export ----
  function exportSettings() {
    const data = {
      version: VERSION,
      plane,
      layers: layers.map(({ strokes, id, ...rest }) => rest),
    };
    const blob = new Blob([JSON.stringify(data, null, 2)], { type: "application/json" });
    const a = document.createElement("a");
    a.href = URL.createObjectURL(blob);
    a.download = "light-painting-settings.json";
    a.click();
    URL.revokeObjectURL(a.href);
    addLog("exported settings");
  }

  function importSettings(e: Event) {
    const file = (e.target as HTMLInputElement).files?.[0];
    if (!file) return;
    const reader = new FileReader();
    reader.onload = () => {
      try {
        const data = JSON.parse(reader.result as string);
        if (data.plane) plane = { ...plane, ...data.plane };
        if (Array.isArray(data.layers)) {
          layers = data.layers.map((l: any) => makeLayer(l));
        }
        addLog(`imported ${layers.length} layer(s)`);
        traceAll();
      } catch (err) {
        addLog(`import failed: ${err}`);
      }
    };
    reader.readAsText(file);
  }
</script>

<div class="app">
  <header class="masthead">
    <div class="brand">
      <div class="wordmark">LIGHT<span>PAINTING</span></div>
      <div class="tagline">long-exposure light plotter</div>
    </div>
    <div class="meta">
      <span class="chip">v{VERSION}</span>
      {#if host}<span class="status"><i class="dot" class:live={client}></i>{host}</span>{/if}
      {#if machineConfigUrl}
        <a class="btnlink" href={machineConfigUrl} target="_blank" rel="noreferrer">machine config <span class="arr">↗</span></a>
      {/if}
    </div>
  </header>

  {#if error}<p class="error">{error}</p>{/if}

  {#if !client}
    <section class="panel connect">
      <div class="phead"><span class="idx">00</span><h2>Connect</h2></div>
      <p class="hint">Served as a Viam Application this fills in automatically. For local development, paste an API key.</p>
      <div class="field"><label for="h">host</label><input id="h" type="text" bind:value={formHost} placeholder="my-machine.abcd.viam.cloud" /></div>
      <div class="field"><label for="i">api key id</label><input id="i" type="text" bind:value={formKeyId} /></div>
      <div class="field"><label for="k">api key</label><input id="k" type="password" bind:value={formKey} /></div>
      <div class="field"><label for="s">service name</label><input id="s" type="text" bind:value={serviceName} /></div>
      <button class="primary" onclick={saveAndConnect} disabled={connecting}>{connecting ? "connecting…" : "connect"}</button>
    </section>
  {:else}
    <div class="grid">
      <section class="panel">
        <div class="phead"><span class="idx">01</span><h2>Source</h2>{#if isSvg}<span class="tag">vector</span>{/if}{#if imgName}<span class="tag">{imgName}</span>{/if}</div>
        <label class="filepick"><input type="file" accept="image/*" onchange={onFile} /><span>choose photo</span></label>
        <div class="frame"><canvas bind:this={previewCanvas} class="preview"></canvas></div>
      </section>

      <section class="panel">
        <div class="phead"><span class="idx">02</span><h2>Layers</h2><span class="tag">{layers.length}</span></div>
        <div class="layers">
          {#each layers as layer (layer.id)}
            <div class="layer" class:off={!layer.enabled}>
              <div class="layerhead">
                <label class="en"><input type="checkbox" bind:checked={layer.enabled} /></label>
                <input class="lname" type="text" bind:value={layer.name} />
                {#if layer.mode === "solid"}
                  <input class="swatch" type="color" bind:value={layer.color} />
                {:else}
                  <span class="rainbowchip">🌈</span>
                {/if}
                <span class="cnt">{layer.strokes.length}</span>
                <button class="x" onclick={() => removeLayer(layer.id)} title="remove">✕</button>
              </div>
              <div class="row2">
                <select bind:value={layer.mode}>
                  <option value="solid">solid</option>
                  <option value="rainbow">rainbow</option>
                </select>
                <label class="mini">intensity<b>{Math.round(layer.intensity * 100)}%</b><input type="range" min="0" max="1" step="0.05" bind:value={layer.intensity} /></label>
              </div>
              {#if !isSvg}
                <label class="mini">edge<b>{layer.threshold}</b><input type="range" min="10" max="255" bind:value={layer.threshold} /></label>
                <label class="mini">detail<b>{layer.maxDim}px</b><input type="range" min="60" max="320" step="10" bind:value={layer.maxDim} /></label>
              {/if}
              <label class="mini">simplify<b>{layer.simplifyPx}px</b><input type="range" min="0" max="5" step="0.5" bind:value={layer.simplifyPx} /></label>
              <label class="mini">min stroke<b>{layer.minStroke}</b><input type="range" min="2" max="30" bind:value={layer.minStroke} /></label>
            </div>
          {/each}
        </div>
        <button onclick={addLayer}>+ add layer</button>
      </section>

      <section class="panel">
        <div class="phead"><span class="idx">03</span><h2>Canvas</h2><span class="tag">aspect {planeAspect.toFixed(2)}</span></div>
        <div class="grp"><span class="grplabel">origin (mm)</span>
          <div class="row"><div class="field sm"><label>x</label><input type="number" bind:value={plane.origin.x} /></div><div class="field sm"><label>y</label><input type="number" bind:value={plane.origin.y} /></div><div class="field sm"><label>z</label><input type="number" bind:value={plane.origin.z} /></div></div>
        </div>
        <div class="grp"><span class="grplabel">size (mm)</span>
          <div class="row"><div class="field sm"><label>width</label><input type="number" bind:value={plane.width_mm} /></div><div class="field sm"><label>height</label><input type="number" bind:value={plane.height_mm} /></div></div>
        </div>
        <label class="toggle"><input type="checkbox" bind:checked={plane.mirror} /><span>mirror (paint from the back)</span></label>
        <div class="actions"><button onclick={applyPlane}>apply</button><button onclick={refreshPlane}>reload</button></div>
      </section>

      <section class="panel expose">
        <div class="phead"><span class="idx">04</span><h2>Expose</h2><span class="tag">{totalStrokes} strokes</span></div>
        <button class="primary big" onclick={paint} disabled={painting || totalStrokes === 0}>{painting ? "exposing…" : "▸ paint"}</button>
        <div class="actions">
          <button onclick={home} disabled={painting}>home</button>
          <button class="danger" onclick={stop}>stop</button>
          <button onclick={clearVisuals}>clear visuals</button>
        </div>
        <div class="actions">
          <button onclick={exportSettings}>export ↓</button>
          <label class="importbtn"><input type="file" accept="application/json" onchange={importSettings} />import ↑</label>
        </div>
      </section>
    </div>

    <section class="panel logpanel">
      <div class="phead"><span class="idx">··</span><h2>Capture log</h2></div>
      <ul class="log">{#each log as line}<li>{line}</li>{/each}</ul>
    </section>
  {/if}
</div>

<style>
  .app { position: relative; z-index: 1; max-width: 1180px; margin: 0 auto; padding: 28px 22px 60px; }
  .masthead { display: flex; align-items: flex-end; justify-content: space-between; gap: 20px; flex-wrap: wrap; padding-bottom: 18px; margin-bottom: 26px; border-bottom: 1px solid var(--line); animation: rise 0.6s both; }
  .wordmark { font-family: var(--font-display); font-weight: 900; font-size: clamp(38px, 7vw, 72px); line-height: 0.84; letter-spacing: 0.01em; color: var(--ink); text-shadow: 0 0 26px rgba(255, 206, 74, 0.18); }
  .wordmark span { display: block; color: var(--amber); text-shadow: 0 0 22px rgba(255, 206, 74, 0.45); }
  .tagline { margin-top: 8px; font-size: 11px; letter-spacing: 0.42em; text-transform: uppercase; color: var(--muted); }
  .meta { display: flex; align-items: center; gap: 12px; flex-wrap: wrap; }
  .chip { font-size: 11px; letter-spacing: 0.12em; color: var(--amber); border: 1px solid var(--line); border-radius: 999px; padding: 4px 11px; }
  .status { font-size: 11px; color: var(--muted); display: flex; align-items: center; gap: 7px; }
  .dot { width: 7px; height: 7px; border-radius: 50%; background: #5a5347; }
  .dot.live { background: var(--amber); box-shadow: var(--glow-amber); animation: pulse 2.4s ease-in-out infinite; }
  .btnlink { font-size: 11px; letter-spacing: 0.1em; text-transform: uppercase; text-decoration: none; color: var(--violet); border: 1px solid rgba(166, 132, 255, 0.32); border-radius: 2px; padding: 8px 13px; transition: all 0.18s ease; }
  .btnlink:hover { border-color: var(--violet); box-shadow: 0 0 18px rgba(166, 132, 255, 0.3); }

  .grid { display: grid; grid-template-columns: 1.15fr 1fr; gap: 16px; }
  .panel { position: relative; background: var(--panel); backdrop-filter: blur(6px); border: 1px solid var(--line); border-radius: 4px; padding: 18px; animation: rise 0.6s both; }
  .grid .panel:nth-child(1) { animation-delay: 0.05s; }
  .grid .panel:nth-child(2) { animation-delay: 0.12s; }
  .grid .panel:nth-child(3) { animation-delay: 0.19s; }
  .grid .panel:nth-child(4) { animation-delay: 0.26s; }
  .logpanel { animation-delay: 0.32s; margin-top: 16px; }
  .connect { max-width: 460px; animation-delay: 0.05s; }
  .expose { display: flex; flex-direction: column; }

  .phead { display: flex; align-items: baseline; gap: 12px; margin-bottom: 16px; }
  .idx { font-family: var(--font-display); font-weight: 800; font-size: 22px; color: var(--amber); opacity: 0.85; min-width: 26px; }
  h2 { font-family: var(--font-display); font-weight: 700; font-size: 21px; letter-spacing: 0.04em; text-transform: uppercase; margin: 0; color: var(--ink); }
  .tag { margin-left: auto; font-size: 10px; letter-spacing: 0.1em; text-transform: uppercase; color: var(--muted); border: 1px solid var(--line-soft); border-radius: 999px; padding: 3px 9px; white-space: nowrap; }

  .field { display: flex; flex-direction: column; gap: 5px; margin-bottom: 10px; }
  .field label, .grplabel { font-size: 10px; letter-spacing: 0.14em; text-transform: uppercase; color: var(--muted); }
  .field.sm input { width: 100%; }
  .grp { margin-bottom: 12px; }
  .grplabel { display: block; margin-bottom: 6px; }
  .row { display: grid; grid-template-columns: repeat(3, 1fr); gap: 8px; }
  .grp:nth-of-type(2) .row { grid-template-columns: repeat(2, 1fr); }
  .actions { display: flex; gap: 8px; flex-wrap: wrap; margin-top: 8px; }
  .expose .big { font-size: 15px; padding: 14px; margin-bottom: 10px; letter-spacing: 0.14em; }

  /* layers */
  .layers { display: flex; flex-direction: column; gap: 10px; margin-bottom: 12px; max-height: 460px; overflow-y: auto; }
  .layer { border: 1px solid var(--line); border-radius: 4px; padding: 10px; background: var(--panel-2); transition: opacity 0.2s; }
  .layer.off { opacity: 0.45; }
  .layerhead { display: flex; align-items: center; gap: 8px; margin-bottom: 8px; }
  .en input { accent-color: var(--amber); width: 15px; height: 15px; }
  .lname { flex: 1; min-width: 0; font-size: 12px; padding: 5px 7px; }
  .swatch { width: 30px; height: 24px; padding: 0; border: 1px solid var(--line); border-radius: 4px; background: transparent; cursor: pointer; }
  .rainbowchip { font-size: 16px; }
  .cnt { font-size: 10px; color: var(--muted); min-width: 18px; text-align: right; }
  .x { padding: 4px 8px; font-size: 11px; color: var(--danger); border-color: rgba(255,111,94,0.3); }
  .row2 { display: flex; gap: 8px; align-items: center; margin-bottom: 6px; }
  .row2 select { flex: 0 0 auto; }
  select { font-family: var(--font-mono); font-size: 12px; background: var(--panel-2); color: var(--ink); border: 1px solid var(--line); border-radius: 2px; padding: 6px 8px; }
  .mini { display: flex; align-items: center; gap: 8px; font-size: 10px; letter-spacing: 0.1em; text-transform: uppercase; color: var(--muted); margin-bottom: 5px; }
  .mini b { color: var(--amber); min-width: 40px; }
  .mini input[type="range"] { flex: 1; }
  .row2 .mini { flex: 1; margin: 0; }

  .filepick { display: inline-block; cursor: pointer; }
  .filepick input { display: none; }
  .filepick span { display: inline-block; font-size: 12px; letter-spacing: 0.08em; text-transform: uppercase; color: var(--amber-bright); border: 1px dashed var(--line); border-radius: 2px; padding: 9px 16px; transition: all 0.18s ease; }
  .filepick:hover span { border-color: var(--amber); color: var(--amber); }
  .importbtn { display: inline-flex; align-items: center; font-size: 12px; letter-spacing: 0.08em; text-transform: uppercase; color: var(--amber-bright); border: 1px solid var(--line); border-radius: 2px; padding: 9px 16px; cursor: pointer; transition: all 0.18s ease; }
  .importbtn:hover { border-color: var(--amber); color: var(--amber); }
  .importbtn input { display: none; }

  .frame { margin-top: 14px; border: 1px solid var(--line); border-radius: 3px; background: linear-gradient(rgba(255, 206, 74, 0.015), transparent), repeating-linear-gradient(0deg, rgba(255, 255, 255, 0.02) 0 1px, transparent 1px 22px), repeating-linear-gradient(90deg, rgba(255, 255, 255, 0.02) 0 1px, transparent 1px 22px), #060504; padding: 10px; overflow: hidden; }
  .preview { width: 100%; display: block; border-radius: 2px; }

  .log { list-style: none; margin: 0; padding: 0; font-size: 12px; max-height: 170px; overflow-y: auto; }
  .log li { padding: 3px 0; color: var(--muted); border-bottom: 1px solid var(--line-soft); }
  .log li:first-child { color: var(--amber-bright); }
  .hint { font-size: 11px; line-height: 1.5; color: var(--muted); margin: 10px 0 0; }
  .error { font-size: 12px; color: var(--danger); border: 1px solid rgba(255, 111, 94, 0.3); border-radius: 3px; padding: 10px 12px; }
  .toggle { display: flex; align-items: center; gap: 9px; margin: 2px 0 12px; cursor: pointer; font-size: 11px; letter-spacing: 0.08em; text-transform: uppercase; color: var(--muted); }
  .toggle input { accent-color: var(--amber); width: 15px; height: 15px; cursor: pointer; }

  @keyframes rise { from { opacity: 0; transform: translateY(14px); } to { opacity: 1; transform: translateY(0); } }
  @keyframes pulse { 0%, 100% { opacity: 1; } 50% { opacity: 0.4; } }
  @media (max-width: 760px) { .grid { grid-template-columns: 1fr; } }
</style>
