// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package runner

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"

	enginehost "github.com/go-quake1/engine/host"
	"github.com/go-quake1/engine/mathlib"
	"github.com/go-quake1/engine/mdl"
	"github.com/go-quake1/engine/model"
	"github.com/go-quake1/engine/progs"
	"github.com/go-quake1/engine/protocol"
	enginerender "github.com/go-quake1/engine/render"
	engineserver "github.com/go-quake1/engine/server"
	enginesound "github.com/go-quake1/engine/sound"
	"github.com/go-quake1/engine/world"
)

// buildHost wires the embedded pak0 into a fully constructed
// enginehost.Host: progs.Load -> progs.NewVM -> model.NewCache ->
// pak-backed FileResolver -> host.NewHost(maxClients=1) ->
// host.SpawnServer(map).
//
// Returns the SpawnServer'd host on success; any failure (missing
// progs.dat, malformed BSP, entity-parse error) is propagated to the
// caller.
//
// mapSlug is the bare map name ("start", "e1m1") -- SpawnServer
// expands it to "maps/<slug>.bsp" internally via MapBSPPath.
func buildHost(pakFS fs.FS, mapSlug string, logf func(string, ...any)) (*enginehost.Host, error) {
	progsBytes, ok := tryReadPakFile(pakFS, "progs.dat")
	if !ok {
		return nil, fmt.Errorf("buildHost: progs.dat missing from pak")
	}
	p, err := progs.Load(bytes.NewReader(progsBytes), int64(len(progsBytes)))
	if err != nil {
		return nil, fmt.Errorf("buildHost: progs.Load: %w", err)
	}
	vm := progs.NewVM(p)
	logf("progs.dat loaded -- %d bytes, %d functions, %d global defs",
		len(progsBytes), len(p.Functions), len(p.GlobalDefs))

	cache := model.NewCache()
	resolver := func(name string) (int64, io.ReaderAt, error) {
		data, ok := tryReadPakFile(pakFS, name)
		if !ok {
			return 0, nil, fmt.Errorf("pak: %s missing", name)
		}
		return int64(len(data)), bytes.NewReader(data), nil
	}

	h, err := enginehost.NewHost(vm, cache, resolver, 1)
	if err != nil {
		return nil, fmt.Errorf("buildHost: NewHost: %w", err)
	}
	h.SetProgs(p)

	h.SetSoundLoader(func(name string) ([]byte, bool) {
		return tryReadPakFile(pakFS, name)
	})

	pool, perr := enginesound.NewPool(8)
	if perr != nil {
		return nil, fmt.Errorf("buildHost: NewPool: %w", perr)
	}
	h.SetSoundPool(pool)

	vm.RegisterMathBuiltins()
	if err := registerSpawnTimeBuiltins(vm, h, logf); err != nil {
		return nil, fmt.Errorf("buildHost: registerSpawnTimeBuiltins: %w", err)
	}
	vm.SetRandomSource(newLCGRandom(0xC0FFEE))

	h.SetOnArenaReady(func(arena *progs.EdictArena) {
		vm.SetArena(arena)
		logf("arena attached -- %d edicts in arena", arena.Cap())
	})

	if selfDef := p.FindGlobal("self"); selfDef != nil {
		selfOfs := int(selfDef.Ofs)
		vm.SetStateHooks(
			func() float32 { return float32(h.Server.Time) },
			func() int32 {
				v, _ := vm.GlobalInt(selfOfs)
				return v
			},
		)
	}
	if frameDef, nextThinkDef, thinkDef := p.FindField("frame"), p.FindField("nextthink"), p.FindField("think"); frameDef != nil && nextThinkDef != nil && thinkDef != nil {
		vm.SetStateFieldOffsets(int(nextThinkDef.Ofs), int(frameDef.Ofs), int(thinkDef.Ofs))
	}

	h.SetSpawnFn(func(ent *progs.Edict, classname string) {
		_, idx := p.FindFunction(classname)
		if idx < 1 {
			return
		}
		if def := p.FindGlobal("self"); def != nil {
			_ = vm.SetGlobalInt(int(def.Ofs), edictSelfPointer(h, ent))
		}
		if err := vm.Run(int32(idx)); err != nil {
			logf("SpawnFn %s err: %v", classname, err)
		}
	})

	if err := h.SpawnServer(mapSlug, protocol.VersionNQ); err != nil {
		return nil, fmt.Errorf("buildHost: SpawnServer(%q): %w", mapSlug, err)
	}

	dispatchPutClientInServer(h, vm, p, logf)

	return h, nil
}

// dispatchPutClientInServer runs the NQ id1 QC "PutClientInServer"
// function (with a SetNewParms warm-up when defined) against the
// player edict at Server.Edicts[1].
func dispatchPutClientInServer(h *enginehost.Host, vm *progs.VM, p *progs.Progs, logf func(string, ...any)) {
	if h == nil || vm == nil || p == nil {
		return
	}
	if len(h.Server.Edicts) < 2 {
		return
	}
	player := h.Server.Edicts[1]
	if player == nil {
		return
	}

	if timeDef := p.FindGlobal("time"); timeDef != nil {
		_ = vm.SetGlobalFloat(int(timeDef.Ofs), float32(h.Server.Time))
	}

	selfDef := p.FindGlobal("self")
	if selfDef != nil {
		_ = vm.SetGlobalInt(int(selfDef.Ofs), edictSelfPointer(h, player))
	}

	if _, snpIdx := p.FindFunction("SetNewParms"); snpIdx >= 1 {
		if err := vm.Run(int32(snpIdx)); err != nil {
			logf("SetNewParms vm.Run err: %v", err)
		} else {
			logf("SetNewParms dispatched -- starting spawn parms seeded")
		}
	}

	if selfDef != nil {
		_ = vm.SetGlobalInt(int(selfDef.Ofs), edictSelfPointer(h, player))
	}
	_, pcisIdx := p.FindFunction("PutClientInServer")
	if pcisIdx < 1 {
		logf("PutClientInServer not found in progs.dat -- player edict stays at bytecode defaults")
		return
	}
	if err := vm.Run(int32(pcisIdx)); err != nil {
		logf("PutClientInServer vm.Run err: %v", err)
	}

	v, _ := progs.NewEntVars(p, player)
	healthStr := "<unset>"
	if hv, err := v.ReadFloat("health"); err == nil {
		healthStr = fmt.Sprintf("%g", hv)
	}
	viewOfsStr := "<unset>"
	if vo, err := v.ReadVec3("view_ofs"); err == nil {
		viewOfsStr = fmt.Sprintf("(%g,%g,%g)", vo[0], vo[1], vo[2])
	}
	itemsStr := "<unset>"
	if it, err := v.ReadFloat("items"); err == nil {
		itemsStr = fmt.Sprintf("%g (0x%x)", it, int32(it))
	}
	weaponStr := "<unset>"
	if wp, err := v.ReadFloat("weapon"); err == nil {
		weaponStr = fmt.Sprintf("%g", wp)
	}
	logf("PutClientInServer dispatched -- player edict 1 health=%s view_ofs=%s items=%s weapon=%s",
		healthStr, viewOfsStr, itemsStr, weaponStr)
}

// edictSelfPointer returns the QC "self" pointer for ent.
func edictSelfPointer(h *enginehost.Host, ent *progs.Edict) int32 {
	if h.Server.Arena != nil {
		return h.Server.Arena.PointerForEdict(ent)
	}
	return edictSlot(h, ent)
}

// edictSlot returns the index of ent inside h.Server.Edicts.
func edictSlot(h *enginehost.Host, ent *progs.Edict) int32 {
	for i, e := range h.Server.Edicts {
		if e == ent {
			return int32(i)
		}
	}
	return 0
}

// registerSpawnTimeBuiltins installs no-op stubs + real implementations
// for the QC built-in indices typical Q1 entity-spawn functions hit.
func registerSpawnTimeBuiltins(vm *progs.VM, h *enginehost.Host, logf func(string, ...any)) error {
	noop := func(_ *progs.VM) error { return nil }
	vm.RegisterBuiltin(progs.BuiltinSetOrigin, builtinSetOrigin(h, logf))
	vm.RegisterBuiltin(progs.BuiltinSetModel, builtinSetModel(h, logf))
	vm.RegisterBuiltin(progs.BuiltinSetSize, builtinSetSize(h, logf))
	vm.RegisterBuiltin(progs.BuiltinBreak, noop)
	vm.RegisterBuiltin(progs.BuiltinSound, builtinSound(h, logf))
	vm.RegisterBuiltin(progs.BuiltinError, noop)
	vm.RegisterBuiltin(progs.BuiltinObjError, noop)
	vm.RegisterBuiltin(progs.BuiltinSpawn, noop)
	vm.RegisterBuiltin(progs.BuiltinRemove, noop)
	vm.RegisterBuiltin(progs.BuiltinTraceLine, builtinTraceLine(h, logf))
	vm.RegisterBuiltin(progs.BuiltinCheckClient, builtinCheckClient(h))
	vm.RegisterBuiltin(progs.BuiltinFind, noop)
	vm.RegisterBuiltin(progs.BuiltinPrecacheSound, builtinPrecacheSound(h, logf))
	vm.RegisterBuiltin(progs.BuiltinPrecacheModel, builtinPrecacheModel(h, logf))
	vm.RegisterBuiltin(progs.BuiltinStuffCmd, noop)
	vm.RegisterBuiltin(progs.BuiltinFindRadius, builtinFindRadius(h, logf))
	vm.RegisterBuiltin(progs.BuiltinBPrint, noop)
	vm.RegisterBuiltin(progs.BuiltinSPrint, noop)
	vm.RegisterBuiltin(progs.BuiltinDPrint, noop)
	vm.RegisterBuiltin(progs.BuiltinFToS, noop)
	vm.RegisterBuiltin(progs.BuiltinVToS, noop)
	vm.RegisterBuiltin(progs.BuiltinCoreDump, noop)
	vm.RegisterBuiltin(progs.BuiltinTraceOn, noop)
	vm.RegisterBuiltin(progs.BuiltinTraceOff, noop)
	vm.RegisterBuiltin(progs.BuiltinEPrint, noop)
	vm.RegisterBuiltin(progs.BuiltinWalkMove, builtinWalkMove(h))
	vm.RegisterBuiltin(progs.BuiltinMoveToGoal, builtinMoveToGoal(h))
	vm.RegisterBuiltin(progs.BuiltinDropToFloor, noop)
	vm.RegisterBuiltin(progs.BuiltinLightStyle, noop)
	vm.RegisterBuiltin(progs.BuiltinCheckBottom, noop)
	vm.RegisterBuiltin(progs.BuiltinPointContents, noop)
	vm.RegisterBuiltin(progs.BuiltinAim, noop)
	vm.RegisterBuiltin(progs.BuiltinCVar, noop)
	vm.RegisterBuiltin(progs.BuiltinLocalCmd, noop)
	vm.RegisterBuiltin(progs.BuiltinNextEnt, noop)
	vm.RegisterBuiltin(progs.BuiltinParticle, noop)
	vm.RegisterBuiltin(progs.BuiltinChangeYaw, builtinChangeYaw(h))
	for _, idx := range []int{68, 69, 71, 72, 73, 75, 76, 77, 78, 79} {
		vm.RegisterBuiltin(idx, noop)
	}
	vm.RegisterBuiltin(enginehost.BuiltinChangeLevelIdx, enginehost.BuiltinChangeLevel(h))
	vm.RegisterBuiltin(74, builtinAmbientSound(h, logf))
	for _, idx := range []int{52, 53, 54, 55, 56, 57, 58, 59, 60} {
		vm.RegisterBuiltin(idx, noop)
	}
	for _, idx := range []int{
		33, 39, 42, 50,
		61, 62, 63, 64, 65, 66, 67,
		80, 81, 82, 83, 84, 85, 86, 87, 88, 89,
	} {
		vm.RegisterBuiltin(idx, noop)
	}
	return nil
}

// builtinPrecacheModel implements the QC precache_model(name) built-in.
func builtinPrecacheModel(h *enginehost.Host, logf func(string, ...any)) progs.Builtin {
	return func(vm *progs.VM) error {
		if h == nil || h.Server == nil {
			return nil
		}
		off, _ := vm.GlobalInt(progs.OfsParm0)
		name := vm.String(off)
		if name == "" {
			return vm.SetGlobalInt(progs.OfsReturn, off)
		}
		if _, err := engineserver.PrecacheModel(h.Server.ModelPrecache, name); err != nil {
			logf("precache_model(%q): %v", name, err)
		}
		return vm.SetGlobalInt(progs.OfsReturn, off)
	}
}

// builtinPrecacheSound implements the QC precache_sound(name) built-in.
func builtinPrecacheSound(h *enginehost.Host, logf func(string, ...any)) progs.Builtin {
	return func(vm *progs.VM) error {
		if h == nil || h.Server == nil {
			return nil
		}
		off, _ := vm.GlobalInt(progs.OfsParm0)
		name := vm.String(off)
		if name == "" {
			return vm.SetGlobalInt(progs.OfsReturn, off)
		}
		if _, err := h.PrecacheSound(name); err != nil {
			logf("precache_sound(%q): %v", name, err)
		}
		return vm.SetGlobalInt(progs.OfsReturn, off)
	}
}

// builtinSound implements the QC sound(ent, ch, name, vol, atten) built-in.
func builtinSound(h *enginehost.Host, logf func(string, ...any)) progs.Builtin {
	return func(vm *progs.VM) error {
		if h == nil {
			return nil
		}
		entPtr, _ := vm.GlobalInt(progs.OfsParm0)
		chanF, _ := vm.GlobalFloat(progs.OfsParm1)
		nameOff, _ := vm.GlobalInt(progs.OfsParm2)
		volF, _ := vm.GlobalFloat(progs.OfsParm3)
		attenF, _ := vm.GlobalFloat(progs.OfsParm4)

		name := vm.String(nameOff)
		if name == "" {
			return nil
		}

		entIdx := 0
		var entEdict *progs.Edict
		if arena := vm.Arena(); arena != nil && entPtr != 0 {
			if ed, _, err := arena.ResolvePointer(entPtr); err == nil {
				entEdict = ed
				for i, e := range h.Server.Edicts {
					if e == ed {
						entIdx = i
						break
					}
				}
			}
		}

		channel := int(chanF)
		if channel < 0 {
			channel = 0
		}
		if channel > 7 {
			channel = 7
		}
		vol := int(volF * 255)
		if vol < 0 {
			vol = 0
		}
		if vol > 255 {
			vol = 255
		}

		var sourceOrigin [3]float32
		if entEdict != nil {
			if p := h.Progs(); p != nil {
				if ev, err := progs.NewEntVars(p, entEdict); err == nil {
					sourceOrigin, _ = ev.ReadVec3("origin")
				}
			}
		}

		atten := enginesound.SoundAttenuation(attenF)
		if _, err := h.StartSoundAt(entIdx, channel, name, vol, atten, sourceOrigin); err != nil {
			logf("sound(ent=%d ch=%d %q vol=%d): %v", entIdx, channel, name, vol, err)
		}
		return nil
	}
}

// ambientSlotCounter is the round-robin index ambientsound advances so
// each ambient source lands on its own reserved-static channel.
var ambientSlotCounter int

// builtinAmbientSound implements the QC ambientsound(pos, samp, vol, atten) built-in.
func builtinAmbientSound(h *enginehost.Host, logf func(string, ...any)) progs.Builtin {
	return func(vm *progs.VM) error {
		if h == nil || h.SoundPool() == nil {
			return nil
		}
		position, _ := vm.GlobalVector(progs.OfsParm0)
		nameOff, _ := vm.GlobalInt(progs.OfsParm1)
		volF, _ := vm.GlobalFloat(progs.OfsParm2)
		attenF, _ := vm.GlobalFloat(progs.OfsParm3)

		name := vm.String(nameOff)
		if name == "" {
			return nil
		}

		if _, err := h.PrecacheSound(name); err != nil {
			logf("ambientsound precache(%q): %v", name, err)
			return nil
		}

		reserved := h.SoundPool().ReservedStatic
		if reserved <= 0 {
			return nil
		}
		slot := ambientSlotCounter % reserved
		ambientSlotCounter++

		vol := int(volF * 255)
		if vol < 0 {
			vol = 0
		}
		if vol > 255 {
			vol = 255
		}
		atten := enginesound.SoundAttenuation(attenF)
		if _, err := h.AmbientSoundAt(slot, 0, name, vol, position, atten); err != nil {
			logf("ambientsound(%q vol=%d slot=%d): %v", name, vol, slot, err)
		}
		return nil
	}
}

// setModelCache caches per-builtinSetModel state.
type setModelCache struct {
	mdlBBox map[int][2][3]float32
	traced  int
}

const setModelTraceCalls = 8

// builtinSetModel implements the QC setmodel(ent, name) built-in.
func builtinSetModel(h *enginehost.Host, logf func(string, ...any)) progs.Builtin {
	cache := &setModelCache{mdlBBox: map[int][2][3]float32{}}
	return func(vm *progs.VM) error {
		if h == nil || h.Server == nil {
			return nil
		}
		entPtr, _ := vm.GlobalInt(progs.OfsParm0)
		nameOff, _ := vm.GlobalInt(progs.OfsParm1)
		name := vm.String(nameOff)
		arena := vm.Arena()
		if arena == nil {
			return nil
		}
		ent, edictIdx, err := arena.ResolvePointer(entPtr)
		if err != nil {
			logf("setmodel(ptr=%d, %q): ResolvePointer: %v", entPtr, name, err)
			return nil
		}
		idx, idxErr := engineserver.ModelIndex(h.Server.ModelPrecache, name)
		if idxErr != nil {
			logf("setmodel(%q): %v", name, idxErr)
		}
		p := vm.Progs()
		if p == nil {
			return nil
		}
		if def := p.FindField("model"); def != nil {
			_ = ent.FieldSetInt(int(def.Ofs), nameOff)
		}
		if def := p.FindField("modelindex"); def != nil {
			_ = ent.FieldSetFloat(int(def.Ofs), float32(idx))
		}

		ev, _ := progs.NewEntVars(p, ent)
		var beforeMins, beforeMaxs, beforeSize [3]float32
		traceThis := cache.traced < setModelTraceCalls
		if traceThis {
			beforeMins, _ = ev.ReadVec3("mins")
			beforeMaxs, _ = ev.ReadVec3("maxs")
			beforeSize, _ = ev.ReadVec3("size")
		}

		mins, maxs, bboxOK := resolveModelBBox(h, cache, name, idx)
		if bboxOK {
			size := [3]float32{
				maxs[0] - mins[0],
				maxs[1] - mins[1],
				maxs[2] - mins[2],
			}
			_ = ev.WriteVec3("mins", mins)
			_ = ev.WriteVec3("maxs", maxs)
			_ = ev.WriteVec3("size", size)

			if h.World != nil {
				origin, _ := ev.ReadVec3("origin")
				absmin := [3]float32{
					origin[0] + mins[0],
					origin[1] + mins[1],
					origin[2] + mins[2],
				}
				absmax := [3]float32{
					origin[0] + maxs[0],
					origin[1] + maxs[1],
					origin[2] + maxs[2],
				}
				kind := solidKindFromEntvars(ev)
				// Key by the edict's arena SLOT (NumFor), not the byte offset
				// ResolvePointer returns (0 for a setmodel(self,...) pointer,
				// which would collapse every entity onto world.Key(0)).
				if slot := h.Server.Arena.NumFor(ent); slot >= 0 {
					h.World.LinkBounds(world.Key(slot), absmin, absmax, kind)
				}
			}

			if traceThis {
				cache.traced++
				logf("setmodel(slot=%d, %q, idx=%d) -- mins/maxs/size BEFORE=%v/%v/%v AFTER=%v/%v/%v",
					edictIdx, name, idx,
					beforeMins, beforeMaxs, beforeSize,
					mins, maxs, size)
			}
		} else if traceThis {
			cache.traced++
			logf("setmodel(slot=%d, %q, idx=%d) -- bbox unresolved (kept mins/maxs %v/%v)",
				edictIdx, name, idx, beforeMins, beforeMaxs)
		}
		return nil
	}
}

// resolveModelBBox returns the world-space (mins, maxs) bounding box.
func resolveModelBBox(h *enginehost.Host, cache *setModelCache, name string, idx int) (mins, maxs [3]float32, ok bool) {
	if name == "" || idx == 0 {
		return mins, maxs, false
	}
	if name[0] == '*' || (idx == 1 && len(name) >= 4 && name[:4] == "maps") {
		if h.Server.WorldModel == nil || h.Server.WorldModel.File == nil {
			return mins, maxs, false
		}
		models, err := h.Server.WorldModel.File.Models()
		if err != nil {
			return mins, maxs, false
		}
		smIdx := idx - 1
		if smIdx < 0 || smIdx >= len(models) {
			return mins, maxs, false
		}
		return models[smIdx].Mins, models[smIdx].Maxs, true
	}
	if bb, hit := cache.mdlBBox[idx]; hit {
		return bb[0], bb[1], true
	}
	if h.Resolver == nil {
		return mins, maxs, false
	}
	size, ra, err := h.Resolver(name)
	if err != nil {
		return mins, maxs, false
	}
	m, err := mdl.Load(ra, size)
	if err != nil {
		return mins, maxs, false
	}
	if len(m.Frames) == 0 {
		return mins, maxs, false
	}
	f := &m.Frames[0]
	var bbMin, bbMax mdl.TriVertx
	switch f.Type {
	case mdl.FrameSingle:
		bbMin, bbMax = f.Single.BBoxMin, f.Single.BBoxMax
	case mdl.FrameGroup:
		if f.Group == nil || len(f.Group.Frames) == 0 {
			return mins, maxs, false
		}
		bbMin, bbMax = f.Group.Frames[0].BBoxMin, f.Group.Frames[0].BBoxMax
	default:
		return mins, maxs, false
	}
	for i := 0; i < 3; i++ {
		mins[i] = m.Header.Scale[i]*float32(bbMin.V[i]) + m.Header.ScaleOrigin[i]
		maxs[i] = m.Header.Scale[i]*float32(bbMax.V[i]) + m.Header.ScaleOrigin[i]
	}
	cache.mdlBBox[idx] = [2][3]float32{mins, maxs}
	return mins, maxs, true
}

// builtinTraceLine implements the QC traceline(v1, v2, nomon, forent) built-in.
func builtinTraceLine(h *enginehost.Host, logf func(string, ...any)) progs.Builtin {
	return func(vm *progs.VM) error {
		v1, _ := vm.GlobalVector(progs.OfsParm0)
		v2, _ := vm.GlobalVector(progs.OfsParm1)
		nomon, _ := vm.GlobalFloat(progs.OfsParm2)
		entPtr, _ := vm.GlobalInt(progs.OfsParm3)

		var passEdict *progs.Edict
		if arena := vm.Arena(); arena != nil {
			if ed, _, err := arena.ResolvePointer(entPtr); err == nil {
				passEdict = ed
			}
		}
		mode := enginehost.MoveNormal
		if nomon != 0 {
			mode = enginehost.MoveNoMonsters
		}

		res, err := h.TraceLine(v1, v2, mode, passEdict)
		if err != nil {
			logf("traceline: %v", err)
			return nil
		}

		var trEntPtr int32
		if res.EntIdx > 0 {
			if arena := vm.Arena(); arena != nil {
				trEntPtr = arena.MakePointer(res.EntIdx, 0)
			}
		}
		return enginehost.WriteTraceGlobals(vm, vm.Progs(), res, trEntPtr)
	}
}

// builtinSetSize implements the QC setsize(ent, mins, maxs) built-in.
//
// tyrquake: PF_setsize -> SV_SetSize. It writes the entity's mins/maxs/size
// then RELINKS it into the area tree with the new absolute bounds. Monsters
// get their collision bbox here (their alias .mdl bbox does not resolve in
// setmodel, so setmodel skips their link) -- without this, a monster has no
// bbox and never enters the area tree, so traceline (hitscan) and every
// SV_Move query pass straight through it (shots never connect). Doors/movers
// use SOLID_BSP and are already linked by setmodel via their submodel bbox.
func builtinSetSize(h *enginehost.Host, logf func(string, ...any)) progs.Builtin {
	return func(vm *progs.VM) error {
		if h == nil || h.Server == nil {
			return nil
		}
		entPtr, _ := vm.GlobalInt(progs.OfsParm0)
		mins, _ := vm.GlobalVector(progs.OfsParm1)
		maxs, _ := vm.GlobalVector(progs.OfsParm2)
		arena := vm.Arena()
		if arena == nil {
			return nil
		}
		ent, _, err := arena.ResolvePointer(entPtr)
		if err != nil {
			logf("setsize(ptr=%d): ResolvePointer: %v", entPtr, err)
			return nil
		}
		p := vm.Progs()
		if p == nil {
			return nil
		}
		ev, _ := progs.NewEntVars(p, ent)
		size := [3]float32{maxs[0] - mins[0], maxs[1] - mins[1], maxs[2] - mins[2]}
		_ = ev.WriteVec3("mins", mins)
		_ = ev.WriteVec3("maxs", maxs)
		_ = ev.WriteVec3("size", size)

		// Relink with the new bounds at the entity's current origin. The
		// area-tree key is the edict's arena SLOT (NumFor), NOT the byte
		// offset ResolvePointer returns -- a setsize(self,...) pointer
		// addresses field-offset 0, so that offset is 0 for every entity
		// and would collapse them all onto world.Key(0).
		if h.World != nil {
			slot := h.Server.Arena.NumFor(ent)
			if slot >= 0 {
				origin, _ := ev.ReadVec3("origin")
				absmin := [3]float32{origin[0] + mins[0], origin[1] + mins[1], origin[2] + mins[2]}
				absmax := [3]float32{origin[0] + maxs[0], origin[1] + maxs[1], origin[2] + maxs[2]}
				kind := solidKindFromEntvars(ev)
				h.World.LinkBounds(world.Key(slot), absmin, absmax, kind)
			}
		}
		return nil
	}
}

// builtinCheckClient implements the QC checkclient() built-in.
//
// tyrquake: PF_checkclient. It hands the caller a client the monster's
// FindTarget then range/infront/visible-checks. Without it (a no-op returns
// world = 0), FindTarget never sees the player, so no monster ever wakes or
// attacks. The upstream rate-limits + PVS-gates the pick across many clients;
// in this single-player loopback we return the first active client
// unconditionally and let FindTarget's own visible() traceline gate sight --
// so an occluded monster fails visible() and stays asleep. Returns world (0)
// when no client is active.
func builtinCheckClient(h *enginehost.Host) progs.Builtin {
	return func(vm *progs.VM) error {
		arena := vm.Arena()
		var ptr int32
		if h != nil && h.Static != nil && arena != nil {
			for _, c := range h.Static.Clients {
				if c == nil || !c.Active || c.Edict == nil {
					continue
				}
				if slot := arena.NumFor(c.Edict); slot > 0 {
					ptr = arena.MakePointer(slot, 0)
					break
				}
			}
		}
		return vm.SetGlobalInt(progs.OfsReturn, ptr)
	}
}

// builtinChangeYaw implements the QC changeyaw() built-in.
//
// tyrquake: PF_changeyaw -> SV_ChangeYaw. Turns the `self` entity's yaw
// (angles[1]) toward its ideal_yaw by at most yaw_speed degrees this tic,
// taking the shorter way around. The monster AI sets ideal_yaw toward its
// enemy each think (ai_face/ai_run) then calls changeyaw, so with this a
// monster rotates to face the player instead of firing only in its spawn
// direction. Reads `self` from the QC global. A no-op left every monster
// frozen at its spawn yaw.
func builtinChangeYaw(h *enginehost.Host) progs.Builtin {
	return func(vm *progs.VM) error {
		p := vm.Progs()
		arena := vm.Arena()
		if p == nil || arena == nil {
			return nil
		}
		selfDef := p.FindGlobal("self")
		if selfDef == nil {
			return nil
		}
		selfPtr, _ := vm.GlobalInt(int(selfDef.Ofs))
		ent, _, err := arena.ResolvePointer(selfPtr)
		if err != nil {
			return nil
		}
		ev, err := progs.NewEntVars(p, ent)
		if err != nil {
			return nil
		}
		angles, _ := ev.ReadVec3("angles")
		ideal, _ := ev.ReadFloat("ideal_yaw")
		speed, _ := ev.ReadFloat("yaw_speed")
		current := mathlib.AngleMod(angles[1])
		if current == ideal {
			return nil
		}
		move := ideal - current
		if ideal > current {
			if move >= 180 {
				move -= 360
			}
		} else {
			if move <= -180 {
				move += 360
			}
		}
		if move > 0 {
			if move > speed {
				move = speed
			}
		} else {
			if move < -speed {
				move = -speed
			}
		}
		angles[1] = mathlib.AngleMod(current + move)
		return ev.WriteVec3("angles", angles)
	}
}

// selfEntVars resolves the QC `self` global to its edict + entvars. Returns
// ok=false when self is unset/unresolvable or there is no progs/arena.
func selfEntVars(vm *progs.VM) (*progs.Edict, *progs.EntVars, bool) {
	p := vm.Progs()
	arena := vm.Arena()
	if p == nil || arena == nil {
		return nil, nil, false
	}
	selfDef := p.FindGlobal("self")
	if selfDef == nil {
		return nil, nil, false
	}
	selfPtr, _ := vm.GlobalInt(int(selfDef.Ofs))
	ent, _, err := arena.ResolvePointer(selfPtr)
	if err != nil {
		return nil, nil, false
	}
	ev, err := progs.NewEntVars(p, ent)
	if err != nil {
		return nil, nil, false
	}
	return ent, ev, true
}

// gatherMoveCandidates returns the solid edicts a `dist`-unit monster step
// from origin might clip against -- every area-tree solid whose bounds could
// overlap the move (actor bbox + step-up slack), minus the actor slot itself.
// SOLID_BSP submodels (doors/movers) carry their per-entity BrushModel. Mirrors
// the candidate build in [enginehost.Host.TraceLine]; p must be the live progs
// (h.Progs() can be nil inside a builtin -- pass vm.Progs()).
func gatherMoveCandidates(h *enginehost.Host, p *progs.Progs, origin, mins, maxs [3]float32, dist float32, excludeSlot int) []world.Target {
	if h == nil || h.World == nil || h.Server == nil || p == nil {
		return nil
	}
	reach := dist + world.StepSize + 8
	lo := [3]float32{origin[0] + mins[0] - reach, origin[1] + mins[1] - reach, origin[2] + mins[2] - reach}
	hi := [3]float32{origin[0] + maxs[0] + reach, origin[1] + maxs[1] + reach, origin[2] + maxs[2] + reach}
	keys := h.World.AreaQuery(lo, hi, world.QuerySolidsOnly)
	out := make([]world.Target, 0, len(keys))
	for _, k := range keys {
		idx := int(k)
		if idx <= 0 || idx >= len(h.Server.Edicts) || idx == excludeSlot {
			continue
		}
		ed := h.Server.Edicts[idx]
		if ed == nil || ed.Free {
			continue
		}
		ev, err := progs.NewEntVars(p, ed)
		if err != nil {
			continue
		}
		solF, err := ev.ReadFloat("solid")
		if err != nil {
			continue
		}
		sol := engineserver.Solid(int32(solF))
		if sol == engineserver.SolidNot {
			continue
		}
		o, _ := ev.ReadVec3("origin")
		emins, _ := ev.ReadVec3("mins")
		emaxs, _ := ev.ReadVec3("maxs")
		tgt := world.Target{Origin: o, Mins: emins, Maxs: emaxs, Solid: sol}
		if sol == engineserver.SolidBSP {
			miF, err := ev.ReadFloat("modelindex")
			if err != nil {
				continue
			}
			mi := int(miF)
			if mi <= 0 || mi >= len(h.Server.BrushModels) || h.Server.BrushModels[mi] == nil {
				continue
			}
			tgt.BrushModel = h.Server.BrushModels[mi]
		}
		out = append(out, tgt)
	}
	return out
}

// builtinWalkMove implements the QC walkmove(yaw, dist) built-in.
//
// tyrquake: PF_walkmove -> SV_movestep. Steps `self` dist units along yaw
// across the world floor (stepping up/down stairs), writes the new origin +
// flags back, relinks, and returns 1 if it moved / 0 if blocked. World-only
// clipping for now (nil candidates): monsters step on world geometry but pass
// through each other / closed doors -- entity clipping is a follow-up.
func builtinWalkMove(h *enginehost.Host) progs.Builtin {
	return func(vm *progs.VM) error {
		ret := func(v float32) error { return vm.SetGlobalFloat(progs.OfsReturn, v) }
		if h == nil || h.Server == nil || h.Server.WorldModel == nil {
			return ret(0)
		}
		self, ev, ok := selfEntVars(vm)
		if !ok {
			return ret(0)
		}
		yaw, _ := vm.GlobalFloat(progs.OfsParm0)
		dist, _ := vm.GlobalFloat(progs.OfsParm1)
		origin, _ := ev.ReadVec3("origin")
		mins, _ := ev.ReadVec3("mins")
		maxs, _ := ev.ReadVec3("maxs")
		flagsF, _ := ev.ReadFloat("flags")
		slot := vm.Arena().NumFor(self)
		in := world.MoveStepIn{
			Origin:    origin,
			Mins:      mins,
			Maxs:      maxs,
			Flags:     engineserver.EntityFlag(int32(flagsF)),
			EntityKey: world.Key(slot),
		}
		cands := gatherMoveCandidates(h, vm.Progs(), origin, mins, maxs, dist, slot)
		out, err := world.StepDirection(yaw, dist, in, h.Server.WorldModel, cands)
		if err != nil || !out.Moved {
			return ret(0)
		}
		_ = ev.WriteVec3("origin", out.NewOrigin)
		_ = ev.WriteFloat("flags", float32(int32(out.NewFlags)))
		h.LinkEdict(self)
		return ret(1)
	}
}

// builtinMoveToGoal implements the QC movetogoal(dist) built-in.
//
// tyrquake: PF_MoveToGoal -> SV_MoveToGoal. This is what ai_run/ai_walk call
// to CHASE: step `self` dist units toward self.goalentity, trying the current
// yaw then goal-aware alternatives (world.MoveToGoal). Writes the new origin,
// yaw (angles[1]) + flags back and relinks. A no-op left every monster
// stationary. World-only clipping for now (nil candidates).
func builtinMoveToGoal(h *enginehost.Host) progs.Builtin {
	return func(vm *progs.VM) error {
		if h == nil || h.Server == nil || h.Server.WorldModel == nil {
			return nil
		}
		self, ev, ok := selfEntVars(vm)
		if !ok {
			return nil
		}
		dist, _ := vm.GlobalFloat(progs.OfsParm0)
		// Resolve self.goalentity -> its origin (the chase target).
		goalPtr, err := ev.ReadInt32("goalentity")
		if err != nil {
			return nil
		}
		goalEnt, _, err := vm.Arena().ResolvePointer(goalPtr)
		if err != nil {
			return nil
		}
		gev, err := progs.NewEntVars(vm.Progs(), goalEnt)
		if err != nil {
			return nil
		}
		goalOrigin, _ := gev.ReadVec3("origin")

		origin, _ := ev.ReadVec3("origin")
		mins, _ := ev.ReadVec3("mins")
		maxs, _ := ev.ReadVec3("maxs")
		flagsF, _ := ev.ReadFloat("flags")
		idealYaw, _ := ev.ReadFloat("ideal_yaw")
		slot := vm.Arena().NumFor(self)
		in := world.MoveToGoalIn{
			Origin:     origin,
			GoalOrigin: goalOrigin,
			Mins:       mins,
			Maxs:       maxs,
			Flags:      engineserver.EntityFlag(int32(flagsF)),
			Yaw:        idealYaw,
			Dist:       dist,
			EntityKey:  world.Key(slot),
		}
		cands := gatherMoveCandidates(h, vm.Progs(), origin, mins, maxs, dist, slot)
		out, err := world.MoveToGoal(in, h.Server.WorldModel, cands)
		if err != nil || !out.Moved {
			return nil
		}
		_ = ev.WriteVec3("origin", out.NewOrigin)
		_ = ev.WriteFloat("flags", float32(int32(out.NewFlags)))
		angles, _ := ev.ReadVec3("angles")
		angles[1] = out.NewYaw
		_ = ev.WriteVec3("angles", angles)
		h.LinkEdict(self)
		return nil
	}
}

// builtinFindRadius implements the QC findradius(org, rad) built-in.
func builtinFindRadius(h *enginehost.Host, logf func(string, ...any)) progs.Builtin {
	return func(vm *progs.VM) error {
		org, _ := vm.GlobalVector(progs.OfsParm0)
		rad, _ := vm.GlobalFloat(progs.OfsParm1)

		slots := h.FindRadius(org, rad)

		arena := vm.Arena()
		var pointerFor func(int) int32
		if arena != nil {
			pointerFor = func(slot int) int32 { return arena.MakePointer(slot, 0) }
		}

		var edicts []*progs.Edict
		if h != nil && h.Server != nil {
			edicts = h.Server.Edicts
		}
		headSlot, err := enginehost.ChainEdicts(vm.Progs(), edicts, slots, pointerFor)
		if err != nil {
			logf("findradius: %v", err)
			return vm.SetGlobalInt(progs.OfsReturn, 0)
		}
		var headPtr int32
		if headSlot > 0 && arena != nil {
			headPtr = arena.MakePointer(headSlot, 0)
		}
		return vm.SetGlobalInt(progs.OfsReturn, headPtr)
	}
}

// builtinSetOrigin implements the QC setorigin(ent, vec) built-in.
func builtinSetOrigin(h *enginehost.Host, logf func(string, ...any)) progs.Builtin {
	return func(vm *progs.VM) error {
		if h == nil || h.Server == nil {
			return nil
		}
		arena := vm.Arena()
		if arena == nil {
			return nil
		}
		entPtr, _ := vm.GlobalInt(progs.OfsParm0)
		origin, _ := vm.GlobalVector(progs.OfsParm1)
		ent, _, err := arena.ResolvePointer(entPtr)
		if err != nil {
			logf("setorigin(ptr=%d, %v): ResolvePointer: %v", entPtr, origin, err)
			return nil
		}
		h.SetOrigin(ent, origin)
		return nil
	}
}

// solidKindFromEntvars reads the QC `solid` field and maps it to a SolidKind.
func solidKindFromEntvars(ev *progs.EntVars) world.SolidKind {
	solid, err := ev.ReadFloat("solid")
	if err != nil {
		return world.SolidKindSkip
	}
	switch engineserver.Solid(int32(solid)) {
	case engineserver.SolidNot:
		return world.SolidKindSkip
	case engineserver.SolidTrigger:
		return world.SolidKindTrigger
	default:
		return world.SolidKindSolid
	}
}

// newLCGRandom returns a float-in-[0,1) callback suitable for VM.SetRandomSource.
func newLCGRandom(seed uint32) func() float32 {
	state := seed
	return func() float32 {
		state = state*1664525 + 1013904223
		return float32(state>>8) / float32(1<<24)
	}
}

// newLCGByteSource returns a uniform-byte callback for the particle pool.
func newLCGByteSource(seed uint32) func() byte {
	state := seed
	return func() byte {
		state = state*1664525 + 1013904223
		return byte(state >> 24)
	}
}

// writePlayerOrigin overwrites the QC "origin" vector on the player edict.
func writePlayerOrigin(h *enginehost.Host, slot int, origin [3]float32) error {
	if h == nil || slot < 0 || slot >= len(h.Server.Edicts) {
		return enginehost.ErrNoEdict
	}
	ent := h.Server.Edicts[slot]
	if ent == nil {
		return enginehost.ErrNoEdict
	}
	p := h.Progs()
	if p == nil {
		return enginehost.ErrNoProgs
	}
	v, err := progs.NewEntVars(p, ent)
	if err != nil {
		return err
	}
	return v.WriteVec3("origin", origin)
}

// initPlayerForPhysicsWalk seeds the per-edict entvars fields the PhysicsWalk handler requires.
func initPlayerForPhysicsWalk(h *enginehost.Host, slot int) error {
	if h == nil || slot < 0 || slot >= len(h.Server.Edicts) {
		return enginehost.ErrNoEdict
	}
	ent := h.Server.Edicts[slot]
	if ent == nil {
		return enginehost.ErrNoEdict
	}
	p := h.Progs()
	if p == nil {
		return enginehost.ErrNoProgs
	}
	v, err := progs.NewEntVars(p, ent)
	if err != nil {
		return err
	}
	if err := v.WriteFloat("movetype", float32(int32(engineserver.MoveTypeWalk))); err != nil {
		return err
	}
	if err := v.WriteFloat("solid", float32(int32(engineserver.SolidSlideBox))); err != nil {
		return err
	}
	if err := v.WriteVec3("mins", [3]float32{-16, -16, -24}); err != nil {
		return err
	}
	if err := v.WriteVec3("maxs", [3]float32{16, 16, 32}); err != nil {
		return err
	}
	if err := v.WriteVec3("velocity", [3]float32{0, 0, 0}); err != nil {
		return err
	}
	if err := v.WriteVec3("v_angle", [3]float32{0, 0, 0}); err != nil {
		return err
	}
	if err := v.WriteFloat("flags", 0); err != nil {
		return err
	}
	if err := v.WriteFloat("gravity", 1.0); err != nil && !errors.Is(err, progs.ErrFieldNotFound) {
		return err
	}
	return nil
}

// trailKindForModel maps a precache model name to the trail kind
// tyrquake's CL_LinkEntities dispatches based on the entity's
// per-model EF_* bits.
func trailKindForModel(name string) (enginerender.TrailKind, bool) {
	switch name {
	case "progs/missile.mdl":
		return enginerender.TrailRocket, true
	case "progs/grenade.mdl":
		return enginerender.TrailGrenade, true
	case "progs/gib1.mdl", "progs/gib2.mdl", "progs/gib3.mdl",
		"progs/zom_gib.mdl":
		return enginerender.TrailBlood, true
	case "progs/k_spike.mdl":
		return enginerender.TrailSlightBlood, true
	case "progs/w_spike.mdl":
		return enginerender.TrailTracer, true
	case "progs/laser.mdl":
		return enginerender.TrailTracer2, true
	case "progs/v_spike.mdl":
		return enginerender.TrailVoor, true
	}
	return 0, false
}

// Silence linter on the io import (kept for the file-resolver signature).
var _ = io.EOF
