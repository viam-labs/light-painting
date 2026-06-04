package controller

import (
	"math"
	"testing"

	"github.com/golang/geo/r3"
)

// defaultTestPlane: vertical plane in front of the arm, approach +X, up +Z.
// origin (top-left) at (300,150,500), 300mm square.
func defaultTestPlane(t *testing.T) *plane {
	t.Helper()
	pl, err := newPlane(PlaneConfig{
		Origin:   vec3{X: 300, Y: 150, Z: 500},
		WidthMM:  300,
		HeightMM: 300,
	})
	if err != nil {
		t.Fatalf("newPlane: %v", err)
	}
	return pl
}

func approxEq(a, b r3.Vector) bool {
	return a.Sub(b).Norm() < 1e-6
}

func TestPlaneCornerMapping(t *testing.T) {
	pl := defaultTestPlane(t)
	cases := []struct {
		name string
		u, v float64
		want r3.Vector
	}{
		{"top-left", 0, 0, r3.Vector{X: 300, Y: 150, Z: 500}},
		{"top-right", 1, 0, r3.Vector{X: 300, Y: -150, Z: 500}},
		{"bottom-left", 0, 1, r3.Vector{X: 300, Y: 150, Z: 200}},
		{"bottom-right", 1, 1, r3.Vector{X: 300, Y: -150, Z: 200}},
		{"center", 0.5, 0.5, r3.Vector{X: 300, Y: 0, Z: 350}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := pl.point(tc.u, tc.v)
			if !approxEq(got, tc.want) {
				t.Errorf("point(%.1f,%.1f) = %v, want %v", tc.u, tc.v, got, tc.want)
			}
		})
	}
}

func TestPlaneMirrorFlipsU(t *testing.T) {
	pl, err := newPlane(PlaneConfig{
		Origin: vec3{X: 300, Y: 150, Z: 500}, WidthMM: 300, HeightMM: 300, Mirror: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Mirrored: u=0 maps to where u=1 lands un-mirrored, and v is unchanged.
	if got, want := pl.point(0, 0), (r3.Vector{X: 300, Y: -150, Z: 500}); !approxEq(got, want) {
		t.Errorf("mirrored point(0,0) = %v, want %v", got, want)
	}
	if got, want := pl.point(1, 0), (r3.Vector{X: 300, Y: 150, Z: 500}); !approxEq(got, want) {
		t.Errorf("mirrored point(1,0) = %v, want %v", got, want)
	}
	if got, want := pl.point(0.5, 1), (r3.Vector{X: 300, Y: 0, Z: 200}); !approxEq(got, want) {
		t.Errorf("mirrored point(0.5,1) = %v, want %v", got, want)
	}
}

func TestPlaneYawRotatesAboutWorldZ(t *testing.T) {
	// Default approach +X; yaw 90° about world Z -> approach points +Y.
	pl, err := newPlane(PlaneConfig{
		Origin: vec3{X: 300, Y: 150, Z: 500}, WidthMM: 300, HeightMM: 300, YawDeg: 90,
	})
	if err != nil {
		t.Fatal(err)
	}
	ov := pl.orientation().OrientationVectorRadians()
	got := r3.Vector{X: ov.OX, Y: ov.OY, Z: ov.OZ}
	if !approxEq(got, r3.Vector{Y: 1}) {
		t.Errorf("yawed approach = %v, want +Y", got)
	}
	// config() round-trips the un-yawed hint + the yaw value.
	cfg := pl.config()
	if cfg.YawDeg != 90 {
		t.Errorf("config yaw = %v, want 90", cfg.YawDeg)
	}
	if a := cfg.Approach; a == nil || !approxEq(a.r3(), r3.Vector{X: 1}) {
		t.Errorf("config approach = %v, want un-yawed +X", cfg.Approach)
	}
	// Origin is unchanged by yaw.
	if !approxEq(pl.point(0, 0), r3.Vector{X: 300, Y: 150, Z: 500}) {
		t.Errorf("yaw should not move the origin, got %v", pl.point(0, 0))
	}
}

func TestPlaneOrientationPointsAlongApproach(t *testing.T) {
	pl := defaultTestPlane(t)
	ov := pl.orientation().OrientationVectorRadians()
	want := r3.Vector{X: 1}
	got := r3.Vector{X: ov.OX, Y: ov.OY, Z: ov.OZ}
	if !approxEq(got, want) {
		t.Errorf("orientation pointing = %v, want %v", got, want)
	}
}

func TestPlaneLiftRetractsAlongOutwardNormal(t *testing.T) {
	pl := defaultTestPlane(t)
	// Outward normal is -approach = (-1,0,0); lifting 50mm moves x from 300 -> 250.
	lifted := pl.liftedPoseAt(0, 0, 50)
	want := r3.Vector{X: 250, Y: 150, Z: 500}
	if !approxEq(lifted.Point(), want) {
		t.Errorf("liftedPoseAt(0,0,50) = %v, want %v", lifted.Point(), want)
	}
}

func TestPlaneAspect(t *testing.T) {
	pl := defaultTestPlane(t)
	if math.Abs(pl.aspect()-1.0) > 1e-9 {
		t.Errorf("aspect = %v, want 1.0", pl.aspect())
	}
	wide, err := newPlane(PlaneConfig{Origin: vec3{}, WidthMM: 400, HeightMM: 200})
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(wide.aspect()-2.0) > 1e-9 {
		t.Errorf("aspect = %v, want 2.0", wide.aspect())
	}
}

func TestPlaneAxesAreOrthonormalRightHanded(t *testing.T) {
	pl := defaultTestPlane(t)
	for _, v := range []r3.Vector{pl.right, pl.down, pl.nOut} {
		if math.Abs(v.Norm()-1.0) > 1e-9 {
			t.Errorf("axis %v not unit length (norm=%v)", v, v.Norm())
		}
	}
	// right x down should equal -nOut (so that right,down,nOut is right-handed
	// with nOut the outward normal): right x down points into the surface.
	cross := pl.right.Cross(pl.down)
	if !approxEq(cross, pl.nOut.Mul(-1)) {
		t.Errorf("right x down = %v, want %v (-nOut)", cross, pl.nOut.Mul(-1))
	}
}

func TestNewPlaneRejectsNonPositiveSize(t *testing.T) {
	if _, err := newPlane(PlaneConfig{WidthMM: 0, HeightMM: 100}); err == nil {
		t.Error("expected error for width_mm = 0")
	}
	if _, err := newPlane(PlaneConfig{WidthMM: 100, HeightMM: -1}); err == nil {
		t.Error("expected error for negative height_mm")
	}
}

func TestPlaneConfigRoundTrip(t *testing.T) {
	pl := defaultTestPlane(t)
	cfg := pl.config()
	pl2, err := newPlane(cfg)
	if err != nil {
		t.Fatalf("newPlane(config()): %v", err)
	}
	for _, uv := range [][2]float64{{0, 0}, {1, 0}, {0, 1}, {0.3, 0.7}} {
		a := pl.point(uv[0], uv[1])
		b := pl2.point(uv[0], uv[1])
		if !approxEq(a, b) {
			t.Errorf("round-trip mismatch at (%.2f,%.2f): %v vs %v", uv[0], uv[1], a, b)
		}
	}
}

func TestDownsampleKeepsEndpoints(t *testing.T) {
	pl := defaultTestPlane(t)
	// Many points within a tiny region; min segment 5mm should collapse them but
	// always keep first and last.
	pts := []pointJSON{}
	for i := 0; i < 100; i++ {
		pts = append(pts, pointJSON{U: float64(i) * 0.0001, V: 0})
	}
	kept := downsample(pl, pts, 5)
	if len(kept) < 2 {
		t.Fatalf("expected at least first+last, got %d", len(kept))
	}
	if kept[0] != pts[0] {
		t.Errorf("first point not kept")
	}
	if kept[len(kept)-1] != pts[len(pts)-1] {
		t.Errorf("last point not kept")
	}
}
