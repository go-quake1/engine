// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package webgpupresent

import (
	"bytes"
	"strings"
	"testing"

	"github.com/go-quake1/engine/render"
)

func testPalette() *render.Palette {
	var p render.Palette
	for i := range p {
		p[i] = [3]byte{byte(i), byte(255 - i), byte(i * 7)}
	}
	return &p
}

func TestShaderNamesItsBindings(t *testing.T) {
	for _, s := range []string{"@vertex fn vs", "@fragment fn fs", "texture_2d<u32>", "@binding(1) var pal"} {
		if !strings.Contains(ShaderWGSL, s) {
			t.Errorf("shader lacks %q", s)
		}
	}
}

func TestPaletteRGBA(t *testing.T) {
	got := PaletteRGBA(testPalette())
	if len(got) != 1024 || !bytes.Equal(got[4*9:4*9+4], []byte{9, 246, 63, 255}) {
		t.Fatalf("palette texture %v", got[4*9:4*9+4])
	}
}

func TestReference(t *testing.T) {
	pal := testPalette()
	// 3x2 frame in a pitch-4 buffer: the padding byte must not show.
	px := []byte{1, 2, 3, 99, 4, 5, 6, 99}
	one, ok := Reference(px, 3, 2, 4, pal, 3, 2)
	if !ok || len(one) != 24 {
		t.Fatal("scale 1")
	}
	for i, idx := range []byte{1, 2, 3, 4, 5, 6} {
		c := pal[idx]
		if !bytes.Equal(one[4*i:4*i+4], []byte{c[0], c[1], c[2], 255}) {
			t.Fatalf("pixel %d", i)
		}
	}
	two, ok := Reference(px, 3, 2, 4, pal, 6, 4)
	if !ok {
		t.Fatal("scale 2")
	}
	for oy := 0; oy < 4; oy++ {
		for ox := 0; ox < 6; ox++ {
			if !bytes.Equal(two[4*(oy*6+ox):][:4], one[4*((oy/2)*3+ox/2):][:4]) {
				t.Fatalf("scale 2 pixel (%d,%d)", ox, oy)
			}
		}
	}
	for _, bad := range []struct {
		px      []byte
		w, h, p int
		pal     *render.Palette
		ow, oh  int
	}{
		{px, 0, 2, 4, pal, 3, 2}, {px, 3, 2, 4, pal, 0, 2}, {px, 3, 2, 2, pal, 3, 2},
		{px, 3, 2, 4, nil, 3, 2}, {px[:5], 3, 2, 4, pal, 3, 2},
	} {
		if _, ok := Reference(bad.px, bad.w, bad.h, bad.p, bad.pal, bad.ow, bad.oh); ok {
			t.Errorf("accepted %+v", bad)
		}
	}
}
