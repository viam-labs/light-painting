# CLAUDE.md — light-painting

Viam module that "light-paints" with a robot arm: a web app traces a photo into a path,
the arm draws it with a light (for long-exposure capture), and a 3D scene visualizer shows
the plane + painted strokes. Built for the `viam` org namespace (org `viam-dev`).

## Architecture

Two models ship in one Go binary (`cmd/module/main.go` registers both):

- **`viam:light-painting:light-painting-controller`** (`rdk:service:generic`, `controller/`)
  — receives normalized strokes via `DoCommand` and drives the arm through the **motion
  service**. Owns the adjustable drawing plane and the (u,v)→task-space pose mapping
  (`controller/plane.go`).
- **`viam:light-painting:painting-scene`** (`rdk:service:world_state_store`, `scene/`) — a
  visualizer built on `github.com/viam-labs/viam-viz-helpers-go`. The controller pushes the
  plane outline + strokes to it **in-process** (`visuals.Register`/`scene.Lookup`), no gRPC.

The **web app** (`web/`, Svelte + Vite) is an embedded Viam Application: photo pick →
in-browser trace (`web/src/lib/trace.ts`, dependency-free Sobel + RDP) → `doCommand`
(`web/src/lib/viam.ts`). Path tracing is browser-side; the controller maps normalized
points onto the plane so the plane stays adjustable without re-tracing.

`DoCommand` verbs: `get_plane`, `set_plane`, `paint_path`, `home`, `stop`, `clear_visuals`,
`set_color` (LED stub).

## Build / test

```bash
make module-fast      # go test + build bin/light-painting + tar (web/dist must exist)
make                  # also rebuilds the web app
go test ./...         # plane-mapping + downsample unit tests
cd web && npm install && npm run build   # web app -> web/dist
go run ./cmd/smoketest [addr]            # headless driver; default localhost:8090
```

The smoke test takes `VIAM_API_KEY` / `VIAM_API_KEY_ID` env vars to drive a cloud machine
(else it connects insecurely to a local server).

## Local run

`test/local-config.json` is local-only (no cloud): simulated `ur5e` arm + visualizer +
this module. `viam-server -config test/local-config.json` serves on `localhost:8090`.

## Deploy (registry + cloud machine)

Published to the registry as `viam:light-painting` (private, `viam` namespace). Release:

```bash
# bump web/package.json + test/cloud-machine-config.json version together
make module-fast
viam module update                                            # syncs meta.json (models, app)
viam module upload --version X.Y.Z --platform linux/amd64 --upload module.tar.gz
```

Test cloud machine: **light-painting** (`71c5dec3-…`) in org `viam-dev` / location
`Build on Viam` (`oeq47g5p1m`). Robot config is `test/cloud-machine-config.json` (registry
module + simulated arm + scene). The Viam Application is hosted at
`https://light-painting-app_viam.viamapplications.com/machine/<machine-id>`.

## Conventions & gotchas

- **RDK v0.124.0**, Go 1.25. `resource.Resource` requires `Status` (provided by
  `resource.Named`); a `world_state_store` service must define `DoCommand` explicitly to
  disambiguate `SceneServiceBase` from `resource.Named`.
- **`motion.MoveReq.ComponentName` is the bare frame name** (`"my_arm"`), not a resource name.
- **The `builtin` motion service is a default service** — do NOT add it to configs; the
  controller's default `motion_service: "builtin"` resolves to the auto-created one.
- **Simulated vs fake arm**: use `rdk:builtin:simulated` (`simulate-time: true`, `arm-model:
  ur5e`) so the arm visibly animates. `fake` teleports; `xarm6` rendered wonky — use `ur5e`.
- **Viz live-update caveat**: the visualizer pushes via full `SetScene` respawns with the
  default `stable` UUID strategy; the renderer caches REMOVED UUIDs, so in-place respawns
  can need a viewer refresh. Switch to the `versioned` UUID strategy / incremental
  `Scene.Add`/`Update` if smoother live streaming is needed.
- **viam-sdk auth**: the `viam-skills` scripts (`machine_up.py`, `update_config.py`) crash on
  viam-sdk 0.77 ("already authenticated"). Work around with a single-auth path: plain
  grpclib `Channel` → one `_get_access_token` → `AppClient` directly. SDK lives in
  `/tmp/viam-venv` (no system pip). The robot-config push script is `/tmp/push_config.py`.
- **mirror**: `drawing_plane.mirror` flips `u → 1-u` for "paint from the back" (camera on the
  far side of the plane).
