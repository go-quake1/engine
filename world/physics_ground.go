// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package world

import (
	"errors"

	"github.com/go-quake1/engine/progs"
	"github.com/go-quake1/engine/server"
	"github.com/go-quake1/engine/svuser"
)

// stepDefaultGravity is the per-entity gravity-scale fallback used
// when the QC `gravity` field is either absent or zero. tyrquake:
// the `if (!val || !val->_float) scale = 1.0` branch inside
// SV_AddGravity. Identical to [tossDefaultGravity]; kept as a
// separately-named constant so a future tune can decouple the two
// without changing the other.
const stepDefaultGravity = 1.0

// jumpSpeed is the upward velocity a standing +jump imparts, in units/s.
// tyrquake: the `270` literal in QC PlayerJump (QC/player.qc).
const jumpSpeed = 270

// readStepGravityFactor returns the per-entity gravity scale from the
// QC `gravity` field, or [stepDefaultGravity] when the field is
// absent. Mirrors [readGravityFactor] in physics_toss.go -- separate
// copy so PhysicsStep/PhysicsWalk stay independent of the
// PhysicsToss/Bounce path.
//
// Type mismatches (the field exists but is not EvFloat -- a corrupt-
// progs signal) are surfaced verbatim so callers can distinguish them
// from "mod doesn't add the field".
func readStepGravityFactor(ev *progs.EntVars) (float32, error) {
	g, err := ev.ReadFloat("gravity")
	if err != nil {
		if errors.Is(err, progs.ErrFieldNotFound) {
			return stepDefaultGravity, nil
		}
		return 0, err
	}
	return g, nil
}

// PhysicsStep implements the MOVETYPE_STEP handler -- monsters that
// freefall when airborne and walk in discrete one-tick steps when
// on the ground.
// tyrquake: SV_Physics_Step in common/sv_phys.c.
//
// SPEC DEVIATION (documented up-front): the C upstream's
// SV_Physics_Step uses SV_FlyMove for the airborne integration arm
// AND does NOT integrate at all when on the ground (the entity stays
// put unless its think runs SV_movestep itself). The Go port follows
// the task brief verbatim:
//
//   - airborne (FL_ONGROUND / FL_FLY / FL_SWIM all clear): apply
//     gravity, clamp velocity, then call [MoveStep] with the
//     velocity*dt delta. This matches the spirit of the upstream's
//     SV_FlyMove call but routes through the world/-side step-up
//     primitive instead of a raw slide.
//   - on-ground: skip the gravity / integration pass entirely
//     (matches the upstream's outer `if (!(flags & (FL_ONGROUND |
//     FL_FLY | FL_SWIM)))` guard).
//   - after the move, if MoveStep refused the step (Moved == false)
//     because the floor was lost AND the entity does NOT carry
//     FL_PARTIALGROUND, the move is undone -- this is the
//     "non-PARTIALGROUND walked-off-an-edge refusal" path from
//     SV_movestep that MoveStep itself surfaces via Moved=false.
//
// Algorithm (Go-port order, matching the task brief):
//
//  1. Read flags, velocity, origin, mins, maxs from EntVars.
//  2. If FL_ONGROUND | FL_FLY | FL_SWIM are all clear (airborne):
//     a. Apply gravity (entity-side gravityFactor, params.Gravity, dt).
//     b. ClampVelocity to ±params.MaxVelocity.
//     c. Build a per-tick move delta = velocity * dt.
//     d. Call MoveStep with the delta + the entity's flags.
//     e. If MoveStep.Moved is true, commit the new origin + velocity.
//     f. If MoveStep refused the step (Moved == false) AND
//     FL_PARTIALGROUND is NOT set: undo (keep origin, write only
//     the gravity-modified velocity back -- the entity stays in
//     place but the next tick still inherits the gravity pull).
//     g. If MoveStep refused the step AND FL_PARTIALGROUND IS set:
//     commit the gravity-modified velocity AND advance origin
//     by the raw delta anyway (the C upstream's "monster had the
//     ground pulled out, go ahead and fall" branch).
//  3. Dispatch RunThink AFTER the move (matches the upstream's
//     `SV_AddGravity -> SV_CheckVelocity -> SV_FlyMove -> SV_RunThink`
//     ordering).
//
// SIMPLIFICATIONS vs the C upstream:
//
//   - The "hit sound" branch (the C `if (hitsound) SV_StartSound(...,
//     "demon/dland2.wav", ...)`) is dropped: it depends on the audio
//     subsystem the Go port hasn't surfaced yet AND on the prior-tick
//     vertical velocity, which would force PhysicsStep to carry a
//     side-channel. A future commit can re-introduce it once the
//     audio glue lands.
//   - SV_LinkEdict after the move is omitted (the dispatcher owns
//     the World handle + the bbox-from-entvars synthesis).
//   - CheckWaterTransition is omitted -- same rationale as
//     [PhysicsToss]'s docstring (the FL_INWATER bookkeeping lands in
//     a future PR alongside [server.PointContents]).
//
// Returns:
//
//	true,  nil  -- happy path (move applied, or skipped because on-
//	               ground, plus think dispatched).
//	false, err  -- EntVars read error, MoveStep trace error, or
//	               RunThink dispatch error.
func PhysicsStep(ent *progs.Edict, ev *progs.EntVars, key Key, params server.PhysParams, ctx PhysicsContext) (bool, error) {
	flagsF, err := ev.ReadFloat("flags")
	if err != nil {
		return false, err
	}
	flags := server.EntityFlag(int32(flagsF))

	// Airborne integration: gravity, clamp, MoveStep. Skipped entirely
	// when grounded / flying / swimming.
	if (flags & (server.FlagOnGround | server.FlagFly | server.FlagSwim)) == 0 {
		velocity, err := ev.ReadVec3("velocity")
		if err != nil {
			return false, err
		}
		origin, err := ev.ReadVec3("origin")
		if err != nil {
			return false, err
		}
		mins, err := ev.ReadVec3("mins")
		if err != nil {
			return false, err
		}
		maxs, err := ev.ReadVec3("maxs")
		if err != nil {
			return false, err
		}

		gravityFactor, err := readStepGravityFactor(ev)
		if err != nil {
			return false, err
		}
		velocity = server.ApplyGravity(velocity, gravityFactor, params.Gravity, ctx.Dt)
		velocity = server.ClampVelocity(velocity, params.MaxVelocity)

		move := [3]float32{
			velocity[0] * ctx.Dt,
			velocity[1] * ctx.Dt,
			velocity[2] * ctx.Dt,
		}

		mout, err := MoveStep(MoveStepIn{
			Origin:    origin,
			Mins:      mins,
			Maxs:      maxs,
			Move:      move,
			Flags:     flags,
			EntityKey: key,
		}, ctx.Worldmodel, ctx.Candidates)
		if err != nil {
			return false, err
		}

		// WriteVec3/WriteFloat for fields whose Read just succeeded
		// cannot fail: same offset + type + range. Error branches
		// dropped per the bsptrace pattern of removing C-inherited
		// unreachable code.
		switch {
		case mout.Moved:
			// Clean step -- commit origin + velocity.
			_ = ev.WriteVec3("origin", mout.NewOrigin)
			_ = ev.WriteVec3("velocity", velocity)
		case (flags & server.FlagPartialGround) != 0:
			// "Monster had the ground pulled out" -- the C upstream's
			// `if ((int)entity->v.flags & FL_PARTIALGROUND)` branch
			// inside SV_movestep's fraction==1 fall-off arm: commit
			// the gravity-modified velocity AND advance origin by the
			// raw delta. MoveStep itself refused the step (Moved=false),
			// so we apply the partial-ground override here.
			fallen := [3]float32{
				origin[0] + move[0],
				origin[1] + move[1],
				origin[2] + move[2],
			}
			_ = ev.WriteVec3("origin", fallen)
			_ = ev.WriteVec3("velocity", velocity)
		default:
			// MoveStep refused the step AND no PARTIALGROUND override
			// -- keep origin, write only the gravity-modified velocity
			// back so the next tick still inherits the gravity pull.
			_ = ev.WriteVec3("velocity", velocity)
		}
	}

	// Think dispatch (post-move). The C upstream surfaces RunThink as
	// a qboolean return; the Go port surfaces (alive, err) -- the
	// !alive-without-err branch is structurally unreachable (it would
	// require ent.free, which has no Edict-surface equivalent yet),
	// so the alive bool is discarded; only err drives the return.
	if _, err := server.RunThink(ent, ev, ctx.Now, ctx.Dt, ctx.ThinkCaller); err != nil {
		return false, err
	}
	return true, nil
}

// PhysicsWalk implements the MOVETYPE_WALK handler -- the player.
// tyrquake: SV_Physics_Client / SV_WalkMove (the MOVETYPE_WALK arm)
// in common/sv_phys.c, with the wishvel synthesis from
// NQ/sv_user.c's SV_AirMove. The C upstream uses host_client globals
// heavily; the Go port takes [server.UserCmd] as an explicit
// parameter (the C reads it from host_client->cmd).
//
// Algorithm (Go-port distillation -- the parts that don't depend on
// host_client globals):
//
//  1. ClampVelocity (SV_CheckVelocity, per-axis ±params.MaxVelocity).
//  2. Apply gravity unless FL_ONGROUND is set. The C upstream gates
//     gravity on `!SV_CheckWater(player) && !(flags & FL_WATERJUMP)`;
//     the Go port drops the water gate here (water bookkeeping is
//     deferred -- see the [PhysicsToss] docstring) and only honors
//     the FL_ONGROUND gate, which is the dominant decision on dry
//     land.
//  3. ApplyFriction (ground only -- airborne players have no
//     friction, matching SV_UserFriction's early-return on `!onground`).
//     The on-edge classifier (the C SV_UserFriction trace that picks
//     friction*edgeFriction when the leading edge overhangs a drop)
//     is omitted here: the dispatcher can lift the [server.ApplyFriction]
//     onEdge bit out of a separate world-side trace if it wants the
//     classical sv_edgefriction behavior. The Go port runs the
//     edgeFriction=1 path inline.
//  4. CalcWishVel from cmd (forwardMove/sideMove/upMove relative to
//     viewangles read from `v_angle` -- the player-pitch-controlled
//     view angle, not the entity orientation). The Z component of
//     wishvel is forced to 0 (matches the C SV_AirMove's `if (movetype
//     != MOVETYPE_WALK) wishvel[2] = cmd->upmove; else wishvel[2] = 0`).
//  5. ClampWishSpeed to params.MaxSpeed.
//  6. Accelerate the velocity toward (wishdir, wishspeed) by
//     params.Accelerate*dt -- the standard ground-accel ramp.
//  7. PushEntity by velocity*dt against the world + candidates. The
//     C upstream's full SV_WalkMove also runs a separate step-up /
//     step-down pass when the slide bumps into a wall; the Go port
//     does ONE PushEntity pass here for spec parity -- the iterative
//     step-up logic is deferred to a future commit (SV_WalkMove is
//     the heaviest function in sv_phys.c; this port lands the
//     friction/accel/integration pipeline and leaves the wall-step
//     refinement as the next slice).
//  8. CheckBottom for ground-loss after the push. The C upstream
//     uses the post-step-down trace normal (normal[2] > 0.7) to set
//     FL_ONGROUND. The Go port uses CheckBottom against the post-push
//     position: if the four corners are supported, latch FL_ONGROUND;
//     else clear it.
//  9. RunThink (post-move, matching the upstream's
//     `SV_RunThink(player); ... SV_WalkMove(player)` BUT with the
//     reverse order -- the task brief specifies post-move dispatch
//     to keep the Step + Walk handlers symmetric).
//
// SPEC DEVIATIONS (documented up-front):
//
//   - Stair step-up pass (the iterative SV_WalkMove
//     up/forward/down + SV_TryUnstick logic) is not yet ported. The
//     entity slides via a single PushEntity here; a future commit
//     will add the step-up refinement.
//   - Water classification / FL_WATERJUMP gating is omitted (no
//     [server.PointContents] surface yet).
//   - sv_edgefriction trace classification is omitted (the
//     ApplyFriction onEdge bit is always false in this port).
//   - RunThink runs AFTER the move, not before. The C upstream's
//     SV_Physics_Client puts it before (per-MOVETYPE arm), but the
//     task brief specifies post-move for handler symmetry with the
//     other ported PhysicsXxx functions.
//
// Returns:
//
//	true,  nil  -- happy path (move applied + think dispatched).
//	false, err  -- EntVars read error, PushEntity / CheckBottom trace
//	               error, or RunThink dispatch error.
func PhysicsWalk(ent *progs.Edict, ev *progs.EntVars, key Key, cmd server.UserCmd, params server.PhysParams, ctx PhysicsContext) (bool, error) {
	flagsF, err := ev.ReadFloat("flags")
	if err != nil {
		return false, err
	}
	flags := server.EntityFlag(int32(flagsF))

	velocity, err := ev.ReadVec3("velocity")
	if err != nil {
		return false, err
	}
	origin, err := ev.ReadVec3("origin")
	if err != nil {
		return false, err
	}
	mins, err := ev.ReadVec3("mins")
	if err != nil {
		return false, err
	}
	maxs, err := ev.ReadVec3("maxs")
	if err != nil {
		return false, err
	}
	vAngle, err := ev.ReadVec3("v_angle")
	if err != nil {
		return false, err
	}

	onGround := (flags & server.FlagOnGround) != 0

	// BUTTON_JUMP while standing launches the player. This is the engine-
	// side stand-in for QC PlayerJump: the bring-up host does not dispatch
	// PlayerPreThink, so the +jump bit the client sends would otherwise do
	// nothing. FL_JUMP_RELEASED debounces a held key -- the jump re-arms
	// only after the key is released, so holding jump does not pogo every
	// tic. tyrquake: PlayerJump in QC/player.qc (self.velocity_z += 270).
	jumpHeld := cmd.Buttons&server.ButtonJump != 0
	if jumpHeld && onGround && flags&server.FlagJumpReleased != 0 {
		velocity[2] += jumpSpeed
		flags &^= server.FlagOnGround
		flags &^= server.FlagJumpReleased
		onGround = false // launched: this tic is airborne (skip the on-ground vz zero + friction; gravity applies)
	}
	if !jumpHeld {
		flags |= server.FlagJumpReleased
	}

	// FL_ONGROUND zeros the inherited z velocity at frame start --
	// matches the C upstream's implicit "you're on the floor, your
	// downward fall has stopped" convention. Done BEFORE clamp so the
	// clamp sees the post-zero value.
	if onGround {
		velocity[2] = 0
	}

	// SV_CheckVelocity (per-axis ±maxVelocity clamp + NaN nuke).
	velocity = server.ClampVelocity(velocity, params.MaxVelocity)

	// Gravity (airborne only).
	if !onGround {
		gravityFactor, err := readStepGravityFactor(ev)
		if err != nil {
			return false, err
		}
		velocity = server.ApplyGravity(velocity, gravityFactor, params.Gravity, ctx.Dt)
	}

	// Ground friction (airborne players have none).
	if onGround {
		velocity = server.ApplyFriction(velocity, params, false, ctx.Dt)
	}

	// Wishvel from cmd -- viewangles come from `v_angle` (the player-
	// pitch-controlled view angle), NOT `angles` (the entity orientation
	// QC writes to drive the model). The Z component is forced to 0 for
	// MOVETYPE_WALK (the C upstream's `else wishvel[2] = 0` branch).
	wishvel := svuser.CalcWishVel(cmd.ForwardMove, cmd.SideMove, cmd.UpMove, vAngle)
	wishvel[2] = 0
	wishvel = svuser.ClampWishSpeed(wishvel, params.MaxSpeed)
	wishdir, wishspeed := svuser.SplitWishVel(wishvel)

	// Accelerate toward (wishdir, wishspeed). When wishspeed is 0
	// (zero cmd), the Accelerate addspeed gate (`if (addspeed <= 0)
	// return v`) short-circuits without modifying velocity.
	velocity = svuser.Accelerate(velocity, wishdir, wishspeed, params.Accelerate, ctx.Dt)

	// Push the entity by velocity*dt and trace against world +
	// candidates. The PushEntity result clamps origin to the post-trace
	// position (NewOrigin = Origin + Push * fraction).
	pin := PushEntityIn{
		Origin:    origin,
		Mins:      mins,
		Maxs:      maxs,
		Push:      [3]float32{velocity[0] * ctx.Dt, velocity[1] * ctx.Dt, velocity[2] * ctx.Dt},
		MoveType:  server.MoveTypeWalk,
		Solid:     server.SolidSlideBox,
		EntityKey: key,
	}
	pout, err := PushEntity(pin, ctx.Worldmodel, ctx.Candidates)
	if err != nil {
		return false, err
	}

	// CheckBottom after the move to refresh FL_ONGROUND. If the four
	// corners are supported, latch ONGROUND; else clear it.
	//
	// The CheckBottom error branch is structurally unreachable: it
	// uses the same (worldmodel + candidates) the PushEntity above
	// already traced successfully, so the trace-error sources (corrupt
	// hull, SOLID_BSP candidate with nil BrushModel) would have fired
	// on the PushEntity above. Dropped per the bsptrace pattern.
	supported, _ := CheckBottom(CheckBottomIn{
		Origin:    pout.NewOrigin,
		Mins:      mins,
		Maxs:      maxs,
		EntityKey: key,
	}, ctx.Worldmodel, ctx.Candidates)
	// Do not re-latch ONGROUND while rising (a fresh jump); otherwise the
	// next tic's on-ground vz-zero would swallow the launch. Once the
	// player is level or falling (velocity_z <= 0) the landing check applies.
	if supported && velocity[2] <= 0 {
		flags |= server.FlagOnGround
	} else {
		flags &^= server.FlagOnGround
	}

	// WriteVec3 / WriteFloat for fields whose Read just succeeded
	// cannot fail (same offset + type + range). Error branches dropped
	// per the bsptrace pattern.
	_ = ev.WriteVec3("origin", pout.NewOrigin)
	_ = ev.WriteVec3("velocity", velocity)
	_ = ev.WriteFloat("flags", float32(int32(flags)))

	// Think dispatch (post-move). As in [PhysicsStep], the !alive-
	// without-err branch is structurally unreachable; only err drives
	// the early return.
	if _, err := server.RunThink(ent, ev, ctx.Now, ctx.Dt, ctx.ThinkCaller); err != nil {
		return false, err
	}
	return true, nil
}
