// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/go-quake1/engine/backend/wasisurface"
	"github.com/go-quake1/engine/pak"
	"github.com/go-quake1/engine/render"
	"github.com/go-wasmcontainers/gpu/wasigpu"
)

// pakEnv points at a pak0.pak with e1m1 (the go-quake1/freepak release
// asset); the tests that draw a real view skip without it.
const pakEnv = "QUAKE_PAK"

func realPak(t *testing.T) string {
	t.Helper()
	p := os.Getenv(pakEnv)
	if p == "" {
		t.Skipf("%s not set: no pak0.pak to draw e1m1 from", pakEnv)
	}
	return p
}

// realFS opens the real pak as an fs.FS.
func realFS(t *testing.T) fs.FS {
	t.Helper()
	f, err := os.Open(realPak(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	p, err := pak.Open(f)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// emptyPak writes a valid pak with no entry: it opens, and holds no map.
func emptyPak(t *testing.T) string {
	t.Helper()
	var b [12]byte
	copy(b[:], "PACK")
	binary.LittleEndian.PutUint32(b[4:], 12) // directory offset
	binary.LittleEndian.PutUint32(b[8:], 0)  // directory length
	p := filepath.Join(t.TempDir(), "pak0.pak")
	if err := os.WriteFile(p, b[:], 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func run(t *testing.T, args ...string) (out, errOut string, err error) {
	t.Helper()
	var o, e bytes.Buffer
	c := RootCmd()
	c.SetArgs(args)
	c.SetOut(&o)
	c.SetErr(&e)
	err = c.Execute()
	return o.String(), e.String(), err
}

func TestRootCmdErrors(t *testing.T) {
	if _, _, err := run(t, "--width", "0"); err == nil || !strings.Contains(err.Error(), "bad frame size") {
		t.Errorf("zero width: %v", err)
	}
	if _, _, err := run(t, "--pak", filepath.Join(t.TempDir(), "nope.pak")); err == nil {
		t.Error("missing pak accepted")
	}
	junk := filepath.Join(t.TempDir(), "junk.pak")
	if err := os.WriteFile(junk, []byte("not a pak at all"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := run(t, "--pak", junk); err == nil || !strings.Contains(err.Error(), junk) {
		t.Errorf("junk pak: %v", err)
	}
	if _, _, err := run(t, "extra"); err == nil {
		t.Error("positional argument accepted")
	}
}

func TestNoSurfaceOnNative(t *testing.T) {
	old := openSurface
	openSurface = nil
	t.Cleanup(func() { openSurface = old })
	if _, _, err := run(t, "--pak", emptyPak(t)); !errors.Is(err, ErrNoSurface) {
		t.Errorf("play without a surface: %v, want ErrNoSurface", err)
	}
}

func TestWorldrefNoMap(t *testing.T) {
	if _, _, err := run(t, "--pak", emptyPak(t), "--worldref", "0"); err == nil {
		t.Error("worldref over a pak without e1m1 succeeded")
	}
}

type fakePresenter struct{ frames int }

func (f *fakePresenter) Present(px []byte, w, h, pitch int, pal *render.Palette) error {
	f.frames++
	return nil
}

type fakeKeys struct{ closed bool }

func (f fakeKeys) Poll() ([]wasigpu.KeyEvent, error) { return nil, nil }
func (f fakeKeys) Closed() bool                      { return f.closed }

func withSurface(t *testing.T, p *fakePresenter, closed bool, closes *int) {
	t.Helper()
	old := openSurface
	openSurface = func(w, h int) (*gpuSurface, error) {
		return &gpuSurface{Presenter: p, Down: fakeKeys{closed: closed}, Up: fakeKeys{},
			Close: func() { *closes++ }}, nil
	}
	t.Cleanup(func() { openSurface = old })
}

func TestSurfaceOpenError(t *testing.T) {
	boom := errors.New("no adapter")
	old := openSurface
	openSurface = func(int, int) (*gpuSurface, error) { return nil, boom }
	t.Cleanup(func() { openSurface = old })
	if err := runGame(&bytes.Buffer{}, &bytes.Buffer{}, fstest.MapFS{}, "e1m1", 64, 40, 1); !errors.Is(err, boom) {
		t.Errorf("runGame: %v", err)
	}
	if _, _, err := run(t, "--pak", realPakOrEmpty(t), "--worldref", "0"); err == nil {
		t.Error("worldref did not fail")
	}
}

func realPakOrEmpty(t *testing.T) string {
	if p := os.Getenv(pakEnv); p != "" {
		return p
	}
	return emptyPak(t)
}

// TestRunGameFrames plays a few frames of the built-in fallback world on a
// fake surface: the runner is wired with SkipMenu, every frame reaches the
// presenter, and the mean frame time is printed.
func TestRunGameFrames(t *testing.T) {
	p := &fakePresenter{}
	closes := 0
	withSurface(t, p, false, &closes)
	var out, errOut bytes.Buffer
	if err := runGame(&out, &errOut, realFS(t), "e1m1", 64, 40, 3); err != nil {
		t.Fatalf("runGame: %v\n%s", err, errOut.String())
	}
	if p.frames != 3 {
		t.Errorf("presented %d frames, want 3", p.frames)
	}
	if !strings.Contains(out.String(), "3 frames in ") || !strings.Contains(out.String(), "ms per frame") {
		t.Errorf("stats = %q", out.String())
	}
	if !strings.Contains(errOut.String(), "menu skipped") {
		t.Errorf("SkipMenu not logged:\n%s", errOut.String())
	}
	if closes != 1 {
		t.Errorf("surface closed %d times, want 1", closes)
	}
}

// TestRunGameStopsWhenSurfaceCloses: a dropped surface (the key-down stream
// ended) quits the loop with no stats.
func TestRunGameStopsWhenSurfaceCloses(t *testing.T) {
	p := &fakePresenter{}
	closes := 0
	withSurface(t, p, true, &closes)
	var out bytes.Buffer
	if err := runGame(&out, &bytes.Buffer{}, realFS(t), "e1m1", 64, 40, 0); err != nil {
		t.Fatal(err)
	}
	if p.frames != 1 || out.Len() != 0 || closes != 1 {
		t.Errorf("frames %d, out %q, closes %d", p.frames, out.String(), closes)
	}
}

func TestRunGameSetupError(t *testing.T) {
	closes := 0
	withSurface(t, &fakePresenter{}, false, &closes)
	err := runGame(&bytes.Buffer{}, &bytes.Buffer{}, fstest.MapFS{}, "e1m1", 0, 40, 1)
	if err == nil || closes != 1 {
		t.Errorf("err %v, closes %d", err, closes)
	}
}

var _ wasisurface.KeyPoller = fakeKeys{}

// TestWorldrefMatchesQuakeWorldref: --worldref N prints the same
// sha256(indices) as quake-worldref for the same view, for every fixed
// view including the player start (the last), and the RGBA hash is the
// reference's.
func TestWorldrefMatchesQuakeWorldref(t *testing.T) {
	pakPath := realPak(t)
	old := openSurface
	openSurface = nil
	t.Cleanup(func() { openSurface = old })

	outDir := t.TempDir()
	cmd := exec.Command("go", "run", "../quake-worldref", "--pak", pakPath, "--out", outDir)
	ref, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("quake-worldref: %v\n%s", err, ref)
	}
	want := map[string]string{} // "view04" -> hex
	for _, line := range strings.Split(string(ref), "\n") {
		f := strings.Fields(line)
		if len(f) == 3 && f[1] == "sha256(indices)" {
			base := filepath.Base(f[0])
			want[strings.TrimSuffix(base[strings.Index(base, "-view")+1:], ".png")] = f[2]
		}
	}
	if len(want) < 2 {
		t.Fatalf("parsed %d views from quake-worldref:\n%s", len(want), ref)
	}
	for name, hex := range want {
		n := strings.TrimPrefix(name, "view")
		n = strings.TrimLeft(n, "0")
		if n == "" {
			n = "0"
		}
		out, _, err := run(t, "--pak", pakPath, "--worldref", n)
		if err != nil {
			t.Fatalf("--worldref %s: %v", n, err)
		}
		if !strings.Contains(out, "sha256(indices) "+hex+"\n") {
			t.Errorf("--worldref %s: indices hash differs from quake-worldref's %s:\n%s", n, hex, out)
		}
		if !strings.Contains(out, "sha256(rgba) ") {
			t.Errorf("--worldref %s: no rgba hash:\n%s", n, out)
		}
	}
	if _, _, err := run(t, "--pak", pakPath, "--worldref", "999"); err == nil {
		t.Error("view 999 accepted")
	}
}

// TestWorldrefPresentsThroughSurface: with a surface, the frame is presented
// worldrefPresents times and the surface is closed.
func TestWorldrefPresentsThroughSurface(t *testing.T) {
	pakPath := realPak(t)
	p := &fakePresenter{}
	closes := 0
	withSurface(t, p, false, &closes)
	if _, _, err := run(t, "--pak", pakPath, "--worldref", "0", "--width", "160", "--height", "100"); err != nil {
		t.Fatal(err)
	}
	if p.frames != worldrefPresents || closes != 1 {
		t.Errorf("presented %d, closed %d", p.frames, closes)
	}
}
