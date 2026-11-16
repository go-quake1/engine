// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package server

import (
	"bytes"
	"errors"
	"testing"

	"github.com/go-quake1/engine/protocol"
	"github.com/go-quake1/engine/sizebuf"
)

// TestEncodeLightStyle: svc_lightstyle is the byte 12, the style byte and the
// NUL-terminated string (reference/NQ/protocol.h:229 "[byte] [string]",
// pr_cmds.c:1333-1335, sv_user.c:915-917).
func TestEncodeLightStyle(t *testing.T) {
	buf := sizebuf.New(make([]byte, 64))
	if err := EncodeLightStyle(buf, 5, "abz"); err != nil {
		t.Fatal(err)
	}
	if want := []byte{12, 5, 'a', 'b', 'z', 0}; !bytes.Equal(buf.Bytes(), want) {
		t.Errorf("bytes %v, want %v", buf.Bytes(), want)
	}
	if protocol.SvcLightStyle != 12 {
		t.Errorf("svc_lightstyle is %d, the reference has 12", protocol.SvcLightStyle)
	}
	for _, c := range []struct {
		name  string
		buf   *sizebuf.Buffer
		style int
		want  error
	}{
		{"nil buffer", nil, 1, ErrNilBuf},
		{"style 64", sizebuf.New(make([]byte, 64)), 64, ErrLightStyleRange},
		{"style -1", sizebuf.New(make([]byte, 64)), -1, ErrLightStyleRange},
	} {
		if err := EncodeLightStyle(c.buf, c.style, "m"); !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", c.name, err, c.want)
		}
	}
	// overflow on each of the three writes
	for _, n := range []int{0, 1, 2} {
		if err := EncodeLightStyle(sizebuf.New(make([]byte, n)), 1, "m"); err == nil {
			t.Errorf("a %d-byte buffer took a light style", n)
		}
	}
	// the highest style
	buf = sizebuf.New(make([]byte, 8))
	if err := EncodeLightStyle(buf, 63, ""); err != nil || !bytes.Equal(buf.Bytes(), []byte{12, 63, 0}) {
		t.Errorf("style 63, empty: %v %v", err, buf.Bytes())
	}
}

// TestSendLightStyles: Host_Spawn_f sends all 64 styles, in order, an unset one
// as the empty string (sv_user.c:913-918).
func TestSendLightStyles(t *testing.T) {
	c := activeClient(4096, true, false)
	styles := make([]string, MaxLightStyles)
	styles[0] = "m"
	styles[1] = "mmnmmommommnonmmonqnmmo"
	styles[63] = "z"
	if err := SendLightStyles(c, styles); err != nil {
		t.Fatal(err)
	}
	var want []byte
	for i := 0; i < MaxLightStyles; i++ {
		want = append(want, 12, byte(i))
		want = append(want, styles[i]...)
		want = append(want, 0)
	}
	if !bytes.Equal(c.Message.Bytes(), want) {
		t.Errorf("%d bytes, want %d", c.Message.Len(), len(want))
	}
	// fewer strings than styles: the rest are empty
	c = activeClient(4096, true, false)
	if err := SendLightStyles(c, []string{"m"}); err != nil || c.Message.Len() != 4+3*63 {
		t.Errorf("a short table: %v, %d bytes", err, c.Message.Len())
	}
	// no client, an inactive one, one with no buffer: nothing, no error
	for _, cl := range []*Client{nil, activeClient(64, false, false), {Active: true}} {
		if err := SendLightStyles(cl, styles); err != nil {
			t.Errorf("%+v: %v", cl, err)
		}
	}
	if err := SendLightStyles(activeClient(10, true, true), styles); err == nil {
		t.Error("a buffer that is too small was accepted")
	}
}

// TestSetLightStyle: PF_lightstyle keeps the string and, when the server is
// active, writes svc_lightstyle to every client that is active or spawned
// (pr_cmds.c:1324-1336); while the server loads, only keeps it.
func TestSetLightStyle(t *testing.T) {
	s := NewServer()
	static := &Static{Clients: []*Client{
		activeClient(64, true, true),   // receives
		activeClient(64, true, false),  // active, not spawned: receives (client->active || client->spawned)
		activeClient(64, false, true),  // spawned: receives
		activeClient(64, false, false), // neither: not
		nil,
		{Active: true}, // no message buffer
	}}
	if err := s.SetLightStyle(static, 3, "a"); err != nil {
		t.Fatal(err)
	}
	if s.LightStyles[3] != "a" {
		t.Errorf("sv.lightstyles[3] = %q", s.LightStyles[3])
	}
	for i, c := range static.Clients {
		if c != nil && c.Message != nil && c.Message.Len() != 0 {
			t.Errorf("client %d got %v while the server was loading", i, c.Message.Bytes())
		}
	}
	s.Active = true
	if err := s.SetLightStyle(static, 33, "mz"); err != nil {
		t.Fatal(err)
	}
	want := []byte{12, 33, 'm', 'z', 0}
	for i, rec := range []bool{true, true, true, false} {
		got := static.Clients[i].Message.Bytes()
		if rec && !bytes.Equal(got, want) || !rec && len(got) != 0 {
			t.Errorf("client %d: %v, want %v (receives %v)", i, got, want, rec)
		}
	}
	if s.LightStyles[33] != "mz" {
		t.Errorf("sv.lightstyles[33] = %q", s.LightStyles[33])
	}
	// a client whose buffer is full: the others still get it, the first error comes back
	static = &Static{Clients: []*Client{activeClient(1, true, true), activeClient(64, true, true)}}
	if err := s.SetLightStyle(static, 1, "m"); err == nil {
		t.Error("an overflow was not reported")
	}
	if static.Clients[1].Message.Len() != 4 {
		t.Errorf("the second client got %d bytes", static.Clients[1].Message.Len())
	}
	if err := s.SetLightStyle(nil, 2, "m"); err != nil || s.LightStyles[2] != "m" {
		t.Errorf("no Static: %v, %q", err, s.LightStyles[2])
	}
	for _, st := range []int{-1, 64} {
		if err := s.SetLightStyle(static, st, "m"); !errors.Is(err, ErrLightStyleRange) {
			t.Errorf("style %d: %v", st, err)
		}
	}
}
