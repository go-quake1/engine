// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

//go:build !js

package main

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

const presentArgs = "--wasm w --wasm-exec e --chromium c --profile p"

func TestPresentInterleavesThreeConfigurations(t *testing.T) {
	fakes(t, "2.0")
	var launches [][]string
	startProcess = func(ctx context.Context, _ string, args ...string) (func(), error) {
		launches = append(launches, args)
		url := args[len(args)-1]
		go func() {
			body := `{"pak":"procedural","go":"go1.27.3","adapter":"apple metal","summary":{"frames":5}}` + "\n"
			req, _ := http.NewRequestWithContext(ctx, "POST", url+"result", strings.NewReader(body))
			req.Header.Set("X-UA", "FakeChrome/9")
			if r, err := http.DefaultClient.Do(req); err == nil {
				r.Body.Close()
			}
		}()
		return func() {}, nil
	}
	out, err := cli(t, append([]string{"present", "--rounds", "2", "--chromium-args", "--use-angle=metal"}, strings.Fields(presentArgs)...)...)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"round 1 wasm: ", "round 2 wasm+canvas2d-present: ", "round 3 wasm+webgpu-present: ", "round 4 wasm: ",
		"| metric | wasm | wasm+canvas2d-present | wasm+webgpu-present |",
		"browser: FakeChrome/9", "adapter: apple metal (fallback false)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if len(launches) != 6 {
		t.Fatalf("%d launches, want 6", len(launches))
	}
	if j := strings.Join(launches[0], " "); !strings.Contains(j, "--disable-gpu") || strings.Contains(j, "webgpu") {
		t.Errorf("the baseline browser must keep its flags: %s", j)
	}
	for _, i := range []int{1, 2} {
		j := strings.Join(launches[i], " ")
		if strings.Contains(j, "--disable-gpu") || !strings.Contains(j, "--enable-unsafe-webgpu --use-angle=metal") {
			t.Errorf("launch %d must enable the GPU: %s", i, j)
		}
	}
}

func TestPresentErrors(t *testing.T) {
	fakes(t, "2.0")
	if _, err := cli(t, "present"); err == nil {
		t.Error("the required flags are required")
	}
	if _, err := cli(t, append([]string{"present", "--rounds", "0"}, strings.Fields(presentArgs)...)...); err == nil {
		t.Error("zero rounds must fail")
	}
	fakes(t, "9.5")
	out, err := cli(t, append([]string{"present", "--rounds", "1", "--max-redo", "1"}, strings.Fields(presentArgs)...)...)
	if err == nil || !strings.Contains(out, "discarded wasm: load1 9.50 before") {
		t.Errorf("a loaded machine must refuse and list the discards: out=%q err=%v", out, err)
	}
}

func TestPresentAllowFallbackIsPassedToTheWebGPURounds(t *testing.T) {
	o := presentOpts{chrome: "c", profile: "p", wasm: "w", execJS: "e", allowFallback: true}
	ts := presentTargets(o)
	if len(ts) != 3 || ts[2].Name != nameWebGPU {
		t.Fatalf("targets %v", ts)
	}
}
