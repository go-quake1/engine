// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

// Command quake-bench measures the engine's frame time through the
// render.Renderer seam: per-stage times (sim, world, models, 2d,
// present), the engine / renderer split and the allocations per frame,
// on the deterministic demo-orbit camera path.
//
//	quake-bench run --frames 600 --json          one run (native, or js/wasm)
//	quake-bench baseline --wasm bench.wasm ...   interleaved native + browser rounds
//
// `run` builds for js/wasm unchanged; `baseline` (native only) drives
// both targets and renders the table.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"runtime"

	"github.com/spf13/cobra"

	"github.com/go-quake1/engine/bench"
)

var osExit = os.Exit

func main() {
	if err := RootCmd().Execute(); err != nil {
		osExit(1)
	}
}

// RootCmd returns the quake-bench command tree.
func RootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "quake-bench",
		Short:         "measure the engine's frame time through the Renderer seam",
		SilenceUsage:  true,
		SilenceErrors: false,
	}
	root.AddCommand(runCmd())
	addBaseline(root)
	return root
}

func runCmd() *cobra.Command {
	cfg := bench.DefaultConfig()
	var pakPath string
	var procs int
	var asJSON bool
	c := &cobra.Command{
		Use:   "run",
		Short: "run the frames once and print the result",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if procs > 0 {
				runtime.GOMAXPROCS(procs)
			}
			fsys, src, err := bench.OpenPak(pakPath)
			if err != nil {
				return err
			}
			res, err := bench.Run(cfg, fsys, src)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if asJSON {
				return json.NewEncoder(out).Encode(res)
			}
			_, err = fmt.Fprintf(out,
				"%s %s pak=%s frames=%d work p50 %.3f ms p95 %.3f ms engine %.1f%% allocs/frame %.1f\n",
				res.Target, res.Go, res.Pak, res.Summary.Frames,
				res.Summary.Work.P50, res.Summary.Work.P95, res.Split.EnginePercent, res.AllocsPerFrame)
			return err
		},
	}
	f := c.Flags()
	f.IntVar(&cfg.Frames, "frames", cfg.Frames, "measured frames")
	f.IntVar(&cfg.Warmup, "warmup", cfg.Warmup, "warm-up frames (not measured)")
	f.IntVar(&cfg.Width, "width", cfg.Width, "framebuffer width")
	f.IntVar(&cfg.Height, "height", cfg.Height, "framebuffer height")
	f.StringVar(&cfg.Renderer, "renderer", cfg.Renderer, "renderer: soft or gpu")
	f.StringVar(&cfg.Map, "map", cfg.Map, "map slug")
	f.StringVar(&pakPath, "pak", "", "pak0.pak to load (default: the embedded pak, empty = procedural assets)")
	f.IntVar(&procs, "procs", 0, "GOMAXPROCS (0 = leave)")
	f.BoolVar(&asJSON, "json", false, "print the result as JSON")
	return c
}
