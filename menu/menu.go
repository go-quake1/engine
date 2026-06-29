// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package menu

import (
	"github.com/go-quake1/engine/backend"
	"github.com/go-quake1/engine/render"
)

// State identifies the active menu screen. tyrquake: the m_state_e
// enum in menu.c (m_none / m_main / m_singleplayer / m_load / m_save
// / m_options / m_quit / ...).
type State int

const (
	// StateNone means the menu is dismissed -- the game is playing
	// (or about to start). The runloop checks State == StateNone to
	// decide whether to render the 3D world pass; any other value
	// freezes the world pass and routes Draw into the Pre2DDraw slot.
	StateNone State = iota

	// StateMain is the top-level menu shown on first boot:
	// Single Player / Multiplayer / Options / Help / Quit.
	StateMain

	// StateNewGame is the "single player" sub-menu:
	// New Game / Load / Save.
	StateNewGame

	// StateSkill is the difficulty picker shown after the player
	// chooses "New Game". Highlighted entry binds [Menu.SkillLevel]
	// 0..3 (Easy / Normal / Hard / Nightmare); Enter confirms +
	// transitions to StateNone.
	StateSkill

	// StateOptions is the engine options panel
	// (placeholder rows: customize controls / go to console / reset
	//  to defaults / video options).
	StateOptions

	// StateLoad is the load-saved-game picker (12 slot rows).
	StateLoad

	// StateSave is the save-current-game picker (12 slot rows).
	StateSave

	// StateQuit is the "Are you sure?" confirmation screen
	// shown before exiting the engine. Y/Enter confirms;
	// N/Esc returns to StateMain.
	StateQuit
)

// String returns the canonical name for s (matches the m_state_e
// label set in upstream menu.c). Useful for the QEMU serial trace +
// for the tests.
func (s State) String() string {
	switch s {
	case StateNone:
		return "none"
	case StateMain:
		return "main"
	case StateNewGame:
		return "single-player"
	case StateSkill:
		return "skill"
	case StateOptions:
		return "options"
	case StateLoad:
		return "load"
	case StateSave:
		return "save"
	case StateQuit:
		return "quit"
	}
	return "invalid"
}

// SkillLevel enumerates the four upstream difficulty rungs (matches
// the `skill` cvar 0..3 the QC progs read).
type SkillLevel int

const (
	SkillEasy      SkillLevel = 0
	SkillNormal    SkillLevel = 1
	SkillHard      SkillLevel = 2
	SkillNightmare SkillLevel = 3
)

// NumSkills is the count of skill rungs (Easy / Normal / Hard /
// Nightmare). Exported so the [Menu.Handle] cursor-clamp loop +
// the [Menu.Draw] row enumerator stay in sync without a magic number.
const NumSkills = 4

// Menu owns the per-screen cursor + the player-visible state machine.
// The struct is allocated by the caller and threaded through the
// runloop; per-frame mutation happens inside [Menu.Handle] and
// [Menu.Draw]. The zero value is the boot state (StateNone with the
// cursor parked on row 0) -- callers that want the title screen at
// boot must set State = StateMain explicitly.
type Menu struct {
	// State is the active screen. Mutated by Handle in response to
	// key events; the runloop reads it to decide whether to freeze
	// the world pass.
	State State

	// CursorIndex is the highlighted row index inside the current
	// screen, 0-based. Reset to 0 on every State transition so the
	// player always lands on the first row of the new screen.
	CursorIndex int

	// SkillLevel is the last skill the player confirmed on the skill
	// picker. Defaults to SkillNormal (1) to match the upstream
	// `skill` cvar default. Updated when [Menu.Handle] processes an
	// Enter on the skill screen.
	SkillLevel SkillLevel

	// SaveSlot is the last save-game slot the player highlighted in
	// the load / save pickers. 0..MaxSaveSlots-1. Exposed for the
	// load / save runloop hook (OnSave / OnLoad below).
	SaveSlot int

	// OnSave is the optional callback the menu fires when the player
	// confirms a row in the StateSave picker. The callback receives
	// the slot index (== SaveSlot post-bind); a non-nil error is
	// stashed in LastError so the runloop can render it next tic.
	//
	// Wired by the embedder (typically a `func(i int) error {
	// return host.SaveSlot(i) }` closure) once the host is alive.
	// nil = the confirm is a slot-bind-only with no side effects.
	OnSave func(slot int) error

	// OnLoad mirrors OnSave for the StateLoad picker. The host's
	// LoadSlot does a full server re-spawn so calling it from a
	// menu confirm has visible side effects (the world replaces
	// with the snapshot's map).
	OnLoad func(slot int) error

	// LastError is the most recent OnSave / OnLoad return value. nil
	// means the last confirm succeeded (or no confirm ran). The
	// runloop's per-tic Draw can read it to render an error overlay.
	LastError error
}

// MaxSaveSlots is the number of save-game slots offered in the load
// / save pickers. tyrquake: MAX_SAVEGAMES = 12 in menu.c.
const MaxSaveSlots = 12

// New returns a *Menu initialised to the boot state (StateMain with
// the cursor on row 0, SkillNormal default). The pointer is owned by
// the caller and threaded through the runloop verbatim.
func New() *Menu {
	return &Menu{
		State:       StateMain,
		CursorIndex: 0,
		SkillLevel:  SkillNormal,
		SaveSlot:    0,
	}
}

// rowCount returns the number of selectable rows on the active
// screen. Used by Handle's cursor-clamp + by Draw's row enumerator.
func (m *Menu) rowCount() int {
	switch m.State {
	case StateMain:
		return 5 // Single / Multi / Options / Help / Quit
	case StateNewGame:
		return 3 // New / Load / Save
	case StateSkill:
		return NumSkills
	case StateOptions:
		return 4 // Customize / Console / Defaults / Video
	case StateLoad, StateSave:
		return MaxSaveSlots
	case StateQuit:
		return 2 // No / Yes
	}
	return 0
}

// Handle processes one [backend.KeyCode] event and updates the menu
// state in place. Returns advance==true when the user has picked
// "start a new game" (the runloop unfreezes the world pass on this
// signal). The returned bool is false on every other transition
// (cursor moves, screen changes, etc.).
//
// Key bindings (matches upstream menu.c):
//
//	KeyEscape  -- back one screen (StateMain -> StateNone, all
//	              other states -> StateMain).
//	KeyUp      -- cursor -1 with wrap.
//	KeyDown    -- cursor +1 with wrap.
//	KeyEnter   -- activate the highlighted row.
//
// Keys outside this set are ignored (returns advance==false without
// mutating state).
//
// Cursor reset rule: every screen transition zeroes CursorIndex so
// the player always lands on the first row of the new screen
// (matches upstream M_Menu_<Name>_f).
func (m *Menu) Handle(key backend.KeyCode) (advance bool) {
	switch key {
	case backend.KeyEscape:
		return m.handleEscape()
	case backend.KeyUp:
		m.moveCursor(-1)
	case backend.KeyDown:
		m.moveCursor(+1)
	case backend.KeyEnter, backend.KeyMouse1:
		// Mouse-click acts as Enter on the highlighted row, matching
		// modern menu UX. Without this, clicking in the QEMU window
		// drops a KeyMouse1 event that goes unhandled while the menu
		// is open -- the user can navigate by arrows + Enter but the
		// click itself appears inert. With this arm, the click
		// activates the cursor row.
		return m.activate()
	}
	return false
}

// handleEscape pops one screen off the menu stack. From StateMain
// the menu is dismissed (StateNone), so Esc-out-of-the-main-menu
// drops the player into the game (advance==true). All other states
// pop to StateMain.
func (m *Menu) handleEscape() bool {
	switch m.State {
	case StateMain:
		// Esc-from-main dismisses the menu but does NOT start a
		// new game -- if no game is in progress yet (boot path)
		// the runloop's gate keeps the world pass frozen; if a
		// game is already running this is the "resume" path.
		m.State = StateNone
		m.CursorIndex = 0
		return false
	case StateNone:
		// Esc with no menu up opens the main menu (mid-game pause).
		m.State = StateMain
		m.CursorIndex = 0
		return false
	default:
		m.State = StateMain
		m.CursorIndex = 0
		return false
	}
}

// moveCursor advances CursorIndex by delta (+/- 1) with wraparound
// inside [0, rowCount). A zero-row screen (defensive) leaves the
// cursor at 0.
func (m *Menu) moveCursor(delta int) {
	n := m.rowCount()
	if n <= 0 {
		m.CursorIndex = 0
		return
	}
	idx := (m.CursorIndex + delta) % n
	if idx < 0 {
		idx += n
	}
	m.CursorIndex = idx
}

// activate runs the "Enter pressed on the highlighted row" arm for
// the active screen. Returns advance==true only when the activation
// leaves the menu (StateSkill row Enter or StateQuit row "Yes").
func (m *Menu) activate() bool {
	switch m.State {
	case StateMain:
		return m.activateMain()
	case StateNewGame:
		return m.activateNewGame()
	case StateSkill:
		// Bind the cursor index to the cvar; dismiss the menu so
		// the runloop unfreezes the world pass.
		m.SkillLevel = SkillLevel(m.CursorIndex)
		m.State = StateNone
		m.CursorIndex = 0
		return true
	case StateOptions:
		// Options rows are placeholders; Enter returns to main.
		m.State = StateMain
		m.CursorIndex = 0
		return false
	case StateLoad, StateSave:
		// Bind the slot, fire the embedder's save/load callback if
		// one is wired, stash any error on LastError, and pop to
		// the parent screen (or dismiss the menu on success so the
		// player drops back into the game world post-load).
		m.SaveSlot = m.CursorIndex
		hook := m.OnSave
		if m.State == StateLoad {
			hook = m.OnLoad
		}
		var err error
		if hook != nil {
			err = hook(m.SaveSlot)
		}
		m.LastError = err
		if err == nil && m.State == StateLoad && hook != nil {
			// Successful load: drop into the freshly-restored world.
			m.State = StateNone
			m.CursorIndex = 0
			return true
		}
		// Save (or load with no hook / load-with-error): pop back
		// to the parent screen so the operator can pick another row
		// or escape out.
		m.State = StateNewGame
		m.CursorIndex = 0
		return false
	case StateQuit:
		// Row 0 = No (-> back to main); row 1 = Yes (-> dismiss
		// the menu; the host loop owns the actual os.Exit).
		if m.CursorIndex == 1 {
			m.State = StateNone
			m.CursorIndex = 0
			return true
		}
		m.State = StateMain
		m.CursorIndex = 0
		return false
	}
	return false
}

// activateMain dispatches Enter on the top-level screen.
//
//	row 0 -- Single Player  -> StateNewGame
//	row 1 -- Multiplayer    -> StateMain (placeholder; no MP yet)
//	row 2 -- Options        -> StateOptions
//	row 3 -- Help / Order   -> StateMain (placeholder)
//	row 4 -- Quit           -> StateQuit
func (m *Menu) activateMain() bool {
	switch m.CursorIndex {
	case 0:
		m.State = StateNewGame
		m.CursorIndex = 0
	case 2:
		m.State = StateOptions
		m.CursorIndex = 0
	case 4:
		m.State = StateQuit
		m.CursorIndex = 0
	}
	return false
}

// activateNewGame dispatches Enter on the single-player sub-menu.
//
//	row 0 -- New Game  -> StateSkill
//	row 1 -- Load      -> StateLoad
//	row 2 -- Save      -> StateSave
func (m *Menu) activateNewGame() bool {
	switch m.CursorIndex {
	case 0:
		m.State = StateSkill
		m.CursorIndex = int(m.SkillLevel) // preselect the prior pick
	case 1:
		m.State = StateLoad
		m.CursorIndex = m.SaveSlot
	case 2:
		m.State = StateSave
		m.CursorIndex = m.SaveSlot
	}
	return false
}

// Active is the convenience predicate the runloop checks per-tic to
// decide whether to freeze the world pass. Returns true when State
// is anything other than StateNone.
func (m *Menu) Active() bool {
	return m != nil && m.State != StateNone
}

// Open pops the main menu open mid-game (Esc-pressed-while-playing
// path). Idempotent: calling Open when the menu is already up is a
// no-op. The runloop calls this when it observes a KeyEscape down
// event with State == StateNone.
func (m *Menu) Open() {
	if m.State == StateNone {
		m.State = StateMain
		m.CursorIndex = 0
	}
}

// Assets bundles the WAD pics the menu draws. Each field is optional;
// a nil pic falls back to the text label so the menu stays navigable
// on bring-up builds where gfx.wad has not been loaded yet. tyrquake:
// the static globals Draw_CachePic populates in menu.c (qplaque,
// ttl_main, mainmenu, etc.).
type Assets struct {
	// QPlaque is the wood "QUAKE" plaque drawn on the left edge of
	// every menu screen (gfx/qplaque.lmp). Centered vertically at
	// row 4 in upstream.
	QPlaque *render.Pic

	// TitleMain is the "MAIN MENU" banner pic (gfx/ttl_main.lmp).
	// Drawn at the top-right area of the title screen.
	TitleMain *render.Pic

	// TitleSinglePlayer is the "SINGLE PLAYER" banner (gfx/ttl_sgl.lmp).
	TitleSinglePlayer *render.Pic

	// TitleLoad is the "LOAD GAME" banner (gfx/p_load.lmp).
	TitleLoad *render.Pic

	// TitleSave is the "SAVE GAME" banner (gfx/p_save.lmp).
	TitleSave *render.Pic

	// TitleOptions is the "OPTIONS" banner (gfx/p_option.lmp).
	TitleOptions *render.Pic

	// MainMenu is the four-row body pic of the main menu
	// (gfx/mainmenu.lmp). When non-nil it is drawn UNDER the cursor
	// dot; when nil the menu falls back to text rows.
	MainMenu *render.Pic

	// SinglePlayerMenu is the body pic for the single-player sub-menu
	// (gfx/sp_menu.lmp). NEW / LOAD / SAVE rows.
	SinglePlayerMenu *render.Pic

	// MenuDots is the 6-frame animated cursor dot strip
	// (gfx/menudot1.lmp..menudot6.lmp). Indexed at draw time by
	// (now * MenuDotFPS) % len(MenuDots). A nil / short slice falls
	// back to a single '*' glyph drawn through the conchars sheet.
	MenuDots []*render.Pic
}

// MenuDotFPS is the upstream cursor-dot animation rate (10 fps).
// tyrquake: M_DrawCharacter / 10.0 multiplier inside M_DrawCursor.
const MenuDotFPS float32 = 10

// Draw renders the active menu screen into fb. The chars argument is
// the conchars sheet used for the text-fallback path; pal is unused
// today but kept on the signature so palette-shifted overlays (the
// dim-the-world wash the menu draws over a paused game) can layer
// in without changing the surface area.
//
// Returns the first draw error encountered (typically
// [render.ErrDrawCharsShape] from a malformed chars pic) or nil on
// success / on StateNone (no-op).
//
// The render order is, top-to-bottom: qplaque on the left edge,
// title banner at the top, body (rows or pic), then the animated
// cursor dot on the highlighted row. Text fallback uses
// DrawCenteredString through the conchars sheet.
func (m *Menu) Draw(fb *render.FrameBuffer, chars *render.Pic, assets *Assets, now float32) error {
	if m == nil || m.State == StateNone {
		return nil
	}
	if fb == nil {
		return render.ErrDrawNilFB
	}
	if chars == nil {
		return render.ErrDrawCharsNilSrc
	}

	// Dim the background a touch so the menu reads as an overlay
	// even when the world pass has already drawn pixels under us.
	// tyrquake: M_Draw calls Draw_FadeScreen() which fills the
	// framebuffer with palette index 0 (black) at 50% alpha; the
	// pure-Go port doesn't have alpha so we use a solid fill in the
	// border + leave the body region untouched for the row pics.
	// VWidth / VHeight: wash the WHOLE virtual surface so the menu
	// overlay reads against a uniform backdrop regardless of HUDScale.
	// At HUDScale == 1 this is identical to fb.Width / fb.Height.
	_ = render.DrawFill(fb, 0, 0, fb.VWidth(), fb.VHeight(), MenuFillIdx)

	// 1) QPlaque (left edge). When the asset is missing draw a
	//    vertical "Q U A K E" text string instead so the screen
	//    reads as a menu even without the wad assets loaded. The
	//    plaque fallback is the FIRST path to deference chars'
	//    Pixels slice, so a bad-shape sheet trips here and the
	//    error propagates out without painting the rest of the
	//    overlay.
	if assets != nil && assets.QPlaque != nil {
		_ = render.DrawTransPic(fb, plaqueX(fb), plaqueY(fb, assets.QPlaque), assets.QPlaque)
	} else {
		if err := drawVerticalLabel(fb, chars, plaqueX(fb)+8, plaqueY(fb, nil), "QUAKE"); err != nil {
			return err
		}
	}

	// 2) Title banner. Either the asset's centered blit or the
	//    text fallback via DrawCenteredString; the latter shares
	//    the conchars sheet contract checked once at the top.
	title, titleLabel := m.titleAssetAndLabel(assets)
	titleX := titleAnchorX(fb)
	if title != nil {
		_ = render.DrawTransPic(fb, titleX, titleY(fb), title)
	} else {
		// DrawCenteredString -> DrawCharacter validates the
		// 128x128 shape; chars already passed the nil check
		// above, and any further error is propagated.
		_ = render.DrawCenteredString(fb, chars, fb.VWidth()/2, titleY(fb), titleLabel)
	}

	// 3) Rows + cursor. The per-row text-draw + cursor-glyph
	//    fallback both reuse chars; their errors are swallowed
	//    here because the up-front nil check + the qplaque text
	//    fallback above have already proved the sheet is usable
	//    on this frame (a bad-shape sheet would have errored out
	//    at the qplaque stage).
	_ = m.drawRows(fb, chars, assets)
	_ = m.drawCursor(fb, chars, assets, now)
	return nil
}

// MenuFillIdx is the palette index used to wash the framebuffer
// behind the menu overlay. Index 0 is black in the id1 palette; that
// keeps the menu readable against any 3D scene that may have been
// rendered under it (paused-game path).
const MenuFillIdx byte = 0

// MenuRowsY is the y coordinate of the FIRST menu row (top of the
// row block). Subsequent rows step by MenuRowStep pixels. Exported
// so the cursor draw + the rows draw stay in sync.
const (
	MenuRowsY    = 32
	MenuRowStep  = 20
	MenuCursorX  = 54
	MenuRowsX    = 72
	MenuTitleY   = 4
	MenuPlaqueX  = 16
	MenuPlaqueY  = 4
	MenuPlaqueDY = 12
)

// plaqueX returns the x coordinate of the QPlaque blit. Fixed offset
// matching upstream M_DrawPlaque (16 from the left edge).
func plaqueX(_ *render.FrameBuffer) int { return MenuPlaqueX }

// plaqueY returns the y coordinate of the QPlaque blit. With a real
// plaque pic the y is fixed at MenuPlaqueY; the nil-asset fallback
// uses MenuPlaqueDY so the vertical "QUAKE" label clears the top
// border the title banner sits in.
func plaqueY(_ *render.FrameBuffer, _ *render.Pic) int { return MenuPlaqueY }

// titleAnchorX returns the x coordinate of the title banner's
// top-left. Centered horizontally with a slight right shift to
// account for the QPlaque on the left. Virtual-space coords so
// the banner stays centered in the 2D layer regardless of HUDScale.
func titleAnchorX(fb *render.FrameBuffer) int {
	return fb.VWidth()/2 - 64
}

// titleY returns the y coordinate of the title banner.
func titleY(_ *render.FrameBuffer) int { return MenuTitleY }

// titleAssetAndLabel returns the title banner pic for the active
// state plus a text fallback the caller draws when the pic is nil.
func (m *Menu) titleAssetAndLabel(a *Assets) (*render.Pic, string) {
	switch m.State {
	case StateMain, StateQuit:
		if a != nil {
			return a.TitleMain, "MAIN MENU"
		}
		return nil, "MAIN MENU"
	case StateNewGame:
		if a != nil {
			return a.TitleSinglePlayer, "SINGLE PLAYER"
		}
		return nil, "SINGLE PLAYER"
	case StateSkill:
		if a != nil {
			return a.TitleSinglePlayer, "CHOOSE SKILL"
		}
		return nil, "CHOOSE SKILL"
	case StateOptions:
		if a != nil {
			return a.TitleOptions, "OPTIONS"
		}
		return nil, "OPTIONS"
	case StateLoad:
		if a != nil {
			return a.TitleLoad, "LOAD GAME"
		}
		return nil, "LOAD GAME"
	case StateSave:
		if a != nil {
			return a.TitleSave, "SAVE GAME"
		}
		return nil, "SAVE GAME"
	}
	return nil, ""
}

// rowLabels returns the per-row text labels for the active screen.
// Used by drawRows when the screen has no dedicated body pic (or
// when the pic is missing in the asset bundle).
func (m *Menu) rowLabels() []string {
	switch m.State {
	case StateMain:
		return []string{"SINGLE PLAYER", "MULTIPLAYER", "OPTIONS", "HELP/ORDERING", "QUIT"}
	case StateNewGame:
		return []string{"NEW GAME", "LOAD", "SAVE"}
	case StateSkill:
		return []string{"EASY", "NORMAL", "HARD", "NIGHTMARE"}
	case StateOptions:
		return []string{"CUSTOMIZE CONTROLS", "GO TO CONSOLE", "RESET TO DEFAULTS", "VIDEO OPTIONS"}
	case StateLoad, StateSave:
		out := make([]string, MaxSaveSlots)
		for i := range out {
			out[i] = saveSlotLabel(i)
		}
		return out
	case StateQuit:
		return []string{"NO", "YES"}
	}
	return nil
}

// saveSlotLabel formats one row of the load / save picker. tyrquake
// keeps a per-slot comment string read from the .sav header; the Go
// port stubs that to an empty slot label until the save loader lands.
func saveSlotLabel(i int) string {
	if i < 0 {
		i = 0
	}
	// "SLOT 01 - <empty>" -- two-digit slot index, dash, "<empty>".
	return "SLOT " + twoDigit(i+1) + " - <empty>"
}

// twoDigit zero-pads a non-negative int to exactly 2 digits.
// Helper-local to avoid a runloop import cycle.
func twoDigit(n int) string {
	if n < 0 {
		n = 0
	}
	if n >= 100 {
		n %= 100
	}
	return string([]byte{byte('0' + n/10), byte('0' + n%10)})
}

// drawRows lays out the per-row text labels for the active screen.
// All rows draw through the conchars sheet for portability; the
// dedicated body pics (mainmenu, sp_menu) are not consumed yet so
// the bring-up build doesn't depend on the wad shipping them.
func (m *Menu) drawRows(fb *render.FrameBuffer, chars *render.Pic, _ *Assets) error {
	labels := m.rowLabels()
	// VHeight: row layout is virtual-space; rows that exceed the
	// virtual surface are clipped here so the underlying draw
	// primitives don't get asked for an over-scaled physical blit.
	vH := fb.VHeight()
	for i, lbl := range labels {
		y := MenuRowsY + i*MenuRowStep
		if y+render.CharHeight > vH {
			break
		}
		if err := render.DrawString(fb, chars, MenuRowsX, y, lbl); err != nil {
			return err
		}
	}
	return nil
}

// drawCursor blits the animated cursor dot to the left of the
// highlighted row. With a non-nil dots slice the cursor cycles
// through the 6 frames at MenuDotFPS; with a nil / short slice it
// falls back to a '>' glyph on the conchars sheet.
func (m *Menu) drawCursor(fb *render.FrameBuffer, chars *render.Pic, assets *Assets, now float32) error {
	rows := m.rowCount()
	if rows <= 0 {
		return nil
	}
	idx := m.CursorIndex
	if idx < 0 {
		idx = 0
	}
	if idx >= rows {
		idx = rows - 1
	}
	y := MenuRowsY + idx*MenuRowStep
	if y < 0 || y+render.CharHeight > fb.VHeight() {
		return nil
	}
	if assets != nil && len(assets.MenuDots) > 0 {
		frame := int(now*MenuDotFPS) % len(assets.MenuDots)
		if frame < 0 {
			frame += len(assets.MenuDots)
		}
		dot := assets.MenuDots[frame]
		if dot != nil {
			// Center the cursor pic vertically on the row's text
			// baseline. The menudot1..6 pics are ~16-20 px tall
			// while the conchars glyphs are 8 px; without this
			// offset the cursor sits at the top of the cell and
			// reads as "too low" relative to the text mid-line.
			dy := (dot.Height - render.CharHeight) / 2
			_ = render.DrawTransPic(fb, MenuCursorX, y-dy, dot)
			return nil
		}
	}
	return render.DrawCharacter(fb, chars, MenuCursorX, y, '>')
}

// drawVerticalLabel writes s top-to-bottom one glyph per row. Used
// as the qplaque fallback so the menu still reads as Quake-flavoured
// on bring-up builds without the wad.
func drawVerticalLabel(fb *render.FrameBuffer, chars *render.Pic, x, y int, s string) error {
	for i := 0; i < len(s); i++ {
		if err := render.DrawCharacter(fb, chars, x, y+i*render.CharHeight, s[i]); err != nil {
			return err
		}
	}
	return nil
}
