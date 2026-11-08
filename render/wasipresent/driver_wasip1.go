// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

//go:build wasip1

package wasipresent

import "github.com/go-wasmcontainers/gpu/wasigpu"

// WasiDriver is the [Driver] over a wasigpu device and surface. It only
// runs in a wasip1 guest on a host that offers wasi:webgpu and
// wasi-gfx:surface (the playground with --gpu); the browser run in the
// playground's e2e checks it end to end.
type WasiDriver struct {
	d        *wasigpu.Device
	s        *wasigpu.Surface
	pipe     *wasigpu.RenderPipeline
	idx, pal *wasigpu.Buffer
}

var _ Driver = (*WasiDriver)(nil)

// NewDriver returns the driver drawing on s with d.
func NewDriver(d *wasigpu.Device, s *wasigpu.Surface) *WasiDriver {
	return &WasiDriver{d: d, s: s}
}

// Pipeline implements [Driver].
func (w *WasiDriver) Pipeline(wgsl string) error {
	p, err := w.s.RenderPipeline(wgsl, "vs", "fs")
	if err != nil {
		return err
	}
	if w.pipe != nil {
		w.pipe.Drop()
	}
	w.pipe = p
	return nil
}

// Buffers implements [Driver].
func (w *WasiDriver) Buffers(indexBytes int) error {
	idx, err := w.d.Buffer(uint64(indexBytes), wasigpu.Storage|wasigpu.CopyDst)
	if err != nil {
		return err
	}
	pal, err := w.d.Buffer(256*4, wasigpu.Storage|wasigpu.CopyDst)
	if err != nil {
		idx.Drop()
		return err
	}
	w.dropBuffers()
	w.idx, w.pal = idx, pal
	return nil
}

func (w *WasiDriver) dropBuffers() {
	if w.idx != nil {
		w.idx.Drop()
		w.pal.Drop()
		w.idx, w.pal = nil, nil
	}
}

// WriteIndices implements [Driver].
func (w *WasiDriver) WriteIndices(data []byte) error { return w.d.Write(w.idx, 0, data) }

// WritePalette implements [Driver].
func (w *WasiDriver) WritePalette(data []byte) error { return w.d.Write(w.pal, 0, data) }

// Draw implements [Driver].
func (w *WasiDriver) Draw() error {
	return w.s.Frame(w.pipe, []*wasigpu.Buffer{w.idx, w.pal}, [4]float64{0, 0, 0, 1}, 3)
}

// Close implements [Driver].
func (w *WasiDriver) Close() {
	if w.pipe != nil {
		w.pipe.Drop()
		w.pipe = nil
	}
	w.dropBuffers()
}
