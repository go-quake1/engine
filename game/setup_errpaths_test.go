// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package game

import (
	"encoding/binary"
	"testing"

	"github.com/go-quake1/engine/client"
	"github.com/go-quake1/engine/render"
	"github.com/go-quake1/engine/runloop"
	engineserver "github.com/go-quake1/engine/server"
	"github.com/go-quake1/engine/vfs"
)

// newBareRunner builds a minimal runloop.Runner over the synthetic VFS
// (no host) so setupRenderer can be exercised with crafted configs.
func newBareRunner(t *testing.T) *runloop.Runner {
	t.Helper()
	v := vfs.New()
	v.Add(syntheticAssets())
	be := &scriptBackend{w: 64, h: 48}
	conn, _ := engineserver.NewLoopbackConn()
	runner, err := runloop.NewRunnerFromVFS(runloop.SetupOpts{
		VFS:           v,
		Host:          stubHost{},
		Client:        client.NewState(),
		Conn:          conn,
		Backend:       be,
		BackgroundIdx: 0x20,
		SoundChannels: 8,
	})
	if err != nil {
		t.Fatalf("NewRunnerFromVFS: %v", err)
	}
	runner.ParticlePool = render.NewPool()
	return runner
}

// TestSetupRendererBSPOpenErr drives setupRenderer with a pakFS whose
// maps/start.bsp is garbage so bspfile.Open fails inside setupRenderer.
func TestSetupRendererBSPOpenErr(t *testing.T) {
	runner := newBareRunner(t)
	cfg := rendererConfig{
		pakFS:          memFS{"maps/start.bsp": []byte("not a bsp at all, but long enough to look like one ............")},
		fov:            90,
		particleRNG:    newLCGByteSource(1),
		tempSpritePool: client.NewTempSpritePool(),
		beamPool:       client.NewBeamPool(),
	}
	if err := setupRenderer(runner, cfg); err == nil {
		t.Fatal("garbage BSP must surface a setupRenderer error")
	}
}

// corruptLumpLen returns a copy of b with lump li's length field made an
// odd byte count (not a multiple of the element size) so the typed
// decoder for that lump rejects it. Lump li's lump_t sits at byte
// 4+8*li (offset int32 + length int32).
func corruptLumpLen(b []byte, li int) []byte {
	c := append([]byte(nil), b...)
	lp := 4 + 8*li
	ln := binary.LittleEndian.Uint32(c[lp+4 : lp+8])
	binary.LittleEndian.PutUint32(c[lp+4:lp+8], ln-1)
	return c
}

// TestSetupRendererMidPipelineErrors covers setupRenderer's LoadBrush,
// Faces, and loadMiptexPics error returns by feeding it a real start.bsp
// with one lump surgically corrupted so the corresponding typed decode
// fails after bspfile.Open succeeds.
func TestSetupRendererMidPipelineErrors(t *testing.T) {
	base := realStartBSP(t)

	cases := []struct {
		name string
		bsp  []byte
	}{
		// Lump 5 (nodes) -> model.LoadBrush fails.
		{"LoadBrush", corruptLumpLen(base, 5)},
		// Lump 7 (faces) -> file.Faces fails.
		{"Faces", corruptLumpLen(base, 7)},
		// Lump 2 (textures): a huge NumMipTex makes loadMiptexPics fail.
		{"loadMiptex", func() []byte {
			c := append([]byte(nil), base...)
			tofs := texturesLumpOfs(c)
			binary.LittleEndian.PutUint32(c[tofs:tofs+4], 0x7FFFFFFF)
			return c
		}()},
	}
	for _, tc := range cases {
		runner := newBareRunner(t)
		cfg := rendererConfig{
			pakFS:          memFS{"maps/start.bsp": tc.bsp},
			fov:            90,
			particleRNG:    newLCGByteSource(1),
			tempSpritePool: client.NewTempSpritePool(),
			beamPool:       client.NewBeamPool(),
		}
		if err := setupRenderer(runner, cfg); err == nil {
			t.Fatalf("%s: corrupt BSP must surface a setupRenderer error", tc.name)
		}
	}
}

// TestSynthPre2DDrawDirect drives the synthetic-BSP Pre2DDraw path
// directly (no host) so the synth walkCtx LeafFaces / NodeKind closures
// + the viewer-outside-leaf early return run.
func TestSynthPre2DDrawDirect(t *testing.T) {
	runner := newBareRunner(t)
	cfg := rendererConfig{
		pakFS:          nil, // -> synthbsp fallback inside setupRenderer
		fov:            90,
		particleRNG:    newLCGByteSource(1),
		tempSpritePool: client.NewTempSpritePool(),
		beamPool:       client.NewBeamPool(),
	}
	if err := setupRenderer(runner, cfg); err != nil {
		t.Fatalf("setupRenderer synth: %v", err)
	}
	fb, err := render.NewFrameBuffer(64, 48)
	if err != nil {
		t.Fatalf("fb: %v", err)
	}
	// In-scene camera so the synth walkCtx closures traverse leaves.
	for i := 0; i < 4; i++ {
		if err := runner.Pre2DDraw(fb, [3]float32{5, 5, 20}, [3]float32{0, float32(i * 30), 0}); err != nil {
			t.Fatalf("Pre2DDraw synth %d: %v", i, err)
		}
	}
}
