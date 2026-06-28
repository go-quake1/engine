// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package runloop

import (
	"bytes"
	"errors"
	"io"
	"testing"

	"github.com/go-quake1/engine/backend"
	"github.com/go-quake1/engine/demo"
	"github.com/go-quake1/engine/menu"
)

// makeDemoBytes constructs a 3-tick .dem blob with NOP-only message
// bodies. Each tick advances state.MsgTime by the default 1/20s and
// stamps the tick's recorded view-angles onto r.ViewAngles.
func makeDemoBytes(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := demo.EncodeHeader(&buf, "0"); err != nil {
		t.Fatalf("EncodeHeader: %v", err)
	}
	ticks := []demo.DemoTick{
		{ViewAngles: [3]float32{10, 0, 0}, Message: []byte{0x01 /* SvcNop */}},
		{ViewAngles: [3]float32{20, 0, 0}, Message: []byte{0x01}},
		{ViewAngles: [3]float32{30, 0, 0}, Message: []byte{0x01}},
	}
	for i := range ticks {
		if err := demo.EncodeTick(&buf, ticks[i]); err != nil {
			t.Fatalf("EncodeTick[%d]: %v", i, err)
		}
	}
	return buf.Bytes()
}

func newDemoReader(t *testing.T) *demo.Reader {
	t.Helper()
	rd, err := demo.NewReader(bytes.NewReader(makeDemoBytes(t)))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	return rd
}

// ----- demoActive predicate --------------------------------------------------

func TestDemoActive_NilRunnerFalse(t *testing.T) {
	var r *Runner
	if r.demoActive() {
		t.Errorf("nil runner demoActive = true, want false")
	}
}

func TestDemoActive_NoDemoFalse(t *testing.T) {
	r := &Runner{}
	if r.demoActive() {
		t.Errorf("no demo demoActive = true, want false")
	}
}

func TestDemoActive_NoReaderFalse(t *testing.T) {
	r := &Runner{Demo: &Demo{}}
	if r.demoActive() {
		t.Errorf("no reader demoActive = true, want false")
	}
}

func TestDemoActive_NilMenuTrue(t *testing.T) {
	r := &Runner{Demo: &Demo{Reader: newDemoReader(t)}}
	if !r.demoActive() {
		t.Errorf("nil menu demoActive = false, want true")
	}
}

func TestDemoActive_MenuStateMainTrue(t *testing.T) {
	r := &Runner{
		Demo: &Demo{Reader: newDemoReader(t)},
		Menu: &menu.Menu{State: menu.StateMain},
	}
	if !r.demoActive() {
		t.Errorf("menu StateMain demoActive = false, want true")
	}
}

func TestDemoActive_MenuStateNoneFalse(t *testing.T) {
	// StateNone = "game running, no menu" -- demo MUST NOT play, else
	// the attract loop overrides the player's live session.
	r := &Runner{
		Demo: &Demo{Reader: newDemoReader(t)},
		Menu: &menu.Menu{State: menu.StateNone},
	}
	if r.demoActive() {
		t.Errorf("menu StateNone demoActive = true, want false (gameplay path)")
	}
}

func TestDemoActive_MenuStateSkillFalse(t *testing.T) {
	r := &Runner{
		Demo: &Demo{Reader: newDemoReader(t)},
		Menu: &menu.Menu{State: menu.StateSkill},
	}
	if r.demoActive() {
		t.Errorf("menu StateSkill demoActive = true, want false (sub-menu pauses attract loop)")
	}
}

// ----- interruptDemoOnInput --------------------------------------------------

func TestInterrupt_NilDemoFalse(t *testing.T) {
	r := &Runner{}
	if r.interruptDemoOnInput(backend.InputSnapshot{KeysDown: []backend.KeyCode{backend.KeyW}}) {
		t.Errorf("nil demo interrupt = true, want false")
	}
}

func TestInterrupt_NoKeyDownFalse(t *testing.T) {
	d := &Demo{Reader: newDemoReader(t)}
	r := &Runner{Demo: d}
	if r.interruptDemoOnInput(backend.InputSnapshot{}) {
		t.Errorf("empty snap interrupt = true, want false")
	}
	if r.Demo != d {
		t.Errorf("demo cleared on empty snap")
	}
}

func TestInterrupt_KeyDownClearsDemo(t *testing.T) {
	r := &Runner{Demo: &Demo{Reader: newDemoReader(t)}}
	if !r.interruptDemoOnInput(backend.InputSnapshot{KeysDown: []backend.KeyCode{backend.KeyW}}) {
		t.Errorf("key-down interrupt = false, want true")
	}
	if r.Demo != nil {
		t.Errorf("demo not cleared post-interrupt")
	}
}

func TestInterrupt_MenuActiveLeavesDemo(t *testing.T) {
	d := &Demo{Reader: newDemoReader(t)}
	r := &Runner{Demo: d, Menu: menu.New()} // menu boots at StateMain (active)
	if r.interruptDemoOnInput(backend.InputSnapshot{KeysDown: []backend.KeyCode{backend.KeyMouse1}}) {
		t.Errorf("interrupt fired while menu active, want skipped")
	}
	if r.Demo != d {
		t.Errorf("demo cleared while menu active")
	}
}

// ----- playDemoTick happy path ----------------------------------------------

func TestPlayDemoTick_AdvancesAnglesAndCount(t *testing.T) {
	rec := backend.NewRecorder(0, 0)
	r, _ := newRunner(t, rec)
	r.Demo = &Demo{Reader: newDemoReader(t)}

	if err := r.playDemoTick(); err != nil {
		t.Fatalf("playDemoTick: %v", err)
	}
	if r.ViewAngles != ([3]float32{10, 0, 0}) {
		t.Errorf("angles = %v want [10 0 0]", r.ViewAngles)
	}
	if r.Demo.FrameCount != 1 {
		t.Errorf("FrameCount = %d want 1", r.Demo.FrameCount)
	}
}

// ----- playDemoTick EOF + restart -------------------------------------------

func TestPlayDemoTick_EOFNoRestartClearsDemo(t *testing.T) {
	rec := backend.NewRecorder(0, 0)
	r, _ := newRunner(t, rec)
	// Reader with no ticks -> first NextFrame returns io.EOF.
	rd, err := demo.NewReader(bytes.NewReader([]byte("\n")))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	r.Demo = &Demo{Reader: rd}

	if err := r.playDemoTick(); err != nil {
		t.Fatalf("playDemoTick: %v", err)
	}
	if r.Demo != nil {
		t.Errorf("demo not cleared at EOF without Restart")
	}
}

func TestPlayDemoTick_EOFRestartRewinds(t *testing.T) {
	rec := backend.NewRecorder(0, 0)
	r, _ := newRunner(t, rec)
	blob := makeDemoBytes(t)
	// Start at end-of-stream: header-only reader, EOF on first tick.
	startRd, err := demo.NewReader(bytes.NewReader([]byte("\n")))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	restarts := 0
	r.Demo = &Demo{
		Reader: startRd,
		Restart: func() (*demo.Reader, error) {
			restarts++
			return demo.NewReader(bytes.NewReader(blob))
		},
	}

	if err := r.playDemoTick(); err != nil {
		t.Fatalf("playDemoTick (EOF rewind): %v", err)
	}
	if restarts != 1 {
		t.Errorf("restart calls = %d, want 1", restarts)
	}
	if r.Demo == nil {
		t.Fatalf("demo cleared despite successful restart")
	}
	// Next tic should now decode the first body of the fresh reader.
	if err := r.playDemoTick(); err != nil {
		t.Fatalf("post-restart tick: %v", err)
	}
	if r.ViewAngles != ([3]float32{10, 0, 0}) {
		t.Errorf("angles = %v want [10 0 0] (first restarted tic)", r.ViewAngles)
	}
}

var errBoom = errors.New("restart boom")

func TestPlayDemoTick_RestartErrorClearsAndPropagates(t *testing.T) {
	rec := backend.NewRecorder(0, 0)
	r, _ := newRunner(t, rec)
	rd, err := demo.NewReader(bytes.NewReader([]byte("\n"))) // EOF on first NextFrame
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	r.Demo = &Demo{
		Reader:  rd,
		Restart: func() (*demo.Reader, error) { return nil, errBoom },
	}
	err = r.playDemoTick()
	if !errors.Is(err, errBoom) {
		t.Fatalf("err = %v want errBoom", err)
	}
	if r.Demo != nil {
		t.Errorf("demo not cleared on restart failure")
	}
}

// ----- playDemoTick mid-tic IO error ----------------------------------------

// truncatedReader returns nil bytes + an error after a fixed number
// of successful reads. Lets the test simulate a mid-tic IO failure
// inside ParseTic.
type truncatedReader struct {
	data []byte
	pos  int
	err  error
}

func (t *truncatedReader) Read(p []byte) (int, error) {
	if t.pos >= len(t.data) {
		return 0, t.err
	}
	n := copy(p, t.data[t.pos:])
	t.pos += n
	return n, nil
}

func TestPlayDemoTick_NonEOFErrorClearsAndPropagates(t *testing.T) {
	rec := backend.NewRecorder(0, 0)
	r, _ := newRunner(t, rec)
	hdr := []byte("\n")              // empty CD-track header
	bodyStart := []byte{0x05, 0x00}  // partial msglen prefix
	want := errors.New("io blew up") // any non-EOF error
	src := &truncatedReader{data: append(hdr, bodyStart...), err: want}
	rd, err := demo.NewReader(src)
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	r.Demo = &Demo{Reader: rd}

	gotErr := r.playDemoTick()
	if gotErr == nil {
		t.Fatalf("playDemoTick: nil err, want %v", want)
	}
	if r.Demo != nil {
		t.Errorf("demo not cleared on non-EOF err")
	}
}

// ----- playDemoTick custom PlayerOpts honoured ------------------------------

func TestPlayDemoTick_OnFrameFires(t *testing.T) {
	rec := backend.NewRecorder(0, 0)
	r, _ := newRunner(t, rec)
	var gotFrame int
	var gotAngles [3]float32
	r.Demo = &Demo{
		Reader: newDemoReader(t),
		OnFrame: func(frame int, angles [3]float32) {
			gotFrame = frame
			gotAngles = angles
		},
	}
	if err := r.playDemoTick(); err != nil {
		t.Fatalf("playDemoTick: %v", err)
	}
	if gotFrame != 1 {
		t.Errorf("OnFrame frame = %d, want 1", gotFrame)
	}
	if gotAngles != ([3]float32{10, 0, 0}) {
		t.Errorf("OnFrame angles = %v, want first-tick angles", gotAngles)
	}
}

func TestPlayDemoTick_CustomPlayerOptsThreaded(t *testing.T) {
	rec := backend.NewRecorder(0, 0)
	r, _ := newRunner(t, rec)
	r.Demo = &Demo{
		Reader:     newDemoReader(t),
		PlayerOpts: demo.PlayerOpts{Protocol: 15, TickDelta: 0.1, SkipUnknownSvc: true},
	}
	if err := r.playDemoTick(); err != nil {
		t.Fatalf("playDemoTick: %v", err)
	}
}

// ----- RunFrame demo integration --------------------------------------------

func TestRunFrame_DemoActiveSkipsHostFrame(t *testing.T) {
	rec := backend.NewRecorder(0, 0)
	r, _ := newRunner(t, rec)
	r.Demo = &Demo{Reader: newDemoReader(t)}
	// No menu -> demoActive returns true unconditionally.
	r.Menu = nil

	host := r.Host.(*fakeHost)
	if err := r.RunFrame(0.05, 1); err != nil {
		t.Fatalf("RunFrame: %v", err)
	}
	if host.calls != 0 {
		t.Errorf("demo active: host.Frame calls = %d, want 0", host.calls)
	}
	if r.Demo == nil || r.Demo.FrameCount != 1 {
		t.Errorf("demo FrameCount not advanced: %+v", r.Demo)
	}
	if r.ViewAngles != ([3]float32{10, 0, 0}) {
		t.Errorf("ViewAngles = %v want first-tick angles", r.ViewAngles)
	}
}

// recorderWithKeysDemo is a tiny wrapper around backend.Recorder that
// returns one InputSnapshot with a single KeyDown event on the first
// call, then empty snapshots thereafter. Used by the demo-interrupt
// integration test.
type recorderWithKeysDemo struct {
	*backend.Recorder
	snap     backend.InputSnapshot
	consumed bool
}

func (r *recorderWithKeysDemo) PollInput() (backend.InputSnapshot, error) {
	if r.consumed {
		return backend.InputSnapshot{}, nil
	}
	r.consumed = true
	return r.snap, nil
}

func TestRunFrame_KeyDownInterruptsDemo(t *testing.T) {
	rec := backend.NewRecorder(0, 0)
	r, _ := newRunner(t, rec)
	rwk := &recorderWithKeysDemo{
		Recorder: rec,
		snap: backend.InputSnapshot{
			KeysDown: []backend.KeyCode{backend.KeyW},
		},
	}
	r.Backend = rwk
	r.Demo = &Demo{Reader: newDemoReader(t)}
	r.Menu = nil

	host := r.Host.(*fakeHost)
	if err := r.RunFrame(0.05, 1); err != nil {
		t.Fatalf("RunFrame: %v", err)
	}
	if r.Demo != nil {
		t.Errorf("KeyDown didn't clear demo: %+v", r.Demo)
	}
	if host.calls != 1 {
		t.Errorf("after interrupt: host.Frame calls = %d, want 1 (live world resumed)", host.calls)
	}
}

func TestRunFrame_DemoAtTitleSkipsHostFrame(t *testing.T) {
	// Title menu + demo loaded == attract loop. host.Frame skipped;
	// demo advances; the menu overlay still renders.
	rec := backend.NewRecorder(0, 0)
	r, _ := newRunner(t, rec)
	r.Demo = &Demo{Reader: newDemoReader(t)}
	r.Menu = menu.New() // StateMain

	host := r.Host.(*fakeHost)
	if err := r.RunFrame(0.05, 1); err != nil {
		t.Fatalf("RunFrame: %v", err)
	}
	if host.calls != 0 {
		t.Errorf("attract loop: host.Frame calls = %d, want 0", host.calls)
	}
	if r.Demo == nil || r.Demo.FrameCount != 1 {
		t.Errorf("demo did not advance under title menu")
	}
}

func TestRunFrame_DemoUnderActiveSubMenuPauses(t *testing.T) {
	// Sub-menu (e.g. StateSkill) -> attract loop OFF, world paused.
	rec := backend.NewRecorder(0, 0)
	r, _ := newRunner(t, rec)
	r.Demo = &Demo{Reader: newDemoReader(t)}
	r.Menu = &menu.Menu{State: menu.StateSkill}

	host := r.Host.(*fakeHost)
	if err := r.RunFrame(0.05, 1); err != nil {
		t.Fatalf("RunFrame: %v", err)
	}
	if host.calls != 0 {
		t.Errorf("sub-menu: host.Frame calls = %d, want 0", host.calls)
	}
	if r.Demo == nil || r.Demo.FrameCount != 0 {
		t.Errorf("demo advanced under non-title menu: %+v", r.Demo)
	}
}

func TestRunFrame_DemoEOFRewindsViaRestart(t *testing.T) {
	rec := backend.NewRecorder(0, 0)
	r, _ := newRunner(t, rec)
	blob := makeDemoBytes(t)
	r.Menu = nil
	// One-tick demo so the second RunFrame trips EOF + rewind.
	rd, err := demo.NewReader(bytes.NewReader(blob))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	restarts := 0
	r.Demo = &Demo{
		Reader: rd,
		Restart: func() (*demo.Reader, error) {
			restarts++
			return demo.NewReader(bytes.NewReader(blob))
		},
	}

	// Three live ticks -> all three demo bodies consumed.
	for i := 0; i < 3; i++ {
		if err := r.RunFrame(0.05, float32(i)); err != nil {
			t.Fatalf("tic %d RunFrame: %v", i, err)
		}
	}
	// Fourth tic: NextFrame returns io.EOF, Restart fires.
	if err := r.RunFrame(0.05, 4); err != nil {
		t.Fatalf("tic 4 RunFrame (EOF rewind): %v", err)
	}
	if restarts != 1 {
		t.Errorf("restart calls = %d, want 1", restarts)
	}
	if r.Demo == nil {
		t.Errorf("demo cleared despite successful restart")
	}
}

// corruptDemoBlob constructs a header + one tick whose body is the
// SvcServerInfo opcode (=0x0b=11) followed by a single stray byte.
// The SvcReader's decodeServerInfo helper expects a 4-byte protocol
// long + a max-clients byte + a game-type byte + a NUL-terminated
// level name + two NUL-terminated string lists; the 1-byte tail can
// only ever trip a short-read inside the body -> the parser flags
// ErrCorruptMessage. Used to drive the playDemoTick swallow-and-
// stop-demo path the wasmbox tic-210 freeze fix introduced.
func corruptDemoBlob(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := demo.EncodeHeader(&buf, ""); err != nil {
		t.Fatalf("EncodeHeader: %v", err)
	}
	tick := demo.DemoTick{
		Message: []byte{0x0b /* SvcServerInfo opcode */, 0x04 /* stray body byte */},
	}
	if err := demo.EncodeTick(&buf, tick); err != nil {
		t.Fatalf("EncodeTick: %v", err)
	}
	return buf.Bytes()
}

// unknownSvcDemoBlob constructs a header + one tick whose body opens
// with svc opcode 100 -- a value SvcReader does not dispatch (it
// falls through to ErrUnknownSvc). With PlayerOpts.SkipUnknownSvc =
// false (the default the runloop substitutes when none is set) the
// SvcReader surfaces the sentinel, which playDemoTick now treats the
// same way as ErrCorruptMessage: clear r.Demo + return nil so the
// game keeps running.
func unknownSvcDemoBlob(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := demo.EncodeHeader(&buf, ""); err != nil {
		t.Fatalf("EncodeHeader: %v", err)
	}
	tick := demo.DemoTick{
		Message: []byte{100 /* svc opcode not in dispatch table */},
	}
	if err := demo.EncodeTick(&buf, tick); err != nil {
		t.Fatalf("EncodeTick: %v", err)
	}
	return buf.Bytes()
}

func TestPlayDemoTick_CorruptMessageSwallowedAndDemoCleared(t *testing.T) {
	rec := backend.NewRecorder(0, 0)
	r, _ := newRunner(t, rec)
	rd, err := demo.NewReader(bytes.NewReader(corruptDemoBlob(t)))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	r.Demo = &Demo{Reader: rd}
	if err := r.playDemoTick(); err != nil {
		t.Fatalf("playDemoTick: %v, want nil (ErrCorruptMessage swallowed)", err)
	}
	if r.Demo != nil {
		t.Errorf("ErrCorruptMessage did not clear the demo (the next tic would re-trip the same broken body)")
	}
}

func TestPlayDemoTick_UnknownSvcSwallowedAndDemoCleared(t *testing.T) {
	rec := backend.NewRecorder(0, 0)
	r, _ := newRunner(t, rec)
	rd, err := demo.NewReader(bytes.NewReader(unknownSvcDemoBlob(t)))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	r.Demo = &Demo{Reader: rd}
	if err := r.playDemoTick(); err != nil {
		t.Fatalf("playDemoTick: %v, want nil (ErrUnknownSvc swallowed)", err)
	}
	if r.Demo != nil {
		t.Errorf("ErrUnknownSvc did not clear the demo (the cursor would stay off-alignment on the next tic)")
	}
}

// applyErrorDemoBlob constructs a header + one tick whose body is
// svc_signonnum stage 4 (opcode 25 = 0x19, payload byte 0x04). From
// StateDisconnected client.Apply rejects that transition with
// ErrApplyBadState -- an APPLY-time error, NOT a decode-time error.
// The fix that swallows ErrCorruptMessage / ErrUnknownSvc must NOT
// also swallow Apply errors: those signal a genuine fault the
// embedder wants to see.
func applyErrorDemoBlob(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := demo.EncodeHeader(&buf, ""); err != nil {
		t.Fatalf("EncodeHeader: %v", err)
	}
	tick := demo.DemoTick{
		Message: []byte{0x19 /* SvcSignonNum opcode */, 0x04 /* stage 4 */},
	}
	if err := demo.EncodeTick(&buf, tick); err != nil {
		t.Fatalf("EncodeTick: %v", err)
	}
	return buf.Bytes()
}

func TestPlayDemoTick_ApplyErrorStillPropagates(t *testing.T) {
	rec := backend.NewRecorder(0, 0)
	r, _ := newRunner(t, rec)
	rd, err := demo.NewReader(bytes.NewReader(applyErrorDemoBlob(t)))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	r.Demo = &Demo{Reader: rd}
	if err := r.playDemoTick(); err == nil {
		t.Fatalf("playDemoTick: nil err, want PlayTick Apply failure")
	}
	if r.Demo == nil {
		t.Errorf("Apply error cleared the demo (it shouldn't -- only decode errors clear)")
	}
}

func TestRunFrame_DemoCorruptMessageDoesNotPropagate(t *testing.T) {
	rec := backend.NewRecorder(0, 0)
	r, _ := newRunner(t, rec)
	rd, err := demo.NewReader(bytes.NewReader(corruptDemoBlob(t)))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	r.Demo = &Demo{Reader: rd}
	r.Menu = nil

	if err := r.RunFrame(0.05, 1); err != nil {
		t.Fatalf("RunFrame: %v, want nil (ErrCorruptMessage must not crash RunUntilQuit)", err)
	}
	if r.Demo != nil {
		t.Errorf("RunFrame did not clear the broken demo")
	}
}

func TestPlayDemoTick_IoEOFAtBoundary(t *testing.T) {
	// Whitebox: confirm errors.Is(io.EOF) path is hit (= the
	// EOF arm). Already covered by EOFNoRestart + EOFRestart
	// but we also assert the underlying error type is exactly
	// io.EOF so the predicate stays correct under refactor.
	rd, err := demo.NewReader(bytes.NewReader([]byte("\n")))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	_, perr := rd.NextFrame()
	if !errors.Is(perr, io.EOF) {
		t.Fatalf("NextFrame err = %v want io.EOF", perr)
	}
}
