// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package render

import "math"

// ParticleType tags each particle with its decay + behavior rule.
// tyrquake: ptype_t enum in include/d_iface.h.
type ParticleType int

const (
	// ParticleStatic is a stationary, non-decaying particle (used for
	// the .pts pointfile debug viewer and the tracer trails).
	ParticleStatic ParticleType = 0
	// ParticleGrav is a gravity-affected particle (lava splash, blood).
	ParticleGrav ParticleType = 1
	// ParticleSlowGrav is the gravity-affected particle used by
	// R_RunParticleEffect for shot-impact dust. The upstream falls
	// through to the same gravity step as pt_grav in CL_RunParticles
	// (the "slow" qualifier is historical naming).
	ParticleSlowGrav ParticleType = 2
	// ParticleFire is a smoke/flame particle: floats up (positive
	// gravity), ramps through Ramp3 at *5 per second.
	ParticleFire ParticleType = 3
	// ParticleExplode is the burst-explosion particle: expanding
	// velocity, gravity-affected, ramps through Ramp1 at *10 per
	// second.
	ParticleExplode ParticleType = 4
	// ParticleExplode2 is the tarbaby/secondary explosion particle:
	// decaying velocity, gravity-affected, ramps through Ramp2 at *15
	// per second.
	ParticleExplode2 ParticleType = 5
	// ParticleBlob is the Voor-style sucking blob (R_BlobExplosion
	// even iterations): expanding velocity on all three axes,
	// gravity-affected.
	ParticleBlob ParticleType = 6
	// ParticleBlob2 is the Voor-style sucking blob's companion (odd
	// iterations of R_BlobExplosion): decaying velocity on X+Y only,
	// gravity-affected on Z.
	ParticleBlob2 ParticleType = 7
)

// Particle is one entry in the pool. Free slots are marked Die <= 0.
//
// tyrquake: particle_t in include/d_iface.h. The C struct also carries
// a `next` linked-list pointer (active vs free lists); the Go port
// uses a flat slot array instead so we drop the pointer.
type Particle struct {
	Origin   [3]float32
	Velocity [3]float32
	Ramp     float32 // for ramp-based types (Fire/Explode/Explode2), indexes the color ramp
	Color    byte    // palette index when no ramp is active
	Die      float32 // server time at which the particle expires (0 = free slot)
	Type     ParticleType
}

// MaxParticles is the pool capacity. tyrquake: MAX_PARTICLES = 2048
// in r_part.c; r_numparticles is cvar-configurable with a floor of
// 512 (ABSOLUTE_MIN_PARTICLES). The Go port uses the upstream default
// for a static, allocation-free pool.
const MaxParticles = 2048

// Pool is the entire particle bank. Indexed by slot; len(Particles)
// == MaxParticles. Layout is a flat array (NOT a free-list) -- Spawn
// walks for the first slot with Die <= 0.
//
// tyrquake: r_particles[] + the active_particles / free_particles
// linked-list. The Go port uses a slot scan for clarity; the cap is
// fixed so the scan is O(n) per spawn but n is small (2048).
type Pool struct {
	Particles [MaxParticles]Particle
	NumAlive  int // live count; maintained by Spawn + Run
}

// NewPool returns a fresh pool with every slot marked free (Die=0).
func NewPool() *Pool {
	return &Pool{}
}

// Spawn allocates one particle slot and writes the supplied init
// state into it. Returns the slot index + true on success, or 0 +
// false if the pool is full.
//
// Spawn does NOT set Die -- the caller computes the expiry time
// (typically now + lifetime) and writes it into the returned slot
// via p.Particles[idx].Die = now + lifetime.
//
// The `now` argument is unused by the linear-scan allocator (a slot
// is free iff Die <= 0 -- see Run, which marks expired slots Die=0).
// It is part of the API to mirror tyrquake's spawn helpers, which
// take cl.time implicitly via globals.
func (p *Pool) Spawn(init Particle, now float32) (int, bool) {
	_ = now
	for i := 0; i < MaxParticles; i++ {
		if p.Particles[i].Die <= 0 {
			p.Particles[i] = init
			p.NumAlive++
			return i, true
		}
	}
	return 0, false
}

// Ramp1, Ramp2, Ramp3 are the color ramps tyrquake uses for the
// fire / explosion / blood-trail particles. Each entry is a palette
// index walked over the particle's lifetime by the Ramp scalar.
//
// tyrquake source-of-truth (r_part.c lines 40-42):
//
//	int ramp1[8] = { 0x6f, 0x6d, 0x6b, 0x69, 0x67, 0x65, 0x63, 0x61 };
//	int ramp2[8] = { 0x6f, 0x6e, 0x6d, 0x6c, 0x6b, 0x6a, 0x68, 0x66 };
//	int ramp3[8] = { 0x6d, 0x6b, 6, 5, 4, 3 };
//
// Note: ramp3 is initialized with only six explicit entries; C
// zero-fills the trailing two. The Go port mirrors that exactly.
// The Run loop guards `ramp >= 6` for Fire (so the trailing zeros
// are never indexed under correct play), but we still preserve the
// upstream layout so a fuzz/regression test that drives ramp past
// 5 by hand sees the same bytes the C build does.
var (
	Ramp1 = [8]byte{0x6f, 0x6d, 0x6b, 0x69, 0x67, 0x65, 0x63, 0x61}
	Ramp2 = [8]byte{0x6f, 0x6e, 0x6d, 0x6c, 0x6b, 0x6a, 0x68, 0x66}
	Ramp3 = [8]byte{0x6d, 0x6b, 6, 5, 4, 3, 0, 0}
)

// Run advances every alive particle by `dt` seconds. Per-type
// integration follows CL_RunParticles in r_part.c verbatim:
//
//	grav  = dt * gravity * 0.05    (the 0.05 is upstream-baked)
//	dvel  = dt * 4
//	time1 = dt * 5    (Fire ramp rate)
//	time2 = dt * 10   (Explode ramp rate)
//	time3 = dt * 15   (Explode2 ramp rate)
//
//	Static    -> position update only (no force, no decay)
//	Grav      -> v[2] -= grav
//	SlowGrav  -> v[2] -= grav   (upstream falls through to same
//	                             step as Grav; the "slow" name is
//	                             historical, NOT a /2 multiplier)
//	Fire      -> ramp += time1; if ramp >= 6 -> die (Die=-1);
//	             else color = Ramp3[uint(ramp)];
//	             v[2] += grav   (note: FIRE FLOATS UP, sign is +)
//	Explode   -> ramp += time2; if ramp >= 8 -> die;
//	             else color = Ramp1[uint(ramp)];
//	             v[i] += v[i]*dvel (i=0..2); v[2] -= grav
//	Explode2  -> ramp += time3; if ramp >= 8 -> die;
//	             else color = Ramp2[uint(ramp)];
//	             v[i] -= v[i]*dt (i=0..2); v[2] -= grav
//	Blob      -> v[i] += v[i]*dvel (i=0..2); v[2] -= grav
//	Blob2     -> v[i] -= v[i]*dvel (i=0..1 ONLY); v[2] -= grav
//
// After motion, particles with Die <= now (expired) or marked
// Die=-1 by the ramp-overflow branch are freed: Die is reset to 0
// and NumAlive is decremented.
//
// tyrquake: CL_RunParticles in r_part.c (note: in upstream it lives
// in CL_*, not R_*, because the per-tick simulation runs on the
// client side, but the spawn + draw helpers live in r_part.c too).
//
// The `now` argument is the wall-clock time the expiry comparison
// uses (Particle.Die < now). dt is the frame delta in seconds.
// gravity is the world gravity scalar (typically sv_gravity = 800).
//
// Deviations from spec preamble (which was a first-pass approximation):
//   - SlowGrav uses `grav`, not `grav/2` (upstream falls through).
//   - Fire's v[2] step is `+= grav` (smoke rises), not `-= grav`.
//   - Explode / Explode2 / Blob / Blob2 also damp/expand velocity,
//     not just gravity.
//   - The gravity scalar is internally multiplied by dt*0.05 (the
//     upstream's per-frame "grav" local).
func (p *Pool) Run(dt, gravity, now float32) {
	grav := dt * gravity * 0.05
	dvel := dt * 4
	time1 := dt * 5
	time2 := dt * 10
	time3 := dt * 15
	for i := 0; i < MaxParticles; i++ {
		pp := &p.Particles[i]
		if pp.Die <= 0 {
			continue
		}
		// expire-before-step: tyrquake's loop frees on `die < cl.time`
		// before the per-type integration runs.
		if pp.Die < now {
			pp.Die = 0
			p.NumAlive--
			continue
		}
		// position update is unconditional (all types except Static
		// in the C source still apply v*dt; Static keeps v == 0 so
		// the add is a no-op for it).
		pp.Origin[0] += pp.Velocity[0] * dt
		pp.Origin[1] += pp.Velocity[1] * dt
		pp.Origin[2] += pp.Velocity[2] * dt

		switch pp.Type {
		case ParticleStatic:
			// no force update
		case ParticleFire:
			pp.Ramp += time1
			if pp.Ramp >= 6 {
				pp.Die = 0
				p.NumAlive--
				continue
			}
			pp.Color = Ramp3[uint(pp.Ramp)]
			pp.Velocity[2] += grav
		case ParticleExplode:
			pp.Ramp += time2
			if pp.Ramp >= 8 {
				pp.Die = 0
				p.NumAlive--
				continue
			}
			pp.Color = Ramp1[uint(pp.Ramp)]
			pp.Velocity[0] += pp.Velocity[0] * dvel
			pp.Velocity[1] += pp.Velocity[1] * dvel
			pp.Velocity[2] += pp.Velocity[2] * dvel
			pp.Velocity[2] -= grav
		case ParticleExplode2:
			pp.Ramp += time3
			if pp.Ramp >= 8 {
				pp.Die = 0
				p.NumAlive--
				continue
			}
			pp.Color = Ramp2[uint(pp.Ramp)]
			pp.Velocity[0] -= pp.Velocity[0] * dt
			pp.Velocity[1] -= pp.Velocity[1] * dt
			pp.Velocity[2] -= pp.Velocity[2] * dt
			pp.Velocity[2] -= grav
		case ParticleBlob:
			pp.Velocity[0] += pp.Velocity[0] * dvel
			pp.Velocity[1] += pp.Velocity[1] * dvel
			pp.Velocity[2] += pp.Velocity[2] * dvel
			pp.Velocity[2] -= grav
		case ParticleBlob2:
			pp.Velocity[0] -= pp.Velocity[0] * dvel
			pp.Velocity[1] -= pp.Velocity[1] * dvel
			pp.Velocity[2] -= grav
		case ParticleGrav, ParticleSlowGrav:
			pp.Velocity[2] -= grav
		}
	}
}

// ParticleExplosion spawns the burst-of-1024 rocket-explosion at
// origin. Each particle gets a random per-axis offset (rand%32 - 16),
// a random per-axis velocity (rand%512 - 256), Ramp = rand & 3,
// Die = now + 5, Color = Ramp1[0]. Even iterations (i&1 == 0) get
// type Explode2, odd iterations Explode -- the alternation creates
// the dual-decay-rate look (one band ramps at *10, the other at *15).
//
// tyrquake: R_ParticleExplosion in r_part.c (lines 252-282). The
// upstream loops `i = 0; i < 1024` and stops early if the free list
// is empty; this port mirrors that count and stops early on Spawn
// failure.
//
// Deviation from spec preamble: spec said "burst-of-30"; upstream is
// 1024. We follow upstream. The pool cap (2048) is large enough that
// one explosion takes about half the bank.
//
// rng is a deterministic byte source -- tests pass a counter or PRNG;
// production passes math/rand. Each call to rng returns one uniform
// byte (0..255); the helper converts to the upstream's `rand() % 32`
// / `rand() % 512` / `rand() & 3` slices using only the low bits.
func ParticleExplosion(pool *Pool, origin [3]float32, now float32, rng func() byte) {
	for i := 0; i < 1024; i++ {
		var pt ParticleType
		if i&1 != 0 {
			pt = ParticleExplode
		} else {
			pt = ParticleExplode2
		}
		var init Particle
		init.Type = pt
		init.Color = Ramp1[0]
		init.Ramp = float32(rng() & 3)
		init.Die = now + 5
		for j := 0; j < 3; j++ {
			init.Origin[j] = origin[j] + float32(int(rng()&31)-16)
			// rand()%512 - 256 needs 9 bits; compose from two bytes.
			lo := uint16(rng())
			hi := uint16(rng() & 1)
			init.Velocity[j] = float32(int((hi<<8)|lo) - 256)
		}
		if _, ok := pool.Spawn(init, now); !ok {
			return
		}
	}
}

// ParticleExplosion2 is R_ParticleExplosion2 (reference/common/r_part.c:292-316,
// TE_EXPLOSION2): 512 particles of type pt_blob that die 0.3 s later, particle i
// coloured colorStart + (i % colorLength), at origin plus a per-axis offset of
// rand()%32 - 16 with a per-axis velocity of rand()%512 - 256 (the draws of
// [ParticleExplosion]). It stops early when the pool is full. A colorLength of
// 0 or less divides by zero in C; here it makes no particle.
func ParticleExplosion2(pool *Pool, origin [3]float32, colorStart, colorLength int, now float32, rng func() byte) {
	if colorLength <= 0 {
		return
	}
	for i := 0; i < 512; i++ {
		var init Particle
		init.Type = ParticleBlob
		init.Die = now + 0.3
		init.Color = byte(colorStart + i%colorLength)
		for j := 0; j < 3; j++ {
			init.Origin[j] = origin[j] + float32(int(rng()&31)-16)
			lo := uint16(rng())
			hi := uint16(rng() & 1)
			init.Velocity[j] = float32(int((hi<<8)|lo) - 256)
		}
		if _, ok := pool.Spawn(init, now); !ok {
			return
		}
	}
}

// LavaSplash spawns the vertical fountain of particles at origin
// (the boss-death effect). Topology: 32*32*1 = 1024 particles
// arranged on an i in [-16,16), j in [-16,16), k = 0 grid.
//
// Per-particle init (verbatim from r_part.c R_LavaSplash):
//
//	die    = now + 2 + (rand & 31)*0.02
//	color  = 224 + (rand & 7)
//	type   = Grav
//	dir    = { j*8 + (rand&7), i*8 + (rand&7), 256 }
//	origin = org + { dir[0], dir[1], (rand&63) }
//	vel    = normalize(dir) * (50 + (rand&63))
//
// Particle count: 32*32*1 = 1024.
func LavaSplash(pool *Pool, origin [3]float32, now float32, rng func() byte) {
	for i := -16; i < 16; i++ {
		for j := -16; j < 16; j++ {
			var init Particle
			init.Die = now + 2 + float32(rng()&31)*0.02
			init.Color = 224 + (rng() & 7)
			init.Type = ParticleGrav

			dx := float32(j*8) + float32(rng()&7)
			dy := float32(i*8) + float32(rng()&7)
			dz := float32(256)

			init.Origin[0] = origin[0] + dx
			init.Origin[1] = origin[1] + dy
			init.Origin[2] = origin[2] + float32(rng()&63)

			// length > 0 unconditionally: dz = 256 is a constant
			// floor, so the normalize divisor is never zero. We
			// drop the guard the spec drafted -- it would be dead
			// defensive code per the "no unreachable branches" rule.
			inv := 1 / sqrt32(dx*dx+dy*dy+dz*dz)
			vel := 50 + float32(rng()&63)
			init.Velocity[0] = dx * inv * vel
			init.Velocity[1] = dy * inv * vel
			init.Velocity[2] = dz * inv * vel

			if _, ok := pool.Spawn(init, now); !ok {
				return
			}
		}
	}
}

// TeleportSplash spawns the cube of particles at origin (teleport
// in/out). Topology: 8*8*14 = 896 particles arranged on an
// i in [-16,16) step 4, j in [-16,16) step 4, k in [-24,32) step 4
// grid.
//
// Per-particle init (verbatim from r_part.c R_TeleportSplash):
//
//	die    = now + 0.2 + (rand & 7)*0.02
//	color  = 7 + (rand & 7)
//	type   = Grav
//	dir    = { j*8, i*8, k*8 }
//	origin = org + { i + (rand&3), j + (rand&3), k + (rand&3) }
//	vel    = normalize(dir) * (50 + (rand&63))
//
// Particle count: 8*8*14 = 896.
func TeleportSplash(pool *Pool, origin [3]float32, now float32, rng func() byte) {
	for i := -16; i < 16; i += 4 {
		for j := -16; j < 16; j += 4 {
			for k := -24; k < 32; k += 4 {
				var init Particle
				init.Die = now + 0.2 + float32(rng()&7)*0.02
				init.Color = 7 + (rng() & 7)
				init.Type = ParticleGrav

				dx := float32(j * 8)
				dy := float32(i * 8)
				dz := float32(k * 8)

				init.Origin[0] = origin[0] + float32(i) + float32(rng()&3)
				init.Origin[1] = origin[1] + float32(j) + float32(rng()&3)
				init.Origin[2] = origin[2] + float32(k) + float32(rng()&3)

				length := sqrt32(dx*dx + dy*dy + dz*dz)
				inv := float32(0)
				if length > 0 {
					inv = 1 / length
				}
				vel := 50 + float32(rng()&63)
				init.Velocity[0] = dx * inv * vel
				init.Velocity[1] = dy * inv * vel
				init.Velocity[2] = dz * inv * vel

				if _, ok := pool.Spawn(init, now); !ok {
					return
				}
			}
		}
	}
}

// sqrt32 is a float32 sqrt -- LavaSplash + TeleportSplash use it for
// VectorNormalize. tyrquake calls sqrtf via VectorNormalize.
func sqrt32(x float32) float32 {
	return float32(math.Sqrt(float64(x)))
}
