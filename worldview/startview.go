// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package worldview

import (
	"fmt"

	"github.com/go-quake1/engine/entparse"
)

// PlayerStartView is the player's eye at info_player_start: the entity's
// origin plus 22 units up, looking along its yaw. A level is lit to be seen
// from there, unlike from the map's centre (where the generic views of
// [World.Views] can fall in an unlit spot). ok is false when the map has no
// info_player_start.
func (w *World) PlayerStartView() (v View, ok bool, err error) {
	ents, err := entparse.ParseEntities(w.File.Entities())
	if err != nil {
		return View{}, false, fmt.Errorf("worldview: %w", err)
	}
	for _, e := range ents {
		if e["classname"] != "info_player_start" {
			continue
		}
		o, err := entparse.ParseVec3(e["origin"])
		if err != nil {
			return View{}, false, fmt.Errorf("worldview: info_player_start origin: %w", err)
		}
		var yaw float32
		if s, has := e["angle"]; has {
			if yaw, err = entparse.ParseFloat(s); err != nil {
				return View{}, false, fmt.Errorf("worldview: info_player_start angle: %w", err)
			}
		}
		return View{Origin: [3]float32{o[0], o[1], o[2] + 22}, Angles: [3]float32{0, yaw, 0}}, true, nil
	}
	return View{}, false, nil
}

// AcceptanceViews is [World.Views] followed, when the map has one, by the
// [World.PlayerStartView]: the numbered views quake-worldref and quake-wctr
// --worldref draw, so the same number is the same frame in both.
func (w *World) AcceptanceViews() ([]View, error) {
	vs := w.Views()
	sv, ok, err := w.PlayerStartView()
	if err != nil {
		return nil, err
	}
	if ok {
		vs = append(vs, sv)
	}
	return vs, nil
}
