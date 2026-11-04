// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package worldview

import (
	"math"
	"testing"

	"github.com/go-quake1/engine/bsprender"
	"github.com/go-quake1/engine/render"
)

// identity maps world to view unchanged: x right, y up, z forward.
func identity() render.Affine {
	var a render.Affine
	a.R[0][0], a.R[1][1], a.R[2][2] = 1, 1, 1
	return a
}

func polygon(pts ...[3]float32) bsprender.FaceVerts {
	return bsprender.FaceVerts{NumVerts: len(pts), Vert: func(i int) [3]float32 { return pts[i] }}
}

func square(z float32) bsprender.FaceVerts {
	return polygon([3]float32{-10, -10, z}, [3]float32{10, -10, z}, [3]float32{10, 10, z}, [3]float32{-10, 10, z})
}

func TestNearZIInsideTheFrustum(t *testing.T) {
	zi, ok := NearZI(identity(), square(100), 1, 0.625)
	if !ok || zi != 0.01 {
		t.Fatalf("zi %v ok %v, want 0.01", zi, ok)
	}
}

// A floor 50 below the eye: the bottom edge of the screen sees it from
// z = 50 / 0.625 = 80, so its nearest visible 1/z is 1/80 whatever its
// vertices at z = 5 say.
func TestNearZIClipsToTheFrustum(t *testing.T) {
	floor := polygon([3]float32{-500, -50, 5}, [3]float32{500, -50, 5}, [3]float32{500, -50, 1000}, [3]float32{-500, -50, 1000})
	zi, ok := NearZI(identity(), floor, 1, 0.625)
	if !ok || math.Abs(float64(zi)-1.0/80) > 1e-6 {
		t.Fatalf("zi %v ok %v, want 1/80 = 0.0125", zi, ok)
	}
}

func TestNearZINothingLeft(t *testing.T) {
	if _, ok := NearZI(identity(), square(2), 1, 0.625); ok {
		t.Error("a face wholly in front of the near plane (z = 2 < 16) has no visible part")
	}
	if _, ok := NearZI(identity(), square(-50), 1, 0.625); ok {
		t.Error("a face behind the eye")
	}
	// wholly to the right of the frustum
	right := polygon([3]float32{500, 0, 100}, [3]float32{600, 0, 100}, [3]float32{600, 10, 100})
	if _, ok := NearZI(identity(), right, 1, 0.625); ok {
		t.Error("a face outside the side plane")
	}
}

// 320 wide at 90 degrees: scale_for_mip 160, so nearzi * 160 * mipadjust is
// 1.6 at z = 100 (mip 0), 0.32 at z = 500 (mip 2), 0.16 at z = 1000 (mip 3);
// mipadjust 2 doubles it (0.64 at z = 500: mip 1), 4 at z = 1000 gives 0.64.
func TestMipViewLevel(t *testing.T) {
	mv := NewMipView(320, 200, 90)
	if mv.Scale != 160 || mv.TX != 1 || math.Abs(float64(mv.TY)-0.625) > 1e-6 {
		t.Fatalf("view: %+v", mv)
	}
	for _, c := range []struct {
		z    float32
		adj  float32
		want int
	}{
		{100, 1, 0}, {500, 1, 2}, {500, 2, 1}, {1000, 1, 3}, {1000, 4, 1}, {160, 1, 0}, {161, 1, 1},
		{399, 1, 1}, {401, 1, 2}, {799, 1, 2}, {801, 1, 3}, // the 0.4 and 0.2 thresholds, 1 / 400 and 1 / 800 of a texel
	} {
		f := &Face{Verts: square(c.z), MipAdjust: c.adj}
		if got := mv.Level(identity(), f); got != c.want {
			t.Errorf("z %v mipadjust %v: level %d, want %d", c.z, c.adj, got, c.want)
		}
	}
	if got := mv.Level(identity(), &Face{Verts: square(-50), MipAdjust: 1}); got != 3 {
		t.Errorf("an invisible face: level %d, want 3", got)
	}
	// d_mipcap 2 raises the floor.
	mv.MinLevel = 2
	if got := mv.Level(identity(), &Face{Verts: square(100), MipAdjust: 1}); got != 2 {
		t.Errorf("d_mipcap 2: level %d", got)
	}
}
