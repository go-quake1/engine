# Licensing of go-quake1/engine

This repository is distributed as a whole under the **GNU General Public
License, version 2 or (at your option) any later version** (`LICENSE`).
Most of it is a port of id Software's Quake engine by way of tyrquake, and
GPL-2.0 §2 requires a work based on the Program to be distributed, as a
whole, on the terms of that licence.

Each source file states its own licence in an `SPDX-License-Identifier`
header, and that header is authoritative for the file:

| licence | where |
|:--|:--|
| `GPL-2.0-or-later` | the engine packages and most of the tree; every file of `reference/`, the verbatim tyrquake C (with id Software's notice in each file); `benchmarks/c/cbench_spans.c`, which copies tyrquake's `D_DrawSpans8` |
| `BSD-3-Clause` (`LICENSE-BSD`) | wrapper code written for this repository: all of `ociassets/` and `embedmusic/`, `runner/` (except one file), most of `cmd/`, and the files so marked in `embedpak/`, `server/` and `quake-tamago/` |

A file under `BSD-3-Clause` may be taken out and used on its own under that
licence, which GPL-2.0 §2 allows for "identifiable sections … reasonably
considered independent and separate works". Once it is distributed as part
of this whole, the whole is GPL-2.0-or-later.

The previous `LICENSE` was the BSD-3-Clause text with a scope note that
described an `engine/` subtree and an `engine/LICENSE.engine` file, neither
of which exists. It is now `LICENSE-BSD`, without that note.
