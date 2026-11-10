// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package gpuworld

import "context"

// Readback returns the target's width*height indices. A remote device has no
// ReadTexture: the frame is copied into a MAP_READ buffer (rows padded to
// 256 bytes) and read back through the stream, then de-padded ([ReadTexture]).
func (r *Renderer) Readback(ctx context.Context) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return ReadTexture(r.dev, r.target, r.width, r.height)
}
