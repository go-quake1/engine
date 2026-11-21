// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

//go:build wasip1

package main

import (
	"fmt"
	"time"

	"github.com/go-gfx/webgpu"
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
	dev  *webgpu.Device
	r    *gpuworld.Renderer
}

func openBatchWorld(wv *worldview.World, cm *render.ColorMap, w, h int) (gpuDrawer, error) {
	s, err := batchsink.Open()
	if err != nil {
		return nil, err
	}
	dev := s.Device("quake")
	r, err := gpuworld.New(dev, wv, cm, w, h)
	if err != nil {
		s.Close()
		return nil, err
	}
	return &batchWorld{sink: s, dev: dev, r: r}, nil
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
	w, l, err := b.r.DrawReadLayers(v, ents)
	return w, l, b.lost(err)
}

func (b *batchWorld) Draw(v worldview.View) ([]byte, error) {
	f, err := b.r.DrawRead(v)
	return f, b.lost(err)
}

// lost adds to an error why the device was lost, once it was: a submit the host
// refuses or cuts short loses it with the host's message ("webgpu: object
// destroyed" alone says only that it is gone).
func (b *batchWorld) lost(err error) error {
	if err == nil {
		return nil
	}
	if info := b.dev.LostInfo(); info.Reason != "" {
		return fmt.Errorf("%w (device lost: %s: %s)", err, info.Reason, info.Message)
	}
	return err
}
func (b *batchWorld) Close() { b.sink.Close() }

// ResetSlowest and Slowest are the sink's longest host call of the frame
// (drawTimer): the submit crosses through the sink, not the presenter's device.
func (b *batchWorld) ResetSlowest()                    { b.sink.ResetSlowest() }
func (b *batchWorld) Slowest() (string, time.Duration) { return b.sink.Slowest() }

// SetIdle makes a frame's wait for the GPU run f between polls of the host
// (batchsink.Sink.SetIdle): WinQuake's S_ExtraUpdate inside rendering.
func (b *batchWorld) SetIdle(f func() error) { b.sink.SetIdle(f) }
