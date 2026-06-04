package controller

import (
	"errors"
	"math"

	"github.com/golang/geo/r3"
	"go.viam.com/rdk/spatialmath"
)

// vec3 is a JSON-friendly 3D vector used in the module config and DoCommand
// payloads. Units are millimeters for positions and unitless for directions.
type vec3 struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	Z float64 `json:"z"`
}

func (v vec3) r3() r3.Vector { return r3.Vector{X: v.X, Y: v.Y, Z: v.Z} }

func fromR3(v r3.Vector) vec3 { return vec3{X: v.X, Y: v.Y, Z: v.Z} }

// PlaneConfig describes the (adjustable) rectangular surface the arm draws on.
// It is intentionally intuitive to edit by hand or from the web UI: an origin
// corner, a size, and two direction hints.
//
// Image space is normalized: u in [0,1] runs left->right, v in [0,1] runs
// top->bottom. (u,v)=(0,0) maps to Origin (the image's top-left corner).
type PlaneConfig struct {
	// Origin is the world position (mm) of the image's top-left corner (u=0,v=0).
	Origin vec3 `json:"origin"`
	// WidthMM / HeightMM are the physical extents of the drawing area.
	WidthMM  float64 `json:"width_mm"`
	HeightMM float64 `json:"height_mm"`
	// Approach is the direction the tool/light points while drawing (into the
	// surface). Defaults to {1,0,0} (arm reaches forward along +X).
	Approach *vec3 `json:"approach,omitempty"`
	// Up is the world direction that maps to "up" in the image (-v). Defaults
	// to {0,0,1} (world Z up).
	Up *vec3 `json:"up,omitempty"`
}

// plane is the resolved, precomputed drawing plane used at runtime. All vectors
// are unit length and form a consistent right-handed basis.
type plane struct {
	origin   r3.Vector // world position of (u,v)=(0,0)
	width    float64   // mm along right
	height   float64   // mm along down
	approach r3.Vector // unit, tool pointing direction (into surface)
	nOut     r3.Vector // unit, outward surface normal (= -approach)
	right    r3.Vector // unit, +u direction in world
	down     r3.Vector // unit, +v direction in world
}

func defaultIfZero(v *vec3, def r3.Vector) r3.Vector {
	if v == nil {
		return def
	}
	r := v.r3()
	if r.Norm2() == 0 {
		return def
	}
	return r
}

// newPlane resolves a PlaneConfig into a runtime plane, deriving the in-plane
// axes from the approach and up hints.
func newPlane(cfg PlaneConfig) (*plane, error) {
	if cfg.WidthMM <= 0 || cfg.HeightMM <= 0 {
		return nil, errors.New("drawing_plane width_mm and height_mm must be > 0")
	}

	approach := defaultIfZero(cfg.Approach, r3.Vector{X: 1}).Normalize()
	up := defaultIfZero(cfg.Up, r3.Vector{Z: 1}).Normalize()
	nOut := approach.Mul(-1) // outward normal points back toward the arm

	// right = up x nOut, down = right x nOut. This yields a right-handed frame
	// where +u points "right" and +v points "down" in the image.
	right := up.Cross(nOut)
	if right.Norm2() < 1e-9 {
		// up is (anti)parallel to the normal; pick an arbitrary in-plane axis.
		right = nOut.Cross(r3.Vector{X: 1})
		if right.Norm2() < 1e-9 {
			right = nOut.Cross(r3.Vector{Y: 1})
		}
	}
	right = right.Normalize()
	down := right.Cross(nOut).Normalize()

	return &plane{
		origin:   cfg.Origin.r3(),
		width:    cfg.WidthMM,
		height:   cfg.HeightMM,
		approach: approach,
		nOut:     nOut,
		right:    right,
		down:     down,
	}, nil
}

// config reconstructs the PlaneConfig that produced this plane (used by the
// get_plane DoCommand so the web UI can read the current geometry).
func (p *plane) config() PlaneConfig {
	approach := fromR3(p.approach)
	up := fromR3(p.down.Mul(-1)) // image "up" is -down
	return PlaneConfig{
		Origin:   fromR3(p.origin),
		WidthMM:  p.width,
		HeightMM: p.height,
		Approach: &approach,
		Up:       &up,
	}
}

// point returns the world position (mm) for a normalized image coordinate.
func (p *plane) point(u, v float64) r3.Vector {
	return p.origin.
		Add(p.right.Mul(u * p.width)).
		Add(p.down.Mul(v * p.height))
}

// orientation is the constant tool orientation while drawing: the tool points
// along the approach direction (into the surface).
func (p *plane) orientation() spatialmath.Orientation {
	return &spatialmath.OrientationVector{
		OX: p.approach.X, OY: p.approach.Y, OZ: p.approach.Z, Theta: 0,
	}
}

// poseAt returns the task-space pose for a normalized image coordinate, with
// the tool on the surface.
func (p *plane) poseAt(u, v float64) spatialmath.Pose {
	return spatialmath.NewPose(p.point(u, v), p.orientation())
}

// liftedPoseAt returns the pose for a normalized coordinate retracted liftMM
// along the outward normal (used for light-off travel moves between strokes).
func (p *plane) liftedPoseAt(u, v, liftMM float64) spatialmath.Pose {
	pos := p.point(u, v).Add(p.nOut.Mul(liftMM))
	return spatialmath.NewPose(pos, p.orientation())
}

// aspect returns width/height, the ratio the web UI uses to letterbox images.
func (p *plane) aspect() float64 {
	if p.height == 0 {
		return math.NaN()
	}
	return p.width / p.height
}
