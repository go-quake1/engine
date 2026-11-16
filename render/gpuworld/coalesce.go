// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package gpuworld

import "slices"

// DefaultCoalesceSlack is how many bytes of unchanged texels one upload may
// carry to save another: two re-baked surfaces on the same shelf of an atlas
// become one write when the gap between them, times the height of the write,
// is at most this. A write costs a host crossing's worth of work whatever its
// size (about 20 microseconds in the browser, 120 a frame with three dynamic
// lights around the view), which a few KB of copying does not come near.
const DefaultCoalesceSlack = 16 << 10

// Write is one upload command: the R.W x R.H texels at (R.X, R.Y) of the atlas
// of level 0 (Low false) or of levels 1 to 3, their rows contiguous in Pix.
type Write struct {
	Low bool
	R   Rect
	Pix []byte
}

// Coalesce turns the surfaces a frame re-baked into as few writes as keep the
// bytes sent near what changed. The atlases (Atlas.Pixels) already hold the new
// surfaces, so a write may cover texels between two surfaces: it sends what the
// atlas holds, which is what the GPU's atlas holds after the other writes.
//
// The surfaces of a shelf share a Y (the shelf packer puts a rectangle at the
// shelf's row); in each atlas they are sorted by Y then X, and a surface joins
// the run before it when it is on the same row and the gap, times the height
// the run then has, is at most slack bytes. A run is one write of the bounding
// rectangle of its surfaces; a run of one is the surface itself, not copied.
// out and buf are reused (buf grows by allocating a new one, so the Pix of the
// writes already made stay valid).
func (g *Geometry) Coalesce(updates []Update, slack int, out []Write, buf []byte) ([]Write, []byte) {
	out = out[:0]
	buf = buf[:0]
	if slack < 0 {
		slack = 0
	}
	g.sorted = append(g.sorted[:0], updates...)
	slices.SortFunc(g.sorted, func(a, b Update) int {
		switch {
		case a.Low != b.Low:
			if a.Low {
				return 1
			}
			return -1
		case a.R.Y != b.R.Y:
			return int(a.R.Y) - int(b.R.Y)
		}
		return int(a.R.X) - int(b.R.X)
	})
	us := g.sorted
	for i := 0; i < len(us); {
		first := us[i]
		x0, x1, h := first.R.X, first.R.X+first.R.W, first.R.H
		j := i + 1
		for ; j < len(us); j++ {
			u := us[j]
			if u.Low != first.Low || u.R.Y != first.R.Y {
				break
			}
			// the gap, and the height the run would have with u in it
			nh := max(h, u.R.H)
			gap := 0
			if u.R.X > x1 {
				gap = int(u.R.X - x1)
			}
			if gap*int(nh) > slack {
				break
			}
			x1, h = max(x1, u.R.X+u.R.W), nh
		}
		if j == i+1 {
			out = append(out, Write{Low: first.Low, R: first.R, Pix: first.Pix})
			i = j
			continue
		}
		w := int(x1 - x0)
		n := w * int(h)
		if start := len(buf); start+n > cap(buf) {
			buf = make([]byte, 0, max(2*cap(buf), n, 1<<16))
		}
		start := len(buf)
		buf = buf[:start+n]
		at := g.Surf
		if first.Low {
			at = g.SurfLow
		}
		for y := 0; y < int(h); y++ {
			copy(buf[start+y*w:start+(y+1)*w], at.Pixels[(int(first.R.Y)+y)*at.Width+int(x0):])
		}
		if g.PlantCoalesceDefect {
			clear(buf[start : start+n])
		}
		out = append(out, Write{Low: first.Low, R: Rect{X: x0, Y: first.R.Y, W: uint32(w), H: h}, Pix: buf[start : start+n : start+n]})
		i = j
	}
	return out, buf
}
