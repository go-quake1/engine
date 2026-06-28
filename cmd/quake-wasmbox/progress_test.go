// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package main

import (
	"image"
	"image/color"
	"testing"
)

// rgbAt returns the (R,G,B) byte triple at (x,y) in an RGBA32 row-major
// slice of width w. Helper for assertions; isolated so the test code
// reads top-down.
func rgbAt(rgba []byte, w, x, y int) (byte, byte, byte) {
	off := (y*w + x) * 4
	return rgba[off+0], rgba[off+1], rgba[off+2]
}

func TestPaintLoadingBar_BackgroundCleared(t *testing.T) {
	// Pre-fill with garbage; paint must overwrite EVERY pixel with the
	// Adwaita background colour outside the bar's footprint.
	const w, h = 320, 240
	rgba := make([]byte, w*h*4)
	for i := range rgba {
		rgba[i] = 0xAA // garbage value distinct from all bar colours
	}
	paintLoadingBar(rgba, w, h, 50, 100)
	// Sample a corner pixel -- safely outside the centred bar.
	r, g, b := rgbAt(rgba, w, 0, 0)
	if r != barBGR || g != barBGG || b != barBGB {
		t.Fatalf("corner pixel: (%d,%d,%d) want (%d,%d,%d)", r, g, b, barBGR, barBGG, barBGB)
	}
}

func TestPaintLoadingBar_TrackAndFill(t *testing.T) {
	const w, h = 320, 240
	rgba := make([]byte, w*h*4)
	// 25% fill: leftmost quarter of the bar should be Adwaita blue,
	// the rest of the bar should be the lighter track grey.
	paintLoadingBar(rgba, w, h, 25, 100)
	// Bar geometry must match the constants in paint.go.
	x0 := (w - barWidth) / 2
	y0 := (h - barHeight) / 2
	midY := y0 + barHeight/2

	// Sample at the very start of the bar -- must be FILL.
	r, g, b := rgbAt(rgba, w, x0+1, midY)
	if r != barFillR || g != barFillG || b != barFillB {
		t.Fatalf("fill-region pixel: (%d,%d,%d) want (%d,%d,%d)", r, g, b, barFillR, barFillG, barFillB)
	}
	// Sample at 90% of the bar -- must be TRACK.
	r, g, b = rgbAt(rgba, w, x0+barWidth*90/100, midY)
	if r != barTrackR || g != barTrackG || b != barTrackB {
		t.Fatalf("track-region pixel: (%d,%d,%d) want (%d,%d,%d)", r, g, b, barTrackR, barTrackG, barTrackB)
	}
}

func TestPaintLoadingBar_FractionGrows(t *testing.T) {
	// Three snapshots -- 10%, 50%, 90% -- the count of filled-blue
	// pixels along the bar's centre row must strictly grow.
	const w, h = 320, 240
	frames := []int64{10, 50, 90}
	prev := -1
	for _, pct := range frames {
		rgba := make([]byte, w*h*4)
		paintLoadingBar(rgba, w, h, pct, 100)
		x0 := (w - barWidth) / 2
		midY := h / 2
		count := 0
		for x := x0; x < x0+barWidth; x++ {
			r, g, b := rgbAt(rgba, w, x, midY)
			if r == barFillR && g == barFillG && b == barFillB {
				count++
			}
		}
		if count <= prev {
			t.Fatalf("fill count for pct=%d (%d) did not grow over previous (%d)", pct, count, prev)
		}
		prev = count
	}
}

func TestPaintLoadingBar_ZeroTotalDrawsTrackOnly(t *testing.T) {
	const w, h = 320, 240
	rgba := make([]byte, w*h*4)
	paintLoadingBar(rgba, w, h, 100, 0)
	x0 := (w - barWidth) / 2
	midY := h / 2
	// Every pixel inside the bar must be TRACK -- the fill branch is
	// gated on total>0.
	for x := x0; x < x0+barWidth; x++ {
		r, g, b := rgbAt(rgba, w, x, midY)
		if r == barFillR && g == barFillG && b == barFillB {
			t.Fatalf("unexpected fill at x=%d when total=0", x)
		}
		if r != barTrackR || g != barTrackG || b != barTrackB {
			t.Fatalf("expected track at x=%d, got (%d,%d,%d)", x, r, g, b)
		}
	}
}

func TestPaintLoadingBar_ClampsReceived(t *testing.T) {
	// received > total: must clamp to 100% fill -- otherwise the bar
	// could read as "regressing" when fed bad inputs.
	const w, h = 320, 240
	rgba := make([]byte, w*h*4)
	paintLoadingBar(rgba, w, h, 200, 100)
	x0 := (w - barWidth) / 2
	midY := h / 2
	// Rightmost pixel of the bar must be FILL.
	r, g, b := rgbAt(rgba, w, x0+barWidth-1, midY)
	if r != barFillR || g != barFillG || b != barFillB {
		t.Fatalf("overshoot did not clamp to full: (%d,%d,%d)", r, g, b)
	}

	// received < 0: must clamp to 0% (track only).
	for i := range rgba {
		rgba[i] = 0
	}
	paintLoadingBar(rgba, w, h, -100, 100)
	for x := x0; x < x0+barWidth; x++ {
		r, g, b := rgbAt(rgba, w, x, midY)
		if r == barFillR && g == barFillG && b == barFillB {
			t.Fatalf("negative received drew fill at x=%d", x)
		}
	}
}

func TestPaintLoadingBar_BadInputsAreNoop(t *testing.T) {
	// Zero / negative dimensions, or a slice that doesn't match the
	// stated dimensions, must early-return without panicking.
	paintLoadingBar(nil, 0, 0, 0, 0)
	paintLoadingBar([]byte{1, 2, 3}, 10, 10, 5, 10) // wrong slice size
	paintLoadingBar(make([]byte, 4), -1, 1, 0, 0)
	paintLoadingBar(make([]byte, 4), 1, -1, 0, 0)
}

func TestPaintLoadingBar_TinySurfaceClipsBar(t *testing.T) {
	// 50x4 surface -- smaller than the default 200x6 bar. The clip
	// path inside paintLoadingBar shrinks the bar to fit so the call
	// still produces a valid frame. On a surface this small the bar
	// covers the entire viewport, so the corner ends up either fill
	// (left half) or track (right half) depending on the ratio --
	// what we verify is just "no panic + at least one fill pixel +
	// at least one track pixel" at 50% progress.
	const w, h = 50, 4
	rgba := make([]byte, w*h*4)
	paintLoadingBar(rgba, w, h, 50, 100)
	var sawFill, sawTrack bool
	for x := 0; x < w; x++ {
		r, g, b := rgbAt(rgba, w, x, 0)
		if r == barFillR && g == barFillG && b == barFillB {
			sawFill = true
		}
		if r == barTrackR && g == barTrackG && b == barTrackB {
			sawTrack = true
		}
	}
	if !sawFill || !sawTrack {
		t.Fatalf("tiny-surface 50%% progress: sawFill=%v sawTrack=%v", sawFill, sawTrack)
	}
}

func TestBarFillFraction(t *testing.T) {
	cases := []struct {
		recv, tot int64
		want      float64
	}{
		{0, 100, 0},
		{50, 100, 0.5},
		{100, 100, 1.0},
		{200, 100, 1.0},  // clamp upper
		{-50, 100, 0},    // clamp lower
		{50, 0, 0},       // zero total -> 0
		{50, -1, 0},      // negative total -> 0
	}
	for _, c := range cases {
		got := barFillFraction(c.recv, c.tot)
		if got != c.want {
			t.Errorf("barFillFraction(%d,%d) = %v, want %v", c.recv, c.tot, got, c.want)
		}
	}
}

func TestFillRect_OutOfBoundsClips(t *testing.T) {
	// Direct fillRect call so we exercise the defensive bounds checks
	// inside it (paintLoadingBar already pre-clips, but the function
	// is still safe to call with bad inputs).
	const w, h = 16, 16
	rgba := make([]byte, w*h*4)
	// Way off the surface -- must noop.
	fillRect(rgba, w, 100, 100, 5, 5, 0xFF, 0, 0)
	// Negative origin -- partial draw, no panic.
	fillRect(rgba, w, -3, -3, 5, 5, 0, 0xFF, 0)
	// Validate at least the in-bounds portion landed at (0,0)..(1,1).
	r, g, b := rgbAt(rgba, w, 0, 0)
	if g != 0xFF {
		t.Fatalf("partial draw at origin: (%d,%d,%d)", r, g, b)
	}
}

// TestPaintLoadingBar_ImageSnapshot ensures we can build an image.Image
// view over the RGBA buffer -- proof the byte order matches RGBA32, the
// wire format wasmbox SAB surfaces expect.
func TestPaintLoadingBar_ImageSnapshot(t *testing.T) {
	const w, h = 64, 16
	rgba := make([]byte, w*h*4)
	paintLoadingBar(rgba, w, h, 25, 100)
	img := &image.RGBA{Pix: rgba, Stride: w * 4, Rect: image.Rect(0, 0, w, h)}
	// Sample the corner via the image API -- value must match a
	// direct slice read.
	rc := img.RGBAAt(0, 0)
	want := color.RGBA{R: barBGR, G: barBGG, B: barBGB, A: 0xFF}
	if rc != want {
		t.Fatalf("image.At(0,0) = %v, want %v", rc, want)
	}
}
