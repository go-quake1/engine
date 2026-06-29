// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package runner

import "testing"

// TestBuildMiptexChains_PrimaryThreeFrames covers the typical case:
// a torch animation with frames +0torch, +1torch, +2torch. Pic()
// should cycle through them at 5 Hz; non-animated siblings should
// be untouched.
func TestBuildMiptexChains_PrimaryThreeFrames(t *testing.T) {
	names := []string{
		"floor1",
		"+0torch",
		"+1torch",
		"+2torch",
		"wall1",
	}
	mc := BuildMiptexChains(names)
	if got := mc.NumChains(); got != 1 {
		t.Fatalf("NumChains = %d, want 1", got)
	}
	if got := mc.ChainLen(1); got != 3 {
		t.Fatalf("ChainLen(1) = %d, want 3", got)
	}
	if got := mc.ChainLen(0); got != 1 {
		t.Fatalf("ChainLen(0) = %d, want 1 (non-animated)", got)
	}
	// At t=0, frame = 0 -> slot 1.
	if got := mc.Pic(1, 0); got != 1 {
		t.Fatalf("Pic(1, 0) = %d, want 1", got)
	}
	if got := mc.Pic(2, 0); got != 1 {
		t.Fatalf("Pic(2, 0) = %d, want 1 (any chain member -> frame 0)", got)
	}
	// At t=0.2 (== 1 tic at 5 Hz), frame = 1 -> slot 2.
	if got := mc.Pic(1, 0.2); got != 2 {
		t.Fatalf("Pic(1, 0.2) = %d, want 2", got)
	}
	// At t=0.4, frame = 2 -> slot 3.
	if got := mc.Pic(1, 0.4); got != 3 {
		t.Fatalf("Pic(1, 0.4) = %d, want 3", got)
	}
	// At t=0.6, wraps to frame 0 again -> slot 1.
	if got := mc.Pic(1, 0.6); got != 1 {
		t.Fatalf("Pic(1, 0.6) = %d, want 1 (wrap)", got)
	}
	// Non-animated slot passes through.
	if got := mc.Pic(0, 12.34); got != 0 {
		t.Fatalf("Pic(0, t) = %d, want 0 (non-animated)", got)
	}
	if got := mc.Pic(4, 99); got != 4 {
		t.Fatalf("Pic(4, t) = %d, want 4 (non-animated wall)", got)
	}
}

// TestBuildMiptexChains_AlternateChain covers the +0/+a dual-chain
// case: only the primary cycle is used by Pic (vanilla brushmodels
// don't flip to alternate per-tic), but the alternate slots are
// still recognised so they don't break Pic dispatch on their own
// indices.
func TestBuildMiptexChains_AlternateChain(t *testing.T) {
	names := []string{
		"+0slip",
		"+1slip",
		"+aslip",
		"+bslip",
	}
	mc := BuildMiptexChains(names)
	if got := mc.NumChains(); got != 1 {
		t.Fatalf("NumChains = %d, want 1", got)
	}
	if got := mc.ChainLen(0); got != 2 {
		t.Fatalf("ChainLen(0) = %d, want 2 (primary)", got)
	}
	// Primary frame 0 == slot 0, frame 1 == slot 1.
	if got := mc.Pic(0, 0); got != 0 {
		t.Fatalf("Pic(0, 0) = %d, want 0", got)
	}
	if got := mc.Pic(0, 0.2); got != 1 {
		t.Fatalf("Pic(0, 0.2) = %d, want 1", got)
	}
	// Alternate slot 2 also resolves to primary (since we use the
	// shared chain record's primary for brushmodels). Frame 0 of
	// chain is slot 0.
	if got := mc.Pic(2, 0); got != 0 {
		t.Fatalf("Pic(2, 0) = %d, want 0 (alt slot resolves to primary frame 0)", got)
	}
}

// TestBuildMiptexChains_UppercaseAlternate covers the case where
// the alternate index uses uppercase ('A'..'J') instead of lowercase.
// tyrquake folds case before grouping; we must too.
func TestBuildMiptexChains_UppercaseAlternate(t *testing.T) {
	names := []string{"+0X", "+1X", "+AX", "+BX"}
	mc := BuildMiptexChains(names)
	if got := mc.NumChains(); got != 1 {
		t.Fatalf("NumChains = %d, want 1 (case-insensitive grouping)", got)
	}
	if got := mc.ChainLen(0); got != 2 {
		t.Fatalf("ChainLen(0) = %d, want 2", got)
	}
}

// TestBuildMiptexChains_HolesRepaired covers a malformed pak that
// declares +0X and +2X but not +1X. The chain must still be
// indexable (we fill the hole with the previous frame).
func TestBuildMiptexChains_HolesRepaired(t *testing.T) {
	names := []string{"+0miss", "+2miss"}
	mc := BuildMiptexChains(names)
	if got := mc.ChainLen(0); got != 3 {
		t.Fatalf("ChainLen(0) = %d, want 3 (hole padded)", got)
	}
	if got := mc.Pic(0, 0); got != 0 {
		t.Fatalf("Pic(0, 0) = %d, want 0", got)
	}
	if got := mc.Pic(0, 0.2); got != 0 {
		t.Fatalf("Pic(0, 0.2) = %d, want 0 (repaired hole == previous frame)", got)
	}
	if got := mc.Pic(0, 0.4); got != 1 {
		t.Fatalf("Pic(0, 0.4) = %d, want 1", got)
	}
}

// TestBuildMiptexChains_NoAnimNames covers the case where no name
// starts with '+': no chains are built, every slot is identity.
func TestBuildMiptexChains_NoAnimNames(t *testing.T) {
	names := []string{"floor", "wall", "sky", "*water"}
	mc := BuildMiptexChains(names)
	if got := mc.NumChains(); got != 0 {
		t.Fatalf("NumChains = %d, want 0", got)
	}
	for i := range names {
		if got := mc.Pic(i, 1.23); got != i {
			t.Fatalf("Pic(%d, 1.23) = %d, want %d (identity)", i, got, i)
		}
		if got := mc.ChainLen(i); got != 1 {
			t.Fatalf("ChainLen(%d) = %d, want 1", i, got)
		}
	}
}

// TestBuildMiptexChains_ShortNames covers degenerate names: lone '+'
// or '+x' with no suffix. The grouper should not crash and should
// not invent a chain it can't index.
func TestBuildMiptexChains_ShortNames(t *testing.T) {
	names := []string{"+", "+0", "+1", ""}
	mc := BuildMiptexChains(names)
	// "+" is len 1, ignored. "+0" / "+1" share suffix "" -> one chain.
	// "" is ignored.
	if got := mc.NumChains(); got != 1 {
		t.Fatalf("NumChains = %d, want 1", got)
	}
	if got := mc.ChainLen(1); got != 2 {
		t.Fatalf("ChainLen(1) = %d, want 2", got)
	}
}

// TestMiptexChains_NilReceiver guards against nil-deref when the
// renderer hasn't constructed chains yet (e.g. synth BSP path that
// skips the loader). All methods must return safe identities.
func TestMiptexChains_NilReceiver(t *testing.T) {
	var mc *MiptexChains
	if got := mc.Pic(3, 1.5); got != 3 {
		t.Fatalf("nil.Pic(3, 1.5) = %d, want 3", got)
	}
	if got := mc.ChainLen(0); got != 1 {
		t.Fatalf("nil.ChainLen(0) = %d, want 1", got)
	}
	if got := mc.NumChains(); got != 0 {
		t.Fatalf("nil.NumChains() = %d, want 0", got)
	}
}

// TestMiptexChains_PicBounds guards against out-of-range baseIdx.
func TestMiptexChains_PicBounds(t *testing.T) {
	mc := BuildMiptexChains([]string{"+0a", "+1a"})
	if got := mc.Pic(-1, 0); got != -1 {
		t.Fatalf("Pic(-1, 0) = %d, want -1", got)
	}
	if got := mc.Pic(99, 0); got != 99 {
		t.Fatalf("Pic(99, 0) = %d, want 99 (out of range -> identity)", got)
	}
	if got := mc.ChainLen(-1); got != 1 {
		t.Fatalf("ChainLen(-1) = %d, want 1", got)
	}
	if got := mc.ChainLen(99); got != 1 {
		t.Fatalf("ChainLen(99) = %d, want 1", got)
	}
}

// TestMiptexChains_NegativeTime guards against negative time inputs
// (e.g. server clock jitter on the first tic): the cycle index must
// still be in [0, len).
func TestMiptexChains_NegativeTime(t *testing.T) {
	mc := BuildMiptexChains([]string{"+0a", "+1a", "+2a"})
	// -0.5s * 5 = -2 (already in range, no wrap needed)
	got := mc.Pic(0, -0.5)
	if got < 0 || got > 2 {
		t.Fatalf("Pic(0, -0.5) = %d, want in [0,2]", got)
	}
	// -0.4s * 5 = -2 -> %3 = -2 in Go -> wrap to 1.
	got = mc.Pic(0, -0.4)
	if got != 1 {
		t.Fatalf("Pic(0, -0.4) = %d, want 1 (wrap)", got)
	}
}

// TestRepairHoles_AllNegative covers the degenerate path where the
// caller passes a chain we couldn't seed -- it must be a no-op,
// not a panic.
func TestRepairHoles_AllNegative(t *testing.T) {
	s := []int{-1, -1, -1}
	repairHoles(s)
	for i, v := range s {
		if v != -1 {
			t.Fatalf("repairHoles all-neg: s[%d] = %d, want -1", i, v)
		}
	}
	repairHoles(nil)
	repairHoles([]int{})
}

// TestBuildMiptexChains_AlternateOnly covers a chain with only
// alternate-frame names (+aX, +bX, no +0X / +1X). The primary
// chain is empty, so Pic falls back to identity; ChainLen is 1.
func TestBuildMiptexChains_AlternateOnly(t *testing.T) {
	names := []string{"+aalt", "+balt"}
	mc := BuildMiptexChains(names)
	if got := mc.NumChains(); got != 1 {
		t.Fatalf("NumChains = %d, want 1", got)
	}
	if got := mc.ChainLen(0); got != 1 {
		t.Fatalf("ChainLen(0) = %d, want 1 (no primary)", got)
	}
	if got := mc.Pic(0, 0.4); got != 0 {
		t.Fatalf("Pic(0, 0.4) = %d, want 0 (no primary -> identity)", got)
	}
	if got := mc.Pic(1, 0.4); got != 1 {
		t.Fatalf("Pic(1, 0.4) = %d, want 1 (no primary -> identity)", got)
	}
}

// TestBuildMiptexChains_UnknownSecondChar covers names like "+!foo"
// where the second character isn't a frame index. Should be ignored
// (no chain entry for that slot).
func TestBuildMiptexChains_UnknownSecondChar(t *testing.T) {
	names := []string{"+!foo", "+0bar", "+1bar"}
	mc := BuildMiptexChains(names)
	if got := mc.ChainLen(0); got != 1 {
		t.Fatalf("ChainLen(0) = %d, want 1 (+!foo ignored)", got)
	}
	if got := mc.ChainLen(1); got != 2 {
		t.Fatalf("ChainLen(1) = %d, want 2", got)
	}
}
