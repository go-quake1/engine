// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package progs

import (
	"errors"
	"math"

	"github.com/go-quake1/engine/mathlib"
)

// MathBuiltinIDs gathers the official QuakeC builtin index numbers
// from tyrquake's pr_cmds.c builtin table (the negative-of-
// first_statement that dispatches to each). These are the
// stable IDs progs.dat references via OP_CALLn -- never renumber
// them or every shipping mod breaks.
const (
	BuiltinMakeVectors   = 1 // makevectors(vector) -- writes v_forward/right/up
	BuiltinSetOrigin     = 2
	BuiltinSetModel      = 3
	BuiltinSetSize       = 4
	BuiltinBreak         = 6
	BuiltinRandom        = 7 // random() -> float in [0, 1)
	BuiltinSound         = 8
	BuiltinNormalize     = 9 // vector normalize(vector)
	BuiltinError         = 10
	BuiltinObjError      = 11
	BuiltinVLen          = 12 // float vlen(vector)
	BuiltinVecToYaw      = 13 // float vectoyaw(vector)
	BuiltinSpawn         = 14
	BuiltinRemove        = 15
	BuiltinTraceLine     = 16
	BuiltinCheckClient   = 17
	BuiltinFind          = 18
	BuiltinPrecacheSound = 19
	BuiltinPrecacheModel = 20
	BuiltinStuffCmd      = 21
	BuiltinFindRadius    = 22
	BuiltinBPrint        = 23
	BuiltinSPrint        = 24
	BuiltinDPrint        = 25
	BuiltinFToS          = 26
	BuiltinVToS          = 27
	BuiltinCoreDump      = 28
	BuiltinTraceOn       = 29
	BuiltinTraceOff      = 30
	BuiltinEPrint        = 31
	BuiltinWalkMove      = 32
	BuiltinDropToFloor   = 34
	BuiltinLightStyle    = 35
	BuiltinRInt          = 36 // float rint(float)
	BuiltinFloor         = 37 // float floor(float)
	BuiltinCeil          = 38 // float ceil(float)
	BuiltinCheckBottom   = 40
	BuiltinPointContents = 41
	BuiltinFAbs          = 43 // float fabs(float)
	BuiltinAim           = 44
	BuiltinCVar          = 45
	BuiltinLocalCmd      = 46
	BuiltinNextEnt       = 47
	BuiltinParticle      = 48
	BuiltinChangeYaw     = 49
	BuiltinVecToAngles   = 51 // vector vectoangles(vector)
	BuiltinMoveToGoal    = 67 // void movetogoal(float dist) -- monster chase step
)

// ErrRandomNotSeeded is returned by the random() builtin when no
// RNG source has been wired via SetRandomSource. The upstream calls
// the C stdlib rand(); the Go port requires the embedder to choose
// (a deterministic PRNG for demo replay, a real entropy source for
// live play, etc.).
var ErrRandomNotSeeded = errors.New("progs: random() builtin called but no SetRandomSource wired")

// SetRandomSource installs the callback the BuiltinRandom function
// pulls from. The callback must return a float32 in the half-open
// interval [0, 1). For demo replay parity, wire a deterministic
// PRNG seeded by the demo header; for live play, wire any
// 0..0x7fff-style RNG that matches tyrquake's `(rand() & 0x7fff) /
// (float)0x7fff` shape so byte-equal demos work both ways.
func (vm *VM) SetRandomSource(fn func() float32) { vm.randomSource = fn }

// RegisterMathBuiltins wires the 10 pure-math QuakeC builtins
// (makevectors / normalize / vlen / vectoyaw / vectoangles / random
// / fabs / rint / floor / ceil) into the VM at their canonical
// index numbers. Callers that want to override individual builtins
// do so AFTER this call.
//
// makevectors is included here -- not in the per-embedder
// side-effect builtins -- because its output is pure math (the
// AngleVectors basis from a pitch/yaw/roll input) parked in three
// QC globals, with no I/O or world state involved. Without a real
// implementation, W_FireShotgun's "makevectors(self.v_angle);
// traceline(src, src + v_forward * 2048, ...)" chain reads v_forward
// = (0,0,0) every tic and every shot trace collapses to a degenerate
// zero-length ray that never clips anything -- so the player can
// hold +attack forever without ever damaging a monster.
func (vm *VM) RegisterMathBuiltins() {
	vm.RegisterBuiltin(BuiltinMakeVectors, BuiltinFnMakeVectors)
	vm.RegisterBuiltin(BuiltinRandom, BuiltinFnRandom)
	vm.RegisterBuiltin(BuiltinNormalize, BuiltinFnNormalize)
	vm.RegisterBuiltin(BuiltinVLen, BuiltinFnVLen)
	vm.RegisterBuiltin(BuiltinVecToYaw, BuiltinFnVecToYaw)
	vm.RegisterBuiltin(BuiltinVecToAngles, BuiltinFnVecToAngles)
	vm.RegisterBuiltin(BuiltinFAbs, BuiltinFnFAbs)
	vm.RegisterBuiltin(BuiltinRInt, BuiltinFnRInt)
	vm.RegisterBuiltin(BuiltinFloor, BuiltinFnFloor)
	vm.RegisterBuiltin(BuiltinCeil, BuiltinFnCeil)
}

// BuiltinFnMakeVectors implements void makevectors(vector angles).
// tyrquake: PF_makevectors (pr_cmds.c) -- computes the forward /
// right / up basis from the pitch/yaw/roll input and parks each
// triple at the QC globals named v_forward / v_right / v_up.
//
// The QC bytecode reads the resulting basis through plain global
// loads (OP_LOAD_V) at the slot offsets the FindGlobal lookup
// resolves, so the writes are by name -- a progs.dat that omits
// any of the three globals (test stubs with stripped definitions)
// silently skips the write for that one and returns nil; real Q1
// progs.dat always declares all three.
//
// A nil bound progs (test stubs constructed without NewVM(p))
// surfaces as the early-return no-op; the read path doesn't have
// FindGlobal so writes can't be located.
func BuiltinFnMakeVectors(vm *VM) error {
	if vm == nil || vm.progs == nil {
		return nil
	}
	angles, _ := vm.GlobalVector(OfsParm0)
	forward, right, up := mathlib.AngleVectors(angles)
	type binding struct {
		name string
		v    [3]float32
	}
	for _, b := range []binding{
		{"v_forward", forward},
		{"v_right", right},
		{"v_up", up},
	} {
		def := vm.progs.FindGlobal(b.name)
		if def == nil {
			continue
		}
		if err := vm.SetGlobalVector(int(def.Ofs), b.v); err != nil {
			return err
		}
	}
	return nil
}

// BuiltinFnNormalize implements vector normalize(vector).
// tyrquake: PF_normalize.
func BuiltinFnNormalize(vm *VM) error {
	v, _ := vm.GlobalVector(OfsParm0)
	lenSq := v[0]*v[0] + v[1]*v[1] + v[2]*v[2]
	length := float32(math.Sqrt(float64(lenSq)))
	if length == 0 {
		return vm.SetGlobalVector(OfsReturn, [3]float32{0, 0, 0})
	}
	inv := 1.0 / length
	return vm.SetGlobalVector(OfsReturn, [3]float32{v[0] * inv, v[1] * inv, v[2] * inv})
}

// BuiltinFnVLen implements float vlen(vector). tyrquake: PF_vlen.
func BuiltinFnVLen(vm *VM) error {
	v, _ := vm.GlobalVector(OfsParm0)
	lenSq := v[0]*v[0] + v[1]*v[1] + v[2]*v[2]
	return vm.SetGlobalFloat(OfsReturn, float32(math.Sqrt(float64(lenSq))))
}

// BuiltinFnVecToYaw implements float vectoyaw(vector). tyrquake's
// integer-cast quirk preserved: the atan2 result is truncated to
// int before being returned, so callers get whole-degree yaws even
// for small input deltas. tyrquake: PF_vectoyaw.
func BuiltinFnVecToYaw(vm *VM) error {
	v, _ := vm.GlobalVector(OfsParm0)
	var yaw float32
	if v[1] == 0 && v[0] == 0 {
		yaw = 0
	} else {
		yaw = float32(int(math.Atan2(float64(v[1]), float64(v[0])) * 180 / math.Pi))
		if yaw < 0 {
			yaw += 360
		}
	}
	return vm.SetGlobalFloat(OfsReturn, yaw)
}

// BuiltinFnVecToAngles implements vector vectoangles(vector).
// Returns (pitch, yaw, 0). tyrquake: PF_vectoangles.
func BuiltinFnVecToAngles(vm *VM) error {
	v, _ := vm.GlobalVector(OfsParm0)
	var yaw, pitch float32
	if v[1] == 0 && v[0] == 0 {
		yaw = 0
		if v[2] > 0 {
			pitch = 90
		} else {
			pitch = 270
		}
	} else {
		yaw = float32(int(math.Atan2(float64(v[1]), float64(v[0])) * 180 / math.Pi))
		if yaw < 0 {
			yaw += 360
		}
		forward := float32(math.Sqrt(float64(v[0]*v[0] + v[1]*v[1])))
		pitch = float32(int(math.Atan2(float64(v[2]), float64(forward)) * 180 / math.Pi))
		if pitch < 0 {
			pitch += 360
		}
	}
	return vm.SetGlobalVector(OfsReturn, [3]float32{pitch, yaw, 0})
}

// BuiltinFnRandom implements float random() in [0, 1). Reads from
// the source wired by SetRandomSource; returns ErrRandomNotSeeded
// otherwise so a runaway-without-seed bug surfaces loudly.
// tyrquake: PF_random.
func BuiltinFnRandom(vm *VM) error {
	if vm.randomSource == nil {
		return ErrRandomNotSeeded
	}
	return vm.SetGlobalFloat(OfsReturn, vm.randomSource())
}

// BuiltinFnFAbs implements float fabs(float). tyrquake: PF_fabs.
func BuiltinFnFAbs(vm *VM) error {
	v, _ := vm.GlobalFloat(OfsParm0)
	if v < 0 {
		v = -v
	}
	return vm.SetGlobalFloat(OfsReturn, v)
}

// BuiltinFnRInt implements float rint(float) using tyrquake's
// half-away-from-zero rounding (NOT the bankers' rounding Go's
// math.Round emits -- preserve the bit shape). tyrquake: PF_rint.
func BuiltinFnRInt(vm *VM) error {
	f, _ := vm.GlobalFloat(OfsParm0)
	var r float32
	if f > 0 {
		r = float32(int(f + 0.5))
	} else {
		r = float32(int(f - 0.5))
	}
	return vm.SetGlobalFloat(OfsReturn, r)
}

// BuiltinFnFloor implements float floor(float). tyrquake: PF_floor.
func BuiltinFnFloor(vm *VM) error {
	f, _ := vm.GlobalFloat(OfsParm0)
	return vm.SetGlobalFloat(OfsReturn, float32(math.Floor(float64(f))))
}

// BuiltinFnCeil implements float ceil(float). tyrquake: PF_ceil.
func BuiltinFnCeil(vm *VM) error {
	f, _ := vm.GlobalFloat(OfsParm0)
	return vm.SetGlobalFloat(OfsReturn, float32(math.Ceil(float64(f))))
}
