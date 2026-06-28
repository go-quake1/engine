// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package main

import (
	"image"
	"image/color"
	"strings"
	"testing"
	"time"
)

// rgbAt returns the (R,G,B) byte triple at (x,y) in an RGBA32
// row-major slice of width w. Helper for assertions; isolated so
// the test code reads top-down.
func rgbAt(rgba []byte, w, x, y int) (byte, byte, byte) {
	off := (y*w + x) * 4
	return rgba[off+0], rgba[off+1], rgba[off+2]
}

// countColor returns the number of pixels in rgba that match the
// (r,g,b) triple exactly. Used to assert that the title text + panel
// backdrop + bar fill each contribute a recognisable number of
// pixels to the final frame.
func countColor(rgba []byte, w, h int, r, g, b byte) int {
	n := 0
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			rr, gg, bb := rgbAt(rgba, w, x, y)
			if rr == r && gg == g && bb == b {
				n++
			}
		}
	}
	return n
}

func TestPaintLoadingBar_BackgroundCleared(t *testing.T) {
	// Pre-fill with garbage; paint must overwrite EVERY pixel with
	// either the Adwaita background colour (outside the panel) or
	// some panel pixel (inside) -- the garbage must not survive.
	const w, h = 320, 240
	rgba := make([]byte, w*h*4)
	for i := range rgba {
		rgba[i] = 0xAA // garbage distinct from all panel colours
	}
	paintLoadingBar(rgba, w, h, 50, 100)
	// Sample a corner pixel -- safely outside the centred panel.
	r, g, b := rgbAt(rgba, w, 0, 0)
	if r != barBGR || g != barBGG || b != barBGB {
		t.Fatalf("corner pixel: (%d,%d,%d) want (%d,%d,%d)", r, g, b, barBGR, barBGG, barBGB)
	}
	// And no garbage pixel survives.
	for i := 0; i < len(rgba); i += 4 {
		if rgba[i] == 0xAA && rgba[i+1] == 0xAA && rgba[i+2] == 0xAA {
			t.Fatalf("garbage pixel survived at offset %d", i)
		}
	}
}

func TestPaintLoadingBar_PanelDominatesSurface(t *testing.T) {
	// The whole point of the redesign: the panel backdrop must
	// cover a large fraction of the surface so the user cannot miss
	// it. Spec: ~94% x 58% = ~54% of total pixels (minus a few text
	// pixels stamped over the backdrop).
	const w, h = 320, 240
	rgba := make([]byte, w*h*4)
	paintLoadingBar(rgba, w, h, 25, 100)
	panelPix := countColor(rgba, w, h, panelBGR, panelBGG, panelBGB)
	total := w * h
	frac := float64(panelPix) / float64(total)
	if frac < 0.40 {
		t.Fatalf("panel covers only %.1f%% of the surface (panel=%d pix / %d) -- want >= 40%%", frac*100, panelPix, total)
	}
}

func TestPaintLoadingBar_TitlePixelsPresent(t *testing.T) {
	// At least a few hundred title-coloured (white) pixels should
	// land inside the title row -- proves drawText actually wrote
	// glyph strokes from concharsfont.
	const w, h = 320, 240
	rgba := make([]byte, w*h*4)
	paintLoadingBar(rgba, w, h, 25, 100)
	white := countColor(rgba, w, h, titleR, titleG, titleB)
	if white < 100 {
		t.Fatalf("title pixels: got %d white pixels, want >= 100", white)
	}
}

func TestPaintLoadingBar_SubtitlePixelsPresent(t *testing.T) {
	const w, h = 320, 240
	rgba := make([]byte, w*h*4)
	paintLoadingBar(rgba, w, h, 25, 100)
	sub := countColor(rgba, w, h, subtitleR, subtitleG, subtitleB)
	if sub < 50 {
		t.Fatalf("subtitle pixels: got %d, want >= 50", sub)
	}
}

func TestPaintLoadingBar_BarTrackAndFill(t *testing.T) {
	const w, h = 320, 240
	rgba := make([]byte, w*h*4)
	paintLoadingBar(rgba, w, h, 25, 100)
	track := countColor(rgba, w, h, barTrackR, barTrackG, barTrackB)
	fill := countColor(rgba, w, h, barFillR, barFillG, barFillB)
	if track < 200 {
		t.Fatalf("bar track pixels: got %d, want >= 200", track)
	}
	if fill < 200 {
		t.Fatalf("bar fill pixels: got %d, want >= 200", fill)
	}
}

func TestPaintLoadingBar_FillFractionGrows(t *testing.T) {
	// Three snapshots -- 10%, 50%, 90% -- the count of fill-blue
	// pixels must strictly grow as more of the bar is filled.
	const w, h = 320, 240
	frames := []int64{10, 50, 90}
	prev := -1
	for _, pct := range frames {
		rgba := make([]byte, w*h*4)
		paintLoadingBar(rgba, w, h, pct, 100)
		count := countColor(rgba, w, h, barFillR, barFillG, barFillB)
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
	fill := countColor(rgba, w, h, barFillR, barFillG, barFillB)
	if fill != 0 {
		t.Fatalf("zero-total drew %d fill pixels, want 0", fill)
	}
	track := countColor(rgba, w, h, barTrackR, barTrackG, barTrackB)
	if track < 200 {
		t.Fatalf("zero-total bar track pixels: got %d, want >= 200", track)
	}
}

func TestPaintLoadingBar_ClampsReceived(t *testing.T) {
	const w, h = 320, 240
	// received > total: must clamp to 100% fill (bar fully blue).
	rgba := make([]byte, w*h*4)
	paintLoadingBar(rgba, w, h, 200, 100)
	rgbaFull := make([]byte, w*h*4)
	paintLoadingBar(rgbaFull, w, h, 100, 100)
	if countColor(rgba, w, h, barFillR, barFillG, barFillB) !=
		countColor(rgbaFull, w, h, barFillR, barFillG, barFillB) {
		t.Fatalf("overshoot did not clamp to full")
	}
	// received < 0: must clamp to 0% (zero fill pixels).
	rgbaNeg := make([]byte, w*h*4)
	paintLoadingBar(rgbaNeg, w, h, -100, 100)
	if got := countColor(rgbaNeg, w, h, barFillR, barFillG, barFillB); got != 0 {
		t.Fatalf("negative received drew %d fill pixels, want 0", got)
	}
}

func TestPaintLoadingBar_BadInputsAreNoop(t *testing.T) {
	// Zero / negative dimensions, or a slice that doesn't match
	// the stated dimensions, must early-return without panicking.
	paintLoadingBar(nil, 0, 0, 0, 0)
	paintLoadingBar([]byte{1, 2, 3}, 10, 10, 5, 10) // wrong slice size
	paintLoadingBar(make([]byte, 4), -1, 1, 0, 0)
	paintLoadingBar(make([]byte, 4), 1, -1, 0, 0)
}

func TestPaintLoadingBar_TinySurfaceStillRenders(t *testing.T) {
	// Smaller-than-default surface -- the panel-size clamp keeps
	// the panel inside the surface + the bar fits inside the panel.
	// We just verify no panic + the bar fill exists at 100%.
	const w, h = 64, 40
	rgba := make([]byte, w*h*4)
	paintLoadingBar(rgba, w, h, 100, 100)
	if got := countColor(rgba, w, h, panelBGR, panelBGG, panelBGB); got == 0 {
		t.Fatalf("tiny surface: panel backdrop had 0 pixels")
	}
	if got := countColor(rgba, w, h, barFillR, barFillG, barFillB); got == 0 {
		t.Fatalf("tiny surface 100%%: bar fill had 0 pixels")
	}
}

func TestPaintLoadingBar_PanelBorderPresent(t *testing.T) {
	// The 1-px slate border must surround the panel.
	const w, h = 320, 240
	rgba := make([]byte, w*h*4)
	paintLoadingBar(rgba, w, h, 50, 100)
	if got := countColor(rgba, w, h, panelBorderR, panelBorderG, panelBorderB); got < 100 {
		t.Fatalf("panel border pixels: got %d, want >= 100", got)
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
		{200, 100, 1.0}, // clamp upper
		{-50, 100, 0},   // clamp lower
		{50, 0, 0},      // zero total -> 0
		{50, -1, 0},     // negative total -> 0
	}
	for _, c := range cases {
		got := barFillFraction(c.recv, c.tot)
		if got != c.want {
			t.Errorf("barFillFraction(%d,%d) = %v, want %v", c.recv, c.tot, got, c.want)
		}
	}
}

func TestFillRect_OutOfBoundsClips(t *testing.T) {
	// Direct fillRect call so we exercise the defensive bounds
	// checks inside it (paintLoadingBar already pre-clips, but the
	// function is still safe to call with bad inputs).
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

func TestDrawText_BadInputsAreNoop(t *testing.T) {
	const w, h = 32, 32
	rgba := make([]byte, w*h*4)
	// scale<=0 / stride<=0 must early-return.
	drawText(rgba, w, h, 0, 0, "X", 0, 8, 0xFF, 0xFF, 0xFF)
	drawText(rgba, w, h, 0, 0, "X", 1, 0, 0xFF, 0xFF, 0xFF)
	// Confirm nothing was written.
	for i := 0; i < len(rgba); i++ {
		if rgba[i] != 0 {
			t.Fatalf("drawText wrote into surface despite bad inputs (off=%d v=%d)", i, rgba[i])
		}
	}
}

func TestDrawText_OffSurfaceClips(t *testing.T) {
	// Drawing past the right + bottom edges must not panic.
	const w, h = 16, 16
	rgba := make([]byte, w*h*4)
	drawText(rgba, w, h, w-4, h-4, "AB", 2, 14, 0xFF, 0, 0)
}

func TestTextWidth(t *testing.T) {
	if got := textWidth("", 14); got != 0 {
		t.Fatalf("textWidth(\"\") = %d, want 0", got)
	}
	if got := textWidth("ABC", 14); got != 42 {
		t.Fatalf("textWidth(\"ABC\", 14) = %d, want 42", got)
	}
}

func TestFormatMB(t *testing.T) {
	cases := []struct {
		n    int64
		want string
	}{
		{0, "0.0 MB"},
		{1024 * 1024, "1.0 MB"},
		{int64(1.5 * 1024 * 1024), "1.5 MB"},
		{180 * 1024 * 1024, "180.0 MB"},
	}
	for _, c := range cases {
		if got := formatMB(c.n); got != c.want {
			t.Errorf("formatMB(%d) = %q, want %q", c.n, got, c.want)
		}
	}
}

func TestFormatSubtitle(t *testing.T) {
	// Unknown total -> single size only.
	if got := formatSubtitle(1024*1024, 0); got != "1.0 MB" {
		t.Errorf("formatSubtitle(1MiB, 0) = %q", got)
	}
	// Known total -> "X / Y" form, with the slash literal.
	got := formatSubtitle(1024*1024, 2*1024*1024)
	if !strings.Contains(got, "/") {
		t.Errorf("formatSubtitle(known total) = %q, want it to contain /", got)
	}
	// Negative received clamps to 0.
	got = formatSubtitle(-5, 1024*1024)
	if !strings.HasPrefix(got, "0.0 MB") {
		t.Errorf("formatSubtitle(neg, ...) = %q, want it to start with 0.0 MB", got)
	}
	// Received > total clamps to total.
	got = formatSubtitle(10*1024*1024, 1024*1024)
	if !strings.HasPrefix(got, "1.0 MB") {
		t.Errorf("formatSubtitle(overshoot, ...) = %q, want clamp", got)
	}
}

func TestPaintLoadingBar_SubtitleUpdatesWithProgress(t *testing.T) {
	// Two distinct progress points produce different subtitle text
	// -> different subtitle-pixel counts (because numeric glyphs
	// differ). Cheap proxy for "subtitle redraws live".
	const w, h = 320, 240
	rgbaA := make([]byte, w*h*4)
	paintLoadingBar(rgbaA, w, h, 1024*1024, 180*1024*1024)
	rgbaB := make([]byte, w*h*4)
	paintLoadingBar(rgbaB, w, h, 90*1024*1024, 180*1024*1024)
	a := countColor(rgbaA, w, h, subtitleR, subtitleG, subtitleB)
	b := countColor(rgbaB, w, h, subtitleR, subtitleG, subtitleB)
	if a == b {
		t.Fatalf("subtitle did not redraw between 1MB and 90MB (both painted %d subtitle pixels)", a)
	}
}

// TestPaintLoadingBar_ImageSnapshot ensures we can build an
// image.Image view over the RGBA buffer -- proof the byte order
// matches RGBA32, the wire format wasmbox SAB surfaces expect.
func TestPaintLoadingBar_ImageSnapshot(t *testing.T) {
	const w, h = 64, 32
	rgba := make([]byte, w*h*4)
	paintLoadingBar(rgba, w, h, 25, 100)
	img := &image.RGBA{Pix: rgba, Stride: w * 4, Rect: image.Rect(0, 0, w, h)}
	rc := img.RGBAAt(0, 0)
	want := color.RGBA{R: barBGR, G: barBGG, B: barBGB, A: 0xFF}
	if rc != want {
		t.Fatalf("image.At(0,0) = %v, want %v", rc, want)
	}
}

// Backwards-compat: a few of the original byte-level invariants
// were keyed off the old constants (barWidth in particular). The
// alias is retained so callers + golden tests keep compiling.
func TestBarWidthAliasUnchanged(t *testing.T) {
	if barWidth != 240 {
		t.Fatalf("barWidth alias = %d, want 240 (callers depend on this)", barWidth)
	}
}

// TestPaintLoadingBar_SubOnePixelClamps exercises the "panel-size
// fraction rounds down to zero" defensive clamps so they're not
// dead code at any surface width. 1x1, 1x2, 2x2 surfaces all force
// pw/ph/bw/bh through the `< 1 -> reset` branch.
func TestPaintLoadingBar_SubOnePixelClamps(t *testing.T) {
	for _, dims := range [][2]int{{1, 1}, {1, 2}, {2, 2}, {2, 1}, {3, 3}} {
		w, h := dims[0], dims[1]
		rgba := make([]byte, w*h*4)
		paintLoadingBar(rgba, w, h, 50, 100)
		// No panic + at least one pixel touched.
		any := false
		for i := 3; i < len(rgba); i += 4 {
			if rgba[i] == 0xFF {
				any = true
				break
			}
		}
		if !any {
			t.Fatalf("dims=%dx%d: no pixel got an alpha=0xFF write", w, h)
		}
	}
}

// TestPaintLoadingBar_PanelClampsToSurface forces the "pw > width"
// + "ph > height" branches by setting the fraction-numerators
// dangerously close to / over the denominators. We can't change
// the constants, but a 1x1 surface exercises the lower-bound
// clamps; a surface exactly equal to the panel fraction exercises
// the equality path.
func TestPaintLoadingBar_PanelClampsToSurface(t *testing.T) {
	const w, h = 100, 100
	rgba := make([]byte, w*h*4)
	paintLoadingBar(rgba, w, h, 50, 100)
	// Sanity: panel painted some pixels.
	if got := countColor(rgba, w, h, panelBGR, panelBGG, panelBGB); got == 0 {
		t.Fatalf("100x100 surface: panel had 0 pixels")
	}
}

// TestFillRect_PartialOverflowDoesNotPanic forces the "off+3 >=
// len(rgba)" tail check in fillRect by writing into the very last
// row of a small surface with an x that would land just past the
// slice end.
func TestFillRect_PartialOverflowDoesNotPanic(t *testing.T) {
	const w, h = 8, 8
	rgba := make([]byte, w*h*4-1) // intentionally short by one byte
	fillRect(rgba, w, w-1, h-1, 1, 1, 0xFF, 0, 0)
	// Just no-panic suffices.
}

func TestLoadingPanelHoldFor(t *testing.T) {
	// Pin the exact arithmetic the renderer goroutine uses to
	// decide how long to keep the loading panel on screen after
	// the fetch returns.
	start := time.Unix(1000, 0)
	cases := []struct {
		name  string
		now   time.Time
		floor time.Duration
		want  time.Duration
	}{
		{"instant cache hit -- wait the full floor",
			start, 1500 * time.Millisecond, 1500 * time.Millisecond},
		{"500 ms in -- wait 1000 ms more",
			start.Add(500 * time.Millisecond), 1500 * time.Millisecond, 1000 * time.Millisecond},
		{"exactly at floor -- 0",
			start.Add(1500 * time.Millisecond), 1500 * time.Millisecond, 0},
		{"past floor -- 0 (no negative sleep)",
			start.Add(3 * time.Second), 1500 * time.Millisecond, 0},
		{"floor=0 -- 0",
			start, 0, 0},
	}
	for _, c := range cases {
		got := loadingPanelHoldFor(start, c.now, c.floor)
		if got != c.want {
			t.Errorf("%s: loadingPanelHoldFor = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestMinLoadingPanelVisibleSentinel(t *testing.T) {
	// Defends against accidental shrinking back below 1 s -- the
	// whole point of the constant is the user notices the panel.
	if minLoadingPanelVisible < time.Second {
		t.Fatalf("minLoadingPanelVisible = %v, want >= 1s so the panel is visible to humans", minLoadingPanelVisible)
	}
}
