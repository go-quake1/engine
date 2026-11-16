// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package worldview

import (
	"testing"

	"github.com/go-quake1/engine/render"
)

// TestLightStyleValueTable pins LightStyleValue on values worked from the
// table of R_AnimateLight: the character at int(t*10) of the style's string,
// times 22 ('a' is 0, 'm' 264, 'z' 550); style 0 is 'm'; a style the table does
// not have is 256.
func TestLightStyleValueTable(t *testing.T) {
	for _, c := range []struct {
		style byte
		t     float32
		want  int
	}{
		{0, 0, 264},
		{0, 9.9, 264},
		{2, 0, 0},     // "abcdef...": 'a'
		{2, 0.5, 110}, // index 5: 'f' = 5
		{2, 2.5, 550}, // index 25: 'z' = 25
		{2, 2.6, 528}, // index 26: 'y' = 24
		{2, 5.1, 0},   // index 51 of a 51-character string: wraps to 'a'
		{1, 0.2, 286}, // "mmnm...": index 2 'n' = 13
		{1, 0.5, 308}, // index 5 'o' = 14
		{4, 0.1, 0},   // "mamama...": index 1 'a'
		{4, 0.0, 264}, // index 0 'm'
		{12, 0.3, 256},
		{200, 0, 256},
	} {
		if got := LightStyleValue(c.style, c.t); got != c.want {
			t.Errorf("LightStyleValue(%d, %v) = %d, want %d", c.style, c.t, got, c.want)
		}
	}
	// the clock before zero wraps into the string
	if got := LightStyleValue(2, -0.1); got != 0 { // index -1 + 51 = 50: 'a'
		t.Errorf("LightStyleValue(2, -0.1) = %d, want 0", got)
	}
}

func TestStyleAnimates(t *testing.T) {
	for s, want := range map[byte]bool{0: false, 1: true, 11: true, 12: false, 33: false, 255: false} {
		if got := StyleAnimates(s); got != want {
			t.Errorf("StyleAnimates(%d) = %v, want %v", s, got, want)
		}
	}
}

// TestSurfaceStateFollowsTheAnimation: a face's surface state is its styles'
// values and its texture's frame under w.Anim, and the resting world's without.
func TestSurfaceStateFollowsTheAnimation(t *testing.T) {
	base, frame := &render.SurfaceTexture{Width: 16, Height: 16}, &render.SurfaceTexture{Width: 16, Height: 16}
	w := &World{Tex: []*render.SurfaceTexture{base, frame}, TexNames: []string{"+0a", "+1a"}}
	f := &Face{MipTex: 0, BaseTex: base}
	f.Surf.Desc.Styles = [4]byte{10, 255, 255, 255}

	if adj, tex := w.SurfaceState(f); adj[0] != 264 || tex != base {
		t.Errorf("resting: adj %v tex %p, want 264 and the base texture", adj, tex)
	}
	w.Anim = w.AnimationAt(0.2) // style 10 "mmamamm..." index 2 is 'a' (0); the chain's frame int(0.2*5) = 1
	if adj, tex := w.SurfaceState(f); adj[0] != 0 || tex != frame {
		t.Errorf("t 0.2: adj %v, want 0 and the second frame (%v)", adj, tex == frame)
	}
	w.Anim = w.AnimationAt(0.4) // index 4 is 'a' too; frame int(0.4*5) = 2 % 2 = 0
	if adj, tex := w.SurfaceState(f); adj[0] != 0 || tex != base {
		t.Errorf("t 0.4: adj %v tex %v", adj, tex == base)
	}
	w.Tex[1] = nil // a frame that did not decode: the face keeps its own texture
	w.Anim = w.AnimationAt(0.2)
	if _, tex := w.SurfaceState(f); tex != base {
		t.Error("a missing frame must fall back to the base texture")
	}
}
