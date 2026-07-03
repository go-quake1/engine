// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

//go:build js && wasm

// quake-wasm is the browser-side entry point for the Go-native Quake
// engine. It mirrors the structure of quake-tamago/main.go but swaps
// the bare-metal virtio probe for the backend/wasm.NewDefault Canvas2D
// + DOMInput + WebAudio adapter set and drops the tamago-specific
// boot sequence (no PCI scan, no kernel halt).
//
// Scope: first browser bring-up. The engine is wired with a stub
// host + synthbsp fallback world, so the in-browser canvas paints
// the title menu + console + a synthetic-BSP scene. Real pak0 / real
// host follow in a later batch (the wasm Backend itself is already
// production-ready; the bottleneck is the cmd/-side glue to load
// real assets via fetch()).
//
// Loop driver: in a single-threaded GOOS=js GOARCH=wasm environment
// the Go scheduler cooperates with the JS event loop via runtime
// calls that yield (channel ops, time.Sleep). [runloop.Runner.RunUntilQuit]
// is blocking, but every per-frame call to Backend.PollInput drains
// the DOM event queue + every Backend.QueueAudio yields long enough
// for the browser to deliver the next batch of DOM events. The user
// closes the tab to exit; the engine also receives EventQuit via the
// beforeunload listener installed by backend/wasm.NewDOMInput.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"syscall/js"
	"time"

	"github.com/go-quake1/engine/assets"
	"github.com/go-quake1/engine/backend"
	"github.com/go-quake1/engine/backend/wasm"
	"github.com/go-quake1/engine/bspfile"
	"github.com/go-quake1/engine/bspfile/synthbsp"
	"github.com/go-quake1/engine/bsprender"
	"github.com/go-quake1/engine/client"
	"github.com/go-quake1/engine/embedpak"
	"github.com/go-quake1/engine/model"
	"github.com/go-quake1/engine/ociassets"
	"github.com/go-quake1/engine/render"
	"github.com/go-quake1/engine/runloop"
	engineserver "github.com/go-quake1/engine/server"
	"github.com/go-quake1/engine/vfs"
)

// OCIReference, when non-empty, points the wasm payload at an OCI
// Distribution v2 endpoint to fetch pak0.pak + the music tracks on
// demand instead of embedding them. Set at link time:
//
//	go build -ldflags "-X main.OCIReference=http://localhost:8080/v2-mock/quake-assets:latest" ./cmd/quake-wasm
//
// The empty default keeps the existing embed path live so unflagged
// builds keep working.
var OCIReference = ""

// fbWidth / fbHeight match the vanilla DOS Quake framebuffer so the
// software rasterizer's per-pixel work stays affordable in a wasm
// runtime. The HTML host page upscales via CSS image-rendering:
// pixelated for crisp browser-window display.
const (
	fbWidth  = 320
	fbHeight = 240
)

// stubHost satisfies [runloop.HostFramer] for the first bring-up.
// The real id-Software game-server tick lands in a follow-up batch;
// for now the loop just processes input + paints the world walk.
type stubHost struct{}

// Frame is a no-op: simulation arrives once a real pak is wired.
func (stubHost) Frame(_ float32) error { return nil }

func main() {
	if err := run(); err != nil {
		// In a browser context Println is captured by Go's wasm
		// runtime + relayed to console.error -- the user opens
		// DevTools to see the failure reason.
		fmt.Println("QUAKE: FAIL", err)
		// Block on an empty channel so the JS-side wasm instance
		// stays alive long enough for the console message to flush
		// + so DOM event handlers retain their js.Func references.
		<-make(chan struct{})
		return
	}
	fmt.Println("QUAKE: exited cleanly")
}

// run is main's testability seam (mirrors quake-tamago/main.go). It
// returns errors instead of halting so the browser console carries
// the failure reason; main then blocks on receipt.
func run() error {
	// 1. Build the wasm backend over canvas#quake. NewDefault
	//    constructs the Framebuffer / Input / Audio adapters
	//    + the performance.now clock. Audio failures (e.g. the
	//    user hasn't gestured yet so AudioContext can't construct)
	//    degrade to silent rather than abort.
	be, err := wasm.NewDefault("#quake", fbWidth, fbHeight)
	if err != nil {
		return fmt.Errorf("wasm.NewDefault: %w", err)
	}
	logf("backend up -- canvas=#quake size=%dx%d", fbWidth, fbHeight)

	// 2. Try the OCI registry FIRST when an OCIReference has been
	//    baked in via -ldflags. Streaming the pak + music tracks on
	//    demand keeps the wasm payload tiny (~10 MB) -- the alternative
	//    is embedding 264 MB of game data into the binary. On any
	//    failure (no reference set, registry unreachable, manifest
	//    parse error) we silently fall back to the embedpak path.
	var pakFS fs.FS
	if OCIReference != "" {
		fsys, err := openOCIAssets(OCIReference)
		if err != nil {
			logf("ociassets: %v -- falling back to embedpak", err)
		} else {
			pakFS = fsys
			logf("ociassets: streaming pak + music from %s", OCIReference)
		}
	}

	// 3. Embedded pak fallback. The placeholder ships empty (12 bytes)
	//    so embedpak.OpenAsFS returns ErrEmbedPakEmpty when no real
	//    assets are baked in -- we then fall through to the synthbsp +
	//    synthetic-asset path. The hook is kept here so a build that
	//    drops a real pak0 in embedpak/empty.pak picks it up with no
	//    code change.
	if pakFS == nil {
		fsys, pakErr := embedpak.OpenAsFS()
		if pakErr != nil {
			logf("embedpak.OpenAsFS: %v -- using synthetic assets + synthbsp", pakErr)
		} else {
			pakFS = fsys
		}
	}

	// 4. Build the asset VFS: synthetic fallback first, real pak last
	//    (vfs.Add prepends, so the LAST Add wins).
	v := vfs.New()
	v.Add(syntheticAssets())
	if pakFS != nil {
		v.Add(pakFS)
	}

	// 4. Loopback client/server pair. No real host -> bare loopback
	//    whose server side is silently dropped.
	cli, _ := engineserver.NewLoopbackConn()
	clientState := client.NewState()

	// 5. Build the Runner via the standard SetupOpts path. The
	//    backend.Backend.Size() return (fbWidth x fbHeight) drives
	//    framebuffer + RGBA buffer dimensions.
	runner, err := runloop.NewRunnerFromVFS(runloop.SetupOpts{
		VFS:            v,
		Host:           stubHost{},
		Client:         clientState,
		Conn:           cli,
		Backend:        be,
		BackgroundIdx:  0x20, // pleasant grey
		NotifyLifetime: 3,
		MaxNotifyRows:  4,
	})
	if err != nil {
		return fmt.Errorf("NewRunnerFromVFS: %w", err)
	}
	runner.Console.Print("PURE-GO QUAKE 1 -- browser bring-up\n")
	runner.Console.Print("backend/wasm: Canvas2D + DOMInput + WebAudio\n")
	logf("runner up -- console seeded, entering RunUntilQuit")

	// 6. Wire a minimal Pre2DDraw that walks a synthbsp scene so the
	//    canvas paints actual 3D pixels (not just the clear colour).
	//    On any setup failure the closure stays nil + the canvas
	//    shows the 2D layer alone (console / menu overlay).
	if err := setupSynthRenderer(runner); err != nil {
		logf("setupSynthRenderer skipped: %v -- 2D-only fallback", err)
	}

	// 7. Surface a runtime-visible "ready" badge on the page so the
	//    user knows the wasm payload is alive even before the first
	//    frame paints. Pure best-effort: missing #status is fine.
	if status := js.Global().Get("document").Call("getElementById", "status"); !status.IsNull() && !status.IsUndefined() {
		status.Set("textContent", fmt.Sprintf("running -- %dx%d canvas, loopback host", fbWidth, fbHeight))
	}

	// 8. Drive the per-tic loop ourselves so the JS event loop has a
	//    chance to deliver paint / input ticks between frames. Calling
	//    runner.RunUntilQuit on the main thread would park us in a
	//    blocking `for {}` whose only JS interaction (js.CopyBytesToJS
	//    in PresentFrame) does NOT yield back to the browser scheduler
	//    -- the page would stay locked on the wasm goroutine forever
	//    and the canvas would never repaint. Sleeping zero seconds per
	//    tic parks the Go runtime via a timer goroutine, which the Go
	//    wasm scheduler implements as setTimeout(0); that releases the
	//    JS thread long enough to drain rAF + flush ImageData puts.
	//
	//    The shape mirrors runloop.RunUntilQuit's body verbatim (we
	//    can't reuse it as a library because that helper hard-codes
	//    the blocking loop) -- minus the explicit dt clamp, which the
	//    runner's MinFrameTime fallback handles inside RunFrame.
	return runUntilQuitYielding(runner)
}

// runUntilQuitYielding is the GOOS=js cmd-side equivalent of
// runloop.RunUntilQuit: drives one RunFrame per iteration, plus a
// time.Sleep(0) per tic so the Go scheduler hands the JS event loop
// back to the browser between frames. Without the yield the browser
// never repaints + never delivers input + Playwright's getImageData
// returns the unflushed initial canvas state (alpha 0), even though
// PresentFrame is running every tic on the wasm side.
//
// Quit-detection mirrors RunUntilQuit's quitObserver wrapper: we read
// the QuitRequested flag off the per-tic snapshot the runner's
// Backend.PollInput returned. The runner re-uses the same Backend so
// the observer hook is the cheapest way to spot the exit signal
// without changing the runloop's contract.
func runUntilQuitYielding(r *runloop.Runner) error {
	original := r.Backend
	defer func() { r.Backend = original }()
	obs := &quitObserver{Backend: original}
	r.Backend = obs

	lastNow := original.Now()
	dt := runloop.MinFrameTime
	for {
		nowSec := original.Now()
		if err := r.RunFrame(dt, float32(nowSec)); err != nil {
			return err
		}
		if obs.quitRequested {
			return nil
		}
		nextDt := float32(nowSec - lastNow)
		if nextDt < runloop.MinFrameTime {
			nextDt = runloop.MinFrameTime
		}
		lastNow = nowSec
		dt = nextDt
		// Cooperative yield: parks the goroutine on a Go-managed timer,
		// which the wasm runtime backs with setTimeout(0) -- that's the
		// hook the JS event loop needs to drain rAF + ImageData paints.
		// MUST be > 0 -- time.Sleep early-returns for any duration <= 0
		// without parking the goroutine, so a zero-Sleep would NOT yield
		// + the page would stay locked exactly like the unmodified
		// runloop.RunUntilQuit. 1 ms is short enough that frame rate
		// stays at the browser-rAF cap (~60 Hz) yet long enough to
		// trigger the actual gopark + setTimeout(0) round-trip.
		time.Sleep(time.Millisecond)
	}
}

// quitObserver wraps a backend so we can observe QuitRequested
// snapshots as they flow past PollInput -- runloop.RunUntilQuit does
// the same dance internally but its wrapper type is unexported so we
// repeat it here. Everything else passes through verbatim via the
// embedded backend.Backend interface.
type quitObserver struct {
	backend.Backend
	quitRequested bool
}

func (q *quitObserver) PollInput() (backend.InputSnapshot, error) {
	snap, err := q.Backend.PollInput()
	if snap.QuitRequested {
		q.quitRequested = true
	}
	return snap, err
}

// setupSynthRenderer wires a Pre2DDraw closure that paints the
// synthbsp BuildWithFaces scene. The closure is intentionally minimal:
// it skips the menu / Compose2D / sound paths (those still run via
// the runner's per-tic schedule, this just handles the 3D layer).
func setupSynthRenderer(runner *runloop.Runner) error {
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

	// Synth-BSP overlays (mirrors quake-tamago/main.go's isSynth path).
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
	// Camera anchored 20 units above the synth-bsp floor (which lives
	// at Z=0 spanning [0,10] x [0,10]); pitch=80 looks down at the
	// floor with a steep enough angle that yaw rotation is visible
	// (gimbal-safe stand-off from straight-down 90 degrees). At
	// fovX=90 the 10-unit-wide floor occupies the central ~25% of the
	// 320x240 canvas; the remaining pixels show the clear-color sky
	// fill below, so the painted frame is guaranteed to carry a mix
	// of distinct palette indices (checker fg + clear bg) instead of
	// the uniform clear color we had before. The original pitch=0
	// horizon shot pointed the camera through the side of the floor
	// + missed every face entirely; that produced the all-clear-color
	// canvas the L2/L3 bring-up reports flagged.
	camOrigin := [3]float32{5, 5, 20}
	const (
		fovX     = 90.0
		camPitch = 80.0
	)

	var surfaces bsprender.SurfaceList
	frameCount := 0
	runner.Pre2DDraw = func(fb *render.FrameBuffer, viewOrigin, viewAngles [3]float32) error {
		frame := frameCount
		frameCount++
		// Slow yaw spin so the canvas shows visible motion; pitch is
		// fixed so we keep looking down at the floor regardless of
		// the per-tic viewAngles state the runloop hands us (the stub
		// host can't drive a real player edict that would refresh
		// these for us).
		viewAngles = [3]float32{camPitch, float32(frame % 360), 0}
		viewOrigin = camOrigin

		// Clear to background palette index 0x10 (sky-like). Compose2D
		// downstream sees Runner.Pre2DDraw != nil and sets
		// SkipBackgroundFill so this clear survives into PresentFrame.
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

// makeCheckerTex returns an NxN palette-indexed checker.
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

// syntheticAssets returns an in-memory fs.FS shaped like the standard
// palette / colormap / conchars triple [assets.LoadStandard] expects.
// Mirrors quake-tamago/main.go's syntheticAssets but inlined here so
// the wasm binary stays self-contained.
func syntheticAssets() fs.FS {
	return memFS{
		"gfx/palette.lmp":  makePaletteLump(),
		"gfx/colormap.lmp": makeColorMapLump(),
		"gfx/conchars.lmp": makeConcharsLump(),
	}
}

func makePaletteLump() []byte {
	buf := make([]byte, render.PaletteLumpSize)
	// Sweep an HSV-ish rainbow across the 256 palette slots so the
	// rendered scene paints in visually distinct colours -- the
	// previous (i, i^0xFF, i<<1) ramp collapsed every checker tile +
	// the clear fill into near-identical greens, which the Playwright
	// verification could not tell apart. The new layout dedicates
	// idx 0x10 (sky fill) to a neutral mid-blue and idx 0x00 / 0x0F /
	// 0x1F / 0x2F (the checker face tiles, see makeCheckerTex) to
	// four well-separated hues so the rasterized triangle stands out
	// against the cleared background in headless screenshots AND
	// to a human eyeballing the canvas.
	for i := 0; i < 256; i++ {
		// 6-channel rainbow with a deterministic light-dark zigzag so
		// neighbouring palette entries aren't visually identical.
		base := byte(i)
		switch i % 6 {
		case 0:
			buf[i*3+0] = base // R-dominant
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
	// Idx 0x10 is the Pre2DDraw clear (sky) -- pin it to a neutral
	// mid-blue so it never collides with a face tile colour.
	buf[0x10*3+0] = 48
	buf[0x10*3+1] = 96
	buf[0x10*3+2] = 176
	// Pin the four checker-face tile indices (see makeCheckerTex)
	// to bright, well-separated colours so the rendered face stands
	// out against the sky. Without these pins the rainbow ramp lands
	// the small indices (0/15/31/47) in the dark/black end of the
	// spectrum + the rasterized triangle reads as black even though
	// the rasterizer is doing its job.
	for k, rgb := range [...][3]byte{
		{255, 64, 64},  // idx 0  -> bright red
		{255, 200, 32}, // idx 15 -> warm yellow
		{32, 200, 255}, // idx 31 -> bright cyan
		{200, 32, 200}, // idx 47 -> magenta
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
	// 128x128 = 16384. Simple ASCII-grid fill so the console
	// background isn't entirely black: alternating palette indices
	// per cell. Real glyph data lands when the pak ships gfx/conchars.lmp.
	buf := make([]byte, assets.ConCharsLumpSize)
	for i := range buf {
		buf[i] = 0
	}
	return buf
}

// memFS is a minimal in-memory fs.FS (signal-handling-free alternative
// to testing/fstest.MapFS — kept consistent with quake-tamago's pattern
// for stable, runtime-light asset bootstrap).
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

// readerAt is a minimal byte-slice ReaderAt so bspfile.Open works
// against the synthbsp byte buffer without pulling in bytes.NewReader
// (avoids one extra dependency in the wasm payload).
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

// logf is fmt.Printf scoped to a QUAKE: prefix so browser-console
// output stays grep-able against the tamago serial log format.
func logf(format string, args ...any) {
	fmt.Printf("QUAKE: "+format+"\n", args...)
}

// openOCIAssets parses the linker-injected reference, builds an OCI
// client over it, fetches the manifest, and returns an fs.FS that
// streams the layers on demand. The error wrapper carries enough
// context for the browser console to point a developer at the
// failing reference.
func openOCIAssets(reference string) (fs.FS, error) {
	ref, err := ociassets.ParseReference(reference)
	if err != nil {
		return nil, fmt.Errorf("parse %q: %w", reference, err)
	}
	client := ociassets.NewClient(ref.Origin)
	return ociassets.NewFSFromManifest(context.Background(), client, ref.Repo, ref.Tag)
}

// errLog forces compile-time use of errors (kept for fast future
// wiring of error sentinels without re-importing).
var _ = errors.New
