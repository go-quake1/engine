// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

//go:build js && wasm

package bench

import (
	"context"
	"fmt"
	"syscall/js"
	"time"

	"github.com/go-quake1/engine/backend/wasm"
	"github.com/go-quake1/engine/render"
	"github.com/go-quake1/engine/render/webgpupresent"
)

func init() { newPresent = browserPresent }

// browserPresent wires the page's canvas#quake: the standalone page's
// software path (CPU RGBA + putImageData) or the WebGPU presenter.
func browserPresent(cfg Config) (*PresentSetup, error) {
	switch cfg.Present {
	case PresentCanvas2D:
		fb, err := wasm.NewCanvasFramebuffer("#quake", cfg.Width, cfg.Height)
		if err != nil {
			return nil, err
		}
		return &PresentSetup{Present: func(rgba []byte, _, _ int) error { return fb.PresentRGBA(rgba) }}, nil
	default: // PresentWebGPU
		canvas := js.Global().Get("document").Call("querySelector", "#quake")
		if !canvas.Truthy() {
			return nil, fmt.Errorf("bench: no canvas#quake on the page")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		p, err := webgpupresent.New(ctx, webgpupresent.Config{
			Width: cfg.Width, Height: cfg.Height,
			Canvas:        canvas.Call("transferControlToOffscreen"),
			AllowFallback: !cfg.RequireGPU,
		})
		if err != nil {
			return nil, err
		}
		info := p.Adapter()
		return &PresentSetup{
			Renderer: func(scene func(*render.FrameBuffer, [3]float32, [3]float32) error) (render.Renderer, error) {
				return render.NewPresentRenderer(scene, p)
			},
			Wait: func() error {
				wctx, wcancel := context.WithTimeout(context.Background(), 60*time.Second)
				defer wcancel()
				return p.Wait(wctx)
			},
			Adapter:  fmt.Sprintf("%s %s %s", info.Vendor, info.Architecture, info.Description),
			Fallback: info.Fallback,
			Close:    p.Close,
		}, nil
	}
}
