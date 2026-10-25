// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

// Package embedmusic embeds the optional Quake CD music tracks
// (track02.ogg .. track11.ogg) into the binary so the runtime can
// stream them through the music subsystem without a host filesystem.
//
// The .ogg files are NOT committed to git -- they are sourced
// separately from the user's own Quake archive (e.g. the id1/music/
// directory inside the Quake.zip distributable on archive.org). Tiny
// placeholder files are committed so //go:embed compiles even on a
// fresh checkout; replacing them with real OGG Vorbis bytes makes the
// music play. With placeholders, the music loader sees a malformed
// stream + silently skips (graceful-degradation path in
// music.Streamer).
//
// Layout: track02.ogg through track11.ogg, mirroring vanilla Quake's
// CDA track numbering (track 1 is the data track on the original CD;
// music starts at track 2).
//
// Build with -tags no_embed_assets to strip the embed directive
// (the wasm OCI-streaming build does that, since shipping ~84 MB of
// music in the payload defeats the purpose of streaming). The
// exported MusicFS keeps the same type either way -- callers see an
// empty embed.FS instead of the embedded tracks.
package embedmusic
