// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package runner

import (
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"time"

	"github.com/go-quake1/engine/assets"
	"github.com/go-quake1/engine/bspfile"
	"github.com/go-quake1/engine/bspfile/synthbsp"
	"github.com/go-quake1/engine/entparse"
	enginehost "github.com/go-quake1/engine/host"
	"github.com/go-quake1/engine/mdl"
	"github.com/go-quake1/engine/menu"
	"github.com/go-quake1/engine/model"
	"github.com/go-quake1/engine/render"
	enginesound "github.com/go-quake1/engine/sound"
	enginespr "github.com/go-quake1/engine/spr"
	"github.com/go-quake1/engine/vfs"
)

// loadBSP returns the BSP bytes + size to RENDER. It must load the same map
// the host's server spawned (SetupOpts.MapSlug), or the camera's PVS/leaf
// bookkeeping desyncs from the collision world. Sources, in order:
//
//  1. The pakFS -- try preferMap (the host's map) first, then "maps/start.bsp".
//  2. synthbsp.BuildWithFaces() -- the always-available fallback.
//
// preferMap is the full pak path (e.g. "maps/lq_e0m1.bsp"); empty falls back
// to start.bsp.
func loadBSP(pakFS fs.FS, preferMap string, logf func(string, ...any)) ([]byte, int64, error) {
	if pakFS != nil {
		candidates := []string{}
		if preferMap != "" {
			candidates = append(candidates, preferMap)
		}
		candidates = append(candidates, "maps/start.bsp")
		for _, mapName := range candidates {
			data, ok := tryReadPakFile(pakFS, mapName)
			if ok {
				logf("loaded %s from pak (%d bytes)", mapName, len(data))
				return data, int64(len(data)), nil
			}
		}
		logf("pak lacks %v; using synthbsp fallback", candidates)
	} else {
		logf("using synthbsp fallback (no pak FS available)")
	}
	return synthbsp.BuildWithFaces()
}

// renderMapFile maps a host map slug (e.g. "lq_e0m1", "start") to the pak
// path loadBSP should render, so the rendered world matches the host's
// spawned map. An empty slug returns "" (loadBSP falls back to start.bsp).
func renderMapFile(slug string) string {
	if slug == "" {
		return ""
	}
	return "maps/" + slug + ".bsp"
}

// tryReadPakFile opens name inside pakFS and returns its contents.
func tryReadPakFile(pakFS fs.FS, name string) ([]byte, bool) {
	if pakFS == nil {
		return nil, false
	}
	f, err := pakFS.Open(name)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, false
	}
	return data, true
}

// tryReadFromFS opens name on src and returns its contents.
func tryReadFromFS(src fs.FS, name string) ([]byte, bool) {
	f, err := src.Open(name)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, false
	}
	return data, true
}

// makeCheckerTex returns an NxN texture with a 4-colour checker pattern.
func makeCheckerTex(n int) *render.Pic {
	pixels := make([]byte, n*n)
	colors := [4]byte{0, 15, 31, 47}
	tile := n / 4
	if tile < 1 {
		tile = 1
	}
	for v := 0; v < n; v++ {
		for u := 0; u < n; u++ {
			idx := ((u / tile) + (v/tile)*2) & 3
			pixels[v*n+u] = colors[idx]
		}
	}
	return &render.Pic{Width: n, Height: n, Pixels: pixels}
}

// syntheticAssets returns an fs.FS holding the three lumps assets.LoadStandard needs.
// Optional conchars override (typically tamago's concharsfont.Build) replaces
// the all-zero glyph sheet when non-nil.
func syntheticAssets(conchars []byte) fs.FS {
	cc := conchars
	if len(cc) != assets.ConCharsLumpSize {
		cc = makeConcharsLump()
	}
	return memFS{
		"gfx/palette.lmp":  makePaletteLump(),
		"gfx/colormap.lmp": makeColorMapLump(),
		"gfx/conchars.lmp": cc,
	}
}

// reportLumpSources probes each named lump against the live SearchPath
// and prints which source (real pak vs synthetic fallback) wins.
func reportLumpSources(v *vfs.SearchPath, pakFS fs.FS, syn fs.FS, lumps []string, logf func(string, ...any)) {
	for _, name := range lumps {
		got, ok := tryReadFromFS(v, name)
		if !ok {
			logf("%s NOT FOUND in any source", name)
			continue
		}
		source := "synthetic"
		if pakFS != nil {
			if real, okp := tryReadFromFS(pakFS, name); okp && bytes.Equal(real, got) {
				source = "real pak"
			}
		}
		logf("%s from %s (%d bytes)", name, source, len(got))
	}
}

// memFS is a minimal in-memory fs.FS.
type memFS map[string][]byte

func (m memFS) Open(name string) (fs.File, error) {
	data, ok := m[name]
	if !ok {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	return &memFile{name: name, data: data}, nil
}

type memFile struct {
	name string
	data []byte
	pos  int
}

func (f *memFile) Read(p []byte) (int, error) {
	if f.pos >= len(f.data) {
		return 0, io.EOF
	}
	n := copy(p, f.data[f.pos:])
	f.pos += n
	return n, nil
}

func (f *memFile) Stat() (fs.FileInfo, error) {
	return &memFileInfo{name: f.name, size: int64(len(f.data))}, nil
}

func (f *memFile) Close() error { return nil }

type memFileInfo struct {
	name string
	size int64
}

func (i *memFileInfo) Name() string       { return i.name }
func (i *memFileInfo) Size() int64        { return i.size }
func (i *memFileInfo) Mode() fs.FileMode  { return 0o444 }
func (i *memFileInfo) ModTime() time.Time { return time.Time{} }
func (i *memFileInfo) IsDir() bool        { return false }
func (i *memFileInfo) Sys() any           { return nil }

// makePaletteLump returns a 768-byte synthetic palette.
func makePaletteLump() []byte {
	buf := make([]byte, render.PaletteLumpSize)
	for i := 0; i < 256; i++ {
		buf[i*3+0] = byte(i)
		buf[i*3+1] = byte(i ^ 0xFF)
		buf[i*3+2] = byte(i << 1)
	}
	return buf
}

// makeColorMapLump returns a 16384-byte identity-mapped colormap.
func makeColorMapLump() []byte {
	buf := make([]byte, render.ColorMapRows*render.ColorMapCols)
	for i := range buf {
		buf[i] = byte(i)
	}
	return buf
}

// makeConcharsLump returns a 16384-byte all-zero conchars sheet (the
// no-conchars-override fallback). Callers that want readable glyphs
// pass a non-nil ConCharsLump into SetupOpts (tamago does, building
// from the embedded concharsfont package).
func makeConcharsLump() []byte {
	buf := make([]byte, assets.ConCharsLumpSize)
	return buf
}

// seedSoundPool loads each candidate WAV name out of pakFS and parks
// it on one of the pool's reserved-static channel slots.
func seedSoundPool(pool *enginesound.Pool, pakFS fs.FS, names []string, logf func(string, ...any)) int {
	seeded := 0
	for _, name := range names {
		if seeded >= pool.ReservedStatic {
			break
		}
		blob, ok := tryReadPakFile(pakFS, name)
		if !ok {
			logf("sound asset missing: %s", name)
			continue
		}
		s, err := enginesound.LoadWav(name, blob)
		if err != nil {
			logf("sound asset load failed: %s -- %v", name, err)
			continue
		}
		logf("loaded WAV %s -- rate=%dHz bits=%d numSamples=%d loopStart=%d dataLen=%d",
			name, s.SampleRate, s.BitsPerSam, s.NumSamples, s.LoopStart, len(s.Data))
		ch := &pool.Channels[seeded]
		ch.Sfx = s
		ch.Position = 0
		ch.EndPos = s.NumSamples
		ch.LeftVol = 200
		ch.RightVol = 200
		ch.Master = true
		seeded++
	}
	return seeded
}

// loadExplosionSprite opens the canonical s_explod.spr asset.
func loadExplosionSprite(pakFS fs.FS, logf func(string, ...any)) (*enginespr.Sprite, string) {
	if pakFS == nil {
		return nil, ""
	}
	candidates := []string{
		"progs/s_explod.spr",
		"sprites/s_explod.spr",
	}
	for _, path := range candidates {
		blob, ok := tryReadPakFile(pakFS, path)
		if !ok {
			continue
		}
		sp, err := enginespr.Load(bytes.NewReader(blob), int64(len(blob)))
		if err != nil {
			logf("spr.Load(%s) err: %v", path, err)
			continue
		}
		return sp, path
	}
	return nil, ""
}

// loadAliasModels walks the model precache and opens every entry that
// names an alias model (".mdl" suffix).
func loadAliasModels(pakFS fs.FS, precache []string, logf func(string, ...any)) ([]*mdl.Model, []*render.Pic, int, int) {
	n := len(precache)
	models := make([]*mdl.Model, n)
	skins := make([]*render.Pic, n)
	if pakFS == nil || n == 0 {
		return models, skins, 0, 0
	}
	loaded := 0
	names := 0
	for i := 0; i < n; i++ {
		name := precache[i]
		if !hasSuffix(name, ".mdl") {
			continue
		}
		names++
		blob, ok := tryReadPakFile(pakFS, name)
		if !ok {
			continue
		}
		m, err := mdl.Load(bytes.NewReader(blob), int64(len(blob)))
		if err != nil {
			logf("mdl.Load(%s) err: %v", name, err)
			continue
		}
		models[i] = m
		skins[i] = firstSkinAsPic(m)
		loaded++
	}
	return models, skins, loaded, names
}

// loadBoltModels opens the three lightning-bolt alias models.
func loadBoltModels(pakFS fs.FS, logf func(string, ...any)) (models [3]*mdl.Model, skins [3]*render.Pic, loaded int) {
	if pakFS == nil {
		return
	}
	paths := [3]string{
		"progs/bolt.mdl",
		"progs/bolt2.mdl",
		"progs/bolt3.mdl",
	}
	alt1 := "progs/bolt1.mdl"
	if blob, ok := tryReadPakFile(pakFS, alt1); ok {
		if m, err := mdl.Load(bytes.NewReader(blob), int64(len(blob))); err == nil {
			models[0] = m
			skins[0] = firstSkinAsPic(m)
			loaded++
		} else {
			logf("mdl.Load(%s) err: %v", alt1, err)
		}
	}
	startIdx := 0
	if models[0] != nil {
		startIdx = 1
	}
	for i := startIdx; i < 3; i++ {
		blob, ok := tryReadPakFile(pakFS, paths[i])
		if !ok {
			continue
		}
		m, err := mdl.Load(bytes.NewReader(blob), int64(len(blob)))
		if err != nil {
			logf("mdl.Load(%s) err: %v", paths[i], err)
			continue
		}
		models[i] = m
		skins[i] = firstSkinAsPic(m)
		loaded++
	}
	return
}

// firstSkinAsPic returns the model's first single-skin as a *render.Pic.
func firstSkinAsPic(m *mdl.Model) *render.Pic {
	if m == nil || len(m.Skins) == 0 {
		return nil
	}
	w := int(m.Header.SkinWidth)
	h := int(m.Header.SkinHeight)
	if w <= 0 || h <= 0 {
		return nil
	}
	var src []byte
	sk := m.Skins[0]
	switch sk.Type {
	case mdl.SkinSingle:
		src = sk.Single.Pixels
	case mdl.SkinGroup:
		if sk.Group == nil || len(sk.Group.Skins) == 0 {
			return nil
		}
		src = sk.Group.Skins[0].Pixels
	default:
		return nil
	}
	if len(src) != w*h {
		return nil
	}
	pix := make([]byte, len(src))
	copy(pix, src)
	return &render.Pic{Width: w, Height: h, Pixels: pix}
}

// hasSuffix is a local strings.HasSuffix.
func hasSuffix(s, suffix string) bool {
	if len(s) < len(suffix) {
		return false
	}
	return s[len(s)-len(suffix):] == suffix
}

// loadMenuAssets opens the menu pic lumps.
func loadMenuAssets(pakFS fs.FS, logf func(string, ...any)) (*menu.Assets, int, int) {
	if pakFS == nil {
		return &menu.Assets{}, 0, 0
	}
	a := &menu.Assets{}
	loaded, total := 0, 0
	overlay := newWADOverlay(pakFS, "gfx.wad")

	load := func(name string, dst **render.Pic) {
		total++
		blob, ok := tryReadPakFile(overlay, name)
		if !ok {
			logf("menu asset %s missing -- text fallback", name)
			return
		}
		pic, err := render.ParsePic(blob)
		if err != nil {
			logf("menu asset %s ParsePic err: %v -- text fallback", name, err)
			return
		}
		*dst = pic
		loaded++
	}

	load("gfx/qplaque.lmp", &a.QPlaque)
	load("gfx/ttl_main.lmp", &a.TitleMain)
	load("gfx/ttl_sgl.lmp", &a.TitleSinglePlayer)
	load("gfx/p_load.lmp", &a.TitleLoad)
	load("gfx/p_save.lmp", &a.TitleSave)
	load("gfx/p_option.lmp", &a.TitleOptions)
	load("gfx/mainmenu.lmp", &a.MainMenu)
	load("gfx/sp_menu.lmp", &a.SinglePlayerMenu)

	a.MenuDots = make([]*render.Pic, 6)
	for i := 0; i < 6; i++ {
		load(fmt.Sprintf("gfx/menudot%d.lmp", i+1), &a.MenuDots[i])
	}
	end := len(a.MenuDots)
	for end > 0 && a.MenuDots[end-1] == nil {
		end--
	}
	a.MenuDots = a.MenuDots[:end]

	return a, loaded, total
}

// loadSBarAssets opens the canonical sbar pic lumps.
func loadSBarAssets(pakFS fs.FS, logf func(string, ...any)) (*render.SBarAssets, int, int, []string) {
	if pakFS == nil {
		return nil, 0, 0, nil
	}
	a := &render.SBarAssets{}
	loaded, total := 0, 0
	var missing []string

	overlay := newWADOverlay(pakFS, "gfx.wad")

	load := func(name string, dst **render.Pic) {
		total++
		blob, ok := tryReadPakFile(overlay, name)
		if !ok {
			missing = append(missing, name)
			logf("sbar asset %s missing -- skipping", name)
			return
		}
		pic, err := render.ParsePic(blob)
		if err != nil {
			missing = append(missing, name)
			logf("sbar asset %s ParsePic err: %v -- skipping", name, err)
			return
		}
		*dst = pic
		loaded++
	}

	load("gfx/sbar.lmp", &a.BG)
	load("gfx/ibar.lmp", &a.IBar)

	for i := 0; i < 10; i++ {
		load(fmt.Sprintf("gfx/num_%d.lmp", i), &a.Nums[i])
	}
	for i := 0; i < 10; i++ {
		load(fmt.Sprintf("gfx/anum_%d.lmp", i), &a.AltNums[i])
	}
	var scratch *render.Pic
	load("gfx/num_minus.lmp", &scratch)
	scratch = nil
	load("gfx/anum_minus.lmp", &scratch)
	scratch = nil

	load("gfx/sb_shells.lmp", &a.Ammo[0])
	load("gfx/sb_nails.lmp", &a.Ammo[1])
	load("gfx/sb_rocket.lmp", &a.Ammo[2])
	load("gfx/sb_cells.lmp", &a.Ammo[3])

	for i := 0; i < 5; i++ {
		load(fmt.Sprintf("gfx/face%d.lmp", i+1), &a.Faces[i][0])
	}
	for i := 0; i < 5; i++ {
		load(fmt.Sprintf("gfx/face_p%d.lmp", i+1), &a.Faces[i][1])
	}

	load("gfx/sb_armor1.lmp", &a.Armor[0])
	load("gfx/sb_armor2.lmp", &a.Armor[1])
	load("gfx/sb_armor3.lmp", &a.Armor[2])

	weaponBase := []string{
		"gfx/inv_shotgun.lmp",
		"gfx/inv_sshotgun.lmp",
		"gfx/inv_nailgun.lmp",
		"gfx/inv_snailgun.lmp",
		"gfx/inv_rlaunch.lmp",
		"gfx/inv_srlaunch.lmp",
		"gfx/inv_lightng.lmp",
	}
	for i, name := range weaponBase {
		load(name, &a.Weapons[i])
	}
	for _, name := range []string{"gfx/inv2_lightng.lmp"} {
		load(name, &scratch)
		scratch = nil
	}

	load("gfx/sb_key1.lmp", &a.Key[0])
	load("gfx/sb_key2.lmp", &a.Key[1])
	load("gfx/sb_invis.lmp", &a.Invis)
	load("gfx/sb_invuln.lmp", &a.Invuln)
	load("gfx/sb_quad.lmp", &a.Quad)
	load("gfx/sb_suit.lmp", &a.Suit)
	for i := 0; i < 4; i++ {
		load(fmt.Sprintf("gfx/sb_sigil%d.lmp", i+1), &a.Sigil[i])
	}

	return a, loaded, total, missing
}

// loadMiptexPicsNamed decodes the BSP's LUMP_TEXTURES.
func loadMiptexPicsNamed(file *bspfile.File) ([]*render.Pic, []string, int, int, error) {
	mtl, err := file.Textures()
	if err != nil {
		return nil, nil, 0, 0, fmt.Errorf("file.Textures: %w", err)
	}
	total := int(mtl.NumMipTex)
	pics := make([]*render.Pic, total)
	names := make([]string, total)
	loaded := 0
	for i := 0; i < total; i++ {
		mt, ok, err := mtl.MipTex(i)
		if err != nil {
			continue
		}
		if !ok || mt == nil {
			continue
		}
		px, err := mt.Pixels(0)
		if err != nil {
			continue
		}
		buf := make([]byte, len(px))
		copy(buf, px)
		pics[i] = &render.Pic{
			Width:  int(mt.Width),
			Height: int(mt.Height),
			Pixels: buf,
		}
		names[i] = mt.Name
		loaded++
	}
	return pics, names, loaded, total, nil
}

// pickInMapCamera returns a viewpoint that lands inside a valid leaf.
func pickInMapCamera(bm *model.BrushModel, file *bspfile.File) [3]float32 {
	models, err := file.Models()
	if err != nil || len(models) == 0 {
		return [3]float32{0, 0, 0}
	}
	m := &models[0]
	centre := [3]float32{
		(m.Mins[0] + m.Maxs[0]) * 0.5,
		(m.Mins[1] + m.Maxs[1]) * 0.5,
		(m.Mins[2] + m.Maxs[2]) * 0.5,
	}
	if leaf := bm.PointInLeaf(centre); leaf > 0 {
		return centre
	}
	const steps = 9
	for ix := 0; ix < steps; ix++ {
		for iy := 0; iy < steps; iy++ {
			for iz := 0; iz < steps; iz++ {
				p := [3]float32{
					m.Mins[0] + (m.Maxs[0]-m.Mins[0])*float32(ix+1)/float32(steps+1),
					m.Mins[1] + (m.Maxs[1]-m.Mins[1])*float32(iy+1)/float32(steps+1),
					m.Mins[2] + (m.Maxs[2]-m.Mins[2])*float32(iz+1)/float32(steps+1),
				}
				if leaf := bm.PointInLeaf(p); leaf > 0 {
					return p
				}
			}
		}
	}
	return centre
}

// findPlayerStart scans the map's entity lump for the info_player_start
// spawn point and returns its origin + spawn yaw (the "angle" key, 0 when
// absent). ok is false when the map has no info_player_start or the entity
// lump can't be parsed -- the caller then falls back to a geometric camera.
// Reading the spawn from the BSP directly does not depend on the QC
// SelectSpawnPoint/find path, which the bring-up host does not fully wire.
func findPlayerStart(file *bspfile.File) (origin [3]float32, yaw float32, ok bool) {
	return parsePlayerStart(file.Entities())
}

// parsePlayerStart is the testable core of [findPlayerStart]: it parses a raw
// entity-lump blob and returns the first info_player_start's origin + yaw.
func parsePlayerStart(blob []byte) (origin [3]float32, yaw float32, ok bool) {
	ents, err := entparse.ParseEntities(blob)
	if err != nil {
		return [3]float32{}, 0, false
	}
	for _, e := range ents {
		if e["classname"] != "info_player_start" {
			continue
		}
		var o [3]float32
		if n, _ := fmt.Sscanf(e["origin"], "%g %g %g", &o[0], &o[1], &o[2]); n != 3 {
			continue
		}
		var y float32
		_, _ = fmt.Sscanf(e["angle"], "%g", &y) // absent/blank angle -> 0
		return o, y, true
	}
	return [3]float32{}, 0, false
}

// buildDemoWaypoints returns a small set of in-map view origins.
func buildDemoWaypoints(bm *model.BrushModel, file *bspfile.File, anchor [3]float32) [][3]float32 {
	out := [][3]float32{anchor}
	models, err := file.Models()
	if err != nil || len(models) == 0 {
		return out
	}
	m := &models[0]
	const (
		nx = 4
		ny = 4
	)
	for ix := 0; ix < nx; ix++ {
		for iy := 0; iy < ny; iy++ {
			fx := float32(ix+1) / float32(nx+1)
			fy := float32(iy+1) / float32(ny+1)
			p := [3]float32{
				m.Mins[0] + (m.Maxs[0]-m.Mins[0])*fx,
				m.Mins[1] + (m.Maxs[1]-m.Mins[1])*fy,
				anchor[2],
			}
			if leaf := bm.PointInLeaf(p); leaf > 0 {
				out = append(out, p)
			}
		}
	}
	const maxWaypoints = 4
	if len(out) > maxWaypoints {
		out = out[:maxWaypoints]
	}
	return out
}

// Reference enginehost so the package import is non-dead even though
// the helpers above don't need it directly.
var _ = enginehost.ErrNoEdict
