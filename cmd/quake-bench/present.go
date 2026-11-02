// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

//go:build !js

package main

import (
	"context"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/go-quake1/engine/bench"
)

// Names of the configurations the present command measures.
const (
	nameSoftware = "wasm"
	nameCanvas2D = "wasm+canvas2d-present"
	nameWebGPU   = "wasm+webgpu-present"
)

type presentOpts struct {
	cfg                           bench.Config
	rounds, maxRedo               int
	maxLoad                       float64
	wait                          time.Duration
	wasm, execJS, chrome, profile string
	chromeArgs                    string
	timeout                       time.Duration
	allowFallback                 bool
}

func addPresent(root *cobra.Command) {
	o := presentOpts{cfg: bench.DefaultConfig()}
	c := &cobra.Command{
		Use:   "present",
		Short: "interleaved in-browser rounds of the software path and the WebGPU presenter",
		Long: `Measures three js/wasm configurations in Chromium, interleaved A B C A B C:

  wasm                   the baseline: the frame is discarded (software browser, no GPU)
  wasm+canvas2d-present  the standalone page's software path: RGBA on the CPU, putImageData
  wasm+webgpu-present    the 8-bit frame through WebGPU: palette lookup and scaling in a shader

The webgpu rounds require a real GPU adapter (a fallback adapter such as
SwiftShader fails them) unless --allow-fallback is given, in which case the
table is a correctness run, not a measurement.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return present(cmd, o) },
	}
	f := c.Flags()
	f.IntVar(&o.cfg.Frames, "frames", o.cfg.Frames, "measured frames per round")
	f.IntVar(&o.cfg.Warmup, "warmup", o.cfg.Warmup, "warm-up frames per round")
	f.IntVar(&o.cfg.Width, "width", o.cfg.Width, "framebuffer width")
	f.IntVar(&o.cfg.Height, "height", o.cfg.Height, "framebuffer height")
	f.StringVar(&o.cfg.Map, "map", o.cfg.Map, "map slug")
	f.IntVar(&o.rounds, "rounds", 7, "rounds per configuration")
	f.Float64Var(&o.maxLoad, "max-load", 8, "discard a round whose 1-minute load is above this")
	f.IntVar(&o.maxRedo, "max-redo", 20, "give up after this many discards for one round")
	f.DurationVar(&o.wait, "wait", 30*time.Second, "pause after a discard")
	f.StringVar(&o.wasm, "wasm", "", "js/wasm build of this command")
	f.StringVar(&o.execJS, "wasm-exec", filepath.Join(runtime.GOROOT(), "lib", "wasm", "wasm_exec.js"), "wasm_exec.js of the Go that built --wasm")
	f.StringVar(&o.chrome, "chromium", "", "Chromium binary (the full browser: the headless shell offers no GPU)")
	f.StringVar(&o.profile, "profile", "", "Chromium user-data dir (a durable directory)")
	f.StringVar(&o.chromeArgs, "chromium-args", "", "extra Chromium arguments for the GPU configurations, space separated (Linux SwiftShader: --enable-unsafe-webgpu --enable-features=Vulkan --use-vulkan=swiftshader --use-angle=swiftshader)")
	f.BoolVar(&o.allowFallback, "allow-fallback", false, "accept a fallback (software) adapter: a correctness run, never a measurement")
	f.DurationVar(&o.timeout, "timeout", 5*time.Minute, "limit for one round")
	_ = c.MarkFlagRequired("wasm")
	_ = c.MarkFlagRequired("chromium")
	_ = c.MarkFlagRequired("profile")
	root.AddCommand(c)
}

// presentTargets builds the three configurations. The baseline keeps its
// browser flags (no GPU); the two presenting configurations run in the
// same GPU-enabled browser so that only the display path differs.
func presentTargets(o presentOpts) []bench.Target {
	launch := func(extra ...string) bench.Launch {
		return func(ctx context.Context, url string) (func(), error) {
			args := append([]string{"--headless", "--no-sandbox"}, extra...)
			args = append(args, "--no-first-run", "--user-data-dir="+o.profile, url)
			return startProcess(ctx, o.chrome, args...)
		}
	}
	gpu := append([]string{"--enable-unsafe-webgpu"}, strings.Fields(o.chromeArgs)...)
	mk := func(name, present string, l bench.Launch) bench.Target {
		return bench.Target{Name: name, Run: func(ctx context.Context, cfg bench.Config) (bench.Result, error) {
			cfg.Present, cfg.RequireGPU = present, present == bench.PresentWebGPU && !o.allowFallback
			cctx, cancel := context.WithTimeout(ctx, o.timeout)
			defer cancel()
			return bench.RunInBrowser(cctx, o.wasm, o.execJS, cfg, l)
		}}
	}
	return []bench.Target{
		mk(nameSoftware, bench.PresentNone, launch("--disable-gpu")),
		mk(nameCanvas2D, bench.PresentCanvas2D, launch(gpu...)),
		mk(nameWebGPU, bench.PresentWebGPU, launch(gpu...)),
	}
}

func present(cmd *cobra.Command, o presentOpts) error {
	if o.rounds < 1 {
		return fmt.Errorf("--rounds must be at least 1")
	}
	ctx := cmd.Context()
	targets := presentTargets(o)
	names := make([]string, len(targets))
	for i, t := range targets {
		names[i] = t.Name
	}
	rounds, disc, err := bench.Interleave(ctx, targets, o.cfg, o.rounds, o.maxLoad, o.maxRedo,
		func() (float64, error) { return uptimeLoad(ctx) },
		func() { sleep(o.wait) })
	out := cmd.OutOrStdout()
	fmt.Fprint(out, bench.LoadLog(rounds, disc))
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "\npak: %s, go: %s, rounds per configuration: %d, frame %dx%d\n\n",
		rounds[0].Result.Pak, rounds[0].Result.Go, o.rounds, o.cfg.Width, o.cfg.Height)
	seen := map[string]bool{}
	for _, r := range rounds {
		if r.Result.Browser != "" && !seen["b"] {
			seen["b"] = true
			fmt.Fprintf(out, "browser: %s\n", r.Result.Browser)
		}
		if r.Result.Adapter != "" && !seen[r.Result.Adapter] {
			seen[r.Result.Adapter] = true
			fmt.Fprintf(out, "adapter: %s (fallback %v)\n", r.Result.Adapter, r.Result.AdapterFallback)
		}
	}
	fmt.Fprintf(out, "\n%s", bench.Table(rounds, names))
	return nil
}
