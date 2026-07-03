// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package menu

import (
	"errors"
	"testing"

	"github.com/go-quake1/engine/backend"
	"github.com/go-quake1/engine/render"
)

// newChars returns a 128x128 conchars pic with every glyph cell
// filled with the cell index value (deterministic; sufficient for
// the draw paths that only need a non-nil shape-valid sheet).
func newChars() *render.Pic {
	pix := make([]byte, 128*128)
	for i := range pix {
		pix[i] = byte(i)
	}
	return &render.Pic{Width: 128, Height: 128, Pixels: pix}
}

// newPic returns an opaque WxH pic with every pixel set to fill.
func newPic(w, h int, fill byte) *render.Pic {
	pix := make([]byte, w*h)
	for i := range pix {
		pix[i] = fill
	}
	return &render.Pic{Width: w, Height: h, Pixels: pix}
}

// newFB returns a 320x240 framebuffer (the standard tamago res).
func newFB(t *testing.T) *render.FrameBuffer {
	t.Helper()
	fb, err := render.NewFrameBuffer(320, 240)
	if err != nil {
		t.Fatalf("NewFrameBuffer: %v", err)
	}
	return fb
}

func TestStateStringCovers(t *testing.T) {
	want := map[State]string{
		StateNone:    "none",
		StateMain:    "main",
		StateNewGame: "single-player",
		StateSkill:   "skill",
		StateOptions: "options",
		StateLoad:    "load",
		StateSave:    "save",
		StateQuit:    "quit",
		State(99):    "invalid",
	}
	for s, w := range want {
		if got := s.String(); got != w {
			t.Errorf("State(%d).String() = %q, want %q", int(s), got, w)
		}
	}
}

func TestNewDefaults(t *testing.T) {
	m := New()
	if m.State != StateMain {
		t.Errorf("State = %v, want StateMain", m.State)
	}
	if m.SkillLevel != SkillNormal {
		t.Errorf("SkillLevel = %v, want SkillNormal", m.SkillLevel)
	}
	if m.CursorIndex != 0 {
		t.Errorf("CursorIndex = %d, want 0", m.CursorIndex)
	}
	if m.SaveSlot != 0 {
		t.Errorf("SaveSlot = %d, want 0", m.SaveSlot)
	}
	if !m.Active() {
		t.Errorf("Active() = false, want true (StateMain is active)")
	}
}

func TestActiveNilAndNone(t *testing.T) {
	var nilMenu *Menu
	if nilMenu.Active() {
		t.Errorf("nil Menu.Active() = true, want false")
	}
	m := &Menu{State: StateNone}
	if m.Active() {
		t.Errorf("StateNone Active() = true, want false")
	}
}

func TestRowCount(t *testing.T) {
	cases := []struct {
		s    State
		want int
	}{
		{StateMain, 5},
		{StateNewGame, 3},
		{StateSkill, NumSkills},
		{StateOptions, 4},
		{StateLoad, MaxSaveSlots},
		{StateSave, MaxSaveSlots},
		{StateQuit, 2},
		{StateNone, 0},
		{State(99), 0},
	}
	for _, tc := range cases {
		m := &Menu{State: tc.s}
		if got := m.rowCount(); got != tc.want {
			t.Errorf("rowCount(%v) = %d, want %d", tc.s, got, tc.want)
		}
	}
}

func TestHandleCursorWrap(t *testing.T) {
	m := &Menu{State: StateMain}
	// Down: 0 -> 1 -> 2 -> ... -> 4 -> 0 (wrap)
	for want := 1; want <= 5; want++ {
		advance := m.Handle(backend.KeyDown)
		if advance {
			t.Fatalf("Down %d advance=true unexpected", want)
		}
		expected := want % 5
		if m.CursorIndex != expected {
			t.Fatalf("after Down %d cursor=%d want %d", want, m.CursorIndex, expected)
		}
	}
	// Up: 0 -> 4 (wrap)
	m.CursorIndex = 0
	m.Handle(backend.KeyUp)
	if m.CursorIndex != 4 {
		t.Fatalf("Up from 0 cursor=%d want 4 (wrap)", m.CursorIndex)
	}
	// Up: 4 -> 3
	m.Handle(backend.KeyUp)
	if m.CursorIndex != 3 {
		t.Fatalf("Up from 4 cursor=%d want 3", m.CursorIndex)
	}
}

func TestHandleCursorZeroRowsNoop(t *testing.T) {
	m := &Menu{State: StateNone, CursorIndex: 7}
	m.moveCursor(+1)
	if m.CursorIndex != 0 {
		t.Errorf("zero-row screen cursor=%d want 0", m.CursorIndex)
	}
}

func TestHandleIgnoresUnknownKeys(t *testing.T) {
	m := &Menu{State: StateMain, CursorIndex: 2}
	beforeState := m.State
	beforeCursor := m.CursorIndex
	beforeSkill := m.SkillLevel
	beforeSlot := m.SaveSlot
	if adv := m.Handle(backend.KeyW); adv {
		t.Errorf("KeyW advance=true unexpected")
	}
	if m.State != beforeState || m.CursorIndex != beforeCursor ||
		m.SkillLevel != beforeSkill || m.SaveSlot != beforeSlot {
		t.Errorf("unknown key mutated state: state=%v cursor=%d skill=%v slot=%d",
			m.State, m.CursorIndex, m.SkillLevel, m.SaveSlot)
	}
}

func TestHandleEscapeFromMain(t *testing.T) {
	m := &Menu{State: StateMain, CursorIndex: 3}
	adv := m.Handle(backend.KeyEscape)
	if adv {
		t.Errorf("Esc from main advance=true; want false")
	}
	if m.State != StateNone {
		t.Errorf("State=%v want StateNone", m.State)
	}
	if m.CursorIndex != 0 {
		t.Errorf("CursorIndex=%d want 0 (reset)", m.CursorIndex)
	}
}

func TestHandleEscapeFromNoneOpens(t *testing.T) {
	m := &Menu{State: StateNone, CursorIndex: 5}
	adv := m.Handle(backend.KeyEscape)
	if adv {
		t.Errorf("Esc from none advance=true; want false")
	}
	if m.State != StateMain {
		t.Errorf("State=%v want StateMain", m.State)
	}
	if m.CursorIndex != 0 {
		t.Errorf("CursorIndex=%d want 0", m.CursorIndex)
	}
}

func TestHandleEscapeFromSubmenuPopsToMain(t *testing.T) {
	for _, s := range []State{StateNewGame, StateSkill, StateOptions, StateLoad, StateSave, StateQuit} {
		m := &Menu{State: s, CursorIndex: 2}
		m.Handle(backend.KeyEscape)
		if m.State != StateMain {
			t.Errorf("Esc from %v -> %v, want StateMain", s, m.State)
		}
		if m.CursorIndex != 0 {
			t.Errorf("Esc from %v cursor=%d want 0", s, m.CursorIndex)
		}
	}
}

func TestActivateMain(t *testing.T) {
	cases := []struct {
		row  int
		want State
	}{
		{0, StateNewGame},
		{1, StateMain}, // multiplayer placeholder
		{2, StateOptions},
		{3, StateMain}, // help placeholder
		{4, StateQuit},
	}
	for _, tc := range cases {
		m := &Menu{State: StateMain, CursorIndex: tc.row}
		adv := m.Handle(backend.KeyEnter)
		if adv {
			t.Errorf("Main row %d advance=true", tc.row)
		}
		if m.State != tc.want {
			t.Errorf("Main row %d -> %v want %v", tc.row, m.State, tc.want)
		}
	}
}

func TestActivateNewGame(t *testing.T) {
	cases := []struct {
		row        int
		want       State
		wantCursor int
	}{
		{0, StateSkill, int(SkillNormal)},
		{1, StateLoad, 0},
		{2, StateSave, 0},
	}
	for _, tc := range cases {
		m := &Menu{State: StateNewGame, CursorIndex: tc.row, SkillLevel: SkillNormal}
		adv := m.Handle(backend.KeyEnter)
		if adv {
			t.Errorf("NewGame row %d advance=true", tc.row)
		}
		if m.State != tc.want {
			t.Errorf("NewGame row %d -> %v want %v", tc.row, m.State, tc.want)
		}
		if m.CursorIndex != tc.wantCursor {
			t.Errorf("NewGame row %d cursor=%d want %d", tc.row, m.CursorIndex, tc.wantCursor)
		}
	}
}

func TestActivateSkillBindsAndAdvances(t *testing.T) {
	for rung := 0; rung < NumSkills; rung++ {
		m := &Menu{State: StateSkill, CursorIndex: rung}
		adv := m.Handle(backend.KeyEnter)
		if !adv {
			t.Errorf("Skill rung %d advance=false; want true", rung)
		}
		if m.State != StateNone {
			t.Errorf("Skill rung %d state=%v want StateNone", rung, m.State)
		}
		if int(m.SkillLevel) != rung {
			t.Errorf("Skill rung %d SkillLevel=%v want %d", rung, m.SkillLevel, rung)
		}
		if m.CursorIndex != 0 {
			t.Errorf("Skill rung %d cursor=%d want 0", rung, m.CursorIndex)
		}
	}
}

func TestActivateOptionsReturnsToMain(t *testing.T) {
	m := &Menu{State: StateOptions, CursorIndex: 2}
	adv := m.Handle(backend.KeyEnter)
	if adv {
		t.Errorf("Options Enter advance=true; want false")
	}
	if m.State != StateMain {
		t.Errorf("Options Enter -> %v want StateMain", m.State)
	}
}

func TestActivateLoadSaveBindsSlot(t *testing.T) {
	for _, s := range []State{StateLoad, StateSave} {
		m := &Menu{State: s, CursorIndex: 7}
		adv := m.Handle(backend.KeyEnter)
		if adv {
			t.Errorf("%v Enter advance=true; want false", s)
		}
		if m.State != StateNewGame {
			t.Errorf("%v Enter -> %v want StateNewGame", s, m.State)
		}
		if m.SaveSlot != 7 {
			t.Errorf("%v Enter SaveSlot=%d want 7", s, m.SaveSlot)
		}
	}
}

func TestActivateQuitNoStaysOnMain(t *testing.T) {
	m := &Menu{State: StateQuit, CursorIndex: 0}
	adv := m.Handle(backend.KeyEnter)
	if adv {
		t.Errorf("Quit row 0 advance=true; want false")
	}
	if m.State != StateMain {
		t.Errorf("Quit row 0 -> %v want StateMain", m.State)
	}
}

func TestActivateQuitYesAdvances(t *testing.T) {
	m := &Menu{State: StateQuit, CursorIndex: 1}
	adv := m.Handle(backend.KeyEnter)
	if !adv {
		t.Errorf("Quit row 1 advance=false; want true")
	}
	if m.State != StateNone {
		t.Errorf("Quit row 1 state=%v want StateNone", m.State)
	}
}

func TestActivateNoneIsNoop(t *testing.T) {
	m := &Menu{State: StateNone, CursorIndex: 3}
	if adv := m.activate(); adv {
		t.Errorf("activate StateNone advance=true; want false")
	}
	if m.State != StateNone {
		t.Errorf("activate StateNone mutated state to %v", m.State)
	}
}

func TestActivateMainInactiveRowsKeepState(t *testing.T) {
	for _, row := range []int{1, 3} {
		m := &Menu{State: StateMain, CursorIndex: row}
		m.Handle(backend.KeyEnter)
		if m.State != StateMain {
			t.Errorf("Main row %d -> %v want StateMain (placeholder)", row, m.State)
		}
	}
}

func TestActivateNewGameUnknownRowIsNoop(t *testing.T) {
	m := &Menu{State: StateNewGame, CursorIndex: 99}
	m.Handle(backend.KeyEnter)
	if m.State != StateNewGame {
		t.Errorf("NewGame row 99 -> %v want StateNewGame (no-op)", m.State)
	}
}

func TestActivateMainOutOfBoundsRowIsNoop(t *testing.T) {
	m := &Menu{State: StateMain, CursorIndex: 99}
	m.Handle(backend.KeyEnter)
	if m.State != StateMain {
		t.Errorf("Main row 99 -> %v want StateMain (no-op)", m.State)
	}
}

func TestOpen(t *testing.T) {
	m := &Menu{State: StateNone}
	m.Open()
	if m.State != StateMain {
		t.Errorf("Open from None -> %v want StateMain", m.State)
	}
	// Idempotent: Open with menu already up is a no-op.
	m.State = StateSkill
	m.CursorIndex = 2
	m.Open()
	if m.State != StateSkill || m.CursorIndex != 2 {
		t.Errorf("Open while up mutated state: %v %d", m.State, m.CursorIndex)
	}
}

func TestSaveSlotLabelBounds(t *testing.T) {
	if got := saveSlotLabel(0); got != "SLOT 01 - <empty>" {
		t.Errorf("saveSlotLabel(0) = %q", got)
	}
	if got := saveSlotLabel(11); got != "SLOT 12 - <empty>" {
		t.Errorf("saveSlotLabel(11) = %q", got)
	}
	if got := saveSlotLabel(-1); got != "SLOT 01 - <empty>" {
		t.Errorf("saveSlotLabel(-1) = %q", got)
	}
}

func TestTwoDigitClamp(t *testing.T) {
	if twoDigit(-5) != "00" {
		t.Errorf("twoDigit(-5) = %q want 00", twoDigit(-5))
	}
	if twoDigit(123) != "23" {
		t.Errorf("twoDigit(123) = %q want 23 (mod 100)", twoDigit(123))
	}
	if twoDigit(7) != "07" {
		t.Errorf("twoDigit(7) = %q want 07", twoDigit(7))
	}
}

func TestRowLabelsInvalidState(t *testing.T) {
	m := &Menu{State: State(99)}
	if got := m.rowLabels(); got != nil {
		t.Errorf("rowLabels(invalid) = %v want nil", got)
	}
}

func TestTitleAssetAndLabel(t *testing.T) {
	cases := []State{
		StateMain, StateQuit, StateNewGame, StateSkill,
		StateOptions, StateLoad, StateSave,
	}
	// With nil assets the pic is always nil and the label is non-empty.
	for _, s := range cases {
		m := &Menu{State: s}
		pic, lbl := m.titleAssetAndLabel(nil)
		if pic != nil {
			t.Errorf("state %v nil-assets pic non-nil", s)
		}
		if lbl == "" {
			t.Errorf("state %v label empty", s)
		}
	}
	// With a populated assets bundle the pic is the per-state pointer.
	pic := newPic(128, 16, 1)
	a := &Assets{
		TitleMain:         pic,
		TitleSinglePlayer: pic,
		TitleOptions:      pic,
		TitleLoad:         pic,
		TitleSave:         pic,
	}
	for _, s := range cases {
		m := &Menu{State: s}
		p, _ := m.titleAssetAndLabel(a)
		if p != pic {
			t.Errorf("state %v wanted asset pic", s)
		}
	}
	// StateNone returns nil, "".
	m := &Menu{State: StateNone}
	p, lbl := m.titleAssetAndLabel(a)
	if p != nil || lbl != "" {
		t.Errorf("StateNone titleAssetAndLabel = (%v, %q) want (nil, \"\")", p, lbl)
	}
}

func TestDrawStateNoneIsNoop(t *testing.T) {
	m := &Menu{State: StateNone}
	if err := m.Draw(nil, nil, nil, 0); err != nil {
		t.Errorf("StateNone Draw(nil,...) err=%v want nil", err)
	}
}

func TestDrawNilReceiverNoop(t *testing.T) {
	var m *Menu
	if err := m.Draw(nil, nil, nil, 0); err != nil {
		t.Errorf("nil receiver Draw err=%v want nil", err)
	}
}

func TestDrawNilFB(t *testing.T) {
	m := New()
	if err := m.Draw(nil, newChars(), nil, 0); !errors.Is(err, render.ErrDrawNilFB) {
		t.Errorf("nil fb err=%v want ErrDrawNilFB", err)
	}
}

func TestDrawNilChars(t *testing.T) {
	m := New()
	fb := newFB(t)
	if err := m.Draw(fb, nil, nil, 0); !errors.Is(err, render.ErrDrawCharsNilSrc) {
		t.Errorf("nil chars err=%v want ErrDrawCharsNilSrc", err)
	}
}

func TestDrawBadCharsShape(t *testing.T) {
	m := New()
	fb := newFB(t)
	bad := &render.Pic{Width: 64, Height: 64, Pixels: make([]byte, 64*64)}
	if err := m.Draw(fb, bad, nil, 0); !errors.Is(err, render.ErrDrawCharsShape) {
		t.Errorf("bad chars err=%v want ErrDrawCharsShape", err)
	}
}

func TestDrawAllStatesTextFallback(t *testing.T) {
	// Walk every state with nil assets so the text-fallback path
	// exercises drawVerticalLabel + DrawCenteredString + drawRows
	// + the cursor's text fallback.
	chars := newChars()
	for _, s := range []State{
		StateMain, StateNewGame, StateSkill, StateOptions,
		StateLoad, StateSave, StateQuit,
	} {
		m := &Menu{State: s, CursorIndex: 0}
		fb := newFB(t)
		if err := m.Draw(fb, chars, nil, 0); err != nil {
			t.Errorf("Draw(state=%v, nil-assets) err=%v", s, err)
		}
	}
}

func TestDrawWithAssets(t *testing.T) {
	chars := newChars()
	fb := newFB(t)
	pic := newPic(128, 16, 2)
	dots := []*render.Pic{
		newPic(16, 16, 3), newPic(16, 16, 4),
		newPic(16, 16, 5), newPic(16, 16, 6),
		newPic(16, 16, 7), newPic(16, 16, 8),
	}
	a := &Assets{
		QPlaque:           newPic(32, 64, 1),
		TitleMain:         pic,
		TitleSinglePlayer: pic,
		TitleOptions:      pic,
		TitleLoad:         pic,
		TitleSave:         pic,
		MainMenu:          pic,
		SinglePlayerMenu:  pic,
		MenuDots:          dots,
	}
	m := &Menu{State: StateMain, CursorIndex: 1}
	if err := m.Draw(fb, chars, a, 0.5); err != nil {
		t.Errorf("Draw with assets err=%v", err)
	}
	// Negative now must wrap; reuse the same call to exercise the
	// frame < 0 branch on the cursor animator.
	if err := m.Draw(fb, chars, a, -0.5); err != nil {
		t.Errorf("Draw negative-now err=%v", err)
	}
	// Skip-fallback: a nil entry inside MenuDots forces the
	// text-glyph branch even though len(dots) > 0.
	a.MenuDots = []*render.Pic{nil}
	if err := m.Draw(fb, chars, a, 0); err != nil {
		t.Errorf("Draw with nil-dot entry err=%v", err)
	}
}

func TestDrawCursorOutOfScreenIsNoop(t *testing.T) {
	// Force a screen with many rows so the highlighted row sits
	// below the framebuffer bottom -- exercises the y-clip branch
	// inside drawCursor without surfacing an error.
	fb, err := render.NewFrameBuffer(80, 40)
	if err != nil {
		t.Fatalf("NewFrameBuffer: %v", err)
	}
	chars := newChars()
	m := &Menu{State: StateLoad, CursorIndex: MaxSaveSlots - 1}
	if err := m.Draw(fb, chars, nil, 0); err != nil {
		t.Errorf("tight-fb Draw err=%v", err)
	}
}

func TestDrawCursorZeroRowsIsNoop(t *testing.T) {
	// StateNone yields rowCount==0; drive drawCursor directly to
	// exercise the early-return branch.
	fb := newFB(t)
	chars := newChars()
	m := &Menu{State: StateNone}
	if err := m.drawCursor(fb, chars, nil, 0); err != nil {
		t.Errorf("zero-rows drawCursor err=%v", err)
	}
}

func TestDrawCursorClampsOutOfRange(t *testing.T) {
	// CursorIndex set out of range -- drawCursor must clamp before
	// computing y rather than panic. We don't call Handle here so
	// the clamp branch is the only path that runs.
	fb := newFB(t)
	chars := newChars()
	m := &Menu{State: StateMain, CursorIndex: -3}
	if err := m.drawCursor(fb, chars, nil, 0); err != nil {
		t.Errorf("neg-cursor drawCursor err=%v", err)
	}
	m.CursorIndex = 999
	if err := m.drawCursor(fb, chars, nil, 0); err != nil {
		t.Errorf("over-cursor drawCursor err=%v", err)
	}
}

func TestDrawVerticalLabelBadChars(t *testing.T) {
	fb := newFB(t)
	bad := &render.Pic{Width: 64, Height: 64, Pixels: make([]byte, 64*64)}
	if err := drawVerticalLabel(fb, bad, 0, 0, "X"); !errors.Is(err, render.ErrDrawCharsShape) {
		t.Errorf("drawVerticalLabel bad chars err=%v", err)
	}
}

func TestPlaqueXAndYIgnoreArgs(t *testing.T) {
	// Defensive: the layout helpers are constants today; lock the
	// values down so the tamago capture stays positionally stable.
	if got := plaqueX(nil); got != MenuPlaqueX {
		t.Errorf("plaqueX = %d", got)
	}
	if got := plaqueY(nil, nil); got != MenuPlaqueY {
		t.Errorf("plaqueY = %d", got)
	}
	if got := titleY(nil); got != MenuTitleY {
		t.Errorf("titleY = %d", got)
	}
}

func TestTitleAnchorXCentres(t *testing.T) {
	fb := newFB(t)
	if got := titleAnchorX(fb); got != fb.Width/2-64 {
		t.Errorf("titleAnchorX = %d", got)
	}
}

func TestDrawAllStatesAndSubmenusVisitTitleBranches(t *testing.T) {
	// One pass that visits every State so the (title pic == nil)
	// branch fires for each label; the previous fan-out tested the
	// asset path only.
	chars := newChars()
	for _, s := range []State{
		StateMain, StateQuit, StateNewGame, StateSkill,
		StateOptions, StateLoad, StateSave,
	} {
		fb := newFB(t)
		m := &Menu{State: s, CursorIndex: 1}
		if err := m.Draw(fb, chars, &Assets{}, 0); err != nil {
			t.Errorf("Draw(state=%v, empty-assets) err=%v", s, err)
		}
	}
}

func TestDrawRowsClipsBelowFB(t *testing.T) {
	// A short framebuffer guarantees the per-row y check trips.
	fb, err := render.NewFrameBuffer(80, 40)
	if err != nil {
		t.Fatalf("NewFrameBuffer: %v", err)
	}
	chars := newChars()
	m := &Menu{State: StateMain, CursorIndex: 0}
	if err := m.drawRows(fb, chars, nil); err != nil {
		t.Errorf("drawRows short fb err=%v", err)
	}
}

func TestDrawRowsBadCharsPropagates(t *testing.T) {
	// Call drawRows directly with a bad-shape chars sheet so the
	// per-row DrawString error path is exercised. The top-level
	// Draw wrapper guards against this with its up-front nil/shape
	// check; this test hits the defence-in-depth path inside the
	// helper itself.
	fb := newFB(t)
	bad := &render.Pic{Width: 64, Height: 64, Pixels: make([]byte, 64*64)}
	m := &Menu{State: StateMain, CursorIndex: 0}
	if err := m.drawRows(fb, bad, nil); !errors.Is(err, render.ErrDrawCharsShape) {
		t.Errorf("drawRows bad-chars err=%v want ErrDrawCharsShape", err)
	}
}

// drawCenteredBadCharsExercise calls the per-state title-banner
// fallback path with a bad-shape chars sheet so the DrawCenteredString
// error return inside Draw is reached. The top-level Draw wrapper
// guards against nil/shape mismatch up front; this test invokes the
// inner branch via a state that has no title pic (StateNone-shaped
// substitute) PLUS forces the chars check off so the centered-string
// arm fires.
func TestDrawTitleBadCharsPropagates(t *testing.T) {
	// Construct a chars pic that PASSES the 128x128 shape check at
	// the top of Draw but FAILS once the per-character row math
	// inside DrawCharacter dereferences past Pixels. A pic with
	// the right shape declared in fields but a short Pixels slice
	// satisfies the up-front check (which only inspects W/H) then
	// blows up inside DrawCharacter.
	//
	// Simpler approach: drive drawRows / drawCursor directly with
	// a known-bad sheet; the title-banner branch error is reached
	// through the same DrawCenteredString -> DrawCharacter chain.
	fb := newFB(t)
	bad := &render.Pic{Width: 128, Height: 128, Pixels: make([]byte, 64)}
	m := New()
	err := m.Draw(fb, bad, nil, 0)
	if err == nil {
		t.Errorf("Draw with short-pixels chars expected error, got nil")
	}
}
