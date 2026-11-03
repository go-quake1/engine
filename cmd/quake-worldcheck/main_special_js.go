// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

//go:build js && wasm

package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/go-quake1/engine/render"
	"github.com/go-quake1/engine/render/gpuworld"
	"github.com/go-quake1/engine/worldview"
)

// specialKind maps --part to a kind of special face.
func specialKind(part string) worldview.SpecialKind {
	if part == "turb" {
		return worldview.KindTurb
	}
	return worldview.KindSky
}

// shiftedTextures returns the true textures of the faces of kind and puts
// in their place copies with every palette index shifted by 16; the caller
// restores them after building the GPU content.
func shiftedTextures(w *worldview.World, kind worldview.SpecialKind) (restore func()) {
	orig := map[*worldview.SpecialFace]*render.Pic{}
	copies := map[*render.Pic]*render.Pic{}
	for _, f := range w.Special {
		if f == nil || f.Kind != kind {
			continue
		}
		orig[f] = f.Tex
		c, ok := copies[f.Tex]
		if !ok {
			px := make([]byte, len(f.Tex.Pixels))
			for i, b := range f.Tex.Pixels {
				px[i] = b + 16
			}
			c = &render.Pic{Width: f.Tex.Width, Height: f.Tex.Height, Pixels: px}
			copies[f.Tex] = c
		}
		f.Tex = c
	}
	return func() {
		for f, p := range orig {
			f.Tex = p
		}
	}
}

// checkSpecial compares the GPU sky (part "sky") or water (part "turb")
// with the engine's software fill on 16 fixed views, over the region of
// the part: the pixels where either side's frame with the part differs from
// the same side's frame without it.
func checkSpecial(ctx context.Context, v verdict, w *worldview.World, cm *render.ColorMap, gr *gpuworld.Renderer, part string, corrupt, benchRun bool) verdict {
	kind := specialKind(part)
	t := worldview.DemoTime
	if opts.ablate["--noScroll"] {
		t = 0
	}
	if opts.ablate["--flatSkin"] {
		flat := &render.Pic{Width: 2, Height: 1, Pixels: []byte{0x70, 0x70}}
		for _, f := range w.Special {
			if f != nil && f.Kind == kind {
				f.Tex = flat
			}
		}
	}
	if opts.ablate["--exactSpans"] {
		render.TurbSubSpan = 1 // the reference exact per pixel: what the 16-pixel steps cost
	}
	views := w.SpecialViews(kind, 16)
	if len(views) == 0 {
		v.Detail = "no " + part + " views"
		return v
	}
	restore := func() {}
	if corrupt {
		restore = shiftedTextures(w, kind)
	}
	err := enableSpecial(gr, kind)
	restore()
	if err != nil {
		v.Detail = fmt.Sprintf("enable %s: %v", part, err)
		return v
	}
	gr.Time = t
	if benchRun {
		v.PartBench = benchSpecial(ctx, w, gr, cm, kind, t, views, v.Width, v.Height)
		v.Check = "BENCH"
		return v
	}
	width, height := v.Width, v.Height
	for i, view := range views {
		refW, _ := render.NewFrameBuffer(width, height)
		if err := w.DrawSoftware(refW, cm, view); err != nil {
			v.Detail = err.Error()
			return v
		}
		refS, _ := render.NewFrameBuffer(width, height)
		copy(refS.Pixels, refW.Pixels)
		if err := w.DrawSpecialSoftware(refS, cm, view, kind, t); err != nil {
			v.Detail = fmt.Sprintf("reference view %d: %v", i, err)
			return v
		}
		setSpecial(gr, kind, false)
		if err := gr.Draw(view); err != nil {
			v.Detail = fmt.Sprintf("draw world view %d: %v", i, err)
			return v
		}
		gotW, err := gr.Readback(ctx)
		if err != nil {
			v.Detail = fmt.Sprintf("readback view %d: %v", i, err)
			return v
		}
		setSpecial(gr, kind, true)
		if err := gr.Draw(view); err != nil {
			v.Detail = fmt.Sprintf("draw view %d: %v", i, err)
			return v
		}
		gotS, err := gr.Readback(ctx)
		if err != nil {
			v.Detail = fmt.Sprintf("readback view %d: %v", i, err)
			return v
		}
		mask := make([]bool, width*height)
		st := viewStat{View: i}
		for y := 0; y < height; y++ {
			for x := 0; x < width; x++ {
				r := refS.Pixels[y*refS.Pitch+x] != refW.Pixels[y*refW.Pitch+x]
				g := gotS[y*width+x] != gotW[y*width+x]
				mask[y*width+x] = r || g
				if g {
					st.Drawn++
				}
			}
		}
		st.Pixels, st.Differ, st.Interior = compare(refS.Pixels, refS.Pitch, gotS, width, height, mask)
		if st.Pixels > 0 {
			st.Rate = float64(st.Differ) / float64(st.Pixels)
		}
		v.Views = append(v.Views, st)
		v.Pixels += st.Pixels
		v.Differ += st.Differ
		v.Interior += st.Interior
		fmt.Fprintf(os.Stderr, "worldcheck %s: view %d: %d region pixels, %d differ, %d unexplained\n", part, i, st.Pixels, st.Differ, st.Interior)
	}
	if v.Pixels == 0 {
		v.Detail = "no " + part + " pixels in any view"
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

func enableSpecial(gr *gpuworld.Renderer, kind worldview.SpecialKind) error {
	if kind == worldview.KindTurb {
		return gr.EnableTurb()
	}
	return gr.EnableSky()
}

func setSpecial(gr *gpuworld.Renderer, kind worldview.SpecialKind, on bool) {
	if kind == worldview.KindTurb {
		gr.DrawTurb = on
		return
	}
	gr.DrawSky = on
}

// benchSpecial alternates, per frame, the engine's software path for the same
// content (DrawEngine world, then the engine's fill of the part), the part
// alone in software, the GPU world alone, the GPU part alone (lit faces
// skipped; the PVS walk still runs) and the GPU world with the part.
func benchSpecial(ctx context.Context, w *worldview.World, gr *gpuworld.Renderer, cm *render.ColorMap, kind worldview.SpecialKind, t float32, views []worldview.View, width, height int) *partBench {
	fb, _ := render.NewFrameBuffer(width, height)
	const passes = 8
	var en, al, gw, gp, ga []float64
	ms := func(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }
	for p := 0; p < passes+1; p++ {
		for _, view := range views {
			t0 := time.Now()
			_ = w.DrawEngine(fb, view)
			t1 := time.Now()
			_ = w.DrawSpecialSoftware(fb, cm, view, kind, t)
			t2 := time.Now()
			setSpecial(gr, kind, false)
			_ = gr.Draw(view)
			_ = gr.Wait(ctx)
			t3 := time.Now()
			setSpecial(gr, kind, true)
			gr.SkipWorld = true
			_ = gr.Draw(view)
			_ = gr.Wait(ctx)
			gr.SkipWorld = false
			t4 := time.Now()
			_ = gr.Draw(view)
			_ = gr.Wait(ctx)
			t5 := time.Now()
			if p > 0 {
				en, al = append(en, ms(t2.Sub(t0))), append(al, ms(t2.Sub(t1)))
				gw, gp, ga = append(gw, ms(t3.Sub(t2))), append(gp, ms(t4.Sub(t3))), append(ga, ms(t5.Sub(t4)))
			}
		}
	}
	return &partBench{Frames: len(en), EngineFrameMS: pct(en, .5), EngineFrameP95MS: pct(en, .95), EnginePartMS: pct(al, .5), EnginePartP95MS: pct(al, .95),
		GPUWorldWallMS: pct(gw, .5), GPUWorldWallP95MS: pct(gw, .95), GPUPartWallMS: pct(gp, .5), GPUPartWallP95MS: pct(gp, .95),
		GPUFrameWallMS: pct(ga, .5), GPUFrameWallP95MS: pct(ga, .95)}
}
