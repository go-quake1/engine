// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

//go:build !js

package main

import (
	"encoding/json"
	"flag"
	"testing"
)

var (
	benchDir  = flag.String("wc.dir", ".", "the quake-worldcheck directory whose program BenchmarkWorldPass builds and runs in the browser (a clean clone of a tag, for abbench's base side)")
	benchAnim = flag.Bool("wc.anim", false, "BenchmarkWorldPass draws with the light styles and '+' textures moving (--anim): the program must know the flag")
)

// BenchmarkWorldPass is one browser run of the world benchmark (1280x800, the
// fixed e1m1 views, software and GPU frames alternating) per iteration, its
// medians reported as metrics, so that abbench can alternate two builds (the
// program is built from -wc.dir) and benchstat compare them:
//
//	go test -c -o wc.test ./cmd/quake-worldcheck
//	abbench --base wc.test --head wc.test --max-load 8 --wait 2h \
//	    --base-arg=-test.bench=. --head-arg=-test.bench=. \
//	    --base-arg=-wc.dir=<clean clone>/cmd/quake-worldcheck \
//	    --head-arg=-wc.dir=<this tree>/cmd/quake-worldcheck -- -test.run '^$' -test.benchtime 1x
//
// It needs QUAKE_PAK, QUAKE_CHROMIUM and QUAKE_PROFILE as the other browser
// tests do.
func BenchmarkWorldPass(b *testing.B) {
	args := []string{"--bench", "--width", "1280", "--height", "800"}
	if *benchAnim {
		args = append(args, "--anim")
	}
	for i := 0; i < b.N; i++ {
		raw := runInBrowserIn(b, *benchDir, args...)
		var v struct {
			Bench *benchRes `json:"bench"`
		}
		if err := json.Unmarshal([]byte(raw), &v); err != nil || v.Bench == nil {
			b.Fatalf("no bench in %q: %v", raw, err)
		}
		b.ReportMetric(v.Bench.GPUCPUMS, "gpu-cpu-ms")
		b.ReportMetric(v.Bench.GPUWallMS, "gpu-wall-ms")
		b.ReportMetric(v.Bench.SoftwareMS, "software-ms")
		b.ReportMetric(v.Bench.GPUCPUP95MS, "gpu-cpu-p95-ms")
		b.ReportMetric(v.Bench.GPUWallP95MS, "gpu-wall-p95-ms")
	}
}
