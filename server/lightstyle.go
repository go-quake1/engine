// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package server

import (
	"errors"
	"fmt"

	"github.com/go-quake1/engine/msg"
	"github.com/go-quake1/engine/protocol"
	"github.com/go-quake1/engine/sizebuf"
)

// ErrLightStyleRange is returned by the light style functions for a style
// outside [0, MaxLightStyles): the C code indexes sv.lightstyles with the QC
// float unchecked; the wire byte of svc_lightstyle is read by the client against
// MAX_LIGHTSTYLES and is an error there (cl_parse.c:1055).
var ErrLightStyleRange = errors.New("server: light style outside [0, 64)")

// EncodeLightStyle writes svc_lightstyle: the style (a byte) and its animation
// string, one letter a tenth of a second, 'a' dark to 'z' twice as bright
// as normal, 'm' (reference/NQ/sv_user.c:915-917, reference/common/pr_cmds.c:1333-1335).
func EncodeLightStyle(buf *sizebuf.Buffer, style int, text string) error {
	if buf == nil {
		return ErrNilBuf
	}
	if style < 0 || style >= MaxLightStyles {
		return fmt.Errorf("%w: style=%d", ErrLightStyleRange, style)
	}
	if err := msg.WriteByte(buf, protocol.SvcLightStyle); err != nil {
		return err
	}
	if err := msg.WriteByte(buf, style); err != nil {
		return err
	}
	return msg.WriteString(buf, text)
}

// SendLightStyles queues every light style of the server to a client that is
// spawning: Host_Spawn_f's "send all current light styles"
// (reference/NQ/sv_user.c:913-918), all MaxLightStyles of them, an unset style
// as the empty string. Silent no-op for a nil, inactive or buffer-less client.
func SendLightStyles(client *Client, styles []string) error {
	if client == nil || !client.Active || client.Message == nil {
		return nil
	}
	for i := 0; i < MaxLightStyles; i++ {
		text := ""
		if i < len(styles) {
			text = styles[i]
		}
		if err := EncodeLightStyle(client.Message, i, text); err != nil {
			return err
		}
	}
	return nil
}

// SetLightStyle is PF_lightstyle (reference/common/pr_cmds.c:1313-1347, the
// NQ branch): the string is kept in sv.lightstyles, and when the server is
// active every client that is active or spawned has svc_lightstyle written to its
// reliable message. A server still loading only keeps it: the clients are sent
// all the styles when they spawn ([SendLightStyles]). The broadcast goes on past
// a client whose buffer overflows and returns the first error.
func (s *Server) SetLightStyle(static *Static, style int, text string) error {
	if style < 0 || style >= MaxLightStyles {
		return fmt.Errorf("%w: style=%d", ErrLightStyleRange, style)
	}
	s.LightStyles[style] = text
	if !s.Active || static == nil {
		return nil
	}
	var first error
	for _, c := range static.Clients {
		if c == nil || !(c.Active || c.Spawned) || c.Message == nil {
			continue
		}
		if err := EncodeLightStyle(c.Message, style, text); err != nil && first == nil {
			first = err
		}
	}
	return first
}
