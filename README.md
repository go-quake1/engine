<p align="center"><img src="https://raw.githubusercontent.com/go-quake1/brand/main/social/go-quake1.png" alt="go-quake1/engine" width="720"></p>

# go-quake1 / engine

[![6-arch CI](https://img.shields.io/badge/CI-amd64%20%C2%B7%20arm64%20%C2%B7%20riscv64%20%C2%B7%20loong64%20%C2%B7%20ppc64le%20%C2%B7%20s390x-success)](https://github.com/go-quake1/engine/actions)
[![CGO](https://img.shields.io/badge/CGO__ENABLED-0-blue)](#)
[![Coverage](https://img.shields.io/badge/coverage-100%25%20on%2022%2F23-brightgreen)](#)
[![License](https://img.shields.io/badge/license-GPL--2.0--or--later%20(BSD--3%20parts)-informational)](NOTICE.md)

Pure-Go [Quake](https://en.wikipedia.org/wiki/Quake_(video_game)) 1
(id Tech 1, 1996) engine for bare-metal TamaGo + UEFI, **hand-ported**
from the [tyrquake][tyr] NetQuake (single-player) branch and wrapped
in [cloud-boot][cb] virtio adapters.

Sibling of [go-doom/engine][godoom] in the [DOOM → Quake roadmap][roadmap].
Family siblings: [go-quake2][gq2] (id Tech 2 — reserved) and
[go-quake3][gq3] (id Tech 3 — reserved).

[tyr]: https://github.com/sezero/tyrquake
[cb]: https://github.com/cloud-boot
[godoom]: https://github.com/go-doom/engine
[roadmap]: https://github.com/cloud-boot/docs/blob/main/docs/architecture/quake-roadmap.md
[gq2]: https://github.com/go-quake2
[gq3]: https://github.com/go-quake3

## Status

**Phase Q-1a in flight.** Hand-porting tyrquake-NQ to pure Go, one
module at a time, with parity tests against the upstream C behaviour.

| Component | State |
|---|---|
| Porting conventions ([CONVENTIONS.md](CONVENTIONS.md)) | done |
| `reference/` tyrquake source mirror (commit `6531579`) | done |
| common: `mathlib`, `crc`, `cmd`, `cvar`, `zone`, `qstr`, `sizebuf`, `qpath`, `qparse`, `msg`, `qargs`, `pak`, `vfs`, `wad`, `keys`, `protocol`, `anorms` | done (17 packages · 100% cov) |
| models: `bspfile`, `mdl`, `spr`, `model` (magic-bytes dispatcher) | done (4 packages · 100% cov) |
| `bsptrace` (Mod_HullPointContents + Mod_TraceHull) | done (87.8% cov — backfill backlog) |
| `progs` (QuakeC VM — 65 opcodes + edicts + parser + 9 math builtins) | done (100% cov) |
| `server` (host + sv_world + sv_main + sv_phys + sv_user) | pending |
| `client` + soft renderer (`r_*`, `d_*`, `cl_*`) | pending |
| `sound` (snd_dma + snd_mem + snd_mix) | pending |
| 64 remaining QuakeC builtins (need sv_world for traceline/findradius/...) | pending |
| `backend/tamago/` virtio adapters | pending (mirrors godoom shape) |
| `embedpak/` shareware loader | pending |
| `cmd/harvest-reference` oracle | pending |
| `phaseQ1_oci_quake1_soft_boot.go` (in cloud-boot/tamago-uefi) | pending |
| provable-test gates A · B · C-1 · C-2 | pending |

## Why hand-port (not ccgo, not ironwail-go)

Phase Q-1a's [open question Q-1][roadmap-q1] was first answered
"ccgo-transpile" — a mechanical C-to-Go translation via
`modernc.org/ccgo/v4` of a Linux-portable Quake source tree. We tried
two upstreams in earnest and documented why both failed:

- **quakeforge** uses C23 (`nullptr`, `#embed`, etc.); ccgo's parser
  predates C23. See [internal/transpile/FINDING-C23.md](internal/transpile/FINDING-C23.md).
- **tyrquake** is clean C99 but ccgo's internal checker drops symbols
  on real game-code constructs (extern globals, function-pointer
  dispatch tables, packed structs). The link stage then reports
  "undefined: com_argc" even though `com_argc` IS defined in `common.c`.
  See [internal/transpile/FINDING-CCGO-LIMITS.md](internal/transpile/FINDING-CCGO-LIMITS.md).

ccgo is production-quality for SQLite-class C (it's literally how
`modernc.org/sqlite` is built), but Quake's surface exposes the v4
checker's soft spots faster than ccgo's curated test corpus does.

The remaining options were:

| Option | Velocity | Audit posture | Picked |
|---|---|---|---|
| Fork `darkliquid/ironwail-go` | fastest | low (AI-assisted upstream, unreviewed) | no |
| Hand-port (LLM-assisted, from tyrquake-NQ) | medium | high (every line under operator's git history) | **YES** |
| Wait on ccgo upstream maturity | slowest | high | deferred (track as a watch) |

The hand-port also gives us natural alignment with TamaGo constraints
(no malloc churn, no goroutines for the main loop, careful with `go`
keyword pressure on the bare-metal scheduler) that no transpiled engine
would ship with out of the box.

[roadmap-q1]: https://github.com/cloud-boot/docs/blob/main/docs/architecture/quake-roadmap.md

## How a port lands

For each tyrquake C module:

1. **Mirror** the C source verbatim into `reference/` (no edits) so the
   diff against upstream stays inspectable.
2. **Port** the module under `<module>/<module>.go` following
   the rules in [CONVENTIONS.md](CONVENTIONS.md). Hand-written or LLM-
   drafted then operator-reviewed; either way the final Go is
   operator-owned.
3. **Test** parity against the C upstream: feed the same input through
   both and assert byte-equal output where the operation is
   deterministic (math, hash, parse), or bounded-tolerance where it is
   not (float trig).
4. **Coverage** target = 100% of the new Go package (the project-wide
   convention inherited from go-virtio and go-deltasync).
5. **Commit** with a `port: <module>` prefix. Each commit lands one
   buildable, tested module so bisecting the engine remains practical.

## Data: shareware `pak0.pak`

`pak0.pak` for the shareware Episode 1 (Dimension of the Doomed) is
freely redistributable per id Software's grant and lands in-tree under
`embedpak/`. Operators can override at boot with their own pak0+pak1
via a probe env. CI gates always exercise the committed shareware pak
so the reference oracle is reproducible.

## Play in a browser (wasm)

The engine ships a `GOOS=js GOARCH=wasm` build alongside the bare-metal
TamaGo target. The `backend/wasm` adapter wires `backend.Backend` onto
Canvas2D (framebuffer), DOM events (input, with Pointer Lock for
mouse-look), and WebAudio (sound). Two top-level tasks drive it:

```sh
task build-wasm   # compiles cmd/quake-wasm -> cmd/quake-wasm/web/quake.wasm
task serve-wasm   # binds localhost:8080 to cmd/quake-wasm/web/
```

then open `http://localhost:8080/` in any modern browser. `task wasm`
chains both. The single-step build is large (~180 MB; the Go runtime
ships the full stdlib in wasm builds); first-load is a one-shot
cache.

## Streaming assets from an OCI registry

The default wasm build embeds `embedpak/empty.pak` + the music
tracks, so the binary balloons to ~180 MB the moment a real
shareware pak is dropped in. For browser distribution that's
wasteful — every page load re-fetches the whole thing. Build
with `-tags no_embed_assets` instead and the wasm payload stays
~11 MB; the pak + music tracks ship as individual layers in an
OCI Distribution v2 registry (any registry that speaks the
[OCI spec][oci-spec]: Distribution v2, ghcr.io, harbor,
self-hosted, …) and the wasm fetches them on demand the first
time a file is `Open`'d, with an IndexedDB write-through cache so
reloads in the same browser skip the network round-trip.

[oci-spec]: https://github.com/opencontainers/distribution-spec

```sh
# 1. Pack the local pak + music into an OCI image-layout dir.
go run ./cmd/oci-pack-quake --out _oci --ref quake-assets:latest

# 2. Push the layout to any registry. oras [1] is the easy path:
oras push localhost:5000/quake-assets:latest \
  --manifest-config /dev/null:application/vnd.go-quake1.config.v1+json \
  $(find _oci/blobs/sha256 -type f)

# 3. Build the slim wasm with the registry URL baked in. The
#    -X main.OCIReference linker flag is what tells the binary
#    which registry to hit at boot.
OCI_REFERENCE=http://localhost:5000/quake-assets:latest task build-wasm-oci

# 4. Serve the wasm + open in a browser.
task serve-wasm
```

[1]: https://oras.land

The runtime falls back to the embedded pak (and from there to the
synthetic-asset bootstrap) on any OCI failure, so an unreachable
registry never aborts the boot — it just degrades to the local
default. See `engine/ociassets/` for the package + tests.

## Renderers and frame timing

Each frame goes through `render.Renderer`: the 3D scene, the 2D
layers, then the RGBA resolve. The software renderer (`soft`) is the
default and the fallback. A WebGPU renderer (`gpu`, GLQuake-style) is
planned; until it lands, asking for `gpu` runs `soft` and the log says
why. `runner.SetupOpts.Renderer` selects it; the wasmbox client reads
`self.__quake_renderer` (`"soft"` or `"gpu"`) in its worker.

`render.FrameTimer` records where each frame's time goes: `sim`,
`world`, `models`, `2d` and `present`, plus the frame interval. The
wasmbox client always measures:

- `self.__quakeStats()` in the Quake worker returns the current
  window as JSON: fps, and mean / p50 / p95 / p99 / max in ms for the
  frame interval, the frame's work and each stage;
- `self.__quakeStatsReset()` starts a new window;
- every 10 s the worker console gets a `frame stats {...}` line with
  the window just finished.

## Provable test protocol (inherited)

Every Quake phase inherits the four-gate protocol shipped with DOOM
([doom-provable-protocol.md][protocol]):

- **GATE A** — engine determinism, BYTE-EQUAL frames at checkpoint tics
- **GATE B** — guest virtio-gpu fidelity, χ² ≤ tolerance
- **GATE C-1** — audio event stream, BYTE-EQUAL CacheSound/PlaySound log
- **GATE C-2** — guest WAV bounded tolerance (per-second RMS envelope)

[protocol]: https://github.com/cloud-boot/docs/blob/main/docs/architecture/doom-provable-protocol.md

## License

The repository as a whole is **GPL-2.0-or-later** ([LICENSE](LICENSE)). It
is a port of id Software's Quake engine by way of tyrquake, and GPL-2.0 §2
puts the whole of such a work under that licence when it is distributed.

- Each file says which licence it is under, in an `SPDX-License-Identifier`
  header, and that header is authoritative.
- **GPL-2.0-or-later:** the engine packages and most of the tree, including
  `backend/`, plus `reference/` (tyrquake's C, verbatim, with id Software's
  notice in each file).
- **BSD-3-Clause** ([LICENSE-BSD](LICENSE-BSD)): the wrapper code written
  here, which can be taken out on its own: all of `ociassets/` and
  `embedmusic/`, most of `runner/` and `cmd/`, and the marked files in
  `embedpak/`, `server/` and `quake-tamago/`.

[NOTICE.md](NOTICE.md) has the table.
