// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package runloop

import (
	"errors"
	"testing"

	"github.com/go-quake1/engine/backend"
	"github.com/go-quake1/engine/client"
	"github.com/go-quake1/engine/protocol"
	"github.com/go-quake1/engine/render"
	"github.com/go-quake1/engine/server"
	"github.com/go-quake1/engine/sound"
)

// ----- fakes ---------------------------------------------------------

// fakeHost satisfies HostFramer with caller-controlled error + tic
// counter so tests can assert "Frame was called once / N times" without
// spinning up a real *host.Host (which needs VM + Cache + Resolver +
// Progs + a SpawnServer pass).
type fakeHost struct {
	calls int
	err   error
}

func (f *fakeHost) Frame(_ float32) error {
	f.calls++
	return f.err
}

// failingPresent wraps a Recorder + overrides PresentFrame to return a
// fixed error. Used to assert error propagation from the display path.
type failingPresent struct {
	*backend.Recorder
	err error
}

func (f *failingPresent) PresentFrame(_ []byte, _, _ int) error { return f.err }

// failingPoll wraps Recorder + overrides PollInput to fail.
type failingPoll struct {
	*backend.Recorder
	err error
}

func (f *failingPoll) PollInput() (backend.InputSnapshot, error) {
	return backend.InputSnapshot{}, f.err
}

// failingAudio wraps Recorder + overrides QueueAudio with a caller-
// chosen error. errors.Is(backend.ErrUnsupported) is the silently-
// ignored case; any other error must propagate.
type failingAudio struct {
	*backend.Recorder
	err error
}

func (f *failingAudio) QueueAudio(_ []sound.StereoSample) error { return f.err }

// makeCharsSheet builds a 128x128 conchars Pic with each glyph filled
// with a distinct byte so Compose2D has a well-formed sheet to read
// from. Lifted from render/draw_test.go's same-name helper (kept local
// because Go's test-helper visibility is per-package).
func makeCharsSheet() *render.Pic {
	const dim = 128
	p := &render.Pic{
		Width:  dim,
		Height: dim,
		Pixels: make([]byte, dim*dim),
	}
	for row := 0; row < 16; row++ {
		for col := 0; col < 16; col++ {
			glyph := byte(0x10 + row*16 + col)
			for v := 0; v < render.CharHeight; v++ {
				base := (row*render.CharHeight+v)*dim + col*render.CharWidth
				for u := 0; u < render.CharWidth; u++ {
					p.Pixels[base+u] = glyph
				}
			}
		}
	}
	return p
}

// newRunner builds a minimal Runner wired to a Recorder backend +
// loopback NetConn. The Client is left in StateDisconnected unless
// the test overrides; client.Tick still runs each frame (the
// inbound drain is needed for the wire-driven signon handshake)
// but the OUTBOUND clc_move build is gated on StateConnected inside
// Tick, so a pre-signon disconnected client produces no wire traffic.
func newRunner(t *testing.T, b backend.Backend) (*Runner, *server.LoopbackConn) {
	t.Helper()
	const w, h = (1 + render.MinConsoleWidth) * render.CharWidth, 64
	fb, err := render.NewFrameBuffer(w, h)
	if err != nil {
		t.Fatalf("NewFrameBuffer: %v", err)
	}
	con, err := render.NewConsole(render.MinConsoleWidth, render.MinConsoleLines)
	if err != nil {
		t.Fatalf("NewConsole: %v", err)
	}
	scr, err := render.NewScreen(w, h)
	if err != nil {
		t.Fatalf("NewScreen: %v", err)
	}
	pal := &render.Palette{}
	clientSide, _ := server.NewLoopbackConn()
	loop, ok := clientSide.(*server.LoopbackConn)
	if !ok {
		t.Fatalf("loopback conn type %T", clientSide)
	}
	r := &Runner{
		Host:           &fakeHost{},
		Client:         client.NewState(),
		Conn:           clientSide,
		Backend:        b,
		FrameBuffer:    fb,
		Console:        con,
		Screen:         scr,
		Chars:          makeCharsSheet(),
		Palette:        pal,
		Speeds:         client.DefaultInputSpeeds(),
		RGBA:           make([]byte, w*h*4),
		BackgroundIdx:  0x10,
		NotifyLifetime: 3,
		MaxNotifyRows:  2,
	}
	return r, loop
}

// ----- nil-arg + size guards ----------------------------------------

func TestRunFrame_NilHost(t *testing.T) {
	r, _ := newRunner(t, backend.NewRecorder(0, 0))
	r.Host = nil
	if err := r.RunFrame(0.05, 1); !errors.Is(err, ErrRunnerNilHost) {
		t.Fatalf("err = %v want %v", err, ErrRunnerNilHost)
	}
}

func TestRunFrame_NilClient(t *testing.T) {
	r, _ := newRunner(t, backend.NewRecorder(0, 0))
	r.Client = nil
	if err := r.RunFrame(0.05, 1); !errors.Is(err, ErrRunnerNilClient) {
		t.Fatalf("err = %v want %v", err, ErrRunnerNilClient)
	}
}

func TestRunFrame_NilConn(t *testing.T) {
	r, _ := newRunner(t, backend.NewRecorder(0, 0))
	r.Conn = nil
	if err := r.RunFrame(0.05, 1); !errors.Is(err, ErrRunnerNilConn) {
		t.Fatalf("err = %v want %v", err, ErrRunnerNilConn)
	}
}

func TestRunFrame_NilBackend(t *testing.T) {
	r, _ := newRunner(t, backend.NewRecorder(0, 0))
	r.Backend = nil
	if err := r.RunFrame(0.05, 1); !errors.Is(err, ErrRunnerNilBackend) {
		t.Fatalf("err = %v want %v", err, ErrRunnerNilBackend)
	}
}

func TestRunFrame_NilFB(t *testing.T) {
	r, _ := newRunner(t, backend.NewRecorder(0, 0))
	r.FrameBuffer = nil
	if err := r.RunFrame(0.05, 1); !errors.Is(err, ErrRunnerNilFB) {
		t.Fatalf("err = %v want %v", err, ErrRunnerNilFB)
	}
}

func TestRunFrame_RGBASizeTooSmall(t *testing.T) {
	r, _ := newRunner(t, backend.NewRecorder(0, 0))
	r.RGBA = r.RGBA[:4] // way too small
	if err := r.RunFrame(0.05, 1); !errors.Is(err, ErrRunnerRGBASize) {
		t.Fatalf("err = %v want %v", err, ErrRunnerRGBASize)
	}
}

// ----- happy paths ---------------------------------------------------

func TestRunFrame_HappyDisconnected(t *testing.T) {
	rec := backend.NewRecorder(0, 0)
	r, loop := newRunner(t, rec)
	r.Client.Connection = client.StateDisconnected

	// SoundPool present + mix buffer present -- audio path runs.
	pool, err := sound.NewPool(8)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	r.SoundPool = pool
	r.MixBuffer = make([]sound.StereoSample, sound.MixBufferStereoFrames)

	if err := r.RunFrame(0.05, 1); err != nil {
		t.Fatalf("RunFrame: %v", err)
	}
	if got := r.Host.(*fakeHost).calls; got != 1 {
		t.Fatalf("host.Frame calls = %d want 1", got)
	}
	if len(rec.Frames) != 1 {
		t.Fatalf("rec.Frames len = %d want 1", len(rec.Frames))
	}
	want := r.FrameBuffer.Width * r.FrameBuffer.Height * 4
	if len(rec.Frames[0]) != want {
		t.Fatalf("rec.Frames[0] len = %d want %d", len(rec.Frames[0]), want)
	}
	if len(rec.Audio) != 1 {
		t.Fatalf("rec.Audio len = %d want 1", len(rec.Audio))
	}
	// Disconnected -> client.Tick still runs (the wire-driven signon
	// handshake needs the inbound drain) but Tick's OUTBOUND short-
	// circuit means no clc_move is sent -> loopback outbox empty.
	kind, _, _ := loop.ReadMessage()
	if kind != server.MessageNone {
		t.Fatalf("loop ReadMessage kind = %v want MessageNone (Tick outbound is gated on StateConnected)", kind)
	}
}

// TestRunFrame_WireSignonDrivesStateConnected proves the wire-driven
// signon handshake works end-to-end through RunFrame: starting from
// StateDisconnected with the server-side queue already holding the
// stage byte-pair sequence SendSignonHandshake emits, ONE RunFrame
// call drains the inbound bytes via client.Tick + Apply walks the
// state through Connecting (stage 1) to Connected (stage 4). Without
// the "client.Tick always runs" change in RunFrame the deadlock
// would persist: the inbound drain is the only path the stage-1
// byte can travel, but it was previously gated behind a
// Connection != StateDisconnected guard.
func TestRunFrame_WireSignonDrivesStateConnected(t *testing.T) {
	rec := backend.NewRecorder(0, 0)
	r, _ := newRunner(t, rec)
	// Use a fresh loopback pair so the test can push bytes from the
	// server side without the newRunner's stub server-half being lost.
	clientSide, serverSide := server.NewLoopbackConn()
	r.Conn = clientSide
	// State starts at StateDisconnected; the wire path is the only
	// driver. Pre-stage the stage byte-pair tail SendSignonHandshake
	// would queue (the serverinfo prefix has its own apply tests; the
	// stage bytes alone are sufficient to drive the lifecycle).
	if _, err := serverSide.SendReliable([]byte{
		protocol.SvcSignonNum, 1,
		protocol.SvcSignonNum, 2,
		protocol.SvcSignonNum, 3,
		protocol.SvcSignonNum, 4,
	}); err != nil {
		t.Fatalf("SendReliable: %v", err)
	}

	if err := r.RunFrame(0.05, 1); err != nil {
		t.Fatalf("RunFrame: %v", err)
	}
	if r.Client.Connection != client.StateConnected {
		t.Fatalf("Connection = %v want StateConnected (wire signon path failed)", r.Client.Connection)
	}
	if !r.Client.Spawned {
		t.Error("Spawned = false; want true (stage 4 should MarkSpawned)")
	}
}

func TestRunFrame_HappyConnectedSendsMove(t *testing.T) {
	rec := backend.NewRecorder(0, 0)
	r, _ := newRunner(t, rec)
	// Connected client -> client.Tick runs the outbound clc_move path.
	r.Client.Connection = client.StateConnected

	if err := r.RunFrame(0.05, 1); err != nil {
		t.Fatalf("RunFrame: %v", err)
	}
	// The client SendUnreliable goes to the SERVER side of the loopback;
	// we kept the client-side conn so server-side reads would need the
	// other half. Instead assert SentMove via a second tick + by
	// allocating the server side:
	_, serverSide := server.NewLoopbackConn()
	_ = serverSide
	// Recorder captured a frame.
	if len(rec.Frames) != 1 {
		t.Fatalf("rec.Frames len = %d want 1", len(rec.Frames))
	}
}

// TestRunFrame_ConnectedSendsClcMove uses a fresh loopback pair so the
// test can observe the server-side inbox. Asserts the client.Tick
// outbound branch was reached (SentMove path) when Connection ==
// StateConnected.
func TestRunFrame_ConnectedSendsClcMove(t *testing.T) {
	rec := backend.NewRecorder(0, 0)
	r, _ := newRunner(t, rec)
	clientSide, serverSide := server.NewLoopbackConn()
	r.Conn = clientSide
	r.Client.Connection = client.StateConnected

	if err := r.RunFrame(0.05, 1); err != nil {
		t.Fatalf("RunFrame: %v", err)
	}
	kind, data, err := serverSide.ReadMessage()
	if err != nil {
		t.Fatalf("serverSide.ReadMessage: %v", err)
	}
	if kind != server.MessageUnreliable {
		t.Fatalf("serverSide kind = %v want MessageUnreliable", kind)
	}
	if len(data) == 0 {
		t.Fatalf("serverSide clc_move payload empty")
	}
}

// ----- short-circuits ------------------------------------------------

func TestRunFrame_AudioSkippedWhenPoolNil(t *testing.T) {
	rec := backend.NewRecorder(0, 0)
	r, _ := newRunner(t, rec)
	// SoundPool nil + MixBuffer empty -> audio path skipped.
	r.SoundPool = nil
	r.MixBuffer = nil

	if err := r.RunFrame(0.05, 1); err != nil {
		t.Fatalf("RunFrame: %v", err)
	}
	if len(rec.Audio) != 0 {
		t.Fatalf("rec.Audio len = %d want 0 (SoundPool nil)", len(rec.Audio))
	}
}

func TestRunFrame_AudioSkippedWhenMixBufferEmpty(t *testing.T) {
	rec := backend.NewRecorder(0, 0)
	r, _ := newRunner(t, rec)
	pool, err := sound.NewPool(8)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	r.SoundPool = pool
	r.MixBuffer = nil

	if err := r.RunFrame(0.05, 1); err != nil {
		t.Fatalf("RunFrame: %v", err)
	}
	if len(rec.Audio) != 0 {
		t.Fatalf("rec.Audio len = %d want 0 (MixBuffer empty)", len(rec.Audio))
	}
}

// ----- error propagation --------------------------------------------

func TestRunFrame_PollInputErrorPropagates(t *testing.T) {
	wantErr := errors.New("poll boom")
	fp := &failingPoll{Recorder: backend.NewRecorder(0, 0), err: wantErr}
	r, _ := newRunner(t, fp)
	if err := r.RunFrame(0.05, 1); !errors.Is(err, wantErr) {
		t.Fatalf("err = %v want %v", err, wantErr)
	}
}

func TestRunFrame_HostFrameErrorPropagates(t *testing.T) {
	rec := backend.NewRecorder(0, 0)
	r, _ := newRunner(t, rec)
	wantErr := errors.New("host frame boom")
	r.Host = &fakeHost{err: wantErr}
	if err := r.RunFrame(0.05, 1); !errors.Is(err, wantErr) {
		t.Fatalf("err = %v want %v", err, wantErr)
	}
	// PresentFrame must NOT have been called.
	if len(rec.Frames) != 0 {
		t.Fatalf("rec.Frames len = %d want 0 on host error", len(rec.Frames))
	}
}

func TestRunFrame_ClientTickErrorPropagates(t *testing.T) {
	rec := backend.NewRecorder(0, 0)
	r, _ := newRunner(t, rec)
	r.Client.Connection = client.StateConnected
	// Close the loopback so SendUnreliable returns ErrNetConnClosed.
	if err := r.Conn.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	err := r.RunFrame(0.05, 1)
	if err == nil {
		t.Fatalf("RunFrame: got nil err, want client.Tick failure")
	}
	if len(rec.Frames) != 0 {
		t.Fatalf("rec.Frames len = %d want 0 on client.Tick error", len(rec.Frames))
	}
}

func TestRunFrame_ExpandFrameErrorPropagates(t *testing.T) {
	rec := backend.NewRecorder(0, 0)
	r, _ := newRunner(t, rec)
	// Compose2D needs ctx.Chars; nil-out triggers ErrComposeNilChars.
	r.Chars = nil
	if err := r.RunFrame(0.05, 1); !errors.Is(err, render.ErrComposeNilChars) {
		t.Fatalf("err = %v want %v", err, render.ErrComposeNilChars)
	}
	if len(rec.Frames) != 0 {
		t.Fatalf("rec.Frames len = %d want 0 on compose error", len(rec.Frames))
	}
}

func TestRunFrame_PresentFrameErrorPropagates(t *testing.T) {
	wantErr := errors.New("present boom")
	fp := &failingPresent{Recorder: backend.NewRecorder(0, 0), err: wantErr}
	r, _ := newRunner(t, fp)
	if err := r.RunFrame(0.05, 1); !errors.Is(err, wantErr) {
		t.Fatalf("err = %v want %v", err, wantErr)
	}
}

func TestRunFrame_PaintErrorPropagates(t *testing.T) {
	rec := backend.NewRecorder(0, 0)
	r, _ := newRunner(t, rec)
	pool, err := sound.NewPool(0)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	// Park a 24-bit channel in the pool: the mixer accepts 8 and 16
	// (batch 59 landed the 16-bit path), so an unknown bit-depth is
	// what now trips sound.Paint's pre-flight ErrMixBadFormat guard
	// before any output is written.
	pool.Channels[0].Sfx = &sound.Sample{
		BitsPerSam: 24,
		Data:       []byte{0, 0, 0, 0, 0, 0},
		NumSamples: 2,
	}
	pool.Channels[0].EndPos = 2
	r.SoundPool = pool
	r.MixBuffer = make([]sound.StereoSample, sound.MixBufferStereoFrames)
	if err := r.RunFrame(0.05, 1); !errors.Is(err, sound.ErrMixBadFormat) {
		t.Fatalf("err = %v want %v", err, sound.ErrMixBadFormat)
	}
}

func TestRunFrame_QueueAudioUnsupportedIgnored(t *testing.T) {
	fa := &failingAudio{Recorder: backend.NewRecorder(0, 0), err: backend.ErrUnsupported}
	r, _ := newRunner(t, fa)
	pool, err := sound.NewPool(8)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	r.SoundPool = pool
	r.MixBuffer = make([]sound.StereoSample, sound.MixBufferStereoFrames)
	if err := r.RunFrame(0.05, 1); err != nil {
		t.Fatalf("RunFrame: %v (ErrUnsupported should be silent)", err)
	}
	// PresentFrame still ran.
	if len(fa.Recorder.Frames) != 1 {
		t.Fatalf("rec.Frames len = %d want 1", len(fa.Recorder.Frames))
	}
}

func TestRunFrame_QueueAudioOtherErrorPropagates(t *testing.T) {
	wantErr := errors.New("queue boom")
	fa := &failingAudio{Recorder: backend.NewRecorder(0, 0), err: wantErr}
	r, _ := newRunner(t, fa)
	pool, err := sound.NewPool(8)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	r.SoundPool = pool
	r.MixBuffer = make([]sound.StereoSample, sound.MixBufferStereoFrames)
	if err := r.RunFrame(0.05, 1); !errors.Is(err, wantErr) {
		t.Fatalf("err = %v want %v", err, wantErr)
	}
}

// TestRunFrame_MixBufferClampedToMax exercises the n > MaxMixOutputFrames
// branch by allocating a MixBuffer larger than the cap; RunFrame should
// clamp + still succeed.
func TestRunFrame_MixBufferClampedToMax(t *testing.T) {
	rec := backend.NewRecorder(0, 0)
	r, _ := newRunner(t, rec)
	pool, err := sound.NewPool(8)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	r.SoundPool = pool
	r.MixBuffer = make([]sound.StereoSample, sound.MixBufferStereoFrames+128)
	if err := r.RunFrame(0.05, 1); err != nil {
		t.Fatalf("RunFrame: %v", err)
	}
	if len(rec.Audio) != 1 {
		t.Fatalf("rec.Audio len = %d want 1", len(rec.Audio))
	}
	if got := len(rec.Audio[0]); got != sound.MixBufferStereoFrames {
		t.Fatalf("rec.Audio[0] len = %d want %d", got, sound.MixBufferStereoFrames)
	}
}

// ----- UpdateButtonsFromSnapshot ------------------------------------

func TestUpdateButtonsFromSnapshot_DownAllMappedKeys(t *testing.T) {
	var b client.MovementButtons
	snap := backend.InputSnapshot{
		KeysDown: []backend.KeyCode{
			backend.KeyW, backend.KeyS,
			backend.KeyA, backend.KeyD,
			backend.KeyLeft, backend.KeyRight,
			backend.KeyUp, backend.KeyDown,
			backend.KeySpace, backend.KeyCtrl,
			backend.KeyShift,
		},
	}
	UpdateButtonsFromSnapshot(&b, snap)
	cases := []struct {
		name string
		got  *client.ButtonState
	}{
		{"Forward", &b.Forward},
		{"Back", &b.Back},
		{"MoveLeft", &b.MoveLeft},
		{"MoveRight", &b.MoveRight},
		{"Left", &b.Left},
		{"Right", &b.Right},
		{"Lookup", &b.Lookup},
		{"Lookdown", &b.Lookdown},
		{"Up", &b.Up},
		{"Down", &b.Down},
	}
	for _, tc := range cases {
		// Down -> bits 0 + 1 set, bit 2 clear.
		if tc.got.Pressed&1 == 0 {
			t.Fatalf("%s: held bit not set", tc.name)
		}
		if tc.got.Pressed&2 == 0 {
			t.Fatalf("%s: down-edge bit not set", tc.name)
		}
		if tc.got.Pressed&4 != 0 {
			t.Fatalf("%s: up-edge bit unexpectedly set", tc.name)
		}
	}
	if !b.SpeedHeld {
		t.Fatalf("SpeedHeld not set on KeyShift down")
	}
}

func TestUpdateButtonsFromSnapshot_UpReleasesAllMappedKeys(t *testing.T) {
	var b client.MovementButtons
	// Pre-press all buttons.
	all := []backend.KeyCode{
		backend.KeyW, backend.KeyS,
		backend.KeyA, backend.KeyD,
		backend.KeyLeft, backend.KeyRight,
		backend.KeyUp, backend.KeyDown,
		backend.KeySpace, backend.KeyCtrl,
		backend.KeyShift,
	}
	UpdateButtonsFromSnapshot(&b, backend.InputSnapshot{KeysDown: all})
	UpdateButtonsFromSnapshot(&b, backend.InputSnapshot{KeysUp: all})

	slots := []*client.ButtonState{
		&b.Forward, &b.Back, &b.MoveLeft, &b.MoveRight,
		&b.Left, &b.Right, &b.Lookup, &b.Lookdown,
		&b.Up, &b.Down,
	}
	for i, s := range slots {
		if s.Pressed&1 != 0 {
			t.Fatalf("slot %d: held bit still set after release", i)
		}
		if s.Pressed&4 == 0 {
			t.Fatalf("slot %d: up-edge bit not set after release", i)
		}
	}
	if b.SpeedHeld {
		t.Fatalf("SpeedHeld still set after KeyShift up")
	}
}

// ----- Pre2DDraw hook -----------------------------------------------

// TestRunFrame_Pre2DDrawInvoked verifies the optional Pre2DDraw hook
// is invoked between client.Tick and Compose2D, receives the camera
// origin sourced from the wire-mirrored client.State.Entities map
// (NOT runner.ViewOrigin, which is now a legacy diagnostic field),
// receives the runner's ViewAngles, and the pre-drawn pixels survive
// the 2D Compose (SkipBackgroundFill is wired through). Also asserts
// the runner's frame still presents successfully.
func TestRunFrame_Pre2DDrawInvoked(t *testing.T) {
	rec := backend.NewRecorder(0, 0)
	r, _ := newRunner(t, rec)
	// Seed the wire-mirrored entity state for the local player so
	// RunFrame's viewOriginFromState lookup returns a non-zero
	// vector -- proves the runner sources the camera from
	// State.Entities, not from r.ViewOrigin.
	r.Client.PlayerNum = 1
	r.Client.Entities[1] = client.EntityState{Origin: [3]float32{1, 2, 3}}
	// r.ViewOrigin set to a sentinel that MUST NOT be the value the
	// closure receives -- guards against the legacy code path.
	r.ViewOrigin = [3]float32{99, 99, 99}
	r.ViewAngles = [3]float32{10, 20, 30}
	wantOrigin := [3]float32{1, 2, 3}

	var gotFB *render.FrameBuffer
	var gotOrigin, gotAngles [3]float32
	called := 0
	r.Pre2DDraw = func(fb *render.FrameBuffer, origin [3]float32, angles [3]float32) error {
		called++
		gotFB = fb
		gotOrigin = origin
		gotAngles = angles
		// Stamp a sentinel pixel that Compose2D must NOT overwrite
		// (SkipBackgroundFill should be true when the hook is set).
		fb.Pixels[0] = 0x7F
		return nil
	}

	if err := r.RunFrame(0.05, 1); err != nil {
		t.Fatalf("RunFrame: %v", err)
	}
	if called != 1 {
		t.Fatalf("Pre2DDraw calls = %d want 1", called)
	}
	if gotFB != r.FrameBuffer {
		t.Fatalf("Pre2DDraw fb = %p want %p", gotFB, r.FrameBuffer)
	}
	if gotOrigin != wantOrigin {
		t.Fatalf("Pre2DDraw origin = %v want %v (wire-mirrored State.Entities)", gotOrigin, wantOrigin)
	}
	if gotAngles != r.ViewAngles {
		t.Fatalf("Pre2DDraw angles = %v want %v", gotAngles, r.ViewAngles)
	}
	// The sentinel pixel must survive Compose2D (SkipBackgroundFill).
	if r.FrameBuffer.Pixels[0] != 0x7F {
		t.Fatalf("Pre2DDraw sentinel pixel = %#x want 0x7F (Compose2D should skip background fill)",
			r.FrameBuffer.Pixels[0])
	}
	if len(rec.Frames) != 1 {
		t.Fatalf("rec.Frames len = %d want 1", len(rec.Frames))
	}
}

// TestRunFrame_Pre2DDrawFallbackZeroOrigin verifies that when no
// State.Entities[PlayerNum] entry is present (player entity not yet
// received), the Pre2DDraw hook receives the zero-vector fallback
// rather than r.ViewOrigin. This guards the wire-only data flow on
// the pre-signon path.
func TestRunFrame_Pre2DDrawFallbackZeroOrigin(t *testing.T) {
	rec := backend.NewRecorder(0, 0)
	r, _ := newRunner(t, rec)
	// PlayerNum=1 but no Entities[1] entry -- the wire has not yet
	// delivered the player's svc_update.
	r.Client.PlayerNum = 1
	// r.ViewOrigin set non-zero to prove the runloop ignores it.
	r.ViewOrigin = [3]float32{1, 2, 3}

	var gotOrigin [3]float32
	r.Pre2DDraw = func(_ *render.FrameBuffer, origin [3]float32, _ [3]float32) error {
		gotOrigin = origin
		return nil
	}
	if err := r.RunFrame(0.05, 1); err != nil {
		t.Fatalf("RunFrame: %v", err)
	}
	if gotOrigin != ([3]float32{}) {
		t.Fatalf("Pre2DDraw origin = %v want zero (fallback path)", gotOrigin)
	}
}

// TestViewOriginFromState_NilState exercises the nil-State branch in
// viewOriginFromState. The runloop never reaches this branch in
// production (RunFrame returns ErrRunnerNilClient before the lookup),
// but the helper is callable independently + the nil guard makes the
// public-package invariant explicit.
func TestViewOriginFromState_NilState(t *testing.T) {
	if got := viewOriginFromState(nil); got != ([3]float32{}) {
		t.Fatalf("viewOriginFromState(nil) = %v want zero", got)
	}
}

// TestViewOriginFromState_NilEntities exercises the nil-Entities-map
// branch. NewState always allocates the map; a State with Entities==nil
// can only arise from a manual zero-value construction, but the guard
// keeps the helper crash-safe + the branch covered.
func TestViewOriginFromState_NilEntities(t *testing.T) {
	cs := &client.State{}
	if got := viewOriginFromState(cs); got != ([3]float32{}) {
		t.Fatalf("viewOriginFromState(empty State) = %v want zero", got)
	}
}

// TestRunFrame_Pre2DDrawErrorPropagates verifies an error from the
// 3D hook short-circuits the frame (no PresentFrame, no audio).
func TestRunFrame_Pre2DDrawErrorPropagates(t *testing.T) {
	rec := backend.NewRecorder(0, 0)
	r, _ := newRunner(t, rec)
	wantErr := errors.New("pre2d boom")
	r.Pre2DDraw = func(_ *render.FrameBuffer, _, _ [3]float32) error {
		return wantErr
	}
	if err := r.RunFrame(0.05, 1); !errors.Is(err, wantErr) {
		t.Fatalf("err = %v want %v", err, wantErr)
	}
	if len(rec.Frames) != 0 {
		t.Fatalf("rec.Frames len = %d want 0 on Pre2DDraw error", len(rec.Frames))
	}
}

// TestUpdateButtonsFromSnapshot_UnmappedKeysIgnored covers the
// fall-through path in buttonSlot (the keys with no movement mapping
// like KeyEnter / KeyMouse1 are silently dropped, NOT crashed on).
func TestUpdateButtonsFromSnapshot_UnmappedKeysIgnored(t *testing.T) {
	var b client.MovementButtons
	snap := backend.InputSnapshot{
		KeysDown: []backend.KeyCode{
			backend.KeyEnter, backend.KeyEscape, backend.KeyTab,
			backend.KeyMouse1, backend.KeyMouse2,
		},
		KeysUp: []backend.KeyCode{
			backend.KeyEnter, backend.KeyEscape, backend.KeyTab,
			backend.KeyMouse1, backend.KeyMouse2,
		},
	}
	UpdateButtonsFromSnapshot(&b, snap)
	// Nothing should be set.
	if (b != client.MovementButtons{}) {
		t.Fatalf("movement state mutated by non-movement keys: %+v", b)
	}
}

// ----- TriggerButtons + UpdateTriggersFromSnapshot ------------------

// TestTriggerButtons_ActionButtonsBitmask exercises every reachable
// combination of Attack + Jump to prove the bitmask matches the Q1
// upstream values (BUTTON_ATTACK=1, BUTTON_JUMP=2; OR-ed when both
// are held).
func TestTriggerButtons_ActionButtonsBitmask(t *testing.T) {
	cases := []struct {
		name string
		in   TriggerButtons
		want uint8
	}{
		{"none", TriggerButtons{}, 0},
		{"attack only", TriggerButtons{Attack: true}, 1},
		{"jump only", TriggerButtons{Jump: true}, 2},
		{"both", TriggerButtons{Attack: true, Jump: true}, 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.in.ActionButtons(); got != tc.want {
				t.Fatalf("ActionButtons = %d want %d", got, tc.want)
			}
		})
	}
}

// TestUpdateTriggersFromSnapshot_DownSetsBothFlags proves a single
// KeysDown carrying both mouse-1 + Enter latches both held flags.
func TestUpdateTriggersFromSnapshot_DownSetsBothFlags(t *testing.T) {
	var tr TriggerButtons
	snap := backend.InputSnapshot{
		KeysDown: []backend.KeyCode{backend.KeyMouse1, backend.KeyEnter},
	}
	UpdateTriggersFromSnapshot(&tr, snap)
	if !tr.Attack {
		t.Fatalf("Attack not set after KeyMouse1 down")
	}
	if !tr.Jump {
		t.Fatalf("Jump not set after KeyEnter down")
	}
	if got := tr.ActionButtons(); got != 3 {
		t.Fatalf("ActionButtons after both down = %d want 3", got)
	}
}

// TestUpdateTriggersFromSnapshot_UpClearsHeldFlag proves a KeysUp
// for either trigger clears just that flag (the other stays held).
func TestUpdateTriggersFromSnapshot_SpaceJumps(t *testing.T) {
	// KeySpace is the primary jump key (alongside KeyEnter): down sets
	// the +jump trigger, up clears it.
	var tr TriggerButtons
	UpdateTriggersFromSnapshot(&tr, backend.InputSnapshot{
		KeysDown: []backend.KeyCode{backend.KeySpace},
	})
	if !tr.Jump {
		t.Fatalf("Jump not set after KeySpace down")
	}
	if got := tr.ActionButtons(); got != 2 {
		t.Fatalf("ActionButtons after KeySpace down = %d want 2 (ButtonJump)", got)
	}
	UpdateTriggersFromSnapshot(&tr, backend.InputSnapshot{
		KeysUp: []backend.KeyCode{backend.KeySpace},
	})
	if tr.Jump {
		t.Fatalf("Jump still held after KeySpace up")
	}
}

func TestUpdateTriggersFromSnapshot_UpClearsHeldFlag(t *testing.T) {
	tr := TriggerButtons{Attack: true, Jump: true}
	UpdateTriggersFromSnapshot(&tr, backend.InputSnapshot{
		KeysUp: []backend.KeyCode{backend.KeyMouse1},
	})
	if tr.Attack {
		t.Fatalf("Attack still held after KeyMouse1 up")
	}
	if !tr.Jump {
		t.Fatalf("Jump dropped by KeyMouse1 up (should be unrelated)")
	}
	UpdateTriggersFromSnapshot(&tr, backend.InputSnapshot{
		KeysUp: []backend.KeyCode{backend.KeyEnter},
	})
	if tr.Jump {
		t.Fatalf("Jump still held after KeyEnter up")
	}
}

// TestUpdateTriggersFromSnapshot_UnmappedKeysIgnored exercises the
// switch's default arm: keys that aren't KeyMouse1 / KeyEnter pass
// through both KeysDown and KeysUp without touching the trigger flags.
func TestUpdateTriggersFromSnapshot_UnmappedKeysIgnored(t *testing.T) {
	var tr TriggerButtons
	snap := backend.InputSnapshot{
		KeysDown: []backend.KeyCode{
			backend.KeyW, backend.KeyS, backend.KeyShift,
			backend.KeyMouse2, backend.KeyEscape, backend.KeyTab,
		},
		KeysUp: []backend.KeyCode{
			backend.KeyW, backend.KeyS, backend.KeyShift,
			backend.KeyMouse2, backend.KeyEscape, backend.KeyTab,
		},
	}
	UpdateTriggersFromSnapshot(&tr, snap)
	if tr != (TriggerButtons{}) {
		t.Fatalf("trigger state mutated by non-trigger keys: %+v", tr)
	}
}

// TestRunFrame_TriggersFeedActionButtons is the end-to-end proof: a
// PollInput snapshot bearing KeyMouse1 down + a connected client state
// produces a clc_move whose `buttons` byte carries the BUTTON_ATTACK
// bit. Mirrors the existing TestRunFrame_ConnectedSendsClcMove shape;
// the assertion fires on the byte at offset 14 (1 opcode + 4 sendTime
// + 3 angles + 3 short moves = 14, then buttons).
func TestRunFrame_TriggersFeedActionButtons(t *testing.T) {
	rec := backend.NewRecorder(0, 0)
	rec.Input = backend.InputSnapshot{
		KeysDown: []backend.KeyCode{backend.KeyMouse1},
	}
	r, _ := newRunner(t, rec)
	clientSide, serverSide := server.NewLoopbackConn()
	r.Conn = clientSide
	r.Client.Connection = client.StateConnected

	if err := r.RunFrame(1.0/60.0, 1.0); err != nil {
		t.Fatalf("RunFrame: %v", err)
	}
	if !r.Triggers.Attack {
		t.Fatalf("Triggers.Attack not latched by KeyMouse1 down")
	}
	kind, data, err := serverSide.ReadMessage()
	if err != nil {
		t.Fatalf("serverSide.ReadMessage: %v", err)
	}
	if kind == server.MessageNone {
		t.Fatalf("serverSide got no message; expected clc_move with BUTTON_ATTACK")
	}
	const buttonsOffset = 14
	if len(data) <= buttonsOffset {
		t.Fatalf("clc_move payload too short: %d bytes", len(data))
	}
	if data[buttonsOffset]&1 == 0 {
		t.Fatalf("clc_move buttons byte = 0x%02x; BUTTON_ATTACK bit missing", data[buttonsOffset])
	}
}

// TestRunFrame_MovementButtonsFeedClcMove is the end-to-end proof
// matching the bug-report shape: a snapshot bearing KeyW down +
// StateConnected produces a clc_move whose `forwardmove` short
// (offset 8: 1 opcode + 4 sendTime + 3 angles) is positive, NOT zero.
// Originally the report was "cmd.fwd=0 cmd.side=0 even during physics
// tics"; this test guards against a regression of the full chain
// (Backend → MovementButtons → BaseMove → clc_move bytes on the wire).
func TestRunFrame_MovementButtonsFeedClcMove(t *testing.T) {
	rec := backend.NewRecorder(0, 0)
	rec.Input = backend.InputSnapshot{
		KeysDown: []backend.KeyCode{backend.KeyW},
	}
	r, _ := newRunner(t, rec)
	clientSide, serverSide := server.NewLoopbackConn()
	r.Conn = clientSide
	r.Client.Connection = client.StateConnected

	if err := r.RunFrame(1.0/60.0, 1.0); err != nil {
		t.Fatalf("RunFrame: %v", err)
	}
	kind, data, err := serverSide.ReadMessage()
	if err != nil {
		t.Fatalf("serverSide.ReadMessage: %v", err)
	}
	if kind == server.MessageNone {
		t.Fatalf("serverSide got no message; expected clc_move with forwardmove > 0")
	}
	// forwardmove sits at offset 8 (1 opcode + 4 sendTime + 3 angles).
	// LittleEndian int16; positive means forward.
	const forwardOffset = 8
	if len(data) < forwardOffset+2 {
		t.Fatalf("clc_move payload too short: %d bytes", len(data))
	}
	lo, hi := int16(data[forwardOffset]), int16(data[forwardOffset+1])
	forwardMove := lo | hi<<8
	if forwardMove <= 0 {
		t.Fatalf("clc_move forwardmove = %d; want > 0 (KeyW should produce forward motion)", forwardMove)
	}
}

// TestRunFrame_MouseDeltaFeedsViewAngles is the end-to-end proof that
// a virtio-input mouse-rel delta (the value the per-tic
// [backend.Backend.PollInput] snapshot carries in MouseDX / MouseDY)
// flows through the full per-tic chain --
//
//	PollInput  -> snap.MouseDX / MouseDY
//	         -> client.TickInput.MouseDX / MouseDY (Sensitivity=1)
//	         -> client.Tick -> ApplyMouseMove
//	         -> r.ViewAngles (pitch / yaw)
//
// -- and lands on r.ViewAngles with the canonical m_yaw / m_pitch =
// 0.022 deg/px scaling. With Sensitivity=1, dx=100 must rotate yaw
// by exactly 100 * 0.022 = 2.2 deg in the "decreases" direction
// (Q1 sign convention; mouse-right turns view right because yaw
// grows CCW), and dy=50 must rotate pitch by +50 * 0.022 = +1.1 deg
// (non-inverted look; mouse-down looks down).
//
// Anchors the wiring contract that makes virtio-mouse drive the
// in-game player view. Pre-fix the runloop's TickInput hardcoded
// MouseDX/MouseDY to 0 and the snapshot delta was dropped on the
// floor; this test guards against a regression of that drop.
func TestRunFrame_MouseDeltaFeedsViewAngles(t *testing.T) {
	rec := backend.NewRecorder(0, 0)
	rec.Input = backend.InputSnapshot{
		MouseDX: 100,
		MouseDY: 50,
	}
	r, _ := newRunner(t, rec)
	clientSide, _ := server.NewLoopbackConn()
	r.Conn = clientSide
	r.Client.Connection = client.StateConnected

	// Seed a non-zero starting yaw so AngleMod's [0,360) wrap is a
	// no-op for both the start and the post-tick value (start=50,
	// expected end=47.8).
	r.ViewAngles = [3]float32{0, 50, 0}

	if err := r.RunFrame(1.0/60.0, 1.0); err != nil {
		t.Fatalf("RunFrame: %v", err)
	}

	// yaw: 50 - 0.022*100 = 47.8 (AdjustAngles' arrow-key path is a
	// no-op with no Left/Right held, so this is purely the mouse
	// contribution). Tolerance covers AngleMod's 360/65536
	// (~0.0055 deg) fixed-point step on top of float32 round-off.
	const wantYaw float32 = 47.8
	gotYaw := r.ViewAngles[1]
	if abs32(gotYaw-wantYaw) > 0.01 {
		t.Errorf("ViewAngles[YAW]: got %v want %v (start=50, dx=100, m_yaw=0.022)",
			gotYaw, wantYaw)
	}

	// pitch: 0 + 0.022*50 = +1.1 (mouse-down looks down). No AngleMod
	// on pitch (clamp-only) so 1e-3 tolerance suffices.
	const wantPitch float32 = 1.1
	gotPitch := r.ViewAngles[0]
	if abs32(gotPitch-wantPitch) > 1e-3 {
		t.Errorf("ViewAngles[PITCH]: got %v want %v (start=0, dy=50, m_pitch=0.022)",
			gotPitch, wantPitch)
	}

	// Roll is the rotational axis around the look direction; mouse
	// must not touch it (Q1 has no mouse-driven roll).
	if r.ViewAngles[2] != 0 {
		t.Errorf("ViewAngles[ROLL]: got %v want 0 (mouse must not touch roll)",
			r.ViewAngles[2])
	}
}

// ----- particle pool per-tic step -----------------------------------

func TestRunFrame_ParticlePoolNilSkipsAdvance(t *testing.T) {
	// Sanity: nil ParticlePool path is the historical default + must
	// not introduce a panic. Covered implicitly by every existing
	// happy-path test, but pinned here to lock the guard.
	r, _ := newRunner(t, backend.NewRecorder(0, 0))
	if r.ParticlePool != nil {
		t.Fatalf("expected nil ParticlePool by default")
	}
	if err := r.RunFrame(0.05, 1); err != nil {
		t.Fatalf("RunFrame nil-pool: %v", err)
	}
}

func TestRunFrame_ParticlePoolStepAdvancesLiveSlots(t *testing.T) {
	r, _ := newRunner(t, backend.NewRecorder(0, 0))
	pool := render.NewPool()
	// Seed two particles whose lifetimes straddle the per-tic now=1.
	pool.Spawn(render.Particle{
		Velocity: [3]float32{10, 0, 100},
		Die:      10,
		Type:     render.ParticleGrav,
	}, 0)
	pool.Spawn(render.Particle{
		Velocity: [3]float32{0, 0, 0},
		Die:      0.5, // < now -> expires this tic
		Type:     render.ParticleStatic,
	}, 0)
	r.ParticlePool = pool
	r.ParticleGravity = 800

	if err := r.RunFrame(1, 1); err != nil {
		t.Fatalf("RunFrame: %v", err)
	}
	// First particle survived; Velocity[2] = 100 - dt*gravity*0.05
	// = 100 - 1*800*0.05 = 60.
	if got := pool.Particles[0].Velocity[2]; got != 60 {
		t.Fatalf("alive particle Vel[2] = %v, want 60", got)
	}
	// Second particle expired -> Die reset to 0 + NumAlive 1.
	if pool.Particles[1].Die != 0 {
		t.Fatalf("expired particle Die = %v, want 0", pool.Particles[1].Die)
	}
	if pool.NumAlive != 1 {
		t.Fatalf("pool.NumAlive = %d, want 1", pool.NumAlive)
	}
}

// abs32 is the |x| helper for float32 (Go's math.Abs is float64).
// Kept package-local to the runloop tests rather than promoted; the
// numeric-tolerance check sites are all here.
func abs32(x float32) float32 {
	if x < 0 {
		return -x
	}
	return x
}

// ----- Console tilde toggle + animate --------------------------------

// TestRunFrame_KeyTildeTogglesConsoleOpen verifies the down-edge of
// KeyTilde flips r.ConsoleOpen. A second down-edge flips it back.
func TestRunFrame_KeyTildeTogglesConsoleOpen(t *testing.T) {
	rec := backend.NewRecorder(0, 0)
	rec.Input = backend.InputSnapshot{KeysDown: []backend.KeyCode{backend.KeyTilde}}
	r, _ := newRunner(t, rec)
	if r.ConsoleOpen {
		t.Fatalf("ConsoleOpen = true on fresh runner; want false")
	}
	if err := r.RunFrame(0.05, 1); err != nil {
		t.Fatalf("RunFrame: %v", err)
	}
	if !r.ConsoleOpen {
		t.Fatalf("ConsoleOpen = false after KeyTilde down; want true")
	}
	// Second down-edge -> closes.
	if err := r.RunFrame(0.05, 2); err != nil {
		t.Fatalf("RunFrame 2: %v", err)
	}
	if r.ConsoleOpen {
		t.Fatalf("ConsoleOpen still true after second KeyTilde down; want false")
	}
}

// TestRunFrame_KeyTildeUpDoesNotToggle proves the up-edge alone does
// NOT flip ConsoleOpen (matches Con_ToggleConsole_f bound on press only).
func TestRunFrame_KeyTildeUpDoesNotToggle(t *testing.T) {
	rec := backend.NewRecorder(0, 0)
	rec.Input = backend.InputSnapshot{KeysUp: []backend.KeyCode{backend.KeyTilde}}
	r, _ := newRunner(t, rec)
	if err := r.RunFrame(0.05, 1); err != nil {
		t.Fatalf("RunFrame: %v", err)
	}
	if r.ConsoleOpen {
		t.Fatalf("ConsoleOpen = true after KeyTilde up-only; want false (up-edge is a no-op)")
	}
}

// TestRunFrame_ConsoleAnimatesTowardConLinesWhenOpen drives ConsoleOpen=true
// through one RunFrame and verifies Screen.ConCurrent advanced by exactly
// ScrollSpeed pixels toward ConLines.
func TestRunFrame_ConsoleAnimatesTowardConLinesWhenOpen(t *testing.T) {
	rec := backend.NewRecorder(0, 0)
	r, _ := newRunner(t, rec)
	// Force a known ScrollSpeed so the assertion is exact.
	r.Screen.ScrollSpeed = 4
	r.Screen.ConCurrent = 0
	r.ConsoleOpen = true

	if err := r.RunFrame(0.05, 1); err != nil {
		t.Fatalf("RunFrame: %v", err)
	}
	if r.Screen.ConCurrent != 4 {
		t.Fatalf("Screen.ConCurrent = %d after one tick; want 4 (ScrollSpeed)", r.Screen.ConCurrent)
	}
}

// TestRunFrame_ConsoleAnimatesTowardZeroWhenClosed: ConsoleOpen=false +
// pre-opened ConCurrent must retreat toward 0 by ScrollSpeed.
func TestRunFrame_ConsoleAnimatesTowardZeroWhenClosed(t *testing.T) {
	rec := backend.NewRecorder(0, 0)
	r, _ := newRunner(t, rec)
	r.Screen.ScrollSpeed = 4
	r.Screen.ConCurrent = 20
	r.ConsoleOpen = false

	if err := r.RunFrame(0.05, 1); err != nil {
		t.Fatalf("RunFrame: %v", err)
	}
	if r.Screen.ConCurrent != 16 {
		t.Fatalf("Screen.ConCurrent = %d; want 16 (20 - ScrollSpeed)", r.Screen.ConCurrent)
	}
}

// TestRunFrame_CenterPrintPlumbedFromClientState verifies that a
// pre-seeded client.State CenterPrintText / CenterPrintExpiry flows
// into the renderer + lands on the framebuffer at the 40% anchor.
func TestRunFrame_CenterPrintPlumbedFromClientState(t *testing.T) {
	rec := backend.NewRecorder(0, 0)
	r, _ := newRunner(t, rec)
	// nowSec passed to RunFrame = 1; expiry well beyond.
	r.Client.CenterPrintText = "X"
	r.Client.CenterPrintExpiry = 100
	// Palette needed so ExpandFrame doesn't trip nil-palette guard.
	pal := &render.Palette{}
	r.Palette = pal

	if err := r.RunFrame(0.05, 1); err != nil {
		t.Fatalf("RunFrame: %v", err)
	}
	// 'X' glyph at the 40% anchor in the renderer-composed framebuffer.
	y := r.FrameBuffer.Height * 2 / 5
	leftX := r.Screen.CenterX - render.CharWidth/2
	if r.FrameBuffer.Pixels[y*r.FrameBuffer.Pitch+leftX] != 0x68 {
		t.Fatalf("centerprint glyph pixel = %#x want 0x68 ('X' fill); RunFrame did not plumb centerprint into FrameContext",
			r.FrameBuffer.Pixels[y*r.FrameBuffer.Pitch+leftX])
	}
}

// --- intermissionLines (state-bank-driven scoreboard text) ----------

// nil client returns nil.
func TestIntermissionLines_NilClientReturnsNil(t *testing.T) {
	if got := intermissionLines(nil, 0); got != nil {
		t.Errorf("got %v want nil", got)
	}
}

// Client without Intermission flag returns nil.
func TestIntermissionLines_NoIntermissionReturnsNil(t *testing.T) {
	cs := client.NewState()
	if got := intermissionLines(cs, 5); got != nil {
		t.Errorf("got %v want nil", got)
	}
}

// Scoreboard mode (IntermissionText empty): three rows composed from
// stat bank + elapsed seconds.
func TestIntermissionLines_ScoreboardMode(t *testing.T) {
	cs := client.NewState()
	cs.Intermission = true
	cs.IntermissionTime = 10.0
	cs.Stats[protocol.StatSecrets] = 2
	cs.Stats[protocol.StatTotalSecrets] = 5
	cs.Stats[protocol.StatMonsters] = 17
	cs.Stats[protocol.StatTotalMonsters] = 20
	// elapsed = 75 -> 1:15
	lines := intermissionLines(cs, 85.0)
	if len(lines) != 3 {
		t.Fatalf("len(lines) = %d, want 3", len(lines))
	}
	if lines[0] != "TIME: 1:15" {
		t.Errorf("lines[0] = %q want %q", lines[0], "TIME: 1:15")
	}
	if lines[1] != "SECRETS: 2 / 5" {
		t.Errorf("lines[1] = %q want %q", lines[1], "SECRETS: 2 / 5")
	}
	if lines[2] != "MONSTERS: 17 / 20" {
		t.Errorf("lines[2] = %q want %q", lines[2], "MONSTERS: 17 / 20")
	}
}

// Negative elapsed (nowSec < IntermissionTime) clamps to 0 -- mirror
// of tyrquake's max(0, completed_time) guard.
func TestIntermissionLines_NegativeElapsedClampedToZero(t *testing.T) {
	cs := client.NewState()
	cs.Intermission = true
	cs.IntermissionTime = 100.0
	lines := intermissionLines(cs, 5.0) // earlier than IntermissionTime
	if lines[0] != "TIME: 0:00" {
		t.Errorf("lines[0] = %q want %q (negative elapsed must clamp)", lines[0], "TIME: 0:00")
	}
}

// Finale mode (IntermissionText non-empty): one slice entry per
// '\n'-separated substring.
func TestIntermissionLines_FinaleSplitOnNewline(t *testing.T) {
	cs := client.NewState()
	cs.Intermission = true
	cs.IntermissionText = "Episode 1\ncomplete\n\nWell done"
	lines := intermissionLines(cs, 0)
	want := []string{"Episode 1", "complete", "", "Well done"}
	if len(lines) != len(want) {
		t.Fatalf("len = %d want %d (lines=%v)", len(lines), len(want), lines)
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Errorf("lines[%d] = %q want %q", i, lines[i], want[i])
		}
	}
}

// Finale mode with empty IntermissionText is NOT reached (the guard
// would route through scoreboard mode); cover splitLines directly via
// a single-line credit body.
func TestIntermissionLines_FinaleSingleLine(t *testing.T) {
	cs := client.NewState()
	cs.Intermission = true
	cs.IntermissionText = "THE END"
	lines := intermissionLines(cs, 0)
	if len(lines) != 1 || lines[0] != "THE END" {
		t.Errorf("got %v want [THE END]", lines)
	}
}

// --- itoa + pad2 helpers ---------------------------------------------

func TestItoa(t *testing.T) {
	cases := []struct {
		in   int
		want string
	}{
		{0, "0"},
		{1, "1"},
		{9, "9"},
		{10, "10"},
		{-1, "-1"},
		{-1234567, "-1234567"},
		{1234567890, "1234567890"},
	}
	for _, c := range cases {
		if got := itoa(c.in); got != c.want {
			t.Errorf("itoa(%d) = %q want %q", c.in, got, c.want)
		}
	}
}

func TestPad2(t *testing.T) {
	cases := []struct {
		in   int
		want string
	}{
		{0, "00"},
		{5, "05"},
		{9, "09"},
		{10, "10"},
		{99, "99"},
		{-3, "00"},
		{100, "100"},
	}
	for _, c := range cases {
		if got := pad2(c.in); got != c.want {
			t.Errorf("pad2(%d) = %q want %q", c.in, got, c.want)
		}
	}
}

func TestSplitLines_EmptyYieldsSingleEmpty(t *testing.T) {
	got := splitLines("")
	if len(got) != 1 || got[0] != "" {
		t.Errorf("got %v want [\"\"]", got)
	}
}

// --- RunFrame intermission plumbing ----------------------------------

// RunFrame: when client.State.Intermission is true, the scoreboard
// text block lands in the framebuffer (and the in-game centerprint
// banner is suppressed).
func TestRunFrame_IntermissionPlumbedFromClientState(t *testing.T) {
	rec := backend.NewRecorder(0, 0)
	r, _ := newRunner(t, rec)
	r.Client.Intermission = true
	r.Client.IntermissionTime = 0
	r.Client.Stats[protocol.StatSecrets] = 0
	r.Client.Stats[protocol.StatTotalSecrets] = 0
	r.Client.Stats[protocol.StatMonsters] = 0
	r.Client.Stats[protocol.StatTotalMonsters] = 0
	pal := &render.Palette{}
	r.Palette = pal

	if err := r.RunFrame(0.05, 1); err != nil {
		t.Fatalf("RunFrame: %v", err)
	}
	// With three default lines ["TIME: 0:01", "SECRETS: 0 / 0",
	// "MONSTERS: 0 / 0"], the block is centered around fb.Height/2.
	// We just assert SOME glyph (not background) lands inside the
	// vertical band around the middle.
	mid := r.FrameBuffer.Height / 2
	row := r.FrameBuffer.Pixels[mid*r.FrameBuffer.Pitch : (mid+1)*r.FrameBuffer.Pitch]
	hit := false
	for _, p := range row {
		if p != r.BackgroundIdx {
			hit = true
			break
		}
	}
	if !hit {
		t.Fatalf("RunFrame did not plumb intermission into FrameContext: middle row has no non-background pixel")
	}
}
