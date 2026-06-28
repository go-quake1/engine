// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

//go:build js && wasm

package wasmbox

import (
	"fmt"
	"syscall/js"
)

// NewClient performs the wasmbox-protocol handshake from the Worker
// scope + returns a Backend wired to the negotiated surface. The flow
// is:
//
//  1. js.Global().Get("SharedArrayBuffer").New(4 * width * height)
//     (requires the page to be served with COOP/COEP, which wasmbox's
//     cmd/serve does). Construct a Uint8ClampedArray view over it.
//  2. Install a one-shot "message" listener that resolves a Go channel
//     when {type:"welcome"} arrives.
//  3. self.postMessage({type:"hello", title, w, h, sab, stride}) + wait
//     on the channel.
//  4. Remove the handshake listener, build SABSurface over the SAB +
//     ProtocolInput over self.
//  5. Best-effort WebAudio sink (silent fallback on failure).
//  6. PerformanceNow clock.
//
// The handshake blocks the calling goroutine via a channel receive.
// The Go wasm scheduler yields to the JS event loop while parked, so
// the welcome message is delivered promptly.
func NewClient(title string, width, height int) (*Backend, error) {
	self := js.Global()
	sabCtor := self.Get("SharedArrayBuffer")
	if !sabCtor.Truthy() {
		return nil, ErrNoSAB
	}
	stride := 4 * width
	sab := sabCtor.New(stride * height)
	pixels := self.Get("Uint8ClampedArray").New(sab)

	welcome := make(chan welcomeReply, 1)
	var handshakeCB js.Func
	handshakeCB = js.FuncOf(func(_ js.Value, args []js.Value) any {
		if len(args) == 0 {
			return nil
		}
		data := args[0].Get("data")
		if !data.Truthy() {
			return nil
		}
		typ := data.Get("type")
		if !typ.Truthy() || typ.Type() != js.TypeString || typ.String() != "welcome" {
			return nil
		}
		reply := welcomeReply{
			windowID: data.Get("window_id").Int(),
			width:    data.Get("granted_w").Int(),
			height:   data.Get("granted_h").Int(),
		}
		// Non-blocking send: the channel is buffered size 1, and we
		// only expect one welcome.
		select {
		case welcome <- reply:
		default:
		}
		return nil
	})
	self.Call("addEventListener", "message", handshakeCB)

	// Build the hello payload via Object constructor (Set per field —
	// js.ValueOf can't construct a plain JS object across the bridge).
	objCtor := self.Get("Object")
	hello := objCtor.New()
	hello.Set("type", "hello")
	hello.Set("title", title)
	hello.Set("w", width)
	hello.Set("h", height)
	hello.Set("sab", sab)
	hello.Set("stride", stride)
	self.Get("postMessage").Invoke(hello)

	reply := <-welcome
	self.Call("removeEventListener", "message", handshakeCB)
	handshakeCB.Release()

	if reply.width == 0 || reply.height == 0 {
		return nil, fmt.Errorf("%w (got %dx%d)", ErrNoWelcome, reply.width, reply.height)
	}

	// Surface: wrap the SAB + bound postMessage.
	surface := NewSABSurface(self, pixels, reply.windowID, reply.width, reply.height)

	// Input: install the long-lived message listener.
	input := NewProtocolInput(self)

	// Audio: best-effort. Worker-scope AudioContext is supported in
	// modern Chromium; a missing constructor degrades to silent.
	//
	// NOTE: NewWebAudio returns (*WebAudio, error) so a failure path
	// hands back a TYPED-nil *WebAudio. Passing that straight through
	// to New() boxes a typed-nil into the AudioDevice interface, which
	// the runtime treats as non-nil + then nil-derefs when QueueAudio
	// dispatches WritePCM. Coerce the typed-nil to the bare interface-
	// nil before handing to New(): same shape New() already expects from
	// callers that have no audio at all.
	audioRaw, audioErr := NewWebAudio()
	var audio AudioDevice
	if audioErr == nil && audioRaw != nil {
		audio = audioRaw
	}

	return New(surface, input, audio, PerformanceNow())
}

// welcomeReply carries the three fields from the compositor's welcome
// message that the handshake needs to build the surface.
type welcomeReply struct {
	windowID int
	width    int
	height   int
}
