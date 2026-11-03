// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package bsprender

import (
	"math"
	"testing"
)

func TestSurfacePlaneOf(t *testing.T) {
	pts := [][3]float32{{0, 0, 7}, {64, 0, 7}, {64, 32, 7}, {0, 32, 7}}
	fv := FaceVerts{NumVerts: 4, Vert: func(i int) [3]float32 { return pts[i] }, UVAxisS: [3]float32{1, 0, 0}, UVOffS: 3, UVAxisT: [3]float32{0, 1, 0}, UVOffT: 4}
	p := SurfacePlaneOf(fv, LightmapInfo{Width: 5, Height: 3, MinS: -16, MinT: 0})
	if math.Abs(float64(p.Normal[2])) != 1 || p.Normal[0] != 0 || p.Normal[1] != 0 {
		t.Errorf("normal %v", p.Normal)
	}
	if math.Abs(float64(p.Dist)) != 7 {
		t.Errorf("dist %v", p.Dist)
	}
	if p.ExtS != 64 || p.ExtT != 32 || p.MinS != -16 || p.OffS != 3 || p.OffT != 4 {
		t.Errorf("%+v", p)
	}
	// all points equal: no plane, the texture parts are kept
	same := FaceVerts{NumVerts: 3, Vert: func(int) [3]float32 { return [3]float32{1, 2, 3} }}
	if q := SurfacePlaneOf(same, LightmapInfo{Width: 1, Height: 1}); q.Normal != ([3]float32{}) || q.Dist != 0 {
		t.Errorf("degenerate: %+v", q)
	}
}
