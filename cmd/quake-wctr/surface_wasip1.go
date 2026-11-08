// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

//go:build wasip1

package main

import (
	"errors"
	"fmt"

	"github.com/go-wasmcontainers/gpu/wasigpu"

	"github.com/go-quake1/engine/render/wasipresent"
)

func init() { openSurface = openWasiSurface }

// openWasiSurface opens the device, a w x h surface on it, the presenter
// and the key streams. It needs a host with wasi:webgpu and
// wasi-gfx:surface: the playground's wctr run --gpu.
func openWasiSurface(w, h int) (*gpuSurface, error) {
	d, err := wasigpu.Open()
	if errors.Is(err, wasigpu.ErrNoAdapter) {
		return nil, errors.New("quake-wctr: no GPU adapter (run with wctr --gpu in a browser with WebGPU)")
	}
	if err != nil {
		return nil, err
	}
	s, err := d.Surface(uint32(w), uint32(h))
	if err != nil {
		d.Drop()
		return nil, fmt.Errorf("quake-wctr: surface %dx%d: %w", w, h, err)
	}
	drv := wasipresent.NewDriver(d, s)
	p, err := wasipresent.New(drv, w, h)
	if err != nil {
		s.Drop()
		d.Drop()
		return nil, err
	}
	down, err := s.Keys(wasigpu.KeyDown)
	if err != nil {
		s.Drop()
		d.Drop()
		return nil, err
	}
	up, err := s.Keys(wasigpu.KeyUp)
	if err != nil {
		down.Drop()
		s.Drop()
		d.Drop()
		return nil, err
	}
	return &gpuSurface{Presenter: p, Down: down, Up: up, Close: func() {
		up.Drop()
		down.Drop()
		p.Close()
		s.Drop()
		d.Drop()
	}}, nil
}
