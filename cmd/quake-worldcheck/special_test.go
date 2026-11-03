// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

//go:build !js

package main

import (
	"os"
	"strings"
	"testing"
)

// specialTolerance is the largest accepted share of region pixels that
// differ from the engine's software fill and are not a one-pixel shift of
// it: 3 %, stated before the first measurement of the sky (the same figure
// as the alias pass). The region is the pixels where either side's frame
// with the part differs from the same side's frame without it.
const specialTolerance = "0.03"

func checkPart(t *testing.T, part string) {
	t.Helper()
	v := runInBrowser(t, "--part", part, "--tolerance", specialTolerance)
	if v.Check != "PASS" || v.Pixels == 0 {
		t.Fatalf("verdict %+v", v)
	}
	if len(v.Views) < 12 {
		t.Fatalf("only %d of 16 views", len(v.Views))
	}
	if os.Getenv("QUAKE_REQUIRE_GPU") == "1" && v.Fallback {
		t.Fatal("a fallback adapter where a GPU is required")
	}
}

func corruptedPartFails(t *testing.T, part string) {
	t.Helper()
	v := runInBrowser(t, "--part", part, "--tolerance", specialTolerance, "--corrupt")
	if v.Check != "FAIL" || !strings.Contains(v.Detail, "unexplained mismatch") {
		t.Fatalf("a corrupted texture was not caught: %+v", v)
	}
}

// TestGPUSkyMatchesTheSoftwareSky compares the GPU sky with
// render.FillSkyPolygon on 16 fixed e1m1 views of sky faces.
func TestGPUSkyMatchesTheSoftwareSky(t *testing.T) { checkPart(t, "sky") }

// TestCorruptedSkyFails is the planted negative: the GPU gets a sky texture
// with every palette index shifted while the reference keeps the true one.
func TestCorruptedSkyFails(t *testing.T) { corruptedPartFails(t, "sky") }

// TestBenchSky measures the sky at 1280x800 (QUAKE_BENCH_ROUNDS=7).
func TestBenchSky(t *testing.T) { benchPart(t, "sky") }

// TestGPUTurbMatchesTheSoftwareTurb compares the GPU water with
// render.FillTurbulentPolygon on 16 fixed e1m1 views of "*" faces.
func TestGPUTurbMatchesTheSoftwareTurb(t *testing.T) { checkPart(t, "turb") }

// TestCorruptedWaterFails is the planted negative: the GPU gets a water
// texture with every palette index shifted while the reference keeps the true one.
func TestCorruptedWaterFails(t *testing.T) { corruptedPartFails(t, "turb") }

// TestBenchTurb measures the water at 1280x800 (QUAKE_BENCH_ROUNDS=7).
func TestBenchTurb(t *testing.T) { benchPart(t, "turb") }

// TestTurbAttribution removes one ingredient on both sides at a time
// (QUAKE_ATTRIBUTION=1): the texture (one flat colour) and the animation.
func TestTurbAttribution(t *testing.T) {
	if os.Getenv("QUAKE_ATTRIBUTION") == "" {
		t.Skip("QUAKE_ATTRIBUTION not set")
	}
	for _, c := range [][]string{{}, {"--flatSkin"}, {"--noScroll"}, {"--flatSkin", "--noScroll"}} {
		t.Logf("probe %v", c)
		runInBrowser(t, append([]string{"--part", "turb", "--tolerance", "1"}, c...)...)
	}
}

// TestSkyAttribution removes one ingredient on both sides at a time
// (QUAKE_ATTRIBUTION=1): the texture (one flat colour) and the scroll.
func TestSkyAttribution(t *testing.T) {
	if os.Getenv("QUAKE_ATTRIBUTION") == "" {
		t.Skip("QUAKE_ATTRIBUTION not set")
	}
	for _, c := range [][]string{{}, {"--flatSkin"}, {"--noScroll"}, {"--flatSkin", "--noScroll"}} {
		t.Logf("probe %v", c)
		runInBrowser(t, append([]string{"--part", "sky", "--tolerance", "1"}, c...)...)
	}
}
