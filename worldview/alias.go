// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package worldview

import (
	"fmt"
	"io"
	"io/fs"
	"math"

	"github.com/go-quake1/engine/bsprender"
	"github.com/go-quake1/engine/bsptrace"
	"github.com/go-quake1/engine/entparse"
	"github.com/go-quake1/engine/mdl"
	"github.com/go-quake1/engine/render"
)

// AliasModel is an alias model with its skins as pictures.
type AliasModel struct {
	Path  string
	M     *mdl.Model
	Skins []*render.Pic
}

// LoadAliasModel loads an .mdl from the pak. A skin group contributes its
// first skin.
func LoadAliasModel(fsys fs.FS, path string) (*AliasModel, error) {
	f, err := fsys.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}
	m, err := mdl.Load(readerAt(b), int64(len(b)))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	am := &AliasModel{Path: path, M: m}
	for _, s := range m.Skins {
		px := s.Single.Pixels
		if s.Type == mdl.SkinGroup && s.Group != nil && len(s.Group.Skins) > 0 {
			px = s.Group.Skins[0].Pixels
		}
		am.Skins = append(am.Skins, &render.Pic{Width: int(m.Header.SkinWidth), Height: int(m.Header.SkinHeight), Pixels: px})
	}
	if len(am.Skins) == 0 || len(m.Frames) == 0 {
		return nil, fmt.Errorf("%s: no skin or no frame", path)
	}
	return am, nil
}

// AliasEnt is one alias model placed in the world: what the engine's
// render.AliasEntityInterp carries, plus the model and the skin.
type AliasEnt struct {
	Model        *AliasModel
	Skin         int
	Origin       [3]float32
	Angles       [3]float32 // pitch, yaw, roll
	Frame, Next  int
	Lerp         float32
	Class        string
	IsViewWeapon bool

	// Light is R_LightPoint at Origin ([World.LightEnt]); both the
	// software and the GPU path light the entity from it.
	Light int
}

// LightEnt sets e.Light to R_LightPoint at the entity's origin, with the
// light styles at their resting value ([bsprender.NormalLightStyle]).
func (w *World) LightEnt(e *AliasEnt) {
	e.Light = w.Lights.LightPoint(e.Origin, bsprender.NormalLightStyle)
}

// classModels maps the e1m1 classes that are alias models to their model
// and skin. Other classes (health and ammo boxes, the explosive box) are
// BSP models, which this step does not draw.
var classModels = map[string]struct {
	path string
	skin int
}{
	"monster_army":                  {"progs/soldier.mdl", 0},
	"monster_dog":                   {"progs/dog.mdl", 0},
	"item_armor1":                   {"progs/armor.mdl", 0},
	"item_armor2":                   {"progs/armor.mdl", 1},
	"weapon_nailgun":                {"progs/g_nail.mdl", 0},
	"weapon_supernailgun":           {"progs/g_nail2.mdl", 0},
	"weapon_supershotgun":           {"progs/g_shot.mdl", 0},
	"weapon_rocketlauncher":         {"progs/g_rock2.mdl", 0},
	"weapon_grenadelauncher":        {"progs/g_rock.mdl", 0},
	"item_artifact_invulnerability": {"progs/invulner.mdl", 0},
	"item_artifact_envirosuit":      {"progs/suit.mdl", 0},
	"item_artifact_super_damage":    {"progs/quaddama.mdl", 0},
}

// IsMonster reports whether a class is a monster.
func IsMonster(class string) bool { return class == "monster_army" || class == "monster_dog" }

// AliasEntities returns the alias-model entities of the map in file order,
// at rest: frame 0, except that every third monster is caught half way
// between frames 1 and 2 so that the interpolation is exercised.
func (w *World) AliasEntities(fsys fs.FS) ([]AliasEnt, error) {
	ents, err := entparse.ParseEntities(w.File.Entities())
	if err != nil {
		return nil, err
	}
	models := map[string]*AliasModel{}
	var out []AliasEnt
	nm := 0
	for _, e := range ents {
		cm, ok := classModels[e["classname"]]
		if !ok {
			continue
		}
		am, ok := models[cm.path]
		if !ok {
			if am, err = LoadAliasModel(fsys, cm.path); err != nil {
				return nil, err
			}
			models[cm.path] = am
		}
		org, err := entparse.ParseVec3(e["origin"])
		if err != nil {
			continue
		}
		ae := AliasEnt{Model: am, Skin: min(cm.skin, len(am.Skins)-1), Origin: org, Class: e["classname"]}
		w.LightEnt(&ae)
		if a, err := entparse.ParseFloat(e["angle"]); err == nil {
			ae.Angles[1] = a
		}
		if IsMonster(ae.Class) {
			if nm%3 == 2 && len(am.M.Frames) > 2 {
				ae.Frame, ae.Next, ae.Lerp = 1, 2, 0.5
			}
			nm++
		}
		out = append(out, ae)
	}
	return out, nil
}

// ViewWeapon is the player's weapon model held in front of the view: the
// shotgun at the eye, turned with the view (the engine's software path does
// not draw one yet).
func ViewWeapon(fsys fs.FS, v View) (AliasEnt, error) {
	am, err := LoadAliasModel(fsys, "progs/v_shot.mdl")
	if err != nil {
		return AliasEnt{}, err
	}
	return AliasEnt{Model: am, Origin: v.Origin, Angles: v.Angles, Class: "viewmodel", IsViewWeapon: true}, nil
}

// AliasView is a camera with the entities drawn in it.
type AliasView struct {
	View
	Target int // index in the entity list of the entity the camera looks at
	Ents   []AliasEnt
}

// AliasViews returns 16 fixed views that each look at one entity from a
// spot that sees it: 10 monsters and 6 items, taken at an even stride from
// the entity list, each seen from the first of a fixed list of distances and
// bearings whose eye point is inside the map and has a clear line to the
// entity. A view carries the entities with a clear line to its eye within
// 1500 units (the software reference has no depth test, so what it is given
// shows through walls; this keeps it to what a server would send) and the
// view weapon.
func (w *World) AliasViews(ents []AliasEnt, weapon func(View) (AliasEnt, error)) ([]AliasView, error) {
	var mon, items []int
	for i, e := range ents {
		if IsMonster(e.Class) {
			mon = append(mon, i)
		} else {
			items = append(items, i)
		}
	}
	pick := func(from []int, n int) []int {
		var out []int
		for k := 0; k < n && len(from) > 0; k++ {
			out = append(out, from[k*len(from)/n])
		}
		return out
	}
	targets := append(pick(mon, 10), pick(items, 6)...)
	var views []AliasView
	for _, ti := range targets {
		t := ents[ti]
		var cam View
		found := false
		for _, dist := range []float32{96, 128, 160, 64, 200} {
			for _, off := range []float32{180, 135, 225, 90, 270, 0, 45, 315} {
				yaw := t.Angles[1] + off
				rad := float64(yaw) * math.Pi / 180
				eye := [3]float32{t.Origin[0] + dist*float32(math.Cos(rad)), t.Origin[1] + dist*float32(math.Sin(rad)), t.Origin[2] + 24}
				if w.BM.PointInLeaf(eye) <= 0 || !w.clear(eye, [3]float32{t.Origin[0], t.Origin[1], t.Origin[2] + 24}) {
					continue
				}
				// Face the entity: from the eye towards it.
				look := float32(math.Atan2(float64(t.Origin[1]-eye[1]), float64(t.Origin[0]-eye[0])) * 180 / math.Pi)
				cam, found = View{Origin: eye, Angles: [3]float32{0, look, 0}}, true
				break
			}
			if found {
				break
			}
		}
		if !found {
			continue
		}
		// The entities a server would send: those with a clear line to the
		// eye within 1500 units (a stand-in for the PVS).
		var all []AliasEnt
		for _, e := range ents {
			dx, dy, dz := e.Origin[0]-cam.Origin[0], e.Origin[1]-cam.Origin[1], e.Origin[2]-cam.Origin[2]
			if dx*dx+dy*dy+dz*dz <= 1500*1500 && w.clear(cam.Origin, [3]float32{e.Origin[0], e.Origin[1], e.Origin[2] + 24}) {
				all = append(all, e)
			}
		}
		if weapon != nil {
			we, err := weapon(cam)
			if err != nil {
				return nil, err
			}
			w.LightEnt(&we)
			all = append(all, we)
		}
		views = append(views, AliasView{View: cam, Target: ti, Ents: all})
	}
	return views, nil
}

// clear reports whether the segment a to b stays in open space.
func (w *World) clear(a, b [3]float32) bool {
	tr := bsptrace.DefaultTrace()
	h := &w.BM.Hulls[0]
	ok, err := bsptrace.TraceHull(h, h.FirstClipNode, a, b, &tr)
	return err == nil && ok && !tr.StartSolid && tr.Fraction == 1
}

// DrawAliasSoftware draws ents over fb the way the engine does
// (game.setupRenderer): render.DrawAliasInterpQuake (Quake's lighting, at
// each entity's R_LightPoint), no depth test, after the world.
func (w *World) DrawAliasSoftware(fb *render.FrameBuffer, cm *render.ColorMap, v View, ents []AliasEnt) error {
	rd := &render.RefDef{
		VRect: render.VRect{Width: fb.Width, Height: fb.Height}, ViewAngles: v.Angles, ViewOrigin: v.Origin,
		FovX: w.FOV, FovY: w.FOV,
	}
	for _, e := range ents {
		ent := render.AliasEntityInterp{
			AliasEntity: render.AliasEntity{
				Origin: e.Origin, AnglePitch: e.Angles[0], AngleYaw: e.Angles[1], AngleRoll: e.Angles[2], FrameIdx: e.Frame,
			},
			FrameIdxNext: e.Next, Lerp: e.Lerp,
		}
		if err := render.DrawAliasInterpQuake(fb, rd, cm, e.Light, e.IsViewWeapon, e.Model.M, e.Model.Skins[e.Skin], ent); err != nil {
			return fmt.Errorf("%s: %w", e.Model.Path, err)
		}
	}
	return nil
}
