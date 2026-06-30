// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

// progress.go owns the per-frame paint logic for the OCI-fetch loading
// indicator that the wasmbox client renders BEFORE the engine takes
// over.
//
// 2026-06-28: re-skinned from a 200x6 hairline bar into a prominent
// "loading panel" that dominates the surface -- impossible to miss
// even when the fetch completes in <1 s. The panel carries:
//
//   - a dark slate backdrop covering ~94%x58% of the surface, framed
//     with a 1-px Adwaita-blue border;
//   - the title "LOADING QUAKE ASSETS" rendered at scale=2 via
//     [github.com/go-quake1/engine/quake-tamago/concharsfont] glyphs;
//   - a live "X.X MB / Y.Y MB" subtitle (or just "X.X MB" when the
//     total is unknown) at scale=1, centred below the title;
//   - a thick 240x20 progress bar (track + fill) at the bottom of the
//     panel, using the Adwaita "Light 4" track + accent-blue fill the
//     wasmbox compositor already paints elsewhere.
//
// Kept in a separate file (no js+wasm build tag) so the paint helper
// is testable on host. It writes directly to a pre-allocated RGBA
// byte slice the caller hands to wasmbox.Backend.PresentFrame.
package main

import (
	"fmt"
	"time"

	"github.com/go-quake1/engine/quake-tamago/concharsfont"
)

// Adwaita palette references for the loading panel. Picked to match
// the rest of the wasmbox chrome (panel + window borders) so the
// in-window progress panel feels continuous with the desktop.
const (
	// barBGR / barBGG / barBGB is the surface clear colour painted
	// every repaint so old fills are wiped. Adwaita "Light 1"
	// (#FAFAFA).
	barBGR = 0xFA
	barBGG = 0xFA
	barBGB = 0xFA
	// barTrackR/G/B is the inactive part of the progress bar
	// (Adwaita "Light 4" #DADCE0).
	barTrackR = 0xDA
	barTrackG = 0xDC
	barTrackB = 0xE0
	// barFillR/G/B is the filled portion of the progress bar -- the
	// Adwaita blue (#3584E4) wasmbox uses for focus.
	barFillR = 0x35
	barFillG = 0x84
	barFillB = 0xE4
	// Panel backdrop -- dark slate the user cannot confuse with any
	// game scene the renderer might paint later.
	panelBGR = 0x20
	panelBGG = 0x20
	panelBGB = 0x28
	// Panel border -- a soft slate so the rectangle reads as a card.
	panelBorderR = 0x5A
	panelBorderG = 0x5A
	panelBorderB = 0x6E
	// Title text colour (white).
	titleR = 0xFF
	titleG = 0xFF
	titleB = 0xFF
	// Subtitle text colour (light gray; ~70% luma on the panel bg).
	subtitleR = 0xB4
	subtitleG = 0xB4
	subtitleB = 0xBE
)

// panelTitle is the short banner shown above the progress bar.
const panelTitle = "LOADING QUAKE ASSETS"

// minLoadingPanelVisible is the floor on the time the loading
// panel stays on screen. On localhost the OCI fetch completes in
// ~1 s -- without this floor the panel barely registers, so we
// hold the render loop for at least this long after the fetch
// finishes (or fails) before letting the engine take over. Tuned
// to "noticeable but not annoying"; tests pin the exact value.
const minLoadingPanelVisible = 1500 * time.Millisecond

// Geometry constants. All sizes are tuned for the 320x240 vanilla
// Quake framebuffer + scale linearly to larger / smaller surfaces
// via the paintLoadingBar code below.
const (
	// panelWidthNum/Den encode the panel width as a fraction of the
	// surface width (~94% at 320 wide = 300 px).
	panelWidthNum = 94
	panelWidthDen = 100
	// panelHeightNum/Den encode the panel height (~58% at 240 tall =
	// 140 px).
	panelHeightNum = 58
	panelHeightDen = 100
	// barWidthNum/Den encode the progress bar width as a fraction of
	// the panel width (80% at 300 panel = 240 px).
	barWidthNum = 80
	barWidthDen = 100
	// barHeight is fixed pixel height of the progress bar -- thick
	// enough to read at a glance.
	barHeight = 20
	// titleScale is the integer up-scale of the 8x8 font for the
	// title row (16-px glyphs).
	titleScale = 2
	// titleStride is the per-character advance at titleScale. Glyphs
	// only use bits 0..6 so a 14-px stride keeps the title from
	// overflowing while staying visually solid.
	titleStride = 14
	// subtitleScale is the integer up-scale for the size readout.
	subtitleScale = 2
	// subtitleStride is the per-character advance for the size
	// readout (chosen for the same overflow margin as titleStride).
	subtitleStride = 14
	// Backwards-compatible aliases kept so external pixel-level
	// tests + callers built against the older API keep compiling.
	// These describe the progress bar's nominal footprint at 320x240
	// (240x20), NOT the panel itself.
	barWidth = 240
)

// paintLoadingBar repaints the entire RGBA surface to the Adwaita
// page background, then overlays the centred loading panel with
// title + size readout + a chunky progress bar whose fill width is
// `received/total` of the bar width. The panel is ALWAYS drawn --
// even when total==0 (unknown size) the empty track is rendered so
// the user sees that SOMETHING is in flight.
//
// rgba MUST be exactly width*height*4 bytes (RGBA32 row-major
// top-left, the wire format wasmbox SAB surfaces consume). Callers
// re-use the same backing slice across repaints; paintLoadingBar
// wipes + redraws in place + does not allocate.
//
// `received` is clamped to [0, total]; out-of-bounds values are
// treated as 0 (e.g. negative received from a buggy counting reader)
// or total (received > total). When total <= 0 the fill is omitted
// (track-only) and the subtitle shows just the received size.
//
// Deprecated: prefer paintLoadingBarAt which carries the animation
// phase the time-driven shine overlay needs. Kept for the existing
// test suite that pins per-pixel output for a STATIC bar.
func paintLoadingBar(rgba []byte, width, height int, received, total int64) {
	paintLoadingBarAt(rgba, width, height, received, total, 0)
}

// paintLoadingBarAt is paintLoadingBar plus a time-driven shine
// overlay so the bar is visibly animating even when the fetch
// progress stalls between browser-buffered chunks. `animPhase` is a
// monotonic seconds counter (the caller passes time.Since(start)
// converted to float64 seconds); the shine sweep cycles every 2 s.
//
// Why this matters: Firefox's worker-side fetch() releases body
// bytes in multi-MB bursts every few seconds, so the `received`
// counter sits frozen for visible stretches. Chromium / WebKit
// release in smaller chunks (~64 KiB) and the bar visibly creeps.
// Without the shine the Firefox UX reads as "the loader is broken";
// with the shine the user sees activity at every browser cadence.
//
// Verified by scratchpad/probe-loadingbar-xbrowser.mjs: with the
// shine, frame-to-frame pixel diffs are non-zero in every browser
// even when `received` is unchanged between captures.
func paintLoadingBarAt(rgba []byte, width, height int, received, total int64, animPhase float64) {
	if width <= 0 || height <= 0 || len(rgba) != width*height*4 {
		return
	}
	// 1. Page background -- wipe the previous frame so the panel
	//    appears on a clean surface (otherwise stale frame contents
	//    bleed through outside the panel rectangle).
	for i := 0; i < len(rgba); i += 4 {
		rgba[i+0] = barBGR
		rgba[i+1] = barBGG
		rgba[i+2] = barBGB
		rgba[i+3] = 0xFF
	}
	// 2. Panel rectangle: a generous central card covering most of
	//    the surface. Sized as a fraction of the surface so smaller
	//    test surfaces still get a panel (instead of an overflow).
	pw := width * panelWidthNum / panelWidthDen
	if pw < 1 {
		pw = width
	}
	if pw > width {
		pw = width
	}
	ph := height * panelHeightNum / panelHeightDen
	if ph < 1 {
		ph = height
	}
	if ph > height {
		ph = height
	}
	px := (width - pw) / 2
	py := (height - ph) / 2
	fillRect(rgba, width, px, py, pw, ph, panelBGR, panelBGG, panelBGB)
	// 3. Panel border (1-pixel slate frame so the dark backdrop
	//    reads as a card rather than a hole in the surface).
	fillRect(rgba, width, px, py, pw, 1, panelBorderR, panelBorderG, panelBorderB)
	fillRect(rgba, width, px, py+ph-1, pw, 1, panelBorderR, panelBorderG, panelBorderB)
	fillRect(rgba, width, px, py, 1, ph, panelBorderR, panelBorderG, panelBorderB)
	fillRect(rgba, width, px+pw-1, py, 1, ph, panelBorderR, panelBorderG, panelBorderB)

	// 4. Title -- "LOADING QUAKE ASSETS" centred in the top third
	//    of the panel, rendered at titleScale.
	titleW := textWidth(panelTitle, titleStride)
	titleH := concharsfont.CellSize * titleScale
	titleX := px + (pw-titleW)/2
	titleY := py + ph/4 - titleH/2
	drawText(rgba, width, height, titleX, titleY, panelTitle, titleScale, titleStride, titleR, titleG, titleB)

	// 5. Subtitle -- "X.X MB / Y.Y MB" centred under the title.
	sub := formatSubtitle(received, total)
	subW := textWidth(sub, subtitleStride)
	subH := concharsfont.CellSize * subtitleScale
	subX := px + (pw-subW)/2
	subY := py + ph/2 - subH/2
	drawText(rgba, width, height, subX, subY, sub, subtitleScale, subtitleStride, subtitleR, subtitleG, subtitleB)

	// 6. Progress bar -- thick, centred in the bottom third.
	bw := pw * barWidthNum / barWidthDen
	if bw < 1 {
		bw = pw
	}
	if bw > pw {
		bw = pw
	}
	bh := barHeight
	if bh > ph/3 {
		bh = ph / 3
	}
	if bh < 1 {
		bh = 1
	}
	bx := px + (pw-bw)/2
	by := py + ph - ph/4 - bh/2
	fillRect(rgba, width, bx, by, bw, bh, barTrackR, barTrackG, barTrackB)
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
			fillRect(rgba, width, bx, by, fillW, bh, barFillR, barFillG, barFillB)
		}
	}

	// Time-driven shine overlay. A narrow lighter band slides across
	// the bar every ShineCycleSeconds regardless of `received`, so
	// the user sees motion even when a browser's fetch is stalled
	// between chunks. shineW is a fraction of the bar width; the
	// band's left edge sweeps from -shineW to bw every cycle. The
	// extra `-shineW` start so the band enters from off-track and
	// leaves on the right rather than popping in.
	shineW := bw / 8
	if shineW < 4 {
		shineW = 4
	}
	phase := animPhase / ShineCycleSeconds
	phase -= float64(int(phase))
	sweepRange := float64(bw + shineW*2)
	sx := bx - shineW + int(phase*sweepRange)
	// Clip the shine to the bar rectangle so it doesn't paint over
	// the panel background.
	sxClip := sx
	swClip := shineW
	if sxClip < bx {
		swClip -= bx - sxClip
		sxClip = bx
	}
	if sxClip+swClip > bx+bw {
		swClip = bx + bw - sxClip
	}
	if swClip > 0 {
		fillRect(rgba, width, sxClip, by, swClip, bh, shineR, shineG, shineB)
	}
}

// ShineCycleSeconds is how long one full left-to-right sweep of the
// shine overlay takes. Tuned for "obvious motion without seizure":
// 2 s lands one sweep per ~2 s = visibly animating to a human eye.
const ShineCycleSeconds = 2.0

// shineR/G/B is the colour of the moving shine band. A lighter blue
// than the bar fill so the band reads as a highlight over both the
// filled and the empty track regions.
const (
	shineR = 0x8E
	shineG = 0xC4
	shineB = 0xF7
)

// fillRect paints a solid RGBA rectangle into rgba at (x,y) with
// size (w,h). Coordinates may extend past the surface -- writes are
// clipped per-pixel so a clipped edge never panics.
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

// drawText rasterises ASCII text into the RGBA surface using the
// concharsfont 8x8 glyph table. Each glyph is up-scaled by `scale`
// (so scale=2 -> 16x16 px) and the cursor advances by `stride`
// pixels per character (chosen smaller than scale*8 to keep title
// width compact since the source glyphs only occupy bits 0..6).
//
// Off-surface writes are clipped by fillRect; this function never
// panics on bad coords.
func drawText(rgba []byte, surfaceW, surfaceH, x, y int, s string, scale, stride int, r, g, b byte) {
	if scale <= 0 || stride <= 0 {
		return
	}
	for i := 0; i < len(s); i++ {
		ch := s[i]
		bm := concharsfont.Glyph(ch)
		cx := x + i*stride
		for py := 0; py < concharsfont.CellSize; py++ {
			rowBits := bm[py]
			for sx := 0; sx < concharsfont.CellSize; sx++ {
				if rowBits&(1<<(7-uint(sx))) == 0 {
					continue
				}
				fillRect(rgba, surfaceW, cx+sx*scale, y+py*scale, scale, scale, r, g, b)
			}
		}
	}
	_ = surfaceH
}

// textWidth returns the pixel width a string occupies on screen
// with the given per-glyph stride.
func textWidth(s string, stride int) int {
	if len(s) == 0 {
		return 0
	}
	return len(s) * stride
}

// formatSubtitle renders "X.X MB / Y.Y MB" (or "X.X MB" when total
// is unknown) using one decimal of precision. Kept ASCII so the
// concharsfont glyphs cover every character.
func formatSubtitle(received, total int64) string {
	r := received
	if r < 0 {
		r = 0
	}
	if total > 0 {
		if r > total {
			r = total
		}
		return fmt.Sprintf("%s / %s", formatMB(r), formatMB(total))
	}
	return formatMB(r)
}

// formatMB renders byte counts as "X.X MB" with one decimal -- the
// readout the panel shows live as the OCI blob streams in.
func formatMB(n int64) string {
	mb := float64(n) / (1024.0 * 1024.0)
	return fmt.Sprintf("%.1f MB", mb)
}

// loadingPanelHoldFor reports how long the caller still needs to
// sleep so the loading panel has been on screen for at least
// `floor` since `start`. Returns 0 (no extra sleep needed) when
// floor has already elapsed -- callers use the return as a
// time.Sleep argument and skip the sleep on zero.
//
// Pure function so the "hold the panel a bit longer on instant
// cache hits" logic in main.go's renderer goroutine is testable on
// host (main.go is js+wasm-only).
func loadingPanelHoldFor(start, now time.Time, floor time.Duration) time.Duration {
	elapsed := now.Sub(start)
	if elapsed >= floor {
		return 0
	}
	return floor - elapsed
}

// barFillFraction is the clamped, unit-interval form of
// received/total used to size the bar. Exposed so tests + future
// telemetry assert on the same arithmetic.
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
