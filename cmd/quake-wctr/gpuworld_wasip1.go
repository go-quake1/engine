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

// EnableEntities builds the alias models on the device and gives the entities a
// layer of their own (gpuworld.Renderer.EnableAliasLayer).
func (b *batchWorld) EnableEntities(models []*worldview.AliasModel) error {
	var set []*worldview.AliasModel
	for _, m := range models {
		if m != nil {
			set = append(set, m)
		}
	}
	as, err := gpuworld.BuildAlias(set)
	if err != nil {
		return err
	}
	if err := b.r.EnableAlias(as); err != nil {
		return err
	}
	return b.r.EnableAliasLayer()
}

func (b *batchWorld) DrawLayers(v worldview.View, ents []worldview.AliasEnt) ([]byte, []byte, error) {
	return b.r.DrawReadLayers(v, ents)
}

func (b *batchWorld) Draw(v worldview.View) ([]byte, error) { return b.r.DrawRead(v) }
func (b *batchWorld) Close()                                { b.sink.Close() }
