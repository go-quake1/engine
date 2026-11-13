// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package gpuworld

// Tolerance is the largest share of pixels that may differ from the software
// reference and not be a one-pixel shift of it (cmd/quake-worldcheck's 2.5 %,
// against 0.0003 % measured on the Apple GPU). A GPU frame is not hash-equal to
// the CPU's: rasterization rules and sampling positions move a few pixels by one.
const Tolerance = 0.025

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
