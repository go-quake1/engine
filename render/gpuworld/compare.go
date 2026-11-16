// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package gpuworld

// Tolerance is the largest share of pixels that may differ from the software
// reference and not be a one-pixel shift of it (cmd/quake-worldcheck's 2.5 %,
// against 0.0003 % measured on the Apple GPU). A GPU frame is not hash-equal to
// the CPU's: rasterization rules and sampling positions move a few pixels by one.
const Tolerance = 0.025

// EntityClear is the index of the entity layer ([Renderer.EnableAliasLayer])
// where no entity was drawn; the layer's shader writes at most EntityClear-1 (an
// entity texel of 255 is 254).
const EntityClear = 255

// AliasTolerance is the largest share of the pixels an entity changes (on either
// side) that may differ and not be a one-pixel shift: cmd/quake-worldcheck's
// aliasTolerance, 3 %.
const AliasTolerance = 0.03

// CompareMasked is [CompareStrided] over the pixels where mask is set (width x
// height, no padding): it returns how many there are, how many differ and how
// many of those a one-pixel shift of the reference does not explain.
func CompareMasked(ref []byte, refPitch int, got []byte, gotPitch, width, height int, mask []bool) (n, differ, unexplained int) {
	at := func(x, y int) byte { return ref[y*refPitch+x] }
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			if !mask[y*width+x] {
				continue
			}
			n++
			g := got[y*gotPitch+x]
			if g == at(x, y) {
				continue
			}
			differ++
			explained := false
			for dy := -1; dy <= 1 && !explained; dy++ {
				for dx := -1; dx <= 1 && !explained; dx++ {
					nx, ny := x+dx, y+dy
					explained = nx >= 0 && ny >= 0 && nx < width && ny < height && at(nx, ny) == g
				}
			}
			if !explained {
				unexplained++
			}
		}
	}
	return n, differ, unexplained
}

// CompositeLayer copies the pixels of layer (width*height, no padding) that an
// entity drew (not EntityClear) over dst (rows of pitch bytes).
func CompositeLayer(dst []byte, pitch int, layer []byte, width, height int) {
	for y := 0; y < height; y++ {
		row := dst[y*pitch : y*pitch+width]
		for x, v := range layer[y*width : (y+1)*width] {
			if v != EntityClear {
				row[x] = v
			}
		}
	}
}

// Compare counts the pixels of got (width x height, no padding) that differ
// from ref (rows of refPitch bytes), and, among them, those that are not the
// reference's index at one of the eight neighbours: a shift cannot account for
// them (a wrong texel, a wrong light). It is cmd/quake-worldcheck's measure.
func Compare(ref []byte, refPitch int, got []byte, width, height int) (differ, unexplained int) {
	return CompareStrided(ref, refPitch, got, width, width, height)
}

// CompareStrided is Compare for a got whose rows are gotPitch bytes apart.
func CompareStrided(ref []byte, refPitch int, got []byte, gotPitch, width, height int) (differ, unexplained int) {
	at := func(x, y int) byte { return ref[y*refPitch+x] }
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			g := got[y*gotPitch+x]
			if g == at(x, y) {
				continue
			}
			differ++
			explained := false
			for dy := -1; dy <= 1 && !explained; dy++ {
				for dx := -1; dx <= 1 && !explained; dx++ {
					nx, ny := x+dx, y+dy
					explained = nx >= 0 && ny >= 0 && nx < width && ny < height && at(nx, ny) == g
				}
			}
			if !explained {
				unexplained++
			}
		}
	}
	return differ, unexplained
}
