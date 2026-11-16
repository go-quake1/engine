// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package main

import (
	"bytes"
	"testing"

	"github.com/go-quake1/engine/render"
	"github.com/go-quake1/engine/runloop"
	"github.com/go-quake1/engine/worldview"
)

// TestServerLightStylesReachTheClient: the real progs.dat's worldspawn calls
// lightstyle(style, string) while the map loads; Host_Spawn_f sends all 64 styles
// (sv_user.c:913-918) and the client keeps them (cl_parse.c:1053-1061): style 0
// is "m", style 1 the torch flicker, 2 the slow pulse (id's world.qc), the
// styles the map does not set are empty. (Needs QUAKE_PAK.)
func TestServerLightStylesReachTheClient(t *testing.T) {
	var r *runloop.Runner
	old := testRunner
	testRunner = func(x *runloop.Runner) { r = x }
	t.Cleanup(func() { testRunner = old })
	closes := 0
	withSurface(t, &fakePresenter{}, false, &closes)
	var errOut bytes.Buffer
	if err := runGame(&bytes.Buffer{}, &errOut, realFS(t), "e1m1", 64, 40, 60); err != nil { // the signon takes several tics to drain
		t.Fatalf("%v\n%s", err, errOut.String())
	}
	cl := r.Client
	if !cl.HaveLightStyles {
		t.Fatal("the client was sent no light style")
	}
	for style, want := range map[int]string{
		0: "m",
		1: "mmnmmommommnonmmonqnmmo",
		2: "abcdefghijklmnopqrstuvwxyzyxwvutsrqponmlkjihgfedcba",
	} {
		if got := string(cl.LightStyles[style].Anim); got != want {
			t.Errorf("style %d: %q, want %q", style, got, want)
		}
	}
	// style 1 at 0.15 s is index 1 of its map, 'm': 264; at 0.2 s index 2, 'n': 286
	if a, b := cl.LightStyleValue(1, 0.15), cl.LightStyleValue(1, 0.25); a != 264 || b != 286 {
		t.Errorf("style 1 at 0.15 and 0.25 s: %d %d, want 264 286", a, b)
	}
}

// TestGameWorldFollowsTheServersLightStyles: the GPU world's animation takes the
// styles the runner gives it (SetLightStyles) over the default table.
func TestGameWorldFollowsTheServersLightStyles(t *testing.T) {
	g := &gameWorld{wv: &worldview.World{}, gd: fakeGPUFrame{frame: make([]byte, 6), frames: new(int), closed: new(int)}, w: 6}
	fb, err := render.NewFrameBuffer(6, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := g.DrawWorld(fb, [3]float32{}, [3]float32{}, 90, 0.25); err != nil {
		t.Fatal(err)
	}
	if got := g.wv.Anim.Style(1); got != worldview.LightStyleValue(1, 0.25) {
		t.Errorf("default table: %d", got)
	}
	g.SetLightStyles(func(s byte) int { return 1000 + int(s) })
	if err := g.DrawWorld(fb, [3]float32{}, [3]float32{}, 90, 0.25); err != nil {
		t.Fatal(err)
	}
	if got := g.wv.Anim.Style(7); got != 1007 {
		t.Errorf("the server's styles: %d, want 1007", got)
	}
}
