// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package worldview

import (
	"math"
	"sort"

	"github.com/go-quake1/engine/bsprender"
)

// AnimatedViews returns up to n views that look at animated faces from close
// by, the views where the light styles and the '+' textures are what the
// picture shows. A face is animated when one of its light styles moves under
// [LightStyleValue] (styles 1 to 11), or its texture is a frame of a '+' chain
// of more than one frame. Candidates are the largest animated faces; each view stands
// off the face along its normal, on whichever side is inside the map and sees
// the face, and looks at its centre. Two views are at least 128 units apart.
func (w *World) AnimatedViews(n int) []View {
	chains := BuildMiptexChains(w.TexNames)
	// Faces of animated textures and faces of animated lights, each largest
	// first, taken in turn so that both kinds are among the views.
	var styled, textured []*Face
	for _, f := range w.Faces {
		if f == nil {
			continue
		}
		if chains.ChainLen(f.MipTex) > 1 {
			textured = append(textured, f)
			continue
		}
		for _, s := range f.Surf.Desc.Styles {
			if StyleAnimates(s) {
				styled = append(styled, f)
				break
			}
		}
	}
	for _, l := range [][]*Face{styled, textured} {
		sort.SliceStable(l, func(i, j int) bool { return l[i].Radius > l[j].Radius })
	}
	var cand []*Face
	for i := 0; i < len(styled) || i < len(textured); i++ {
		if i < len(textured) {
			cand = append(cand, textured[i])
		}
		if i < len(styled) {
			cand = append(cand, styled[i])
		}
	}
	var out []View
	for _, f := range cand {
		if len(out) >= n {
			break
		}
		near := false
		for _, v := range out {
			dx, dy, dz := v.Origin[0]-f.Centre[0], v.Origin[1]-f.Centre[1], v.Origin[2]-f.Centre[2]
			near = near || dx*dx+dy*dy+dz*dz < 128*128
		}
		if near {
			continue
		}
		nm := bsprender.SurfacePlaneOf(f.Verts, f.Info).Normal
		for _, d := range []float32{96, 160, 64} {
			var found bool
			for _, sign := range []float32{1, -1} {
				o := [3]float32{f.Centre[0] + sign*d*nm[0], f.Centre[1] + sign*d*nm[1], f.Centre[2] + sign*d*nm[2]}
				if w.BM.PointInLeaf(o) <= 0 {
					continue
				}
				v := View{Origin: o, Angles: lookAt(o, f.Centre)}
				vis, err := w.Visible(v, 320, 200)
				if err != nil {
					continue
				}
				for _, i := range vis {
					if i == f.Idx {
						found = true
					}
				}
				if found {
					out = append(out, v)
					break
				}
			}
			if found {
				break
			}
		}
	}
	return out
}

// lookAt is the (pitch, yaw, roll) of a camera at from looking at to: Quake's
// pitch is positive downward.
func lookAt(from, to [3]float32) [3]float32 {
	dx, dy, dz := float64(to[0]-from[0]), float64(to[1]-from[1]), float64(to[2]-from[2])
	yaw := math.Atan2(dy, dx) * 180 / math.Pi
	pitch := -math.Atan2(dz, math.Hypot(dx, dy)) * 180 / math.Pi
	return [3]float32{float32(pitch), float32(yaw), 0}
}
