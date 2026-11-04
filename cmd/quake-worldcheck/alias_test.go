// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

//go:build !js

package main

import (
	"encoding/json"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// aliasTolerance is the largest accepted share of alias-region pixels that
// differ from the engine's software alias pass and are not a one-pixel
// shift of it. Stated before the first measurement: 3 %. The alias region is
// the pixels where either side's frame with the entities differs from the
// same side's frame without them.
const aliasTolerance = "0.03"

// TestGPUAliasMatchesTheSoftwareAlias compares the GPU alias pass with
// render.DrawAliasInterpLit on the 16 fixed alias views of e1m1.
func TestGPUAliasMatchesTheSoftwareAlias(t *testing.T) {
	v := runInBrowser(t, "--part", "alias", "--tolerance", aliasTolerance)
	if v.Check != "PASS" || v.Pixels == 0 {
		t.Fatalf("verdict %+v", v)
	}
	if os.Getenv("QUAKE_REQUIRE_GPU") == "1" && v.Fallback {
		t.Fatal("a fallback adapter where a GPU is required")
	}
}

// TestCorruptedSkinFails is the planted negative: the GPU gets skins with
// every palette index shifted while the reference keeps the true ones.
func TestCorruptedSkinFails(t *testing.T) {
	v := runInBrowser(t, "--part", "alias", "--tolerance", aliasTolerance, "--corrupt")
	if v.Check != "FAIL" || !strings.Contains(v.Detail, "unexplained mismatch") {
		t.Fatalf("a corrupted skin was not caught: %+v", v)
	}
}

// TestWrongLightFails is the planted negative of the R_LightPoint step: the
// GPU lights every entity at the old constant (76), the reference at each
// entity's R_LightPoint.
func TestWrongLightFails(t *testing.T) {
	v := runInBrowser(t, "--part", "alias", "--tolerance", aliasTolerance, "--wrongLight")
	if v.Check != "FAIL" || !strings.Contains(v.Detail, "unexplained mismatch") {
		t.Fatalf("a wrong entity light was not caught: %+v", v)
	}
}

type partBenchRes struct {
	Frames            int     `json:"frames"`
	EngineFrameMS     float64 `json:"engine_frame_ms"`
	EngineFrameP95MS  float64 `json:"engine_frame_p95_ms"`
	EnginePartMS      float64 `json:"engine_part_ms"`
	GPUWorldWallMS    float64 `json:"gpu_world_wall_ms"`
	GPUFrameWallMS    float64 `json:"gpu_frame_wall_ms"`
	GPUFrameWallP95MS float64 `json:"gpu_frame_wall_p95_ms"`
	GPUPartWallMS     float64 `json:"gpu_part_wall_ms"`
}

// benchPart runs QUAKE_BENCH_ROUNDS browser runs of a part at 1280x800 (a
// run whose 1-minute load, before or after, is above 8 is discarded) and
// logs the medians (min-max). In each run the engine's software path and
// the GPU path alternate frame by frame on the fixed views.
func benchPart(t *testing.T, part string) {
	rounds, _ := strconv.Atoi(os.Getenv("QUAKE_BENCH_ROUNDS"))
	if rounds == 0 {
		t.Skip("QUAKE_BENCH_ROUNDS not set")
	}
	var all []partBenchRes
	discarded := 0
	for len(all) < rounds {
		before := load(t)
		if before > 8 {
			if discarded++; discarded > 80 {
				t.Fatalf("the load stayed above 8 (%d discards)", discarded)
			}
			time.Sleep(30 * time.Second)
			continue
		}
		raw := runInBrowserRaw(t, "--part", part, "--bench", "--width", "1280", "--height", "800")
		after := load(t)
		if after > 8 {
			discarded++
			continue
		}
		var v struct {
			B *partBenchRes `json:"part_bench"`
		}
		if err := json.Unmarshal([]byte(raw), &v); err != nil || v.B == nil {
			t.Fatalf("no part_bench in %q: %v", raw, err)
		}
		t.Logf("round %d: load %.2f/%.2f: engine frame %.3f ms, engine %s %.3f ms, gpu world %.3f ms, gpu frame %.3f ms (p95 %.3f / %.3f)",
			len(all)+1, before, after, v.B.EngineFrameMS, part, v.B.EnginePartMS, v.B.GPUWorldWallMS, v.B.GPUFrameWallMS, v.B.EngineFrameP95MS, v.B.GPUFrameWallP95MS)
		all = append(all, *v.B)
	}
	med := func(f func(partBenchRes) float64) (m, lo, hi float64) {
		var xs []float64
		for _, r := range all {
			xs = append(xs, f(r))
		}
		sort.Float64s(xs)
		return xs[len(xs)/2], xs[0], xs[len(xs)-1]
	}
	for _, c := range []struct {
		name string
		f    func(partBenchRes) float64
	}{
		{"engine software frame: world path + " + part + " (ms)", func(r partBenchRes) float64 { return r.EngineFrameMS }},
		{"engine software " + part + " alone (ms)", func(r partBenchRes) float64 { return r.EnginePartMS }},
		{"gpu world alone, wall to idle (ms)", func(r partBenchRes) float64 { return r.GPUWorldWallMS }},
		{"gpu " + part + " alone (lit faces skipped), wall to idle (ms)", func(r partBenchRes) float64 { return r.GPUPartWallMS }},
		{"gpu world + " + part + ", wall to idle (ms)", func(r partBenchRes) float64 { return r.GPUFrameWallMS }},
		{"engine frame p95 (ms)", func(r partBenchRes) float64 { return r.EngineFrameP95MS }},
		{"gpu frame p95 (ms)", func(r partBenchRes) float64 { return r.GPUFrameWallP95MS }},
	} {
		m, lo, hi := med(c.f)
		t.Logf("| %s | %.3f (%.3f-%.3f) |", c.name, m, lo, hi)
	}
	t.Logf("rounds %d, discarded %d, frames per round %d", len(all), discarded, all[0].Frames)
}

// TestBenchAlias measures the alias pass at 1280x800 (QUAKE_BENCH_ROUNDS=7).
func TestBenchAlias(t *testing.T) { benchPart(t, "alias") }

// TestAliasAttribution removes one ingredient on both sides at a time
// (QUAKE_ATTRIBUTION=1).
func TestAliasAttribution(t *testing.T) {
	if os.Getenv("QUAKE_ATTRIBUTION") == "" {
		t.Skip("QUAKE_ATTRIBUTION not set")
	}
	for _, c := range [][]string{{}, {"--flatSkin"}, {"--flatLight"}, {"--noLerp"}, {"--noWeapon"}, {"--standInLight"}, {"--flatSkin", "--flatLight", "--noLerp"}} {
		t.Logf("probe %v", c)
		runInBrowser(t, append([]string{"--part", "alias", "--tolerance", "1"}, c...)...)
	}
}
