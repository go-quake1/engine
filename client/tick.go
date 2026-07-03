// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package client

import (
	"errors"

	"github.com/go-quake1/engine/msg"
	"github.com/go-quake1/engine/protocol"
	"github.com/go-quake1/engine/server"
	"github.com/go-quake1/engine/sizebuf"
)

// TickInput is the per-frame caller-supplied input bundle. Frontends
// (SDL / Wayland / DirectFB / TamaGo IO) fill this from their input
// source each tick. tyrquake: IN_Move + the in_* cvar reads.
//
// Buttons is a POINTER, not a copy: [KeyState] (called by [BaseMove]
// and [AdjustAngles] downstream) clears the per-frame impulse bits as
// it samples them, and the drain MUST land on the caller's persistent
// kbutton state -- a stack copy would let the down-edge bit live on
// forever and KeyState would report a constant 0.5 every frame the key
// stayed held, collapsing the move axes to cl_*speed/2 on the wire.
type TickInput struct {
	Buttons       *MovementButtons // current button state (with this-frame events merged in)
	MouseDX       float32          // pixel delta since last tick
	MouseDY       float32
	Sensitivity   float32     // cl_sensitivity cvar (typical default 3)
	Speeds        InputSpeeds // per-axis sensitivity bundle (DefaultInputSpeeds() ok)
	Dt            float32     // frame delta in seconds
	NowSec        float32     // wall-clock-like time -- threaded to Apply + EncodeClcMove
	ActionButtons uint8       // BUTTON_ATTACK / BUTTON_JUMP / ... bits ORed up by caller
	Impulse       uint8       // +impulse N this tick (0 for none)
}

// TickOutput is the per-frame summary. Frontends use it for
// telemetry / debug HUD; nothing in this layer reads it.
type TickOutput struct {
	MessagesApplied int        // total Decoded values dispatched to Apply this tick
	DatagramsRead   int        // number of NetConn payloads consumed
	SentMove        bool       // true iff a clc_move was successfully sent
	ViewAngles      [3]float32 // pitch/yaw/roll after the input adjustments
}

// ErrTickNilState is returned by [Tick] when state == nil.
var ErrTickNilState = errors.New("client: Tick called with nil State")

// ErrTickNilConn is returned by [Tick] when conn == nil.
var ErrTickNilConn = errors.New("client: Tick called with nil NetConn")

// ErrTickBadInput is returned by [Tick] when TickInput.Dt < 0 or
// TickInput.Sensitivity < 0 (the two invariants the input-math
// helpers assume but do not themselves enforce).
var ErrTickBadInput = errors.New("client: Tick input rejected (negative dt or sensitivity)")

// tickOutBufSize is the fresh-each-tick outbound sizebuf cap. The
// clc_move payload is fixed at 16 bytes (1 opcode + 4 float +
// 3*1 angle + 3*2 short + 1 buttons + 1 impulse); 64 leaves headroom
// for the FITZ-extend / QSG2-checksum follow-up without re-sizing.
const tickOutBufSize = 64

// Tick runs one client frame: drains inbound NetConn messages,
// applies each to state, then (post-signon) builds + sends one
// clc_move.
//
// INBOUND PROTOCOL (per datagram returned by conn.ReadMessage):
//
//   - wrap the bytes in a [msg.Reader]; wrap that in [SvcReader]
//   - loop: call SvcReader.Next(protocolVersion); on ErrEOF break;
//     on any other err return it verbatim
//   - for each Decoded value, call [Apply](state, msg, in.NowSec)
//   - increment MessagesApplied for each successful Apply
//
// ReadMessage returns (kind, data, err). The drain stops when:
//
//   - err != nil                  -- propagate
//   - kind == server.MessageNone  -- queue empty
//   - kind == server.MessageDisconnect -- remote closed, no more data
//
// OUTBOUND BUILD (skipped when state.Connection != [StateConnected]):
//
//   - angles := ApplyMouseMove(viewAngles, MouseDX, MouseDY,
//     Sensitivity, Speeds.MouseYaw, Speeds.MousePitch)
//   - angles  = AdjustAngles(angles, Buttons, Speeds, Dt)
//   - cmd    := BaseMove(Buttons, Speeds, Dt)
//   - cmd.ViewAngles = angles
//   - cmd.Buttons    = ActionButtons
//   - cmd.Impulse    = Impulse
//   - buf := sizebuf.New(make([]byte, tickOutBufSize))
//   - EncodeClcMove(buf, NowSec, cmd)
//   - conn.SendUnreliable(buf.Bytes())
//
// PROTOCOL VERSION for SvcReader.Next: passed as [protocol.VersionNQ]
// (the vanilla NQ value, 15). The C upstream tracks this in
// cl.protocol; the Go port's [State] does not yet carry a
// ProtocolVersion field. TODO: when the FITZ / BJP extensions land,
// add State.ProtocolVersion + thread it here.
//
// SHORT-CIRCUIT: when state.Connection != StateConnected, the OUTBOUND
// section is SKIPPED -- clc_move only makes sense post-signon. The
// inbound section still runs so the handshake messages (serverinfo,
// baseline, clientdata, signonnum) arrive WHILE state.Connection is
// StateConnecting; Apply transitions to StateConnected when
// DecodedSignonNum{Stage: 4} lands.
//
// Returns:
//
//   - (out, nil) on success
//   - (zero, ErrTickNilState) if state == nil
//   - (zero, ErrTickNilConn)  if conn == nil
//   - (zero, ErrTickBadInput) if in.Dt < 0 OR in.Sensitivity < 0
//   - wrapped decode / apply / encoder / conn errors verbatim otherwise
func Tick(
	state *State,
	conn server.NetConn,
	in TickInput,
	viewAngles [3]float32,
) (TickOutput, error) {
	if state == nil {
		return TickOutput{}, ErrTickNilState
	}
	if conn == nil {
		return TickOutput{}, ErrTickNilConn
	}
	if in.Dt < 0 || in.Sensitivity < 0 {
		return TickOutput{}, ErrTickBadInput
	}

	var out TickOutput
	out.ViewAngles = viewAngles

	// INBOUND -------------------------------------------------------
	for {
		kind, data, err := conn.ReadMessage()
		if err != nil {
			return TickOutput{}, err
		}
		if kind == server.MessageNone || kind == server.MessageDisconnect {
			break
		}
		out.DatagramsRead++

		reader := msg.NewReader(data)
		sr := SvcReader{R: reader}
		for {
			decoded, derr := sr.Next(protocol.VersionNQ)
			if errors.Is(derr, ErrEOF) {
				break
			}
			if derr != nil {
				return TickOutput{}, derr
			}
			if aerr := Apply(state, decoded, in.NowSec); aerr != nil {
				return TickOutput{}, aerr
			}
			out.MessagesApplied++
		}
	}

	// OUTBOUND SIGNON STRINGCMD ------------------------------------
	// Once the inbound drain has advanced state.Connection past
	// StateDisconnected (the server's svc_signonnum stage-1 byte fires
	// applySignonNum's SetConnecting path), the client is responsible
	// for emitting the canonical "spawn" clc_stringcmd ONCE so the
	// server's ParseClcStringCmd parser flips Client.Spawned + queues
	// the matching svc_signonnum(4) reply. Stages 2 + 3 from the
	// server are advisory acknowledgements; we don't wait for them --
	// the minimal NQ-loopback flow short-circuits the round-trip and
	// just sends "spawn" the moment we see StateConnecting. The
	// SentSpawn flag latches the emission so subsequent ticks don't
	// retransmit; State.Disconnect clears it so a reconnect re-arms.
	//
	// The send goes through SendReliable so the loopback peer reads
	// the bytes as MessageReliable (matching the inbound shape the
	// server's ReadClientMoves expects for clc_stringcmd).
	if state.Connection == StateConnecting && !state.SentSpawn {
		buf := sizebuf.New(make([]byte, tickOutBufSize))
		// EncodeClcStringCmd can only fail on a nil buf or an empty
		// command; the literal "spawn" + the freshly-allocated buf
		// above structurally rule both out, so the err return is
		// unreachable (matches the EncodeClcMove pattern below).
		_ = EncodeClcStringCmd(buf, "spawn")
		if _, err := conn.SendReliable(buf.Bytes()); err != nil {
			return TickOutput{}, err
		}
		state.SentSpawn = true
	}

	// OUTBOUND ------------------------------------------------------
	if state.Connection != StateConnected {
		return out, nil
	}

	// MouseYaw / MousePitch come off the per-tic InputSpeeds bundle so
	// the player's m_yaw / m_pitch cvar values flow through without
	// reaching into a global -- the runloop seeds DefaultInputSpeeds()
	// (MouseYaw=MousePitch=0.022) at startup, an in-game menu binding
	// would re-publish a new bundle on change.
	angles := ApplyMouseMove(
		viewAngles, in.MouseDX, in.MouseDY,
		in.Sensitivity, in.Speeds.MouseYaw, in.Speeds.MousePitch,
	)
	// in.Buttons is a *MovementButtons -- pass through verbatim so
	// KeyState's impulse drain (inside AdjustAngles + BaseMove) lands
	// on the caller's persistent kbutton state, not a stack copy.
	if in.Buttons != nil {
		angles = AdjustAngles(angles, in.Buttons, in.Speeds, in.Dt)
	}
	var cmd server.UserCmd
	if in.Buttons != nil {
		cmd = BaseMove(in.Buttons, in.Speeds, in.Dt)
	}
	cmd.ViewAngles = angles
	cmd.Buttons = in.ActionButtons
	cmd.Impulse = in.Impulse

	buf := sizebuf.New(make([]byte, tickOutBufSize))
	// EncodeClcMove can only fail on a nil buf or a sizebuf overflow;
	// neither is reachable here -- buf is freshly allocated above with
	// tickOutBufSize >> the fixed 16-byte clc_move payload. The error
	// return is structurally part of the encoder contract; the caller
	// side is provably error-free.
	_ = EncodeClcMove(buf, in.NowSec, cmd)
	if _, err := conn.SendUnreliable(buf.Bytes()); err != nil {
		return TickOutput{}, err
	}

	out.SentMove = true
	out.ViewAngles = angles
	return out, nil
}
