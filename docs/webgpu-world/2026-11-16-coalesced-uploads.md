# Coalesced surface uploads, and where the dynamic-light cost is (engine#106)

With three dynamic lights around the view a frame re-bakes about 100 to 120 surfaces and
`2026-11-16-dynamic-lights.md` measured the GPU world at 0.225 ms of CPU time and 1.41 ms of
wall time without lights against 1.715 ms and 3.72 ms with them, each surface uploaded with
its own `WriteTexture`. This change coalesces the uploads and measures what they cost.

## What changed

`Geometry.Coalesce` turns the frame's re-baked surfaces into fewer writes. The atlases already
hold the new texels (`Atlas.Pixels` is the CPU copy the bake writes into), so a write may cover
the texels between two surfaces: it sends what the atlas holds, which is what the GPU's atlas
holds once the other writes are in. The surfaces of a shelf share a row, so in each atlas they
are sorted by row then column, and a surface joins the write before it when the gap, times the
height the write then has, is at most `DefaultCoalesceSlack` (16 KiB). A write of one surface
is the surface itself, not copied; a merged one is the bounding rectangle, its rows copied
contiguous into a reused staging buffer. `Renderer.NoCoalesce` writes every surface alone as before.

The slack was chosen from the native counts over the 16 views with the three lights
(`TestCoalescedUploadsAreWhatTheAtlasHolds`, 1553 surfaces, 8.1 MB of changed texels):

| slack | writes | bytes sent |
|---|---|---|
| 0 (touching only) | 1185 | 8.1 MB |
| 4 KiB | 980 | 8.4 MB |
| **16 KiB** | **833** | **9.7 MB** |
| 64 KiB | 738 | 13.2 MB |
| 256 KiB | 607 | 34.0 MB |
| 1 MiB | 523 | 68.7 MB |

The lit surfaces of a frame are scattered over the atlas (it is packed tallest first, not by
position in the world), so two on one shelf are seldom neighbours: a write carries 1.9 surfaces
and 1.19 times the changed bytes, and a larger slack buys few writes for many bytes.

## Check (a real GPU, Apple M4 Max, Chrome, `QUAKE_REQUIRE_GPU=1`)

`quake-worldcheck --dlights` (the coalesced uploads are the default): 7 of 1,024,000 pixels
unexplained, as before; planted: the merged writes carry zeros (`--badCoalesce`): 298,809
unexplained (29.2 %), FAIL. Natively, applying a frame's writes to the atlases as the GPU had
them after the previous frame gives exactly the atlases the CPU holds, for the 16 views
(`TestCoalescedUploadsAreWhatTheAtlasHolds`), and the merge rules are pinned on a synthetic
atlas (`TestCoalesce`, `TestCoalesceBufferGrowth`).

## Before and after

`abbench --max-load 8`, 7 rounds kept (4 discarded: the machine's load was above 8 after them)
alternating a clean clone of v0.21.0 and this change, `BenchmarkWorldPass -wc.dlights`
(1280x800, the 16 fixed views, three lights around each), Apple M4 Max:

| | v0.21.0 | this change | |
|---|---|---|---|
| GPU world CPU time (ms) | 1.580 ± 15 % | 1.750 ± 15 % | ~ (p = 0.069) |
| same, p95 (ms) | 3.350 | 3.575 | ~ |
| GPU world wall to idle (ms) | 3.630 ± 9 % | 3.365 ± 11 % | -7.3 % (p = 0.017) |
| same, p95 (ms) | 6.200 | 5.835 | -5.9 % (p = 0.023) |
| software frame (ms) | 40.10 | 41.13 | ~ |

## Where the time is

The head build also times the parts of the CPU time (`gpu-bake-ms`, `gpu-upload-ms`):

| part of the frame with three lights | ms |
|---|---|
| baking the re-baked surfaces (`render.DrawSurface`) | 1.026 ± 11 % |
| recording the uploads (the `WriteTexture` calls) | 0.085 ± 17 % |
| the rest (visible set, draw list, submit) | about 0.64 |

So the uploads were never the cost on the CPU: recording a `WriteTexture` takes about 0.7
microseconds, and coalescing cuts the wall time by a tenth (the browser has fewer calls to
run) and leaves the CPU time where it was. 59 % of it is the CPU bake of the lit surfaces,
the same work the software renderer does for the same surfaces in its own frame (which is 40
ms, 41 with the lights). Making that cheaper is a different design (building the lit surface
on the GPU from the base texture and the lightmap, so that the CPU sends only the lightmap, a
few samples a surface instead of its texels), and is not done here: it is an open issue of the
repository (engine#106 lists it).
