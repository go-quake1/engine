// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/go-quake1/engine/render"
	"github.com/go-quake1/engine/worldview"
)

func TestStatsOf(t *testing.T) {
	ms := time.Millisecond
	s := statsOf(90*ms, []time.Duration{5 * ms, 1 * ms, 3 * ms, 100 * ms})
	if s.Warmup != 90*ms || s.Frames != 4 || s.Min != ms || s.Max != 100*ms || s.Median != 4*ms || s.Mean != 27250*time.Microsecond {
		t.Errorf("%+v", s)
	}
	if s = statsOf(ms, []time.Duration{7 * ms, 1 * ms, 3 * ms}); s.Median != 3*ms {
		t.Errorf("odd median %v", s.Median)
	}
	if s = statsOf(ms, nil); s.Frames != 0 || s.Mean != 0 {
		t.Errorf("no frames: %+v", s)
	}
}

// TestTimeFramesWarmupApart: the first of frames+1 draws is the warm-up and is
// not in the mean.
func TestTimeFramesWarmupApart(t *testing.T) {
	n := 0
	s, err := timeFrames(3, func() error {
		n++
		if n == 1 {
			time.Sleep(60 * time.Millisecond)
		}
		return nil
	})
	if err != nil || n != 4 || s.Frames != 3 || s.Warmup < 50*time.Millisecond || s.Max > 30*time.Millisecond {
		t.Errorf("n %d, %+v, %v", n, s, err)
	}
	boom := errors.New("boom")
	if _, err := timeFrames(2, func() error { return boom }); !errors.Is(err, boom) {
		t.Errorf("error: %v", err)
	}
}

func TestTimeGPUOpensOnceAndCloses(t *testing.T) {
	closes := 0
	opens := 0
	old := openGPUWorld
	openGPUWorld = func(*worldview.World, *render.ColorMap, int, int) (gpuDrawer, error) {
		opens++
		return fakeGPU{frame: make([]byte, 4), closed: &closes}, nil
	}
	t.Cleanup(func() { openGPUWorld = old })
	var out bytes.Buffer
	if err := timeGPU(&out, nil, nil, worldview.View{}, 4, 2, 2, 5); err != nil {
		t.Fatal(err)
	}
	if opens != 1 || closes != 1 || !strings.Contains(out.String(), "view 4 2x2 gpuworld frames: warmup ") || !strings.Contains(out.String(), "then 5 frames: mean ") {
		t.Errorf("opens %d closes %d: %q", opens, closes, out.String())
	}
	openGPUWorld = nil
	if err := timeGPU(&out, nil, nil, worldview.View{}, 4, 2, 2, 5); !errors.Is(err, ErrNoGPUWorld) {
		t.Errorf("no device: %v", err)
	}
}
