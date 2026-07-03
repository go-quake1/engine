// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package game

import (
	"bytes"
	"fmt"
	"io"
	"testing"

	"github.com/go-quake1/engine/embedpak"
	"github.com/go-quake1/engine/progs"
)

// ---- buildHost error branches ----

func TestBuildHostErrors(t *testing.T) {
	// progs.dat missing.
	if _, err := buildHost(memFS{"x": []byte("y")}, "start"); err == nil {
		t.Fatal("missing progs.dat must error")
	}
	// progs.dat present but corrupt -> progs.Load error.
	if _, err := buildHost(memFS{"progs.dat": []byte("not a progs file")}, "start"); err == nil {
		t.Fatal("corrupt progs.dat must error")
	}

	// Real progs.dat but no map -> SpawnServer error.
	pakFS, err := embedpak.OpenAsFS()
	if err != nil {
		t.Skipf("no real pak (%v)", err)
	}
	pb, _ := tryReadPakFile(pakFS, "progs.dat")
	onlyProgs := memFS{"progs.dat": pb}
	if _, err := buildHost(onlyProgs, "start"); err == nil {
		t.Fatal("missing map must surface a SpawnServer error")
	}
}

// ---- builtinPrecacheModel: PrecacheModel full -> error branch ----

func TestBuiltinPrecacheModelFull(t *testing.T) {
	h, vm, p := buildRealHost(t)
	mdlOfs := findStringOfs(p, ".mdl")
	if mdlOfs == 0 {
		t.Skip("no .mdl string")
	}
	// Fill every precache slot so PrecacheModel returns ErrPrecacheFull
	// for a name not already present.
	for i := range h.Server.ModelPrecache {
		h.Server.ModelPrecache[i] = fmt.Sprintf("__fill_%d__", i)
	}
	fn := builtinPrecacheModel(h)
	must(t, vm.SetGlobalInt(progs.OfsParm0, mdlOfs)) // ".mdl" name, not in the filler set
	if err := fn(vm); err != nil {
		t.Fatalf("precache_model full: %v", err)
	}
}

// ---- builtinSetModel: ModelIndex error + nil-arena early-out ----

func TestBuiltinSetModelModelIndexErr(t *testing.T) {
	h, vm, p := buildRealHost(t)
	if vm.Arena() == nil || len(h.Server.Edicts) < 2 || h.Server.Edicts[1] == nil {
		t.Skip("no arena/player")
	}
	mdlOfs := findStringOfs(p, ".mdl") // a real name string, but unlikely precached at this slot
	ptr := vm.Arena().PointerForEdict(h.Server.Edicts[1])
	must(t, vm.SetGlobalInt(progs.OfsParm0, ptr))
	must(t, vm.SetGlobalInt(progs.OfsParm1, mdlOfs))
	// Clear the precache so ModelIndex(name) fails for sure.
	for i := range h.Server.ModelPrecache {
		h.Server.ModelPrecache[i] = ""
	}
	fn := builtinSetModel(h)
	if err := fn(vm); err != nil {
		t.Fatalf("setmodel ModelIndex err: %v", err)
	}
}

func TestBuiltinSetModelNoArena(t *testing.T) {
	h, _, p := buildRealHost(t)
	// A fresh VM with no arena set so vm.Arena() == nil -> early return.
	noArena := progs.NewVM(p)
	if err := builtinSetModel(h)(noArena); err != nil {
		t.Fatalf("setmodel no-arena: %v", err)
	}
	// setorigin no-arena early return too.
	if err := builtinSetOrigin(h)(noArena); err != nil {
		t.Fatalf("setorigin no-arena: %v", err)
	}
}

// ---- buildHost SpawnFn dispatch coverage via a real map ----
// (Driven implicitly by buildRealHost; here we additionally exercise the
// classname-not-found early return by spawning with a junk classname is
// internal -- skipped. The real spawn pass already covers the path.)

// ---- resolveModelBBox: garbage .mdl through a fake resolver ----

func TestResolveModelBBoxGarbageMDL(t *testing.T) {
	h, _, _ := buildRealHost(t)
	// Swap in a resolver that returns garbage bytes so mdl.Load fails.
	orig := h.Resolver
	defer func() { h.Resolver = orig }()
	h.Resolver = func(name string) (int64, io.ReaderAt, error) {
		b := []byte("garbage not an mdl")
		return int64(len(b)), bytes.NewReader(b), nil
	}
	cache := &setModelCache{mdlBBox: map[int][2][3]float32{}}
	if _, _, ok := resolveModelBBox(h, cache, "progs/junk.mdl", 50); ok {
		t.Fatal("garbage mdl must be !ok")
	}
}
