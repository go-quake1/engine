// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/go-quake1/engine/render"
	"github.com/go-quake1/engine/worldview"
)

type failingGPU struct{ err error }

func (f failingGPU) Draw(worldview.View) ([]byte, error) { return nil, f.err }
func (f failingGPU) Close()                              {}

// A failed draw returns its error whether or not a check reads it.
func TestFailedDrawReturnsItsError(t *testing.T) {
	want := errors.New("device lost")
	g := &gameWorld{wv: &worldview.World{}, w: 4, gd: failingGPU{err: want}}
	fb, err := render.NewFrameBufferAligned(4, 2, 4)
	if err != nil {
		t.Fatal(err)
	}
	if got := g.DrawWorld(fb, [3]float32{}, [3]float32{}, 90, 0); !errors.Is(got, want) {
		t.Errorf("DrawWorld = %v, want %v", got, want)
	}
}

// TestCheckNoFrameNamesTheCause: when no frame is compared because every GPU
// draw failed, the error carries the number of fallback frames and the FIRST
// draw error (it said only "compared no frame").
func TestCheckNoFrameNamesTheCause(t *testing.T) {
	g := &gameWorld{wv: &worldview.World{}, w: 4}
	c := checkStats{ents: g}
	fb, err := render.NewFrameBufferAligned(4, 2, 4)
	if err != nil {
		t.Fatal(err)
	}
	for _, msg := range []string{"device lost", "second error"} {
		g.gd = failingGPU{err: errors.New(msg)}
		if err := g.DrawWorld(fb, [3]float32{}, [3]float32{}, 90, 0); err == nil {
			t.Fatal("a failing draw returned nil")
		}
	}
	err = c.report(&bytes.Buffer{})
	if err == nil {
		t.Fatal("no frame compared passed")
	}
	for _, want := range []string{"compared no frame", "2 frames fell back", "device lost"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "second error") {
		t.Errorf("error %q names a later error, not the first", err)
	}
}

// With no failed draw the error says the world was never drawn.
func TestCheckNoFrameNeverDrawn(t *testing.T) {
	c := checkStats{ents: &gameWorld{}}
	err := c.report(&bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "never drawn") {
		t.Errorf("got %v", err)
	}
}
