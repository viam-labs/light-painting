<script lang="ts">
  import { getCookie, setCookie } from "typescript-cookie";
  import type { RobotClient } from "@viamrobotics/sdk";
  import { connect, Painter, type Credentials } from "./lib/viam";
  import { traceImage, letterbox, type Stroke } from "./lib/trace";

  // ---- connection state ----
  let host = $state("");
  let credentials = $state<Credentials | null>(null);
  let client = $state<RobotClient | null>(null);
  let painter = $state<Painter | null>(null);
  let serviceName = $state("painter");
  let connecting = $state(false);
  let error = $state("");
  let log = $state<string[]>([]);

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
      const cookieValue = getCookie(parts[2]);
      if (cookieValue) {
        const v = JSON.parse(cookieValue);
        return [
          v.hostname,
          { type: "api-key", payload: v.key, authEntity: v.id },
        ];
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
    doConnect(formHost, {
      type: "api-key",
      payload: formKey,
      authEntity: formKeyId,
    });
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
      };
      addLog(`plane: ${plane.width_mm}x${plane.height_mm}mm @ ${JSON.stringify(plane.origin)}`);
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
    addLog(`traced ${strokes.length} stroke(s)`);
    drawPreview();
  }

  // Draw the image with the traced strokes overlaid (in image space).
  function drawPreview() {
    const c = previewCanvas;
    if (!c || !img) return;
    const W = 480;
    const H = Math.round(W / imgAspect);
    c.width = W;
    c.height = H;
    const ctx = c.getContext("2d");
    if (!ctx) return;
    ctx.fillStyle = "#000";
    ctx.fillRect(0, 0, W, H);
    ctx.globalAlpha = 0.5;
    ctx.drawImage(img, 0, 0, W, H);
    ctx.globalAlpha = 1;
    ctx.strokeStyle = "#a855f7";
    ctx.lineWidth = 1.5;
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
  }

  let painting = $state(false);

  async function paint() {
    if (!painter || strokes.length === 0) return;
    painting = true;
    try {
      const mapped = letterbox(strokes, imgAspect, planeAspect);
      addLog(`painting ${mapped.length} stroke(s)...`);
      const res = (await painter.paintPath(mapped)) as any;
      addLog(`done: ${res.strokes} strokes / ${res.points} points`);
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
</script>

<main>
  <header>
    <h1>🖌️ Light Painting</h1>
    {#if host}<span class="badge">{host}</span>{/if}
  </header>

  {#if error}<p class="error">{error}</p>{/if}

  {#if !client}
    <section class="card connect">
      <h2>Connect to a machine</h2>
      <p class="hint">
        When served as a Viam Application this fills in automatically. For local
        development, paste an API key below.
      </p>
      <div class="field"><label for="h">Host</label><input id="h" type="text" bind:value={formHost} placeholder="my-machine.abcd.viam.cloud" /></div>
      <div class="field"><label for="i">API Key ID</label><input id="i" type="text" bind:value={formKeyId} /></div>
      <div class="field"><label for="k">API Key</label><input id="k" type="password" bind:value={formKey} /></div>
      <div class="field"><label for="s">Service name</label><input id="s" type="text" bind:value={serviceName} /></div>
      <button class="primary" onclick={saveAndConnect} disabled={connecting}>
        {connecting ? "Connecting…" : "Connect"}
      </button>
    </section>
  {:else}
    <div class="grid">
      <section class="card">
        <h2>1 · Photo</h2>
        <input type="file" accept="image/*" onchange={onFile} />
        <canvas bind:this={previewCanvas} class="preview"></canvas>
      </section>

      <section class="card">
        <h2>2 · Trace</h2>
        <div class="slider"><label>Edge threshold: {threshold}</label><input type="range" min="10" max="255" bind:value={threshold} /></div>
        <div class="slider"><label>Resolution: {maxDim}px</label><input type="range" min="60" max="320" step="10" bind:value={maxDim} /></div>
        <div class="slider"><label>Simplify: {simplifyPx}px</label><input type="range" min="0" max="5" step="0.5" bind:value={simplifyPx} /></div>
        <div class="slider"><label>Min stroke: {minStroke}</label><input type="range" min="2" max="30" bind:value={minStroke} /></div>
        <button onclick={trace} disabled={!img}>Trace image</button>
        <p class="hint">{strokes.length} stroke(s)</p>
      </section>

      <section class="card">
        <h2>3 · Drawing plane <small>(adjustable)</small></h2>
        <div class="row">
          <div class="field sm"><label>origin x</label><input type="number" bind:value={plane.origin.x} /></div>
          <div class="field sm"><label>origin y</label><input type="number" bind:value={plane.origin.y} /></div>
          <div class="field sm"><label>origin z</label><input type="number" bind:value={plane.origin.z} /></div>
        </div>
        <div class="row">
          <div class="field sm"><label>width mm</label><input type="number" bind:value={plane.width_mm} /></div>
          <div class="field sm"><label>height mm</label><input type="number" bind:value={plane.height_mm} /></div>
        </div>
        <div class="row">
          <div class="field sm"><label>approach x</label><input type="number" bind:value={plane.approach.x} /></div>
          <div class="field sm"><label>approach y</label><input type="number" bind:value={plane.approach.y} /></div>
          <div class="field sm"><label>approach z</label><input type="number" bind:value={plane.approach.z} /></div>
        </div>
        <div class="actions">
          <button onclick={applyPlane}>Apply plane</button>
          <button onclick={refreshPlane}>Reload</button>
        </div>
      </section>

      <section class="card">
        <h2>4 · Paint</h2>
        <div class="actions">
          <button class="primary" onclick={paint} disabled={painting || strokes.length === 0}>
            {painting ? "Painting…" : "Paint"}
          </button>
          <button onclick={home} disabled={painting}>Home</button>
          <button class="danger" onclick={stop}>Stop</button>
        </div>
      </section>
    </div>

    <section class="card log">
      <h2>Log</h2>
      <ul>{#each log as line}<li>{line}</li>{/each}</ul>
    </section>
  {/if}
</main>

<style>
  main {
    max-width: 1100px;
    margin: 0 auto;
    padding: 20px;
  }
  header {
    display: flex;
    align-items: center;
    gap: 12px;
    margin-bottom: 16px;
  }
  h1 {
    font-size: 22px;
    margin: 0;
  }
  h2 {
    font-size: 15px;
    margin: 0 0 10px;
    color: #c9b8f0;
  }
  .badge {
    font-size: 12px;
    color: #9a9ab8;
    background: #1a1a28;
    padding: 3px 8px;
    border-radius: 10px;
  }
  .grid {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 14px;
  }
  .card {
    background: #14141f;
    border: 1px solid #26263a;
    border-radius: 10px;
    padding: 14px;
  }
  .connect {
    max-width: 440px;
  }
  .field {
    margin-bottom: 10px;
    display: flex;
    flex-direction: column;
    gap: 4px;
  }
  .field.sm input {
    width: 90px;
  }
  .row {
    display: flex;
    gap: 8px;
    margin-bottom: 8px;
  }
  .slider {
    display: flex;
    flex-direction: column;
    gap: 2px;
    margin-bottom: 8px;
  }
  .slider input {
    width: 100%;
  }
  .actions {
    display: flex;
    gap: 8px;
    flex-wrap: wrap;
  }
  .preview {
    width: 100%;
    margin-top: 10px;
    border-radius: 6px;
    background: #000;
  }
  .hint {
    font-size: 12px;
    color: #8a8aa6;
  }
  .error {
    color: #f87171;
  }
  .log {
    margin-top: 14px;
  }
  .log ul {
    list-style: none;
    margin: 0;
    padding: 0;
    font-family: ui-monospace, monospace;
    font-size: 12px;
    max-height: 160px;
    overflow-y: auto;
  }
  .log li {
    padding: 2px 0;
    color: #9a9ab8;
  }
</style>
