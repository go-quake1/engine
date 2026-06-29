// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package main

import "testing"

func TestClampDim(t *testing.T) {
	cases := []struct {
		v, lo, hi, want int
	}{
		{50, 100, 200, 100}, // below floor -> floor
		{300, 100, 200, 200}, // above ceiling -> ceiling
		{150, 100, 200, 150}, // inside range -> identity
		{100, 100, 200, 100}, // exact floor
		{200, 100, 200, 200}, // exact ceiling
	}
	for _, c := range cases {
		if got := clampDim(c.v, c.lo, c.hi); got != c.want {
			t.Errorf("clampDim(%d,%d,%d) = %d, want %d", c.v, c.lo, c.hi, got, c.want)
		}
	}
}

func TestChooseFB_DefaultsWhenJSAbsent(t *testing.T) {
	w, h := chooseFB(0, 0)
	if w != defaultFBWidth || h != defaultFBHeight {
		t.Fatalf("chooseFB(0,0) = %dx%d, want %dx%d", w, h, defaultFBWidth, defaultFBHeight)
	}
}

func TestChooseFB_NegativeFallsBackToDefault(t *testing.T) {
	// One axis negative is enough to mark the global as not-set.
	w, h := chooseFB(-1, 600)
	if w != defaultFBWidth || h != defaultFBHeight {
		t.Fatalf("chooseFB(-1,600) = %dx%d, want default %dx%d", w, h, defaultFBWidth, defaultFBHeight)
	}
	w, h = chooseFB(800, -1)
	if w != defaultFBWidth || h != defaultFBHeight {
		t.Fatalf("chooseFB(800,-1) = %dx%d, want default %dx%d", w, h, defaultFBWidth, defaultFBHeight)
	}
}

func TestChooseFB_ValidPassesThrough(t *testing.T) {
	w, h := chooseFB(900, 700)
	if w != 900 || h != 700 {
		t.Fatalf("chooseFB(900,700) = %dx%d, want 900x700", w, h)
	}
}

func TestChooseFB_BelowMinClamps(t *testing.T) {
	w, h := chooseFB(100, 50)
	if w != minFBWidth || h != minFBHeight {
		t.Fatalf("chooseFB(100,50) = %dx%d, want clamp to %dx%d", w, h, minFBWidth, minFBHeight)
	}
}

func TestChooseFB_AboveMaxClamps(t *testing.T) {
	w, h := chooseFB(4000, 3000)
	if w != maxFBWidth || h != maxFBHeight {
		t.Fatalf("chooseFB(4000,3000) = %dx%d, want clamp to %dx%d", w, h, maxFBWidth, maxFBHeight)
	}
}

func TestChooseFB_ExactBoundsPassThrough(t *testing.T) {
	w, h := chooseFB(minFBWidth, minFBHeight)
	if w != minFBWidth || h != minFBHeight {
		t.Fatalf("chooseFB(min) = %dx%d, want %dx%d", w, h, minFBWidth, minFBHeight)
	}
	w, h = chooseFB(maxFBWidth, maxFBHeight)
	if w != maxFBWidth || h != maxFBHeight {
		t.Fatalf("chooseFB(max) = %dx%d, want %dx%d", w, h, maxFBWidth, maxFBHeight)
	}
}

func TestChooseFB_SavedSizeFromTask(t *testing.T) {
	// The post-resize+reload saved-size case from the task description:
	// the JS side reads "900x700" from localStorage and hands it down.
	w, h := chooseFB(900, 700)
	if w != 900 || h != 700 {
		t.Fatalf("chooseFB(900,700) = %dx%d, want 900x700", w, h)
	}
}

func TestChooseFB_DefaultMatchesDocumentedSpec(t *testing.T) {
	// Pin the default at 800x600 -- the JS side hardcodes the same
	// pair and the two MUST stay in sync (the SAB the JS allocates
	// has to match the dims the Go side believes the surface has).
	if defaultFBWidth != 800 || defaultFBHeight != 600 {
		t.Fatalf("default fb = %dx%d, want 800x600 (must match clients/quake/worker.js)", defaultFBWidth, defaultFBHeight)
	}
}

func TestChooseFB_MinIsClassicQuake(t *testing.T) {
	// Defending the 320x240 floor: it's the vanilla DOS Quake
	// framebuffer + the value the engine + JS shim used to hardcode.
	// Anything smaller would lose readability of the in-game menus.
	if minFBWidth != 320 || minFBHeight != 240 {
		t.Fatalf("min fb = %dx%d, want 320x240", minFBWidth, minFBHeight)
	}
}

func TestChooseFB_MaxIsFullHD(t *testing.T) {
	// 1920x1080 = full HD; the software rasterizer chokes past that.
	if maxFBWidth != 1920 || maxFBHeight != 1080 {
		t.Fatalf("max fb = %dx%d, want 1920x1080", maxFBWidth, maxFBHeight)
	}
}
