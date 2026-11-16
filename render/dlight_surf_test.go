// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package render

import (
	"reflect"
	"testing"
)

// Every expected value below was worked out by hand from
// reference/common/r_surf.c (R_AddDynamicLights, R_BuildLightMap) and d_surf.c
// (D_CacheSurface), not produced by the code under test.

// dlightSurf is a 32 x 16 texel surface (3 x 2 light samples) on the plane
// z = dist, texture axes s = x and t = y, texturemins 0.
func dlightSurf(planeDist float32, bits uint32) *SurfaceDesc {
	return &SurfaceDesc{
		Extents: [2]int{32, 16},
		Plane:   Plane{Normal: [3]float32{0, 0, 1}, Dist: planeDist},
		TexVecs: [2][4]float32{{1, 0, 0, 0}, {0, 1, 0, 0}},
		Styles:  [4]byte{255, 255, 255, 255},

		DLightBits: bits,
	}
}

func TestAddDynamicLightsWorked(t *testing.T) {
	for _, c := range []struct {
		name   string
		plane  float32
		light  DLight
		want   [6]uint32
		reason string
	}{
		{"on the surface's middle", 0, DLight{Origin: [3]float32{16, 8, 24}, Radius: 100},
			// rad = 100 - 24 = 76, minlight = 76; impact (16, 8); td = 8 on both rows.
			// s = 0: sd 16 > td: 16 + 4 = 20, (76-20)*256 = 14336; s = 1: sd 0: 8 + 0 = 8, 68*256 = 17408; s = 2: sd 16: 20.
			[6]uint32{14336, 17408, 14336, 14336, 17408, 14336}, ""},
		{"to the side", 0, DLight{Origin: [3]float32{60, 8, 24}, Radius: 100},
			// sd 60, 44, 28 over td 8: 60+4 = 64, 44+4 = 48, 28+4 = 32: 12, 28 and 44 times 256.
			[6]uint32{3072, 7168, 11264, 3072, 7168, 11264}, ""},
		{"minlight 30 cuts the far samples", 0, DLight{Origin: [3]float32{60, 8, 24}, Radius: 100, MinLight: 30},
			// minlight becomes rad - 30 = 46: 64 and 48 are not under it, 32 is: (76-32)*256.
			[6]uint32{0, 0, 11264, 0, 0, 11264}, ""},
		{"minlight above the radius left adds nothing", 0, DLight{Origin: [3]float32{16, 8, 24}, Radius: 30, MinLight: 10},
			// rad = 30 - 24 = 6 < 10.
			[6]uint32{}, ""},
		{"the larger axis distance is td", 0, DLight{Origin: [3]float32{24, 40, 24}, Radius: 100},
			// impact (24, 40): row 0 td 40: sd 24, 8, 8 give 40 + 12, 40 + 4, 40 + 4;
			// row 1 td 24 (sd 24 is not > td): 24 + 12, 24 + 4, 24 + 4; each (76 - d) * 256.
			[6]uint32{6144, 8192, 8192, 10240, 12288, 12288}, ""},
		{"td is truncated toward zero", 0, DLight{Origin: [3]float32{16, 8.9, 24}, Radius: 100},
			// row 1: int(8.9 - 16) = -7, so td 7: 16 + 3 = 19 (14592), 7 + 0 (17664), 19.
			[6]uint32{14336, 17408, 14336, 14592, 17664, 14592}, ""},
		{"the plane's distance counts", 10, DLight{Origin: [3]float32{16, 8, 34}, Radius: 100},
			[6]uint32{14336, 17408, 14336, 14336, 17408, 14336}, ""},
		{"a light behind the plane is lit by |dist|", 0, DLight{Origin: [3]float32{16, 8, -24}, Radius: 100},
			[6]uint32{14336, 17408, 14336, 14336, 17408, 14336}, ""},
	} {
		lights := make([]DLight, 2)
		lights[1] = c.light
		var got [6]uint32
		AddDynamicLights(got[:], dlightSurf(c.plane, 1<<1), lights)
		if got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

func TestAddDynamicLightsBitsAndSum(t *testing.T) {
	a := DLight{Origin: [3]float32{16, 8, 24}, Radius: 100}
	b := DLight{Origin: [3]float32{60, 8, 24}, Radius: 100}
	lights := []DLight{a, b}
	// no bit: nothing
	var got [6]uint32
	AddDynamicLights(got[:], dlightSurf(0, 0), lights)
	if got != ([6]uint32{}) {
		t.Errorf("no bits: %v", got)
	}
	// light 1 only
	AddDynamicLights(got[:], dlightSurf(0, 1<<1), lights)
	if got != ([6]uint32{3072, 7168, 11264, 3072, 7168, 11264}) {
		t.Errorf("light 1: %v", got)
	}
	// both: the sums, added onto what the grid already held
	got = [6]uint32{1, 1, 1, 1, 1, 1}
	AddDynamicLights(got[:], dlightSurf(0, 0b11), lights)
	want := [6]uint32{1 + 14336 + 3072, 1 + 17408 + 7168, 1 + 14336 + 11264, 1 + 14336 + 3072, 1 + 17408 + 7168, 1 + 14336 + 11264}
	if got != want {
		t.Errorf("both: %v, want %v", got, want)
	}
}

// TestBuildLightMapWithDynamicLights: R_BuildLightMap adds the lights only to a
// surface whose dlightframe is r_framecount, before the bound and the
// inversion t = (255*256 - light) >> 2.
func TestBuildLightMapWithDynamicLights(t *testing.T) {
	d := dlightSurf(0, 1)
	d.DLightFrame = 7
	lights := []DLight{{Origin: [3]float32{16, 8, 24}, Radius: 100}}
	var bl [18 * 18]uint32
	env := &SurfaceEnv{Colormap: testMap(), HaveLightData: true, DLights: lights, FrameCount: 7}
	if err := BuildLightMap(bl[:], d, [4]int{}, env); err != nil {
		t.Fatal(err)
	}
	// (65280 - 14336) >> 2 = 12736 and (65280 - 17408) >> 2 = 11968
	if got := bl[:6]; !reflect.DeepEqual(got, []uint32{12736, 11968, 12736, 12736, 11968, 12736}) {
		t.Errorf("lit: %v", got)
	}
	env.FrameCount = 8 // the surface was marked in another frame
	if err := BuildLightMap(bl[:], d, [4]int{}, env); err != nil {
		t.Fatal(err)
	}
	if got := bl[:6]; !reflect.DeepEqual(got, []uint32{16320, 16320, 16320, 16320, 16320, 16320}) {
		t.Errorf("not marked in this frame: %v, want 65280 >> 2 = 16320", got)
	}
	// a light strong enough to take the grid below the floor of 1 << 6:
	// 255 * 256 - sum is negative, so the value is bounded to 64.
	d.DLightFrame, env.FrameCount = 3, 3
	env.DLights = []DLight{{Origin: [3]float32{16, 8, 0}, Radius: 1000}}
	if err := BuildLightMap(bl[:], d, [4]int{}, env); err != nil {
		t.Fatal(err)
	}
	// on the plane: rad 1000, dist 8 at s = 1, t = 0: (1000-8)*256 = 253952 > 65280
	if bl[1] != 64 {
		t.Errorf("bounded: %d, want 64", bl[1])
	}
}

// TestCacheSurfaceDynamicLights: D_CacheSurface rebuilds a surface in every
// frame it is lit by a dynamic light, and once more after, to take the light
// off; a surface that is not lit and not changed is not rebuilt.
func TestCacheSurfaceDynamicLights(t *testing.T) {
	tex := &SurfaceTexture{Width: 32, Height: 32}
	for k := range tex.Mips {
		tex.Mips[k] = make([]byte, (32>>k)*(32>>k))
		for i := range tex.Mips[k] {
			tex.Mips[k][i] = byte(i)
		}
	}
	f := &SurfaceFace{Desc: *dlightSurf(0, 1)}
	f.Desc.Texture = tex
	f.Desc.Samples = make([]byte, 6) // one style
	f.Desc.Styles = [4]byte{0, 255, 255, 255}
	cache := NewSurfaceCache(SurfaceCacheForRes(320, 200))
	env := &SurfaceEnv{Colormap: testMap(), HaveLightData: true,
		DLights: []DLight{{Origin: [3]float32{16, 8, 24}, Radius: 100}}, FrameCount: 1}
	adj := [4]int{256}

	build := func(frame int32, marked bool) (*SurfaceBlock, []byte) {
		env.FrameCount = frame
		f.Desc.DLightFrame = 0
		if marked {
			f.Desc.DLightFrame = frame
		}
		cache.SetupFrame()
		b, err := cache.Cache(f, 0, adj, env)
		if err != nil {
			t.Fatal(err)
		}
		return b, append([]byte(nil), b.Data...)
	}
	_, clean := build(1, false)
	_, lit := build(2, true)
	if reflect.DeepEqual(clean, lit) {
		t.Fatal("a light 24 units over the surface did not change it")
	}
	b, again := build(3, true) // lit again: rebuilt (here to the same values)
	if !reflect.DeepEqual(lit, again) {
		t.Fatal("the lit surface differs between two frames of the same light")
	}
	b.Data[0] ^= 0xff // a rebuild overwrites this; a cache hit keeps it
	b, back := build(4, false)
	if !reflect.DeepEqual(clean, back) {
		t.Fatal("the surface kept the light after it passed: the cache did not rebuild it")
	}
	b.Data[0] ^= 0xff
	if b2, again := build(5, false); b2 != b || again[0] != clean[0]^0xff {
		t.Fatal("an unlit, unchanged surface was rebuilt")
	}
}
