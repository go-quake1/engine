// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/go-quake1/engine/render"
	"github.com/go-quake1/engine/render/gpuworld"
	"github.com/go-quake1/engine/worldview"
)

// layerGPU is a fake device with an entity layer: the world frame is all 0x10,
// the layer is EntityClear except its first row, which is 0x31.
type layerGPU struct {
	w, h      int
	models    *[]*worldview.AliasModel
	frames    *int
	entsSeen  *[][]worldview.AliasEnt
	enableErr error
}

func (l layerGPU) Draw(worldview.View) ([]byte, error) { *l.frames++; return nil, nil }
func (l layerGPU) Close()                              {}
func (l layerGPU) EnableEntities(m []*worldview.AliasModel) error {
	*l.models = m
	return l.enableErr
}
func (l layerGPU) DrawLayers(_ worldview.View, ents []worldview.AliasEnt) ([]byte, []byte, error) {
	*l.frames++
	*l.entsSeen = append(*l.entsSeen, ents)
	world := bytes.Repeat([]byte{0x10}, l.w*l.h)
	layer := bytes.Repeat([]byte{gpuworld.EntityClear}, l.w*l.h)
	for x := 0; x < l.w; x++ {
		layer[x] = 0x31
	}
	return world, layer, nil
}

// TestGPUEntities (needs QUAKE_PAK): with a device that has an entity layer the
// runner gives it the alias models (those without animated frame groups) once,
// hands it each frame's entities and composites its layer over the frame after
// the CPU's sky and water; with the planted defect (--corrupt-gpu-skins) the device
// gets copies whose skins are shifted by 16, and the entities carry the originals.
func TestGPUEntities(t *testing.T) {
	firstTexel := map[bool]byte{}
	for _, corrupt := range []bool{false, true} {
		var models []*worldview.AliasModel
		var seen [][]worldview.AliasEnt
		frames := 0
		withSurface(t, &fakePresenter{}, false, new(int))
		old := openGPUWorld
		openGPUWorld = func(_ *worldview.World, _ *render.ColorMap, w, h int) (gpuDrawer, error) {
			return layerGPU{w: w, h: h, models: &models, frames: &frames, entsSeen: &seen}, nil
		}
		var out, errOut bytes.Buffer
		err := runGameWith(&out, &errOut, realFS(t), "e1m1", 320, 200, 150, gameOpts{auto: true, corruptSkins: corrupt, minGPUEntities: 1, gpuEntities: true})
		openGPUWorld = old
		if err != nil {
			t.Fatalf("%v\n%s", err, errOut.String())
		}
		n := 0
		for _, m := range models {
			if m != nil {
				n++
			}
		}
		if n == 0 {
			t.Fatalf("corrupt %v: no model on the GPU", corrupt)
		}
		if !strings.Contains(errOut.String(), "GPU entities:") {
			t.Errorf("no log line of the GPU entities in %q", errOut.String())
		}
		for _, m := range models {
			if m == nil {
				continue
			}
			if m.M == nil || len(m.Skins) == 0 {
				t.Fatalf("model %+v", m)
			}
			firstTexel[corrupt] = m.Skins[0].Pixels[0]
			break
		}
		// (the entities of a frame reach the device only once the signon has
		// drained, which the playground's TestQuake sees on a real GPU)
		for _, ents := range seen {
			for _, e := range ents {
				if e.Model == nil {
					t.Errorf("an entity with no model")
				}
			}
		}
	}
	if firstTexel[true] != firstTexel[false]+16 {
		t.Errorf("the planted defect gives the GPU skin texel %d, want the true %d + 16", firstTexel[true], firstTexel[false])
	}
}

var errTest = errors.New("the device refuses the models")

// TestGPUEntitiesAreOptIn: by default the CPU draws the alias entities and the log
// says why (the entity layer lost the device on the macOS CI lane).
func TestGPUEntitiesAreOptIn(t *testing.T) {
	var m []*worldview.AliasModel
	var s [][]worldview.AliasEnt
	withSurface(t, &fakePresenter{}, false, new(int))
	old := openGPUWorld
	openGPUWorld = func(_ *worldview.World, _ *render.ColorMap, w, h int) (gpuDrawer, error) {
		return layerGPU{w: w, h: h, models: &m, frames: new(int), entsSeen: &s}, nil
	}
	var out, errOut bytes.Buffer
	err := runGameWith(&out, &errOut, realFS(t), "e1m1", 64, 40, 3, gameOpts{auto: true})
	openGPUWorld = old
	if err != nil {
		t.Fatal(err)
	}
	if m != nil || !strings.Contains(errOut.String(), "off by default, pass --gpu-entities") {
		t.Errorf("models given to the device: %v; log %q", m, errOut.String())
	}
}

// TestGPUEntitiesUnavailable: a device with no entity layer, or that refuses the
// models, leaves the alias entities to the CPU, and the game still plays.
func TestGPUEntitiesUnavailable(t *testing.T) {
	for _, c := range []struct {
		name string
		open func(w, h int) gpuDrawer
	}{
		{"no entity layer", func(w, h int) gpuDrawer { return countingGPU{frames: new(int), w: w, h: h, closed: new(int)} }},
		{"refused", func(w, h int) gpuDrawer {
			var m []*worldview.AliasModel
			var s [][]worldview.AliasEnt
			return layerGPU{w: w, h: h, models: &m, frames: new(int), entsSeen: &s, enableErr: errTest}
		}},
	} {
		withSurface(t, &fakePresenter{}, false, new(int))
		old := openGPUWorld
		openGPUWorld = func(_ *worldview.World, _ *render.ColorMap, w, h int) (gpuDrawer, error) { return c.open(w, h), nil }
		var out, errOut bytes.Buffer
		err := runGameWith(&out, &errOut, realFS(t), "e1m1", 64, 40, 3, gameOpts{auto: true, gpuEntities: true})
		openGPUWorld = old
		if err != nil {
			t.Fatalf("%s: %v\n%s", c.name, err, errOut.String())
		}
		if !strings.Contains(errOut.String(), "GPU entities unavailable") {
			t.Errorf("%s: no log line in %q", c.name, errOut.String())
		}
	}
}
