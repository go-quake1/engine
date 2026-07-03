// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package game

import (
	"io"
	"io/fs"
	"testing"
	"time"

	"github.com/go-quake1/engine/mdl"
	"github.com/go-quake1/engine/render"
	"github.com/go-quake1/engine/vfs"
)

// ---- trailKindForModel: every case incl. default ----

func TestTrailKindForModel(t *testing.T) {
	cases := []struct {
		name string
		want render.TrailKind
		ok   bool
	}{
		{"progs/missile.mdl", render.TrailRocket, true},
		{"progs/grenade.mdl", render.TrailGrenade, true},
		{"progs/gib1.mdl", render.TrailBlood, true},
		{"progs/gib2.mdl", render.TrailBlood, true},
		{"progs/gib3.mdl", render.TrailBlood, true},
		{"progs/zom_gib.mdl", render.TrailBlood, true},
		{"progs/k_spike.mdl", render.TrailSlightBlood, true},
		{"progs/w_spike.mdl", render.TrailTracer, true},
		{"progs/laser.mdl", render.TrailTracer2, true},
		{"progs/v_spike.mdl", render.TrailVoor, true},
		{"progs/player.mdl", 0, false},
		{"", 0, false},
	}
	for _, c := range cases {
		got, ok := trailKindForModel(c.name)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("trailKindForModel(%q) = (%v,%v), want (%v,%v)", c.name, got, ok, c.want, c.ok)
		}
	}
}

// ---- newLCGByteSource: call the returned closure ----

func TestNewLCGByteSource(t *testing.T) {
	src := newLCGByteSource(0xC0FFEF)
	// Drive several iterations so the multiply+add+shift line runs.
	var seen [256]bool
	distinct := 0
	for i := 0; i < 64; i++ {
		b := src()
		if !seen[b] {
			seen[b] = true
			distinct++
		}
	}
	if distinct < 8 {
		t.Fatalf("LCG byte source too repetitive: only %d distinct in 64", distinct)
	}
}

func TestNewLCGRandom(t *testing.T) {
	r := newLCGRandom(0xC0FFEE)
	for i := 0; i < 32; i++ {
		v := r()
		if v < 0 || v >= 1 {
			t.Fatalf("newLCGRandom out of [0,1): %v", v)
		}
	}
}

// ---- makeCheckerTex: tile<1 path + normal path ----

func TestMakeCheckerTex(t *testing.T) {
	// n=16 -> tile = 4 (>=1).
	p := makeCheckerTex(16)
	if p.Width != 16 || p.Height != 16 || len(p.Pixels) != 256 {
		t.Fatalf("makeCheckerTex(16): bad pic %dx%d len=%d", p.Width, p.Height, len(p.Pixels))
	}
	// n=2 -> tile = 0 -> clamped to 1.
	p2 := makeCheckerTex(2)
	if p2.Width != 2 || len(p2.Pixels) != 4 {
		t.Fatalf("makeCheckerTex(2): bad pic %dx%d", p2.Width, p2.Height)
	}
}

// ---- makeConcharsLump: success path (size matches) ----

func TestMakeConcharsLump(t *testing.T) {
	b := makeConcharsLump()
	if len(b) != 16384 {
		t.Fatalf("makeConcharsLump len=%d want 16384", len(b))
	}
}

// ---- hasSuffix ----

func TestHasSuffix(t *testing.T) {
	if !hasSuffix("progs/foo.mdl", ".mdl") {
		t.Fatal("hasSuffix true case")
	}
	if hasSuffix("x", ".mdl") {
		t.Fatal("hasSuffix short case must be false")
	}
	if hasSuffix("foo.bsp", ".mdl") {
		t.Fatal("hasSuffix mismatch case")
	}
}

// ---- wadLumpName: all branches ----

func TestWadLumpName(t *testing.T) {
	if l, ok := wadLumpName("gfx/sbar.lmp"); !ok || l != "sbar" {
		t.Fatalf("wadLumpName(gfx/sbar.lmp) = %q,%v", l, ok)
	}
	if _, ok := wadLumpName("gfx/.lmp"); ok {
		t.Fatal("too-short path must be false")
	}
	if _, ok := wadLumpName("models/x.lmp"); ok {
		t.Fatal("non-gfx prefix must be false")
	}
	if _, ok := wadLumpName("gfx/sbar.dat"); ok {
		t.Fatal("non-.lmp suffix must be false")
	}
}

// ---- firstSkinAsPic: every branch ----

func TestFirstSkinAsPic(t *testing.T) {
	if firstSkinAsPic(nil) != nil {
		t.Fatal("nil model -> nil")
	}
	// Zero skins.
	if firstSkinAsPic(&mdl.Model{}) != nil {
		t.Fatal("zero skins -> nil")
	}
	// w<=0.
	mBadDim := &mdl.Model{Skins: []mdl.Skin{{Type: mdl.SkinSingle}}}
	if firstSkinAsPic(mBadDim) != nil {
		t.Fatal("w<=0 -> nil")
	}
	// Single skin with mismatched length.
	mMismatch := &mdl.Model{
		Header: mdl.Header{SkinWidth: 2, SkinHeight: 2},
		Skins:  []mdl.Skin{{Type: mdl.SkinSingle, Single: mdl.SingleSkin{Pixels: []byte{1, 2}}}},
	}
	if firstSkinAsPic(mMismatch) != nil {
		t.Fatal("len mismatch -> nil")
	}
	// Single skin OK.
	mOK := &mdl.Model{
		Header: mdl.Header{SkinWidth: 2, SkinHeight: 2},
		Skins:  []mdl.Skin{{Type: mdl.SkinSingle, Single: mdl.SingleSkin{Pixels: []byte{1, 2, 3, 4}}}},
	}
	p := firstSkinAsPic(mOK)
	if p == nil || p.Width != 2 || p.Height != 2 || len(p.Pixels) != 4 {
		t.Fatalf("single-skin pic = %+v", p)
	}
	// Group skin with empty group.
	mGrpEmpty := &mdl.Model{
		Header: mdl.Header{SkinWidth: 2, SkinHeight: 2},
		Skins:  []mdl.Skin{{Type: mdl.SkinGroup, Group: nil}},
	}
	if firstSkinAsPic(mGrpEmpty) != nil {
		t.Fatal("nil group -> nil")
	}
	// Group skin OK (first sub-skin).
	mGrp := &mdl.Model{
		Header: mdl.Header{SkinWidth: 2, SkinHeight: 2},
		Skins: []mdl.Skin{{Type: mdl.SkinGroup, Group: &mdl.GroupSkin{
			Intervals: []float32{0.1},
			Skins:     []mdl.SingleSkin{{Pixels: []byte{5, 6, 7, 8}}},
		}}},
	}
	if p := firstSkinAsPic(mGrp); p == nil || p.Pixels[0] != 5 {
		t.Fatalf("group-skin pic = %+v", p)
	}
	// Unknown skin type -> nil.
	mUnknown := &mdl.Model{
		Header: mdl.Header{SkinWidth: 2, SkinHeight: 2},
		Skins:  []mdl.Skin{{Type: 99}},
	}
	if firstSkinAsPic(mUnknown) != nil {
		t.Fatal("unknown skin type -> nil")
	}
}

// ---- readerAt.ReadAt: all branches ----

func TestReaderAtReadAt(t *testing.T) {
	ra := bytesReaderAt([]byte{0, 1, 2, 3, 4, 5, 6, 7})
	// off < 0.
	if _, err := ra.ReadAt(make([]byte, 1), -1); err == nil {
		t.Fatal("off<0 must error")
	}
	// off >= size.
	if _, err := ra.ReadAt(make([]byte, 1), 8); err == nil {
		t.Fatal("off>=size must error")
	}
	// partial copy mid-buffer (n+off < size && n < len(p)).
	buf := make([]byte, 2)
	n, err := ra.ReadAt(buf, 2)
	if err != nil || n != 2 || buf[0] != 2 || buf[1] != 3 {
		t.Fatalf("mid-buffer read n=%d err=%v buf=%v", n, err, buf)
	}
	// read to exact end with p larger than remaining -> n<len(p) -> EOF.
	big := make([]byte, 4)
	n, err = ra.ReadAt(big, 6)
	if n != 2 || err == nil {
		t.Fatalf("end read with short n: n=%d err=%v", n, err)
	}
	// read covering exactly to the end with p == remaining: n==len(p), n+off==size.
	exact := make([]byte, 2)
	n, err = ra.ReadAt(exact, 6)
	if n != 2 || err != nil {
		t.Fatalf("exact-end read n=%d err=%v", n, err)
	}
}

// ---- stubHost.Frame ----

func TestStubHostFrame(t *testing.T) {
	if err := (stubHost{}).Frame(0.1); err != nil {
		t.Fatalf("stubHost.Frame: %v", err)
	}
}

// ---- memFS / memFile / memFileInfo ----

func TestMemFS(t *testing.T) {
	m := syntheticAssets()
	f, err := m.Open("gfx/palette.lmp")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	// Stat + FileInfo methods.
	fi, err := f.Stat()
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if fi.Name() != "gfx/palette.lmp" {
		t.Errorf("Name = %q", fi.Name())
	}
	if fi.Size() != 768 {
		t.Errorf("Size = %d", fi.Size())
	}
	if fi.Mode() != fs.FileMode(0o444) {
		t.Errorf("Mode = %v", fi.Mode())
	}
	if !fi.ModTime().Equal(time.Time{}) {
		t.Errorf("ModTime not zero")
	}
	if fi.IsDir() {
		t.Errorf("IsDir true")
	}
	if fi.Sys() != nil {
		t.Errorf("Sys not nil")
	}
	// Read the whole thing (Read EOF path).
	data, err := io.ReadAll(f)
	if err != nil || len(data) != 768 {
		t.Fatalf("ReadAll: %d %v", len(data), err)
	}
	// Reading again at EOF returns 0, io.EOF.
	if n, err := f.Read(make([]byte, 4)); n != 0 || err != io.EOF {
		t.Fatalf("post-EOF read n=%d err=%v", n, err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// Missing entry.
	if _, err := m.Open("nope"); err == nil {
		t.Fatal("missing entry must error")
	}
}

// ---- tryReadPakFile / tryReadFromFS / reportLumpSources ----

// errFS is an fs.FS whose files fail their Read so io.ReadAll errors.
type errReadFile struct{}

func (errReadFile) Stat() (fs.FileInfo, error) { return nil, io.ErrUnexpectedEOF }
func (errReadFile) Read([]byte) (int, error)   { return 0, io.ErrUnexpectedEOF }
func (errReadFile) Close() error               { return nil }

type errReadFS struct{}

func (errReadFS) Open(string) (fs.File, error) { return errReadFile{}, nil }

func TestTryReadHelpers(t *testing.T) {
	m := memFS{"a": []byte("hello")}
	// hit.
	if data, ok := tryReadPakFile(m, "a"); !ok || string(data) != "hello" {
		t.Fatalf("tryReadPakFile hit: %q %v", data, ok)
	}
	// miss (open error).
	if _, ok := tryReadPakFile(m, "b"); ok {
		t.Fatal("tryReadPakFile miss must be false")
	}
	// read error.
	if _, ok := tryReadPakFile(errReadFS{}, "x"); ok {
		t.Fatal("tryReadPakFile read-err must be false")
	}
	// tryReadFromFS variants.
	if data, ok := tryReadFromFS(m, "a"); !ok || string(data) != "hello" {
		t.Fatalf("tryReadFromFS hit: %q %v", data, ok)
	}
	if _, ok := tryReadFromFS(m, "b"); ok {
		t.Fatal("tryReadFromFS miss must be false")
	}
	if _, ok := tryReadFromFS(errReadFS{}, "x"); ok {
		t.Fatal("tryReadFromFS read-err must be false")
	}
}

func TestReportLumpSources(t *testing.T) {
	v := vfs.New()
	syn := memFS{"gfx/palette.lmp": []byte{1, 2, 3}}
	pak := memFS{"gfx/palette.lmp": []byte{1, 2, 3}}
	v.Add(syn)
	v.Add(pak)
	// One present (real-pak match), one missing.
	reportLumpSources(v, pak, syn, []string{"gfx/palette.lmp", "gfx/missing.lmp"})

	// Now with a synthetic-only mismatch + nil pak.
	v2 := vfs.New()
	v2.Add(memFS{"gfx/palette.lmp": []byte{9, 9, 9}})
	reportLumpSources(v2, nil, syn, []string{"gfx/palette.lmp"})

	// pak present but bytes differ -> "synthetic".
	v3 := vfs.New()
	v3.Add(memFS{"gfx/palette.lmp": []byte{9, 9, 9}})
	reportLumpSources(v3, memFS{"gfx/palette.lmp": []byte{1, 1, 1}}, syn, []string{"gfx/palette.lmp"})
}
