// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package game

import (
	"testing"

	"github.com/go-quake1/engine/bspfile"
	"github.com/go-quake1/engine/embedpak"
	enginesound "github.com/go-quake1/engine/sound"
)

// ---- loadBSP + loadMiptexPics(Named) ----

func TestLoadBSPSynthAndPics(t *testing.T) {
	// nil pak -> synthbsp fallback.
	b, size, err := loadBSP(nil)
	if err != nil || len(b) == 0 || size == 0 {
		t.Fatalf("loadBSP(nil): %d %v", len(b), err)
	}
	// pak present but lacking the canonical maps -> synth fallback.
	b2, _, err := loadBSP(memFS{"foo": []byte("bar")})
	if err != nil || len(b2) == 0 {
		t.Fatalf("loadBSP(memFS): %d %v", len(b2), err)
	}

	// Drive loadMiptexPics (the wrapper) + loadMiptexPicsNamed on the
	// synth BSP (no textures lump -> total 0, no error).
	file, err := bspfile.Open(bytesReaderAt(b), size)
	if err != nil {
		t.Fatalf("bspfile.Open: %v", err)
	}
	pics, loaded, total, err := loadMiptexPics(file)
	if err != nil {
		t.Fatalf("loadMiptexPics: %v", err)
	}
	_ = pics
	_ = loaded
	_ = total
}

func TestLoadMiptexPicsRealPak(t *testing.T) {
	pakFS, err := embedpak.OpenAsFS()
	if err != nil {
		t.Skipf("no real pak (%v)", err)
	}
	b, size, err := loadBSP(pakFS)
	if err != nil {
		t.Fatalf("loadBSP: %v", err)
	}
	file, err := bspfile.Open(bytesReaderAt(b), size)
	if err != nil {
		t.Fatalf("bspfile.Open: %v", err)
	}
	_, _, _, total, err := loadMiptexPicsNamed(file)
	if err != nil {
		t.Fatalf("loadMiptexPicsNamed: %v", err)
	}
	if total == 0 {
		t.Fatal("expected a textures lump on the real start.bsp")
	}
}

// ---- seedSoundPool ----

func TestSeedSoundPool(t *testing.T) {
	pool, err := enginesound.NewPool(8)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	pakFS, perr := embedpak.OpenAsFS()
	if perr != nil {
		t.Skipf("no real pak (%v)", perr)
	}
	// Mix real, missing, and (to hit the >= ReservedStatic break) more
	// names than the reserved-static count.
	names := []string{
		"sound/ambience/water1.wav",
		"sound/weapons/r_exp3.wav",
		"sound/does/not/exist.wav",
		"sound/misc/talk.wav",
	}
	// Pad past ReservedStatic so the break branch runs.
	for i := 0; i < pool.ReservedStatic+2; i++ {
		names = append(names, "sound/ambience/water1.wav")
	}
	n := seedSoundPool(pool, pakFS, names)
	if n == 0 {
		t.Fatal("seedSoundPool seeded nothing")
	}
}

func TestSeedSoundPoolBadWav(t *testing.T) {
	pool, err := enginesound.NewPool(4)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	// A present-but-malformed WAV exercises the LoadWav error branch.
	m := memFS{"sound/bad.wav": []byte("not a wav at all")}
	if got := seedSoundPool(pool, m, []string{"sound/bad.wav"}); got != 0 {
		t.Fatalf("malformed WAV seeded %d channels, want 0", got)
	}
}

// ---- loadExplosionSprite ----

func TestLoadExplosionSprite(t *testing.T) {
	// nil pak.
	if sp, p := loadExplosionSprite(nil); sp != nil || p != "" {
		t.Fatal("nil pak -> nil sprite")
	}
	// Real pak: progs/s_explod.spr present.
	pakFS, err := embedpak.OpenAsFS()
	if err != nil {
		t.Skipf("no real pak (%v)", err)
	}
	if sp, path := loadExplosionSprite(pakFS); sp == nil || path == "" {
		t.Fatalf("expected real explosion sprite, got nil")
	}
	// Malformed .spr at the first candidate path -> parse-error branch,
	// then fall through to the (missing) second path -> (nil,"").
	bad := memFS{"progs/s_explod.spr": []byte("nope")}
	if sp, _ := loadExplosionSprite(bad); sp != nil {
		t.Fatal("malformed spr should yield nil")
	}
}

// ---- loadAliasModels ----

func TestLoadAliasModels(t *testing.T) {
	// nil pak / empty precache.
	if m, s, l, n := loadAliasModels(nil, []string{"x.mdl"}); l != 0 || n != 0 || len(m) != 1 || len(s) != 1 {
		t.Fatalf("nil pak: %d %d", l, n)
	}
	if _, _, l, _ := loadAliasModels(memFS{}, nil); l != 0 {
		t.Fatalf("empty precache loaded %d", l)
	}

	pakFS, err := embedpak.OpenAsFS()
	if err != nil {
		t.Skipf("no real pak (%v)", err)
	}
	precache := []string{
		"",                  // sentinel slot
		"maps/start.bsp",    // not a .mdl -> skipped
		"progs/player.mdl",  // real .mdl -> loaded
		"progs/missing.mdl", // .mdl name but missing in pak -> name counted, not loaded
	}
	m, s, loaded, names := loadAliasModels(pakFS, precache)
	if loaded == 0 || names < 2 {
		t.Fatalf("loadAliasModels real: loaded=%d names=%d", loaded, names)
	}
	if m[2] == nil || s[2] == nil {
		t.Fatal("player.mdl slot should be populated with model + skin")
	}

	// Malformed .mdl -> mdl.Load error branch.
	badPak := memFS{"progs/x.mdl": []byte("garbage")}
	if _, _, l, n := loadAliasModels(badPak, []string{"progs/x.mdl"}); l != 0 || n != 1 {
		t.Fatalf("malformed mdl: loaded=%d names=%d", l, n)
	}
}

// ---- loadBoltModels ----

func TestLoadBoltModels(t *testing.T) {
	// nil pak.
	if m, s, l := loadBoltModels(nil); l != 0 || m[0] != nil || s[0] != nil {
		t.Fatal("nil pak -> empty bolts")
	}
	pakFS, err := embedpak.OpenAsFS()
	if err != nil {
		t.Skipf("no real pak (%v)", err)
	}
	// Real pak has progs/bolt.mdl (slot 0 via paths[0]), bolt2, bolt3 but
	// no bolt1.mdl alias, so the alt1 branch misses and the loop loads
	// slot 0..2 from paths.
	_, _, loaded := loadBoltModels(pakFS)
	if loaded == 0 {
		t.Fatal("expected at least one bolt model from the real pak")
	}

	// A pak with a malformed bolt1 alias -> alt1 mdl.Load error branch.
	badAlt := memFS{"progs/bolt1.mdl": []byte("garbage")}
	if _, _, l := loadBoltModels(badAlt); l != 0 {
		t.Fatalf("malformed bolt1 alias loaded %d", l)
	}
}

// ---- loadMenuAssets / loadSBarAssets ----

func TestLoadMenuAndSBarAssets(t *testing.T) {
	// nil pak paths.
	if a, l, total := loadMenuAssets(nil); a == nil || l != 0 || total != 0 {
		t.Fatalf("loadMenuAssets(nil): %v %d %d", a, l, total)
	}
	if a, l, total, missing := loadSBarAssets(nil); a != nil || l != 0 || total != 0 || missing != nil {
		t.Fatalf("loadSBarAssets(nil): %v %d %d", a, l, total)
	}

	pakFS, err := embedpak.OpenAsFS()
	if err != nil {
		t.Skipf("no real pak (%v)", err)
	}
	if _, loaded, total := loadMenuAssets(pakFS); total == 0 {
		t.Fatalf("menu assets: loaded=%d total=%d", loaded, total)
	}
	if _, loaded, total, _ := loadSBarAssets(pakFS); total == 0 {
		t.Fatalf("sbar assets: loaded=%d total=%d", loaded, total)
	}

	// A pak that has the gfx/<name>.lmp shape but holds a non-pic blob so
	// render.ParsePic errors -> the ParsePic-error branch in both loaders.
	badPic := memFS{
		"gfx/qplaque.lmp": []byte("xx"),
		"gfx/sbar.lmp":    []byte("xx"),
	}
	_, _, _ = loadMenuAssets(badPic)
	_, _, _, _ = loadSBarAssets(badPic)
}
