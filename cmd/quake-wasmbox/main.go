// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

//go:build js && wasm

// quake-wasmbox is the wasmbox-external-client entry point for the
// Go-native Quake engine. It is a sibling of cmd/quake-wasm: same
// engine wiring, same synthbsp fallback when no real pak is available,
// same loopback host -- but the presentation surface is a wasmbox-
// protocol SharedArrayBuffer + the `{type:"commit"}` postMessage rather
// than a DOM canvas.
//
// The wasm runs inside a Web Worker. The wasmbox compositor (on the
// main thread) owns the desktop canvas + stacking + focus + input
// routing; our backend.Backend talks to it via the step-B wire protocol
// documented at github.com/wasmdesk/wasmbox/docs/protocol.md.
//
// 2026-06-28: when the OCI-streamed pak0 successfully reaches
// maps/start.bsp, we hand off to runner.Setup -- the SAME shared
// real-pak bring-up tamago uses (host + client + loopback + sound pool +
// music + particles + alias preload + sbar + menu + attract demo). When
// the pak is absent / corrupt / placeholder, we fall back to the synth
// renderer that has shipped here since first bring-up.
package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strings"
	"sync/atomic"
	"syscall/js"
	"time"

	"github.com/go-quake1/engine/assets"
	"github.com/go-quake1/engine/backend/wasmbox"
	"github.com/go-quake1/engine/bspfile"
	"github.com/go-quake1/engine/bspfile/synthbsp"
	"github.com/go-quake1/engine/bsprender"
	"github.com/go-quake1/engine/client"
	"github.com/go-quake1/engine/embedpak"
	"github.com/go-quake1/engine/model"
	"github.com/go-quake1/engine/ociassets"
	"github.com/go-quake1/engine/pak"
	"github.com/go-quake1/engine/render"
	"github.com/go-quake1/engine/runloop"
	"github.com/go-quake1/engine/runner"
	engineserver "github.com/go-quake1/engine/server"
	"github.com/go-quake1/engine/vfs"
)

// OCIReference is the wasmbox-build twin of cmd/quake-wasm's
// OCIReference. Same plumbing: set at link time to stream pak +
// music from an OCI registry instead of relying on the embedded
// placeholders.
var OCIReference = ""

// jsGlobalFBSize reads the (width, height) the worker.js shim wrote
// into self.__quake_fb_w / __quake_fb_h before importing quake.wasm.
// Missing / non-integer / zero values come back as (0, 0) so chooseFB
// can fold them into the default. A panic anywhere in the js.Value
// dance (host page intentionally cleared the globals between
// importScripts() + go.run, missing js.Global, etc.) is treated as
// "no JS dims available" so the engine still boots at the default.
func jsGlobalFBSize() (int, int) {
	defer func() { _ = recover() }()
	g := js.Global()
	w := g.Get("__quake_fb_w")
	h := g.Get("__quake_fb_h")
	if w.Type() != js.TypeNumber || h.Type() != js.TypeNumber {
		return 0, 0
	}
	return w.Int(), h.Int()
}

// stubHost satisfies [runloop.HostFramer] for the synth-only fallback.
type stubHost struct{}

func (stubHost) Frame(_ float32) error { return nil }

func main() {
	if err := run(); err != nil {
		fmt.Println("QUAKE: FAIL", err)
		// Block on an empty channel so the JS-side wasm instance stays
		// alive long enough for the console message to flush + so DOM
		// event handlers retain their js.Func references.
		<-make(chan struct{})
		return
	}
	fmt.Println("QUAKE: exited cleanly")
}

// run is main's testability seam. It returns errors instead of halting
// so the worker console carries the failure reason; main then blocks
// on receipt.
func run() error {
	// 1. Pick the framebuffer dims. worker.js (the JS shim that loaded
	//    this wasm) reads localStorage["wasmbox.layout"] BEFORE go.run
	//    and publishes the chosen surface size on two globals; chooseFB
	//    folds those + the defaults + the [min..max] clamps into the
	//    final (w, h). Native-rendering at the saved window size means
	//    the compositor no longer has to scale-fit a 320x240 SAB up to
	//    a 900x700 frame, eliminating a costly per-pixel upscale + the
	//    aliasing that came with it.
	jsW, jsH := jsGlobalFBSize()
	fbWidth, fbHeight := chooseFB(jsW, jsH)
	logf("fb dims: js=(%d,%d) chosen=%dx%d", jsW, jsH, fbWidth, fbHeight)

	// 2. Handshake with the wasmbox compositor + build the backend.
	be, err := wasmbox.NewClient("quake (wasm)", fbWidth, fbHeight)
	if err != nil {
		return fmt.Errorf("wasmbox.NewClient: %w", err)
	}
	// The compositor MAY clamp the requested dims (e.g. MIN_W / MIN_H).
	// Trust the granted size from here on so the loading-panel paint,
	// the run-loop FBWidth/FBHeight + PresentFrame stride all agree.
	fbWidth, fbHeight = be.Size()
	logf("backend up -- wasmbox surface=%dx%d", fbWidth, fbHeight)

	// 3. OCI streaming first (when -ldflags '-X main.OCIReference=...'
	//    has been set at build time). The OCI FS exposes top-level entries
	//    named pak0.pak + music/track*.ogg. The pak0.pak entry is the .pak
	//    archive itself -- we need to fetch its bytes + reparse via
	//    pak.Open so the inner "maps/start.bsp" path becomes resolvable.
	//    The music/ entries are passthrough fs.FS reads that the runner's
	//    MusicOpen closure tries from the OCI overlay first, then the
	//    embedmusic fallback.
	var (
		pakFS   fs.FS // the real .pak archive (pak.FS) -- exposes maps/start.bsp etc.
		musicFS fs.FS // optional music overlay (ociFS for OCI builds, nil otherwise)
		ociErr  error
	)
	if OCIReference != "" {
		ociFS, err := openOCIAssets(OCIReference)
		if err != nil {
			ociErr = err
			logf("ociassets: %v -- falling back to embedpak", err)
		} else {
			logf("ociassets: streaming pak + music from %s", OCIReference)
			musicFS = ociFS
			// Wire the progress sink BEFORE kicking the fetch goroutine.
			// The renderer goroutine reads the latest received/total via
			// atomic loads + paints a centred bar 20x/s until the fetch
			// completes (or fails).
			var (
				received int64
				total    int64
				done     atomic.Bool
			)
			if ociFSImpl, ok := ociFS.(*ociassets.FS); ok {
				ociFSImpl.SetProgress(func(_, _ string, r, tot int64) {
					atomic.StoreInt64(&received, r)
					if tot > 0 {
						atomic.StoreInt64(&total, tot)
					}
				})
				// Force a JS-event-loop yield every 1 MiB of body so the
				// paint goroutine keeps ticking on Firefox -- without it
				// Firefox's worker fetch can deliver multi-MB chunks to
				// wasm net/http without an intervening event-loop tick
				// and the loading bar appears frozen until the fetch
				// completes.
				ociFSImpl.SetYieldEvery(1 << 20)
			}
			// Reusable RGBA backing the paint helper writes into. Allocated
			// once -- repainted in place every frame so we don't churn the
			// wasm GC during the fetch.
			rgba := make([]byte, fbWidth*fbHeight*4)
			renderStart := time.Now()
			renderDone := make(chan struct{})
			go func() {
				defer close(renderDone)
				tick := time.NewTicker(50 * time.Millisecond)
				defer tick.Stop()
				for {
					if done.Load() {
						return
					}
					r := atomic.LoadInt64(&received)
					tot := atomic.LoadInt64(&total)
					// animPhase carries the wall-clock seconds since the
					// fetch started so paintLoadingBarAt can drive the
					// shine sweep -- the user sees motion even when
					// Firefox's fetch releases body bytes in multi-MB
					// bursts that leave `received` frozen for seconds at
					// a time.
					animPhase := time.Since(renderStart).Seconds()
					paintLoadingBarAt(rgba, fbWidth, fbHeight, r, tot, animPhase)
					if err := be.PresentFrame(rgba, fbWidth, fbHeight); err != nil {
						logf("loading-bar PresentFrame: %v", err)
					}
					<-tick.C
				}
			}()

			// Fetch pak0.pak from the OCI overlay + reparse as a pak.FS.
			// This blocks on the HTTP GET (~180 MB on localhost ~= 2 s);
			// runs in a goroutine with a 60s timeout so a hung registry
			// doesn't trap the worker.
			fetchDone := make(chan fetchPakResult, 1)
			go func() { fetchDone <- fetchPakFromOCI(ociFS) }()
			select {
			case res := <-fetchDone:
				if res.err != nil {
					logf("ociassets: pak0.pak fetch/parse failed: %v -- falling back", res.err)
				} else {
					pakFS = res.fs
					logf("ociassets: pak0.pak parsed -- %d entries (size=%d bytes)", res.entries, res.size)
				}
			case <-time.After(60 * time.Second):
				logf("ociassets: pak0.pak fetch timeout after 60s -- falling back")
			}
			// 2026-06-28: hold the panel on screen for at least
			// minLoadingPanelVisible so an instant cache hit doesn't blow
			// past the loading state in a single frame. Without this the
			// user sees a 50 ms flash and then the game; with it the panel
			// is on screen long enough to read.
			if wait := loadingPanelHoldFor(renderStart, time.Now(), minLoadingPanelVisible); wait > 0 {
				logf("ociassets: holding loading panel for %v (fetch finished early)", wait)
				time.Sleep(wait)
			}
			done.Store(true)
			<-renderDone
		}
	}

	// 4. Embedded pak fallback (placeholder = 12 bytes -> error
	//    -> drop through to the synthetic-asset path).
	if pakFS == nil {
		fsys, pakErr := embedpak.OpenAsFS()
		if pakErr != nil {
			logf("embedpak.OpenAsFS: %v -- using synthetic assets + synthbsp", pakErr)
		} else {
			pakFS = fsys
		}
	}

	// 5. Probe for a real BSP. With pak0.pak materialised in step 3 the
	//    probe is a straight in-memory lookup; with the embedpak
	//    placeholder it will fail (the placeholder has no entries).
	isReal := false
	if pakFS != nil {
		f, err := pakFS.Open("maps/start.bsp")
		if err != nil {
			logf("real BSP probe failed: %v -- falling back to synth", err)
		} else {
			f.Close()
			isReal = true
			logf("real BSP probe ok -- maps/start.bsp is resolvable")
		}
	}
	_ = ociErr // ociErr already logged above; keep var for clarity.

	if isReal {
		// 6a. Real-pak path: hand off to the shared runner. MusicOverlayFS
		// = the OCI manifest FS so music/track*.ogg layers stream from the
		// registry (pak0.pak does NOT carry music).
		r, err := runner.Setup(runner.SetupOpts{
			Backend:                     be,
			PakFS:                       pakFS,
			MusicOverlayFS:              musicFS,
			FBWidth:                     fbWidth,
			FBHeight:                    fbHeight,
			MapSlug:                     "start",
			Logf:                        logf,
			DemoOrbit:                   true,
			DemoOrbitAutoDisableOnInput: true,
			// The attract-loop demo stream desyncs this SvcReader (its
			// recorded opcodes aren't all decoded yet), which flashes the
			// world in + out at the menu on the wasmbox client. Off until
			// the demo opcode coverage lands.
			DisableAttractDemo: true,
		})
		if err != nil {
			logf("runner.Setup failed: %v -- falling back to synth", err)
		} else {
			r.ShowFPS = true // bring-up perf HUD (top-left)
			logf("real-pak bring-up live -- entering RunUntilQuit")
			return r.RunUntilQuit()
		}
	}

	// 6b. Synth fallback. The original wasmbox bring-up path: hand-built
	//     VFS over synthetic palette/colormap/conchars + an in-process
	//     synthbsp renderer.
	return runSynth(be, pakFS)
}

// runSynth is the synth-only fallback path. Kept here (rather than
// shared) because the synth-only bring-up is wasmbox-specific: tamago
// has a real-pak host always and never needs this branch.
func runSynth(be *wasmbox.Backend, pakFS fs.FS) error {
	v := vfs.New()
	v.Add(syntheticAssets())
	if pakFS != nil {
		v.Add(pakFS)
		// Goroutine-launched OCI prefetch so a 180 MB pak0 fetch
		// doesn't block backend bring-up.
		go prefetchOCIAssets(pakFS)
	}

	cli, _ := engineserver.NewLoopbackConn()
	clientState := client.NewState()

	r, err := runloop.NewRunnerFromVFS(runloop.SetupOpts{
		VFS:            v,
		Host:           stubHost{},
		Client:         clientState,
		Conn:           cli,
		Backend:        be,
		BackgroundIdx:  0x20,
		NotifyLifetime: 3,
		MaxNotifyRows:  4,
	})
	if err != nil {
		return fmt.Errorf("NewRunnerFromVFS: %w", err)
	}
	r.Console.Print("PURE-GO QUAKE 1 -- wasmbox synth bring-up\n")
	r.Console.Print("backend/wasmbox: SAB + protocol commits\n")
	logf("synth runner up -- entering RunUntilQuit")

	if err := setupSynthRenderer(r); err != nil {
		logf("setupSynthRenderer skipped: %v -- 2D-only fallback", err)
	}

	r.ShowFPS = true // bring-up perf HUD (top-left)
	return r.RunUntilQuit()
}

// setupSynthRenderer wires a Pre2DDraw closure that paints the synthbsp
// BuildWithFaces scene.
func setupSynthRenderer(r *runloop.Runner) error {
	bspBytes, size, err := synthbsp.BuildWithFaces()
	if err != nil {
		return fmt.Errorf("synthbsp.BuildWithFaces: %w", err)
	}
	file, err := bspfile.Open(newReaderAt(bspBytes), size)
	if err != nil {
		return fmt.Errorf("bspfile.Open: %w", err)
	}
	bm, err := model.LoadBrush(file, 0)
	if err != nil {
		return fmt.Errorf("model.LoadBrush: %w", err)
	}
	faces, err := file.Faces()
	if err != nil {
		return fmt.Errorf("file.Faces: %w", err)
	}

	walkCtx := bsprender.NewWalkContext(bm)
	allFaceIdx := make([]int, len(faces))
	for i := range allFaceIdx {
		allFaceIdx[i] = i
	}
	walkCtx.LeafFaces = func(id int) []int {
		if walkCtx.NodeKind(id) == bsprender.NodeKindLeaf {
			return allFaceIdx
		}
		return nil
	}
	walkCtx.NodeKind = func(id int) bsprender.NodeKind {
		if id < walkCtx.NumNodes {
			return bsprender.NodeKindInterior
		}
		leafIdx := id - walkCtx.NumNodes
		if leafIdx < 0 || leafIdx >= bm.TotalLeaves() {
			return bsprender.NodeKindEmpty
		}
		if bm.Leaf(leafIdx).Contents == bspfile.ContentsSolid {
			return bsprender.NodeKindEmpty
		}
		return bsprender.NodeKindLeaf
	}
	const bigF = float32(1e6)
	walkCtx.NodeBBox = func(id int) (mins, maxs [3]float32) {
		return [3]float32{-bigF, -bigF, -bigF}, [3]float32{bigF, bigF, bigF}
	}

	checker := makeCheckerTex(16)
	var cm render.ColorMap
	for light := 0; light < render.ColorMapRows; light++ {
		for src := 0; src < render.ColorMapCols; src++ {
			cm[light][src] = byte(src)
		}
	}
	camOrigin := [3]float32{5, 5, 20}
	const (
		fovX     = 90.0
		camPitch = 80.0
	)

	var surfaces bsprender.SurfaceList
	frameCount := 0
	r.Pre2DDraw = func(fb *render.FrameBuffer, viewOrigin, viewAngles [3]float32) error {
		frame := frameCount
		frameCount++
		viewAngles = [3]float32{camPitch, float32(frame % 360), 0}
		viewOrigin = camOrigin

		for i := range fb.Pixels {
			fb.Pixels[i] = 0x10
		}

		rd := &render.RefDef{
			VRect:      render.VRect{Width: fb.Width, Height: fb.Height},
			ViewAngles: viewAngles,
			ViewOrigin: viewOrigin,
			FovX:       fovX,
			FovY:       fovX,
		}
		view := rd.SetupView()
		frustum := rd.BuildFrustum()
		stampFrame := int32(frame + 1)

		for n := 0; n < bm.NumNodes(); n++ {
			bm.SetNodeVisFrame(n, stampFrame)
		}
		for l := 0; l < bm.TotalLeaves(); l++ {
			bm.Leaf(l).VisFrame = stampFrame
		}

		surfaces.Reset()
		if err := bsprender.WalkWorld(walkCtx, 0, rd.ViewOrigin, frustum, stampFrame, &surfaces); err != nil {
			return fmt.Errorf("WalkWorld: %w", err)
		}
		for i := 0; i < surfaces.Len(); i++ {
			ref := surfaces.Refs[i]
			fv, err := bsprender.NewBrushFaceVerts(bm, ref.FaceIdx)
			if err != nil {
				continue
			}
			verts, err := bsprender.TransformFace(view, fb, fovX, fv)
			if err != nil {
				continue
			}
			_ = render.FillTexturedPolygon(fb, checker, &cm, 0, verts)
		}
		return nil
	}
	return nil
}

func makeCheckerTex(n int) *render.Pic {
	pixels := make([]byte, n*n)
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			tile := ((x / 4) + (y / 4)) & 3
			var idx byte
			switch tile {
			case 0:
				idx = 0
			case 1:
				idx = 15
			case 2:
				idx = 31
			default:
				idx = 47
			}
			pixels[y*n+x] = idx
		}
	}
	return &render.Pic{Width: n, Height: n, Pixels: pixels}
}

func syntheticAssets() fs.FS {
	return memFS{
		"gfx/palette.lmp":  makePaletteLump(),
		"gfx/colormap.lmp": makeColorMapLump(),
		"gfx/conchars.lmp": makeConcharsLump(),
	}
}

func makePaletteLump() []byte {
	buf := make([]byte, render.PaletteLumpSize)
	for i := 0; i < 256; i++ {
		base := byte(i)
		switch i % 6 {
		case 0:
			buf[i*3+0] = base
			buf[i*3+1] = base / 4
			buf[i*3+2] = base / 4
		case 1:
			buf[i*3+0] = base
			buf[i*3+1] = base
			buf[i*3+2] = base / 4
		case 2:
			buf[i*3+0] = base / 4
			buf[i*3+1] = base
			buf[i*3+2] = base / 4
		case 3:
			buf[i*3+0] = base / 4
			buf[i*3+1] = base
			buf[i*3+2] = base
		case 4:
			buf[i*3+0] = base / 4
			buf[i*3+1] = base / 4
			buf[i*3+2] = base
		case 5:
			buf[i*3+0] = base
			buf[i*3+1] = base / 4
			buf[i*3+2] = base
		}
	}
	buf[0x10*3+0] = 48
	buf[0x10*3+1] = 96
	buf[0x10*3+2] = 176
	for k, rgb := range [...][3]byte{
		{255, 64, 64},
		{255, 200, 32},
		{32, 200, 255},
		{200, 32, 200},
	} {
		i := []int{0, 15, 31, 47}[k]
		buf[i*3+0] = rgb[0]
		buf[i*3+1] = rgb[1]
		buf[i*3+2] = rgb[2]
	}
	return buf
}

func makeColorMapLump() []byte {
	buf := make([]byte, render.ColorMapRows*render.ColorMapCols)
	for i := range buf {
		buf[i] = byte(i)
	}
	return buf
}

func makeConcharsLump() []byte {
	buf := make([]byte, assets.ConCharsLumpSize)
	for i := range buf {
		buf[i] = 0
	}
	return buf
}

type memFS map[string][]byte

func (m memFS) Open(name string) (fs.File, error) {
	data, ok := m[name]
	if !ok {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	return &memFile{name: name, data: data}, nil
}

type memFile struct {
	name string
	data []byte
	pos  int
}

func (f *memFile) Read(p []byte) (int, error) {
	if f.pos >= len(f.data) {
		return 0, io.EOF
	}
	n := copy(p, f.data[f.pos:])
	f.pos += n
	return n, nil
}

func (f *memFile) Stat() (fs.FileInfo, error) {
	return &memFileInfo{name: f.name, size: int64(len(f.data))}, nil
}

func (f *memFile) Close() error { return nil }

type memFileInfo struct {
	name string
	size int64
}

func (i *memFileInfo) Name() string       { return i.name }
func (i *memFileInfo) Size() int64        { return i.size }
func (i *memFileInfo) Mode() fs.FileMode  { return 0o444 }
func (i *memFileInfo) ModTime() time.Time { return time.Time{} }
func (i *memFileInfo) IsDir() bool        { return false }
func (i *memFileInfo) Sys() any           { return nil }

type readerAt struct {
	data []byte
	size int64
}

func newReaderAt(b []byte) *readerAt { return &readerAt{data: b, size: int64(len(b))} }

func (r *readerAt) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 || off >= r.size {
		return 0, io.EOF
	}
	n := copy(p, r.data[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

// logf is fmt.Printf scoped to a QUAKE: prefix.
func logf(format string, args ...any) {
	fmt.Printf("QUAKE: "+format+"\n", args...)
}

// prefetchOCIAssets opens the well-known pak + music names so
// ociassets.FS.Open lazily fetches each layer.
func prefetchOCIAssets(pakFS fs.FS) {
	defer func() {
		if r := recover(); r != nil {
			logf("ociassets: prefetch PANIC: %v", r)
		}
	}()
	time.Sleep(50 * time.Millisecond)
	logf("ociassets: prefetch starting (pakFS=%T)", pakFS)
	names := []string{}
	for n := 2; n <= 11; n++ {
		names = append(names, fmt.Sprintf("music/track%02d.ogg", n))
	}
	names = append(names, "pak0.pak")
	for _, name := range names {
		t0 := time.Now()
		time.Sleep(1 * time.Millisecond)
		logf("ociassets: prefetch %s opening...", name)
		file, err := pakFS.Open(name)
		logf("ociassets: prefetch %s open returned err=%v", name, err)
		if err != nil {
			time.Sleep(10 * time.Millisecond)
			continue
		}
		n, _ := io.Copy(io.Discard, file)
		file.Close()
		logf("ociassets: prefetched %s (%d bytes in %.2fs)", name, n, time.Since(t0).Seconds())
		time.Sleep(10 * time.Millisecond)
	}
	logf("ociassets: prefetch complete")
}

// fetchPakResult bundles the outcome of fetchPakFromOCI so the timeout
// select-case stays cheap.
type fetchPakResult struct {
	fs      fs.FS
	entries int
	size    int
	err     error
}

// fetchPakFromOCI opens "pak0.pak" through the OCI manifest-FS, drains it
// into memory, and reparses the bytes via pak.Open. The returned fs.FS is
// a *pak.FS that exposes the inner directory layout ("maps/start.bsp",
// "progs.dat", "gfx/palette.lmp", "progs/*.mdl", etc.) -- which is what
// the shared runner.Setup pipeline (loadBSP, buildHost, loadMiptexPics)
// expects.
//
// The fetch is synchronous and blocks on the underlying HTTP GET (~180 MB
// pak0 on localhost lands in ~2 s; the registry plus chromium-streaming
// path is the load-bearing piece). Caller wraps the call in a goroutine
// + select for the timeout guard.
func fetchPakFromOCI(ociFS fs.FS) fetchPakResult {
	f, err := ociFS.Open("pak0.pak")
	if err != nil {
		return fetchPakResult{err: fmt.Errorf("open pak0.pak: %w", err)}
	}
	defer f.Close()
	blob, err := io.ReadAll(f)
	if err != nil {
		return fetchPakResult{err: fmt.Errorf("read pak0.pak: %w", err)}
	}
	if len(blob) <= 12 {
		return fetchPakResult{err: fmt.Errorf("pak0.pak too small (%d bytes)", len(blob))}
	}
	pfs, err := pak.Open(bytes.NewReader(blob))
	if err != nil {
		return fetchPakResult{err: fmt.Errorf("pak.Open: %w", err)}
	}
	return fetchPakResult{fs: pfs, entries: pfs.Len(), size: len(blob)}
}

// openOCIAssets mirrors cmd/quake-wasm/main.go's helper.
func openOCIAssets(reference string) (fs.FS, error) {
	ref, err := ociassets.ParseReference(reference)
	if err != nil {
		return nil, fmt.Errorf("parse %q: %w", reference, err)
	}
	origin := ref.Origin
	if origin == "" {
		// Same-origin reference ("/repo:tag"): the wasmbox quake worker.js
		// publishes the site base URL (origin + any base path, e.g.
		// https://user.github.io/project) on globalThis.__quakeOCIBase before
		// importing this wasm. The mirror lives at <base>/v2/<repo>, so a
		// GitHub-Pages sub-path deploy is reachable (a fixed <scheme>://<host>
		// origin would drop the /project/ prefix and 404).
		origin = jsQuakeOCIBase()
		if origin == "" {
			return nil, fmt.Errorf("same-origin reference %q but globalThis.__quakeOCIBase is unset", reference)
		}
	}
	c := ociassets.NewClient(origin)
	return ociassets.NewFSFromManifest(context.Background(), c, ref.Repo, ref.Tag)
}

// jsQuakeOCIBase reads the same-origin OCI base URL the wasmbox quake
// worker.js publishes on globalThis.__quakeOCIBase (origin + any base path,
// no trailing slash). Returns "" -- so the caller errors cleanly -- when the
// global is unset or not a string. Any panic in the js.Value dance is
// swallowed the way jsGlobalFBSize treats a missing host global.
func jsQuakeOCIBase() string {
	defer func() { _ = recover() }()
	v := js.Global().Get("__quakeOCIBase")
	if v.Type() != js.TypeString {
		return ""
	}
	return strings.TrimRight(v.String(), "/")
}

var _ = errors.New
