// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package gpuworld

import (
	"bytes"
	"testing"

	"github.com/go-quake1/engine/render"
)

// synthGeo is a geometry with two 64 x 16 atlases whose texel (x, y) is
// (x + 3y) mod 251 + 1, and an update for each rectangle (the atlas already
// holds the new surface, as after a bake).
func synthGeo() *Geometry {
	mk := func() *Atlas {
		a := &Atlas{Width: 64, Height: 16, Pixels: make([]byte, 64*16)}
		for y := 0; y < 16; y++ {
			for x := 0; x < 64; x++ {
				a.Pixels[y*64+x] = byte((x+3*y)%251 + 1)
			}
		}
		return a
	}
	return &Geometry{Surf: mk(), SurfLow: mk()}
}

func upd(g *Geometry, low bool, x, y, w, h uint32) Update {
	at := g.Surf
	if low {
		at = g.SurfLow
	}
	px := make([]byte, w*h)
	for r := uint32(0); r < h; r++ {
		copy(px[r*w:(r+1)*w], at.Pixels[(y+r)*uint32(at.Width)+x:])
	}
	return Update{Low: low, R: Rect{X: x, Y: y, W: w, H: h}, Pix: px}
}

// TestCoalesce: the writes of a shelf close enough join into one write of their
// bounding rectangle with the atlas' texels in it; the others stay as they were.
//
// Rectangles are (x, y, w, h). Slack 64 bytes.
//
//	(0,0,8,4) and (10,0,8,2): same shelf (y 0), gap 2, height 4: 8 bytes <= 64: one
//	  write (0,0,18,4).
//	(40,0,8,4): gap 22 from x 18, height 4: 88 > 64: its own write.
//	(0,8,8,4) alone on its shelf; (0,0,8,4) of the other atlas is not joined.
func TestCoalesce(t *testing.T) {
	g := synthGeo()
	us := []Update{
		upd(g, false, 10, 0, 8, 2), // out of order on purpose
		upd(g, false, 40, 0, 8, 4),
		upd(g, false, 0, 0, 8, 4),
		upd(g, false, 0, 8, 8, 4),
		upd(g, true, 0, 0, 8, 4),
	}
	out, _ := g.Coalesce(us, 64, nil, nil)
	want := []struct {
		low        bool
		x, y, w, h uint32
	}{
		{false, 0, 0, 18, 4}, {false, 40, 0, 8, 4}, {false, 0, 8, 8, 4}, {true, 0, 0, 8, 4},
	}
	if len(out) != len(want) {
		t.Fatalf("%d writes, want %d: %+v", len(out), len(want), out)
	}
	for i, w := range want {
		o := out[i]
		if o.Low != w.low || o.R != (Rect{X: w.x, Y: w.y, W: w.w, H: w.h}) {
			t.Errorf("write %d: %+v, want %+v", i, o, w)
		}
		at := g.Surf
		if o.Low {
			at = g.SurfLow
		}
		for y := uint32(0); y < o.R.H; y++ {
			got := o.Pix[y*o.R.W : (y+1)*o.R.W]
			exp := at.Pixels[(o.R.Y+y)*uint32(at.Width)+o.R.X:][:o.R.W]
			if !bytes.Equal(got, exp) {
				t.Errorf("write %d row %d: %v, want the atlas' %v", i, y, got, exp)
			}
		}
	}
	// slack 0 joins only rectangles that touch or overlap in x; 1<<20 joins a shelf
	if out, _ := g.Coalesce(us, 0, nil, nil); len(out) != 5 {
		t.Errorf("slack 0: %d writes, want 5 (the two on y 0 are 2 apart)", len(out))
	}
	if out, _ := g.Coalesce(us, 1<<20, nil, nil); len(out) != 3 {
		t.Errorf("a large slack: %d writes, want 3 (one per shelf and atlas)", len(out))
	}
	if out, _ := g.Coalesce(nil, 64, nil, nil); len(out) != 0 {
		t.Errorf("no updates: %d writes", len(out))
	}
	// the planted defect zeroes the merged write only
	g.PlantCoalesceDefect = true
	out, _ = g.Coalesce(us, 64, nil, nil)
	if !bytes.Equal(out[0].Pix, make([]byte, len(out[0].Pix))) || bytes.Equal(out[1].Pix, make([]byte, len(out[1].Pix))) {
		t.Error("the planted defect does not zero exactly the merged writes")
	}
}

// TestCoalesceBufferGrowth: a buffer too small for the merged writes grows by a
// new allocation, and the writes already made keep their texels.
func TestCoalesceBufferGrowth(t *testing.T) {
	g := synthGeo()
	us := []Update{upd(g, false, 0, 0, 4, 4), upd(g, false, 6, 0, 4, 4), upd(g, false, 0, 8, 4, 4), upd(g, false, 6, 8, 4, 4)}
	out, buf := g.Coalesce(us, 1<<20, nil, make([]byte, 0, 1))
	if len(out) != 2 || len(buf) != 2*10*4 {
		t.Fatalf("%d writes, %d staged bytes", len(out), len(buf))
	}
	for i, o := range out {
		for y := uint32(0); y < o.R.H; y++ {
			if !bytes.Equal(o.Pix[y*o.R.W:(y+1)*o.R.W], g.Surf.Pixels[(o.R.Y+y)*64+o.R.X:][:o.R.W]) {
				t.Errorf("write %d row %d changed after the buffer grew", i, y)
			}
		}
	}
}

// TestCoalescedUploadsAreWhatTheAtlasHolds (needs QUAKE_PAK): over the 16 fixed
// views of e1m1 with the three dynamic lights of a rocket launcher (the
// worst case of the dynamic-light measurement), applying a frame's coalesced
// writes to the atlases as the GPU had them after the previous frame gives exactly
// the atlases the CPU holds, and the frame takes far fewer writes than surfaces.
func TestCoalescedUploadsAreWhatTheAtlasHolds(t *testing.T) {
	w, cm := loadE1M1(t)
	g, err := Build(w, cm)
	if err != nil {
		t.Fatal(err)
	}
	gpu := [2][]byte{append([]byte(nil), g.Surf.Pixels...), append([]byte(nil), g.SurfLow.Pixels...)}
	var draws []FaceDraw
	surfaces, writes, bytesSent, bytesChanged, frames := 0, 0, 0, 0, 0
	var out []Write
	var buf []byte
	slacks := []int{0, 4 << 10, 16 << 10, 64 << 10, 256 << 10, 1 << 20}
	sw, sb := make([]int, len(slacks)), make([]int, len(slacks))
	for k, v := range w.Views() {
		yaw := float64(v.Angles[1]) * 3.141592653589793 / 180
		_ = yaw
		w.DLights = [render.MaxDLights]render.DLight{}
		w.DLights[0] = render.DLight{Origin: [3]float32{v.Origin[0] + 18, v.Origin[1], v.Origin[2] + 16}, Radius: 220, MinLight: 32, Die: 0.1}
		w.DLights[1] = render.DLight{Origin: [3]float32{v.Origin[0] + 160, v.Origin[1], v.Origin[2]}, Radius: 200, Die: 0.01}
		w.DLights[2] = render.DLight{Origin: [3]float32{v.Origin[0] + 320, v.Origin[1], v.Origin[2] + 40}, Radius: 350, Die: 0.5, Decay: 300}
		if draws, err = g.Draws(w, v, 1280, 800, draws, Chosen); err != nil {
			t.Fatal(err)
		}
		out, buf = g.Coalesce(g.Updates, DefaultCoalesceSlack, out, buf)
		for _, o := range out {
			i, at := 0, g.Surf
			if o.Low {
				i, at = 1, g.SurfLow
			}
			for y := 0; y < int(o.R.H); y++ {
				copy(gpu[i][(int(o.R.Y)+y)*at.Width+int(o.R.X):][:o.R.W], o.Pix[y*int(o.R.W):][:o.R.W])
			}
			bytesSent += len(o.Pix)
		}
		for _, u := range g.Updates {
			bytesChanged += len(u.Pix)
		}
		if !bytes.Equal(gpu[0], g.Surf.Pixels) || !bytes.Equal(gpu[1], g.SurfLow.Pixels) {
			t.Fatalf("view %d: the GPU's atlas differs from the CPU's after %d writes for %d surfaces", k, len(out), len(g.Updates))
		}
		for i, s := range slacks {
			o, _ := g.Coalesce(g.Updates, s, nil, nil)
			sw[i] += len(o)
			for _, x := range o {
				sb[i] += len(x.Pix)
			}
		}
		surfaces += len(g.Updates)
		writes += len(out)
		frames++
	}
	for i, s := range slacks {
		t.Logf("slack %7d: %d writes, %d bytes", s, sw[i], sb[i])
	}
	if surfaces == 0 || writes >= surfaces {
		t.Fatalf("%d surfaces in %d writes: nothing was coalesced", surfaces, writes)
	}
	t.Logf("%d frames: %d surfaces (%d bytes) in %d writes (%d bytes); %.1f surfaces a write, %.2fx the bytes",
		frames, surfaces, bytesChanged, writes, bytesSent, float64(surfaces)/float64(writes), float64(bytesSent)/float64(bytesChanged))
}
