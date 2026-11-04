// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package render

import (
	"errors"
	"math"
	"testing"
)

// Every expected value below was worked out by hand from reference/common
// (r_surf.c, d_surf.c, d_edge.c, d_init.c, d_modech.c, r_main.c, model.c),
// not produced by the code under test.

func TestMipLevelThresholds(t *testing.T) {
	m := DefaultMipScales
	if m != (MipScales{1, 0.4, 0.2}) {
		t.Fatalf("d_scalemip = %v, want {1, 0.4, 0.2} (basemip {1, 0.5*0.8, 0.25*0.8} times d_mipscale 1)", m)
	}
	for _, c := range []struct {
		scale  float32
		minMip int
		want   int
	}{
		{5, 0, 0},
		{1, 0, 0},
		{math.Nextafter32(1, 0), 0, 1},
		{0.4, 0, 1},
		{math.Nextafter32(0.4, 0), 0, 2},
		{0.2, 0, 2},
		{math.Nextafter32(0.2, 0), 0, 3},
		{0, 0, 3},
		{5, 2, 2}, // d_mipcap 2 raises the floor
		{0.3, 3, 3},
		{0.3, 1, 2},
	} {
		if got := m.Level(c.scale, c.minMip); got != c.want {
			t.Errorf("Level(%v, min %d) = %d, want %d", c.scale, c.minMip, got, c.want)
		}
	}
	if got := NewMipScales(2); got != (MipScales{2, 0.8, 0.4}) {
		t.Errorf("d_mipscale 2: %v", got)
	}
}

func TestScaleForMip(t *testing.T) {
	// horizontalFieldOfView = 2 tan(90 / 360 pi) = 2, xscale = width / 2.
	for _, c := range []struct {
		w    int
		fov  float32
		want float32
	}{{320, 90, 160}, {1280, 90, 640}} {
		if got := ScaleForMip(c.w, c.fov); got != c.want {
			t.Errorf("ScaleForMip(%d, %v) = %v, want %v", c.w, c.fov, got, c.want)
		}
	}
	// 60 degrees: 2 tan(pi/6) = 1.1547005, 320 / 1.1547005 = 277.12815.
	if got := ScaleForMip(320, 60); math.Abs(float64(got)-277.12813) > 1e-3 {
		t.Errorf("60 degrees: %v", got)
	}
}

func TestMipAdjust(t *testing.T) {
	for _, c := range []struct {
		s, t [3]float32
		want float32
	}{
		{[3]float32{1, 0, 0}, [3]float32{0, 1, 0}, 1},       // len 1 >= 0.99
		{[3]float32{0.5, 0, 0}, [3]float32{0, 0.5, 0}, 2},   // 0.49 <= 0.5 < 0.99
		{[3]float32{0.4, 0, 0}, [3]float32{0, 0.4, 0}, 3},   // 0.32 <= 0.4 < 0.49
		{[3]float32{0.25, 0, 0}, [3]float32{0, 0.25, 0}, 4}, // < 0.32
		{[3]float32{0, 0.6, 0.8}, [3]float32{0.5, 0, 0}, 2}, // (1 + 0.5) / 2 = 0.75
		{[3]float32{2, 0, 0}, [3]float32{0, 0, 0}, 1},       // (2 + 0) / 2 = 1
	} {
		if got := MipAdjust(c.s, c.t); got != c.want {
			t.Errorf("MipAdjust(%v, %v) = %v, want %v", c.s, c.t, got, c.want)
		}
	}
}

// testMap is a colormap whose entry is row xor texel, so that a wrong row or a
// wrong texel changes the value.
func testMap() *ColorMap {
	var cm ColorMap
	for r := range cm {
		for c := range cm[r] {
			cm[r][c] = byte(r) ^ byte(c)
		}
	}
	return &cm
}

func TestBuildLightMapValues(t *testing.T) {
	env := &SurfaceEnv{Colormap: testMap(), HaveLightData: true}
	d := &SurfaceDesc{Extents: [2]int{0, 0}, Styles: [4]byte{0, 255, 255, 255}, Samples: []byte{100}}
	bl := make([]uint32, 4)
	for _, c := range []struct {
		name    string
		samples []byte
		styles  [4]byte
		adj     [4]int
		ambient int
		want    uint32
	}{
		// (255 * 256 - 100 * 256) >> 2 = 39680 >> 2
		{"one style", []byte{100}, [4]byte{0, 255, 255, 255}, [4]int{256}, 0, 9920},
		// ambient 20 << 8 = 5120, plus 25600: (65280 - 30720) >> 2
		{"ambient", []byte{100}, [4]byte{0, 255, 255, 255}, [4]int{256}, 20, 8640},
		// 100 * 264 + 50 * 128 = 32800: (65280 - 32800) >> 2
		{"two styles", []byte{100, 50}, [4]byte{0, 1, 255, 255}, [4]int{264, 128}, 0, 8120},
		// 255 * 264 = 67320 > 65280: negative, held at 1 << 6
		{"overbright", []byte{255}, [4]byte{0, 255, 255, 255}, [4]int{264}, 0, 64},
		// a style after a 255 is never read
		{"stop at 255", []byte{100, 200}, [4]byte{0, 255, 1, 255}, [4]int{256, 0, 256}, 0, 9920},
		// no samples: the ambient level alone, (65280 - 0) >> 2
		{"no samples", nil, [4]byte{255, 255, 255, 255}, [4]int{}, 0, 16320},
	} {
		d.Samples, d.Styles = c.samples, c.styles
		env.Ambient = c.ambient
		if err := BuildLightMap(bl, d, c.adj, env); err != nil {
			t.Fatal(c.name, err)
		}
		if bl[0] != c.want {
			t.Errorf("%s: %d, want %d", c.name, bl[0], c.want)
		}
	}
	// r_fullbright, or a world without a light lump: zero, not inverted.
	d.Samples, d.Styles = []byte{100}, [4]byte{0, 255, 255, 255}
	env.Fullbright = true
	_ = BuildLightMap(bl, d, [4]int{256}, env)
	if bl[0] != 0 {
		t.Errorf("fullbright: %d", bl[0])
	}
	env.Fullbright, env.HaveLightData = false, false
	_ = BuildLightMap(bl, d, [4]int{256}, env)
	if bl[0] != 0 {
		t.Errorf("no lightdata: %d", bl[0])
	}
}

func TestBuildLightMapRefusesBigExtents(t *testing.T) {
	env := &SurfaceEnv{Colormap: testMap(), HaveLightData: true}
	d := &SurfaceDesc{Extents: [2]int{16 * 18, 16}, Styles: [4]byte{255, 255, 255, 255}}
	if err := BuildLightMap(make([]uint32, 18*18), d, [4]int{}, env); !errors.Is(err, ErrSurfaceExtents) {
		t.Fatalf("19 samples wide: %v", err)
	}
}

func testTexture(w, h int, base byte) *SurfaceTexture {
	mt := &SurfaceTexture{Width: w, Height: h}
	for k := 0; k < SurfMips; k++ {
		mw, mh := w>>k, h>>k
		px := make([]byte, mw*mh)
		for y := 0; y < mh; y++ {
			for x := 0; x < mw; x++ {
				px[y*mw+x] = base + byte(x+mw*y)
			}
		}
		mt.Mips[k] = px
	}
	return mt
}

// The light grid is {0, 64 / 0, 64} samples: colormap rows 16320 and 12224 in
// 8.8 (row 63.75 and 47.75), constant down the surface (leftstep = rightstep = 0)
// and falling by (16320 - 12224) >> 4 = 256 (one row) per texel at mip 0.
func TestDrawSurfaceMip0Gradient(t *testing.T) {
	mt := testTexture(16, 16, 0)
	d := &SurfaceDesc{Extents: [2]int{16, 16}, Samples: []byte{0, 64, 0, 64}, Styles: [4]byte{0, 255, 255, 255}, Texture: mt}
	env := &SurfaceEnv{Colormap: testMap(), HaveLightData: true}
	dst := make([]byte, 16*16)
	if err := DrawSurface(dst, d, [4]int{256}, 0, env); err != nil {
		t.Fatal(err)
	}
	// b = 15 takes lightright = 12224 (row 47); each texel to the left adds a
	// row: row(b) = 47 + 15 - b. Texel (x, y) is x + 16 y.
	for _, c := range []struct{ x, y, row, texel int }{
		{0, 0, 62, 0},
		{15, 0, 47, 15},
		{1, 3, 61, 49},
	} {
		want := byte(c.row) ^ byte(c.texel)
		if got := dst[c.y*16+c.x]; got != want {
			t.Errorf("(%d,%d) = %d, want %d (row %d xor texel %d)", c.x, c.y, got, want, c.row, c.texel)
		}
	}
	if dst[0] != 62 || dst[15] != 32 || dst[3*16+1] != 12 {
		t.Errorf("hand values 62, 32, 12: got %d, %d, %d", dst[0], dst[15], dst[3*16+1])
	}
}

// At mip 1 the block is 8 texels and the step (16320 - 12224) >> 3 = 512: two
// rows per texel, from row 47 at b = 7.
func TestDrawSurfaceMip1Gradient(t *testing.T) {
	mt := testTexture(16, 16, 0)
	for i := range mt.Mips[1] {
		mt.Mips[1][i] = 100 + byte(i) // texel (x, y) = 100 + x + 8 y
	}
	d := &SurfaceDesc{Extents: [2]int{16, 16}, Samples: []byte{0, 64, 0, 64}, Styles: [4]byte{0, 255, 255, 255}, Texture: mt}
	env := &SurfaceEnv{Colormap: testMap(), HaveLightData: true}
	dst := make([]byte, 8*8)
	if err := DrawSurface(dst, d, [4]int{256}, 1, env); err != nil {
		t.Fatal(err)
	}
	// (0,0): row 61, texel 100 -> 89; (7,0): row 47, texel 107 -> 68;
	// (3,2): row 47 + 2 * (7 - 3) = 55, texel 100 + 3 + 16 = 119 -> 64.
	if dst[0] != 89 || dst[7] != 68 || dst[2*8+3] != 64 {
		t.Errorf("got %d, %d, %d, want 89, 68, 64", dst[0], dst[7], dst[2*8+3])
	}
}

// texturemins 16 on a 32 wide texture: the first block starts at column
// (16 + 32 << 16) % 32 = 16, the second wraps to 0.
func TestDrawSurfaceTextureOrigin(t *testing.T) {
	mt := testTexture(32, 16, 0)
	d := &SurfaceDesc{Extents: [2]int{32, 16}, TextureMins: [2]int{16, 0}, Texture: mt,
		Samples: []byte{100, 100, 100, 100, 100, 100}, Styles: [4]byte{0, 255, 255, 255}}
	env := &SurfaceEnv{Colormap: testMap(), HaveLightData: true}
	dst := make([]byte, 32*16)
	if err := DrawSurface(dst, d, [4]int{256}, 0, env); err != nil {
		t.Fatal(err)
	}
	// Constant light 9920 is row 38. Texel (x, y) = x + 32 y.
	// (0,0) reads column 16: 38 xor 16 = 54; (16,0) wraps to column 0: 38;
	// (31,0) is column 15: 38 xor 15 = 41.
	if dst[0] != 54 || dst[16] != 38 || dst[31] != 41 {
		t.Errorf("got %d, %d, %d, want 54, 38, 41", dst[0], dst[16], dst[31])
	}
}

func TestDrawSurfaceErrors(t *testing.T) {
	env := &SurfaceEnv{Colormap: testMap(), HaveLightData: true}
	d := &SurfaceDesc{Extents: [2]int{16, 16}, Texture: testTexture(16, 16, 0), Styles: [4]byte{255, 255, 255, 255}}
	if err := DrawSurface(make([]byte, 256), d, [4]int{}, 4, env); !errors.Is(err, ErrSurfaceMip) {
		t.Errorf("mip 4: %v", err)
	}
	if err := DrawSurface(make([]byte, 255), d, [4]int{}, 0, env); !errors.Is(err, ErrSurfaceDst) {
		t.Errorf("short dst: %v", err)
	}
	d.Texture = testTexture(8, 8, 0) // smaller than a mip-0 block
	if err := DrawSurface(make([]byte, 256), d, [4]int{}, 0, env); !errors.Is(err, ErrSurfaceTexture) {
		t.Errorf("small texture: %v", err)
	}
}

func TestSurfaceCacheForRes(t *testing.T) {
	for _, c := range []struct{ w, h, want int }{
		{320, 200, 896000},
		{640, 480, 896000 + (640*480-320*200)*6},
		{800, 600, 896000 + (800*600-320*200)*6},
		{1280, 800, 5024000},
	} {
		if got := SurfaceCacheForRes(c.w, c.h); got != c.want {
			t.Errorf("%dx%d: %d, want %d", c.w, c.h, got, c.want)
		}
	}
}

func blockLayout(c *SurfaceCache) [][2]int {
	var out [][2]int
	for _, b := range c.Blocks() {
		out = append(out, [2]int{b.Off, b.Size})
	}
	return out
}

func sameLayout(got, want [][2]int) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// A 4100 byte arena is sc_size 4096 (D_InitCaches takes the guard off). A
// block is 64 bytes of header and the data, rounded to 4.
func TestSurfaceCacheLayout(t *testing.T) {
	c := NewSurfaceCache(4100)
	c.SetupFrame()
	if c.Size() != 4096 || !sameLayout(blockLayout(c), [][2]int{{0, 4096}}) {
		t.Fatalf("fresh cache: %d %v", c.Size(), blockLayout(c))
	}
	a, err := c.Alloc(16, 256)
	if err != nil {
		t.Fatal(err)
	}
	if a.Off != 0 || a.Size != 320 || a.Width != 16 || a.Height != (320-72+4)/16 {
		t.Errorf("first block: %+v", a)
	}
	b, _ := c.Alloc(8, 64)
	if b.Off != 320 || b.Size != 128 {
		t.Errorf("second block: off %d size %d", b.Off, b.Size)
	}
	if want := [][2]int{{0, 320}, {320, 128}, {448, 3648}}; !sameLayout(blockLayout(c), want) || c.Rover().Off != 448 {
		t.Errorf("layout %v rover %d, want %v rover 448", blockLayout(c), c.Rover().Off, want)
	}
	// Only 396 bytes are left after the rover and 3700 are asked: the rover
	// wraps to the start and frees both blocks and the tail.
	var ownerA, ownerB *SurfaceBlock
	a.owner, b.owner = &ownerA, &ownerB
	ownerA, ownerB = a, b
	big, err := c.Alloc(16, 3700-64)
	if err != nil {
		t.Fatal(err)
	}
	if big.Off != 0 || big.Size != 3700 || ownerA != nil || ownerB != nil {
		t.Errorf("big block %+v, owners %v %v: the wrapped alloc must free what it covers", big, ownerA, ownerB)
	}
	if want := [][2]int{{0, 3700}, {3700, 396}}; !sameLayout(blockLayout(c), want) || c.Rover().Off != 3700 {
		t.Errorf("layout %v", blockLayout(c))
	}
	if c.Thrash {
		t.Error("a first wrap is not thrashing")
	}
	// 256 bytes fit in the 396 tail, whose leftover (140) is under 256 and
	// stays in the block: no fragment, the rover falls off the end.
	tail, _ := c.Alloc(16, 192)
	if tail.Off != 3700 || tail.Size != 396 || c.Rover() != nil {
		t.Errorf("tail block %+v rover %v", tail, c.Rover())
	}
	if c.Thrash {
		t.Error("thrash set before the second wrap")
	}
	// The rover is nil: wrap again, and now the cache has wrapped twice in the
	// frame (sc_rover >= d_initial_rover).
	if _, err := c.Alloc(4, 100); err != nil {
		t.Fatal(err)
	}
	if !c.Thrash {
		t.Error("the second wrap in one frame must set r_cache_thrash")
	}
}

func TestSurfaceCacheAllocErrors(t *testing.T) {
	c := NewSurfaceCache(1000)
	for _, c2 := range []struct {
		w, size int
		want    error
	}{{-1, 10, ErrCacheWidth}, {257, 10, ErrCacheWidth}, {4, 0, ErrCacheSize}, {4, 0x10001, ErrCacheSize}, {4, 2000, ErrCacheTooBig}} {
		if _, err := c.Alloc(c2.w, c2.size); !errors.Is(err, c2.want) {
			t.Errorf("Alloc(%d, %d) = %v, want %v", c2.w, c2.size, err, c2.want)
		}
	}
}

func TestSurfaceCacheFlush(t *testing.T) {
	c := NewSurfaceCache(4100)
	a, _ := c.Alloc(16, 256)
	var owner *SurfaceBlock = a
	a.owner = &owner
	c.Flush()
	if owner != nil || !sameLayout(blockLayout(c), [][2]int{{0, 4096}}) || c.Rover() != c.Blocks()[0] {
		t.Errorf("after flush: %v %v", owner, blockLayout(c))
	}
}

func TestCacheSurfaceReuseAndRebuild(t *testing.T) {
	c := NewSurfaceCache(100000)
	env := &SurfaceEnv{Colormap: testMap(), HaveLightData: true}
	mt := testTexture(16, 16, 0)
	f := &SurfaceFace{Desc: SurfaceDesc{Extents: [2]int{16, 16}, Texture: mt,
		Samples: []byte{0, 64, 0, 64}, Styles: [4]byte{0, 255, 255, 255}}}
	adj := [4]int{256}
	b1, err := c.Cache(f, 0, adj, env)
	if err != nil {
		t.Fatal(err)
	}
	if b1.Width != 16 || len(b1.Data) != 256 || b1.MipScale != 1 || f.Spot(0) != b1 || b1.Data[0] != 62 {
		t.Fatalf("first build: %+v", b1)
	}
	b1.Data[0] = 7 // a hit must return the block as it is
	if b2, _ := c.Cache(f, 0, adj, env); b2 != b1 || b2.Data[0] != 7 {
		t.Error("same texture and light: expected the cached block untouched")
	}
	// A strobing style (lightadj changed): rebuilt in the same block.
	if b3, _ := c.Cache(f, 0, [4]int{256 + 22}, env); b3 != b1 || b3.Data[0] == 7 {
		t.Error("a changed lightadj must rebuild in place")
	}
	// An animated texture: rebuilt in place from the new texture.
	f.Desc.Texture = testTexture(16, 16, 1)
	b1.Data[0] = 7
	if b4, _ := c.Cache(f, 0, [4]int{256 + 22}, env); b4 != b1 || b4.Texture != f.Desc.Texture || b4.Data[0] == 7 {
		t.Error("a new texture must rebuild in place")
	}
	// Mip 2 is its own block of a quarter of the size, MipScale 0.25.
	m2, err := c.Cache(f, 2, [4]int{256 + 22}, env)
	if err != nil {
		t.Fatal(err)
	}
	if m2 == b1 || m2.Width != 4 || len(m2.Data) != 16 || m2.MipScale != 0.25 {
		t.Errorf("mip 2: %+v", m2)
	}
	// Evicting a block clears the owner's spot.
	c.Flush()
	if f.Spot(0) != nil || f.Spot(2) != nil {
		t.Error("a flush must empty the surface's cachespots")
	}
}
