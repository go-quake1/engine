// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

//go:build js && wasm

package main

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"time"

	"github.com/go-quake1/engine/render"
	"github.com/go-quake1/engine/render/gpuworld"
	"github.com/go-quake1/engine/worldview"
)

// aliasContent loads the map's alias entities and the 16 alias views, with
// the ablations of opts applied to the data both sides read.
func aliasContent(fsys fs.FS, w *worldview.World) ([]worldview.AliasEnt, []worldview.AliasView, render.AliasShadeRange, error) {
	shade := worldview.AliasShade
	ents, err := w.AliasEntities(fsys)
	if err != nil {
		return nil, nil, shade, err
	}
	var weapon worldview.AliasEnt
	haveWeapon := false
	views, err := w.AliasViews(ents, func(v worldview.View) (worldview.AliasEnt, error) {
		if !haveWeapon {
			weapon, err = worldview.ViewWeapon(fsys, v)
			if err != nil {
				return worldview.AliasEnt{}, err
			}
			haveWeapon = true
		}
		we := weapon
		we.Origin, we.Angles = v.Origin, v.Angles
		return we, nil
	})
	if err != nil {
		return nil, nil, shade, err
	}
	if opts.ablate["--flatLight"] {
		shade = render.AliasShadeRange{Ambient: 1, LightDir: [3]float32{0, 0, -1}}
	}
	flat := &render.Pic{Width: 1, Height: 1, Pixels: []byte{0x70}}
	seen := map[*worldview.AliasModel]bool{}
	for vi := range views {
		kept := views[vi].Ents[:0]
		for _, e := range views[vi].Ents {
			if opts.ablate["--noWeapon"] && e.IsViewWeapon {
				continue
			}
			if opts.ablate["--noLerp"] {
				e.Next, e.Lerp = e.Frame, 0
			}
			if opts.ablate["--flatSkin"] && !seen[e.Model] {
				seen[e.Model] = true
				for i := range e.Model.Skins {
					e.Model.Skins[i] = flat
				}
			}
			kept = append(kept, e)
		}
		views[vi].Ents = kept
	}
	return ents, views, shade, nil
}

// modelsOf lists the distinct models of the views' entities.
func modelsOf(views []worldview.AliasView) []*worldview.AliasModel {
	seen := map[*worldview.AliasModel]bool{}
	var out []*worldview.AliasModel
	for _, v := range views {
		for _, e := range v.Ents {
			if !seen[e.Model] {
				seen[e.Model] = true
				out = append(out, e.Model)
			}
		}
	}
	return out
}

// corruptSkins returns a copy of the models whose skins have every palette
// index shifted by 16, and a map from the true models to the copies.
func corruptSkins(models []*worldview.AliasModel) ([]*worldview.AliasModel, map[*worldview.AliasModel]*worldview.AliasModel) {
	m := map[*worldview.AliasModel]*worldview.AliasModel{}
	var out []*worldview.AliasModel
	for _, am := range models {
		c := *am
		c.Skins = nil
		for _, s := range am.Skins {
			px := make([]byte, len(s.Pixels))
			for i, b := range s.Pixels {
				px[i] = b + 16
			}
			c.Skins = append(c.Skins, &render.Pic{Width: s.Width, Height: s.Height, Pixels: px})
		}
		m[am] = &c
		out = append(out, &c)
	}
	return out, m
}

// compare counts, over the pixels of mask (all when nil), those where got
// differs from ref and those that are not a one-pixel shift of ref.
func compare(ref []byte, pitch int, got []byte, width, height int, mask []bool) (n, differ, unexplained int) {
	at := func(x, y int) byte { return ref[y*pitch+x] }
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			if mask != nil && !mask[y*width+x] {
				continue
			}
			n++
			g := got[y*width+x]
			if g == at(x, y) {
				continue
			}
			differ++
			explained := false
			for dy := -1; dy <= 1 && !explained; dy++ {
				for dx := -1; dx <= 1 && !explained; dx++ {
					nx, ny := x+dx, y+dy
					explained = nx >= 0 && ny >= 0 && nx < width && ny < height && at(nx, ny) == g
				}
			}
			if !explained {
				unexplained++
			}
		}
	}
	return
}

// checkAlias compares the GPU alias pass with the engine's software one on
// the 16 alias views. The figures are over the alias region: the pixels where
// either side's alias frame differs from the same side's world-only frame.
func checkAlias(ctx context.Context, v verdict, fsys fs.FS, w *worldview.World, cm *render.ColorMap, gr *gpuworld.Renderer, corrupt, benchRun bool) verdict {
	_, views, shade, err := aliasContent(fsys, w)
	if err != nil {
		v.Detail = err.Error()
		return v
	}
	if len(views) == 0 {
		v.Detail = "no alias views"
		return v
	}
	gpuModels := modelsOf(views)
	var swap map[*worldview.AliasModel]*worldview.AliasModel
	if corrupt {
		gpuModels, swap = corruptSkins(gpuModels)
	}
	set, err := gpuworld.BuildAlias(gpuModels, shade)
	if err != nil {
		v.Detail = fmt.Sprintf("alias set: %v", err)
		return v
	}
	if err := gr.EnableAlias(set); err != nil {
		v.Detail = fmt.Sprintf("alias gpu: %v", err)
		return v
	}
	if benchRun {
		v.PartBench = benchAlias(ctx, w, gr, cm, shade, views, swap, v.Width, v.Height)
		v.Check = "BENCH"
		return v
	}
	width, height := v.Width, v.Height
	for i, av := range views {
		refW, _ := render.NewFrameBuffer(width, height)
		if err := w.DrawSoftware(refW, cm, av.View); err != nil {
			v.Detail = err.Error()
			return v
		}
		refA, _ := render.NewFrameBuffer(width, height)
		copy(refA.Pixels, refW.Pixels)
		if err := w.DrawAliasSoftware(refA, cm, shade, av.View, av.Ents); err != nil {
			v.Detail = fmt.Sprintf("reference view %d: %v", i, err)
			return v
		}
		gents := av.Ents
		if swap != nil {
			gents = make([]worldview.AliasEnt, len(av.Ents))
			for k, e := range av.Ents {
				e.Model = swap[e.Model]
				gents[k] = e
			}
		}
		if err := gr.Draw(av.View); err != nil {
			v.Detail = fmt.Sprintf("draw world view %d: %v", i, err)
			return v
		}
		gotW, err := gr.Readback(ctx)
		if err != nil {
			v.Detail = fmt.Sprintf("readback view %d: %v", i, err)
			return v
		}
		if err := gr.DrawEntities(av.View, gents); err != nil {
			v.Detail = fmt.Sprintf("draw view %d: %v", i, err)
			return v
		}
		gotA, err := gr.Readback(ctx)
		if err != nil {
			v.Detail = fmt.Sprintf("readback view %d: %v", i, err)
			return v
		}
		mask := make([]bool, width*height)
		st := viewStat{View: i}
		for y := 0; y < height; y++ {
			for x := 0; x < width; x++ {
				r := refA.Pixels[y*refA.Pitch+x] != refW.Pixels[y*refW.Pitch+x]
				g := gotA[y*width+x] != gotW[y*width+x]
				if r || g {
					mask[y*width+x] = true
				}
				if g {
					st.Drawn++
				}
			}
		}
		st.Pixels, st.Differ, st.Interior = compare(refA.Pixels, refA.Pitch, gotA, width, height, mask)
		if st.Pixels > 0 {
			st.Rate = float64(st.Differ) / float64(st.Pixels)
		}
		v.Views = append(v.Views, st)
		v.Pixels += st.Pixels
		v.Differ += st.Differ
		v.Interior += st.Interior
		fmt.Fprintf(os.Stderr, "worldcheck alias: view %d (%d entities): %d alias pixels, %d differ, %d unexplained\n", i, len(av.Ents), st.Pixels, st.Differ, st.Interior)
	}
	if v.Pixels == 0 {
		v.Detail = "no alias pixels in any view"
		return v
	}
	v.TotalRate = float64(v.Differ) / float64(v.Pixels)
	v.InteriorRate = float64(v.Interior) / float64(v.Pixels)
	if v.InteriorRate <= v.Tolerance {
		v.Check = "PASS"
	} else {
		v.Detail = fmt.Sprintf("unexplained mismatch %.4f above the tolerance %.4f", v.InteriorRate, v.Tolerance)
	}
	return v
}

// benchAlias alternates, per frame, the engine's software path for the same
// content (DrawEngine world, then the engine's alias pass), the software
// reference, the GPU world alone and the GPU world with entities.
func benchAlias(ctx context.Context, w *worldview.World, gr *gpuworld.Renderer, cm *render.ColorMap, shade render.AliasShadeRange, views []worldview.AliasView, swap map[*worldview.AliasModel]*worldview.AliasModel, width, height int) *partBench {
	fb, _ := render.NewFrameBuffer(width, height)
	const passes = 8
	var en, al, gw, ga []float64
	ms := func(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }
	for p := 0; p < passes+1; p++ {
		for _, av := range views {
			t0 := time.Now()
			_ = w.DrawEngine(fb, av.View)
			t1 := time.Now()
			_ = w.DrawAliasSoftware(fb, cm, shade, av.View, av.Ents)
			t2 := time.Now()
			_ = gr.Draw(av.View)
			_ = gr.Wait(ctx)
			t3 := time.Now()
			_ = gr.DrawEntities(av.View, av.Ents)
			_ = gr.Wait(ctx)
			t4 := time.Now()
			if p > 0 {
				en, al = append(en, ms(t2.Sub(t0))), append(al, ms(t2.Sub(t1)))
				gw, ga = append(gw, ms(t3.Sub(t2))), append(ga, ms(t4.Sub(t3)))
			}
		}
	}
	return &partBench{Frames: len(en), EngineFrameMS: pct(en, .5), EngineFrameP95MS: pct(en, .95), EnginePartMS: pct(al, .5), EnginePartP95MS: pct(al, .95),
		GPUWorldWallMS: pct(gw, .5), GPUWorldWallP95MS: pct(gw, .95), GPUFrameWallMS: pct(ga, .5), GPUFrameWallP95MS: pct(ga, .95)}
}

// partBench is the benchmark of a part after the world: the engine's
// software frame (world path plus the part) and the part alone, and the GPU
// world alone and with the part, wall time until the GPU is idle.
type partBench struct {
	Frames            int     `json:"frames"`
	EngineFrameMS     float64 `json:"engine_frame_ms"`
	EngineFrameP95MS  float64 `json:"engine_frame_p95_ms"`
	EnginePartMS      float64 `json:"engine_part_ms"`
	EnginePartP95MS   float64 `json:"engine_part_p95_ms"`
	GPUWorldWallMS    float64 `json:"gpu_world_wall_ms"`
	GPUWorldWallP95MS float64 `json:"gpu_world_wall_p95_ms"`
	GPUFrameWallMS    float64 `json:"gpu_frame_wall_ms"`
	GPUFrameWallP95MS float64 `json:"gpu_frame_wall_p95_ms"`
}
