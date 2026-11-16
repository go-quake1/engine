// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package render

import "testing"

// TestParticleExplosion2 pins R_ParticleExplosion2 (reference/common/r_part.c:292-316)
// on values worked from it, with every random draw 20: the offset rand()%32 - 16
// is 20 - 16 = 4, the velocity rand()%512 - 256 is 20 - 256 = -236; 512 particles,
// die = cl.time + 0.3, pt_blob, colour colorStart + (colorMod % colorLength): with
// 73 and 24, particle 0 is 73, 23 is 96, 24 is 73 again and 511 is 73 + 511 % 24 = 80.
func TestParticleExplosion2(t *testing.T) {
	pool := NewPool()
	ParticleExplosion2(pool, [3]float32{100, 200, 300}, 73, 24, 10, func() byte { return 20 })
	if pool.NumAlive != 512 {
		t.Fatalf("%d particles, want 512", pool.NumAlive)
	}
	for _, p := range pool.Particles {
		if p.Die == 0 {
			continue
		}
		if p.Type != ParticleBlob || p.Die != 10.3 || p.Origin != [3]float32{104, 204, 304} || p.Velocity != [3]float32{-236, -236, -236} {
			t.Fatalf("particle %+v", p)
		}
	}
	// the colours, in spawn order (the pool fills slots from 0)
	for i, want := range map[int]byte{0: 73, 23: 96, 24: 73, 511: 80} {
		if got := pool.Particles[i].Color; got != want {
			t.Errorf("particle %d colour %d, want %d", i, got, want)
		}
	}
	// a draw of 255 in every position: offset 31 - 16 = 15, velocity (1<<8 | 255) - 256 = 255
	p2 := NewPool()
	ParticleExplosion2(p2, [3]float32{}, 0, 1, 0, func() byte { return 255 })
	if q := p2.Particles[0]; q.Origin != [3]float32{15, 15, 15} || q.Velocity != [3]float32{255, 255, 255} || q.Color != 0 {
		t.Errorf("%+v", q)
	}
	// no colours: nothing (C divides by zero); a full pool: stops early
	p3 := NewPool()
	ParticleExplosion2(p3, [3]float32{}, 5, 0, 0, func() byte { return 0 })
	if p3.NumAlive != 0 {
		t.Errorf("colorLength 0 made %d particles", p3.NumAlive)
	}
	for i := 0; i < 5; i++ {
		ParticleExplosion2(p3, [3]float32{}, 5, 3, 0, func() byte { return 0 })
	}
	if p3.NumAlive != MaxParticles {
		t.Errorf("a full pool holds %d, want %d", p3.NumAlive, MaxParticles)
	}
}
