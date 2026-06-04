<script lang="ts">
  import { getCookie, setCookie } from "typescript-cookie";
  import type { RobotClient } from "@viamrobotics/sdk";
  import { connect, Painter, type Credentials } from "./lib/viam";
  import { traceImage, letterbox, type Stroke } from "./lib/trace";

  const VERSION = __APP_VERSION__;

  // ---- connection state ----
  let host = $state("");
  let machineId = $state("");
  let credentials = $state<Credentials | null>(null);
  let client = $state<RobotClient | null>(null);
  let painter = $state<Painter | null>(null);
  let serviceName = $state("painter");
  let connecting = $state(false);
  let error = $state("");
  let log = $state<string[]>([]);

  let machineConfigUrl = $derived(
    machineId ? `https://app.viam.com/machine/${machineId}` : "",
  );

  // manual connect form (local dev)
  let formHost = $state("");
  let formKeyId = $state("");
  let formKey = $state("");

  function addLog(msg: string) {
    log = [`${new Date().toLocaleTimeString()}  ${msg}`, ...log].slice(0, 30);
  }

  // Discover credentials from the Viam Application cookie (path /machine/<id>),
  // falling back to a manually-saved default.
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
      credentials = creds;
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
    setCookie(
      "light-painting-host",
      JSON.stringify({ hostname: formHost, key: formKey, id: formKeyId }),
    );
    doConnect(formHost, { type: "api-key", payload: formKey, authEntity: formKeyId });
  }


  const found = discover();
  if (found) {
    doConnect(found[0], found[1]);
  }

  // ---- plane state ----
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
      addLog(`plane ${plane.width_mm}×${plane.height_mm}mm @ ${JSON.stringify(plane.origin)}`);
    } catch (e) {
      addLog(`get_plane failed: ${e}`);
    }
  }

  async function applyPlane() {
    if (!painter) return;
    try {
      await painter.setPlane(plane);
      addLog("plane updated");
      drawPreview();
    } catch (e) {
      addLog(`set_plane failed: ${e}`);
    }
  }

  // ---- image + tracing state ----
  let img = $state<HTMLImageElement | null>(null);
  let imgName = $state("");
  let imgAspect = $state(1);
  let strokes = $state<Stroke[]>([]);
  let threshold = $state(80);
  let simplifyPx = $state(1.5);
  let maxDim = $state(180);
  let minStroke = $state(6);
  let previewCanvas: HTMLCanvasElement | undefined = $state();

  function onFile(e: Event) {
    const file = (e.target as HTMLInputElement).files?.[0];
    if (!file) return;
    imgName = file.name;
    const image = new Image();
    image.onload = () => {
      img = image;
      imgAspect = image.width / image.height;
      strokes = [];
      drawPreview();
    };
    image.src = URL.createObjectURL(file);
  }

  function trace() {
    if (!img) return;
    const res = traceImage(img, { maxDim, threshold, minStroke, simplifyPx });
    strokes = res.strokes;
    drawPreview();
  }

  // Live-trace: re-trace (debounced) whenever the image or any trace control
  // changes, so there's no need to click a button.
  let traceTimer: ReturnType<typeof setTimeout> | undefined;
  $effect(() => {
    // Touch the reactive deps so the effect re-runs on any change.
    void [img, threshold, maxDim, simplifyPx, minStroke];
    if (!img) return;
    clearTimeout(traceTimer);
    traceTimer = setTimeout(trace, 120);
    return () => clearTimeout(traceTimer);
  });

  // Redraw the preview overlay when the paint color changes.
  $effect(() => {
    void paintColor;
    drawPreview();
  });

  // Draw the image with the traced strokes overlaid (in image space).
  function drawPreview() {
    const c = previewCanvas;
    if (!c) return;
    const W = 520;
    const H = img ? Math.round(W / imgAspect) : 300;
    c.width = W;
    c.height = H;
    const ctx = c.getContext("2d");
    if (!ctx) return;
    ctx.fillStyle = "#08070500";
    ctx.clearRect(0, 0, W, H);
    if (img) {
      ctx.globalAlpha = 0.42;
      ctx.drawImage(img, 0, 0, W, H);
      ctx.globalAlpha = 1;
    }
    ctx.strokeStyle = paintColor;
    ctx.shadowColor = paintColor;
    ctx.shadowBlur = 7;
    ctx.lineWidth = 1.4;
    ctx.lineJoin = "round";
    for (const s of strokes) {
      ctx.beginPath();
      s.points.forEach((p, i) => {
        const x = p.u * W;
        const y = p.v * H;
        if (i === 0) ctx.moveTo(x, y);
        else ctx.lineTo(x, y);
      });
      ctx.stroke();
    }
    ctx.shadowBlur = 0;
  }

  let painting = $state(false);
  let paintColor = $state("#ff36c2"); // LED / paint color

  function hexToRgb(hex: string) {
    const n = parseInt(hex.replace("#", ""), 16);
    return { r: (n >> 16) & 255, g: (n >> 8) & 255, b: n & 255 };
  }

  async function paint() {
    if (!painter || strokes.length === 0) return;
    painting = true;
    try {
      const rgb = hexToRgb(paintColor);
      const mapped = letterbox(strokes, imgAspect, planeAspect).map((s) => ({
        ...s,
        color: rgb,
      }));
      addLog(`exposing ${mapped.length} stroke(s)…`);
      const res = (await painter.paintPath(mapped)) as any;
      addLog(`done · ${res.strokes} strokes / ${res.points} points`);
    } catch (e) {
      addLog(`paint failed: ${e}`);
    } finally {
      painting = false;
    }
  }

  async function home() {
    if (!painter) return;
    try {
      await painter.home();
      addLog("homed");
    } catch (e) {
      addLog(`home failed: ${e}`);
    }
  }

  async function stop() {
    if (!painter) return;
    try {
      await painter.stop();
      addLog("stop sent");
    } catch (e) {
      addLog(`stop failed: ${e}`);
    }
  }

  async function clearVisuals() {
    if (!painter) return;
    try {
      await painter.clearVisuals();
      addLog("cleared visuals");
    } catch (e) {
      addLog(`clear failed: ${e}`);
    }
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
      {#if host}
        <span class="status"><i class="dot" class:live={client}></i>{host}</span>
      {/if}
      {#if machineConfigUrl}
        <a class="btnlink" href={machineConfigUrl} target="_blank" rel="noreferrer">
          machine config <span class="arr">↗</span>
        </a>
      {/if}
    </div>
  </header>

  {#if error}<p class="error">{error}</p>{/if}

  {#if !client}
    <section class="panel connect">
      <div class="phead"><span class="idx">00</span><h2>Connect</h2></div>
      <p class="hint">
        Served as a Viam Application this fills in automatically. For local development,
        paste an API key.
      </p>
      <div class="field"><label for="h">host</label><input id="h" type="text" bind:value={formHost} placeholder="my-machine.abcd.viam.cloud" /></div>
      <div class="field"><label for="i">api key id</label><input id="i" type="text" bind:value={formKeyId} /></div>
      <div class="field"><label for="k">api key</label><input id="k" type="password" bind:value={formKey} /></div>
      <div class="field"><label for="s">service name</label><input id="s" type="text" bind:value={serviceName} /></div>
      <button class="primary" onclick={saveAndConnect} disabled={connecting}>
        {connecting ? "connecting…" : "connect"}
      </button>
    </section>
  {:else}
    <div class="grid">
      <section class="panel">
        <div class="phead"><span class="idx">01</span><h2>Source</h2>{#if imgName}<span class="tag">{imgName}</span>{/if}</div>
        <label class="filepick">
          <input type="file" accept="image/*" onchange={onFile} />
          <span>choose photo</span>
        </label>
        <div class="frame"><canvas bind:this={previewCanvas} class="preview"></canvas></div>
      </section>

      <section class="panel">
        <div class="phead"><span class="idx">02</span><h2>Trace</h2><span class="tag">{strokes.length} strokes</span></div>
        <div class="slider"><label>edge threshold<b>{threshold}</b></label><input type="range" min="10" max="255" bind:value={threshold} /></div>
        <div class="slider"><label>resolution<b>{maxDim}px</b></label><input type="range" min="60" max="320" step="10" bind:value={maxDim} /></div>
        <div class="slider"><label>simplify<b>{simplifyPx}px</b></label><input type="range" min="0" max="5" step="0.5" bind:value={simplifyPx} /></div>
        <div class="slider"><label>min stroke<b>{minStroke}</b></label><input type="range" min="2" max="30" bind:value={minStroke} /></div>
        <p class="hint">{img ? "live · traces as you adjust" : "load a photo to trace"}</p>
      </section>

      <section class="panel">
        <div class="phead"><span class="idx">03</span><h2>Canvas</h2><span class="tag">aspect {planeAspect.toFixed(2)}</span></div>
        <div class="grp"><span class="grplabel">origin (mm)</span>
          <div class="row">
            <div class="field sm"><label>x</label><input type="number" bind:value={plane.origin.x} /></div>
            <div class="field sm"><label>y</label><input type="number" bind:value={plane.origin.y} /></div>
            <div class="field sm"><label>z</label><input type="number" bind:value={plane.origin.z} /></div>
          </div>
        </div>
        <div class="grp"><span class="grplabel">size (mm)</span>
          <div class="row">
            <div class="field sm"><label>width</label><input type="number" bind:value={plane.width_mm} /></div>
            <div class="field sm"><label>height</label><input type="number" bind:value={plane.height_mm} /></div>
          </div>
        </div>
        <div class="grp"><span class="grplabel">approach</span>
          <div class="row">
            <div class="field sm"><label>x</label><input type="number" bind:value={plane.approach.x} /></div>
            <div class="field sm"><label>y</label><input type="number" bind:value={plane.approach.y} /></div>
            <div class="field sm"><label>z</label><input type="number" bind:value={plane.approach.z} /></div>
          </div>
        </div>
        <label class="toggle">
          <input type="checkbox" bind:checked={plane.mirror} />
          <span>mirror (paint from the back)</span>
        </label>
        <div class="actions">
          <button onclick={applyPlane}>apply</button>
          <button onclick={refreshPlane}>reload</button>
        </div>
      </section>

      <section class="panel expose">
        <div class="phead"><span class="idx">04</span><h2>Expose</h2></div>
        <label class="color">
          <input type="color" bind:value={paintColor} />
          <span>light color <b style="color:{paintColor}">{paintColor}</b></span>
        </label>
        <button class="primary big" onclick={paint} disabled={painting || strokes.length === 0}>
          {painting ? "exposing…" : "▸ paint"}
        </button>
        <div class="actions">
          <button onclick={home} disabled={painting}>home</button>
          <button class="danger" onclick={stop}>stop</button>
          <button onclick={clearVisuals}>clear visuals</button>
        </div>
        <p class="hint">Painted trajectories persist in the 3D scene — clear visuals erases them.</p>
      </section>
    </div>

    <section class="panel logpanel">
      <div class="phead"><span class="idx">··</span><h2>Capture log</h2></div>
      <ul class="log">{#each log as line}<li>{line}</li>{/each}</ul>
    </section>
  {/if}
</div>

<style>
  .app {
    position: relative;
    z-index: 1;
    max-width: 1180px;
    margin: 0 auto;
    padding: 28px 22px 60px;
  }

  /* ---- masthead ---- */
  .masthead {
    display: flex;
    align-items: flex-end;
    justify-content: space-between;
    gap: 20px;
    flex-wrap: wrap;
    padding-bottom: 18px;
    margin-bottom: 26px;
    border-bottom: 1px solid var(--line);
    animation: rise 0.6s both;
  }
  .wordmark {
    font-family: var(--font-display);
    font-weight: 900;
    font-size: clamp(38px, 7vw, 72px);
    line-height: 0.84;
    letter-spacing: 0.01em;
    color: var(--ink);
    text-shadow: 0 0 26px rgba(255, 206, 74, 0.18);
  }
  .wordmark span {
    display: block;
    color: var(--amber);
    text-shadow: 0 0 22px rgba(255, 206, 74, 0.45);
  }
  .tagline {
    margin-top: 8px;
    font-size: 11px;
    letter-spacing: 0.42em;
    text-transform: uppercase;
    color: var(--muted);
  }
  .meta {
    display: flex;
    align-items: center;
    gap: 12px;
    flex-wrap: wrap;
  }
  .chip {
    font-size: 11px;
    letter-spacing: 0.12em;
    color: var(--amber);
    border: 1px solid var(--line);
    border-radius: 999px;
    padding: 4px 11px;
  }
  .status {
    font-size: 11px;
    color: var(--muted);
    display: flex;
    align-items: center;
    gap: 7px;
  }
  .dot {
    width: 7px;
    height: 7px;
    border-radius: 50%;
    background: #5a5347;
  }
  .dot.live {
    background: var(--amber);
    box-shadow: var(--glow-amber);
    animation: pulse 2.4s ease-in-out infinite;
  }
  .btnlink {
    font-size: 11px;
    letter-spacing: 0.1em;
    text-transform: uppercase;
    text-decoration: none;
    color: var(--violet);
    border: 1px solid rgba(166, 132, 255, 0.32);
    border-radius: 2px;
    padding: 8px 13px;
    transition: all 0.18s ease;
  }
  .btnlink:hover {
    border-color: var(--violet);
    box-shadow: 0 0 18px rgba(166, 132, 255, 0.3);
  }
  .arr {
    opacity: 0.8;
  }

  /* ---- panels / grid ---- */
  .grid {
    display: grid;
    grid-template-columns: 1.15fr 1fr;
    gap: 16px;
  }
  .panel {
    position: relative;
    background: var(--panel);
    backdrop-filter: blur(6px);
    border: 1px solid var(--line);
    border-radius: 4px;
    padding: 18px;
    animation: rise 0.6s both;
  }
  .panel::after {
    content: "";
    position: absolute;
    inset: 0;
    border-radius: 4px;
    pointer-events: none;
    box-shadow: inset 0 1px 0 rgba(255, 255, 255, 0.03);
  }
  .grid .panel:nth-child(1) { animation-delay: 0.05s; }
  .grid .panel:nth-child(2) { animation-delay: 0.12s; }
  .grid .panel:nth-child(3) { animation-delay: 0.19s; }
  .grid .panel:nth-child(4) { animation-delay: 0.26s; }
  .logpanel { animation-delay: 0.32s; margin-top: 16px; }
  .connect { max-width: 460px; animation-delay: 0.05s; }
  .expose { display: flex; flex-direction: column; }

  .phead {
    display: flex;
    align-items: baseline;
    gap: 12px;
    margin-bottom: 16px;
  }
  .idx {
    font-family: var(--font-display);
    font-weight: 800;
    font-size: 22px;
    color: var(--amber);
    opacity: 0.85;
    min-width: 26px;
  }
  h2 {
    font-family: var(--font-display);
    font-weight: 700;
    font-size: 21px;
    letter-spacing: 0.04em;
    text-transform: uppercase;
    margin: 0;
    color: var(--ink);
  }
  .tag {
    margin-left: auto;
    font-size: 10px;
    letter-spacing: 0.1em;
    text-transform: uppercase;
    color: var(--muted);
    border: 1px solid var(--line-soft);
    border-radius: 999px;
    padding: 3px 9px;
    white-space: nowrap;
  }

  /* ---- inputs ---- */
  .field {
    display: flex;
    flex-direction: column;
    gap: 5px;
    margin-bottom: 10px;
  }
  .field label,
  .grplabel {
    font-size: 10px;
    letter-spacing: 0.14em;
    text-transform: uppercase;
    color: var(--muted);
  }
  .field.sm input { width: 100%; }
  .grp { margin-bottom: 12px; }
  .grplabel { display: block; margin-bottom: 6px; }
  .row { display: grid; grid-template-columns: repeat(3, 1fr); gap: 8px; }
  .grp:nth-of-type(2) .row { grid-template-columns: repeat(2, 1fr); }

  .slider {
    margin-bottom: 13px;
  }
  .slider label {
    display: flex;
    justify-content: space-between;
    font-size: 10px;
    letter-spacing: 0.14em;
    text-transform: uppercase;
    color: var(--muted);
    margin-bottom: 7px;
  }
  .slider label b {
    color: var(--amber);
    font-weight: 700;
  }
  .slider input { width: 100%; }

  .actions {
    display: flex;
    gap: 8px;
    flex-wrap: wrap;
    margin-top: 6px;
  }
  .expose .big {
    font-size: 15px;
    padding: 14px;
    margin-bottom: 10px;
    letter-spacing: 0.14em;
  }

  /* ---- file picker ---- */
  .filepick {
    display: inline-block;
    cursor: pointer;
  }
  .filepick input { display: none; }
  .filepick span {
    display: inline-block;
    font-size: 12px;
    letter-spacing: 0.08em;
    text-transform: uppercase;
    color: var(--amber-bright);
    border: 1px dashed var(--line);
    border-radius: 2px;
    padding: 9px 16px;
    transition: all 0.18s ease;
  }
  .filepick:hover span {
    border-color: var(--amber);
    color: var(--amber);
  }

  /* ---- preview ---- */
  .frame {
    margin-top: 14px;
    border: 1px solid var(--line);
    border-radius: 3px;
    background:
      linear-gradient(rgba(255, 206, 74, 0.015), transparent),
      repeating-linear-gradient(0deg, rgba(255, 255, 255, 0.02) 0 1px, transparent 1px 22px),
      repeating-linear-gradient(90deg, rgba(255, 255, 255, 0.02) 0 1px, transparent 1px 22px),
      #060504;
    padding: 10px;
    overflow: hidden;
  }
  .preview {
    width: 100%;
    display: block;
    border-radius: 2px;
  }

  /* ---- log ---- */
  .log {
    list-style: none;
    margin: 0;
    padding: 0;
    font-size: 12px;
    max-height: 170px;
    overflow-y: auto;
  }
  .log li {
    padding: 3px 0;
    color: var(--muted);
    border-bottom: 1px solid var(--line-soft);
  }
  .log li:first-child {
    color: var(--amber-bright);
  }

  .hint {
    font-size: 11px;
    line-height: 1.5;
    color: var(--muted);
    margin: 10px 0 0;
  }
  .error {
    font-size: 12px;
    color: var(--danger);
    border: 1px solid rgba(255, 111, 94, 0.3);
    border-radius: 3px;
    padding: 10px 12px;
  }

  .toggle {
    display: flex;
    align-items: center;
    gap: 9px;
    margin: 2px 0 12px;
    cursor: pointer;
    font-size: 11px;
    letter-spacing: 0.08em;
    text-transform: uppercase;
    color: var(--muted);
  }
  .toggle input {
    accent-color: var(--amber);
    width: 15px;
    height: 15px;
    cursor: pointer;
  }
  .toggle:hover span {
    color: var(--amber);
  }

  .color {
    display: flex;
    align-items: center;
    gap: 10px;
    margin-bottom: 10px;
    font-size: 11px;
    letter-spacing: 0.08em;
    text-transform: uppercase;
    color: var(--muted);
    cursor: pointer;
  }
  .color input[type="color"] {
    width: 34px;
    height: 26px;
    padding: 0;
    border: 1px solid var(--line);
    border-radius: 4px;
    background: transparent;
    cursor: pointer;
  }

  @keyframes rise {
    from { opacity: 0; transform: translateY(14px); }
    to { opacity: 1; transform: translateY(0); }
  }
  @keyframes pulse {
    0%, 100% { opacity: 1; }
    50% { opacity: 0.4; }
  }

  @media (max-width: 760px) {
    .grid { grid-template-columns: 1fr; }
  }
</style>
