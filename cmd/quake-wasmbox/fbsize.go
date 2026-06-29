// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

// fbsize.go owns the "what (width, height) should the SAB + the
// software rasterizer use this boot?" decision. Originally hardcoded
// to 320x240 (the vanilla DOS Quake framebuffer); since 2026-06-29
// the JS-side worker (clients/quake/worker.js in the wasmbox repo)
// publishes the chosen dims on two `self.*` globals
// (`__quake_fb_w` / `__quake_fb_h`) so the engine renders natively at
// the user's saved window size instead of forcing the compositor to
// scale-fit a 320x240 surface up. We still ship a defensible default
// (800x600, a 4:3 modern-desktop size that matches Quake's internal
// aspect) plus min/max clamps so a malformed JS-side global or a
// missing reader cannot allocate a 1-pixel or a 16384x16384 SAB.
//
// Kept in a NON-(js+wasm) file so the picker logic is fully unit
// testable on host -- main.go is js+wasm-only.
package main

// Public dim constants (kept addressable for host tests + downstream
// callers; the JS side has equivalents in clients/quake/worker.js +
// the two MUST agree -- the JS side's chosen dim sizes the SAB the Go
// side reads through here, so a mismatch desyncs the surface).
const (
	defaultFBWidth  = 800
	defaultFBHeight = 600
	minFBWidth      = 320
	minFBHeight     = 240
	maxFBWidth      = 1920
	maxFBHeight     = 1080
)

// clampDim folds v into [lo, hi]. NaN / out-of-range / nonsense
// values collapse to lo so the engine cannot boot with a
// hostile-tiny or hostile-huge framebuffer.
func clampDim(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// chooseFB takes the dims the worker.js shim wrote into
// `self.__quake_fb_w` / `__quake_fb_h` (0 = not set / not readable)
// and returns the (width, height) the engine should boot at. Missing
// or non-positive values fall back to the 800x600 default; valid
// values are clamped to the same [min..max] envelope the JS-side
// clamps to so the two stay symmetric.
//
// Pure function so tests pin the picker without needing a real JS
// global. The js+wasm main.go reads the globals once, hands the
// values here, and feeds the result into wasmbox.NewClient.
func chooseFB(jsW, jsH int) (int, int) {
	w := jsW
	h := jsH
	if w <= 0 || h <= 0 {
		w = defaultFBWidth
		h = defaultFBHeight
	}
	w = clampDim(w, minFBWidth, maxFBWidth)
	h = clampDim(h, minFBHeight, maxFBHeight)
	return w, h
}
