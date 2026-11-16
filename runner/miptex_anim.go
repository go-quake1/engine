// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package runner

import "github.com/go-quake1/engine/worldview"

// The miptex animation chains (R_TextureAnimation) and the default light
// styles live in package worldview, where the GPU world follows the same
// animation; see worldview/texanim.go.

// MiptexChains maps each miptex slot index to the chain it belongs to.
type MiptexChains = worldview.MiptexChains

// BuildMiptexChains groups the '+'-named slots into their cycles.
func BuildMiptexChains(names []string) *MiptexChains { return worldview.BuildMiptexChains(names) }
