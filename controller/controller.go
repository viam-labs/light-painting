// Package controller implements the light-painting-controller generic service.
//
// The controller receives a traced path (normalized image strokes) from the
// web app and "draws" it with an arm: for each stroke it travels to the start
// with the tool lifted off the surface, lowers onto the drawing plane, moves
// the tool through the stroke's points, then lifts off again. All arm motion
// goes through the Viam motion service.
//
// The drawing plane (where image pixels land in the arm's workspace) is owned
// by the controller and is adjustable both via config and at runtime through
// the set_plane DoCommand, so the plane can be repositioned or resized without
// re-tracing the image.
package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/golang/geo/r3"
	"go.viam.com/rdk/components/arm"
	"go.viam.com/rdk/logging"
	"go.viam.com/rdk/referenceframe"
	"go.viam.com/rdk/resource"
	generic "go.viam.com/rdk/services/generic"
	"go.viam.com/rdk/services/motion"
	"go.viam.com/rdk/services/worldstatestore"
	"go.viam.com/rdk/spatialmath"

	"light-painting/scene"
)

// Model is the resource model for the light-painting controller.
var Model = resource.NewModel("viam", "light-painting", "light-painting-controller")

const (
	defaultMotionService = "builtin"
	defaultLiftMM        = 50.0
	defaultMinSegmentMM  = 5.0
)

func init() {
	resource.RegisterService(generic.API, Model,
		resource.Registration[resource.Resource, *Config]{
			Constructor: newController,
		},
	)
}

// Config is the JSON configuration for the controller.
type Config struct {
	// Arm is the name of the arm component to move (required).
	Arm string `json:"arm"`
	// MotionService is the name of the motion service to plan with. Defaults to
	// "builtin".
	MotionService string `json:"motion_service,omitempty"`
	// DrawingPlane is the (adjustable) surface image strokes are mapped onto.
	DrawingPlane PlaneConfig `json:"drawing_plane"`
	// LiftMM is how far to retract along the surface normal for light-off travel
	// moves between strokes. Defaults to 50mm.
	LiftMM float64 `json:"lift_mm,omitempty"`
	// MinSegmentMM down-samples dense traced paths: consecutive points closer
	// than this (in mm on the plane) are dropped. Defaults to 5mm.
	MinSegmentMM float64 `json:"min_segment_mm,omitempty"`
	// LED is the name of an end-effector LED component (future use; unused today).
	LED string `json:"led,omitempty"`
	// Scene is the name of a painting-scene world_state_store visualizer to draw
	// the plane and painted strokes into the Viam 3D viewer. Optional.
	Scene string `json:"scene,omitempty"`
}

// Validate checks required fields and returns the resource dependencies.
func (cfg *Config) Validate(path string) ([]string, []string, error) {
	if cfg.Arm == "" {
		return nil, nil, resource.NewConfigValidationFieldRequiredError(path, "arm")
	}
	if cfg.DrawingPlane.WidthMM <= 0 || cfg.DrawingPlane.HeightMM <= 0 {
		return nil, nil, resource.NewConfigValidationError(path,
			fmt.Errorf("drawing_plane.width_mm and drawing_plane.height_mm must be > 0"))
	}
	ms := cfg.MotionService
	if ms == "" {
		ms = defaultMotionService
	}
	deps := []string{motion.Named(ms).String(), arm.Named(cfg.Arm).String()}
	if cfg.Scene != "" {
		// Depend on the visualizer so it is constructed (and registered
		// in-process) before this controller.
		deps = append(deps, worldstatestore.Named(cfg.Scene).String())
	}
	return deps, nil, nil
}

type controller struct {
	resource.AlwaysRebuild

	name    resource.Name
	logger  logging.Logger
	motion  motion.Service
	arm     arm.Arm
	armName string
	lift    float64
	minSeg  float64
	scene   scene.Sink // nil when no visualizer is configured

	// strokeSeq assigns persistent, monotonically-increasing visual stroke ids
	// so strokes from successive paint runs accumulate rather than overwrite.
	strokeSeq int

	// stateMu guards the adjustable plane and the cancel scope.
	stateMu    sync.Mutex
	plane      *plane
	cancelCtx  context.Context
	cancelFunc context.CancelFunc

	// opMu serializes paint/home operations so only one runs at a time.
	opMu sync.Mutex
}

func newController(
	ctx context.Context,
	deps resource.Dependencies,
	rawConf resource.Config,
	logger logging.Logger,
) (resource.Resource, error) {
	conf, err := resource.NativeConfig[*Config](rawConf)
	if err != nil {
		return nil, err
	}

	msName := conf.MotionService
	if msName == "" {
		msName = defaultMotionService
	}
	motionSvc, err := motion.FromDependencies(deps, msName)
	if err != nil {
		return nil, fmt.Errorf("getting motion service %q: %w", msName, err)
	}

	armRes, ok := deps[arm.Named(conf.Arm)]
	if !ok {
		return nil, fmt.Errorf("arm %q not found in dependencies", conf.Arm)
	}
	armComp, ok := armRes.(arm.Arm)
	if !ok {
		return nil, fmt.Errorf("resource %q is not an arm", conf.Arm)
	}

	pl, err := newPlane(conf.DrawingPlane)
	if err != nil {
		return nil, err
	}

	lift := conf.LiftMM
	if lift <= 0 {
		lift = defaultLiftMM
	}
	minSeg := conf.MinSegmentMM
	if minSeg <= 0 {
		minSeg = defaultMinSegmentMM
	}

	cancelCtx, cancelFunc := context.WithCancel(context.Background())
	s := &controller{
		name:       rawConf.ResourceName(),
		logger:     logger,
		motion:     motionSvc,
		arm:        armComp,
		armName:    conf.Arm,
		lift:       lift,
		minSeg:     minSeg,
		plane:      pl,
		cancelCtx:  cancelCtx,
		cancelFunc: cancelFunc,
	}

	if conf.Scene != "" {
		if sink, ok := scene.Lookup(conf.Scene); ok {
			s.scene = sink
			logger.Infof("visualizing into painting-scene %q", conf.Scene)
		} else {
			logger.Warnf("scene %q not found in-process; visualization disabled", conf.Scene)
		}
	}
	logger.Infof("light-painting-controller ready: arm=%q motion=%q plane(w=%.0f h=%.0f aspect=%.2f) lift=%.0fmm",
		conf.Arm, msName, pl.width, pl.height, pl.aspect(), lift)
	return s, nil
}

func (s *controller) Name() resource.Name { return s.name }

func (s *controller) Status(ctx context.Context) (map[string]interface{}, error) {
	s.stateMu.Lock()
	cfg := s.plane.config()
	aspect := s.plane.aspect()
	s.stateMu.Unlock()
	m, err := structToMap(cfg)
	if err != nil {
		return nil, err
	}
	return map[string]interface{}{
		"arm":            s.armName,
		"lift_mm":        s.lift,
		"min_segment_mm": s.minSeg,
		"plane":          m,
		"aspect":         aspect,
	}, nil
}

func (s *controller) Close(context.Context) error {
	s.stateMu.Lock()
	s.cancelFunc()
	s.stateMu.Unlock()
	return nil
}

// ---- DoCommand dispatch -----------------------------------------------------

func (s *controller) DoCommand(ctx context.Context, cmd map[string]interface{}) (map[string]interface{}, error) {
	action, _ := cmd["command"].(string)
	if action == "" {
		for _, k := range []string{"get_plane", "set_plane", "paint_path", "home", "stop", "set_color", "clear_visuals"} {
			if _, ok := cmd[k]; ok {
				action = k
				break
			}
		}
	}

	switch action {
	case "get_plane":
		return s.getPlane()
	case "set_plane":
		return s.setPlane(cmd)
	case "paint_path":
		return s.paintPath(ctx, cmd)
	case "home":
		return s.home(ctx)
	case "stop":
		return s.stop()
	case "set_color":
		return s.setColor(cmd)
	case "clear_visuals":
		return s.clearVisuals()
	default:
		return nil, fmt.Errorf(
			"unknown command %q; supported: get_plane, set_plane, paint_path, home, stop, set_color, clear_visuals", action)
	}
}

// ---- plane commands ---------------------------------------------------------

func (s *controller) getPlane() (map[string]interface{}, error) {
	s.stateMu.Lock()
	cfg := s.plane.config()
	aspect := s.plane.aspect()
	s.stateMu.Unlock()

	m, err := structToMap(cfg)
	if err != nil {
		return nil, err
	}
	m["aspect"] = aspect
	return m, nil
}

func (s *controller) setPlane(cmd map[string]interface{}) (map[string]interface{}, error) {
	var cfg PlaneConfig
	if err := jsonRoundTrip(cmd, &cfg); err != nil {
		return nil, fmt.Errorf("parsing plane: %w", err)
	}
	pl, err := newPlane(cfg)
	if err != nil {
		return nil, err
	}
	s.stateMu.Lock()
	s.plane = pl
	s.stateMu.Unlock()
	s.logger.Infof("drawing plane updated: origin=%v w=%.0f h=%.0f", pl.origin, pl.width, pl.height)
	return s.getPlane()
}

// ---- LED stub (future) ------------------------------------------------------

// clearVisuals erases the plane and all painted strokes from the 3D scene.
func (s *controller) clearVisuals() (map[string]interface{}, error) {
	if s.scene != nil {
		s.scene.Clear()
	}
	return map[string]interface{}{"cleared": true}, nil
}

func (s *controller) setColor(cmd map[string]interface{}) (map[string]interface{}, error) {
	// Forward-compatible no-op until an LED component is wired in.
	return map[string]interface{}{
		"ok":   true,
		"note": "LED color control not yet wired; command accepted for forward-compatibility",
	}, nil
}

// ---- motion -----------------------------------------------------------------

// stop cancels any in-flight paint operation and best-effort stops the arm.
func (s *controller) stop() (map[string]interface{}, error) {
	s.stateMu.Lock()
	s.cancelFunc()
	s.cancelCtx, s.cancelFunc = context.WithCancel(context.Background())
	armComp := s.arm
	s.stateMu.Unlock()

	if err := armComp.Stop(context.Background(), nil); err != nil {
		s.logger.Warnf("stop: arm stop returned: %v", err)
	}
	return map[string]interface{}{"stopped": true}, nil
}

func (s *controller) home(ctx context.Context) (map[string]interface{}, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()

	s.stateMu.Lock()
	pl := s.plane
	lift := s.lift
	cancelCtx := s.cancelCtx
	s.stateMu.Unlock()

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer context.AfterFunc(cancelCtx, cancel)()

	pose := pl.liftedPoseAt(0.5, 0.5, lift)
	if err := s.moveTo(ctx, pose); err != nil {
		return nil, fmt.Errorf("home move: %w", err)
	}
	return map[string]interface{}{"home": true}, nil
}

func (s *controller) moveTo(ctx context.Context, pose spatialmath.Pose) error {
	dest := referenceframe.NewPoseInFrame(referenceframe.World, pose)
	_, err := s.motion.Move(ctx, motion.MoveReq{
		ComponentName: s.armName,
		Destination:   dest,
	})
	return err
}

// ---- paint ------------------------------------------------------------------

type pointJSON struct {
	U float64 `json:"u"`
	V float64 `json:"v"`
}

type colorJSON struct {
	R float64 `json:"r"`
	G float64 `json:"g"`
	B float64 `json:"b"`
}

type strokeJSON struct {
	Points []pointJSON `json:"points"`
	Color  *colorJSON  `json:"color,omitempty"`
}

type paintPayload struct {
	Strokes []strokeJSON `json:"strokes"`
}

func (s *controller) paintPath(ctx context.Context, cmd map[string]interface{}) (map[string]interface{}, error) {
	var payload paintPayload
	if err := jsonRoundTrip(cmd, &payload); err != nil {
		return nil, fmt.Errorf("parsing paint_path: %w", err)
	}
	if len(payload.Strokes) == 0 {
		return nil, fmt.Errorf("paint_path: no strokes provided")
	}

	s.opMu.Lock()
	defer s.opMu.Unlock()

	s.stateMu.Lock()
	pl := s.plane
	lift := s.lift
	minSeg := s.minSeg
	cancelCtx := s.cancelCtx
	s.stateMu.Unlock()

	// Link this operation to the controller cancel scope so stop() cancels it.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer context.AfterFunc(cancelCtx, cancel)()

	// Draw the current drawing-plane outline. Prior strokes are left in place so
	// successive paint runs accumulate (clear them with the clear_visuals command).
	if s.scene != nil {
		s.scene.ShowPlane([]r3.Vector{
			pl.point(0, 0), pl.point(1, 0), pl.point(1, 1), pl.point(0, 1),
		})
	}

	totalPoints := 0
	for i, st := range payload.Strokes {
		n, err := s.execStroke(ctx, pl, lift, minSeg, st)
		totalPoints += n
		if err != nil {
			return nil, fmt.Errorf("stroke %d/%d: %w", i+1, len(payload.Strokes), err)
		}
	}
	s.logger.Infof("painted %d stroke(s), %d point(s)", len(payload.Strokes), totalPoints)
	return map[string]interface{}{
		"strokes": len(payload.Strokes),
		"points":  totalPoints,
	}, nil
}

// execStroke draws a single stroke: travel (lifted) to the start, lower, trace
// the down-sampled points, then lift off.
func (s *controller) execStroke(
	ctx context.Context, pl *plane, lift, minSeg float64, st strokeJSON,
) (int, error) {
	kept := downsample(pl, st.Points, minSeg)
	if len(kept) == 0 {
		return 0, nil
	}

	// Draw the stroke into the 3D scene up-front in the active color so the
	// trajectory being painted is highlighted while the arm traces it. The
	// finished stroke is recolored once the arm completes it (deferred below).
	strokeID := -1
	if s.scene != nil && len(kept) >= 2 {
		worldPts := make([]r3.Vector, len(kept))
		for i, pt := range kept {
			worldPts[i] = pl.point(pt.U, pt.V)
		}
		strokeID = s.nextStrokeID()
		s.scene.StartStroke(strokeID, worldPts)
		defer func() {
			if strokeID >= 0 {
				s.scene.FinishStroke(strokeID)
			}
		}()
	}

	first := kept[0]
	if err := s.moveTo(ctx, pl.liftedPoseAt(first.U, first.V, lift)); err != nil {
		return 0, fmt.Errorf("travel to start: %w", err)
	}
	if err := s.moveTo(ctx, pl.poseAt(first.U, first.V)); err != nil {
		return 0, fmt.Errorf("lower to surface: %w", err)
	}
	// (future) set LED color from st.Color here.

	count := 1
	for _, pt := range kept[1:] {
		if err := s.moveTo(ctx, pl.poseAt(pt.U, pt.V)); err != nil {
			return count, fmt.Errorf("draw point: %w", err)
		}
		count++
	}

	last := kept[len(kept)-1]
	if err := s.moveTo(ctx, pl.liftedPoseAt(last.U, last.V, lift)); err != nil {
		return count, fmt.Errorf("lift off: %w", err)
	}
	return count, nil
}

// downsample drops points closer than minSeg mm (measured on the plane) to the
// previous kept point. The first and last points are always kept.
func downsample(pl *plane, pts []pointJSON, minSeg float64) []pointJSON {
	if len(pts) == 0 {
		return nil
	}
	kept := []pointJSON{pts[0]}
	lastWorld := pl.point(pts[0].U, pts[0].V)
	for i := 1; i < len(pts); i++ {
		w := pl.point(pts[i].U, pts[i].V)
		isLast := i == len(pts)-1
		if isLast || w.Sub(lastWorld).Norm() >= minSeg {
			kept = append(kept, pts[i])
			lastWorld = w
		}
	}
	return kept
}

// ---- helpers ----------------------------------------------------------------

// nextStrokeID returns a fresh, never-reused visual stroke id.
func (s *controller) nextStrokeID() int {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	id := s.strokeSeq
	s.strokeSeq++
	return id
}

func jsonRoundTrip(in, out interface{}) error {
	b, err := json.Marshal(in)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}

func structToMap(in interface{}) (map[string]interface{}, error) {
	var m map[string]interface{}
	if err := jsonRoundTrip(in, &m); err != nil {
		return nil, err
	}
	return m, nil
}
