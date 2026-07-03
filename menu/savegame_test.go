// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package menu

import (
	"errors"
	"testing"

	"github.com/go-quake1/engine/backend"
)

func TestActivateSave_HookFiresAndPopsToParent(t *testing.T) {
	var got int
	m := &Menu{
		State:       StateSave,
		CursorIndex: 4,
		OnSave: func(slot int) error {
			got = slot
			return nil
		},
	}
	adv := m.Handle(backend.KeyEnter)
	if adv {
		t.Errorf("Save Enter advance=true; want false")
	}
	if got != 4 {
		t.Errorf("hook saw slot=%d want 4", got)
	}
	if m.State != StateNewGame {
		t.Errorf("post-save state=%v want StateNewGame", m.State)
	}
	if m.LastError != nil {
		t.Errorf("LastError=%v want nil", m.LastError)
	}
}

func TestActivateLoad_HookFiresAndDismisses(t *testing.T) {
	var got int
	m := &Menu{
		State:       StateLoad,
		CursorIndex: 6,
		OnLoad: func(slot int) error {
			got = slot
			return nil
		},
	}
	adv := m.Handle(backend.KeyEnter)
	if !adv {
		t.Errorf("Load Enter advance=false; want true (dismiss menu post-load)")
	}
	if got != 6 {
		t.Errorf("hook saw slot=%d want 6", got)
	}
	if m.State != StateNone {
		t.Errorf("post-load state=%v want StateNone", m.State)
	}
	if m.LastError != nil {
		t.Errorf("LastError=%v want nil", m.LastError)
	}
}

func TestActivateLoad_NoHookPopsToParent(t *testing.T) {
	m := &Menu{State: StateLoad, CursorIndex: 0}
	adv := m.Handle(backend.KeyEnter)
	if adv {
		t.Errorf("Load Enter with no hook advance=true; want false")
	}
	if m.State != StateNewGame {
		t.Errorf("post-no-hook state=%v want StateNewGame", m.State)
	}
}

func TestActivateLoad_HookErrorPopsToParent(t *testing.T) {
	sentinel := errors.New("boom")
	m := &Menu{
		State:       StateLoad,
		CursorIndex: 2,
		OnLoad:      func(slot int) error { return sentinel },
	}
	adv := m.Handle(backend.KeyEnter)
	if adv {
		t.Errorf("Load Enter with error advance=true; want false")
	}
	if m.State != StateNewGame {
		t.Errorf("post-error state=%v want StateNewGame", m.State)
	}
	if !errors.Is(m.LastError, sentinel) {
		t.Errorf("LastError: got %v want %v", m.LastError, sentinel)
	}
}

func TestActivateSave_HookErrorStashedOnLastError(t *testing.T) {
	sentinel := errors.New("disk full")
	m := &Menu{
		State:       StateSave,
		CursorIndex: 0,
		OnSave:      func(slot int) error { return sentinel },
	}
	m.Handle(backend.KeyEnter)
	if !errors.Is(m.LastError, sentinel) {
		t.Errorf("LastError: got %v want %v", m.LastError, sentinel)
	}
	if m.State != StateNewGame {
		t.Errorf("post-save-error state=%v want StateNewGame", m.State)
	}
}
