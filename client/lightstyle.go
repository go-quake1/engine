// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package client

// MaxStyleString is MAX_STYLESTRING (reference/NQ/quakedef.h:86): the size of
// a light style's string with its terminator, so the longest map has 63 letters.
const MaxStyleString = 64

// applyLightStyle is the svc_lightstyle arm of CL_ParseServerMessage
// (reference/NQ/cl_parse.c:1053-1061): the style's map is the string, cut to
// MAX_STYLESTRING - 1 letters as qsnprintf does. The state remembers that the
// server has sent styles (HaveLightStyles).
func applyLightStyle(state *State, m DecodedLightStyle) {
	if m.Style < 0 || m.Style >= MaxLightStyles {
		return
	}
	text := m.Map
	if len(text) > MaxStyleString-1 {
		text = text[:MaxStyleString-1]
	}
	state.LightStyles[m.Style].Anim = []byte(text)
	state.HaveLightStyles = true
}

// LightStyleValue is d_lightstylevalue[style] after R_AnimateLight
// (reference/common/r_light.c:42-61) at time t (cl.time): 8.8 fixed point, 256 is
// the light as baked and 264 ('m') what a map's style 0 gives. A style the server
// has not set (no letters) is 256; with i = (int)(t * 10), the letter is
// map[i % length], its level (letter - 'a') * 22. A style not below
// MaxLightStyles has the initial 264 of d_lightstylevalue (gl_rmisc.c:509).
func (s *State) LightStyleValue(style byte, t float32) int {
	if int(style) >= MaxLightStyles {
		return 264
	}
	m := s.LightStyles[style].Anim
	if len(m) == 0 {
		return 256
	}
	k := int(t*10) % len(m)
	if k < 0 { // a time before zero: C would index before the map
		k += len(m)
	}
	return (int(m[k]) - 'a') * 22
}
