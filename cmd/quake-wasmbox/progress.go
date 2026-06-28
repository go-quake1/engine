// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

// progress.go owns the per-frame paint logic for the OCI-fetch loading
// indicator that the wasmbox client renders BEFORE the engine takes over.
//
// Kept in a separate file (no js+wasm build tag) so the paint helper is
// testable on host. It writes directly to a pre-allocated RGBA byte slice
// the caller hands to wasmbox.Backend.PresentFrame.
package main

// Adwaita palette references for the loading bar. Picked to match the
// rest of the wasmbox chrome (panel + window borders) so the in-window
// progress bar feels continuous with the desktop.
const (
	// barBGR / barBGG / barBGB are the page-background RGB (Adwaita
	// "Light 1" #FAFAFA), cleared every repaint so old fills are wiped.
	barBGR = 0xFA
	barBGG = 0xFA
	barBGB = 0xFA
	// barTrackR/G/B is the inactive part of the track (Adwaita "Light 4"
	// #DADCE0). Slightly darker than the page so the bar is visible
	// even when empty.
	barTrackR = 0xDA
	barTrackG = 0xDC
	barTrackB = 0xE0
	// barFillR/G/B is the filled portion of the bar -- Adwaita blue
	// (#3584E4), the same accent the wasmbox compositor uses for focus.
	barFillR = 0x35
	barFillG = 0x84
	barFillB = 0xE4
	// barWidth / barHeight is the bar's pixel footprint inside the
	// surface. Centred horizontally + vertically; tuned for the
	// 320x240 vanilla Quake framebuffer (~60% of width).
	barWidth  = 200
	barHeight = 6
)

// paintLoadingBar repaints the entire RGBA surface to the Adwaita
// page background, then overlays a centred progress bar whose fill
// width is `received/total` of `barWidth`. The bar is always drawn --
// even when total==0 (unknown size) the empty track is rendered so
// the user sees that SOMETHING is in flight.
//
// rgba MUST be exactly width*height*4 bytes (RGBA32 row-major top-left,
// the wire format wasmbox SAB surfaces consume). Callers re-use the
// same backing slice across repaints; paintLoadingBar wipes + redraws
// in place + does not allocate.
//
// `received` is clamped to [0, total]; out-of-bounds values are
// treated as 0 (e.g. negative received from a buggy counting reader)
// or total (received > total). When total <= 0 the fill is omitted
// (track-only).
func paintLoadingBar(rgba []byte, width, height int, received, total int64) {
	if width <= 0 || height <= 0 || len(rgba) != width*height*4 {
		return
	}
	// 1. Page background -- wipe the previous frame so the bar appears
	//    on a clean surface (otherwise stale frame contents would bleed
	//    through where the track does not cover).
	for i := 0; i < len(rgba); i += 4 {
		rgba[i+0] = barBGR
		rgba[i+1] = barBGG
		rgba[i+2] = barBGB
		rgba[i+3] = 0xFF
	}
	// 2. Bar geometry. Centred + clipped against the surface so callers
	//    with tiny test surfaces don't have to special-case overflow.
	bw := barWidth
	if bw > width {
		bw = width
	}
	bh := barHeight
	if bh > height {
		bh = height
	}
	x0 := (width - bw) / 2
	y0 := (height - bh) / 2
	// 3. Track (always painted).
	fillRect(rgba, width, x0, y0, bw, bh, barTrackR, barTrackG, barTrackB)
	// 4. Fill (only when total>0; clamps received).
	if total > 0 {
		r := received
		if r < 0 {
			r = 0
		}
		if r > total {
			r = total
		}
		fillW := int(int64(bw) * r / total)
		if fillW > 0 {
			fillRect(rgba, width, x0, y0, fillW, bh, barFillR, barFillG, barFillB)
		}
	}
}

// fillRect paints a solid RGBA rectangle into rgba at (x,y) with size
// (w,h). Coordinates are pre-clipped by the caller (paintLoadingBar);
// we still defensively skip out-of-range writes so a future caller
// can't trigger a panic on a clipped edge.
func fillRect(rgba []byte, surfaceW, x, y, w, h int, r, g, b byte) {
	for row := 0; row < h; row++ {
		py := y + row
		if py < 0 || py*surfaceW*4 >= len(rgba) {
			continue
		}
		for col := 0; col < w; col++ {
			px := x + col
			if px < 0 || px >= surfaceW {
				continue
			}
			off := (py*surfaceW + px) * 4
			if off+3 >= len(rgba) {
				continue
			}
			rgba[off+0] = r
			rgba[off+1] = g
			rgba[off+2] = b
			rgba[off+3] = 0xFF
		}
	}
}

// barFillFraction is the clamped, unit-interval form of received/total
// the paint helper uses to size the bar. Exposed so tests + future
// telemetry can assert on the same arithmetic.
func barFillFraction(received, total int64) float64 {
	if total <= 0 {
		return 0
	}
	r := received
	if r < 0 {
		r = 0
	}
	if r > total {
		r = total
	}
	return float64(r) / float64(total)
}
