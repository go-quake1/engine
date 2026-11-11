// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

//go:build wasip1

package main

import (
	"github.com/go-wasmcontainers/gpu/wasigpu/batchsink"

	"github.com/go-quake1/engine/render"
	"github.com/go-quake1/engine/render/gpuworld"
	"github.com/go-quake1/engine/worldview"
)

func init() { openGPUWorld = openBatchWorld }

// batchWorld is render/gpuworld on the wasi_webgpu_batch device of a container
// run with --gpu: a frame is one host crossing, with the read-back in it.
type batchWorld struct {
	sink *batchsink.Sink
	r    *gpuworld.Renderer
}

func openBatchWorld(wv *worldview.World, cm *render.ColorMap, w, h int) (gpuDrawer, error) {
	s, err := batchsink.Open()
	if err != nil {
		return nil, err
	}
	r, err := gpuworld.New(s.Device("quake"), wv, cm, w, h)
	if err != nil {
		s.Close()
		return nil, err
	}
	return &batchWorld{sink: s, r: r}, nil
}

func (b *batchWorld) Draw(v worldview.View) ([]byte, error) { return b.r.DrawRead(v) }
func (b *batchWorld) Close()                                { b.sink.Close() }
