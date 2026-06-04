# light-painting

A Viam module + web app for **light painting** with a robot arm: pick a photo, the
web app traces a path through it, and the arm "draws" that path with a light/pen on an
adjustable plane — ideal for long-exposure capture in a dark room.

```
Web app (Svelte)                light-painting-controller (Go)         Viam motion service
  pick photo                      maps normalized strokes                 plans + executes
  trace -> strokes  ── paint_path ─►  onto the drawing plane  ── Move ──►   arm motion
  adjust plane      ── set_plane ─►   (adjustable, server-owned)
```

## Components

| Path | What |
|------|------|
| `controller/` | `viam:light-painting:light-painting-controller`, a `rdk:service:generic` model |
| `controller/plane.go` | the adjustable drawing plane and the normalized-(u,v) → task-space pose mapping |
| `web/` | Svelte + Vite web app (embedded Viam Application): photo pick, in-browser tracing, plane editor, paint controls |
| `cmd/module/` | module entrypoint |
| `cmd/smoketest/` | headless client that drives the controller against a local server |
| `test/local-config.json` | local-only robot config: fake `xarm6` arm + builtin motion + this module |

## How it works

The web app traces the selected image into **normalized strokes** (points in `[0,1]`
image space) and sends them with `paint_path`. The **controller** owns the drawing-plane
geometry and maps each normalized point onto a task-space pose, so the plane can be moved
or resized (`set_plane`) without re-tracing the image. For each stroke the controller
travels to the start with the tool lifted off the surface, lowers onto the plane, traces
the points (light on), then lifts off. All arm motion goes through the Viam **motion
service**.

### Configuration

```json
{
  "arm": "my_arm",
  "motion_service": "builtin",
  "drawing_plane": {
    "origin":   { "x": 300, "y": 150, "z": 500 },
    "width_mm": 300,
    "height_mm": 300,
    "approach": { "x": 1, "y": 0, "z": 0 },
    "up":       { "x": 0, "y": 0, "z": 1 }
  },
  "lift_mm": 50,
  "min_segment_mm": 5
}
```

- `drawing_plane.origin` is the world position (mm) of the image's **top-left** corner.
- `approach` is the direction the tool points while drawing (into the surface).
- `up` is the world direction that maps to image "up".
- `min_segment_mm` down-samples dense traced paths to keep motion tractable.

### DoCommand API

| Command | Payload | Result |
|---------|---------|--------|
| `get_plane` | – | current plane + `aspect` |
| `set_plane` | `origin`, `width_mm`, `height_mm`, `approach?`, `up?` | updated plane |
| `paint_path` | `{ "strokes": [ { "points": [{"u":..,"v":..}], "color"?: {r,g,b} } ] }` | `{strokes, points}` |
| `home` | – | moves to the lifted plane center |
| `stop` | – | cancels the in-flight paint and stops the arm |
| `set_color` | `{r,g,b}` | **stub** — accepted, no-op until an LED is wired in |

## Build

```bash
make            # go test + build bin/light-painting + build web app + module.tar.gz
make module-fast  # binary + tar without rebuilding the web app
go test ./...   # unit tests (plane mapping)
cd web && npm install && npm run build   # web app -> web/dist
```

## Local testing with a simulated arm

`test/local-config.json` runs everything locally with a **fake xArm6** — no cloud needed.

```bash
make                                            # build bin/light-painting
viam-server -config test/local-config.json &    # starts on localhost:8090
go run ./cmd/smoketest                           # drives get_plane/set_plane/home/paint_path/stop
```

The smoke test prints each command's result and the server log shows the motion service
planning and executing each pose. To drive it from the browser instead, serve the web app
(`cd web && npm run dev`) or deploy it as the module's Viam Application and connect to the
machine.

## Registry & Viam Application

Published to the Viam registry as **`viam:light-painting`** (the model is
`viam:light-painting:light-painting-controller`). Deploy on a machine with a registry
module entry instead of a local path:

```json
"modules": [
  { "type": "registry", "name": "light-painting", "module_id": "viam:light-painting", "version": "0.0.1" }
]
```

The web app ships as the module's Viam Application (`light-painting-app`), hosted at:

```
https://light-painting-app_viam.viamapplications.com/machine/<machine-id>
```

Opening that URL (after logging into Viam) connects the app to the given machine using
cookie-provided credentials — no manual host/key entry needed.

Release a new version after changes:

```bash
make module-fast                                  # rebuild binary + tar (web/dist included)
viam module upload --version <semver> --platform linux/amd64 --upload module.tar.gz
```

## Roadmap

- Wire `set_color` to a real end-effector LED component (the `led` config field and
  per-stroke `color` payload are already in place).
- Batch trajectories instead of one `motion.Move` per waypoint for faster drawing.
- Long-exposure camera capture integration.
