// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package host

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"testing"

	"github.com/go-quake1/engine/bspfile"
	"github.com/go-quake1/engine/model"
	"github.com/go-quake1/engine/progs"
	"github.com/go-quake1/engine/protocol"
	"github.com/go-quake1/engine/server"
	"github.com/go-quake1/engine/sizebuf"
)

// --- test fixtures --------------------------------------------------------

// addStr appends s + a trailing NUL to strs and returns the offset s
// was written at. The first byte stays the upstream's empty-string
// sentinel (offset 0 is reserved).
func addStr(strs *[]byte, s string) int32 {
	ofs := int32(len(*strs))
	*strs = append(*strs, []byte(s)...)
	*strs = append(*strs, 0)
	return ofs
}

// progsForHost builds a Progs stub with:
//   - "classname" + "origin" fields (SpawnServer's spawn pass uses them)
//   - "movetype" + "solid" fields (RunPhysics reads them per-edict)
//   - "nextthink" + "think" fields (RunThink reads them)
//   - "self" + "other" + "time" globals (the thinkCaller bridge writes them)
//   - Functions[0] = null + Functions[1] = a "store 42 to OFS_RETURN" body
//
// EntityFields=8 gives 32 bytes per edict -- enough for the 6 fields
// we define here at distinct offsets.
func progsForHost() *progs.Progs {
	strs := []byte{0}
	classnameName := addStr(&strs, "classname")
	originName := addStr(&strs, "origin")
	movetypeName := addStr(&strs, "movetype")
	solidName := addStr(&strs, "solid")
	nextthinkName := addStr(&strs, "nextthink")
	thinkName := addStr(&strs, "think")
	selfName := addStr(&strs, "self")
	otherName := addStr(&strs, "other")
	timeName := addStr(&strs, "time")
	constName := addStr(&strs, "k42")

	// Globals: pool needs to be large enough for OfsReturn (1..3) +
	// the named globals' slots + a constant slot holding 42.0.
	const numGlobals = 64
	globals := make([]byte, numGlobals*4)
	const k42Slot = 30
	binary.LittleEndian.PutUint32(globals[k42Slot*4:k42Slot*4+4], math.Float32bits(42))

	return &progs.Progs{
		Header:  progs.Header{EntityFields: 8},
		Strings: strs,
		FieldDefs: []progs.Def{
			{Type: uint16(progs.EvString), Ofs: 1, SName: classnameName},
			{Type: uint16(progs.EvVector), Ofs: 2, SName: originName},
			{Type: uint16(progs.EvFloat), Ofs: 5, SName: movetypeName},
			{Type: uint16(progs.EvFloat), Ofs: 6, SName: solidName},
			{Type: uint16(progs.EvFloat), Ofs: 7, SName: nextthinkName},
			{Type: uint16(progs.EvFunction), Ofs: 0, SName: thinkName},
		},
		GlobalDefs: []progs.Def{
			{Type: uint16(progs.EvEntity), Ofs: 40, SName: selfName},
			{Type: uint16(progs.EvEntity), Ofs: 41, SName: otherName},
			{Type: uint16(progs.EvFloat), Ofs: 42, SName: timeName},
			{Type: uint16(progs.EvFloat), Ofs: k42Slot, SName: constName},
		},
		Globals: globals,
		// Functions: index 0 is the null slot; index 1 returns the
		// k42 constant via OP_RETURN (which copies A..A+2 into
		// OfsReturn). Statement layout: Statements[0]=OP_DONE (the
		// pre-roll the runner skips), Statements[1] = OP_RETURN with
		// A = k42Slot.
		Statements: []progs.Statement{
			{Op: progs.OP_DONE},
			{Op: progs.OP_RETURN, A: int16(k42Slot)},
		},
		Functions: []progs.Function{
			{FirstStatement: 0, SName: 0},
			{FirstStatement: 1, SName: 0, NumParms: 0, Locals: 0, ParmStart: 0},
		},
	}
}

// resolverFor returns a FileResolver that hands back data for any
// requested name.
func resolverFor(data []byte) server.FileResolver {
	return func(name string) (int64, io.ReaderAt, error) {
		return int64(len(data)), bytes.NewReader(data), nil
	}
}

// buildHostBSP synthesises a minimal valid Q1 BSP for SpawnServer
// tests. Copy of server/spawnserver_test.go's buildSpawnBSP --
// kept local to keep the host package independent of the server's
// test helpers.
func buildHostBSP(t *testing.T, entityBlob string, modelCount int) []byte {
	t.Helper()
	if modelCount < 1 {
		modelCount = 1
	}

	planes := []bspfile.Plane{
		{Normal: [3]float32{1, 0, 0}, Dist: 0, Type: bspfile.PlaneX},
	}
	nodes := []bspfile.Node{
		{PlaneNum: 0, Children: [2]int16{^int16(0), ^int16(1)}},
	}
	leafs := []bspfile.Leaf{
		{Contents: bspfile.ContentsEmpty},
		{Contents: bspfile.ContentsSolid},
	}
	clipnodes := []bspfile.ClipNode{
		{PlaneNum: 0, Children: [2]int16{bspfile.ContentsEmpty, bspfile.ContentsSolid}},
	}
	models := make([]bspfile.Model, modelCount)
	for i := range models {
		models[i] = bspfile.Model{
			Mins:     [3]float32{-100, -100, -100},
			Maxs:     [3]float32{100, 100, 100},
			Headnode: [bspfile.MaxMapHulls]int32{0, 0, 0, 0},
		}
	}

	pb := &bytes.Buffer{}
	for _, p := range planes {
		_ = binary.Write(pb, binary.LittleEndian, p.Normal[0])
		_ = binary.Write(pb, binary.LittleEndian, p.Normal[1])
		_ = binary.Write(pb, binary.LittleEndian, p.Normal[2])
		_ = binary.Write(pb, binary.LittleEndian, p.Dist)
		_ = binary.Write(pb, binary.LittleEndian, p.Type)
	}
	nb := &bytes.Buffer{}
	for _, n := range nodes {
		_ = binary.Write(nb, binary.LittleEndian, n.PlaneNum)
		_ = binary.Write(nb, binary.LittleEndian, n.Children[0])
		_ = binary.Write(nb, binary.LittleEndian, n.Children[1])
		for j := 0; j < 3; j++ {
			_ = binary.Write(nb, binary.LittleEndian, n.Mins[j])
		}
		for j := 0; j < 3; j++ {
			_ = binary.Write(nb, binary.LittleEndian, n.Maxs[j])
		}
		_ = binary.Write(nb, binary.LittleEndian, n.FirstFace)
		_ = binary.Write(nb, binary.LittleEndian, n.NumFaces)
	}
	lb := &bytes.Buffer{}
	for _, l := range leafs {
		_ = binary.Write(lb, binary.LittleEndian, l.Contents)
		_ = binary.Write(lb, binary.LittleEndian, l.VisOfs)
		for j := 0; j < 3; j++ {
			_ = binary.Write(lb, binary.LittleEndian, l.Mins[j])
		}
		for j := 0; j < 3; j++ {
			_ = binary.Write(lb, binary.LittleEndian, l.Maxs[j])
		}
		_ = binary.Write(lb, binary.LittleEndian, l.FirstMarkSurface)
		_ = binary.Write(lb, binary.LittleEndian, l.NumMarkSurfaces)
		lb.Write(l.AmbientLevel[:])
	}
	cnb := &bytes.Buffer{}
	for _, c := range clipnodes {
		_ = binary.Write(cnb, binary.LittleEndian, c.PlaneNum)
		_ = binary.Write(cnb, binary.LittleEndian, c.Children[0])
		_ = binary.Write(cnb, binary.LittleEndian, c.Children[1])
	}
	mb := &bytes.Buffer{}
	for _, m := range models {
		for j := 0; j < 3; j++ {
			_ = binary.Write(mb, binary.LittleEndian, m.Mins[j])
		}
		for j := 0; j < 3; j++ {
			_ = binary.Write(mb, binary.LittleEndian, m.Maxs[j])
		}
		for j := 0; j < 3; j++ {
			_ = binary.Write(mb, binary.LittleEndian, m.Origin[j])
		}
		for j := 0; j < bspfile.MaxMapHulls; j++ {
			_ = binary.Write(mb, binary.LittleEndian, m.Headnode[j])
		}
		_ = binary.Write(mb, binary.LittleEndian, m.VisLeafs)
		_ = binary.Write(mb, binary.LittleEndian, m.FirstFace)
		_ = binary.Write(mb, binary.LittleEndian, m.NumFaces)
	}

	const headerSize = 4 + 15*8
	body := &bytes.Buffer{}

	type lumpInfo struct {
		kind   bspfile.LumpKind
		data   []byte
		offset int32
	}
	entBytes := append([]byte(entityBlob), 0)
	lumps := []lumpInfo{
		{kind: bspfile.LumpEntities, data: entBytes},
		{kind: bspfile.LumpPlanes, data: pb.Bytes()},
		{kind: bspfile.LumpNodes, data: nb.Bytes()},
		{kind: bspfile.LumpLeafs, data: lb.Bytes()},
		{kind: bspfile.LumpClipnodes, data: cnb.Bytes()},
		{kind: bspfile.LumpModels, data: mb.Bytes()},
	}
	offsetByKind := map[bspfile.LumpKind]int32{}
	lenByKind := map[bspfile.LumpKind]int32{}
	for i := range lumps {
		lumps[i].offset = int32(headerSize) + int32(body.Len())
		body.Write(lumps[i].data)
		offsetByKind[lumps[i].kind] = lumps[i].offset
		lenByKind[lumps[i].kind] = int32(len(lumps[i].data))
	}

	hdr := &bytes.Buffer{}
	_ = binary.Write(hdr, binary.LittleEndian, int32(bspfile.Version29))
	for k := bspfile.LumpKind(0); int(k) < bspfile.HeaderLumps; k++ {
		_ = binary.Write(hdr, binary.LittleEndian, offsetByKind[k])
		_ = binary.Write(hdr, binary.LittleEndian, lenByKind[k])
	}
	return append(hdr.Bytes(), body.Bytes()...)
}

// makeHost is the happy-path NewHost wrapper: a fresh progs + VM +
// cache + the BSP-bytes resolver. Returns the host and the *Progs
// stashed in it so tests can mutate fields/globals post-construction.
func makeHost(t *testing.T, bspBytes []byte, maxClients int) (*Host, *progs.Progs) {
	t.Helper()
	p := progsForHost()
	vm := progs.NewVM(p)
	cache := model.NewCache()
	resolver := resolverFor(bspBytes)
	h, err := NewHost(vm, cache, resolver, maxClients)
	if err != nil {
		t.Fatalf("NewHost: %v", err)
	}
	h.SetProgs(p)
	return h, p
}

// --- NewHost: nil-dep guards -----------------------------------------------

func TestNewHost_NilVM(t *testing.T) {
	_, err := NewHost(nil, model.NewCache(), resolverFor(nil), 1)
	if !errors.Is(err, ErrNilDep) {
		t.Errorf("got %v want ErrNilDep", err)
	}
}

func TestNewHost_NilCache(t *testing.T) {
	vm := progs.NewVM(progsForHost())
	_, err := NewHost(vm, nil, resolverFor(nil), 1)
	if !errors.Is(err, ErrNilDep) {
		t.Errorf("got %v want ErrNilDep", err)
	}
}

func TestNewHost_NilResolver(t *testing.T) {
	vm := progs.NewVM(progsForHost())
	_, err := NewHost(vm, model.NewCache(), nil, 1)
	if !errors.Is(err, ErrNilDep) {
		t.Errorf("got %v want ErrNilDep", err)
	}
}

// --- NewHost: happy path ---------------------------------------------------

func TestNewHost_PopulatesFields(t *testing.T) {
	p := progsForHost()
	vm := progs.NewVM(p)
	cache := model.NewCache()
	resolver := resolverFor(nil)
	h, err := NewHost(vm, cache, resolver, 4)
	if err != nil {
		t.Fatalf("NewHost: %v", err)
	}
	if h.Server == nil || h.Static == nil || h.VM == nil || h.World == nil {
		t.Error("NewHost should populate Server/Static/VM/World")
	}
	if h.Cache != cache {
		t.Error("Cache not set")
	}
	if h.Resolver == nil {
		t.Error("Resolver not set")
	}
	if h.FrameTime != DefaultFrameTime {
		t.Errorf("FrameTime got %v want %v", h.FrameTime, DefaultFrameTime)
	}
	if h.NowFn == nil {
		t.Error("NowFn must default to a non-nil clock")
	}
	if got := h.NowFn(); got <= 0 {
		t.Errorf("defaultNowFn returned %v; want > 0", got)
	}
	if h.Static.MaxClients != 4 {
		t.Errorf("MaxClients got %d want 4", h.Static.MaxClients)
	}
}

// maxClients <= 0 falls back to 1 (single local-client minimum).
func TestNewHost_ZeroClientsFallsBackToOne(t *testing.T) {
	vm := progs.NewVM(progsForHost())
	h, err := NewHost(vm, model.NewCache(), resolverFor(nil), 0)
	if err != nil {
		t.Fatalf("NewHost: %v", err)
	}
	if h.Static.MaxClients != 1 {
		t.Errorf("MaxClients got %d want 1", h.Static.MaxClients)
	}
}

// --- Frame --------------------------------------------------------------

// Frame on an inactive (un-spawned) server is a no-op.
func TestFrame_InactiveServerNoOp(t *testing.T) {
	h, _ := makeHost(t, nil, 1)
	if err := h.Frame(0.05); err != nil {
		t.Errorf("Frame on inactive server: %v want nil", err)
	}
	if h.Server.Time != 0 {
		t.Errorf("sv.time advanced despite inactive server: %v", h.Server.Time)
	}
}

// Frame on a freshly-spawned server: runs without panic, returns nil.
// The reserved client slots (1..MaxClients) have empty Fields blocks
// but RunPhysics reads movetype/solid which are at offsets 5/6 -- the
// 8-slot EntityFields layout makes that work + every slot starts
// movetype=0 (None) solid=0 (Not) -> skipped (free-entity rule).
func TestFrame_CleanTickReturnsNil(t *testing.T) {
	bsp := buildHostBSP(t, `{ "classname" "worldspawn" }`, 1)
	h, _ := makeHost(t, bsp, 2)
	if err := h.SpawnServer("test", protocol.VersionNQ); err != nil {
		t.Fatalf("SpawnServer: %v", err)
	}
	tBefore := h.Server.Time
	if err := h.Frame(0.05); err != nil {
		t.Fatalf("Frame: %v", err)
	}
	if h.Server.Time <= tBefore {
		t.Errorf("sv.time did not advance: before=%v after=%v", tBefore, h.Server.Time)
	}
}

// Frame propagates RunPhysics errors. Strategy: spawn the server,
// then corrupt the progs so the per-edict ReadFloat("movetype")
// surfaces ErrFieldNotFound. We replace progsRef + every edict's
// Fields slice in one shot to a Progs that has no movetype field --
// the per-physics ReadFloat on any non-free slot then errors.
func TestFrame_PropagatesRunPhysicsError(t *testing.T) {
	bsp := buildHostBSP(t, `{ "classname" "worldspawn" }`, 1)
	h, _ := makeHost(t, bsp, 1)
	if err := h.SpawnServer("test", protocol.VersionNQ); err != nil {
		t.Fatalf("SpawnServer: %v", err)
	}
	// Replace the bound Progs with one that has NO field defs at all
	// -- RunPhysics's ReadFloat("movetype") will surface
	// ErrFieldNotFound on the first non-nil edict (slot 0, the world).
	// Bump NumEdicts to 2 so the dispatcher walks slot 0 + 1, and the
	// world's empty-field-defs progs guarantees a movetype-not-found.
	stripped := &progs.Progs{
		Header:  progs.Header{EntityFields: 8},
		Strings: []byte{0},
	}
	h.SetProgs(stripped)
	h.Server.NumEdicts = 2
	err := h.Frame(0.05)
	if err == nil {
		t.Fatal("Frame: got nil; want propagated RunPhysics error")
	}
}

// Frame propagates the per-client svc_clientdata write error.
// Force this by shrinking an active client's Message buffer to 0
// capacity so EncodeClientData's first WriteByte overflows. Frame
// surfaces the propagated sizebuf overflow before SendClientFrames
// even runs.
func TestFrame_PropagatesWriteClientDataError(t *testing.T) {
	bsp := buildHostBSP(t, `{ "classname" "worldspawn" }`, 1)
	h, _ := makeHost(t, bsp, 1)
	if err := h.SpawnServer("test", protocol.VersionNQ); err != nil {
		t.Fatalf("SpawnServer: %v", err)
	}
	_, _, err := h.ConnectLoopback()
	if err != nil {
		t.Fatalf("ConnectLoopback: %v", err)
	}
	c := h.Static.Clients[0]
	c.Spawned = true
	c.Message = sizebuf.New(nil) // 0 cap forces the first encoder byte to overflow

	if err := h.Frame(0.05); !errors.Is(err, sizebuf.ErrSizeBufOverflow) {
		t.Errorf("Frame: got %v; want sizebuf overflow from WriteClientData", err)
	}
}

// Frame propagates a SendEntityUpdates write error. Isolate it from
// WriteClientData overflow by clearing c.Edict (so WriteClientData
// short-circuits), then cap c.Message to zero bytes so the per-entity
// update walk fails on the first encoder byte. The BSP entity lump
// declares a second entity past worldspawn so SpawnServer's
// entity-spawn pass marks Edicts[2] (NumEdicts=3) non-free; the walk
// then has at least one slot to emit + the zero-cap Message overflows.
func TestFrame_PropagatesSendEntityUpdatesError(t *testing.T) {
	bsp := buildHostBSP(t,
		`{ "classname" "worldspawn" }
		 { "classname" "info_player_start" "origin" "0 0 24" }`, 1)
	h, _ := makeHost(t, bsp, 1)
	if err := h.SpawnServer("test", protocol.VersionNQ); err != nil {
		t.Fatalf("SpawnServer: %v", err)
	}
	_, _, err := h.ConnectLoopback()
	if err != nil {
		t.Fatalf("ConnectLoopback: %v", err)
	}
	c := h.Static.Clients[0]
	c.Spawned = true
	c.Edict = nil                // bypass WriteClientData (compose helper short-circuits)
	c.Message = sizebuf.New(nil) // zero cap -> SendEntityUpdates overflows on first byte
	if err := h.Frame(0.05); !errors.Is(err, sizebuf.ErrSizeBufOverflow) {
		t.Errorf("Frame: got %v; want sizebuf overflow from SendEntityUpdates", err)
	}
}

// Frame propagates a SendClientFrames write error. We isolate the
// SendClientFrames overflow from the now-eager WriteClientData
// overflow by clearing the bound edict (so WriteClientData
// short-circuits) and pushing a byte into ReliableDatagram that
// exceeds the client's pruned Message capacity. The
// PreparePerClientMessage copy then surfaces the propagated
// sizebuf overflow.
func TestFrame_PropagatesSendClientFramesError(t *testing.T) {
	bsp := buildHostBSP(t, `{ "classname" "worldspawn" }`, 1)
	h, _ := makeHost(t, bsp, 1)
	if err := h.SpawnServer("test", protocol.VersionNQ); err != nil {
		t.Fatalf("SpawnServer: %v", err)
	}
	_, _, err := h.ConnectLoopback()
	if err != nil {
		t.Fatalf("ConnectLoopback: %v", err)
	}
	c := h.Static.Clients[0]
	c.Spawned = true
	c.Edict = nil                // bypass WriteClientData (compose helper short-circuits)
	c.Message = sizebuf.New(nil) // zero cap -> reliable-datagram copy overflows
	if err := h.Server.ReliableDatagram.Write([]byte{0x42}); err != nil {
		t.Fatalf("seed reliable_datagram: %v", err)
	}
	if err := h.Frame(0.05); !errors.Is(err, sizebuf.ErrSizeBufOverflow) {
		t.Errorf("Frame: got %v; want sizebuf overflow from SendClientFrames", err)
	}
}

// Frame propagates a FlushClientMessage SendReliable error. Close
// the loopback peer so SendReliable returns ErrNetConnClosed, then
// seed the reliable_datagram so the per-client copy puts at least
// one byte into client.Message (so the flush actually fires).
func TestFrame_PropagatesFlushClientMessageError(t *testing.T) {
	bsp := buildHostBSP(t, `{ "classname" "worldspawn" }`, 1)
	h, _ := makeHost(t, bsp, 1)
	if err := h.SpawnServer("test", protocol.VersionNQ); err != nil {
		t.Fatalf("SpawnServer: %v", err)
	}
	cli, _, err := h.ConnectLoopback()
	if err != nil {
		t.Fatalf("ConnectLoopback: %v", err)
	}
	c := h.Static.Clients[0]
	c.Spawned = true
	c.Edict = nil // bypass WriteClientData so the flush is the failing step
	if err := h.Server.ReliableDatagram.Write([]byte{0x77}); err != nil {
		t.Fatalf("seed reliable_datagram: %v", err)
	}
	if err := cli.Close(); err != nil {
		t.Fatalf("close client side: %v", err)
	}
	if err := h.Frame(0.05); !errors.Is(err, server.ErrNetConnClosed) {
		t.Errorf("Frame: got %v; want propagated ErrNetConnClosed from flush", err)
	}
}

// --- runClientCmds --------------------------------------------------------

// runClientCmds is a no-op on a host with no active clients.
func TestRunClientCmds_NoActiveClients(t *testing.T) {
	bsp := buildHostBSP(t, `{ "classname" "worldspawn" }`, 1)
	h, _ := makeHost(t, bsp, 2)
	if err := h.SpawnServer("test", protocol.VersionNQ); err != nil {
		t.Fatalf("SpawnServer: %v", err)
	}
	if err := h.runClientCmds(); err != nil {
		t.Errorf("runClientCmds with no active clients: %v want nil", err)
	}
}

// runClientCmds skips an active client without an Edict (defensive
// branch -- ConnectClient binds an edict but a test stub could carry
// Active+NetConnection without a bound edict).
func TestRunClientCmds_ActiveClientNoEdict(t *testing.T) {
	bsp := buildHostBSP(t, `{ "classname" "worldspawn" }`, 1)
	h, _ := makeHost(t, bsp, 1)
	if err := h.SpawnServer("test", protocol.VersionNQ); err != nil {
		t.Fatalf("SpawnServer: %v", err)
	}
	clientSide, _, err := h.ConnectLoopback()
	if err != nil {
		t.Fatalf("ConnectLoopback: %v", err)
	}
	_ = clientSide
	// Detach the edict so the second-half v_angle copy is skipped.
	h.Static.Clients[0].Edict = nil
	if err := h.runClientCmds(); err != nil {
		t.Errorf("runClientCmds with edict-less active client: %v want nil", err)
	}
}

// runClientCmds skips the v_angle copy silently when the bound progs
// lacks a v_angle field (test stubs with stripped progs). No error,
// no panic.
func TestRunClientCmds_NoVAngleField(t *testing.T) {
	bsp := buildHostBSP(t, `{ "classname" "worldspawn" }`, 1)
	h, _ := makeHost(t, bsp, 1)
	if err := h.SpawnServer("test", protocol.VersionNQ); err != nil {
		t.Fatalf("SpawnServer: %v", err)
	}
	if _, _, err := h.ConnectLoopback(); err != nil {
		t.Fatalf("ConnectLoopback: %v", err)
	}
	// progsForHost doesn't declare a v_angle field -- WriteVec3 returns
	// ErrFieldNotFound which runClientCmds silently drops.
	if err := h.runClientCmds(); err != nil {
		t.Errorf("runClientCmds with no v_angle field: %v want nil", err)
	}
}

// runClientCmds returns nil when the progs handle is nil -- the
// v_angle copy is skipped, the drain still runs.
func TestRunClientCmds_NoProgs(t *testing.T) {
	bsp := buildHostBSP(t, `{ "classname" "worldspawn" }`, 1)
	h, _ := makeHost(t, bsp, 1)
	if err := h.SpawnServer("test", protocol.VersionNQ); err != nil {
		t.Fatalf("SpawnServer: %v", err)
	}
	if _, _, err := h.ConnectLoopback(); err != nil {
		t.Fatalf("ConnectLoopback: %v", err)
	}
	h.SetProgs(nil)
	if err := h.runClientCmds(); err != nil {
		t.Errorf("runClientCmds with nil progs: %v want nil", err)
	}
}

// runClientCmds propagates a ReadClientMoves transport error.
func TestRunClientCmds_PropagatesReadError(t *testing.T) {
	bsp := buildHostBSP(t, `{ "classname" "worldspawn" }`, 1)
	h, _ := makeHost(t, bsp, 1)
	if err := h.SpawnServer("test", protocol.VersionNQ); err != nil {
		t.Fatalf("SpawnServer: %v", err)
	}
	if _, _, err := h.ConnectLoopback(); err != nil {
		t.Fatalf("ConnectLoopback: %v", err)
	}
	// Swap the package-level drain hook with a stub that errors.
	want := errors.New("boom drain")
	orig := readClientMoves
	readClientMoves = func(*server.Client) (int, error) { return 0, want }
	defer func() { readClientMoves = orig }()
	if err := h.runClientCmds(); !errors.Is(err, want) {
		t.Errorf("runClientCmds: got %v want %v", err, want)
	}
}

// Frame calls runClientCmds before RunPhysics; a runClientCmds error
// short-circuits the frame.
func TestFrame_PropagatesRunClientCmdsError(t *testing.T) {
	bsp := buildHostBSP(t, `{ "classname" "worldspawn" }`, 1)
	h, _ := makeHost(t, bsp, 1)
	if err := h.SpawnServer("test", protocol.VersionNQ); err != nil {
		t.Fatalf("SpawnServer: %v", err)
	}
	if _, _, err := h.ConnectLoopback(); err != nil {
		t.Fatalf("ConnectLoopback: %v", err)
	}
	want := errors.New("frame drain boom")
	orig := readClientMoves
	readClientMoves = func(*server.Client) (int, error) { return 0, want }
	defer func() { readClientMoves = orig }()
	if err := h.Frame(0.05); !errors.Is(err, want) {
		t.Errorf("Frame: got %v want %v", err, want)
	}
}

// boolToFloat is the inline 0/1 widen runClientCmds uses on each
// button bit. Verified standalone so the table-of-bits cases below
// don't have to re-prove the float encoding.
func TestBoolToFloat(t *testing.T) {
	if got := boolToFloat(true); got != 1 {
		t.Errorf("boolToFloat(true) got %v want 1", got)
	}
	if got := boolToFloat(false); got != 0 {
		t.Errorf("boolToFloat(false) got %v want 0", got)
	}
}

// progsForButtons builds a Progs stub that mirrors progsForHost but
// also declares button0 / button2 / impulse as EvFloat fields so the
// runClientCmds button-propagation path has somewhere to land its
// writes. Field offsets are unique + within the EntityFields=16
// slot budget (16 floats * 4 bytes = 64 bytes per edict). v_angle
// is added too so the existing pre-existing v_angle write lands
// alongside the buttons.
func progsForButtons() *progs.Progs {
	strs := []byte{0}
	classnameName := addStr(&strs, "classname")
	originName := addStr(&strs, "origin")
	movetypeName := addStr(&strs, "movetype")
	solidName := addStr(&strs, "solid")
	nextthinkName := addStr(&strs, "nextthink")
	thinkName := addStr(&strs, "think")
	vAngleName := addStr(&strs, "v_angle")
	button0Name := addStr(&strs, "button0")
	button2Name := addStr(&strs, "button2")
	impulseName := addStr(&strs, "impulse")

	return &progs.Progs{
		Header:  progs.Header{EntityFields: 16},
		Strings: strs,
		FieldDefs: []progs.Def{
			{Type: uint16(progs.EvString), Ofs: 1, SName: classnameName},
			{Type: uint16(progs.EvVector), Ofs: 2, SName: originName},
			{Type: uint16(progs.EvFloat), Ofs: 5, SName: movetypeName},
			{Type: uint16(progs.EvFloat), Ofs: 6, SName: solidName},
			{Type: uint16(progs.EvFloat), Ofs: 7, SName: nextthinkName},
			{Type: uint16(progs.EvFunction), Ofs: 8, SName: thinkName},
			{Type: uint16(progs.EvVector), Ofs: 9, SName: vAngleName},
			{Type: uint16(progs.EvFloat), Ofs: 12, SName: button0Name},
			{Type: uint16(progs.EvFloat), Ofs: 13, SName: button2Name},
			{Type: uint16(progs.EvFloat), Ofs: 14, SName: impulseName},
		},
		Globals:    make([]byte, 64*4),
		Statements: []progs.Statement{{Op: progs.OP_DONE}},
		Functions:  []progs.Function{{FirstStatement: 0, SName: 0}},
	}
}

// makeHostWithProgs is a makeHost variant that lets the caller swap
// in a Progs with extra entvars fields (= progsForButtons here).
func makeHostWithProgs(t *testing.T, bspBytes []byte, p *progs.Progs, maxClients int) *Host {
	t.Helper()
	vm := progs.NewVM(p)
	cache := model.NewCache()
	resolver := resolverFor(bspBytes)
	h, err := NewHost(vm, cache, resolver, maxClients)
	if err != nil {
		t.Fatalf("NewHost: %v", err)
	}
	h.SetProgs(p)
	return h
}

// runClientCmds writes self.button0 = 1 when Cmd.Buttons has the
// ButtonAttack bit set. This is the missing link without which
// every +attack on the client flips a wire bit the server reads but
// the QC's W_Attack chain never sees -- so no shot fires.
func TestRunClientCmds_PropagatesButtonAttackToButton0(t *testing.T) {
	bsp := buildHostBSP(t, `{ "classname" "worldspawn" }`, 1)
	p := progsForButtons()
	h := makeHostWithProgs(t, bsp, p, 1)
	if err := h.SpawnServer("test", protocol.VersionNQ); err != nil {
		t.Fatalf("SpawnServer: %v", err)
	}
	if _, _, err := h.ConnectLoopback(); err != nil {
		t.Fatalf("ConnectLoopback: %v", err)
	}
	h.Static.Clients[0].Cmd.Buttons = server.ButtonAttack
	if err := h.runClientCmds(); err != nil {
		t.Fatalf("runClientCmds: %v", err)
	}
	ev, _ := progs.NewEntVars(p, h.Static.Clients[0].Edict)
	got, err := ev.ReadFloat("button0")
	if err != nil {
		t.Fatalf("ReadFloat button0: %v", err)
	}
	if got != 1 {
		t.Errorf("button0 got %v want 1 (ButtonAttack bit set)", got)
	}
}

// runClientCmds writes self.button0 = 0 when Cmd.Buttons does NOT
// have the ButtonAttack bit set -- proves the release-edge case
// (player let go of +attack mid-tic) is propagated.
func TestRunClientCmds_PropagatesButtonAttackReleaseToButton0(t *testing.T) {
	bsp := buildHostBSP(t, `{ "classname" "worldspawn" }`, 1)
	p := progsForButtons()
	h := makeHostWithProgs(t, bsp, p, 1)
	if err := h.SpawnServer("test", protocol.VersionNQ); err != nil {
		t.Fatalf("SpawnServer: %v", err)
	}
	if _, _, err := h.ConnectLoopback(); err != nil {
		t.Fatalf("ConnectLoopback: %v", err)
	}
	// Pre-seed the field to 1 so the test proves the write
	// actually flips it down (not just that the default is 0).
	ev, _ := progs.NewEntVars(p, h.Static.Clients[0].Edict)
	_ = ev.WriteFloat("button0", 1)
	h.Static.Clients[0].Cmd.Buttons = 0
	if err := h.runClientCmds(); err != nil {
		t.Fatalf("runClientCmds: %v", err)
	}
	got, err := ev.ReadFloat("button0")
	if err != nil {
		t.Fatalf("ReadFloat button0: %v", err)
	}
	if got != 0 {
		t.Errorf("button0 got %v want 0 (no ButtonAttack bit)", got)
	}
}

// runClientCmds writes self.button2 = 1 when Cmd.Buttons has the
// ButtonJump bit set.
func TestRunClientCmds_PropagatesButtonJumpToButton2(t *testing.T) {
	bsp := buildHostBSP(t, `{ "classname" "worldspawn" }`, 1)
	p := progsForButtons()
	h := makeHostWithProgs(t, bsp, p, 1)
	if err := h.SpawnServer("test", protocol.VersionNQ); err != nil {
		t.Fatalf("SpawnServer: %v", err)
	}
	if _, _, err := h.ConnectLoopback(); err != nil {
		t.Fatalf("ConnectLoopback: %v", err)
	}
	h.Static.Clients[0].Cmd.Buttons = server.ButtonJump
	if err := h.runClientCmds(); err != nil {
		t.Fatalf("runClientCmds: %v", err)
	}
	ev, _ := progs.NewEntVars(p, h.Static.Clients[0].Edict)
	got, err := ev.ReadFloat("button2")
	if err != nil {
		t.Fatalf("ReadFloat button2: %v", err)
	}
	if got != 1 {
		t.Errorf("button2 got %v want 1 (ButtonJump bit set)", got)
	}
}

// runClientCmds writes self.impulse = Cmd.Impulse (cast to float).
// 27 is the upstream "+impulse 27" weapon-cycle code; the float
// shape preserves the byte verbatim.
func TestRunClientCmds_PropagatesImpulse(t *testing.T) {
	bsp := buildHostBSP(t, `{ "classname" "worldspawn" }`, 1)
	p := progsForButtons()
	h := makeHostWithProgs(t, bsp, p, 1)
	if err := h.SpawnServer("test", protocol.VersionNQ); err != nil {
		t.Fatalf("SpawnServer: %v", err)
	}
	if _, _, err := h.ConnectLoopback(); err != nil {
		t.Fatalf("ConnectLoopback: %v", err)
	}
	h.Static.Clients[0].Cmd.Impulse = 27
	if err := h.runClientCmds(); err != nil {
		t.Fatalf("runClientCmds: %v", err)
	}
	ev, _ := progs.NewEntVars(p, h.Static.Clients[0].Edict)
	got, err := ev.ReadFloat("impulse")
	if err != nil {
		t.Fatalf("ReadFloat impulse: %v", err)
	}
	if got != 27 {
		t.Errorf("impulse got %v want 27", got)
	}
}

// runClientCmds with progs that has no button0 / button2 / impulse
// fields silently skips each write (test stubs with stripped progs).
// progsForHost() lacks all three -- the call still succeeds.
func TestRunClientCmds_MissingButtonFieldsAreSilentlySkipped(t *testing.T) {
	bsp := buildHostBSP(t, `{ "classname" "worldspawn" }`, 1)
	h, _ := makeHost(t, bsp, 1)
	if err := h.SpawnServer("test", protocol.VersionNQ); err != nil {
		t.Fatalf("SpawnServer: %v", err)
	}
	if _, _, err := h.ConnectLoopback(); err != nil {
		t.Fatalf("ConnectLoopback: %v", err)
	}
	h.Static.Clients[0].Cmd.Buttons = server.ButtonAttack | server.ButtonJump
	h.Static.Clients[0].Cmd.Impulse = 9
	if err := h.runClientCmds(); err != nil {
		t.Errorf("runClientCmds with no button fields: %v want nil", err)
	}
}

// --- SpawnServer ----------------------------------------------------------

func TestSpawnServer_HappyPath(t *testing.T) {
	bsp := buildHostBSP(t, `{ "classname" "worldspawn" }`, 1)
	h, _ := makeHost(t, bsp, 4)
	if err := h.SpawnServer("test", protocol.VersionNQ); err != nil {
		t.Fatalf("SpawnServer: %v", err)
	}
	if !h.Server.Active {
		t.Error("Server.Active should be true after SpawnServer")
	}
	if h.Server.WorldModel == nil {
		t.Error("Server.WorldModel should be set")
	}
}

func TestSpawnServer_PropagatesError(t *testing.T) {
	// Empty map name -> server.Reset surfaces ErrEmptyMapName.
	bsp := buildHostBSP(t, `{ "classname" "worldspawn" }`, 1)
	h, _ := makeHost(t, bsp, 1)
	if err := h.SpawnServer("", protocol.VersionNQ); err == nil {
		t.Error("SpawnServer with empty name should error")
	}
}

// SetOnArenaReady installs the arena-publication hook the Host's
// SpawnServer pipes into SpawnDeps. The hook must fire with the
// same arena that lands on Server.Arena, BEFORE entity-spawn runs.
func TestSetOnArenaReady_HookFiresDuringSpawnServer(t *testing.T) {
	bsp := buildHostBSP(t, `{ "classname" "worldspawn" }`, 1)
	h, _ := makeHost(t, bsp, 1)
	var seen *progs.EdictArena
	h.SetOnArenaReady(func(a *progs.EdictArena) {
		seen = a
	})
	if err := h.SpawnServer("test", protocol.VersionNQ); err != nil {
		t.Fatalf("SpawnServer: %v", err)
	}
	if seen == nil {
		t.Fatal("OnArenaReady hook should have fired")
	}
	if seen != h.Server.Arena {
		t.Error("hook arena should match Server.Arena")
	}
}

// --- ConnectLoopback ------------------------------------------------------

func TestConnectLoopback_HappyPath(t *testing.T) {
	bsp := buildHostBSP(t, `{ "classname" "worldspawn" }`, 1)
	h, _ := makeHost(t, bsp, 2)
	if err := h.SpawnServer("test", protocol.VersionNQ); err != nil {
		t.Fatalf("SpawnServer: %v", err)
	}
	clientSide, idx, err := h.ConnectLoopback()
	if err != nil {
		t.Fatalf("ConnectLoopback: %v", err)
	}
	if idx != 0 {
		t.Errorf("first slot index got %d want 0", idx)
	}
	if clientSide == nil {
		t.Error("clientSide NetConn should be non-nil")
	}
	if !h.Static.Clients[0].Active {
		t.Error("Static.Clients[0].Active should be true after ConnectLoopback")
	}
	// The bound server-side conn should be a LoopbackConn.
	if _, ok := h.Static.Clients[0].NetConnection.(*server.LoopbackConn); !ok {
		t.Errorf("NetConnection type got %T want *server.LoopbackConn", h.Static.Clients[0].NetConnection)
	}
	// The bound edict should be Server.Edicts[idx+1].
	if h.Static.Clients[0].Edict != h.Server.Edicts[1] {
		t.Error("client.Edict should reference Server.Edicts[1]")
	}
}

func TestConnectLoopback_NoFreeSlot(t *testing.T) {
	bsp := buildHostBSP(t, `{ "classname" "worldspawn" }`, 1)
	h, _ := makeHost(t, bsp, 1)
	if err := h.SpawnServer("test", protocol.VersionNQ); err != nil {
		t.Fatalf("SpawnServer: %v", err)
	}
	if _, _, err := h.ConnectLoopback(); err != nil {
		t.Fatalf("first ConnectLoopback: %v", err)
	}
	_, idx, err := h.ConnectLoopback()
	if !errors.Is(err, server.ErrNoFreeClientSlot) {
		t.Errorf("got err=%v want ErrNoFreeClientSlot", err)
	}
	if idx != -1 {
		t.Errorf("got idx=%d want -1 on full pool", idx)
	}
}

// ConnectLoopback before SpawnServer: the edict pool is empty (the
// host's Server.Edicts is nil-length until SpawnServer runs), so the
// makeEdict hook returns nil. ConnectClient still succeeds; the
// resulting client just has a nil Edict (which the lifecycle layer
// would normally fix up on the first per-client physics).
func TestConnectLoopback_BeforeSpawnServer(t *testing.T) {
	h, _ := makeHost(t, nil, 1)
	clientSide, idx, err := h.ConnectLoopback()
	if err != nil {
		t.Fatalf("ConnectLoopback: %v", err)
	}
	if idx != 0 || clientSide == nil {
		t.Errorf("idx=%d clientSide=%v want (0, non-nil)", idx, clientSide)
	}
	if h.Static.Clients[0].Edict != nil {
		t.Error("client.Edict should be nil before SpawnServer")
	}
}

// --- thinkCaller bridge ---------------------------------------------------

// The bridge invokes vm.Run with the given funcID. We verify by
// checking the OFS_RETURN slot afterwards -- progsForHost's
// Functions[1] returns 42 from the k42 global into OfsReturn.
func TestThinkCaller_InvokesVMRun(t *testing.T) {
	h, _ := makeHost(t, nil, 1)
	// World edict needs to exist for the worldEdict() path; build
	// one via a manual arena so the bridge can resolve "other".
	if err := h.thinkCaller(nil, 1); err != nil {
		t.Fatalf("thinkCaller: %v", err)
	}
	// The bridge calls vm.Run(1), which executes Functions[1] = OP_RETURN
	// reading from k42Slot. OfsReturn should now hold 42.0.
	got, err := h.VM.GlobalFloat(progs.OfsReturn)
	if err != nil {
		t.Fatalf("GlobalFloat(OfsReturn): %v", err)
	}
	if got != 42 {
		t.Errorf("OfsReturn got %v want 42", got)
	}
}

// The bridge surfaces vm.Run errors verbatim. funcID 0 = the null
// function, which vm.Run rejects with ErrBadFunctionIndex.
func TestThinkCaller_PropagatesVMError(t *testing.T) {
	h, _ := makeHost(t, nil, 1)
	err := h.thinkCaller(nil, 0)
	if !errors.Is(err, progs.ErrBadFunctionIndex) {
		t.Errorf("got %v want ErrBadFunctionIndex", err)
	}
}

// With a nil progsRef the bridge skips the named-global hand-off but
// still dispatches vm.Run. The k42-return path proves the dispatch
// still fires.
func TestThinkCaller_NilProgsRefSkipsGlobals(t *testing.T) {
	h, _ := makeHost(t, nil, 1)
	h.SetProgs(nil)
	if err := h.thinkCaller(nil, 1); err != nil {
		t.Fatalf("thinkCaller: %v", err)
	}
	got, _ := h.VM.GlobalFloat(progs.OfsReturn)
	if got != 42 {
		t.Errorf("OfsReturn got %v want 42 (dispatch should still fire)", got)
	}
}

// Bridge with a Progs that DOES NOT declare self/other/time globals
// skips each write but still dispatches.
func TestThinkCaller_MissingNamedGlobals(t *testing.T) {
	h, _ := makeHost(t, nil, 1)
	// Build a Progs that has only Functions + Statements + the k42
	// constant but no self/other/time globals.
	p := progsForHost()
	p.GlobalDefs = nil // strip every named global
	h.SetProgs(p)
	if err := h.thinkCaller(nil, 1); err != nil {
		t.Fatalf("thinkCaller: %v", err)
	}
}

// Bridge with a non-nil ent + a populated edict pool exercises the
// entityPointer non-nil branch. We can't directly observe the
// SetGlobalInt write target (the bridge writes 0 either way under
// the current scope), but we verify the dispatch still fires.
func TestThinkCaller_NonNilEntPath(t *testing.T) {
	bsp := buildHostBSP(t, `{ "classname" "worldspawn" }`, 1)
	h, _ := makeHost(t, bsp, 1)
	if err := h.SpawnServer("test", protocol.VersionNQ); err != nil {
		t.Fatalf("SpawnServer: %v", err)
	}
	ent := h.Server.Edicts[0]
	if err := h.thinkCaller(ent, 1); err != nil {
		t.Fatalf("thinkCaller: %v", err)
	}
	got, _ := h.VM.GlobalFloat(progs.OfsReturn)
	if got != 42 {
		t.Errorf("OfsReturn got %v want 42", got)
	}
}

// worldEdict returns nil when Server.Edicts is empty (pre-spawn).
func TestWorldEdict_EmptyEdictsSliceIsNil(t *testing.T) {
	h, _ := makeHost(t, nil, 1)
	if got := h.worldEdict(); got != nil {
		t.Errorf("worldEdict on fresh host: %v want nil", got)
	}
}

// worldEdict returns Edicts[0] post-spawn.
func TestWorldEdict_PostSpawnReturnsWorld(t *testing.T) {
	bsp := buildHostBSP(t, `{ "classname" "worldspawn" }`, 1)
	h, _ := makeHost(t, bsp, 1)
	if err := h.SpawnServer("test", protocol.VersionNQ); err != nil {
		t.Fatalf("SpawnServer: %v", err)
	}
	if got := h.worldEdict(); got != h.Server.Edicts[0] {
		t.Error("worldEdict should return Server.Edicts[0]")
	}
}

// entityPointer returns 0 for nil ent, 0 for non-nil ent when no
// arena is attached (pre-SpawnServer / test-stub shape), and the
// arena byte-offset for non-nil ent post-SpawnServer (production
// shape -- the value OP_STATE consumes via vm.selfEdict() to pick
// which edict to write nextthink/frame/think into).
func TestEntityPointer_NilEntIsZero(t *testing.T) {
	h, _ := makeHost(t, nil, 1)
	if got := h.entityPointer(nil); got != 0 {
		t.Errorf("nil ent -> %d want 0", got)
	}
	// No-arena branch: any *Edict (we synthesize one here) returns
	// 0 because h.Server.Arena is nil pre-SpawnServer.
	if got := h.entityPointer(&progs.Edict{}); got != 0 {
		t.Errorf("non-nil ent (no arena) -> %d want 0", got)
	}
}

// entityPointer returns the arena byte-offset when an arena is
// attached, matching what OP_STATE expects vm.selfEdict() to
// produce. The world edict (slot 0) is byte 0; slot 1 is byte
// FieldBytes(); etc.
func TestEntityPointer_ArenaPointerForEdict(t *testing.T) {
	bsp := buildHostBSP(t, `{ "classname" "worldspawn" }`, 1)
	h, _ := makeHost(t, bsp, 1)
	if err := h.SpawnServer("test", protocol.VersionNQ); err != nil {
		t.Fatalf("SpawnServer: %v", err)
	}
	if h.Server.Arena == nil {
		t.Fatal("SpawnServer should populate Server.Arena")
	}
	// World edict -> byte offset 0.
	if got := h.entityPointer(h.Server.Edicts[0]); got != 0 {
		t.Errorf("world edict -> %d want 0", got)
	}
	// Slot 1 -> arena's MakePointer(1, 0).
	if len(h.Server.Edicts) > 1 {
		want := int(h.Server.Arena.PointerForEdict(h.Server.Edicts[1]))
		if got := h.entityPointer(h.Server.Edicts[1]); got != want {
			t.Errorf("slot-1 -> %d want %d", got, want)
		}
		// The slot-1 pointer must be non-zero so OP_STATE writes
		// land on the dispatching edict rather than the world.
		if want == 0 {
			t.Error("slot-1 arena pointer should be non-zero")
		}
	}
}

// --- per-tic resolver branches -------------------------------------------

// edictAt covers: in-range happy path, negative index, past-end index.
func TestEdictAt_AllBranches(t *testing.T) {
	bsp := buildHostBSP(t, `{ "classname" "worldspawn" }`, 1)
	h, _ := makeHost(t, bsp, 1)
	if err := h.SpawnServer("test", protocol.VersionNQ); err != nil {
		t.Fatalf("SpawnServer: %v", err)
	}
	if got := h.edictAt(0); got != h.Server.Edicts[0] {
		t.Error("edictAt(0) should return the world edict")
	}
	if got := h.edictAt(-1); got != nil {
		t.Error("edictAt(-1) should be nil")
	}
	if got := h.edictAt(1 << 30); got != nil {
		t.Error("edictAt(huge) should be nil")
	}
}

// cmdAt covers: out-of-range below, out-of-range above, nil slot,
// happy path with a seeded Cmd.
func TestCmdAt_AllBranches(t *testing.T) {
	bsp := buildHostBSP(t, `{ "classname" "worldspawn" }`, 2)
	h, _ := makeHost(t, bsp, 2)
	if err := h.SpawnServer("test", protocol.VersionNQ); err != nil {
		t.Fatalf("SpawnServer: %v", err)
	}
	if got := h.cmdAt(0); got != (server.UserCmd{}) {
		t.Error("cmdAt(0) should be zero")
	}
	if got := h.cmdAt(h.Static.MaxClients + 1); got != (server.UserCmd{}) {
		t.Error("cmdAt(>max) should be zero")
	}
	h.Static.Clients[1] = nil
	if got := h.cmdAt(2); got != (server.UserCmd{}) {
		t.Error("cmdAt(2) on nil slot should be zero")
	}
	want := server.UserCmd{ForwardMove: 100}
	h.Static.Clients[0].Cmd = want
	if got := h.cmdAt(1); got != want {
		t.Errorf("cmdAt(1) got %v want %v", got, want)
	}
}

// hostKeyAt is a one-liner identity.
func TestHostKeyAt(t *testing.T) {
	if hostKeyAt(7) != 7 {
		t.Errorf("hostKeyAt(7) want 7")
	}
}

// --- EdictOrigin ----------------------------------------------------------

// Happy path: write origin into the world edict via EntVars, read it
// back through EdictOrigin.
func TestEdictOrigin_HappyPath(t *testing.T) {
	bsp := buildHostBSP(t, `{ "classname" "worldspawn" }`, 1)
	h, p := makeHost(t, bsp, 1)
	if err := h.SpawnServer("test", protocol.VersionNQ); err != nil {
		t.Fatalf("SpawnServer: %v", err)
	}
	v, err := progs.NewEntVars(p, h.Server.Edicts[1])
	if err != nil {
		t.Fatalf("NewEntVars: %v", err)
	}
	want := [3]float32{12, 34, 56}
	if err := v.WriteVec3("origin", want); err != nil {
		t.Fatalf("WriteVec3: %v", err)
	}
	got, err := h.EdictOrigin(1)
	if err != nil {
		t.Fatalf("EdictOrigin: %v", err)
	}
	if got != want {
		t.Errorf("EdictOrigin got %v want %v", got, want)
	}
}

// Out-of-range slot indices fail with ErrNoEdict.
func TestEdictOrigin_OutOfRange(t *testing.T) {
	bsp := buildHostBSP(t, `{ "classname" "worldspawn" }`, 1)
	h, _ := makeHost(t, bsp, 1)
	if err := h.SpawnServer("test", protocol.VersionNQ); err != nil {
		t.Fatalf("SpawnServer: %v", err)
	}
	if _, err := h.EdictOrigin(-1); !errors.Is(err, ErrNoEdict) {
		t.Errorf("EdictOrigin(-1) got %v want ErrNoEdict", err)
	}
	if _, err := h.EdictOrigin(1 << 30); !errors.Is(err, ErrNoEdict) {
		t.Errorf("EdictOrigin(huge) got %v want ErrNoEdict", err)
	}
}

// nil edict in an in-range slot also surfaces ErrNoEdict.
func TestEdictOrigin_NilEdict(t *testing.T) {
	bsp := buildHostBSP(t, `{ "classname" "worldspawn" }`, 1)
	h, _ := makeHost(t, bsp, 1)
	if err := h.SpawnServer("test", protocol.VersionNQ); err != nil {
		t.Fatalf("SpawnServer: %v", err)
	}
	h.Server.Edicts[0] = nil
	if _, err := h.EdictOrigin(0); !errors.Is(err, ErrNoEdict) {
		t.Errorf("EdictOrigin(nil) got %v want ErrNoEdict", err)
	}
}

// EdictOrigin pre-SpawnServer: the edict pool is empty -> ErrNoEdict.
func TestEdictOrigin_BeforeSpawn(t *testing.T) {
	h, _ := makeHost(t, nil, 1)
	if _, err := h.EdictOrigin(0); !errors.Is(err, ErrNoEdict) {
		t.Errorf("EdictOrigin pre-spawn got %v want ErrNoEdict", err)
	}
}

// Without a bound Progs the helper can't resolve the field name; the
// guard returns ErrNoProgs.
func TestEdictOrigin_NoProgs(t *testing.T) {
	bsp := buildHostBSP(t, `{ "classname" "worldspawn" }`, 1)
	h, _ := makeHost(t, bsp, 1)
	if err := h.SpawnServer("test", protocol.VersionNQ); err != nil {
		t.Fatalf("SpawnServer: %v", err)
	}
	h.SetProgs(nil)
	if _, err := h.EdictOrigin(0); !errors.Is(err, ErrNoProgs) {
		t.Errorf("EdictOrigin no-progs got %v want ErrNoProgs", err)
	}
}

// EntVars-layer errors propagate verbatim: a Progs that omits the
// "origin" field def surfaces ErrFieldNotFound.
func TestEdictOrigin_FieldNotFound(t *testing.T) {
	bsp := buildHostBSP(t, `{ "classname" "worldspawn" }`, 1)
	h, _ := makeHost(t, bsp, 1)
	if err := h.SpawnServer("test", protocol.VersionNQ); err != nil {
		t.Fatalf("SpawnServer: %v", err)
	}
	stripped := &progs.Progs{
		Header:  progs.Header{EntityFields: 8},
		Strings: []byte{0},
	}
	h.SetProgs(stripped)
	if _, err := h.EdictOrigin(0); !errors.Is(err, progs.ErrFieldNotFound) {
		t.Errorf("EdictOrigin missing-field got %v want ErrFieldNotFound", err)
	}
}

// SetInterner replaces the bound interner.
func TestSetInterner(t *testing.T) {
	h, _ := makeHost(t, nil, 1)
	called := false
	h.SetInterner(func(string) int32 { called = true; return 42 })
	if got := h.interner("foo"); got != 42 || !called {
		t.Errorf("SetInterner override not honoured: got=%d called=%v", got, called)
	}
}

func TestSetSpawnFn(t *testing.T) {
	h, _ := makeHost(t, nil, 1)
	if h.spawnFn != nil {
		t.Errorf("spawnFn default: got non-nil, want nil")
	}
	called := 0
	h.SetSpawnFn(func(_ *progs.Edict, _ string) { called++ })
	if h.spawnFn == nil {
		t.Errorf("SetSpawnFn did not install the hook")
	}
	h.spawnFn(nil, "info_player_start")
	if called != 1 {
		t.Errorf("installed hook not invoked: called=%d want 1", called)
	}
}

func TestProgs_DefaultNil(t *testing.T) {
	h, _ := makeHost(t, nil, 1)
	// makeHost installs a Progs via SetProgs; clear it to assert the
	// nil-default surface.
	h.SetProgs(nil)
	if h.Progs() != nil {
		t.Errorf("Progs() = %v want nil after SetProgs(nil)", h.Progs())
	}
}

func TestProgs_ReturnsInstalled(t *testing.T) {
	h, p := makeHost(t, nil, 1)
	if got := h.Progs(); got != p {
		t.Errorf("Progs() = %p want %p (the Progs installed via SetProgs)", got, p)
	}
}

// --- runThink (SV_RunThink-equivalent top-level walker) -------------------

// runThink with a nil progsRef short-circuits (no fields to read).
// Counters reset to zero regardless.
func TestRunThink_NilProgsShortCircuits(t *testing.T) {
	h, _ := makeHost(t, nil, 1)
	h.SetProgs(nil)
	h.LastThinksDispatched = 7
	h.LastThinkErrors = 3
	h.runThink()
	if h.LastThinksDispatched != 0 || h.LastThinkErrors != 0 {
		t.Errorf("counters not reset on nil-progs short-circuit: dispatched=%d errors=%d",
			h.LastThinksDispatched, h.LastThinkErrors)
	}
}

// runThink walks [1, NumEdicts) and bound-clips against
// len(Server.Edicts) defensively. Empty pool -> no work, no panic.
func TestRunThink_EmptyEdictPool(t *testing.T) {
	h, _ := makeHost(t, nil, 1)
	// Pre-spawn: Server.Edicts is nil-length, NumEdicts==0.
	h.runThink()
	if h.LastThinksDispatched != 0 || h.LastThinkErrors != 0 {
		t.Errorf("pre-spawn runThink should be a no-op: dispatched=%d errors=%d",
			h.LastThinksDispatched, h.LastThinkErrors)
	}
}

// Happy path: spawn an entity with nextthink scheduled inside the
// current tic's window + think pointing at the k42 function. runThink
// fires it, clears nextthink to 0, and bumps LastThinksDispatched.
func TestRunThink_DispatchesScheduledThink(t *testing.T) {
	bsp := buildHostBSP(t,
		`{ "classname" "worldspawn" }
		 { "classname" "monster_test" "origin" "0 0 0" }`, 1)
	h, p := makeHost(t, bsp, 1)
	if err := h.SpawnServer("test", protocol.VersionNQ); err != nil {
		t.Fatalf("SpawnServer: %v", err)
	}
	// Advance sv.time so nextthink=0.5 is in-window.
	h.Server.Time = 1.0
	// Slot 1 is the reserved client slot; the parsed monster_test
	// entity lands at slot 2 (the first post-client slot).
	ent := h.Server.Edicts[2]
	ev, err := progs.NewEntVars(p, ent)
	if err != nil {
		t.Fatalf("NewEntVars: %v", err)
	}
	if err := ev.WriteFloat("nextthink", 0.5); err != nil {
		t.Fatalf("WriteFloat nextthink: %v", err)
	}
	if err := ev.WriteInt32("think", 1); err != nil {
		t.Fatalf("WriteInt32 think: %v", err)
	}

	h.runThink()

	if h.LastThinksDispatched != 1 {
		t.Errorf("LastThinksDispatched got %d want 1", h.LastThinksDispatched)
	}
	if h.LastThinkErrors != 0 {
		t.Errorf("LastThinkErrors got %d want 0", h.LastThinkErrors)
	}
	cleared, err := ev.ReadFloat("nextthink")
	if err != nil {
		t.Fatalf("re-read nextthink: %v", err)
	}
	if cleared != 0 {
		t.Errorf("nextthink should be cleared to 0; got %v", cleared)
	}
	// Confirm the QC function actually ran (k42 returns 42 into OfsReturn).
	got, err := h.VM.GlobalFloat(progs.OfsReturn)
	if err != nil {
		t.Fatalf("GlobalFloat(OfsReturn): %v", err)
	}
	if got != 42 {
		t.Errorf("OfsReturn got %v want 42 (proves think dispatched)", got)
	}
}

// nil edict slot AND a Free edict slot are both skipped silently.
func TestRunThink_SkipsNilAndFree(t *testing.T) {
	bsp := buildHostBSP(t,
		`{ "classname" "worldspawn" }
		 { "classname" "monster_test" "origin" "0 0 0" }`, 1)
	h, _ := makeHost(t, bsp, 1)
	if err := h.SpawnServer("test", protocol.VersionNQ); err != nil {
		t.Fatalf("SpawnServer: %v", err)
	}
	// Stretch the edict pool with a nil slot + a freed slot; neither
	// should be dispatched.
	h.Server.Edicts = append(h.Server.Edicts, nil)
	freed := &progs.Edict{Free: true, Fields: make([]byte, 32)}
	h.Server.Edicts = append(h.Server.Edicts, freed)
	h.Server.NumEdicts = len(h.Server.Edicts)
	h.runThink()
	if h.LastThinksDispatched != 0 || h.LastThinkErrors != 0 {
		t.Errorf("dispatched=%d errors=%d want both 0", h.LastThinksDispatched, h.LastThinkErrors)
	}
}

// runThink clips NumEdicts down to len(Server.Edicts) so a corrupted
// count past the allocated pool can't index out of range.
func TestRunThink_ClipsNumEdictsToPoolLen(t *testing.T) {
	bsp := buildHostBSP(t, `{ "classname" "worldspawn" }`, 1)
	h, _ := makeHost(t, bsp, 1)
	if err := h.SpawnServer("test", protocol.VersionNQ); err != nil {
		t.Fatalf("SpawnServer: %v", err)
	}
	h.Server.NumEdicts = len(h.Server.Edicts) + 100 // past the pool
	// No panic + counters stay zero (every in-pool slot is either
	// the world (skipped via i:=1 start) or a fresh client slot
	// with nextthink=0).
	h.runThink()
	if h.LastThinksDispatched != 0 || h.LastThinkErrors != 0 {
		t.Errorf("dispatched=%d errors=%d want both 0", h.LastThinksDispatched, h.LastThinkErrors)
	}
}

// nextthink == 0 (default) -> per-slot skip.
func TestRunThink_NoThinkScheduledSkips(t *testing.T) {
	bsp := buildHostBSP(t,
		`{ "classname" "worldspawn" }
		 { "classname" "monster_test" "origin" "0 0 0" }`, 1)
	h, _ := makeHost(t, bsp, 1)
	if err := h.SpawnServer("test", protocol.VersionNQ); err != nil {
		t.Fatalf("SpawnServer: %v", err)
	}
	h.runThink()
	if h.LastThinksDispatched != 0 {
		t.Errorf("LastThinksDispatched got %d want 0 (no nextthink scheduled)",
			h.LastThinksDispatched)
	}
}

// nextthink > sv.time -> per-slot skip (think is in the future).
func TestRunThink_FutureThinkSkips(t *testing.T) {
	bsp := buildHostBSP(t,
		`{ "classname" "worldspawn" }
		 { "classname" "monster_test" "origin" "0 0 0" }`, 1)
	h, p := makeHost(t, bsp, 1)
	if err := h.SpawnServer("test", protocol.VersionNQ); err != nil {
		t.Fatalf("SpawnServer: %v", err)
	}
	h.Server.Time = 1.0
	ent := h.Server.Edicts[2] // monster_test at slot 2 (slot 1 = client)
	ev, _ := progs.NewEntVars(p, ent)
	_ = ev.WriteFloat("nextthink", 5.0) // future
	_ = ev.WriteInt32("think", 1)
	h.runThink()
	if h.LastThinksDispatched != 0 {
		t.Errorf("LastThinksDispatched got %d want 0 (think is in the future)",
			h.LastThinksDispatched)
	}
}

// nextthink field absent (stripped progs) -> per-slot skip (no error,
// no dispatch). Strategy: install a progs with a "nextthink" name but
// no FieldDef for it; ReadFloat surfaces ErrFieldNotFound.
func TestRunThink_MissingNextthinkSkips(t *testing.T) {
	bsp := buildHostBSP(t,
		`{ "classname" "worldspawn" }
		 { "classname" "monster_test" "origin" "0 0 0" }`, 1)
	h, _ := makeHost(t, bsp, 1)
	if err := h.SpawnServer("test", protocol.VersionNQ); err != nil {
		t.Fatalf("SpawnServer: %v", err)
	}
	// Swap to a progs that declares no fields at all -> ReadFloat
	// surfaces ErrFieldNotFound for every edict.
	stripped := &progs.Progs{
		Header:  progs.Header{EntityFields: 8},
		Strings: []byte{0},
	}
	h.SetProgs(stripped)
	h.runThink()
	if h.LastThinksDispatched != 0 || h.LastThinkErrors != 0 {
		t.Errorf("dispatched=%d errors=%d; missing field should be silent skip",
			h.LastThinksDispatched, h.LastThinkErrors)
	}
}

// think field absent (only nextthink is declared) -> per-slot skip.
func TestRunThink_MissingThinkSkips(t *testing.T) {
	bsp := buildHostBSP(t,
		`{ "classname" "worldspawn" }
		 { "classname" "monster_test" "origin" "0 0 0" }`, 1)
	h, _ := makeHost(t, bsp, 1)
	if err := h.SpawnServer("test", protocol.VersionNQ); err != nil {
		t.Fatalf("SpawnServer: %v", err)
	}
	// Build a progs that declares nextthink (so the schedule check
	// passes) but no "think" field -> ReadInt32 surfaces
	// ErrFieldNotFound.
	strs := []byte{0}
	nextthinkName := addStr(&strs, "nextthink")
	p2 := &progs.Progs{
		Header:  progs.Header{EntityFields: 8},
		Strings: strs,
		FieldDefs: []progs.Def{
			{Type: uint16(progs.EvFloat), Ofs: 7, SName: nextthinkName},
		},
	}
	h.SetProgs(p2)
	// Now arm nextthink on slot 1 via the new EntVars.
	h.Server.Time = 1.0
	ev, err := progs.NewEntVars(p2, h.Server.Edicts[2]) // monster_test slot
	if err != nil {
		t.Fatalf("NewEntVars: %v", err)
	}
	if err := ev.WriteFloat("nextthink", 0.5); err != nil {
		t.Fatalf("WriteFloat: %v", err)
	}
	h.runThink()
	if h.LastThinksDispatched != 0 || h.LastThinkErrors != 0 {
		t.Errorf("dispatched=%d errors=%d; missing think field should be silent skip",
			h.LastThinksDispatched, h.LastThinkErrors)
	}
}

// thinkCaller error (funcID=0 -> ErrBadFunctionIndex) is swallowed
// and tallied into LastThinkErrors -- the frame does NOT abort.
func TestRunThink_ThinkCallerErrorTalliedAndSwallowed(t *testing.T) {
	bsp := buildHostBSP(t,
		`{ "classname" "worldspawn" }
		 { "classname" "monster_test" "origin" "0 0 0" }`, 1)
	h, p := makeHost(t, bsp, 1)
	if err := h.SpawnServer("test", protocol.VersionNQ); err != nil {
		t.Fatalf("SpawnServer: %v", err)
	}
	h.Server.Time = 1.0
	ev, _ := progs.NewEntVars(p, h.Server.Edicts[2]) // monster_test slot
	_ = ev.WriteFloat("nextthink", 0.5)
	_ = ev.WriteInt32("think", 0) // funcID 0 -> vm.Run returns ErrBadFunctionIndex
	h.runThink()
	if h.LastThinksDispatched != 0 {
		t.Errorf("LastThinksDispatched got %d want 0 (dispatch failed)",
			h.LastThinksDispatched)
	}
	if h.LastThinkErrors != 1 {
		t.Errorf("LastThinkErrors got %d want 1 (failure should be tallied)",
			h.LastThinkErrors)
	}
	// LastThinkErrorMsgs captures the first 8 unique error strings so
	// the per-tic instrumentation can name the failure; with a single
	// erroring slot we expect exactly one captured message.
	if got := len(h.LastThinkErrorMsgs); got != 1 {
		t.Fatalf("LastThinkErrorMsgs len got %d want 1", got)
	}
	if h.LastThinkErrorMsgs[0] == "" {
		t.Errorf("LastThinkErrorMsgs[0] should be the swallowed err.Error(); got empty")
	}
}

// LastThinkErrorMsgs de-duplicates identical error strings. Strategy:
// stretch the edict pool with 10 monster_test slots all firing the
// same funcID 0 -> same per-slot error message; LastThinkErrors tallies
// 10 but LastThinkErrorMsgs holds exactly 1 (dedup branch).
func TestRunThink_ErrorMsgsDedup(t *testing.T) {
	bsp := buildHostBSP(t,
		`{ "classname" "worldspawn" }
		 { "classname" "monster_test" "origin" "0 0 0" }`, 1)
	h, p := makeHost(t, bsp, 1)
	if err := h.SpawnServer("test", protocol.VersionNQ); err != nil {
		t.Fatalf("SpawnServer: %v", err)
	}
	h.Server.Time = 1.0
	// Arm the seeded monster_test slot first (proves the dedup path).
	ev, _ := progs.NewEntVars(p, h.Server.Edicts[2])
	_ = ev.WriteFloat("nextthink", 0.5)
	_ = ev.WriteInt32("think", 0)
	// Append 9 more identical-failure slots so runThink walks 10 total.
	for i := 0; i < 9; i++ {
		extra := &progs.Edict{Fields: make([]byte, len(h.Server.Edicts[2].Fields))}
		copy(extra.Fields, h.Server.Edicts[2].Fields)
		h.Server.Edicts = append(h.Server.Edicts, extra)
	}
	h.Server.NumEdicts = len(h.Server.Edicts)
	h.runThink()
	if h.LastThinkErrors != 10 {
		t.Errorf("LastThinkErrors got %d want 10", h.LastThinkErrors)
	}
	if got := len(h.LastThinkErrorMsgs); got != 1 {
		t.Errorf("LastThinkErrorMsgs len got %d want 1 (dedup)", got)
	}
}

// LastThinkErrorMsgs caps at 8 entries even when every error string is
// distinct. Strategy: arm 10 erroring slots, each with a different
// (also-bad) funcID. The capture prefixes the funcID into the msg so
// each per-slot string is unique; dedup does NOT collapse them.
// LastThinkErrors counts all 10, LastThinkErrorMsgs stops at 8.
func TestRunThink_ErrorMsgsCap8(t *testing.T) {
	bsp := buildHostBSP(t,
		`{ "classname" "worldspawn" }
		 { "classname" "monster_test" "origin" "0 0 0" }`, 1)
	h, p := makeHost(t, bsp, 1)
	if err := h.SpawnServer("test", protocol.VersionNQ); err != nil {
		t.Fatalf("SpawnServer: %v", err)
	}
	h.Server.Time = 1.0
	// Arm seeded monster_test slot 2 with think funcID 1000 (way past
	// the progs.Functions table -> ErrBadFunctionIndex with that
	// prefix on the captured msg).
	ev, _ := progs.NewEntVars(p, h.Server.Edicts[2])
	_ = ev.WriteFloat("nextthink", 0.5)
	_ = ev.WriteInt32("think", 1000)
	// Append 9 more clones, each with a different (also-bad) funcID so
	// every per-slot msg string is unique.
	for i := 0; i < 9; i++ {
		extra := &progs.Edict{Fields: make([]byte, len(h.Server.Edicts[2].Fields))}
		copy(extra.Fields, h.Server.Edicts[2].Fields)
		h.Server.Edicts = append(h.Server.Edicts, extra)
		extraEv, _ := progs.NewEntVars(p, extra)
		_ = extraEv.WriteInt32("think", int32(1001+i))
	}
	h.Server.NumEdicts = len(h.Server.Edicts)
	h.runThink()
	if h.LastThinkErrors != 10 {
		t.Errorf("LastThinkErrors got %d want 10", h.LastThinkErrors)
	}
	if got := len(h.LastThinkErrorMsgs); got != 8 {
		t.Errorf("LastThinkErrorMsgs len got %d want 8 (cap)", got)
	}
}

// Frame calls runThink as part of the per-tic sequence. Smoke-test:
// schedule a think + call Frame, expect LastThinksDispatched == 1.
func TestFrame_InvokesRunThink(t *testing.T) {
	bsp := buildHostBSP(t,
		`{ "classname" "worldspawn" }
		 { "classname" "monster_test" "origin" "0 0 0" }`, 1)
	h, p := makeHost(t, bsp, 1)
	if err := h.SpawnServer("test", protocol.VersionNQ); err != nil {
		t.Fatalf("SpawnServer: %v", err)
	}
	// Pre-arm: sv.time will be advanced by dt at the head of Frame,
	// so a nextthink at the resulting sv.time fires this frame.
	h.Server.Time = 0.5
	ev, _ := progs.NewEntVars(p, h.Server.Edicts[2]) // monster_test slot
	_ = ev.WriteFloat("nextthink", 0.55)             // <= 0.5 + 0.05 = 0.55 (post-advance sv.time)
	_ = ev.WriteInt32("think", 1)
	if err := h.Frame(0.05); err != nil {
		t.Fatalf("Frame: %v", err)
	}
	if h.LastThinksDispatched != 1 {
		t.Errorf("Frame should have dispatched 1 think; LastThinksDispatched=%d",
			h.LastThinksDispatched)
	}
}

// progsForPostThink is progsForButtons plus a no-op function named
// "PlayerPostThink" so FindFunction resolves it and runPlayerPostThink
// dispatches. FirstStatement 0 = the pre-roll OP_DONE (a clean return).
func progsForPostThink() *progs.Progs {
	p := progsForButtons()
	ppt := addStr(&p.Strings, "PlayerPostThink")
	p.Functions = append(p.Functions, progs.Function{FirstStatement: 0, SName: ppt})
	return p
}

// runPlayerPostThink dispatches PlayerPostThink once per active client edict
// (the weapon-fire half of SV_Physics_Client).
func TestRunPlayerPostThink_DispatchesForActiveClient(t *testing.T) {
	bsp := buildHostBSP(t, `{ "classname" "worldspawn" }`, 1)
	h := makeHostWithProgs(t, bsp, progsForPostThink(), 1)
	if err := h.SpawnServer("test", protocol.VersionNQ); err != nil {
		t.Fatalf("SpawnServer: %v", err)
	}
	if _, _, err := h.ConnectLoopback(); err != nil {
		t.Fatalf("ConnectLoopback: %v", err)
	}
	h.runPlayerPostThink()
	if h.LastPlayerPostThinks != 1 {
		t.Fatalf("LastPlayerPostThinks = %d, want 1", h.LastPlayerPostThinks)
	}
}

// A client without an edict is skipped (no dispatch, no panic).
func TestRunPlayerPostThink_SkipsClientWithoutEdict(t *testing.T) {
	bsp := buildHostBSP(t, `{ "classname" "worldspawn" }`, 1)
	h := makeHostWithProgs(t, bsp, progsForPostThink(), 1)
	if err := h.SpawnServer("test", protocol.VersionNQ); err != nil {
		t.Fatalf("SpawnServer: %v", err)
	}
	if _, _, err := h.ConnectLoopback(); err != nil {
		t.Fatalf("ConnectLoopback: %v", err)
	}
	h.Static.Clients[0].Edict = nil
	h.runPlayerPostThink()
	if h.LastPlayerPostThinks != 0 {
		t.Fatalf("LastPlayerPostThinks = %d, want 0 (client skipped)", h.LastPlayerPostThinks)
	}
}

// A progs without a PlayerPostThink function is a no-op (FindFunction < 1).
func TestRunPlayerPostThink_AbsentFunctionNoOp(t *testing.T) {
	bsp := buildHostBSP(t, `{ "classname" "worldspawn" }`, 1)
	h := makeHostWithProgs(t, bsp, progsForButtons(), 1) // no PlayerPostThink
	if err := h.SpawnServer("test", protocol.VersionNQ); err != nil {
		t.Fatalf("SpawnServer: %v", err)
	}
	if _, _, err := h.ConnectLoopback(); err != nil {
		t.Fatalf("ConnectLoopback: %v", err)
	}
	h.runPlayerPostThink()
	if h.LastPlayerPostThinks != 0 {
		t.Fatalf("LastPlayerPostThinks = %d, want 0 (no PlayerPostThink)", h.LastPlayerPostThinks)
	}
}

// A nil progsRef short-circuits before the FindFunction lookup.
func TestRunPlayerPostThink_NilProgsNoOp(t *testing.T) {
	h, _ := makeHost(t, nil, 1)
	h.SetProgs(nil)
	h.runPlayerPostThink()
	if h.LastPlayerPostThinks != 0 {
		t.Fatalf("LastPlayerPostThinks = %d, want 0", h.LastPlayerPostThinks)
	}
}
