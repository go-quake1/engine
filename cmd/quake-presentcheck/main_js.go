// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

//go:build js && wasm

// quake-presentcheck is the browser test of the WebGPU presenter. Under
// js/wasm in a page (see present_test.go) it presents fixed frames through
// the GPU, reads the canvas back and compares every pixel with the CPU's
// conversion (webgpupresent.Reference). It prints one JSON line:
//
//	{"check":"PASS", ...}  every pixel of every frame matched
//	{"check":"FAIL", ...}  the first mismatch is named
//
// --corrupt feeds the GPU a frame with one palette index changed while the
// reference keeps the true one: the check must then FAIL. That is the
// planted negative proving the comparison can fail.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/go-quake1/engine/render"
	"github.com/go-quake1/engine/render/webgpupresent"
)

type verdict struct {
	Check    string `json:"check"`
	Detail   string `json:"detail,omitempty"`
	Adapter  string `json:"adapter,omitempty"`
	Fallback bool   `json:"fallback"`
	Frames   int    `json:"frames"`
	Pixels   int    `json:"pixels"`
}

func main() {
	var corrupt, requireGPU bool
	for _, a := range os.Args[1:] {
		switch a {
		case "--corrupt":
			corrupt = true
		case "--require-gpu":
			requireGPU = true
		}
	}
	v := check(corrupt, requireGPU)
	b, _ := json.Marshal(v)
	fmt.Println(string(b))
}

// testFrame is deterministic: every palette index occurs.
func testFrame(w, h, pitch, n int) []byte {
	px := make([]byte, pitch*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			px[y*pitch+x] = byte(x*7 + y*13 + n*5)
		}
		for x := w; x < pitch; x++ {
			px[y*pitch+x] = 0xEE // padding: must never show
		}
	}
	return px
}

func testPalette(n int) *render.Palette {
	var p render.Palette
	for i := range p {
		p[i] = [3]byte{byte(i + n), byte(255 - i), byte(i*7 + n*3)}
	}
	return &p
}

func check(corrupt, requireGPU bool) verdict {
	v := verdict{Check: "FAIL"}
	const w, h = 64, 40
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	// scale 1 (identity) and scale 3 (an integer upscale), a padded pitch.
	for _, scale := range []int{1, 3} {
		pitch := w + 8*(scale-1)/2
		p, err := webgpupresent.New(ctx, webgpupresent.Config{
			Width: w, Height: h, OutWidth: w * scale, OutHeight: h * scale,
			AllowFallback: !requireGPU,
		})
		if err != nil {
			v.Detail = fmt.Sprintf("no presenter: %v", err)
			return v
		}
		info := p.Adapter()
		fmt.Fprintf(os.Stderr, "presentcheck: scale %d: device up on %q fallback %v\n", scale, info.Description, info.Fallback)
		v.Adapter, v.Fallback = fmt.Sprintf("%s %s %s", info.Vendor, info.Architecture, info.Description), info.Fallback
		// Three frames; the palette changes between the second and the
		// third (the presenter must re-upload it) and not before.
		for n := 0; n < 3; n++ {
			pal := testPalette(n / 2)
			frame := testFrame(w, h, pitch, n)
			want, ok := webgpupresent.Reference(frame, w, h, pitch, pal, w*scale, h*scale)
			if !ok {
				v.Detail = "reference refused the frame"
				return v
			}
			sent := frame
			if corrupt {
				sent = append([]byte(nil), frame...)
				sent[5*pitch+7]++ // one wrong palette index
			}
			if err := p.Present(sent, w, h, pitch, pal); err != nil {
				v.Detail = fmt.Sprintf("present: %v", err)
				return v
			}
			fmt.Fprintf(os.Stderr, "presentcheck: scale %d frame %d presented\n", scale, n)
			got, gw, gh, err := p.Readback()
			if err != nil {
				v.Detail = fmt.Sprintf("readback: %v", err)
				return v
			}
			if gw != w*scale || gh != h*scale {
				v.Detail = fmt.Sprintf("readback is %dx%d, want %dx%d", gw, gh, w*scale, h*scale)
				return v
			}
			for i := 0; i < len(want); i += 4 {
				if got[i] != want[i] || got[i+1] != want[i+1] || got[i+2] != want[i+2] || got[i+3] != want[i+3] {
					px := i / 4
					v.Detail = fmt.Sprintf("scale %d frame %d: pixel (%d,%d) is %v, want %v",
						scale, n, px%(w*scale), px/(w*scale), got[i:i+4], want[i:i+4])
					return v
				}
			}
			v.Frames++
			v.Pixels += len(want) / 4
		}
		p.Close()
	}
	v.Check = "PASS"
	return v
}
