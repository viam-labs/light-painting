// Package scene implements painting-scene: a world_state_store visualizer that
// renders the light-painting drawing plane and the painted strokes into the
// Viam 3D scene viewer.
//
// It is the "visualizer" half of a driver->visualizer pair: the
// light-painting-controller (driver) holds a direct in-process reference to
// this service (via the viz-helpers registry) and pushes the plane outline and
// each stroke as it paints. Built on github.com/viam-labs/viam-viz-helpers-go,
// which handles the world-state-store wire format underneath.
package scene

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/golang/geo/r3"
	visuals "github.com/viam-labs/viam-viz-helpers-go"
	"go.viam.com/rdk/logging"
	"go.viam.com/rdk/resource"
	"go.viam.com/rdk/services/worldstatestore"
)

// Model is the world_state_store model provided by this module.
var Model = resource.NewModel("viam", "light-painting", "painting-scene")

func init() {
	resource.RegisterService(worldstatestore.API, Model,
		resource.Registration[worldstatestore.Service, resource.NoNativeConfig]{
			Constructor: newScene,
		},
	)
}

// Sink is the in-process interface the controller uses to push painting visuals.
// Strokes persist until Clear is called, so successive paint runs accumulate.
type Sink interface {
	// Clear erases the plane outline and all painted strokes.
	Clear()
	// ShowPlane draws the drawing-plane outline through the given corner points.
	ShowPlane(corners []r3.Vector)
	// StartStroke draws stroke id as a polyline through pts in the active color.
	StartStroke(id int, pts []r3.Vector)
	// FinishStroke recolors stroke id from the active to the finished color.
	FinishStroke(id int)
}

// Stroke colors: the trajectory currently being painted is highlighted; once
// complete it switches to the finished color so prior strokes are distinct.
var (
	activeColor   = visuals.Color{R: 255, G: 225, B: 70}  // bright yellow — painting now
	finishedColor = visuals.Color{R: 150, G: 80, B: 230}  // purple — completed
	planeColor    = visuals.Color{R: 110, G: 110, B: 140} // gray — drawing plane
)

// Lookup returns the in-process painting-scene registered under name, if it
// lives in this module binary.
func Lookup(name string) (Sink, bool) {
	s, ok := visuals.Lookup(name).(Sink)
	return s, ok
}

type stroke struct {
	pts    []r3.Vector
	active bool
}

type paintingScene struct {
	resource.Named
	visuals.SceneServiceBase
	logger logging.Logger

	mu      sync.Mutex
	plane   []r3.Vector
	strokes map[int]stroke
}

func newScene(
	ctx context.Context,
	deps resource.Dependencies,
	conf resource.Config,
	logger logging.Logger,
) (worldstatestore.Service, error) {
	s := &paintingScene{
		logger:  logger,
		strokes: map[int]stroke{},
	}
	s.Named = conf.ResourceName().AsNamed()
	s.SceneServiceBase.Hooks = s
	s.SceneServiceBase.Logger = logger
	s.SceneServiceBase.DefaultParentFrame = "world"
	// Rotate UUIDs on every update so re-added geometry isn't dropped by the
	// viewer's REMOVED-UUID cache (otherwise strokes only appear after a manual
	// 3D refresh). New UUID per change instead of updating the existing one.
	s.SceneServiceBase.DefaultUUIDStrategy = "versioned"
	if err := s.Reconfigure(ctx, deps, conf); err != nil {
		return nil, err
	}
	logger.Info("painting-scene visualizer ready")
	return s, nil
}

func (s *paintingScene) Reconfigure(_ context.Context, _ resource.Dependencies, conf resource.Config) error {
	// No items at startup — the driver pushes them at paint time.
	if err := s.SceneServiceBase.ReconfigureWith(nil, 0, "", "world"); err != nil {
		return err
	}
	visuals.Register(conf.ResourceName().Name, s)
	return nil
}

// DoCommand disambiguates the SceneServiceBase verb set from resource.Named.
func (s *paintingScene) DoCommand(ctx context.Context, cmd map[string]interface{}) (map[string]interface{}, error) {
	return s.SceneServiceBase.DoCommand(ctx, cmd)
}

func (s *paintingScene) Close(ctx context.Context) error {
	visuals.Unregister(s.Named.Name().Name)
	return s.SceneServiceBase.Close(ctx)
}

// ---- Sink implementation ----------------------------------------------------

func (s *paintingScene) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.plane = nil
	s.strokes = map[int]stroke{}
	s.rebuildLocked()
}

func (s *paintingScene) ShowPlane(corners []r3.Vector) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.plane = append([]r3.Vector(nil), corners...)
	s.rebuildLocked()
}

func (s *paintingScene) StartStroke(id int, pts []r3.Vector) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.strokes[id] = stroke{pts: append([]r3.Vector(nil), pts...), active: true}
	s.rebuildLocked()
}

func (s *paintingScene) FinishStroke(id int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if st, ok := s.strokes[id]; ok {
		st.active = false
		s.strokes[id] = st
		s.rebuildLocked()
	}
}

// rebuildLocked recomputes the full visual set and pushes it to the viewer.
// SetScene broadcasts REMOVED for the prior scene and ADDED for the new one,
// so each call updates any subscribed 3D viewer. Caller must hold s.mu.
func (s *paintingScene) rebuildLocked() {
	var vs []interface{}

	if len(s.plane) >= 2 {
		pts := make([]visuals.Pose, 0, len(s.plane)+1)
		for _, c := range s.plane {
			pts = append(pts, visuals.PoseAt(c.X, c.Y, c.Z, 0, 0, 1, 0))
		}
		pts = append(pts, pts[0]) // close the rectangle
		pc := planeColor
		vs = append(vs, &visuals.Line{
			LabelPrefix: "plane", Points: pts, WidthMM: 3, ParentFrame: "world", Color: &pc,
		})
	}

	idxs := make([]int, 0, len(s.strokes))
	for i := range s.strokes {
		idxs = append(idxs, i)
	}
	sort.Ints(idxs)
	for _, i := range idxs {
		st := s.strokes[i]
		if len(st.pts) < 2 {
			continue
		}
		pts := make([]visuals.Pose, 0, len(st.pts))
		for _, p := range st.pts {
			pts = append(pts, visuals.PoseAt(p.X, p.Y, p.Z, 0, 0, 1, 0))
		}
		col := finishedColor
		if st.active {
			col = activeColor
		}
		vs = append(vs, &visuals.Line{
			LabelPrefix: fmt.Sprintf("stroke_%03d", i), Points: pts, WidthMM: 5,
			ParentFrame: "world", Color: &col,
		})
	}

	if err := s.SceneServiceBase.SetScene(visuals.SetSceneOpts{ParentFrame: "world"}, vs...); err != nil {
		s.logger.Warnw("painting-scene SetScene failed", "err", err)
	}
}
