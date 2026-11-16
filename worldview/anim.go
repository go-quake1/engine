// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package worldview

import "github.com/go-quake1/engine/render"

// defaultLightStyles is tyrquake's R_AnimateLight() built-in table:
// 11 standard Quake lightstyles indexed by Face.Styles[i]. Each char
// 'a'..'z' encodes a brightness 0..25; the engine samples it at 10 Hz
// from cl.time and multiplies by 22 to yield a 0..550 light scale
// (256 = ~='m', the resting level used by static lights).
//
// Style 0 is hard-set "m" (full-bright steady, NOT in this table) by
// engine convention; this slice holds 1..11.
//
// Source: tyrquake's r_light.c R_AnimateLight + the lightstyle string
// table in NQ/cl_main.c CL_ClearState / S_INIT.
var defaultLightStyles = [...]string{
	"m",                       // 0 - normal (steady)
	"mmnmmommommnonmmonqnmmo", // 1 - FLICKER (torches)
	"abcdefghijklmnopqrstuvwxyzyxwvutsrqponmlkjihgfedcba", // 2 - SLOW PULSE
	"mmmmmaaaaammmmmaaaaaabcdefgabcdefg",                  // 3 - CANDLE 1
	"mamamamamama",                                        // 4 - FAST STROBE
	"jklmnopqrstuvwxyzyxwvutsrqponmlkj",                   // 5 - GENTLE PULSE
	"nmonqnmomnmomomno",                                   // 6 - FLICKER 2
	"mmmaaaabcdefgmmmmaaaammmaamm",                        // 7 - CANDLE 2
	"mmmaaammmaaammmabcdefaaaammmmabcdefmmmaaaa",          // 8 - CANDLE 3
	"aaaaaaaazzzzzzzz",                                    // 9 - SLOW STROBE
	"mmamammmmammamamaaamammma",                           // 10 - FLUORESCENT
	"abcdefghijklmnopqrrqponmlkjihgfedcba",                // 11 - SLOW PULSE 2
}

// LightStyleValue returns the per-style brightness (0..550) at
// the given time, sampling the style's Anim string at 10 Hz. style 0
// is the constant full-bright. Unknown / out-of-range styles fall
// back to the static-style brightness 256 so a face with a style not
// in the default table still renders. It is d_lightstylevalue[style] at
// cl.time (R_AnimateLight).
func LightStyleValue(style byte, t float32) int {
	if style == 0 {
		return 264 // 'm' = 12, * 22 = 264
	}
	if int(style) >= len(defaultLightStyles) {
		return 256
	}
	anim := defaultLightStyles[style]
	if anim == "" {
		return 256
	}
	idx := int(t*10) % len(anim)
	if idx < 0 {
		idx += len(anim)
	}
	ch := anim[idx]
	if ch < 'a' || ch > 'z' {
		return 256
	}
	return int(ch-'a') * 22
}

// StyleAnimates reports whether [LightStyleValue] moves with time for style s:
// the styles 1 to 11 of the default table. Style 0 is steady and the others
// (the map's switchable lights, from 32) are not known to the table.
func StyleAnimates(s byte) bool { return s >= 1 && int(s) < len(defaultLightStyles) }

// Animation is what moves in the world between two frames: the light styles
// (d_lightstylevalue) and the frame of each animated '+' texture
// (R_TextureAnimation). The zero value is the resting world: every style at
// [bsprender.NormalLightStyle] and every face on its own texture, which is
// what the fixed acceptance views draw.
type Animation struct {
	// Style is d_lightstylevalue[style]; nil is the resting value.
	Style func(style byte) int
	// Frame is the miptex slot the texture of slot shows now; nil is slot.
	Frame func(slot int) int
}

// AnimationAt is the animation of the default light styles and of the '+'
// chains of the map's miptexes at time t (seconds): the one the runner's
// software path draws with.
func (w *World) AnimationAt(t float32) Animation {
	if w.chains == nil {
		w.chains = BuildMiptexChains(w.TexNames)
	}
	chains := w.chains
	return Animation{
		Style: func(s byte) int { return LightStyleValue(s, t) },
		Frame: func(slot int) int { return chains.Pic(slot, t) },
	}
}

// SurfaceState is the state of face f's lit surface under w.Anim: the styles'
// values (D_CacheSurface's lightadj) and the texture its miptex shows now. Two
// frames with the same state have the same surface.
func (w *World) SurfaceState(f *Face) (adj [4]int, tex *render.SurfaceTexture) {
	adj = StyleAdj(f.Surf.Desc.Styles)
	if w.Anim.Style != nil {
		for i, s := range f.Surf.Desc.Styles {
			adj[i] = w.Anim.Style(s)
		}
	}
	tex = f.BaseTex
	if w.Anim.Frame != nil {
		if slot := w.Anim.Frame(f.MipTex); slot >= 0 && slot < len(w.Tex) && w.Tex[slot] != nil {
			tex = w.Tex[slot]
		}
	}
	return adj, tex
}
