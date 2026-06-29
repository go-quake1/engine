// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package runner

// Miptex animation chains -- the Quake convention for animated
// textures (torches, lava ripples, slipgate). Names starting with
// '+' are grouped into 1- to 10-frame cycles keyed by the second
// character:
//
//	+0FOO, +1FOO, ... +9FOO   = primary chain
//	+aFOO, +bFOO, ... +jFOO   = alternate chain (selected by entity
//	                             frame state in vanilla; we never
//	                             flip to it here -- the brush model
//	                             always uses the primary)
//
// At render time, tyrquake's R_TextureAnimation picks frame
// `int(cl.time * 5) % chainLen`. We replicate that exactly so the
// 5-Hz cycle matches gameplay expectations.

// MiptexChains maps each miptex slot index to the chain it belongs
// to (nil if the slot has no '+' name or chain construction failed).
//
// Slot N's pic for time t is `chains.Pic(N, t)`. Non-animated slots
// (sky, plain, '*'-water) return baseIdx untouched.
type MiptexChains struct {
	// chain[i] is nil for non-animated slots, else points to the
	// shared chain record both i and all its siblings share.
	chain []*miptexChain
}

// miptexChain is one 1..10-frame primary cycle. `slots[k]` is the
// miptex slot index of frame k (0-based); altSlots is the parallel
// alternate cycle (may be empty).
type miptexChain struct {
	slots    []int // primary chain, len == count
	altSlots []int // alternate chain, may be len 0
}

// BuildMiptexChains walks the miptex name table and groups '+'-named
// slots into their cycles. The shared suffix (name[2:]) identifies a
// group; the second character is the frame index ('0'..'9' primary,
// 'a'..'j' alternate, case-insensitive per tyrquake).
//
// Slots that don't start with '+' get a nil chain entry; lone '+'
// slots (missing siblings) get a chain of length 1 = identity.
func BuildMiptexChains(names []string) *MiptexChains {
	mc := &MiptexChains{chain: make([]*miptexChain, len(names))}

	// Group slot indices by shared suffix.
	type slotInfo struct {
		idx       int
		frame     int
		alternate bool
	}
	groups := make(map[string][]slotInfo)
	for i, n := range names {
		if len(n) < 2 || n[0] != '+' {
			continue
		}
		c := n[1]
		var frame int
		var alt bool
		switch {
		case c >= '0' && c <= '9':
			frame = int(c - '0')
			alt = false
		case c >= 'a' && c <= 'j':
			frame = int(c - 'a')
			alt = true
		case c >= 'A' && c <= 'J':
			frame = int(c - 'A')
			alt = true
		default:
			continue
		}
		suffix := n[2:]
		groups[suffix] = append(groups[suffix], slotInfo{
			idx: i, frame: frame, alternate: alt,
		})
	}

	// For each suffix, build a shared chain record and point all
	// member slots at it.
	for _, members := range groups {
		var primaryMax, altMax int
		for _, m := range members {
			if m.alternate {
				if m.frame+1 > altMax {
					altMax = m.frame + 1
				}
			} else {
				if m.frame+1 > primaryMax {
					primaryMax = m.frame + 1
				}
			}
		}
		ch := &miptexChain{}
		if primaryMax > 0 {
			ch.slots = make([]int, primaryMax)
			for k := range ch.slots {
				ch.slots[k] = -1
			}
		}
		if altMax > 0 {
			ch.altSlots = make([]int, altMax)
			for k := range ch.altSlots {
				ch.altSlots[k] = -1
			}
		}
		for _, m := range members {
			if m.alternate {
				ch.altSlots[m.frame] = m.idx
			} else {
				ch.slots[m.frame] = m.idx
			}
		}
		// Drop holes by collapsing -1 entries onto the previous
		// real frame; missing intermediate frames are rare but a
		// malformed pak shouldn't crash the renderer.
		repairHoles(ch.slots)
		repairHoles(ch.altSlots)
		for _, m := range members {
			mc.chain[m.idx] = ch
		}
	}
	return mc
}

// repairHoles replaces any -1 entry with the closest non-negative
// neighbour (preferring the previous one) so the chain is always
// indexable. An all-negative chain is left as-is.
func repairHoles(s []int) {
	if len(s) == 0 {
		return
	}
	// First pass: find any positive seed.
	seed := -1
	for _, v := range s {
		if v >= 0 {
			seed = v
			break
		}
	}
	if seed < 0 {
		return
	}
	last := seed
	for i := range s {
		if s[i] < 0 {
			s[i] = last
		} else {
			last = s[i]
		}
	}
}

// Pic returns the miptex slot index to use for slot `baseIdx` at
// time `timeSec`. Non-animated slots return baseIdx unchanged; for
// animated slots the cycle frame is `int(timeSec*5) % chainLen`,
// matching tyrquake's R_TextureAnimation.
func (mc *MiptexChains) Pic(baseIdx int, timeSec float32) int {
	if mc == nil || baseIdx < 0 || baseIdx >= len(mc.chain) {
		return baseIdx
	}
	ch := mc.chain[baseIdx]
	if ch == nil || len(ch.slots) == 0 {
		return baseIdx
	}
	rel := int(timeSec*5) % len(ch.slots)
	if rel < 0 {
		rel += len(ch.slots)
	}
	return ch.slots[rel]
}

// ChainLen returns the cycle length for slot `baseIdx` (1 for
// non-animated slots, N for an N-frame chain). Useful for tests and
// debug logging.
func (mc *MiptexChains) ChainLen(baseIdx int) int {
	if mc == nil || baseIdx < 0 || baseIdx >= len(mc.chain) {
		return 1
	}
	ch := mc.chain[baseIdx]
	if ch == nil || len(ch.slots) == 0 {
		return 1
	}
	return len(ch.slots)
}

// NumChains counts distinct primary chains (each suffix-group
// counted once, regardless of how many member slots it has). Used
// for boot-time logging so we can confirm animations were detected.
func (mc *MiptexChains) NumChains() int {
	if mc == nil {
		return 0
	}
	seen := make(map[*miptexChain]struct{})
	for _, ch := range mc.chain {
		if ch == nil {
			continue
		}
		seen[ch] = struct{}{}
	}
	return len(seen)
}
