// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

// Command _play is a throwaway NATIVE harness that drives the shared
// game package against a PPM-writing backend so we can confirm a REAL
// Quake level renders from the player view + responds to input WITHOUT
// a browser. It is not shipped; it exists for the development loop
// behind the playable-wasmbox work.
//
// Usage:
//
//	go run ./cmd/_play -frames 90 -out /tmp/play -move
//
// It opens the embedded pak0 (embedpak.OpenAsFS), builds a real session
// (game.Build), runs -frames ticks, dumps every Nth frame as a PPM, and
// for the second half (when -move is set) injects a "forward held" input
// snapshot so the dumped frames show the view moving.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/go-quake1/engine/backend"
	"github.com/go-quake1/engine/backend/ppm"
	"github.com/go-quake1/engine/embedpak"
	"github.com/go-quake1/engine/game"
	"github.com/go-quake1/engine/sound"
)

const (
	fbWidth  = 320
	fbHeight = 240
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "PLAY: FAIL", err)
		os.Exit(1)
	}
}

func run() error {
	frames := flag.Int("frames", 90, "number of ticks to run")
	dumpEvery := flag.Int("every", 15, "dump every Nth frame as PPM")
	outPrefix := flag.String("out", "/tmp/play_", "PPM file prefix")
	move := flag.Bool("move", false, "inject forward-held input in the 2nd half")
	demoOrbit := flag.Bool("orbit", false, "demo-orbit camera (no input needed)")
	flag.Parse()

	pakFS, err := embedpak.OpenAsFS()
	if err != nil {
		return fmt.Errorf("embedpak.OpenAsFS: %w", err)
	}

	ppmBE, err := ppm.New(fbWidth, fbHeight, ppm.NumberedFileFactory(*outPrefix, "ppm", 4))
	if err != nil {
		return fmt.Errorf("ppm.New: %w", err)
	}
	// Wrap so we can inject scripted input snapshots + skip writing
	// non-dumped frames (the PPM backend writes on every PresentFrame).
	be := &scriptedBackend{ppm: ppmBE}

	sess, err := game.Build(pakFS, be, game.Options{
		Map:       "start",
		DemoOrbit: *demoOrbit,
	})
	if err != nil {
		return fmt.Errorf("game.Build: %w", err)
	}
	fmt.Printf("PLAY: session built -- realHost=%v playerSlot=%d\n", sess.Host != nil, sess.PlayerSlot)

	const dt = float32(1.0 / 20.0)
	dumpIdx := 0
	for f := 0; f < *frames; f++ {
		// Inject forward movement for the second half when -move.
		// The runloop tracks held keys via edge events: a KeyW DOWN
		// sets the held bit which stays latched until a KeyUp, so we
		// only emit the DOWN edge on the first movement frame and feed
		// a small mouse-yaw drift each tic so the shot is unmistakably
		// moving.
		be.snap = backend.InputSnapshot{}
		if *move && f >= *frames/2 {
			if f == *frames/2 {
				be.snap.KeysDown = []backend.KeyCode{backend.KeyW}
			}
			be.snap.MouseDX = 4 // slow yaw drift
		}
		be.wantDump = (f%*dumpEvery == 0) || (f == *frames-1)

		now := float32(f) * dt
		if err := sess.Runner.RunFrame(dt, now); err != nil {
			return fmt.Errorf("RunFrame(%d): %w", f, err)
		}
		if be.wantDump {
			dumpIdx++
		}
		if f < 6 || f%*dumpEvery == 0 {
			ents := 0
			if sess.Client != nil {
				ents = len(sess.Client.Entities)
			}
			origin := [3]float32{}
			if sess.Client != nil {
				if es, ok := sess.Client.Entities[sess.Client.PlayerNum]; ok {
					origin = es.Origin
				}
			}
			fmt.Printf("PLAY: tic %d -- conn=%d entities=%d playerOrigin=%v viewAngles=%v\n",
				f, sessConn(sess), ents, origin, sess.Runner.ViewAngles)
		}
	}
	fmt.Printf("PLAY: done -- %d frames run, %d PPM dumps written (prefix=%s)\n", *frames, dumpIdx, *outPrefix)
	return nil
}

func sessConn(s *game.Session) int {
	if s.Client == nil {
		return -1
	}
	return int(s.Client.Connection)
}

// scriptedBackend wraps the PPM backend so the harness can (a) inject a
// scripted InputSnapshot per tic and (b) only emit a PPM on the frames
// it wants dumped. The PPM backend's PresentFrame always writes, so we
// gate it on wantDump and forward to a no-op otherwise.
type scriptedBackend struct {
	ppm      *ppm.Backend
	snap     backend.InputSnapshot
	wantDump bool
	tick     int
}

func (b *scriptedBackend) PresentFrame(rgba []byte, w, h int) error {
	if !b.wantDump {
		return nil
	}
	return b.ppm.PresentFrame(rgba, w, h)
}

func (b *scriptedBackend) Size() (int, int) { return fbWidth, fbHeight }

func (b *scriptedBackend) QueueAudio(s []sound.StereoSample) error { return nil }
func (b *scriptedBackend) SampleRate() int                         { return 22050 }

func (b *scriptedBackend) PollInput() (backend.InputSnapshot, error) {
	return b.snap, nil
}

func (b *scriptedBackend) Now() float64 {
	t := float64(b.tick) * (1.0 / 20.0)
	b.tick++
	return t
}

var _ backend.Backend = (*scriptedBackend)(nil)
var _ = io.EOF
