// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

//go:build !js

package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"github.com/spf13/cobra"

	"github.com/go-quake1/engine/bench"
)

// Seams replaced by the tests.
var (
	// execOutput runs a command and returns its stdout.
	execOutput = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		var out bytes.Buffer
		c := exec.CommandContext(ctx, name, args...)
		c.Stdout = &out
		c.Stderr = os.Stderr
		err := c.Run()
		return out.Bytes(), err
	}
	// startProcess starts a command in the background; stop kills it.
	startProcess = func(ctx context.Context, name string, args ...string) (func(), error) {
		c := exec.CommandContext(ctx, name, args...)
		if err := c.Start(); err != nil {
			return nil, err
		}
		return func() { _ = c.Process.Kill(); _ = c.Wait() }, nil
	}
	sleep = time.Sleep
	self  = os.Executable
)

type baselineOpts struct {
	cfg                           bench.Config
	pak                           string
	rounds, maxRedo               int
	maxLoad                       float64
	wait                          time.Duration
	wasm, execJS, chrome, profile string
	timeout                       time.Duration
}

func addBaseline(root *cobra.Command) {
	o := baselineOpts{cfg: bench.DefaultConfig()}
	c := &cobra.Command{
		Use:   "baseline",
		Short: "interleaved native and in-browser rounds, with the load around each",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return baseline(cmd, o)
		},
	}
	f := c.Flags()
	f.IntVar(&o.cfg.Frames, "frames", o.cfg.Frames, "measured frames per round")
	f.IntVar(&o.cfg.Warmup, "warmup", o.cfg.Warmup, "warm-up frames per round")
	f.IntVar(&o.cfg.Width, "width", o.cfg.Width, "framebuffer width")
	f.IntVar(&o.cfg.Height, "height", o.cfg.Height, "framebuffer height")
	f.StringVar(&o.cfg.Renderer, "renderer", o.cfg.Renderer, "renderer: soft or gpu")
	f.StringVar(&o.cfg.Map, "map", o.cfg.Map, "map slug")
	f.StringVar(&o.pak, "pak", "", "pak0.pak for the native target (default: embedded / procedural)")
	f.IntVar(&o.rounds, "rounds", 5, "rounds per target (at least 5 for a publishable table)")
	f.Float64Var(&o.maxLoad, "max-load", 8, "discard a round whose 1-minute load is above this")
	f.IntVar(&o.maxRedo, "max-redo", 20, "give up after this many discards for one round")
	f.DurationVar(&o.wait, "wait", 30*time.Second, "pause after a discard")
	f.StringVar(&o.wasm, "wasm", "", "js/wasm build of this command (omit: native only)")
	f.StringVar(&o.execJS, "wasm-exec", filepath.Join(runtime.GOROOT(), "lib", "wasm", "wasm_exec.js"), "wasm_exec.js of the Go that built --wasm")
	f.StringVar(&o.chrome, "chromium", "", "headless Chromium binary (with --wasm)")
	f.StringVar(&o.profile, "profile", "", "Chromium user-data dir (with --wasm; a durable directory)")
	f.DurationVar(&o.timeout, "timeout", 5*time.Minute, "limit for one round")
	root.AddCommand(c)
}

func uptimeLoad(ctx context.Context) (float64, error) {
	out, err := execOutput(ctx, "uptime")
	if err != nil {
		return 0, err
	}
	return bench.ParseLoad(string(out))
}

func baseline(cmd *cobra.Command, o baselineOpts) error {
	if o.rounds < 1 {
		return fmt.Errorf("--rounds must be at least 1")
	}
	ctx := cmd.Context() // cobra defaults it to context.Background()
	exe, err := self()
	if err != nil {
		return err
	}
	native := bench.Target{Name: "native", Run: func(ctx context.Context, cfg bench.Config) (bench.Result, error) {
		args := append(cfg.Args(), "--procs", "1")
		if o.pak != "" {
			args = append(args, "--pak", o.pak)
		}
		cctx, cancel := context.WithTimeout(ctx, o.timeout)
		defer cancel()
		out, err := execOutput(cctx, exe, args...)
		if err != nil {
			return bench.Result{}, err
		}
		return bench.ParseResult(string(out))
	}}
	targets := []bench.Target{native}
	if o.wasm != "" {
		if o.chrome == "" || o.profile == "" {
			return fmt.Errorf("--wasm needs --chromium and --profile")
		}
		launch := func(ctx context.Context, url string) (func(), error) {
			return startProcess(ctx, o.chrome, "--headless", "--no-sandbox", "--disable-gpu",
				"--no-first-run", "--user-data-dir="+o.profile, url)
		}
		targets = append(targets, bench.Target{Name: "js/wasm (Chromium)",
			Run: func(ctx context.Context, cfg bench.Config) (bench.Result, error) {
				cctx, cancel := context.WithTimeout(ctx, o.timeout)
				defer cancel()
				return bench.RunInBrowser(cctx, o.wasm, o.execJS, cfg, launch)
			}})
	}
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
	fmt.Fprintf(out, "\npak: %s, go: %s, rounds per target: %d, GOMAXPROCS native: 1\n\n",
		rounds[0].Result.Pak, rounds[0].Result.Go, o.rounds)
	for _, r := range rounds {
		if r.Result.Browser != "" {
			fmt.Fprintf(out, "browser: %s\n\n", r.Result.Browser)
			break
		}
	}
	fmt.Fprint(out, bench.Table(rounds, names))
	return nil
}
