// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package host

import (
	"bytes"
	"testing"

	"github.com/go-quake1/engine/progs"
	"github.com/go-quake1/engine/server"
	"github.com/go-quake1/engine/sizebuf"
)

// TestBuiltinLightStyle: lightstyle(style, "value") (PF_lightstyle, builtin 35)
// keeps the string in sv.lightstyles and, once the server is active, queues
// svc_lightstyle to the spawned client; the style is the float truncated; a
// style out of the array is ignored; no host is a no-op.
func TestBuiltinLightStyle(t *testing.T) {
	strs := []byte{0}
	val := addStr(&strs, "mmnmmommommnonmmonqnmmo")
	p := &progs.Progs{Header: progs.Header{EntityFields: 4}, Strings: strs, Globals: make([]byte, 96*4)}
	vm := progs.NewVM(p)
	c := &server.Client{Active: true, Spawned: true, Message: sizebuf.New(make([]byte, 256))}
	h := &Host{VM: vm, Server: server.NewServer(), Static: &server.Static{Clients: []*server.Client{c}}}
	h.SetProgs(p)
	fn := BuiltinLightStyle(h)
	call := func(style float32) error {
		_ = vm.SetGlobalFloat(progs.OfsParm0, style)
		_ = vm.SetGlobalInt(progs.OfsParm1, val)
		return fn(vm)
	}
	// loading: kept, not sent
	if err := call(1.9); err != nil {
		t.Fatal(err)
	}
	if h.Server.LightStyles[1] != "mmnmmommommnonmmonqnmmo" || c.Message.Len() != 0 {
		t.Errorf("loading: %q, %d bytes sent", h.Server.LightStyles[1], c.Message.Len())
	}
	h.Server.Active = true
	if err := call(1); err != nil {
		t.Fatal(err)
	}
	want := append([]byte{12, 1}, append([]byte("mmnmmommommnonmmonqnmmo"), 0)...)
	if !bytes.Equal(c.Message.Bytes(), want) {
		t.Errorf("sent %v, want %v", c.Message.Bytes(), want)
	}
	n := c.Message.Len()
	for _, bad := range []float32{-1, 64, 1000} {
		if err := call(bad); err != nil {
			t.Errorf("style %v: %v", bad, err)
		}
	}
	if c.Message.Len() != n {
		t.Error("an out-of-range style was sent")
	}
	if err := BuiltinLightStyle(nil)(vm); err != nil {
		t.Errorf("nil host: %v", err)
	}
	if err := BuiltinLightStyle(&Host{})(vm); err != nil {
		t.Errorf("no server: %v", err)
	}
}
