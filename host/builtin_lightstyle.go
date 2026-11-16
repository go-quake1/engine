// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package host

import (
	"errors"

	"github.com/go-quake1/engine/progs"
	"github.com/go-quake1/engine/server"
)

// BuiltinLightStyle returns the QC `lightstyle(float style, string value)`
// builtin (PF_lightstyle, reference/common/pr_cmds.c:1313-1347; builtin 35):
// [server.Server.SetLightStyle] with the style truncated to an int as
// `style = G_FLOAT(OFS_PARM0)` does. A style outside [0, 64), which C would
// write out of the array, is ignored. nil host: a no-op.
func BuiltinLightStyle(h *Host) progs.Builtin {
	return func(vm *progs.VM) error {
		if h == nil || h.Server == nil {
			return nil
		}
		style, _ := vm.GlobalFloat(progs.OfsParm0)
		off, _ := vm.GlobalInt(progs.OfsParm1)
		err := h.Server.SetLightStyle(h.Static, int(style), vm.String(off))
		if errors.Is(err, server.ErrLightStyleRange) {
			return nil
		}
		return err
	}
}
