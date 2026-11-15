// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package progs

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
)

// VM is the QuakeC bytecode interpreter. tyrquake's pr_exec.c
// expresses the same state via globals (pr_globals, pr_stack,
// pr_depth, pr_xfunction, pr_xstatement); the Go port owns them as
// fields so multiple VMs can coexist for tests + savegame loading.
//
// Scope of THIS commit: arithmetic, comparison, logical,
// store-to-globals, jumps, DONE / RETURN, ENTER / LEAVE. CALL +
// STATE + STOREP / LOAD / ADDRESS (the entity-pointer arithmetic
// ops) are deferred to follow-ups -- they need a builtin function
// table + entity-byte-offset resolution that depend on subsystems
// not ported yet.
type VM struct {
	progs   *Progs
	arena   *EdictArena // edict pool, optional; required for LOAD_* / STORE_P_* / ADDRESS
	globals []byte      // writable copy of progs.Globals
	stack   []frame     // return-frame stack
	xFunc   int32       // currently-executing function index
	xStmt   int32       // currently-executing statement index
	runaway int         // runaway-loop budget (decremented per stmt)
	depth   int         // = len(stack); cached for readability
	// runtime parameter count carried across an OP_CALLn dispatch
	// (callee reads its arguments from OfsParm0..).
	argc int

	// builtins is the registry the OP_CALLn opcode dispatches into
	// when the callee's first_statement is negative.
	builtins map[int]Builtin

	// stateHooks are the two server callbacks OP_STATE needs.
	timeSource func() float32 // returns sv.time
	selfEdict  func() int32   // returns pr_global_struct->self (an edict pointer)

	// stateField{NextThink,Frame,Think} are the field-slot offsets
	// OP_STATE writes into the self edict. Set via
	// SetStateFieldOffsets so the upstream's hard-coded entvars_t
	// layout becomes a per-Progs lookup the embedder does once at
	// init time.
	stateFieldNextThink int
	stateFieldFrame     int
	stateFieldThink     int
	stateFieldsSet      bool

	// randomSource is the float-in-[0,1) callback BuiltinFnRandom
	// pulls from. nil means the random() builtin returns
	// ErrRandomNotSeeded.
	randomSource func() float32

	// particleSink is the side-effect closure BuiltinFnParticle hands
	// the four QC stack values to (origin, dir, color, count). nil
	// means BuiltinFnParticle silently swallows the call -- matches
	// the "side-effect builtin with no embedder wiring is a no-op"
	// convention the other builtins (setorigin, sound, ...) follow
	// in quake-tamago's noop registrations. The embedder wires this
	// via SetParticleSink to bridge QC particle() calls into
	// render.Pool.Emit (which holds the per-process 2048-slot pool).
	particleSink func(origin, dir [3]float32, color int, count int)
}

// SetArena wires an EdictArena into the VM so the LOAD_*, STORE_P_*,
// and ADDRESS opcodes can resolve entity pointers. The arena must
// have been built from the same Progs (or another Progs with the
// same EntityFields layout) -- the VM does not verify this.
func (vm *VM) SetArena(a *EdictArena) { vm.arena = a }

// Builtin is the signature every native function exposed to QuakeC
// implements. tyrquake's pr_builtins[] is an array of `void(void)`
// functions that read arguments from OfsParm* slots and write the
// result into OfsReturn. The Go port keeps the same shape but
// returns an error instead of calling PR_RunError directly.
type Builtin func(vm *VM) error

// RegisterBuiltin associates idx with fn. The QuakeC OP_CALLn
// opcode dispatches to the builtin at index `-function.FirstStatement`
// when first_statement is negative. idx 0 is reserved (the upstream
// pr_builtins[0] is "should never be called"); callers that pass 0
// get a no-op registration.
func (vm *VM) RegisterBuiltin(idx int, fn Builtin) {
	if idx <= 0 {
		return
	}
	if vm.builtins == nil {
		vm.builtins = make(map[int]Builtin)
	}
	vm.builtins[idx] = fn
}

// Argc returns the number of arguments the currently-executing
// builtin was called with (= the n in OP_CALLn). The builtin reads
// each argument from OfsParm0 + i*3.
func (vm *VM) Argc() int { return vm.argc }

// SetStateHooks wires the OP_STATE callbacks. timeSource returns
// the current server tic (the upstream's sv.time); selfEdict
// returns the QuakeC pointer to the "self" entity (the
// pr_global_struct->self value). Both MUST be set before any
// progs.dat code can execute OP_STATE; the absence of either
// surfaces as ErrNoStateHooks at dispatch time.
func (vm *VM) SetStateHooks(timeSource func() float32, selfEdict func() int32) {
	vm.timeSource = timeSource
	vm.selfEdict = selfEdict
}

// SetStateFieldOffsets tells the VM which entvars_t fields OP_STATE
// updates on the self edict: nextthink (float, scheduled think tic),
// frame (float, current animation frame), think (function, the next
// callback). The embedder typically looks these up once via
// (*Progs).FindField("nextthink") etc. and passes the .Ofs values
// here. Without a call, OP_STATE surfaces ErrNoStateHooks.
func (vm *VM) SetStateFieldOffsets(nextThink, frame, think int) {
	vm.stateFieldNextThink = nextThink
	vm.stateFieldFrame = frame
	vm.stateFieldThink = think
	vm.stateFieldsSet = true
}

// MaxStackDepth caps the EnterFunction stack. tyrquake's
// MAX_STACK_DEPTH is 32, which is enough for stock single-player QC
// but the shareware progs.dat's spawn-time chains (worldspawn ->
// precache_* fanout -> StartItem helpers etc.) can recurse deeper
// than 32 frames once the spawn-pass-fires-everything entry path is
// taken. 256 mirrors the FTE/QuakeForge-era headroom + leaves plenty
// of slack for community progs without breaking the runaway-loop
// guard's overall budget.
const MaxStackDepth = 256

// MaxLocalstack caps the per-call local slot saves; the value
// matches tyrquake's LOCALSTACK_SIZE (~2048 slots) but the Go port
// uses a growable slice so the cap is only enforced when the
// interpreter is in tracking-the-upstream mode.
const MaxLocalstack = 2048

// MaxRunaway is the runaway-loop guard count. tyrquake uses
// 1,000,000 as the default; the Go port exposes it for tests that
// want to detect infinite loops on a tighter budget.
const MaxRunaway = 1000000

type frame struct {
	stmt int32 // pre-call statement index
	fn   int32 // pre-call function index
	// localStackSize is the size of the locals slice the callee
	// can pop on return. Used by LeaveFunction to restore the
	// pre-call locals.
	localStackSize int
	// savedLocals are the local-slot values the callee will
	// stomp; restored by LeaveFunction.
	savedLocals []byte
}

// VM-specific sentinels.
var (
	ErrRunaway          = errors.New("progs: VM: runaway loop budget exceeded")
	ErrBadFunctionIndex = errors.New("progs: VM: function index out of range")
	ErrStackOverflow    = errors.New("progs: VM: enter-function stack overflow")
	ErrStackUnderflow   = errors.New("progs: VM: leave-function stack underflow")
	ErrUnsupportedOp    = errors.New("progs: VM: opcode not implemented in this build")
	ErrBadStatement     = errors.New("progs: VM: statement index out of range")
	ErrGlobalOffset     = errors.New("progs: VM: global-pool offset out of range")
	ErrDivByZero        = errors.New("progs: VM: division by zero")
	ErrNoArena          = errors.New("progs: VM: entity-pointer opcode but no arena attached (call SetArena)")
	ErrNullCall         = errors.New("progs: VM: OP_CALLn target function index is 0 (null function)")
	ErrBadBuiltin       = errors.New("progs: VM: OP_CALLn target builtin index not registered")
	ErrNoStateHooks     = errors.New("progs: VM: OP_STATE needs SetStateHooks + SetStateFieldOffsets")
	ErrBadFunction      = errors.New("progs: VM: function entry out of range (locals, parameters or parm_start)")
)

// NewVM returns an interpreter ready to Run functions in p. The VM
// owns a copy of p.Globals so concurrent VMs over the same Progs
// stay isolated.
func NewVM(p *Progs) *VM {
	if p == nil {
		panic("progs: NewVM: nil progs")
	}
	g := make([]byte, len(p.Globals))
	copy(g, p.Globals)
	return &VM{
		progs:   p,
		globals: g,
		stack:   make([]frame, 0, MaxStackDepth),
	}
}

// Globals returns the writable global-pool byte slice. Tests use
// this to seed pre-condition values + assert post-conditions. The
// returned slice IS the live storage; mutations persist.
func (vm *VM) Globals() []byte { return vm.globals }

// Progs returns the immutable [Progs] backing this VM. Builtins that
// need to resolve string offsets (PF_precache_model / PF_setmodel
// read OFS_PARM* int32 string_t values out of the QC string table)
// reach for this rather than carrying a separate handle. tyrquake:
// the implicit `progs` global the C upstream accesses via
// pr_strings + pr_functions etc.
func (vm *VM) Progs() *Progs { return vm.progs }

// String resolves the NUL-terminated string at the given offset in
// the QC string table. Convenience wrapper around vm.Progs().String
// for builtins reading OFS_PARM* string_t values without holding a
// separate Progs handle. tyrquake: PR_GetString.
func (vm *VM) String(off int32) string { return vm.progs.String(off) }

// Arena returns the EdictArena attached via SetArena (or nil when
// none is wired). Builtins that take an `entity` argument (setmodel,
// setorigin, setsize) read the OFS_PARM0 int32 as a QC entity-
// pointer + need the arena to resolve it back to an *Edict.
// tyrquake: the implicit sv.edicts pool the entity-pointer macros
// (EDICT_NUM / NUM_FOR_EDICT) walk.
func (vm *VM) Arena() *EdictArena { return vm.arena }

// GlobalFloat reads the float at slot ofs (ofs is a 32-bit-slot
// index, not a byte offset). Returns ErrGlobalOffset if ofs falls
// outside the pool. tyrquake: G_FLOAT macro.
func (vm *VM) GlobalFloat(ofs int) (float32, error) {
	b := ofs * globalSlotSize
	if ofs < 0 || b+4 > len(vm.globals) {
		return 0, ErrGlobalOffset
	}
	return math.Float32frombits(binary.LittleEndian.Uint32(vm.globals[b : b+4])), nil
}

// SetGlobalFloat is the corresponding writer.
func (vm *VM) SetGlobalFloat(ofs int, v float32) error {
	b := ofs * globalSlotSize
	if ofs < 0 || b+4 > len(vm.globals) {
		return ErrGlobalOffset
	}
	binary.LittleEndian.PutUint32(vm.globals[b:b+4], math.Float32bits(v))
	return nil
}

// GlobalInt reads the int32 at slot ofs.
func (vm *VM) GlobalInt(ofs int) (int32, error) {
	b := ofs * globalSlotSize
	if ofs < 0 || b+4 > len(vm.globals) {
		return 0, ErrGlobalOffset
	}
	return int32(binary.LittleEndian.Uint32(vm.globals[b : b+4])), nil
}

// SetGlobalInt is the corresponding writer.
func (vm *VM) SetGlobalInt(ofs int, v int32) error {
	b := ofs * globalSlotSize
	if ofs < 0 || b+4 > len(vm.globals) {
		return ErrGlobalOffset
	}
	binary.LittleEndian.PutUint32(vm.globals[b:b+4], uint32(v))
	return nil
}

// GlobalVector reads the 3-component vector starting at slot ofs.
func (vm *VM) GlobalVector(ofs int) ([3]float32, error) {
	b := ofs * globalSlotSize
	if ofs < 0 || b+12 > len(vm.globals) {
		return [3]float32{}, ErrGlobalOffset
	}
	return [3]float32{
		math.Float32frombits(binary.LittleEndian.Uint32(vm.globals[b : b+4])),
		math.Float32frombits(binary.LittleEndian.Uint32(vm.globals[b+4 : b+8])),
		math.Float32frombits(binary.LittleEndian.Uint32(vm.globals[b+8 : b+12])),
	}, nil
}

// SetGlobalVector writes a 3-component vector starting at slot ofs.
func (vm *VM) SetGlobalVector(ofs int, v [3]float32) error {
	b := ofs * globalSlotSize
	if ofs < 0 || b+12 > len(vm.globals) {
		return ErrGlobalOffset
	}
	binary.LittleEndian.PutUint32(vm.globals[b:b+4], math.Float32bits(v[0]))
	binary.LittleEndian.PutUint32(vm.globals[b+4:b+8], math.Float32bits(v[1]))
	binary.LittleEndian.PutUint32(vm.globals[b+8:b+12], math.Float32bits(v[2]))
	return nil
}

// XFunction / XStatement / Depth expose the interpreter's
// runtime location for tests + debug dumps.
func (vm *VM) XFunction() int32  { return vm.xFunc }
func (vm *VM) XStatement() int32 { return vm.xStmt }
func (vm *VM) Depth() int        { return vm.depth }

// Reset clears the per-execution state -- the return-frame stack,
// the depth/xFunc/xStmt cursors, the runaway-loop budget, and the
// carried builtin arg count. Globals, builtins, arena, and state
// hooks survive. Useful after a Run() that returned an error left
// the VM in a non-zero depth, or between tic boundaries where the
// embedder wants to guarantee a fresh-start posture even if a
// previous tic mis-exited.
//
// tyrquake: no direct analogue -- the upstream relies on
// PR_ExecuteProgram never returning early, but the Go port surfaces
// errors so callers need an explicit clear.
func (vm *VM) Reset() {
	vm.stack = vm.stack[:0]
	vm.depth = 0
	vm.xFunc = 0
	vm.xStmt = 0
	vm.runaway = 0
	vm.argc = 0
}

// Run executes the function at index fn until OP_DONE / OP_RETURN
// pops the frame the call entered. Function 0 is the empty-function
// slot (tyrquake reserves it for "null function") and is rejected.
// tyrquake: PR_ExecuteProgram.
func (vm *VM) Run(fn int32) error {
	if fn <= 0 || int(fn) >= len(vm.progs.Functions) {
		return ErrBadFunctionIndex
	}
	exitDepth := vm.depth
	vm.runaway = MaxRunaway

	if err := vm.enterFunction(fn); err != nil {
		return err
	}

	for {
		if vm.runaway--; vm.runaway < 0 {
			return ErrRunaway
		}
		vm.xStmt++
		if vm.xStmt < 0 || int(vm.xStmt) >= len(vm.progs.Statements) {
			return ErrBadStatement
		}
		st := vm.progs.Statements[vm.xStmt]
		// pr_xfunction->profile++ -- skipped (profile is debug only)

		done, err := vm.execOne(st, exitDepth)
		if err != nil {
			return err
		}
		if done {
			return nil
		}
	}
}

// execOne runs one statement and reports (allDone, error).
func (vm *VM) execOne(st Statement, exitDepth int) (bool, error) {
	switch st.Op {

	// --- arithmetic ----------------------------------------------------------

	case OP_ADD_F:
		a, _ := vm.GlobalFloat(int(uint16(st.A)))
		b, _ := vm.GlobalFloat(int(uint16(st.B)))
		return false, vm.SetGlobalFloat(int(uint16(st.C)), a+b)
	case OP_ADD_V:
		a, _ := vm.GlobalVector(int(uint16(st.A)))
		b, _ := vm.GlobalVector(int(uint16(st.B)))
		return false, vm.SetGlobalVector(int(uint16(st.C)),
			[3]float32{a[0] + b[0], a[1] + b[1], a[2] + b[2]})
	case OP_SUB_F:
		a, _ := vm.GlobalFloat(int(uint16(st.A)))
		b, _ := vm.GlobalFloat(int(uint16(st.B)))
		return false, vm.SetGlobalFloat(int(uint16(st.C)), a-b)
	case OP_SUB_V:
		a, _ := vm.GlobalVector(int(uint16(st.A)))
		b, _ := vm.GlobalVector(int(uint16(st.B)))
		return false, vm.SetGlobalVector(int(uint16(st.C)),
			[3]float32{a[0] - b[0], a[1] - b[1], a[2] - b[2]})
	case OP_MUL_F:
		a, _ := vm.GlobalFloat(int(uint16(st.A)))
		b, _ := vm.GlobalFloat(int(uint16(st.B)))
		return false, vm.SetGlobalFloat(int(uint16(st.C)), a*b)
	case OP_MUL_V:
		// vector dot product, returns float into C
		a, _ := vm.GlobalVector(int(uint16(st.A)))
		b, _ := vm.GlobalVector(int(uint16(st.B)))
		return false, vm.SetGlobalFloat(int(uint16(st.C)), a[0]*b[0]+a[1]*b[1]+a[2]*b[2])
	case OP_MUL_FV:
		a, _ := vm.GlobalFloat(int(uint16(st.A)))
		b, _ := vm.GlobalVector(int(uint16(st.B)))
		return false, vm.SetGlobalVector(int(uint16(st.C)),
			[3]float32{a * b[0], a * b[1], a * b[2]})
	case OP_MUL_VF:
		a, _ := vm.GlobalVector(int(uint16(st.A)))
		b, _ := vm.GlobalFloat(int(uint16(st.B)))
		return false, vm.SetGlobalVector(int(uint16(st.C)),
			[3]float32{b * a[0], b * a[1], b * a[2]})
	case OP_DIV_F:
		a, _ := vm.GlobalFloat(int(uint16(st.A)))
		b, _ := vm.GlobalFloat(int(uint16(st.B)))
		if b == 0 {
			return false, ErrDivByZero
		}
		return false, vm.SetGlobalFloat(int(uint16(st.C)), a/b)
	case OP_BITAND:
		a, _ := vm.GlobalFloat(int(uint16(st.A)))
		b, _ := vm.GlobalFloat(int(uint16(st.B)))
		return false, vm.SetGlobalFloat(int(uint16(st.C)), float32(int32(a)&int32(b)))
	case OP_BITOR:
		a, _ := vm.GlobalFloat(int(uint16(st.A)))
		b, _ := vm.GlobalFloat(int(uint16(st.B)))
		return false, vm.SetGlobalFloat(int(uint16(st.C)), float32(int32(a)|int32(b)))

	// --- comparison ----------------------------------------------------------

	case OP_GE, OP_LE, OP_GT, OP_LT:
		a, _ := vm.GlobalFloat(int(uint16(st.A)))
		b, _ := vm.GlobalFloat(int(uint16(st.B)))
		var r bool
		switch st.Op {
		case OP_GE:
			r = a >= b
		case OP_LE:
			r = a <= b
		case OP_GT:
			r = a > b
		case OP_LT:
			r = a < b
		}
		return false, vm.SetGlobalFloat(int(uint16(st.C)), boolToFloat(r))
	case OP_AND:
		a, _ := vm.GlobalFloat(int(uint16(st.A)))
		b, _ := vm.GlobalFloat(int(uint16(st.B)))
		return false, vm.SetGlobalFloat(int(uint16(st.C)), boolToFloat(a != 0 && b != 0))
	case OP_OR:
		a, _ := vm.GlobalFloat(int(uint16(st.A)))
		b, _ := vm.GlobalFloat(int(uint16(st.B)))
		return false, vm.SetGlobalFloat(int(uint16(st.C)), boolToFloat(a != 0 || b != 0))
	case OP_EQ_F:
		a, _ := vm.GlobalFloat(int(uint16(st.A)))
		b, _ := vm.GlobalFloat(int(uint16(st.B)))
		return false, vm.SetGlobalFloat(int(uint16(st.C)), boolToFloat(a == b))
	case OP_EQ_V:
		a, _ := vm.GlobalVector(int(uint16(st.A)))
		b, _ := vm.GlobalVector(int(uint16(st.B)))
		return false, vm.SetGlobalFloat(int(uint16(st.C)),
			boolToFloat(a == b))
	case OP_EQ_S:
		a, _ := vm.GlobalInt(int(uint16(st.A)))
		b, _ := vm.GlobalInt(int(uint16(st.B)))
		return false, vm.SetGlobalFloat(int(uint16(st.C)),
			boolToFloat(vm.progs.String(a) == vm.progs.String(b)))
	case OP_EQ_E, OP_EQ_FNC:
		a, _ := vm.GlobalInt(int(uint16(st.A)))
		b, _ := vm.GlobalInt(int(uint16(st.B)))
		return false, vm.SetGlobalFloat(int(uint16(st.C)), boolToFloat(a == b))
	case OP_NE_F:
		a, _ := vm.GlobalFloat(int(uint16(st.A)))
		b, _ := vm.GlobalFloat(int(uint16(st.B)))
		return false, vm.SetGlobalFloat(int(uint16(st.C)), boolToFloat(a != b))
	case OP_NE_V:
		a, _ := vm.GlobalVector(int(uint16(st.A)))
		b, _ := vm.GlobalVector(int(uint16(st.B)))
		return false, vm.SetGlobalFloat(int(uint16(st.C)), boolToFloat(a != b))
	case OP_NE_S:
		a, _ := vm.GlobalInt(int(uint16(st.A)))
		b, _ := vm.GlobalInt(int(uint16(st.B)))
		return false, vm.SetGlobalFloat(int(uint16(st.C)),
			boolToFloat(vm.progs.String(a) != vm.progs.String(b)))
	case OP_NE_E, OP_NE_FNC:
		a, _ := vm.GlobalInt(int(uint16(st.A)))
		b, _ := vm.GlobalInt(int(uint16(st.B)))
		return false, vm.SetGlobalFloat(int(uint16(st.C)), boolToFloat(a != b))

	// --- logical NOT ---------------------------------------------------------

	case OP_NOT_F:
		a, _ := vm.GlobalFloat(int(uint16(st.A)))
		return false, vm.SetGlobalFloat(int(uint16(st.C)), boolToFloat(a == 0))
	case OP_NOT_V:
		a, _ := vm.GlobalVector(int(uint16(st.A)))
		return false, vm.SetGlobalFloat(int(uint16(st.C)),
			boolToFloat(a[0] == 0 && a[1] == 0 && a[2] == 0))
	case OP_NOT_S:
		a, _ := vm.GlobalInt(int(uint16(st.A)))
		s := vm.progs.String(a)
		return false, vm.SetGlobalFloat(int(uint16(st.C)), boolToFloat(a == 0 || s == ""))
	case OP_NOT_FNC, OP_NOT_ENT:
		a, _ := vm.GlobalInt(int(uint16(st.A)))
		return false, vm.SetGlobalFloat(int(uint16(st.C)), boolToFloat(a == 0))

	// --- store-to-globals ----------------------------------------------------

	case OP_STORE_F, OP_STORE_S, OP_STORE_ENT, OP_STORE_FLD, OP_STORE_FNC:
		// Single-slot copy from A to B (the int32 form covers
		// float / string / ent / field / function; the byte
		// pattern is the same).
		v, _ := vm.GlobalInt(int(uint16(st.A)))
		return false, vm.SetGlobalInt(int(uint16(st.B)), v)
	case OP_STORE_V:
		v, _ := vm.GlobalVector(int(uint16(st.A)))
		return false, vm.SetGlobalVector(int(uint16(st.B)), v)

	// --- jumps ---------------------------------------------------------------

	case OP_IFNOT:
		a, _ := vm.GlobalInt(int(uint16(st.A)))
		if a == 0 {
			vm.xStmt += int32(st.B) - 1
		}
		return false, nil
	case OP_IF:
		a, _ := vm.GlobalInt(int(uint16(st.A)))
		if a != 0 {
			vm.xStmt += int32(st.B) - 1
		}
		return false, nil
	case OP_GOTO:
		vm.xStmt += int32(st.A) - 1
		return false, nil

	// --- function control ----------------------------------------------------

	case OP_DONE, OP_RETURN:
		// Copy A..A+2 into OfsReturn..OfsReturn+2 (a vector copy
		// even for scalar returns -- the pool layout reserves the
		// slot triple).
		v, _ := vm.GlobalVector(int(uint16(st.A)))
		if err := vm.SetGlobalVector(OfsReturn, v); err != nil {
			return false, err
		}
		if err := vm.leaveFunction(); err != nil {
			return false, err
		}
		return vm.depth == exitDepth, nil

	// --- entity-pointer ops (need an arena attached via SetArena) -----------

	case OP_ADDRESS:
		if vm.arena == nil {
			return false, ErrNoArena
		}
		edPtr, _ := vm.GlobalInt(int(uint16(st.A)))
		slotOfs, _ := vm.GlobalInt(int(uint16(st.B)))
		ed, _, err := vm.arena.ResolvePointer(edPtr)
		if err != nil {
			return false, err
		}
		idx := vm.arena.NumFor(ed)
		// Result is the byte offset to (edict origin) + (slotOfs * 4).
		out := vm.arena.MakePointer(idx, int(slotOfs))
		return false, vm.SetGlobalInt(int(uint16(st.C)), out)

	case OP_LOAD_F, OP_LOAD_FLD, OP_LOAD_ENT, OP_LOAD_S, OP_LOAD_FNC:
		if vm.arena == nil {
			return false, ErrNoArena
		}
		edPtr, _ := vm.GlobalInt(int(uint16(st.A)))
		slotOfs, _ := vm.GlobalInt(int(uint16(st.B)))
		ed, _, err := vm.arena.ResolvePointer(edPtr)
		if err != nil {
			return false, err
		}
		v, err := ed.FieldInt(int(slotOfs))
		if err != nil {
			return false, err
		}
		return false, vm.SetGlobalInt(int(uint16(st.C)), v)

	case OP_LOAD_V:
		if vm.arena == nil {
			return false, ErrNoArena
		}
		edPtr, _ := vm.GlobalInt(int(uint16(st.A)))
		slotOfs, _ := vm.GlobalInt(int(uint16(st.B)))
		ed, _, err := vm.arena.ResolvePointer(edPtr)
		if err != nil {
			return false, err
		}
		v, err := ed.FieldVector(int(slotOfs))
		if err != nil {
			return false, err
		}
		return false, vm.SetGlobalVector(int(uint16(st.C)), v)

	case OP_STOREP_F, OP_STOREP_S, OP_STOREP_ENT, OP_STOREP_FLD, OP_STOREP_FNC:
		if vm.arena == nil {
			return false, ErrNoArena
		}
		v, _ := vm.GlobalInt(int(uint16(st.A)))
		ptr, _ := vm.GlobalInt(int(uint16(st.B)))
		ed, fieldByteOfs, err := vm.arena.ResolvePointer(ptr)
		if err != nil {
			return false, err
		}
		if fieldByteOfs%globalSlotSize != 0 {
			return false, ErrFieldOffset
		}
		return false, ed.FieldSetInt(fieldByteOfs/globalSlotSize, v)

	case OP_STOREP_V:
		if vm.arena == nil {
			return false, ErrNoArena
		}
		v, _ := vm.GlobalVector(int(uint16(st.A)))
		ptr, _ := vm.GlobalInt(int(uint16(st.B)))
		ed, fieldByteOfs, err := vm.arena.ResolvePointer(ptr)
		if err != nil {
			return false, err
		}
		if fieldByteOfs%globalSlotSize != 0 {
			return false, ErrFieldOffset
		}
		return false, ed.FieldSetVector(fieldByteOfs/globalSlotSize, v)

	// --- function call -------------------------------------------------------

	case OP_CALL0, OP_CALL1, OP_CALL2, OP_CALL3, OP_CALL4,
		OP_CALL5, OP_CALL6, OP_CALL7, OP_CALL8:
		argc := int(st.Op - OP_CALL0)
		fnIdx, _ := vm.GlobalInt(int(uint16(st.A)))
		if fnIdx == 0 {
			return false, ErrNullCall
		}
		if fnIdx < 0 || int(fnIdx) >= len(vm.progs.Functions) {
			return false, ErrBadFunctionIndex
		}
		callee := &vm.progs.Functions[fnIdx]
		vm.argc = argc
		if callee.FirstStatement < 0 {
			// Negative => builtin.
			bidx := int(-callee.FirstStatement)
			fn, ok := vm.builtins[bidx]
			if !ok {
				return false, fmt.Errorf("%w: %d", ErrBadBuiltin, bidx)
			}
			return false, fn(vm)
		}
		// QuakeC function -- enter it; the dispatch loop's xStmt++
		// continues from FirstStatement-1.
		return false, vm.enterFunction(fnIdx)

	// --- OP_STATE ------------------------------------------------------------

	case OP_STATE:
		if vm.arena == nil {
			return false, ErrNoArena
		}
		if vm.timeSource == nil || vm.selfEdict == nil || !vm.stateFieldsSet {
			return false, ErrNoStateHooks
		}
		ed, _, err := vm.arena.ResolvePointer(vm.selfEdict())
		if err != nil {
			return false, err
		}
		frame, _ := vm.GlobalFloat(int(uint16(st.A)))
		think, _ := vm.GlobalInt(int(uint16(st.B)))
		if err := ed.FieldSetFloat(vm.stateFieldNextThink, vm.timeSource()+0.1); err != nil {
			return false, err
		}
		if err := ed.FieldSetFloat(vm.stateFieldFrame, frame); err != nil {
			return false, err
		}
		if err := ed.FieldSetInt(vm.stateFieldThink, think); err != nil {
			return false, err
		}
		return false, nil

	default:
		return false, fmt.Errorf("%w: %d", ErrUnsupportedOp, st.Op)
	}
}

// enterFunction pushes the current (xFunc, xStmt) onto the return
// stack, saves the callee's locals, copies parameters from the
// OfsParm* slots into the callee's parm_start, and returns the new
// xStmt (= first_statement - 1; the dispatch loop's ++ undoes the -1).
// tyrquake: PR_EnterFunction.
func (vm *VM) enterFunction(fn int32) error {
	if vm.depth >= MaxStackDepth {
		return ErrStackOverflow
	}
	f := &vm.progs.Functions[fn]

	// The function table is the file's: its locals, parm_start and parameter
	// count size the save below and index ParmSize, so they are checked first (a
	// negative count panicked in make, 2 G locals asked for 8 GiB per call, a
	// parm_start past the globals panicked in leaveFunction, and NumParms above
	// MaxParms indexed past ParmSize; found by FuzzLoad).
	if f.Locals < 0 || f.Locals > MaxLocalstack || f.NumParms < 0 || f.NumParms > MaxParms ||
		(f.Locals > 0 && (f.ParmStart < 0 || (int(f.ParmStart)+int(f.Locals))*globalSlotSize > len(vm.globals))) {
		return fmt.Errorf("%w: function %d (locals %d, parm_start %d, %d parms, %d globals)", ErrBadFunction, fn, f.Locals, f.ParmStart, f.NumParms, len(vm.globals)/globalSlotSize)
	}

	// Save callee's locals so a recursive call doesn't clobber them.
	localBytes := int(f.Locals) * globalSlotSize
	saved := make([]byte, localBytes)
	if localBytes > 0 {
		copy(saved, vm.globals[int(f.ParmStart)*globalSlotSize:int(f.ParmStart)*globalSlotSize+localBytes])
	}

	vm.stack = append(vm.stack, frame{
		stmt:           vm.xStmt,
		fn:             vm.xFunc,
		localStackSize: localBytes,
		savedLocals:    saved,
	})
	vm.depth = len(vm.stack)

	// Copy parameters: parm i is at OfsParm0 + 3*i in the global pool,
	// and goes to parm_start + (cumulative parm_size so far) in the
	// callee's local slot range.
	dst := int(f.ParmStart)
	for i := int32(0); i < f.NumParms; i++ {
		for j := byte(0); j < f.ParmSize[i]; j++ {
			src := OfsParm0 + int(i)*3 + int(j)
			v, err := vm.GlobalInt(src)
			if err != nil {
				return err
			}
			if err := vm.SetGlobalInt(dst, v); err != nil {
				return err
			}
			dst++
		}
	}

	vm.xFunc = fn
	vm.xStmt = f.FirstStatement - 1
	return nil
}

// leaveFunction pops the return stack, restores the callee's locals,
// and returns. tyrquake: PR_LeaveFunction.
func (vm *VM) leaveFunction() error {
	if vm.depth <= 0 {
		return ErrStackUnderflow
	}
	top := vm.stack[len(vm.stack)-1]
	vm.stack = vm.stack[:len(vm.stack)-1]
	vm.depth = len(vm.stack)

	// Restore locals into the callee's parm_start slot range.
	f := &vm.progs.Functions[vm.xFunc]
	if top.localStackSize > 0 {
		copy(vm.globals[int(f.ParmStart)*globalSlotSize:int(f.ParmStart)*globalSlotSize+top.localStackSize], top.savedLocals)
	}

	vm.xFunc = top.fn
	vm.xStmt = top.stmt
	return nil
}

// boolToFloat mirrors the C `(int)(boolean expression)` idiom: 1.0
// for true, 0.0 for false. tyrquake stores the result of every
// comparison opcode this way.
func boolToFloat(b bool) float32 {
	if b {
		return 1
	}
	return 0
}
