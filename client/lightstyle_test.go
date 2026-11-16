// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package client

import (
	"errors"
	"strings"
	"testing"

	"github.com/go-quake1/engine/protocol"
)

// decodeOne decodes the first message of raw.
func decodeOne(t *testing.T, raw []byte) (Decoded, error) {
	t.Helper()
	return newReader(raw).Next(protocol.VersionNQ)
}

// TestDecodeLightStyle: svc_lightstyle is [byte 12] [byte style] [string]
// (reference/NQ/cl_parse.c:1053-1057); a style of MAX_LIGHTSTYLES (64) or more is
// Sys_Error there, an error here.
func TestDecodeLightStyle(t *testing.T) {
	d, err := decodeOne(t, []byte{12, 5, 'a', 'b', 'z', 0})
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := d.(DecodedLightStyle); !ok || got.Style != 5 || got.Map != "abz" {
		t.Errorf("%#v", d)
	}
	if d, err = decodeOne(t, []byte{12, 63, 0}); err != nil || d.(DecodedLightStyle) != (DecodedLightStyle{Style: 63}) {
		t.Errorf("style 63, empty: %#v %v", d, err)
	}
	if _, err = decodeOne(t, []byte{12, 64, 'm', 0}); !errors.Is(err, ErrLightStyleRange) {
		t.Errorf("style 64: %v", err)
	}
	for _, raw := range [][]byte{{12}, {12, 3}, {12, 3, 'm'}} { // no style, no string, no terminator
		if _, err = decodeOne(t, raw); !errors.Is(err, ErrCorruptMessage) {
			t.Errorf("%v: %v", raw, err)
		}
	}
}

// TestApplyLightStyle: the map is the string, cut to MAX_STYLESTRING - 1 = 63
// letters (qsnprintf into char map[64]); a later message replaces it; the state
// remembers it has been told.
func TestApplyLightStyle(t *testing.T) {
	s := NewState()
	if s.HaveLightStyles {
		t.Fatal("a new state has light styles")
	}
	if err := Apply(s, DecodedLightStyle{Style: 2, Map: "abcd"}, 1); err != nil {
		t.Fatal(err)
	}
	if string(s.LightStyles[2].Anim) != "abcd" || !s.HaveLightStyles {
		t.Errorf("%q, have %v", s.LightStyles[2].Anim, s.HaveLightStyles)
	}
	_ = Apply(s, DecodedLightStyle{Style: 2, Map: "z"}, 2)
	if string(s.LightStyles[2].Anim) != "z" {
		t.Errorf("not replaced: %q", s.LightStyles[2].Anim)
	}
	long := strings.Repeat("m", 70)
	_ = Apply(s, DecodedLightStyle{Style: 7, Map: long}, 2)
	if len(s.LightStyles[7].Anim) != 63 {
		t.Errorf("a 70-letter map kept %d letters, want 63", len(s.LightStyles[7].Anim))
	}
	_ = Apply(s, DecodedLightStyle{Style: 64, Map: "m"}, 2) // out of range: nothing
	// a new map clears them (CL_ClearState memsets cl_lightstyle)
	s.Clear()
	if s.HaveLightStyles || s.LightStyles[2].Anim != nil {
		t.Error("Clear kept the light styles")
	}
}

// TestLightStyleValue pins R_AnimateLight (reference/common/r_light.c:50-60) on
// worked values: i = (int)(t * 10); an empty map is 256; otherwise
// (map[i % length] - 'a') * 22.
//
//	"abcd" at t = 0.35: i = 3, 'd' = 3 -> 66; at 0.4: i = 4 % 4 = 0, 'a' -> 0;
//	at 1.25: i = 12 % 4 = 0 -> 0; at 1.55: i = 15 % 4 = 3 -> 66.
//	"m": 12 * 22 = 264 at any time; "z": 25 * 22 = 550.
func TestLightStyleValue(t *testing.T) {
	s := NewState()
	s.LightStyles[1].Anim = []byte("abcd")
	s.LightStyles[0].Anim = []byte("m")
	s.LightStyles[2].Anim = []byte("z")
	for _, c := range []struct {
		style byte
		t     float32
		want  int
	}{
		{1, 0.35, 66}, {1, 0.4, 0}, {1, 1.25, 0}, {1, 1.55, 66}, {1, 0.15, 22},
		{0, 0, 264}, {0, 123.4, 264}, {2, 5, 550},
		{3, 1, 256},    // not set by the server: 256
		{63, 1, 256},   // the last style, not set
		{64, 1, 264},   // not an animated style: d_lightstylevalue's initial 264
		{255, 1, 264},  // an unused face style slot
		{1, -0.15, 66}, // before time zero: int(-1.5) = -1, -1 % 4 = -1 in C (reads before the map); wrapped to 3, 'd'
	} {
		if got := s.LightStyleValue(c.style, c.t); got != c.want {
			t.Errorf("style %d at %v: %d, want %d", c.style, c.t, got, c.want)
		}
	}
}
