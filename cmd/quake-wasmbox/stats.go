// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

//go:build js && wasm

package main

import (
	"encoding/json"
	"syscall/js"
	"time"

	"github.com/go-quake1/engine/render"
)

// statsWindow is how often the frame-timing summary is logged (and the
// window restarted). Each log line is one measurement window.
const statsWindow = 10 * time.Second

// statsCapacity bounds the frames one window keeps (a 10 s window at
// 200 fps; older frames are dropped first).
const statsCapacity = 2048

// jsRendererKind reads the renderer setting the worker may publish on
// self.__quake_renderer ("soft" or "gpu"). Absent or not a string
// means the default.
func jsRendererKind() string {
	defer func() { _ = recover() }()
	v := js.Global().Get("__quake_renderer")
	if v.Type() != js.TypeString {
		return ""
	}
	return v.String()
}

// frameStats measures every frame and makes the numbers readable in
// the page without a screenshot:
//
//   - self.__quakeStats() returns the current window's summary as JSON;
//   - self.__quakeStatsReset() starts a new window;
//   - every statsWindow a "frame stats" line with the window's summary
//     goes to the worker console, then a new window starts.
//
// The summary is [render.Summary]: fps, frame-interval and work
// distributions, and the per-stage (sim, world, models, 2d, present)
// distributions, in milliseconds.
type frameStats struct {
	timer    *render.FrameTimer
	renderer string
	w, h     int
}

type statsReport struct {
	Renderer string `json:"renderer"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
	render.Summary
}

func (s *frameStats) json() string {
	b, err := json.Marshal(statsReport{
		Renderer: s.renderer, Width: s.w, Height: s.h,
		Summary: render.Summarize(s.timer.Samples()),
	})
	if err != nil {
		return `{"error":` + jsonString(err.Error()) + `}`
	}
	return string(b)
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// newFrameStats builds the timer on the backend clock (performance.now)
// and installs the JS hooks and the periodic log.
func newFrameStats(now func() float64, renderer string, w, h int) (*frameStats, error) {
	timer, err := render.NewFrameTimer(now, statsCapacity)
	if err != nil {
		return nil, err
	}
	s := &frameStats{timer: timer, renderer: renderer, w: w, h: h}
	g := js.Global()
	g.Set("__quakeStats", js.FuncOf(func(js.Value, []js.Value) any { return s.json() }))
	g.Set("__quakeStatsReset", js.FuncOf(func(js.Value, []js.Value) any { s.timer.Reset(); return nil }))
	go func() {
		tick := time.NewTicker(statsWindow)
		defer tick.Stop()
		for range tick.C {
			if s.timer.Len() == 0 {
				continue
			}
			logf("frame stats %s", s.json())
			s.timer.Reset()
		}
	}()
	return s, nil
}
