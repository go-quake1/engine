// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package render

import (
	"errors"

	"github.com/go-quake1/engine/client"
)

// SBarHeight is the status bar's pixel height. tyrquake: SBAR_HEIGHT
// (24) in NQ/sbar.h. The bar always occupies the bottom 24 scanlines
// of the framebuffer; everything sbar.c draws is positioned relative
// to (0, scr_scaled_height - SBAR_HEIGHT).
const SBarHeight = 24

// numDigitWidth is the pixel pitch between consecutive sb_nums digits
// when [DrawNumber] lays them out. tyrquake hard-codes 24 in
// Sbar_DrawNum's `x += 24` step (matching the `num_X` WAD pic width).
const numDigitWidth = 24

// numAmmoIcons + numFaces + numArmorTiers + numKeys + numSigils +
// numWeaponSlots size the [SBarAssets] arrays. All values are taken
// from NQ/sbar.c's static-pic tables; no gameplay logic depends on
// them being tunable.
const (
	numAmmoIcons    = 4 // shells / nails / rockets / cells
	numFaces        = 5 // 5 health bands (0-19, 20-39, 40-59, 60-79, 80+)
	numFaceStates   = 2 // [0] normal, [1] pained (= recently damaged)
	numArmorTiers   = 3 // green / yellow / red
	numKeys         = 2 // silver / gold
	numSigils       = 4 // 4 episode-end runes
	numWeaponSlots  = 7 // shotgun .. lightning (single-player set)
	numDigits       = 10
	armorAltThresh  = 25 // armor <= 25 -> alt (yellow) digits
	healthAltThresh = 25 // health <= 25 -> alt (yellow) digits
	ammoAltThresh   = 10 // ammo <= 10 -> alt (yellow) digits
)

// Stat-bank indices the status bar reads from [client.State.Stats].
// tyrquake: NQ/quakedef.h STAT_* defines (the bank is generic so the
// Go port mirrors the C indices instead of adding named accessors).
const (
	statArmor = 4 // STAT_ARMOR
)

// Item-flag bits the single-player layout cares about. tyrquake:
// NQ/quakedef.h IT_* defines.
const (
	itemShells          = 1 << 8
	itemNails           = 1 << 9
	itemRockets         = 1 << 10
	itemCells           = 1 << 11
	itemArmor1          = 1 << 13
	itemArmor2          = 1 << 14
	itemArmor3          = 1 << 15
	itemKey1            = 1 << 17
	itemKey2            = 1 << 18
	itemInvisibility    = 1 << 19
	itemInvulnerability = 1 << 20
	itemSuit            = 1 << 21
	itemQuad            = 1 << 22
	itemSigil0          = 1 << 28
)

// SBarAssets bundles every Pic the status bar needs to render the
// single-player HUD. Callers load each Pic once from gfx.wad (via the
// loadpic + wad subsystems) and pass the bundle by reference per
// frame -- the renderer never owns the pixel storage.
//
// tyrquake: the file-scope sb_* / draw_disc pointer table in
// NQ/sbar.c that Sbar_InitPics populates.
type SBarAssets struct {
	Nums    [numDigits]*Pic               // digit pics 0..9 (white)   -- num_0..num_9
	AltNums [numDigits]*Pic               // digit pics 0..9 (yellow)  -- anum_0..anum_9
	Faces   [numFaces][numFaceStates]*Pic // 5 health bands x (normal, pained) -- face[1-5] / face_p[1-5]
	Ammo    [numAmmoIcons]*Pic            // sb_shells / sb_nails / sb_rocket / sb_cells
	BG      *Pic                          // status bar background     -- sbar
	IBar    *Pic                          // inventory bar background  -- ibar
	Weapons [numWeaponSlots]*Pic          // inv_shotgun .. inv_lightng
	Armor   [numArmorTiers]*Pic           // sb_armor1 / sb_armor2 / sb_armor3
	Sigil   [numSigils]*Pic               // sb_sigil1..sb_sigil4
	Key     [numKeys]*Pic                 // sb_key1 / sb_key2
	Invis   *Pic                          // sb_invis
	Invuln  *Pic                          // sb_invuln
	Quad    *Pic                          // sb_quad
	Suit    *Pic                          // sb_suit
	Disc    *Pic                          // disconnected / invulnerable disc overlay (draw_disc)
	Colon   *Pic                          // num_colon -- intermission separator
	Slash   *Pic                          // num_slash -- intermission separator
}

// Sentinel errors returned by the status bar primitives. Match the
// nil-arg pattern the rest of the render package uses
// ([ErrDrawNilFB] etc.); the sbar layer adds dedicated names so
// callers can distinguish a render-side nil from a state-side nil.
var (
	ErrSbarNilFB     = errors.New("render: nil framebuffer in sbar op")
	ErrSbarNilState  = errors.New("render: nil client state in sbar op")
	ErrSbarNilAssets = errors.New("render: nil sbar assets in sbar op")
)

// pickDigitSet returns the [SBarAssets.Nums] (alt=false) or
// [SBarAssets.AltNums] (alt=true) slice -- the indirection keeps
// [DrawNumber]'s digit loop free of branches.
func pickDigitSet(assets *SBarAssets, alt bool) *[numDigits]*Pic {
	if alt {
		return &assets.AltNums
	}
	return &assets.Nums
}

// DrawNumber renders an integer at (x, y) using `digits` columns,
// each [numDigitWidth] pixels wide. Negative numbers clamp to 0
// (the tyrquake upstream draws a '-' frame; the Go port collapses
// the negative case to zero -- the only call sites that could feed
// a negative value are armor/health/ammo, all of which are clamped
// non-negative at the wire level). Overflow truncates the high
// digits (n=12345 with digits=3 renders "345"), matching the
// upstream's silent rollover in Sbar_itoa + Sbar_DrawNum.
//
// alt=true selects [SBarAssets.AltNums] (yellow); alt=false picks
// [SBarAssets.Nums] (white). tyrquake: the `color` parameter on
// Sbar_DrawNum (0 = white, 1 = yellow).
//
// Errors:
//
//	ErrSbarNilFB      fb == nil
//	ErrSbarNilAssets  assets == nil OR a picked digit Pic is nil
//
// tyrquake: Sbar_DrawNum in NQ/sbar.c.
func DrawNumber(fb *FrameBuffer, assets *SBarAssets, x, y, n, digits int, alt bool) error {
	if fb == nil {
		return ErrSbarNilFB
	}
	if assets == nil {
		return ErrSbarNilAssets
	}
	if n < 0 {
		n = 0
	}
	// Format n as a decimal string; if it is too long, drop the
	// high digits (mirrors tyrquake's `ptr += (l - digits)` step).
	str := itoaDigits(n)
	if len(str) > digits {
		str = str[len(str)-digits:]
	}
	// If shorter, right-justify by advancing x past the missing
	// leading columns (matches tyrquake's `x += (digits - l) * 24`).
	if len(str) < digits {
		x += (digits - len(str)) * numDigitWidth
	}
	set := pickDigitSet(assets, alt)
	for i := 0; i < len(str); i++ {
		pic := set[str[i]-'0']
		if pic == nil {
			return ErrSbarNilAssets
		}
		if err := DrawTransPic(fb, x, y, pic); err != nil {
			return err
		}
		x += numDigitWidth
	}
	return nil
}

// itoaDigits formats a non-negative int as its decimal digit string.
// Specialised to skip strconv import + sign handling (the caller
// guarantees n >= 0). The "0" short-circuit matches the canonical
// itoa idiom and keeps the do/while shape of upstream Sbar_itoa.
func itoaDigits(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// PickFaceFrame returns the (row, col) into [SBarAssets.Faces] for
// the player's current health. Rows index the 5 health bands the
// upstream uses (80+, 60-79, 40-59, 20-39, 0-19) -- band 4 is the
// healthiest face, band 0 is the gibbed/near-death frame. col 0 is
// the resting face; col 1 is the "pained" twin played for half a
// second after damage (tyrquake: cl.faceanimtime).
//
// Negative health clamps to 0 (band 0). tyrquake: the
// `f = cl.stats[STAT_HEALTH] / 20` step inside Sbar_DrawFace.
func PickFaceFrame(health int, recentlyDamaged bool) (row, col int) {
	if health < 0 {
		health = 0
	}
	if health >= 100 {
		row = 4
	} else {
		row = health / 20
	}
	if recentlyDamaged {
		col = 1
	}
	return
}

// DrawFace blits the face icon for the current health at (x, y).
// Wraps [PickFaceFrame] + [DrawTransPic]; same nil-arg errors as
// [DrawNumber] plus [ErrSbarNilAssets] when the picked face Pic is
// nil. tyrquake: the Sbar_DrawPic(112, 0, sb_faces[f][anim]) tail of
// Sbar_DrawFace.
func DrawFace(fb *FrameBuffer, assets *SBarAssets, x, y int, health int, recentlyDamaged bool) error {
	if fb == nil {
		return ErrSbarNilFB
	}
	if assets == nil {
		return ErrSbarNilAssets
	}
	row, col := PickFaceFrame(health, recentlyDamaged)
	pic := assets.Faces[row][col]
	if pic == nil {
		return ErrSbarNilAssets
	}
	return DrawTransPic(fb, x, y, pic)
}

// pickAmmoIcon returns the [SBarAssets.Ammo] slot for the player's
// current ammo category, derived from the item flag-bits in
// `items`. The order matches the upstream `else-if` chain in
// Sbar_Draw (shells -> nails -> rockets -> cells); the second
// return value is false when no ammo flag is set, so callers can
// skip the icon entirely (matches the upstream's silent no-op).
func pickAmmoIcon(items int32) (int, bool) {
	switch {
	case items&itemShells != 0:
		return 0, true
	case items&itemNails != 0:
		return 1, true
	case items&itemRockets != 0:
		return 2, true
	case items&itemCells != 0:
		return 3, true
	}
	return 0, false
}

// pickArmorIcon returns the [SBarAssets.Armor] slot (0 = green,
// 1 = yellow, 2 = red) for the player's current armor flag, matching
// the upstream's else-if chain (red wins over yellow wins over
// green). The second return value is false when no armor flag is
// set.
func pickArmorIcon(items int32) (int, bool) {
	switch {
	case items&itemArmor3 != 0:
		return 2, true
	case items&itemArmor2 != 0:
		return 1, true
	case items&itemArmor1 != 0:
		return 0, true
	}
	return 0, false
}

// drawIfNotNil is a small helper -- the inventory + item layer
// touches many optional pics; this hides the nil-skip pattern so
// the call sites stay flat.
func drawIfNotNil(fb *FrameBuffer, x, y int, pic *Pic) error {
	if pic == nil {
		return nil
	}
	return DrawTransPic(fb, x, y, pic)
}

// drawIBar paints the inventory bar above the status bar (the
// upstream IBAR strip at y = baseY - SBarHeight). It also walks the
// weapon-flag bits and blits the corresponding [SBarAssets.Weapons]
// icon for every owned weapon. Returns the first DrawTransPic error
// from the chain.
//
// tyrquake: the head of Sbar_DrawInventory (the single-player
// branches only; hipnotic + rogue + flash animation are out of
// scope -- this port renders a static "owned" icon, no flash).
func drawIBar(fb *FrameBuffer, assets *SBarAssets, items int32, baseY int) error {
	ibarY := baseY - SBarHeight
	if assets.IBar != nil {
		if err := DrawTransPic(fb, 0, ibarY, assets.IBar); err != nil {
			return err
		}
	}
	weaponsY := baseY - 16
	for i := 0; i < numWeaponSlots; i++ {
		if items&(int32(1)<<i) == 0 {
			continue
		}
		if err := drawIfNotNil(fb, i*numDigitWidth, weaponsY, assets.Weapons[i]); err != nil {
			return err
		}
	}
	return nil
}

// drawItemRow paints the right-aligned item icons (keys, invis,
// invuln, suit, quad, sigils) on the inventory bar. tyrquake: the
// item-loop tail of Sbar_DrawInventory plus the sigil loop. Keys
// occupy the first two slots; powerups follow.
func drawItemRow(fb *FrameBuffer, assets *SBarAssets, items int32, baseY int) error {
	y := baseY - 16
	// Slots 0..5 mirror upstream's sb_items[] order (key1, key2,
	// invis, invuln, suit, quad) at x = 192 + i*16.
	slots := [6]*Pic{
		assets.Key[0],
		assets.Key[1],
		assets.Invis,
		assets.Invuln,
		assets.Suit,
		assets.Quad,
	}
	bits := [6]int32{itemKey1, itemKey2, itemInvisibility, itemInvulnerability, itemSuit, itemQuad}
	for i := 0; i < 6; i++ {
		if items&bits[i] == 0 {
			continue
		}
		if err := drawIfNotNil(fb, 192+i*16, y, slots[i]); err != nil {
			return err
		}
	}
	for i := 0; i < numSigils; i++ {
		if items&(itemSigil0<<i) == 0 {
			continue
		}
		if err := drawIfNotNil(fb, 320-32+i*8, y, assets.Sigil[i]); err != nil {
			return err
		}
	}
	return nil
}

// drawArmorBlock paints the armor digit + the appropriate armor
// icon. When the player is invulnerable, the upstream replaces the
// armor digit with "666" and overlays the disc icon; the Go port
// preserves that behaviour for single-player parity.
//
// tyrquake: the armor branch at the top of Sbar_Draw.
func drawArmorBlock(fb *FrameBuffer, assets *SBarAssets, items int32, armor, baseY int) error {
	if items&itemInvulnerability != 0 {
		if err := DrawNumber(fb, assets, 24, baseY, 666, 3, true); err != nil {
			return err
		}
		return drawIfNotNil(fb, 0, baseY, assets.Disc)
	}
	if err := DrawNumber(fb, assets, 24, baseY, armor, 3, armor <= armorAltThresh); err != nil {
		return err
	}
	if idx, ok := pickArmorIcon(items); ok {
		return drawIfNotNil(fb, 0, baseY, assets.Armor[idx])
	}
	return nil
}

// drawAmmoBlock paints the ammo icon + the current-weapon ammo
// count. The icon is omitted when no ammo flag is set; the count is
// always drawn (tyrquake mirror).
//
// tyrquake: the ammo branch at the tail of Sbar_Draw.
func drawAmmoBlock(fb *FrameBuffer, assets *SBarAssets, items int32, ammo, baseY int) error {
	if idx, ok := pickAmmoIcon(items); ok {
		if err := drawIfNotNil(fb, 224, baseY, assets.Ammo[idx]); err != nil {
			return err
		}
	}
	return DrawNumber(fb, assets, 248, baseY, ammo, 3, ammo <= ammoAltThresh)
}

// DrawSBar renders the full single-player status bar at the bottom
// of fb (y = fb.Height - SBarHeight). Reads Health / Items / Stats
// (for armor) / CurrentAmmo from `state`. The multiplayer
// scoreboard overlay (Sbar_DeathmatchOverlay /
// Sbar_MiniDeathmatchOverlay) is out of scope; deathmatch HUDs use
// the same primitives plus an overlay layer that lives elsewhere.
//
// Layout (all coordinates relative to the status bar's top-left
// corner, i.e. (0, fb.Height - SBarHeight); pixel values verbatim
// from NQ/sbar.c):
//
//	(0, -24)      : inventory bar background (IBar)
//	(i*24, -16)   : weapon icon i (i in 0..6)
//	(192+i*16,-16): item icon i (keys / invis / invuln / suit / quad)
//	(288+i*8, -16): sigil i (4 episode runes)
//	(0, 0)        : status bar background (BG)
//	(0, 0)        : armor icon overlay
//	(24, 0)       : armor digits (3 wide, alt if armor <= 25)
//	(112, 0)      : face icon
//	(136, 0)      : health digits (3 wide, alt if health <= 25)
//	(224, 0)      : active-ammo icon
//	(248, 0)      : ammo digits (3 wide, alt if ammo <= 10)
//
// Returns [ErrSbarNilFB] / [ErrSbarNilState] / [ErrSbarNilAssets]
// on nil arguments.
//
// tyrquake: Sbar_Draw in NQ/sbar.c.
func DrawSBar(fb *FrameBuffer, state *client.State, assets *SBarAssets) error {
	if fb == nil {
		return ErrSbarNilFB
	}
	if state == nil {
		return ErrSbarNilState
	}
	if assets == nil {
		return ErrSbarNilAssets
	}
	// VHeight so the bar anchors at the virtual bottom; with
	// HUDScale > 1 this is fb.Height / scale, and the low-level
	// DrawTransPic call upscales the BG (320x24 virtual) into a
	// (320*scale) x (24*scale) block at physical (0, baseY*scale).
	// At HUDScale == 1 the math collapses to the vanilla behaviour.
	baseY := fb.VHeight() - SBarHeight

	// Inventory strip (weapons + items + sigils) above the main bar.
	if err := drawIBar(fb, assets, state.Items, baseY); err != nil {
		return err
	}
	if err := drawItemRow(fb, assets, state.Items, baseY); err != nil {
		return err
	}

	// Main status bar background.
	if assets.BG != nil {
		if err := DrawTransPic(fb, 0, baseY, assets.BG); err != nil {
			return err
		}
	}

	// Armor (digit + icon) + face + health + ammo (icon + digit).
	armor := int(state.Stats[statArmor])
	if err := drawArmorBlock(fb, assets, state.Items, armor, baseY); err != nil {
		return err
	}
	if err := DrawFace(fb, assets, 112, baseY, state.Health, false); err != nil {
		return err
	}
	if err := DrawNumber(fb, assets, 136, baseY, state.Health, 3, state.Health <= healthAltThresh); err != nil {
		return err
	}
	return drawAmmoBlock(fb, assets, state.Items, state.CurrentAmmo, baseY)
}
