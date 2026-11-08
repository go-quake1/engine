// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package runloop

import "github.com/go-quake1/engine/menu"

// SkipMenu dismisses the title menu and drops the attract demo, so the
// next RunFrame steps the host from the first tic with the world drawn.
// Escape still opens the menu afterwards. A nil Menu stays nil.
func (r *Runner) SkipMenu() {
	if r.Menu != nil {
		r.Menu.State = menu.StateNone
	}
	r.Demo = nil
}
