// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package runner

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/go-quake1/engine/backend"
	"github.com/go-quake1/engine/render"
	"github.com/go-quake1/engine/runloop"
)

func TestSetupRejectsUnknownRenderer(t *testing.T) {
	_, err := Setup(SetupOpts{
		Backend:  backend.NewRecorder(320, 240),
		PakFS:    fstest.MapFS{},
		FBWidth:  320,
		FBHeight: 240,
		Renderer: "vulkan",
		Logf:     func(string, ...any) {},
	})
	if !errors.Is(err, render.ErrUnknownKind) {
		t.Fatalf("err = %v, want ErrUnknownKind", err)
	}
}

// TestInstallRenderer: soft is honoured silently; gpu, which does not
// exist yet, falls back to the software renderer over Pre2DDraw and
// the log says why. The timer is wired either way.
func TestInstallRenderer(t *testing.T) {
	for _, tc := range []struct {
		kind     render.Kind
		fallback bool
	}{{render.KindSoft, false}, {render.KindGPU, true}} {
		t.Run(string(tc.kind), func(t *testing.T) {
			var log strings.Builder
			logf := func(format string, args ...any) { fmt.Fprintf(&log, format+"\n", args...) }
			ft, err := render.NewFrameTimer(func() float64 { return 0 }, 4)
			if err != nil {
				t.Fatal(err)
			}
			r := &runloop.Runner{}
			called := false
			r.Pre2DDraw = func(*render.FrameBuffer, [3]float32, [3]float32) error {
				called = true
				return nil
			}
			installRenderer(r, tc.kind, ft, logf)
			if r.Renderer == nil || r.Renderer.Kind() != render.KindSoft || !r.Renderer.HasScene() {
				t.Fatalf("renderer = %#v", r.Renderer)
			}
			if err := r.Renderer.DrawScene(nil, [3]float32{}, [3]float32{}); err != nil || !called {
				t.Fatalf("scene not routed to Pre2DDraw: %v %v", err, called)
			}
			if r.Timer != ft {
				t.Fatal("frame timer not wired")
			}
			if got := strings.Contains(log.String(), "unavailable"); got != tc.fallback {
				t.Fatalf("fallback logged = %v, want %v:\n%s", got, tc.fallback, log.String())
			}
		})
	}
}
