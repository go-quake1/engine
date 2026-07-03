// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package client

import (
	"encoding/binary"
	"errors"
	"math"
	"testing"

	"github.com/go-quake1/engine/protocol"
	"github.com/go-quake1/engine/server"
)

// --- helpers --------------------------------------------------------

// defaultTickInput returns a TickInput that is valid for the input-
// guard checks (Dt and Sensitivity both non-negative) but otherwise
// quiescent (no mouse motion, no buttons, no impulses). Tests layer
// the per-case knobs on top.
func defaultTickInput() TickInput {
	return TickInput{
		Speeds:      DefaultInputSpeeds(),
		Sensitivity: 3,
		Dt:          0.0166, // ~60 Hz
		NowSec:      1.0,
	}
}

// pushFromServer queues a payload on the server side of the pair so
// the client side will read it on its next Tick. SendReliable +
// SendUnreliable share the same drain path so the choice is
// arbitrary; SendUnreliable matches the per-tick datagram shape.
func pushFromServer(t *testing.T, srv server.NetConn, data []byte) {
	t.Helper()
	if _, err := srv.SendUnreliable(data); err != nil {
		t.Fatalf("stunt-double SendUnreliable: %v", err)
	}
}

// faultyConn is a NetConn stub whose SendUnreliable returns a caller-
// supplied error. ReadMessage is the no-message branch. Everything
// else is a stub. Used by TestTick_SendUnreliableError to verify the
// error propagates verbatim.
type faultyConn struct {
	readErr    error // returned by ReadMessage; nil = "no messages"
	sendErr    error // returned by SendUnreliable
	sendRelErr error // returned by SendReliable
}

func (f *faultyConn) SendReliable(b []byte) (int, error)   { return len(b), f.sendRelErr }
func (f *faultyConn) SendUnreliable(b []byte) (int, error) { return 0, f.sendErr }
func (f *faultyConn) ReadMessage() (server.MessageKind, []byte, error) {
	if f.readErr != nil {
		return server.MessageNone, nil, f.readErr
	}
	return server.MessageNone, nil, nil
}
func (f *faultyConn) Address() string { return "fault:test" }
func (f *faultyConn) Close() error    { return nil }

// --- guard checks ---------------------------------------------------

func TestTick_NilState(t *testing.T) {
	cli, _ := server.NewLoopbackConn()
	if _, err := Tick(nil, cli, defaultTickInput(), [3]float32{}); !errors.Is(err, ErrTickNilState) {
		t.Errorf("got %v want ErrTickNilState", err)
	}
}

func TestTick_NilConn(t *testing.T) {
	st := NewState()
	if _, err := Tick(st, nil, defaultTickInput(), [3]float32{}); !errors.Is(err, ErrTickNilConn) {
		t.Errorf("got %v want ErrTickNilConn", err)
	}
}

func TestTick_BadInput(t *testing.T) {
	st := NewState()
	cli, _ := server.NewLoopbackConn()

	cases := []struct {
		name string
		in   TickInput
	}{
		{
			name: "negative Dt",
			in:   TickInput{Dt: -1, Sensitivity: 1},
		},
		{
			name: "negative Sensitivity",
			in:   TickInput{Dt: 0, Sensitivity: -1},
		},
		{
			name: "both negative",
			in:   TickInput{Dt: -1, Sensitivity: -1},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Tick(st, cli, tc.in, [3]float32{}); !errors.Is(err, ErrTickBadInput) {
				t.Errorf("got %v want ErrTickBadInput", err)
			}
		})
	}
}

// --- inbound paths --------------------------------------------------

func TestTick_InboundSingleNop(t *testing.T) {
	st := NewState()
	cli, srv := server.NewLoopbackConn()

	pushFromServer(t, srv, []byte{protocol.SvcNop})

	out, err := Tick(st, cli, defaultTickInput(), [3]float32{})
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if out.DatagramsRead != 1 {
		t.Errorf("DatagramsRead: got %d want 1", out.DatagramsRead)
	}
	if out.MessagesApplied != 1 {
		t.Errorf("MessagesApplied: got %d want 1", out.MessagesApplied)
	}
	if out.SentMove {
		t.Error("SentMove: got true want false (state is StateDisconnected)")
	}
}

func TestTick_InboundTwoDatagrams(t *testing.T) {
	st := NewState()
	cli, srv := server.NewLoopbackConn()

	pushFromServer(t, srv, []byte{protocol.SvcNop})
	pushFromServer(t, srv, []byte{protocol.SvcSignonNum, 1})

	out, err := Tick(st, cli, defaultTickInput(), [3]float32{})
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if out.DatagramsRead != 2 {
		t.Errorf("DatagramsRead: got %d want 2", out.DatagramsRead)
	}
	if out.MessagesApplied != 2 {
		t.Errorf("MessagesApplied: got %d want 2", out.MessagesApplied)
	}
}

func TestTick_InboundCorrupt(t *testing.T) {
	st := NewState()
	cli, srv := server.NewLoopbackConn()

	// SvcSound's body starts with a mandatory fieldMask byte; a lone
	// cmd byte with no body trips msg.Reader.Bad on the first ReadU8
	// of the decoder body -> ErrCorruptMessage.
	pushFromServer(t, srv, []byte{protocol.SvcSound})

	if _, err := Tick(st, cli, defaultTickInput(), [3]float32{}); !errors.Is(err, ErrCorruptMessage) {
		t.Errorf("got %v want ErrCorruptMessage", err)
	}
}

func TestTick_InboundUnknownOpcode(t *testing.T) {
	st := NewState()
	cli, srv := server.NewLoopbackConn()

	// 35 (0x23) sits in the gap between SvcCutscene (34) and the
	// FITZ-extension block (37+) and is not in SvcReader's table;
	// its high bit is clear so it does not route to decodeUpdate.
	pushFromServer(t, srv, []byte{35})

	if _, err := Tick(st, cli, defaultTickInput(), [3]float32{}); !errors.Is(err, ErrUnknownSvc) {
		t.Errorf("got %v want ErrUnknownSvc", err)
	}
}

func TestTick_InboundUnknownTEDropsFrame(t *testing.T) {
	st := NewState()
	cli, srv := server.NewLoopbackConn()

	// svc_temp_entity with sub-type 23 -- beyond the vanilla TE_* range
	// (0..13), so decodeTempEntity returns ErrTEUnknownKind. Rather than kill
	// the host, Tick drops the rest of the datagram and keeps going. The
	// trailing SvcNop is therefore NOT applied (we can't know the unknown TE's
	// body length to resume decoding past it).
	pushFromServer(t, srv, []byte{protocol.SvcTempEntity, 23, protocol.SvcNop})

	out, err := Tick(st, cli, defaultTickInput(), [3]float32{})
	if err != nil {
		t.Fatalf("Tick: got %v, want nil (unknown TE must be recoverable)", err)
	}
	if out.DatagramsDropped != 1 {
		t.Errorf("DatagramsDropped: got %d want 1", out.DatagramsDropped)
	}
	if out.MessagesApplied != 0 {
		t.Errorf("MessagesApplied: got %d want 0 (frame dropped before the trailing nop)", out.MessagesApplied)
	}
}

// --- inbound + state transition (signon completion) -----------------

func TestTick_SignonStage4TransitionsToConnected(t *testing.T) {
	st := NewState()
	if err := st.SetConnecting(); err != nil {
		t.Fatalf("SetConnecting: %v", err)
	}
	cli, srv := server.NewLoopbackConn()

	pushFromServer(t, srv, []byte{protocol.SvcSignonNum, 4})

	out, err := Tick(st, cli, defaultTickInput(), [3]float32{})
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if out.MessagesApplied != 1 {
		t.Errorf("MessagesApplied: got %d want 1", out.MessagesApplied)
	}
	if st.Connection != StateConnected {
		t.Errorf("Connection: got %v want StateConnected", st.Connection)
	}
	if !st.Spawned {
		t.Error("Spawned: got false want true")
	}
	// State is StateConnected by the END of this Tick, so the
	// outbound path WILL fire (same-frame post-signon). The signon-
	// completion test asserts the transition; the dedicated outbound
	// tests below assert the wire shape.
	if !out.SentMove {
		t.Error("SentMove: got false want true (state transitioned mid-tick)")
	}
}

// TestTick_WireDrivenSignonHandshake exercises the full
// Disconnected → Connecting → Connected walk that
// server.SendSignonHandshake's byte stream produces. Starting from
// StateDisconnected (no caller-side SetConnecting), push the same
// stage-byte sequence the server emits over the loopback
// (signonnum{1,2,3,4}); a single client Tick must drain all four
// stages + Apply them, with stage 1 driving the Connecting
// transition and stage 4 driving MarkSpawned. Proves the wire path
// the quake-tamago binary now relies on: the manual SetConnecting +
// MarkSpawned hack in main.go is removable because the wire bytes
// drive the lifecycle end-to-end.
func TestTick_WireDrivenSignonHandshake(t *testing.T) {
	st := NewState() // StateDisconnected default
	cli, srv := server.NewLoopbackConn()

	// Push the stage-byte tail (the relevant subset of the bytes
	// SendSignonHandshake queues; the serverinfo prefix is exercised
	// separately in server/signon_test.go + by other Apply tests).
	pushFromServer(t, srv, []byte{
		protocol.SvcSignonNum, 1,
		protocol.SvcSignonNum, 2,
		protocol.SvcSignonNum, 3,
		protocol.SvcSignonNum, 4,
	})

	out, err := Tick(st, cli, defaultTickInput(), [3]float32{})
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if out.MessagesApplied != 4 {
		t.Errorf("MessagesApplied: got %d want 4 (one per stage byte-pair)", out.MessagesApplied)
	}
	if st.Connection != StateConnected {
		t.Errorf("Connection: got %v want StateConnected (wire path failed)", st.Connection)
	}
	if !st.Spawned {
		t.Error("Spawned: got false want true (stage 4 should MarkSpawned)")
	}
}

// --- outbound: short-circuits ---------------------------------------

func TestTick_OutboundShortCircuit_Disconnected(t *testing.T) {
	st := NewState() // StateDisconnected default
	cli, srv := server.NewLoopbackConn()

	out, err := Tick(st, cli, defaultTickInput(), [3]float32{})
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if out.SentMove {
		t.Error("SentMove: got true want false")
	}
	// Verify nothing landed on the server's inbox.
	kind, data, err := srv.ReadMessage()
	if err != nil {
		t.Fatalf("server ReadMessage: %v", err)
	}
	if kind != server.MessageNone || data != nil {
		t.Errorf("server inbox: got kind=%v data=%v want (None, nil)", kind, data)
	}
}

func TestTick_OutboundShortCircuit_Connecting(t *testing.T) {
	st := NewState()
	if err := st.SetConnecting(); err != nil {
		t.Fatalf("SetConnecting: %v", err)
	}
	cli, srv := server.NewLoopbackConn()

	out, err := Tick(st, cli, defaultTickInput(), [3]float32{})
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if out.SentMove {
		t.Error("SentMove: got true want false (clc_move gated on StateConnected)")
	}
	// StateConnecting fires the one-shot wire 'spawn' clc_stringcmd
	// (the server-side ParseClcStringCmd trigger); it is the ONLY
	// outbound the connecting tick produces. The clc_move path stays
	// gated on StateConnected (out.SentMove guarded above).
	kind, data, err := srv.ReadMessage()
	if err != nil {
		t.Fatalf("server ReadMessage: %v", err)
	}
	if kind != server.MessageReliable {
		t.Fatalf("server inbox: got kind=%v want MessageReliable (spawn stringcmd)", kind)
	}
	if len(data) < 2 || data[0] != protocol.ClcStringCmd {
		t.Fatalf("data: got %v want ClcStringCmd+payload", data)
	}
	if string(data[1:len(data)-1]) != "spawn" {
		t.Errorf("payload: got %q want \"spawn\"", string(data[1:len(data)-1]))
	}
	if !st.SentSpawn {
		t.Error("SentSpawn: got false want true (latch should flip on emit)")
	}
}

// TestTick_SpawnStringCmd_SendReliableError propagates the conn's
// SendReliable failure when the wire 'spawn' emit hits a fault.
func TestTick_SpawnStringCmd_SendReliableError(t *testing.T) {
	st := NewState()
	if err := st.SetConnecting(); err != nil {
		t.Fatalf("SetConnecting: %v", err)
	}
	sentinel := errors.New("relsend boom")
	conn := &faultyConn{sendRelErr: sentinel}
	if _, err := Tick(st, conn, defaultTickInput(), [3]float32{}); !errors.Is(err, sentinel) {
		t.Errorf("got %v want sentinel", err)
	}
	if st.SentSpawn {
		t.Error("SentSpawn: got true want false (error must not latch)")
	}
}

// TestTick_SpawnStringCmdEmittedOnce verifies the SentSpawn latch:
// the FIRST StateConnecting tick emits the stringcmd; the SECOND
// tick is a pure inbound-drain (no retransmit). Matches the wire
// contract -- the server-side ParseClcStringCmd needs exactly one
// "spawn" to flip Client.Spawned + queue signonnum(4).
func TestTick_SpawnStringCmdEmittedOnce(t *testing.T) {
	st := NewState()
	if err := st.SetConnecting(); err != nil {
		t.Fatalf("SetConnecting: %v", err)
	}
	cli, srv := server.NewLoopbackConn()

	// First tick: spawn stringcmd.
	if _, err := Tick(st, cli, defaultTickInput(), [3]float32{}); err != nil {
		t.Fatalf("Tick 1: %v", err)
	}
	if !st.SentSpawn {
		t.Fatal("SentSpawn after tick 1: got false want true")
	}
	if _, _, err := srv.ReadMessage(); err != nil {
		t.Fatalf("drain tick 1 msg: %v", err)
	}

	// Second tick: SHOULD NOT retransmit; server inbox empty.
	if _, err := Tick(st, cli, defaultTickInput(), [3]float32{}); err != nil {
		t.Fatalf("Tick 2: %v", err)
	}
	kind, _, err := srv.ReadMessage()
	if err != nil {
		t.Fatalf("server ReadMessage tick 2: %v", err)
	}
	if kind != server.MessageNone {
		t.Errorf("server inbox tick 2: got kind=%v want MessageNone (latch failed)", kind)
	}
}

// --- outbound: happy path -------------------------------------------

func TestTick_OutboundSendsClcMove(t *testing.T) {
	st := NewState()
	st.Connection = StateConnected
	cli, srv := server.NewLoopbackConn()

	in := defaultTickInput()
	in.NowSec = 12.5

	out, err := Tick(st, cli, in, [3]float32{0, 90, 0})
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if !out.SentMove {
		t.Error("SentMove: got false want true")
	}
	if out.DatagramsRead != 0 {
		t.Errorf("DatagramsRead: got %d want 0", out.DatagramsRead)
	}

	kind, data, err := srv.ReadMessage()
	if err != nil {
		t.Fatalf("server ReadMessage: %v", err)
	}
	if kind != server.MessageUnreliable {
		t.Errorf("kind: got %v want MessageUnreliable", kind)
	}
	if len(data) == 0 || data[0] != protocol.ClcMove {
		t.Fatalf("first byte: got %v want ClcMove(%d)", data, protocol.ClcMove)
	}
	// Wire shape: 1 (opcode) + 4 (sendTime) + 3 (angles) + 6 (3 shorts)
	// + 1 (buttons) + 1 (impulse) = 16 bytes for an idle frame.
	const wantLen = 16
	if len(data) != wantLen {
		t.Errorf("wire len: got %d want %d", len(data), wantLen)
	}
	// Bytes 1..5 are the IEEE-754 LE sendTime.
	gotTime := math.Float32frombits(binary.LittleEndian.Uint32(data[1:5]))
	if gotTime != in.NowSec {
		t.Errorf("sendTime: got %v want %v", gotTime, in.NowSec)
	}
}

// --- outbound: input math reflected on the wire ---------------------

func TestTick_ForwardButtonProducesPositiveForwardMove(t *testing.T) {
	st := NewState()
	st.Connection = StateConnected
	cli, srv := server.NewLoopbackConn()

	in := defaultTickInput()
	// Hold the +forward key for the entire frame.
	in.Buttons = &MovementButtons{Forward: ButtonState{Pressed: 1}}

	if _, err := Tick(st, cli, in, [3]float32{}); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	_, data, _ := srv.ReadMessage()
	// ForwardMove sits at offset 8 (1 opcode + 4 sendTime + 3 angles)
	// as a signed 16-bit little-endian short.
	fm := int16(binary.LittleEndian.Uint16(data[8:10]))
	if fm <= 0 {
		t.Errorf("ForwardMove: got %d want > 0", fm)
	}
}

func TestTick_MouseDXDecreasesYaw(t *testing.T) {
	st := NewState()
	st.Connection = StateConnected
	cli, srv := server.NewLoopbackConn()

	in := defaultTickInput()
	in.MouseDX = 100 // positive dx => yaw decreases (Q1 sign convention)

	startAngles := [3]float32{0, 50, 0} // pitch, yaw, roll
	out, err := Tick(st, cli, in, startAngles)
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if out.ViewAngles[1] >= startAngles[1] {
		t.Errorf("yaw: got %v want < %v (positive MouseDX should decrease yaw)",
			out.ViewAngles[1], startAngles[1])
	}

	// Verify the angle that made it into the clc_move matches the
	// angle reported in TickOutput.ViewAngles. Wire layout:
	// 1 opcode + 4 sendTime + then pitch/yaw/roll as 1-byte
	// MSG_WriteAngle each (value*256/360). The encoded yaw byte must
	// round-trip to the same coarse angle.
	_, data, _ := srv.ReadMessage()
	const yawOff = 1 + 4 + 1 // skip opcode, sendTime, pitch byte
	wireYawByte := data[yawOff]
	// MSG_WriteAngle quantises via floor(f*256/360 + 0.5); the wire
	// byte must match TickOutput.ViewAngles[1] under the same
	// normalisation, confirming the angle reported back to the
	// caller is the same one that hit the wire.
	wantByte := byte(int(math.Floor(float64(out.ViewAngles[1])*(256.0/360.0)+0.5)) & 0xff)
	if wireYawByte != wantByte {
		t.Errorf("wire yaw byte: got %d want %d (TickOutput.ViewAngles[1]=%v)",
			wireYawByte, wantByte, out.ViewAngles[1])
	}
}

// --- outbound: encoder / conn error propagation ---------------------

func TestTick_SendUnreliableError(t *testing.T) {
	st := NewState()
	st.Connection = StateConnected

	sentinel := errors.New("send-side fault")
	conn := &faultyConn{sendErr: sentinel}

	_, err := Tick(st, conn, defaultTickInput(), [3]float32{})
	if !errors.Is(err, sentinel) {
		t.Errorf("got %v want %v", err, sentinel)
	}
}

func TestTick_ReadMessageError(t *testing.T) {
	st := NewState()
	sentinel := errors.New("read-side fault")
	conn := &faultyConn{readErr: sentinel}

	_, err := Tick(st, conn, defaultTickInput(), [3]float32{})
	if !errors.Is(err, sentinel) {
		t.Errorf("got %v want %v", err, sentinel)
	}
}

// --- inbound: Apply error propagation -------------------------------

// applyErrConn is a NetConn stub that returns one canned payload then
// reports MessageNone. Used to drive Tick into Apply's error path.
type applyErrConn struct {
	payload []byte
	sent    bool
}

func (a *applyErrConn) SendReliable(b []byte) (int, error)   { return len(b), nil }
func (a *applyErrConn) SendUnreliable(b []byte) (int, error) { return len(b), nil }
func (a *applyErrConn) ReadMessage() (server.MessageKind, []byte, error) {
	if a.sent {
		return server.MessageNone, nil, nil
	}
	a.sent = true
	return server.MessageUnreliable, a.payload, nil
}
func (a *applyErrConn) Address() string { return "apply-err:test" }
func (a *applyErrConn) Close() error    { return nil }

func TestTick_ApplyErrorPropagates(t *testing.T) {
	// SvcSignonNum stage 4 from StateDisconnected: applySignonNum
	// calls state.MarkSpawned which returns ErrNotConnecting because
	// the state is not StateConnecting. Apply wraps that as
	// ErrApplyBadState; Tick must surface the wrapped error.
	st := NewState() // StateDisconnected
	conn := &applyErrConn{payload: []byte{protocol.SvcSignonNum, 4}}

	_, err := Tick(st, conn, defaultTickInput(), [3]float32{})
	if !errors.Is(err, ErrApplyBadState) {
		t.Errorf("got %v want ErrApplyBadState", err)
	}
}

// --- inbound: stop on MessageDisconnect -----------------------------

func TestTick_StopsOnMessageDisconnect(t *testing.T) {
	st := NewState()
	cli, srv := server.NewLoopbackConn()

	pushFromServer(t, srv, []byte{protocol.SvcNop})
	// Close the server side: the client-side ReadMessage will drain
	// the one queued message then return (MessageDisconnect, nil, nil)
	// on the next call. Tick must break out without erroring.
	if err := srv.Close(); err != nil {
		t.Fatalf("srv.Close: %v", err)
	}

	out, err := Tick(st, cli, defaultTickInput(), [3]float32{})
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if out.DatagramsRead != 1 {
		t.Errorf("DatagramsRead: got %d want 1", out.DatagramsRead)
	}
	if out.MessagesApplied != 1 {
		t.Errorf("MessagesApplied: got %d want 1", out.MessagesApplied)
	}
}
