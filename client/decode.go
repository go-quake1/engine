// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package client

import (
	"errors"
	"fmt"

	"github.com/go-quake1/engine/msg"
	"github.com/go-quake1/engine/protocol"
)

// ErrUnknownSvc is returned by [SvcReader.Next] when the cmd byte
// at the cursor does not dispatch to any svc_* opcode this build
// recognises. The cursor IS advanced past the offending byte so
// callers may continue scanning the buffer; the upstream's
// CL_ParseServerMessage Host_Error's instead, which terminates the
// whole connection.
var ErrUnknownSvc = errors.New("client: unknown svc opcode")

// ErrEOF is returned by [SvcReader.Next] when the reader has no
// bytes left at the current cursor. The cursor is not advanced.
var ErrEOF = errors.New("client: unexpected EOF reading svc opcode")

// ErrCorruptMessage wraps a per-message decoder's "ran off the end
// while reading my own body" failure. The Reader's Bad flag is
// flipped by the offending [msg.Reader].Read* call; the decoder
// surfaces the condition via this sentinel so callers can
// distinguish "unknown opcode" (ErrUnknownSvc) from "known opcode
// with a truncated body" (ErrCorruptMessage).
var ErrCorruptMessage = errors.New("client: short read inside svc body")

// Decoded is the marker interface every Decoded* struct implements.
// Callers switch on the concrete type to handle each opcode.
type Decoded interface{ isDecoded() }

// DecodedNop is the empty-body svc_nop keepalive ping. tyrquake:
// svc_nop arm of CL_ParseServerMessage.
type DecodedNop struct{}

// DecodedDisconnect is the empty-body svc_disconnect. tyrquake:
// svc_disconnect arm (upstream Host_EndGame's; the Go port returns
// it as a typed value).
type DecodedDisconnect struct{}

// DecodedKilledMonster is the empty-body svc_killedmonster (HUD
// "kill counter" tick). tyrquake: svc_killedmonster arm.
type DecodedKilledMonster struct{}

// DecodedFoundSecret is the empty-body svc_foundsecret (HUD
// "secret found" announcement + counter tick). tyrquake:
// svc_foundsecret arm.
type DecodedFoundSecret struct{}

// DecodedSellScreen is the empty-body svc_sellscreen (shareware
// end-of-episode upgrade prompt). tyrquake: svc_sellscreen arm.
type DecodedSellScreen struct{}

// DecodedSetView carries the entity index the client should bind
// its render-camera to. tyrquake: svc_setview arm.
type DecodedSetView struct{ EntityNum int }

// DecodedSignonNum carries the signon-handshake stage byte (1..4).
// tyrquake: svc_signonnum arm.
type DecodedSignonNum struct{ Stage int }

// DecodedPrint carries the console-print payload. tyrquake:
// svc_print arm (the C side calls Con_Printf; the Go port surfaces
// the string).
type DecodedPrint struct{ Text string }

// DecodedStuffText carries the console-stuff payload (commands the
// client will feed to Cbuf_AddText). tyrquake: svc_stufftext arm.
type DecodedStuffText struct{ Text string }

// DecodedIntermission is the empty-body svc_intermission marker.
// On receipt the client hides the in-game HUD and switches the
// renderer into intermission mode (end-of-level scoreboard:
// time taken, secrets X/Y, monsters X/Y). The stats themselves are
// pulled from the client's cached stat bank (svc_updatestat pushes
// that landed before the intermission marker); no wire payload is
// needed. tyrquake: svc_intermission arm + the intermission
// rendering in screen.c.
type DecodedIntermission struct{}

// DecodedFinale carries the end-of-episode banner text. tyrquake:
// svc_finale arm. On receipt the client flips into the same
// "intermission" mode DecodedIntermission triggers but with a
// caller-supplied multi-line credits string in place of the
// per-map scoreboard. The Apply arm stashes the text in
// [State.IntermissionText] and sets [State.Intermission] = true.
type DecodedFinale struct{ Text string }

// DecodedCutscene carries the cutscene caption text. tyrquake:
// svc_cutscene arm.
type DecodedCutscene struct{ Text string }

// DecodedCDTrack carries the (track, loopTrack) byte pair from
// svc_cdtrack. The client opens "music/trackXX.ogg" (XX = Track
// zero-padded to two digits) off the asset source and streams the
// decoded PCM through the mixer alongside the per-tic SFX; on EOF the
// streamer falls back to LoopTrack (typically == Track for self-loop;
// 0 stops at EOF). Track == 0 silences any active music stream.
//
// In id Software's original Q1 the two bytes addressed an audio-CD
// track index (the 1996 retail discs shipped 10 score tracks on
// audio-CD tracks 2..11); every modern source port replaced the
// CD-DA dependency with .ogg files keyed by the same wire byte, and
// the Go port follows that convention.
//
// tyrquake: svc_cdtrack arm of CL_ParseServerMessage (the C upstream
// writes the two bytes onto cl.cdtrack + cl.looptrack then calls
// CDAudio_Play / BGM_PlayCDtrack to start the backend).
type DecodedCDTrack struct {
	Track     int // 0..255; 0 = silence
	LoopTrack int // 0..255; track to switch to when Track reaches EOF
}

// DecodedLightStyle carries svc_lightstyle: the style number and its animation
// string (reference/NQ/cl_parse.c:1053-1061).
type DecodedLightStyle struct {
	Style int
	Map   string
}

// DecodedCenterPrint carries the svc_centerprint payload (the
// "you got the shotgun" / intermission banner the server pushes
// for the renderer to overlay horizontally-centered at ~40% of
// screen height). tyrquake: svc_centerprint arm + SCR_CenterPrint
// in screen.c. The Go port surfaces the text; the Apply arm
// stamps the per-frame expiry into [State.CenterPrintExpiry].
type DecodedCenterPrint struct{ Text string }

// DecodedUpdateName carries a scoreboard-slot name change.
// tyrquake: svc_updatename arm.
type DecodedUpdateName struct {
	Slot int
	Name string
}

// DecodedUpdateColors carries a scoreboard-slot color (packed
// shirt|pants nibble pair). tyrquake: svc_updatecolors arm.
type DecodedUpdateColors struct {
	Slot   int
	Colors int
}

// DecodedUpdateFrags carries a scoreboard-slot frag-count update.
// tyrquake: svc_updatefrags arm.
type DecodedUpdateFrags struct {
	Slot  int
	Frags int
}

// DecodedUpdateStat carries a per-stat int32 push (HP, ammo, ...).
// tyrquake: svc_updatestat arm.
type DecodedUpdateStat struct {
	Stat  int
	Value int32
}

// DecodedParticle is one svc_particle temp-entity burst. Dir is
// already de-quantized (raw_char / 16, restoring the float the
// encoder rounded down). tyrquake: svc_particle arm +
// R_ParseParticleEffect in NQ/r_part.c.
type DecodedParticle struct {
	Origin [3]float32
	Dir    [3]float32
	Color  int
	Count  int
}

// DecodedSound is one svc_sound start-sound event. Volume is the
// 0..255 wire byte (the encoder writes DefaultSoundVolume=255 when
// the bitmask omits it); Atten is in the protocol's float scale
// (DefaultSoundAttenuation=1.0 when omitted). tyrquake:
// CL_ParseStartSoundPacket.
type DecodedSound struct {
	EntityIdx int
	Channel   int
	SoundNum  int
	Origin    [3]float32
	Volume    int
	Atten     float32
}

// DecodedServerInfo is the per-spawn handshake payload (protocol
// version + max clients + gametype + level banner + sentinel-
// terminated precache lists). tyrquake: CL_ParseServerInfo.
//
// ModelPrecache + SoundPrecache match the wire layout, which omits
// slot 0 from both lists (slot 0 is the worldmodel for models, the
// "no sound" reserved entry for sounds; the encoder skips them on
// emit). Callers wanting a 1-indexed array should prepend a sentinel
// entry of their own.
type DecodedServerInfo struct {
	Protocol      int
	MaxClients    int
	GameType      int
	LevelName     string
	ModelPrecache []string
	SoundPrecache []string
}

// DecodedBaseline is one svc_spawnbaseline entity snapshot. Alpha
// is FITZ-only; this decoder is vanilla-NQ scoped, so it is always
// zero (= ENTALPHA_DEFAULT). tyrquake: CL_ParseBaseline (with the
// FITZ extend bits skipped).
type DecodedBaseline struct {
	EntityNum int
	ModelIdx  int
	Frame     int
	ColorMap  int
	SkinNum   int
	Origin    [3]float32
	Angles    [3]float32
	Alpha     int
}

// DecodedUpdate is one fast-update per-entity delta. The Bits field
// is the 16-bit U_* mask the encoder packed (USignal stripped); only
// the fields whose U_* bit is set in Bits carry meaningful data --
// the rest stay at the zero value the caller should resolve against
// the entity's last-known baseline. tyrquake: CL_ParseUpdate (with
// the FITZ extend bits and the 24-bit-wide bitmask skipped -- this
// is the vanilla-NQ shape).
type DecodedUpdate struct {
	EntityNum int
	Bits      int
	Origin    [3]float32
	Angles    [3]float32
	Model     int
	Frame     int
	ColorMap  int
	Skin      int
	Effects   int
}

// DecodedClientData is one svc_clientdata per-tick player-state
// snapshot. Bits is the SU_* mask the encoder packed. Velocity is
// already multiplied by 16 (the encoder quantises by /16; this
// decoder restores the original scale). tyrquake: CL_ParseClientdata
// (vanilla-NQ shape; the FITZ extend bits are skipped).
type DecodedClientData struct {
	Bits             int
	ViewHeightOffset float32
	IdealPitch       float32
	PunchAngle       [3]float32
	Velocity         [3]float32
	Items            int32
	OnGround         bool
	InWater          bool
	WeaponFrame      int
	ArmorValue       int
	WeaponModel      int
	Health           int
	CurrentAmmo      int
	Ammo             [4]int
	ActiveWeapon     int
}

func (DecodedNop) isDecoded()           {}
func (DecodedDisconnect) isDecoded()    {}
func (DecodedKilledMonster) isDecoded() {}
func (DecodedFoundSecret) isDecoded()   {}
func (DecodedSellScreen) isDecoded()    {}
func (DecodedSetView) isDecoded()       {}
func (DecodedSignonNum) isDecoded()     {}
func (DecodedPrint) isDecoded()         {}
func (DecodedStuffText) isDecoded()     {}
func (DecodedFinale) isDecoded()        {}
func (DecodedIntermission) isDecoded()  {}
func (DecodedCutscene) isDecoded()      {}
func (DecodedCDTrack) isDecoded()       {}
func (DecodedLightStyle) isDecoded()    {}
func (DecodedCenterPrint) isDecoded()   {}
func (DecodedUpdateName) isDecoded()    {}
func (DecodedUpdateColors) isDecoded()  {}
func (DecodedUpdateFrags) isDecoded()   {}
func (DecodedUpdateStat) isDecoded()    {}
func (DecodedParticle) isDecoded()      {}
func (DecodedSound) isDecoded()         {}
func (DecodedServerInfo) isDecoded()    {}
func (DecodedBaseline) isDecoded()      {}
func (DecodedUpdate) isDecoded()        {}
func (DecodedClientData) isDecoded()    {}

// SvcReader wraps a [msg.Reader] with the cmd-dispatch logic for
// the server-to-client wire protocol. The caller owns the reader;
// SvcReader only advances its cursor.
type SvcReader struct {
	R *msg.Reader
}

// Next reads the next svc_* message at the cursor and returns its
// decoded value. The cursor is advanced past every byte the message
// occupied (including its cmd byte).
//
// Return shape:
//
//   - (Decoded, nil) on success
//   - (nil, ErrEOF) when the cursor is at end-of-buffer (no cmd byte left)
//   - (nil, ErrUnknownSvc) when the cmd byte does not dispatch to a
//     supported opcode; the cursor IS advanced past the cmd byte
//   - (nil, ErrCorruptMessage) when a supported opcode's body is
//     truncated mid-read (the reader's Bad flag tripped)
//
// Supported opcodes (23):
//
//	svc_nop, svc_disconnect, svc_updatestat, svc_setview, svc_sound,
//	svc_print, svc_stufftext, svc_serverinfo, svc_updatename,
//	svc_updatefrags, svc_clientdata, svc_updatecolors, svc_particle,
//	svc_spawnbaseline, svc_signonnum, svc_killedmonster,
//	svc_foundsecret, svc_intermission, svc_finale, svc_sellscreen, svc_cutscene,
//	svc_centerprint, svc_temp_entity, svc_update (the high-bit
//	fast-update; cmd>=128).
//
// The proto argument is the active protocol version (one of
// protocol.Version*); it selects the per-protocol field widths in
// the svc_sound + svc_spawnbaseline decoders.
func (sr *SvcReader) Next(proto int) (Decoded, error) {
	// ReadU8 sets Bad without advancing pos when the cursor is at
	// EOF, so a single read-then-check is the canonical EOF probe.
	cmd := sr.R.ReadU8()
	if sr.R.Bad() {
		return nil, ErrEOF
	}

	// High bit of cmd flags the fast-update opcode -- the encoder
	// always OR's USignal (=128) into the first byte. The remaining
	// 7 bits are the low byte of the U_* bitmask.
	if cmd&0x80 != 0 {
		return sr.decodeUpdate(cmd & 0x7f)
	}

	switch cmd {
	case protocol.SvcNop:
		return DecodedNop{}, nil
	case protocol.SvcDisconnect:
		return DecodedDisconnect{}, nil
	case protocol.SvcKilledMonster:
		return DecodedKilledMonster{}, nil
	case protocol.SvcFoundSecret:
		return DecodedFoundSecret{}, nil
	case protocol.SvcIntermission:
		return DecodedIntermission{}, nil
	case protocol.SvcSellScreen:
		return DecodedSellScreen{}, nil
	case protocol.SvcSetView:
		return sr.decodeSetView()
	case protocol.SvcSignonNum:
		return sr.decodeSignonNum()
	case protocol.SvcPrint:
		return sr.decodeString(decodeKindPrint)
	case protocol.SvcStuffText:
		return sr.decodeString(decodeKindStuff)
	case protocol.SvcFinale:
		return sr.decodeString(decodeKindFinale)
	case protocol.SvcCutscene:
		return sr.decodeString(decodeKindCutscene)
	case protocol.SvcCenterPrint:
		return sr.decodeString(decodeKindCenterPrint)
	case protocol.SvcUpdateName:
		return sr.decodeUpdateName()
	case protocol.SvcUpdateColors:
		return sr.decodeUpdateColors()
	case protocol.SvcUpdateFrags:
		return sr.decodeUpdateFrags()
	case protocol.SvcUpdateStat:
		return sr.decodeUpdateStat()
	case protocol.SvcParticle:
		return sr.decodeParticle()
	case protocol.SvcSound:
		return sr.decodeSound(proto)
	case protocol.SvcServerInfo:
		return sr.decodeServerInfo()
	case protocol.SvcSpawnBaseline:
		return sr.decodeBaseline(proto)
	case protocol.SvcClientData:
		return sr.decodeClientData()
	case protocol.SvcTempEntity:
		return sr.decodeTempEntity()
	case protocol.SvcCDTrack:
		return sr.decodeCDTrack()
	case protocol.SvcLightStyle:
		return sr.decodeLightStyle()
	}
	return nil, fmt.Errorf("%w: cmd=%d", ErrUnknownSvc, cmd)
}

// stringKind tags which Decoded* string-payload variant a [SvcReader]
// helper should emit. The four svc_* opcodes that carry "byte + NUL-
// terminated string" payloads (print, stufftext, finale, cutscene)
// share the same wire shape; the kind discriminator keeps four
// near-identical helpers folded into one.
type stringKind int

const (
	decodeKindPrint stringKind = iota
	decodeKindStuff
	decodeKindFinale
	decodeKindCutscene
	decodeKindCenterPrint
)

// decodeString reads a NUL-terminated string body and packs it into
// the right Decoded* variant per the kind tag.
func (sr *SvcReader) decodeString(kind stringKind) (Decoded, error) {
	s := sr.R.ReadString()
	if sr.R.Bad() {
		return nil, ErrCorruptMessage
	}
	switch kind {
	case decodeKindPrint:
		return DecodedPrint{Text: s}, nil
	case decodeKindStuff:
		return DecodedStuffText{Text: s}, nil
	case decodeKindFinale:
		return DecodedFinale{Text: s}, nil
	case decodeKindCutscene:
		return DecodedCutscene{Text: s}, nil
	}
	return DecodedCenterPrint{Text: s}, nil
}

// decodeSetView reads a short entityNum.
func (sr *SvcReader) decodeSetView() (Decoded, error) {
	n := sr.R.ReadShort()
	if sr.R.Bad() {
		return nil, ErrCorruptMessage
	}
	// The encoder writes an unsigned short (entityNum in [0, 0xffff]
	// is validated server-side); the reader returns a signed int16
	// which silently negates entries >= 32768. Re-widen to a 16-bit
	// unsigned int so the decoded value matches the encoder's input.
	return DecodedSetView{EntityNum: int(uint16(int16(n)))}, nil
}

// decodeSignonNum reads a single stage byte.
func (sr *SvcReader) decodeSignonNum() (Decoded, error) {
	stage := sr.R.ReadU8()
	if sr.R.Bad() {
		return nil, ErrCorruptMessage
	}
	return DecodedSignonNum{Stage: stage}, nil
}

// decodeUpdateName reads slot byte + NUL-terminated name.
func (sr *SvcReader) decodeUpdateName() (Decoded, error) {
	slot := sr.R.ReadU8()
	name := sr.R.ReadString()
	if sr.R.Bad() {
		return nil, ErrCorruptMessage
	}
	return DecodedUpdateName{Slot: slot, Name: name}, nil
}

// decodeUpdateColors reads slot byte + colors byte.
func (sr *SvcReader) decodeUpdateColors() (Decoded, error) {
	slot := sr.R.ReadU8()
	colors := sr.R.ReadU8()
	if sr.R.Bad() {
		return nil, ErrCorruptMessage
	}
	return DecodedUpdateColors{Slot: slot, Colors: colors}, nil
}

// decodeUpdateFrags reads slot byte + signed short frags.
func (sr *SvcReader) decodeUpdateFrags() (Decoded, error) {
	slot := sr.R.ReadU8()
	frags := sr.R.ReadShort()
	if sr.R.Bad() {
		return nil, ErrCorruptMessage
	}
	return DecodedUpdateFrags{Slot: slot, Frags: frags}, nil
}

// decodeUpdateStat reads stat byte + int32 value.
func (sr *SvcReader) decodeUpdateStat() (Decoded, error) {
	stat := sr.R.ReadU8()
	value := sr.R.ReadLong()
	if sr.R.Bad() {
		return nil, ErrCorruptMessage
	}
	return DecodedUpdateStat{Stat: stat, Value: value}, nil
}

// decodeCDTrack reads the (track, loopTrack) byte pair from
// svc_cdtrack. Both bytes are wire-unsigned; the upstream's MSG_ReadByte
// returns an int in [0, 255], and the Go port preserves that range
// (the decoder does NOT validate the values -- the active streamer
// degrades gracefully when the named track is missing from the asset
// source, so an arbitrary byte is acceptable on the wire).
func (sr *SvcReader) decodeCDTrack() (Decoded, error) {
	track := sr.R.ReadU8()
	loopTrack := sr.R.ReadU8()
	if sr.R.Bad() {
		return nil, ErrCorruptMessage
	}
	return DecodedCDTrack{Track: track, LoopTrack: loopTrack}, nil
}

// ErrLightStyleRange is returned for an svc_lightstyle whose style is not below
// MaxLightStyles: the C client's Sys_Error("svc_lightstyle > MAX_LIGHTSTYLES").
var ErrLightStyleRange = errors.New("client: svc_lightstyle > MAX_LIGHTSTYLES")

// decodeLightStyle reads the style byte and the animation string
// (cl_parse.c:1054-1057).
func (sr *SvcReader) decodeLightStyle() (Decoded, error) {
	style := sr.R.ReadU8()
	if sr.R.Bad() {
		return nil, ErrCorruptMessage
	}
	if style >= MaxLightStyles {
		return nil, fmt.Errorf("%w: %d", ErrLightStyleRange, style)
	}
	text := sr.R.ReadString()
	if sr.R.Bad() {
		return nil, ErrCorruptMessage
	}
	return DecodedLightStyle{Style: style, Map: text}, nil
}

// decodeParticle reads the 11-byte svc_particle body (cmd byte
// already consumed by Next).
func (sr *SvcReader) decodeParticle() (Decoded, error) {
	var origin [3]float32
	var dir [3]float32
	for i := 0; i < 3; i++ {
		origin[i] = sr.R.ReadCoord()
	}
	for i := 0; i < 3; i++ {
		// dir was encoded as clamp(f*16) to int8; restore by /16.
		dir[i] = float32(sr.R.ReadChar()) / 16.0
	}
	count := sr.R.ReadU8()
	color := sr.R.ReadU8()
	if sr.R.Bad() {
		return nil, ErrCorruptMessage
	}
	return DecodedParticle{Origin: origin, Dir: dir, Color: color, Count: count}, nil
}

// readSoundNum mirrors server.writeSoundNum (sound.go) for the
// decode side: NQ/BJP=byte, BJP2/BJP3=short, FITZ=short iff the
// SndFitzLargeSound bit is set else byte.
func readSoundNum(r *msg.Reader, fieldMask, proto int) (int, error) {
	switch proto {
	case protocol.VersionNQ, protocol.VersionBJP:
		return r.ReadU8(), nil
	case protocol.VersionBJP2, protocol.VersionBJP3:
		return int(uint16(int16(r.ReadShort()))), nil
	case protocol.VersionFitz:
		if fieldMask&protocol.SndFitzLargeSound != 0 {
			return int(uint16(int16(r.ReadShort()))), nil
		}
		return r.ReadU8(), nil
	}
	return 0, fmt.Errorf("%w: proto=%d", ErrUnknownSvc, proto)
}

// decodeSound reads the variable-length svc_sound body. proto
// selects the per-protocol sound-num width (see [readSoundNum]).
func (sr *SvcReader) decodeSound(proto int) (Decoded, error) {
	fieldMask := sr.R.ReadU8()
	if sr.R.Bad() {
		return nil, ErrCorruptMessage
	}

	volume := protocol.DefaultSoundVolume
	if fieldMask&protocol.SndVolume != 0 {
		volume = sr.R.ReadU8()
	}
	atten := float32(protocol.DefaultSoundAttenuation)
	if fieldMask&protocol.SndAttenuation != 0 {
		atten = float32(sr.R.ReadU8()) / 64.0
	}

	var entIdx, channel int
	if proto == protocol.VersionFitz && fieldMask&protocol.SndFitzLargeEntity != 0 {
		entIdx = int(uint16(int16(sr.R.ReadShort())))
		channel = sr.R.ReadU8()
	} else {
		packed := sr.R.ReadShort()
		entIdx = packed >> 3
		channel = packed & 7
	}

	soundNum, err := readSoundNum(sr.R, fieldMask, proto)
	if err != nil {
		return nil, err
	}

	var origin [3]float32
	for i := 0; i < 3; i++ {
		origin[i] = sr.R.ReadCoord()
	}
	if sr.R.Bad() {
		return nil, ErrCorruptMessage
	}
	return DecodedSound{
		EntityIdx: entIdx,
		Channel:   channel,
		SoundNum:  soundNum,
		Origin:    origin,
		Volume:    volume,
		Atten:     atten,
	}, nil
}

// decodeServerInfo reads the per-spawn handshake body.
//
// Wire shape (mirrors server.EncodeServerInfo):
//
//	long    protocol
//	byte    maxclients
//	byte    gametype
//	string  levelname (NUL-terminated)
//	string* model precache (until empty string)
//	string* sound precache (until empty string)
//
// Note: the encoder appends a trailing svc_signonnum byte + stage 1
// byte AFTER the serverinfo body proper. Those two bytes are a
// SEPARATE message and are NOT consumed here -- the next Next()
// call will see them as a DecodedSignonNum{Stage: 1}.
func (sr *SvcReader) decodeServerInfo() (Decoded, error) {
	proto := int(sr.R.ReadLong())
	maxClients := sr.R.ReadU8()
	gameType := sr.R.ReadU8()
	levelName := sr.R.ReadString()
	if sr.R.Bad() {
		return nil, ErrCorruptMessage
	}

	models, err := readStringList(sr.R)
	if err != nil {
		return nil, err
	}
	sounds, err := readStringList(sr.R)
	if err != nil {
		return nil, err
	}

	return DecodedServerInfo{
		Protocol:      proto,
		MaxClients:    maxClients,
		GameType:      gameType,
		LevelName:     levelName,
		ModelPrecache: models,
		SoundPrecache: sounds,
	}, nil
}

// readStringList reads a sentinel-terminated (empty-string-terminated)
// list of NUL-delimited strings. The terminating empty string is
// consumed and NOT included in the returned slice.
func readStringList(r *msg.Reader) ([]string, error) {
	var out []string
	for {
		s := r.ReadString()
		if r.Bad() {
			return nil, ErrCorruptMessage
		}
		if s == "" {
			return out, nil
		}
		out = append(out, s)
	}
}

// readBaselineModelIndex mirrors server.writeBaselineModelIndex:
// NQ=byte, BJP*=short, FITZ=short-iff-LARGEMODEL-else-byte. (The
// FITZ branch is reachable only via the deferred
// svc_fitz_spawnbaseline2 path; vanilla svc_spawnbaseline always
// takes the byte branch on FITZ.)
func readBaselineModelIndex(r *msg.Reader, proto int) int {
	switch proto {
	case protocol.VersionBJP, protocol.VersionBJP2, protocol.VersionBJP3:
		return int(uint16(int16(r.ReadShort())))
	}
	// VersionNQ / VersionFitz (vanilla baseline) / unknown -> single byte.
	return r.ReadU8()
}

// decodeBaseline reads the svc_spawnbaseline body (the FITZ-bits
// extension svc_fitz_spawnbaseline2 path is deferred -- this is the
// vanilla, no-extra-bits variant). Layout:
//
//	short  entityNum
//	byte   modelIdx (NQ/FITZ) | short (BJP*)
//	byte   frame
//	byte   colormap
//	byte   skinnum
//	for axis in 0..2: coord origin[axis] + angle angles[axis]
//
// Alpha is the FITZ-only ENTALPHA_DEFAULT (= 0) for vanilla
// baselines; this decoder always returns 0.
func (sr *SvcReader) decodeBaseline(proto int) (Decoded, error) {
	entNum := int(uint16(int16(sr.R.ReadShort())))
	modelIdx := readBaselineModelIndex(sr.R, proto)
	frame := sr.R.ReadU8()
	colorMap := sr.R.ReadU8()
	skinNum := sr.R.ReadU8()
	var origin, angles [3]float32
	for i := 0; i < 3; i++ {
		origin[i] = sr.R.ReadCoord()
		angles[i] = sr.R.ReadAngle()
	}
	if sr.R.Bad() {
		return nil, ErrCorruptMessage
	}
	return DecodedBaseline{
		EntityNum: entNum,
		ModelIdx:  modelIdx,
		Frame:     frame,
		ColorMap:  colorMap,
		SkinNum:   skinNum,
		Origin:    origin,
		Angles:    angles,
		Alpha:     protocol.EntAlphaDefault,
	}, nil
}

// decodeUpdate reads the variable-length fast-update body. cmdLow7
// is the low 7 bits of the cmd byte already consumed by Next (i.e.
// cmd ^ 0x80); the U_MOREBITS gated high byte may extend it to 16
// bits.
//
// Wire shape (mirrors server.EncodeUpdate):
//
//	[byte    bits >> 8]               iff U_MOREBITS in low byte
//	byte | short entityNum            short iff U_LONGENTITY
//	[byte   Model]                    iff U_MODEL
//	[byte   Frame]                    iff U_FRAME
//	[byte   ColorMap]                 iff U_COLORMAP
//	[byte   Skin]                     iff U_SKIN
//	[byte   Effects]                  iff U_EFFECTS
//	per axis (origin, angle):
//	  [coord origin[i]]               iff U_ORIGIN(1+i)
//	  [angle angles[i]]               iff U_ANGLE(1+i)
func (sr *SvcReader) decodeUpdate(cmdLow7 int) (Decoded, error) {
	bits := cmdLow7
	if bits&protocol.UMoreBits != 0 {
		bits |= sr.R.ReadU8() << 8
	}

	var entNum int
	if bits&protocol.ULongEntity != 0 {
		entNum = int(uint16(int16(sr.R.ReadShort())))
	} else {
		entNum = sr.R.ReadU8()
	}

	out := DecodedUpdate{EntityNum: entNum, Bits: bits}

	if bits&protocol.UModel != 0 {
		out.Model = sr.R.ReadU8()
	}
	if bits&protocol.UFrame != 0 {
		out.Frame = sr.R.ReadU8()
	}
	if bits&protocol.UColorMap != 0 {
		out.ColorMap = sr.R.ReadU8()
	}
	if bits&protocol.USkin != 0 {
		out.Skin = sr.R.ReadU8()
	}
	if bits&protocol.UEffects != 0 {
		out.Effects = sr.R.ReadU8()
	}

	if bits&protocol.UOrigin1 != 0 {
		out.Origin[0] = sr.R.ReadCoord()
	}
	if bits&protocol.UAngle1 != 0 {
		out.Angles[0] = sr.R.ReadAngle()
	}
	if bits&protocol.UOrigin2 != 0 {
		out.Origin[1] = sr.R.ReadCoord()
	}
	if bits&protocol.UAngle2 != 0 {
		out.Angles[1] = sr.R.ReadAngle()
	}
	if bits&protocol.UOrigin3 != 0 {
		out.Origin[2] = sr.R.ReadCoord()
	}
	if bits&protocol.UAngle3 != 0 {
		out.Angles[2] = sr.R.ReadAngle()
	}

	if sr.R.Bad() {
		return nil, ErrCorruptMessage
	}
	return out, nil
}

// decodeClientData reads the variable-length svc_clientdata body.
// Mirrors server.EncodeClientData: bits short, conditional char/
// byte fields gated by the SU_* bitmask, mandatory items long +
// health short + currentammo byte + 4-byte ammo array + activeweapon
// byte.
func (sr *SvcReader) decodeClientData() (Decoded, error) {
	bits := int(uint16(int16(sr.R.ReadShort())))

	out := DecodedClientData{Bits: bits}

	// Defaults that match the encoder's "omitted from wire" branches.
	out.ViewHeightOffset = protocol.DefaultViewHeight

	if bits&protocol.SUViewHeight != 0 {
		out.ViewHeightOffset = float32(sr.R.ReadChar())
	}
	if bits&protocol.SUIdealPitch != 0 {
		out.IdealPitch = float32(sr.R.ReadChar())
	}
	for i := 0; i < 3; i++ {
		if bits&(protocol.SUPunch1<<i) != 0 {
			out.PunchAngle[i] = float32(sr.R.ReadChar())
		}
		if bits&(protocol.SUVelocity1<<i) != 0 {
			// Encoder quantises via /16; restore *16.
			out.Velocity[i] = float32(sr.R.ReadChar()) * 16.0
		}
	}

	out.Items = sr.R.ReadLong()
	out.OnGround = bits&protocol.SUOnGround != 0
	out.InWater = bits&protocol.SUInWater != 0

	if bits&protocol.SUWeaponFrame != 0 {
		out.WeaponFrame = sr.R.ReadU8()
	}
	if bits&protocol.SUArmor != 0 {
		out.ArmorValue = sr.R.ReadU8()
	}
	if bits&protocol.SUWeapon != 0 {
		out.WeaponModel = sr.R.ReadU8()
	}

	out.Health = sr.R.ReadShort()
	out.CurrentAmmo = sr.R.ReadU8()
	for i := 0; i < 4; i++ {
		out.Ammo[i] = sr.R.ReadU8()
	}
	out.ActiveWeapon = sr.R.ReadU8()

	if sr.R.Bad() {
		return nil, ErrCorruptMessage
	}
	return out, nil
}
