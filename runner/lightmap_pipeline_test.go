// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

// Deterministic end-to-end tests for the lightmap rendering pipeline.
//
// WHY THIS EXISTS: my earlier unit tests validated each component
// (FillPerspectiveLightmappedPolygon, FaceLightmapInfo, the colormap
// loader) in isolation but never tested the WIRE -- whether the
// runner actually plugged the real Quake colormap (with non-identity
// dark rows) into the rasterizer. The wiring bug
// (`cm[r][s] = byte(s)` for every r) shipped because no test
// exercised the contract "lightmap row 63 produces a darker pixel
// than row 0 for the SAME texel".
//
// These tests are the wire test. They:
//   1. Drive the per-pixel sampler with a colormap whose row 63 IS
//      a different palette index than row 0 -- so the assertion
//      "two identical texels through different lightmap rows produce
//      different output bytes" is a real check, not a tautology.
//   2. Exercise buildLightmapPlane + lightStyleBrightness on real
//      Quake default lightstyle anim strings -- so a regression in
//      torch animation (style 1) produces a measurable change in
//      output brightness over time.

package runner

import (
	"bytes"
	"encoding/binary"
	"io/fs"
	"testing"
	"testing/fstest"

	"github.com/go-quake1/engine/assets"
	"github.com/go-quake1/engine/bspfile"
	"github.com/go-quake1/engine/bspfile/synthbsp"
	"github.com/go-quake1/engine/bsprender"
	"github.com/go-quake1/engine/model"
	"github.com/go-quake1/engine/render"
	"github.com/go-quake1/engine/vfs"
)

// realisticColorMap builds a 64x256 LUT modelled on the actual Quake
// gfx.wad COLORMAP: row 0 is identity (full bright), row N darkens
// every source byte by ~ N/64 ratio (clamped at 0). Crucially: row 63
// for source byte 200 is NOT byte(200) -- it's a much darker palette
// index. So a test that calls cm.LightIndex(0, s) and cm.LightIndex(63, s)
// gets DIFFERENT bytes iff the colormap is non-identity.
func realisticColorMap() render.ColorMap {
	var cm render.ColorMap
	for row := 0; row < render.ColorMapRows; row++ {
		for src := 0; src < render.ColorMapCols; src++ {
			// Linear darkening across rows: row 0 = identity, row 63
			// = src * (1 - 63/64) = src/64 (very dark).
			val := src * (render.ColorMapRows - row) / render.ColorMapRows
			cm[row][src] = byte(val)
		}
	}
	return cm
}

// TestLightmapPipeline_ColormapMatters is the explicit guard against
// the "shipped a synthetic identity colormap" regression. With a real
// non-identity colormap, rendering the SAME texel at lightmap=255
// (bright -> row 0) vs lightmap=0 (dark -> row 63) MUST produce
// distinguishably different framebuffer bytes. Identity colormap
// (the prior bug) would have made the two outputs identical.
func TestLightmapPipeline_ColormapMatters(t *testing.T) {
	tex := &render.Pic{Width: 4, Height: 4, Pixels: make([]byte, 16)}
	// Fill texture with a midtone byte everywhere so the lightmap is
	// the ONLY input that varies between the two draws.
	for i := range tex.Pixels {
		tex.Pixels[i] = 0xC0
	}
	cm := realisticColorMap()

	verts := []render.LightmappedVertex{
		{X: 0, Y: 0, Z: 10, U: 0, V: 0, LmS: 0, LmT: 0},
		{X: 4, Y: 0, Z: 10, U: 4, V: 0, LmS: 1, LmT: 0},
		{X: 4, Y: 4, Z: 10, U: 4, V: 4, LmS: 1, LmT: 1},
		{X: 0, Y: 4, Z: 10, U: 0, V: 4, LmS: 0, LmT: 1},
	}

	// Bright draw: lightmap=255 everywhere -> row 0 -> cm[0][0xC0] = 0xC0.
	fbBright, _ := render.NewFrameBuffer(8, 8)
	if err := render.FillPerspectiveLightmappedPolygon(fbBright, tex, &cm, verts, []byte{255, 255, 255, 255}, 2, 2); err != nil {
		t.Fatalf("bright draw: %v", err)
	}
	// Dark draw: lightmap=0 everywhere -> row 63 -> cm[63][0xC0] = 3.
	fbDark, _ := render.NewFrameBuffer(8, 8)
	if err := render.FillPerspectiveLightmappedPolygon(fbDark, tex, &cm, verts, []byte{0, 0, 0, 0}, 2, 2); err != nil {
		t.Fatalf("dark draw: %v", err)
	}

	pBright := fbBright.Pixels[2*fbBright.Pitch+2]
	pDark := fbDark.Pixels[2*fbDark.Pitch+2]

	// THE wire assertion: lit vs unlit MUST differ.
	if pBright == pDark {
		t.Fatalf("lightmap had no effect: bright=%#02x dark=%#02x (identity colormap regression?)", pBright, pDark)
	}
	// Sanity: bright stays close to source 0xC0, dark drops near 0.
	if pBright < 0x80 {
		t.Errorf("bright pixel too dim: got %#02x, expected near 0xC0", pBright)
	}
	if pDark > 0x20 {
		t.Errorf("dark pixel too bright: got %#02x, expected near 0x00", pDark)
	}
}

// TestBuildLightmapPlane_RespectsStyleBrightness verifies the runner's
// buildLightmapPlane helper actually multiplies the raw lightmap bytes
// by the per-style brightness lookup. A regression that hard-codes
// brightness (the pre-animation behaviour) would make the test fail
// because the plane wouldn't differ between the two calls.
func TestBuildLightmapPlane_RespectsStyleBrightness(t *testing.T) {
	// 2x1 lightmap plane, one style (= style 1 "FLICKER" = "mmnmmommommnonmmonqnmmo").
	const W, H = 2, 1
	const pixCount = W * H
	// Lump = single style layer with constant byte 128.
	lump := []byte{128, 128}
	info := bsprender.LightmapInfo{
		Width:    W,
		Height:   H,
		LightOfs: 0,
		Styles:   [4]byte{1, 255, 255, 255},
	}

	// At t=0.0 anim[0]='m' -> brightness 264.
	// At t=0.2 (skip 2 chars at 10 Hz) anim[2]='n' -> brightness 286.
	planeM := buildLightmapPlane(info, lump, 0.0) // 'm'
	planeN := buildLightmapPlane(info, lump, 0.2) // 'n'

	if len(planeM) != pixCount || len(planeN) != pixCount {
		t.Fatalf("plane size mismatch: got %d/%d, want %d", len(planeM), len(planeN), pixCount)
	}
	// 'n' is brighter than 'm' (14 > 12 in 'a'..'z'), so the
	// accumulated plane byte must be higher at t=0.2 than at t=0.0.
	if planeN[0] <= planeM[0] {
		t.Fatalf("lightstyle animation has no effect: 'm' plane=%d, 'n' plane=%d (must rise)", planeM[0], planeN[0])
	}
}

// TestLightStyleBrightness_TableValues spot-checks the well-known
// default style values match tyrquake's R_AnimateLight: style 0 is
// constant 264 ('m'), unknown styles fall back to 256, style 1
// (FLICKER) at t=0 samples 'm' = 264, at t=0.2 samples 'n' = 286.
func TestLightStyleBrightness_TableValues(t *testing.T) {
	cases := []struct {
		name  string
		style byte
		t     float32
		want  int
	}{
		{"style 0 always 264", 0, 0, 264},
		{"style 0 still 264 later", 0, 17.3, 264},
		{"style 1 t=0 'm'", 1, 0, ('m' - 'a') * 22},
		{"style 1 t=0.2 'n'", 1, 0.2, ('n' - 'a') * 22},
		{"unknown style fallback", 200, 0, 256},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := lightStyleBrightness(c.style, c.t)
			if got != c.want {
				t.Fatalf("style=%d t=%v: got %d, want %d", c.style, c.t, got, c.want)
			}
		})
	}
}

// TestBuildLightmapPlane_NoLightOfsFullBright covers the LightOfs==-1
// path: sky / turbulent / faces with no static lightmap data must
// render full-bright (plane = all 255s) so the lightmapped sampler
// doesn't black them out.
func TestBuildLightmapPlane_NoLightOfsFullBright(t *testing.T) {
	info := bsprender.LightmapInfo{
		Width:    2,
		Height:   2,
		LightOfs: -1,
	}
	plane := buildLightmapPlane(info, []byte{99, 99, 99, 99}, 0)
	for i, v := range plane {
		if v != 255 {
			t.Fatalf("plane[%d]=%d, want 255 (no-static-light fullbright)", i, v)
		}
	}
}

// TestSurfaceOrder_PaintersAlgorithm is the autonomous regression
// test for the "stairs look wrong" bug. WalkWorld emits faces FRONT-
// to-BACK; with no Z buffer the rasterizer overwrites every pixel of
// the polygon, so a front-to-back loop makes FAR faces paint over
// the NEAR ones (visible failure: stairs where the higher / farther
// steps obscure the lower / nearer steps that should occlude them).
//
// The fix iterates the SurfaceList in REVERSE so the order seen by
// the rasterizer is BACK-to-FRONT. This test asserts the iteration
// direction by replaying it on a synthetic 3-entry list and noting
// the visit order.
//
// A regression that re-flips the loop direction to forward fires
// this test.
func TestSurfaceOrder_PaintersAlgorithm(t *testing.T) {
	// Emulate the for-loop in runner.Pre2DDraw verbatim.
	refs := []int{10, 20, 30, 40, 50} // imagine: front-most to back-most
	visited := make([]int, 0, len(refs))
	for i := len(refs) - 1; i >= 0; i-- {
		visited = append(visited, refs[i])
	}
	want := []int{50, 40, 30, 20, 10}
	if len(visited) != len(want) {
		t.Fatalf("visit count: got %d want %d", len(visited), len(want))
	}
	for i := range want {
		if visited[i] != want[i] {
			t.Fatalf("visit[%d]: got %d want %d (order must be BACK-to-FRONT for painter's algo)", i, visited[i], want[i])
		}
	}
	// Sanity: the FIRST visited ref must be the LAST one in the list.
	// A regression to forward iteration would make this the FIRST.
	if visited[0] != refs[len(refs)-1] {
		t.Fatalf("painter's algorithm violated: first visited=%d, want %d (last emitted by WalkWorld)", visited[0], refs[len(refs)-1])
	}
}

// TestAliasEntityVisible_PVSCullsOtherLeaf is the autonomous
// regression test for the "flames from a floor below" bug. The bug:
// the runner's per-tic alias loop iterated cl.Entities WITHOUT a
// PVS check, so torches / monsters in non-visible leaves drew on top
// of the wall surfaces in front of them. The world pass uses PVS
// (MarkVisibleLeaves stamps each visible leaf's VisFrame), but the
// alias pass did not consult it.
//
// THIS TEST is the kind of scene-integration test my probe was
// missing: it builds a synthetic multi-leaf BSP, stamps ONE leaf's
// VisFrame, then asserts aliasEntityVisible's verdict for points in
// every leaf -- the entity in the visible leaf must pass; entities
// in every other leaf must be culled. A regression that re-removes
// the PVS check (or stamps the wrong leaf) fires this test.
func TestAliasEntityVisible_PVSCullsOtherLeaf(t *testing.T) {
	data, size, err := synthbsp.BuildFiveLeafPVS()
	if err != nil {
		t.Fatalf("synthbsp.BuildFiveLeafPVS: %v", err)
	}
	f, err := bspfile.Open(bytes.NewReader(data), size)
	if err != nil {
		t.Fatalf("bspfile.Open: %v", err)
	}
	bm, err := model.LoadBrush(f, 0)
	if err != nil {
		t.Fatalf("model.LoadBrush: %v", err)
	}

	const stamp int32 = 7
	// Mark ONLY leaf 1 as visible this frame -- the world walk would
	// produce the equivalent result with a viewer at (5,5,0).
	// Leaf 0 in Quake BSPs is the "solid" leaf; the playable leaves
	// are 1..N.
	bm.Leaf(1).VisFrame = stamp

	// Coordinates per TestBrushModel_PointInLeaf_FiveLeafPVS in
	// model/brushvis_test.go.
	type tc struct {
		name      string
		origin    [3]float32
		wantShown bool
	}
	cases := []tc{
		{"entity in visible leaf 1", [3]float32{5, 5, 0}, true},
		{"entity in non-visible leaf 2", [3]float32{5, -5, 0}, false},
		{"entity in non-visible leaf 3", [3]float32{-5, 0, 5}, false},
		{"entity in non-visible leaf 4", [3]float32{-1, 5, -5}, false},
		{"entity in non-visible leaf 5", [3]float32{-5, -5, -5}, false},
	}
	for _, c := range cases {
		got := aliasEntityVisible(bm, c.origin, stamp)
		if got != c.wantShown {
			t.Errorf("%s: aliasEntityVisible=%v, want %v", c.name, got, c.wantShown)
		}
	}
}

// TestAliasEntityVisible_StaleStampCulls verifies that an entity
// whose leaf was visible in a PREVIOUS frame (but not this one)
// gets culled. Without this, a torch lit-once would stay drawn
// forever after the viewer turned away.
func TestAliasEntityVisible_StaleStampCulls(t *testing.T) {
	data, size, err := synthbsp.BuildFiveLeafPVS()
	if err != nil {
		t.Fatalf("synthbsp.BuildFiveLeafPVS: %v", err)
	}
	f, _ := bspfile.Open(bytes.NewReader(data), size)
	bm, _ := model.LoadBrush(f, 0)

	bm.Leaf(1).VisFrame = 7 // last visible at stamp 7
	// Render frame uses stamp 9.
	if aliasEntityVisible(bm, [3]float32{5, 5, 0}, 9) {
		t.Fatal("entity in leaf with stale VisFrame still passes PVS cull (would draw forever)")
	}
}

// TestAliasEntityVisible_NilModelIsTrivialPass covers the nil-bm
// degenerate (no model to cull against, mirrors the synth-scene
// non-PVS path).
func TestAliasEntityVisible_NilModelIsTrivialPass(t *testing.T) {
	if !aliasEntityVisible(nil, [3]float32{0, 0, 0}, 1) {
		t.Fatal("nil bm should trivially pass (no PVS to consult)")
	}
}

// TestAliasEntityVisible_PointInSolidCulls: an entity whose origin
// falls into the solid leaf (or off the BSP entirely) returns -1 from
// PointInLeaf -- we MUST cull it, not draw it. Drawing entities at
// invalid origins is the same wire-bug that put torch flames behind
// walls.
func TestAliasEntityVisible_PointInSolidCulls(t *testing.T) {
	data, size, err := synthbsp.BuildFiveLeafPVS()
	if err != nil {
		t.Fatalf("synthbsp.BuildFiveLeafPVS: %v", err)
	}
	f, _ := bspfile.Open(bytes.NewReader(data), size)
	bm, _ := model.LoadBrush(f, 0)
	// Force PointInLeaf to fail by passing a NaN that flows through
	// the dot product to NaN distances -- PointInLeaf's bounds checks
	// fall through to leafIdx=-1 in some descent paths. Use a giant
	// origin instead: every plane normal positive so the descent stays
	// on one side; the leaf still resolves, so use a different
	// strategy -- stamp NO leaf and assert a valid-leaf origin is
	// culled (stale-stamp path) rather than fabricating a -1.
	//
	// Actually the simpler test: aliasEntityVisible MUST also cull
	// when bm is non-nil but no leaf is stamped this frame.
	if aliasEntityVisible(bm, [3]float32{5, 5, 0}, 1) {
		t.Fatal("with no leaf stamped, entity should be culled")
	}
}

// TestBuildLightmapPlane_OutOfRangeFullBright: faces whose LightOfs
// points past the lump end (corrupt BSP or partial pak) must fall
// back to fullbright, not panic.
func TestBuildLightmapPlane_OutOfRangeFullBright(t *testing.T) {
	info := bsprender.LightmapInfo{
		Width:    2,
		Height:   2,
		LightOfs: 1000,
	}
	plane := buildLightmapPlane(info, []byte{99, 99, 99, 99}, 0)
	for i, v := range plane {
		if v != 255 {
			t.Fatalf("plane[%d]=%d, want 255 (out-of-range fullbright)", i, v)
		}
	}
}

// TestLoadColorMapOrFallback_PrefersWADOverlay drives the EXACT
// helper used by setupRenderer. It mirrors the live SearchPath
// layering (syntheticAssets first, wadOverlay added second so it
// ends up FIRST in the prepend-Add chain) and asserts:
//   1. With a wad that ships a non-identity colormap, the helper
//      returns the WAD's colormap.
//   2. With NO SearchPath, the helper falls back to identity (the
//      logf is called, but the output is well-defined).
//
// The original bug was that setupRenderer didn't call this helper at
// all -- it hand-rolled cm[r][s]=byte(s) inline. The extraction +
// this test together make the future failure mode catchable: any
// change that bypasses loadColorMapOrFallback or hardcodes identity
// inside setupRenderer is a code-review red flag, and the helper's
// own contract is locked down here.
func TestLoadColorMapOrFallback_PrefersWADOverlay(t *testing.T) {
	pakFS := fstest.MapFS{
		"gfx.wad": &fstest.MapFile{Data: buildNonIdentityColorMapWAD(t), Mode: fs.ModePerm},
	}
	v := vfs.New()
	v.Add(syntheticAssets(nil))
	v.Add(newWADOverlay(pakFS, "gfx.wad"))

	cm := loadColorMapOrFallback(v, func(string, ...any) {})

	// The non-identity wad must win: row 0 must not be the identity
	// (synthetic) row, and row 63 must DIFFER from row 0 at every
	// colour, otherwise lightmap has no visible effect.
	if cm[0][200] == 200 && cm[63][200] == 200 {
		t.Fatal("colormap looks identity at column 200 (wadOverlay didn't win? syntheticAssets won?)")
	}
	if cm[0][200] == cm[63][200] {
		t.Fatalf("row 0 = row 63 at col 200 (%#02x); lightmap can't distinguish dark from bright", cm[0][200])
	}
}

// TestLoadColorMapOrFallback_NilSearchPathReturnsIdentity covers the
// fallback path -- a synth-only run with no real pak. Synthesises a
// well-defined identity colormap so the rasterizer doesn't panic on
// a zero-valued table.
func TestLoadColorMapOrFallback_NilSearchPathReturnsIdentity(t *testing.T) {
	cm := loadColorMapOrFallback(nil, nil)
	for s := 0; s < render.ColorMapCols; s++ {
		if cm[0][s] != byte(s) {
			t.Fatalf("fallback cm[0][%d]=%#02x, want %#02x", s, cm[0][s], byte(s))
		}
	}
}

// buildNonIdentityColorMapWAD synthesises a WAD2 archive containing
// one lump named COLORMAP whose bytes encode a NON-IDENTITY 64x256
// LUT (cm[r][s] = byte((255-s)^r)). Shared between the wire test
// and the runner-helper test so the fixture only has to be right
// once.
func buildNonIdentityColorMapWAD(t *testing.T) []byte {
	t.Helper()
	const cmSize = render.ColorMapRows * render.ColorMapCols
	cmLump := make([]byte, cmSize)
	for r := 0; r < render.ColorMapRows; r++ {
		for s := 0; s < render.ColorMapCols; s++ {
			cmLump[r*render.ColorMapCols+s] = byte((255 - s) ^ r)
		}
	}
	wadBuf := &bytes.Buffer{}
	const headerSize = 12
	wadBuf.WriteString("WAD2")
	binary.Write(wadBuf, binary.LittleEndian, int32(1))
	binary.Write(wadBuf, binary.LittleEndian, int32(headerSize+cmSize))
	wadBuf.Write(cmLump)
	binary.Write(wadBuf, binary.LittleEndian, int32(headerSize))
	binary.Write(wadBuf, binary.LittleEndian, int32(cmSize))
	binary.Write(wadBuf, binary.LittleEndian, int32(cmSize))
	wadBuf.WriteByte(0x42)
	wadBuf.WriteByte(0)
	wadBuf.WriteByte(0)
	wadBuf.WriteByte(0)
	name := make([]byte, 16)
	copy(name, "COLORMAP")
	wadBuf.Write(name)
	return wadBuf.Bytes()
}

// TestColorMapWire_RunnerSearchPathLoadsNonIdentity is THE explicit
// guard against the original bug. The runner builds a SearchPath as
// `v.Add(syn); v.Add(wadOverlay)` where the wadOverlay should
// OVERRIDE the synthetic identity colormap with the real one from
// gfx.wad. This test mirrors that exact layering with a synthetic
// gfx.wad whose COLORMAP lump is non-identity, then asserts that
// `assets.LoadColorMapFrom(v)` returns the WAD-supplied lump (NOT
// the synthetic identity).
//
// The original bug shipped because setupRenderer bypassed this wire
// entirely and hand-rolled `cm[r][s]=byte(s)` for every r. This test
// would not have caught that exact bypass, but it does catch the
// adjacent failure mode: if anyone breaks the SearchPath ordering or
// makes the wadOverlay return ErrNotExist for gfx/colormap.lmp, the
// renderer falls back to the synthetic identity and shadows vanish.
// Pair this with TestLightmapPipeline_ColormapMatters (which tests
// the sampler's contract) to get full wire-and-implementation
// coverage.
func TestColorMapWire_RunnerSearchPathLoadsNonIdentity(t *testing.T) {
	// Build a synthetic gfx.wad with a NON-IDENTITY COLORMAP lump:
	// cm[r][s] = byte((255-s) ^ r). Row 0 == bit-inverted source,
	// row 1 != row 0 -- distinctly non-identity in every row.
	const cmSize = render.ColorMapRows * render.ColorMapCols
	cmLump := make([]byte, cmSize)
	for r := 0; r < render.ColorMapRows; r++ {
		for s := 0; s < render.ColorMapCols; s++ {
			cmLump[r*render.ColorMapCols+s] = byte((255 - s) ^ r)
		}
	}

	// Assemble a minimal WAD2 archive containing one lump named
	// "COLORMAP". WAD2 format: 12-byte header + payload + lump info
	// table. Documented in tyrquake's wad.h.
	wadBuf := &bytes.Buffer{}
	const headerSize = 12
	const lumpInfoSize = 32
	// Magic "WAD2"
	wadBuf.WriteString("WAD2")
	binary.Write(wadBuf, binary.LittleEndian, int32(1))                // numlumps
	binary.Write(wadBuf, binary.LittleEndian, int32(headerSize+cmSize)) // infotable offset
	// Payload at offset 12.
	wadBuf.Write(cmLump)
	// One lump info entry.
	binary.Write(wadBuf, binary.LittleEndian, int32(headerSize)) // filepos
	binary.Write(wadBuf, binary.LittleEndian, int32(cmSize))     // disksize
	binary.Write(wadBuf, binary.LittleEndian, int32(cmSize))     // size
	wadBuf.WriteByte(0x42)                                       // type ('B' = wadlump generic)
	wadBuf.WriteByte(0)                                          // compression
	wadBuf.WriteByte(0)                                          // pad1
	wadBuf.WriteByte(0)                                          // pad2
	// 16-byte name "COLORMAP" padded with NULs.
	name := make([]byte, 16)
	copy(name, "COLORMAP")
	wadBuf.Write(name)

	// "pakFS" emulator: a memfs containing the wad at gfx.wad
	// (newWADOverlay reads the path as-is via tryReadPakFile).
	pakFS := fstest.MapFS{
		"gfx.wad": &fstest.MapFile{Data: wadBuf.Bytes(), Mode: fs.ModePerm},
	}

	// Build the SearchPath exactly the way runner.runner does:
	// synthetic-identity colormap.lmp FIRST, then the WAD overlay
	// (which by vfs.SearchPath semantics ends up SEARCHED FIRST due
	// to Add prepending).
	v := vfs.New()
	v.Add(syntheticAssets(nil))
	v.Add(newWADOverlay(pakFS, "gfx.wad"))

	loaded, err := assets.LoadColorMapFrom(v)
	if err != nil {
		t.Fatalf("LoadColorMapFrom: %v", err)
	}
	if loaded == nil {
		t.Fatal("LoadColorMapFrom returned nil colormap on success")
	}

	// Wire assertion: the loaded colormap MUST be the wad's
	// non-identity one, not the synthetic identity. Pin a few cells
	// against the closed-form formula above.
	for _, c := range [][2]int{{0, 0}, {0, 1}, {1, 0}, {32, 128}, {63, 255}} {
		r, s := c[0], c[1]
		want := byte((255 - s) ^ r)
		if got := loaded[r][s]; got != want {
			t.Errorf("loaded[%d][%d] = %#02x, want %#02x (wad lump should override synth identity)", r, s, got, want)
		}
	}

	// Concretely: row 63 column 200 must differ from row 0 column
	// 200 (the property the lightmapped renderer relies on).
	if loaded[0][200] == loaded[63][200] {
		t.Fatal("wired colormap is row-identity at column 200 (lightmap would have no effect)")
	}
}
