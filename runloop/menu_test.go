// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package runloop

import (
	"testing"

	"github.com/go-quake1/engine/backend"
	"github.com/go-quake1/engine/menu"
	"github.com/go-quake1/engine/render"
)

// recorderWithKeys is a thin wrapper around backend.Recorder that
// returns a caller-supplied InputSnapshot on the first PollInput
// call + empty snapshots thereafter. Lets the menu-integration
// tests inject one frame's worth of KeysDown without touching the
// recorder's audio / video plumbing.
type recorderWithKeys struct {
	*backend.Recorder
	snap     backend.InputSnapshot
	consumed bool
}

func (r *recorderWithKeys) PollInput() (backend.InputSnapshot, error) {
	if r.consumed {
		return backend.InputSnapshot{}, nil
	}
	r.consumed = true
	return r.snap, nil
}

func TestRunFrame_MenuActiveSkipsHostFrame(t *testing.T) {
	rec := backend.NewRecorder(0, 0)
	r, _ := newRunner(t, rec)
	r.Menu = menu.New() // boots into StateMain (active)

	host := r.Host.(*fakeHost)
	if err := r.RunFrame(0.05, 1); err != nil {
		t.Fatalf("RunFrame: %v", err)
	}
	if host.calls != 0 {
		t.Errorf("menu active: host.Frame calls = %d, want 0", host.calls)
	}
	if len(rec.Frames) != 1 {
		t.Errorf("PresentFrame calls = %d, want 1 (menu overlay still presents)", len(rec.Frames))
	}
}

func TestRunFrame_MenuInactiveRunsHost(t *testing.T) {
	rec := backend.NewRecorder(0, 0)
	r, _ := newRunner(t, rec)
	r.Menu = &menu.Menu{State: menu.StateNone}

	host := r.Host.(*fakeHost)
	if err := r.RunFrame(0.05, 1); err != nil {
		t.Fatalf("RunFrame: %v", err)
	}
	if host.calls != 1 {
		t.Errorf("menu inactive: host.Frame calls = %d, want 1", host.calls)
	}
}

func TestRunFrame_NoMenuRunsHost(t *testing.T) {
	rec := backend.NewRecorder(0, 0)
	r, _ := newRunner(t, rec)
	r.Menu = nil

	host := r.Host.(*fakeHost)
	if err := r.RunFrame(0.05, 1); err != nil {
		t.Fatalf("RunFrame: %v", err)
	}
	if host.calls != 1 {
		t.Errorf("nil menu: host.Frame calls = %d, want 1", host.calls)
	}
}

func TestRunFrame_MenuEscapeOpensMenu(t *testing.T) {
	rec := backend.NewRecorder(0, 0)
	r, _ := newRunner(t, rec)
	rwk := &recorderWithKeys{
		Recorder: rec,
		snap: backend.InputSnapshot{
			KeysDown: []backend.KeyCode{backend.KeyEscape},
		},
	}
	r.Backend = rwk
	r.Menu = &menu.Menu{State: menu.StateNone}

	host := r.Host.(*fakeHost)
	if err := r.RunFrame(0.05, 1); err != nil {
		t.Fatalf("RunFrame: %v", err)
	}
	if r.Menu.State != menu.StateMain {
		t.Errorf("after Esc, menu state = %v, want StateMain", r.Menu.State)
	}
	if host.calls != 0 {
		t.Errorf("Esc-opened menu: host.Frame calls = %d, want 0 (menu froze tic)", host.calls)
	}
}

func TestRunFrame_MenuHandlesKeyDownEvents(t *testing.T) {
	rec := backend.NewRecorder(0, 0)
	r, _ := newRunner(t, rec)
	rwk := &recorderWithKeys{
		Recorder: rec,
		snap: backend.InputSnapshot{
			KeysDown: []backend.KeyCode{backend.KeyDown},
		},
	}
	r.Backend = rwk
	r.Menu = menu.New()

	if err := r.RunFrame(0.05, 1); err != nil {
		t.Fatalf("RunFrame: %v", err)
	}
	if r.Menu.CursorIndex != 1 {
		t.Errorf("after KeyDown, cursor = %d, want 1", r.Menu.CursorIndex)
	}
}

func TestRunFrame_MenuConsumesInputSoButtonsStaySticky(t *testing.T) {
	// When the menu is up, holding W should NOT register as a
	// forward press on r.Buttons. Verifies the "skip
	// UpdateButtonsFromSnapshot when menu consumed" branch.
	rec := backend.NewRecorder(0, 0)
	r, _ := newRunner(t, rec)
	rwk := &recorderWithKeys{
		Recorder: rec,
		snap: backend.InputSnapshot{
			KeysDown: []backend.KeyCode{backend.KeyW},
		},
	}
	r.Backend = rwk
	r.Menu = menu.New()

	if err := r.RunFrame(0.05, 1); err != nil {
		t.Fatalf("RunFrame: %v", err)
	}
	if r.Buttons.Forward.Pressed != 0 {
		t.Errorf("menu consumed W but Forward.Pressed=%d, want 0", r.Buttons.Forward.Pressed)
	}
}

func TestRunFrame_MenuSkillConfirmUnfreezesNextTic(t *testing.T) {
	// Setting Skill via Enter advances the menu out of active
	// state. The CURRENT tic skips host.Frame (menu was active
	// at tic start); the NEXT tic runs host.Frame because the
	// menu is now inactive.
	rec := backend.NewRecorder(0, 0)
	r, _ := newRunner(t, rec)
	rwk := &recorderWithKeys{
		Recorder: rec,
		snap: backend.InputSnapshot{
			KeysDown: []backend.KeyCode{backend.KeyEnter},
		},
	}
	r.Backend = rwk
	r.Menu = &menu.Menu{State: menu.StateSkill, CursorIndex: 2}

	host := r.Host.(*fakeHost)
	if err := r.RunFrame(0.05, 1); err != nil {
		t.Fatalf("tic 1 RunFrame: %v", err)
	}
	if r.Menu.State != menu.StateNone {
		t.Errorf("after Skill Enter, state = %v, want StateNone", r.Menu.State)
	}
	if r.Menu.SkillLevel != menu.SkillLevel(2) {
		t.Errorf("after Skill Enter, SkillLevel = %v, want 2", r.Menu.SkillLevel)
	}
	// Skill Enter dismisses the menu IN-tic so the runloop runs
	// the host the same frame the player picked: dispatchMenuInput
	// returned false because Menu.Active() flipped to false during
	// Handle. host.Frame fires for tic 1 + 2.
	if host.calls != 1 {
		t.Errorf("tic 1 host.Frame calls = %d, want 1 (menu dismissed in-tic)", host.calls)
	}
	// Tic 2: no inputs, menu still StateNone => host runs again.
	if err := r.RunFrame(0.05, 2); err != nil {
		t.Fatalf("tic 2 RunFrame: %v", err)
	}
	if host.calls != 2 {
		t.Errorf("tic 2 host.Frame calls = %d, want 2 (menu inactive)", host.calls)
	}
}

func TestDispatchMenuInput_NilMenuFalse(t *testing.T) {
	r := &Runner{}
	if got := r.dispatchMenuInput(backend.InputSnapshot{}); got {
		t.Errorf("nil menu dispatchMenuInput = true, want false")
	}
}

func TestDispatchMenuInput_InactiveNoEscFalse(t *testing.T) {
	r := &Runner{Menu: &menu.Menu{State: menu.StateNone}}
	snap := backend.InputSnapshot{KeysDown: []backend.KeyCode{backend.KeyW}}
	if got := r.dispatchMenuInput(snap); got {
		t.Errorf("inactive menu + W key dispatchMenuInput = true, want false")
	}
}

func TestDispatchMenuInput_InactiveEscOpensReturnsTrue(t *testing.T) {
	m := &menu.Menu{State: menu.StateNone}
	r := &Runner{Menu: m}
	snap := backend.InputSnapshot{KeysDown: []backend.KeyCode{backend.KeyEscape}}
	if got := r.dispatchMenuInput(snap); !got {
		t.Errorf("inactive menu + Esc dispatchMenuInput = false, want true")
	}
	if m.State != menu.StateMain {
		t.Errorf("after Esc dispatch, state = %v, want StateMain", m.State)
	}
}

func TestDispatchMenuInput_ActiveRoutesEveryKeyDown(t *testing.T) {
	m := menu.New()
	r := &Runner{Menu: m}
	// Down + Down = cursor 2.
	snap := backend.InputSnapshot{KeysDown: []backend.KeyCode{backend.KeyDown, backend.KeyDown}}
	if got := r.dispatchMenuInput(snap); !got {
		t.Errorf("active menu dispatchMenuInput = false, want true")
	}
	if m.CursorIndex != 2 {
		t.Errorf("after 2x Down, cursor = %d, want 2", m.CursorIndex)
	}
}

func TestDispatchMenuInput_ActiveSkillEnterReturnsFalse(t *testing.T) {
	// When the menu transitions to StateNone (e.g. Skill confirm)
	// dispatch returns false on the SAME call so the runloop runs
	// the rest of the tic.
	m := &menu.Menu{State: menu.StateSkill, CursorIndex: 1}
	r := &Runner{Menu: m}
	snap := backend.InputSnapshot{KeysDown: []backend.KeyCode{backend.KeyEnter}}
	if got := r.dispatchMenuInput(snap); got {
		t.Errorf("active menu post-Enter dispatchMenuInput = true, want false")
	}
	if m.State != menu.StateNone {
		t.Errorf("after Skill Enter, state = %v, want StateNone", m.State)
	}
}

// TestRunFrame_MenuDrawErrorPropagates exercises the menu-overlay
// error branch (runloop.go step 5b): when the menu is active and
// Menu.Draw fails, RunFrame returns that error. A conchars Pic that
// declares the required 128x128 shape but carries a short Pixels
// slice passes Draw's up-front shape check, then blows up inside
// DrawCharacter once the title banner is painted.
func TestRunFrame_MenuDrawErrorPropagates(t *testing.T) {
	rec := backend.NewRecorder(0, 0)
	r, _ := newRunner(t, rec)
	r.Menu = menu.New() // active (StateMain)
	r.Chars = &render.Pic{Width: 128, Height: 128, Pixels: make([]byte, 64)}

	if err := r.RunFrame(0.05, 1); err == nil {
		t.Fatalf("RunFrame with broken menu chars: expected error, got nil")
	}
}
