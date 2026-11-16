// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package main

import (
	"bytes"
	"testing"
)

// TestLoopYieldsEachFrame: the frame loop yields to the scheduler once per frame.
// Without it the quake image, in the browser, never collected its garbage and
// died of "out of memory" after 20 to 30 s in the main menu (heap +331 KiB per
// frame, the GC count frozen; go-wasmcontainers/playground#56). A wasm guest
// under wazero collects without the yield, so the browser's behaviour cannot be
// reproduced natively: this pins the call, the playground's 60 s soaks pin its
// effect.
func TestLoopYieldsEachFrame(t *testing.T) {
	var n int
	old := yield
	yield = func() { n++ }
	t.Cleanup(func() { yield = old })
	oldOpen := openSurface
	openSurface = func(w, h int) (*gpuSurface, error) {
		return &gpuSurface{Presenter: &fakePresenter{}, Down: fakeKeys{}, Up: fakeKeys{}, Close: func() {}}, nil
	}
	t.Cleanup(func() { openSurface = oldOpen })
	var errOut bytes.Buffer
	const frames = 12
	if err := runGame(&bytes.Buffer{}, &errOut, realFS(t), "", 64, 40, frames); err != nil {
		t.Fatalf("runGame: %v\n%s", err, errOut.String())
	}
	if n != frames {
		t.Errorf("the loop yielded %d times in %d frames, want once per frame", n, frames)
	}
}
