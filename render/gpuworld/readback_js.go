// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package gpuworld

import (
	"context"

	"github.com/go-gfx/webgpu"
)

// Readback returns the target's width*height indices (for tests).
func (r *Renderer) Readback(ctx context.Context) ([]byte, error) {
	return r.dev.ReadTexture(ctx, webgpu.TexelCopyTextureInfo{Texture: r.target}, r.width, r.height, 1)
}
