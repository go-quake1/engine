// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

//go:build js && wasm

// quake-worldcheck is the browser test of the GPU world. In a page (see
// world_test.go) it fetches the shareware pak0.pak from the harness, draws
// the fixed e1m1 views with the software reference (package worldview) and
// with the GPU (package gpuworld), and compares the two 8-bit frames
// pixel by pixel. It prints one JSON line with the mismatch figures and a
// verdict against --tolerance (the largest accepted share of pixels that differ
// and are not a one-pixel shift of the reference).
//
// --corrupt feeds the GPU a damaged colormap while the reference keeps the
// true one: the check must then FAIL (the planted negative).
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"syscall/js"
	"time"

	"github.com/go-gfx/webgpu"

	"github.com/go-quake1/engine/assets"
	"github.com/go-quake1/engine/pak"
	"github.com/go-quake1/engine/render"
	"github.com/go-quake1/engine/render/gpuworld"
	"github.com/go-quake1/engine/vfs"
	"github.com/go-quake1/engine/worldview"
)

type viewStat struct {
	View     int     `json:"view"`
	Drawn    int     `json:"gpu_non_clear"`
	Differ   int     `json:"differ"`
	Interior int     `json:"unexplained"`
	Pixels   int     `json:"pixels"`
	Rate     float64 `json:"rate"`
}

type verdict struct {
	Check        string     `json:"check"`
	Detail       string     `json:"detail,omitempty"`
	Adapter      string     `json:"adapter,omitempty"`
	Fallback     bool       `json:"fallback"`
	Width        int        `json:"width"`
	Height       int        `json:"height"`
	Faces        int        `json:"faces_on_gpu"`
	Views        []viewStat `json:"views,omitempty"`
	Pixels       int        `json:"pixels"`
	Differ       int        `json:"differ"`
	Interior     int        `json:"unexplained"`
	InteriorRate float64    `json:"interior_rate"`
	TotalRate    float64    `json:"total_rate"`
	Tolerance    float64    `json:"tolerance"`
	Bench        *benchRes  `json:"bench,omitempty"`
	PartBench    *partBench `json:"part_bench,omitempty"`
}

func fetchBytes(url string) ([]byte, error) {
	type res struct {
		b   []byte
		err error
	}
	ch := make(chan res, 1)
	var f1, f2, fail js.Func
	f1 = js.FuncOf(func(_ js.Value, a []js.Value) any {
		if !a[0].Get("ok").Bool() {
			ch <- res{err: fmt.Errorf("fetch %s: %d", url, a[0].Get("status").Int())}
			return nil
		}
		return a[0].Call("arrayBuffer")
	})
	f2 = js.FuncOf(func(_ js.Value, a []js.Value) any {
		u := js.Global().Get("Uint8Array").New(a[0])
		b := make([]byte, u.Get("length").Int())
		js.CopyBytesToGo(b, u)
		ch <- res{b: b}
		return nil
	})
	fail = js.FuncOf(func(_ js.Value, a []js.Value) any {
		ch <- res{err: fmt.Errorf("fetch %s: %v", url, a[0])}
		return nil
	})
	js.Global().Call("fetch", url).Call("then", f1).Call("then", f2).Call("catch", fail)
	r := <-ch
	f1.Release()
	f2.Release()
	fail.Release()
	return r.b, r.err
}

// opts are the options of the parts after the world: which part is checked
// ("" is the world alone) and the ablations of that part.
var opts = struct {
	part   string
	ablate map[string]bool
}{ablate: map[string]bool{}}

func main() {
	width, height, tol := 320, 200, 0.025
	corrupt, requireGPU, benchRun := false, false, false
	noLight, noTex := false, false
	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--corrupt":
			corrupt = true
		case "--require-gpu":
			requireGPU = true
		case "--nolight":
			noLight = true
		case "--flatTex":
			noTex = true
		case "--bench":
			benchRun = true
		case "--part":
			i++
			opts.part = args[i]
		case "--flatSkin", "--flatLight", "--noLerp", "--noWeapon", "--noScroll", "--exactSpans", "--standInLight", "--wrongLight", "--noFar", "--wrongWalk":
			opts.ablate[args[i]] = true
		case "--width":
			i++
			width, _ = strconv.Atoi(args[i])
		case "--height":
			i++
			height, _ = strconv.Atoi(args[i])
		case "--tolerance":
			i++
			tol, _ = strconv.ParseFloat(args[i], 64)
		}
	}
	v := check(width, height, tol, corrupt, requireGPU, benchRun, noLight, noTex)
	b, _ := json.Marshal(v)
	fmt.Println(string(b))
}

func check(width, height int, tol float64, corrupt, requireGPU, benchRun, noLight, noTex bool) verdict {
	v := verdict{Check: "FAIL", Width: width, Height: height, Tolerance: tol}
	data, err := fetchBytes("/pak0.pak")
	if err != nil {
		v.Detail = err.Error()
		return v
	}
	p, err := pak.Open(bytes.NewReader(data))
	if err != nil {
		v.Detail = fmt.Sprintf("pak: %v", err)
		return v
	}
	w, err := worldview.LoadMap(p, "e1m1")
	if err != nil {
		v.Detail = err.Error()
		return v
	}
	sv := vfs.New()
	sv.Add(p)
	cm, err := assets.LoadColorMapFrom(sv)
	if err != nil {
		v.Detail = err.Error()
		return v
	}
	// Attribution probes (not part of the acceptance check): every face's
	// lightmap full bright, or every texture one flat colour, on both sides.
	for _, f := range w.Faces {
		if f == nil {
			continue
		}
		if noLight {
			f.Light, f.Info.Width, f.Info.Height = []byte{128}, 1, 1
		}
		if noTex {
			f.Tex = &render.Pic{Width: 1, Height: 1, Pixels: []byte{0x70}}
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	g, err := webgpu.Navigator()
	if err != nil {
		v.Detail = err.Error()
		return v
	}
	adapter, err := g.RequestAdapter(ctx, &webgpu.RequestAdapterOptions{AllowFallback: !requireGPU})
	if err != nil {
		v.Detail = fmt.Sprintf("adapter: %v", err)
		return v
	}
	info := adapter.Info()
	v.Adapter, v.Fallback = fmt.Sprintf("%s %s %s", info.Vendor, info.Architecture, info.Description), info.Fallback
	if err := webgpu.InstallReplay(ctx, ""); err != nil {
		v.Detail = err.Error()
		return v
	}
	dev, err := adapter.RequestDevice(ctx, &webgpu.DeviceDescriptor{Label: "worldcheck", Quota: &webgpu.Quota{MemoryBytes: 512 << 20, Pipelines: 8, ShaderBytes: 32 << 10}})
	if err != nil {
		v.Detail = err.Error()
		return v
	}
	defer dev.Destroy()
	gcm := *cm
	if corrupt {
		// Every light level one eighth of the table too dark: the wrong
		// colormap row, as a wrong lightmap-to-row conversion would give.
		for r := range gcm {
			gcm[r] = cm[min(r+8, render.ColorMapRows-1)]
		}
	}
	gr, err := gpuworld.New(dev, w, &gcm, width, height)
	if err != nil {
		v.Detail = fmt.Sprintf("gpuworld: %v", err)
		return v
	}
	v.Faces = gr.Faces()
	switch opts.part {
	case "alias":
		return checkAlias(ctx, v, p, w, cm, gr, corrupt, benchRun)
	case "sky", "turb":
		return checkSpecial(ctx, v, w, cm, gr, opts.part, corrupt, benchRun)
	}
	if benchRun {
		v.Bench = bench(ctx, w, gr, cm, width, height)
		v.Check = "BENCH"
		return v
	}
	if opts.ablate["--exactSpans"] {
		render.PerspSubdivStep = 1 // the reference exact per pixel: what the 8-pixel steps cost
	}
	for i, view := range w.Views() {
		ref, _ := render.NewFrameBuffer(width, height)
		if err := w.DrawSoftware(ref, cm, view); err != nil {
			v.Detail = err.Error()
			return v
		}
		if err := gr.Draw(view); err != nil {
			v.Detail = fmt.Sprintf("draw view %d: %v", i, err)
			return v
		}
		got, err := gr.Readback(ctx)
		if err != nil {
			v.Detail = fmt.Sprintf("readback view %d: %v", i, err)
			return v
		}
		st := viewStat{View: i, Pixels: width * height}
		at := func(x, y int) byte { return ref.Pixels[y*ref.Pitch+x] }
		for y := 0; y < height; y++ {
			for x := 0; x < width; x++ {
				g := got[y*width+x]
				if g != worldview.ClearIndex {
					st.Drawn++
				}
				if g == at(x, y) {
					continue
				}
				st.Differ++
				// Explained: the GPU's index is the reference's at one of the
				// eight neighbours, i.e. a one-pixel shift of the same image
				// (rasterization rules, sampling position). Unexplained is
				// what a shift cannot account for: wrong texel, wrong light.
				explained := false
				for dy := -1; dy <= 1 && !explained; dy++ {
					for dx := -1; dx <= 1 && !explained; dx++ {
						nx, ny := x+dx, y+dy
						explained = nx >= 0 && ny >= 0 && nx < width && ny < height && at(nx, ny) == g
					}
				}
				if !explained {
					st.Interior++
				}
			}
		}
		st.Rate = float64(st.Differ) / float64(st.Pixels)
		v.Views = append(v.Views, st)
		v.Pixels += st.Pixels
		v.Differ += st.Differ
		v.Interior += st.Interior
		fmt.Fprintf(os.Stderr, "worldcheck: view %d: %d differ (%d interior) of %d\n", i, st.Differ, st.Interior, st.Pixels)
	}
	v.TotalRate = float64(v.Differ) / float64(v.Pixels)
	v.InteriorRate = float64(v.Interior) / float64(v.Pixels)
	if v.InteriorRate <= tol {
		v.Check = "PASS"
	} else {
		v.Detail = fmt.Sprintf("unexplained mismatch %.4f above the tolerance %.4f", v.InteriorRate, tol)
	}
	return v
}

type benchRes struct {
	Frames        int     `json:"frames"`
	SoftwareMS    float64 `json:"software_ms"`
	EngineMS      float64 `json:"engine_ms"`
	EngineP95MS   float64 `json:"engine_p95_ms"`
	GPUCPUMS      float64 `json:"gpu_cpu_ms"`
	GPUWallMS     float64 `json:"gpu_wall_ms"`
	SoftwareP95MS float64 `json:"software_p95_ms"`
	GPUCPUP95MS   float64 `json:"gpu_cpu_p95_ms"`
	GPUWallP95MS  float64 `json:"gpu_wall_p95_ms"`
}

func pct(v []float64, p float64) float64 {
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	return s[int(p*float64(len(s)-1))]
}

// bench alternates, per frame, the software reference and the GPU world on
// the fixed views: the software frame's wall time, the GPU frame's CPU time
// (the walk and the submit) and its wall time until the GPU is idle.
func bench(ctx context.Context, w *worldview.World, gr *gpuworld.Renderer, cm *render.ColorMap, width, height int) *benchRes {
	views := w.Views()
	fb, _ := render.NewFrameBuffer(width, height)
	const passes = 8
	var sw, gc, gw, en []float64
	ms := func(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }
	for p := 0; p < passes+1; p++ { // the first pass warms up
		for _, view := range views {
			e0 := time.Now()
			_ = w.DrawEngine(fb, view)
			e1 := time.Now()
			t0 := time.Now()
			_ = w.DrawSoftware(fb, cm, view)
			t1 := time.Now()
			_ = gr.Draw(view)
			t2 := time.Now()
			_ = gr.Wait(ctx)
			t3 := time.Now()
			if p > 0 {
				sw, gc, gw = append(sw, ms(t1.Sub(t0))), append(gc, ms(t2.Sub(t1))), append(gw, ms(t3.Sub(t1)))
				en = append(en, ms(e1.Sub(e0)))
			}
		}
	}
	return &benchRes{Frames: len(sw), SoftwareMS: pct(sw, .5), EngineMS: pct(en, .5), EngineP95MS: pct(en, .95), GPUCPUMS: pct(gc, .5), GPUWallMS: pct(gw, .5),
		SoftwareP95MS: pct(sw, .95), GPUCPUP95MS: pct(gc, .95), GPUWallP95MS: pct(gw, .95)}
}
