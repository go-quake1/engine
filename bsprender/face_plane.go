// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package bsprender

import (
	"math"

	"github.com/go-quake1/engine/render"
)

// SurfacePlaneOf describes a face for [render.NewSurfaceGradients] (Quake's
// D_CalcGradients): its plane from its vertices (Newell's method), its
// texture axes and offsets, its texturemins and its extents, the latter two
// from the face's lightmap geometry ([FaceLightmapInfo]).
func SurfacePlaneOf(fv FaceVerts, info LightmapInfo) render.SurfacePlane {
	var n [3]float64
	for i := 0; i < fv.NumVerts; i++ {
		a, b := fv.Vert(i), fv.Vert((i+1)%fv.NumVerts)
		n[0] += float64(a[1]-b[1]) * float64(a[2]+b[2])
		n[1] += float64(a[2]-b[2]) * float64(a[0]+b[0])
		n[2] += float64(a[0]-b[0]) * float64(a[1]+b[1])
	}
	l := math.Sqrt(n[0]*n[0] + n[1]*n[1] + n[2]*n[2])
	p := render.SurfacePlane{AxisS: fv.UVAxisS, OffS: fv.UVOffS, AxisT: fv.UVAxisT, OffT: fv.UVOffT,
		MinS: float32(info.MinS), MinT: float32(info.MinT), ExtS: (info.Width - 1) * 16, ExtT: (info.Height - 1) * 16}
	if l == 0 {
		return p
	}
	for i := range n {
		n[i] /= l
		p.Normal[i] = float32(n[i])
	}
	v0 := fv.Vert(0)
	p.Dist = float32(n[0]*float64(v0[0]) + n[1]*float64(v0[1]) + n[2]*float64(v0[2]))
	return p
}
