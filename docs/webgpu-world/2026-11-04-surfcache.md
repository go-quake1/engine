# Step 6, item 3: the surface cache with mip levels, 2026-11-04

The last piece of step 6. The world is no longer sampled per pixel from the
full-size texture and the lightmap; it is drawn from Quake's **surface cache**:
per face and per mip level, a lit texture (`render.DrawSurface`) that the span
drawer reads, with the mip level chosen from the face's distance. The software
reference and the GPU both do it, and the 16 fixed e1m1 views, the 2.5 %
tolerance of the world check (unchanged since step 3, not touched by this
step), the ablation and the planted negatives are those of
[2026-11-02.md](2026-11-02.md).

## Bibliography (what was opened)

In `reference/common`:

- `r_surf.c`: the globals and the four-entry `surfmiptable` (lines 28 to 57),
  `R_BuildLightMap` (133), `R_DrawSurface` (227) and
  `R_DrawSurfaceBlock8_mip0` to `_mip3` (319 to 502);
- `d_surf.c`: `D_SurfaceCacheForRes` (45), `D_InitCaches` (108),
  `D_FlushCaches` (130), `D_SCAlloc` (154), `D_CacheSurface` (282);
- `d_edge.c`: `D_MipLevelForScale` (45), `D_CalcGradients` (110) and the two
  call sites of the mip choice, `D_MipLevelForScale(surf->nearzi * scale_for_mip
  * pface->texinfo->mipadjust)` (247, 286);
- `d_init.c`: `basemip`, `d_mipcap`, `d_mipscale` (27 to 36) and `D_SetupFrame`
  (120); `d_modech.c` `D_ViewChanged` (`scale_for_mip`, line 42); `r_main.c`
  `xscale` (492); `model.c` `mipadjust` (1102 to 1112); `r_draw.c`
  `R_EmitEdge` (`r_nearzi` is the largest `1 / z` of the emitted edges, lines
  120 to 141); `include/d_local.h` `surfcache_t`; `r_misc.c` (`ambientlight`,
  443).

What they say, and what was done with it:

- **Mip level.** `D_MipLevelForScale` compares `nearzi * scale_for_mip *
  mipadjust` with `d_scalemip = {1, 0.5 * 0.8, 0.25 * 0.8}` (times the
  `d_mipscale` cvar, default 1), and takes `d_mipcap` (default 0) as the floor.
  `scale_for_mip` is `xscale = width / (2 tan(fov / 2))`; `mipadjust` is 4, 3, 2
  or 1 from the mean length of the texture axes (0.32, 0.49, 0.99).
- **Light.** `R_BuildLightMap` sums the styles' lightmaps in 8.8 with the
  ambient level, inverts and shifts (`(255 * 256 - sum) >> 2`, not below 64);
  `R_DrawSurfaceBlock8_mipN` interpolates that grid over blocks of `16 >> N`
  texels, rows and columns by integer steps, and reads the colormap row
  `light & 0xFF00`. The step values are shifted as **unsigned** in C (the
  grid is `unsigned`); only bits 8 to 15 of the light are read, and the
  difference does not reach them, so signed and unsigned give the same
  surface. The port uses the unsigned form anyway.
- **A face without a lightmap** (`surf->samples == NULL`) gets the ambient
  level only, which is dark. In e1m1 that is 586 of the 5149 drawn faces; the
  earlier references drew them full bright.
- **Light styles** are at their resting value (`d_lightstylevalue` 264 for
  `m`, `bsprender.NormalLightStyle`), as in the alias work of this step.
- **The cache itself.** One arena (`D_SurfaceCacheForRes`: 896,000 bytes at
  320 x 200, 5,024,000 at 1280 x 800), carved in address order by a rover:
  `D_SCAlloc` frees the blocks it overlaps (through their `owner`), keeps a
  leftover as a fragment only above 256 bytes, and sets `r_cache_thrash` when
  the rover wraps twice in a frame. The header is 64 bytes (an LP64 `surfcache_t`).
  `D_CacheSurface` returns the block when texture and light-style values are
  unchanged and rebuilds in place when they are not.

## What was not copied exactly, and why

`nearzi` exists only in Quake's edge-based hidden-surface pass (`r_edge.c`);
this engine draws faces one by one in the BSP walk's order. The stand-in
(`worldview.NearZI`) is the largest `1 / z` over the face's polygon **clipped
to the view frustum** (the screen rectangle and the near plane), which is what
`r_nearzi` is for a face seen whole: the emitted edges are the clipped ones.
Where Quake's would differ: a face partly hidden by a nearer one (Quake still
counts the hidden edges, as the polygon does), and the 3-pixel shrink of the
clip planes (`xscaleshrink`, not used). Dynamic lights
(`R_AddDynamicLights`) are not ported. The engine's own world loop
(`runner/render.go`) baked one full-size surface per face with
`render.BakeSurface` in v0.7.x; v0.8.0 moves it onto `render.SurfaceCache` (see
the update below).

## Tests pinned on the source

`render/surfcache_test.go` (100 % of the statements): the thresholds of
`D_MipLevelForScale` at, just above and just below each of 1, 0.4 and 0.2, and
with `d_mipcap`; `scale_for_mip` 160 at 320 wide and 90 degrees; `mipadjust`
for axes of length 1, 0.5, 0.4, 0.25; `R_BuildLightMap` values (100 at scale
256 is 9920; with ambient 20, 8640; two styles 8120; overbright held at 64; no
samples 16320; fullbright 0); block layouts and texels worked by hand (a 2 x 2
light grid {0, 64 / 0, 64}: row 47 at the right edge of a block, one row more
per texel at mip 0, two at mip 1; the texture origin wrapping at `texturemins`
16 on a 32-wide texture); the allocator on a 4096-byte arena (block 0 to 320,
then 320 to 448, a wrap that frees both and leaves 3700 + 396, a 396-byte tail
whose leftover stays in the block, the second wrap that sets
`r_cache_thrash`); `D_CacheSurface` reuse, rebuild on a changed style value or
texture, and the owner cleared by a flush. `worldview/mip_test.go`: the
clipped nearest `1 / z` of a square at z = 100 (0.01), of a floor 50 below the
eye (1/80: the bottom of the screen sees it from z = 80, not from its vertex at
z = 5), and the mip level at z = 100, 160, 399, 401, 500, 799, 801, 1000 for
`mipadjust` 1, 2 and 4. `render/gpuworld`: the atlas holds exactly what
`DrawSurface` builds, at the rectangle of the face table, for 400 faces and all
four levels, and the frame's levels are `worldview`'s.

## The GPU

Per map, once: every drawn face is baked at the four levels with the software
path's `DrawSurface` and packed (tallest first) into one `r8uint` atlas,
8192 x 4256 for e1m1 (25.2 MB at level 0, 33.5 MB in all). Per frame the CPU
gives each visible face its level (`worldview.MipView.Level`, 1 u32 per face
written to a storage buffer); per fragment the shader rebuilds the reference's
8-pixel sub-span as before, takes `(s, t)` relative to `texturemins`, scales by
`1 / 2^level`, clamps to the surface (`bbextents`) and loads the texel. The
lightmap atlas, the texture atlas and the per-fragment lightmap bilinear and
colormap lookup are gone from the world shader; the vertex is 16 bytes, not 64.

## Results

Same 16 views, 320 x 200, Apple GPU (`apple metal-3`, not a fallback), Chrome
for Testing 156. **Tolerance, 2.5 % unexplained, the value of the world check
since step 3 (it was in the repository before this step began).**

| | differ | unexplained |
|---|---|---|
| v0.6.1's reference (per-pixel lightmap, full-size texture, which the v0.6.1 GPU matched to 0.006 %) against the surface-cache reference | 40.47 % (414,451) | **23.86 % (244,374), before** |
| **GPU against the surface-cache reference, now** | **0.006 % (58)** | **0.0006 % (6)** |
| worst view | 10 pixels differ | 3 unexplained (view 13) |

### Ablation and negatives (1,024,000 pixels each)

| probe | differ | unexplained |
|---|---|---|
| the acceptance run | 0.006 % | 0.0006 % |
| lightmaps flat (128) on both sides | 0.006 % | 0.0008 % |
| every texture one flat colour | 0.001 % | 0 |
| flat light and flat texture | 1 pixel | 0 |
| reference exact per pixel (8-pixel steps off on the reference only) | 2.20 % | 0.57 % |
| **GPU at mip level 0, reference at Quake's levels (no mip choice on the GPU)** | 27.57 % | **13.68 % (fails)** |
| reference at level 0, GPU at Quake's levels | 27.57 % | 13.28 % |
| **both at level 0** (the mip choice removed on both sides) | 0.011 % | 0.0023 % |
| GPU against the v0.6.1 reference (`--oldRef`) | 40.47 % | 23.23 % |
| **planted negative: the GPU one mip level too far** | 57.93 % | **28.20 % (fails)** |
| planted negative: colormap one eighth too dark | 95.91 % | 76.55 % (fails) |

What the ablation says. The 23.9 % that the surface cache moved the reference
is two things, and removing one shows which: the mip choice is about 13.7
points (GPU and reference differ by 13.7 % when one of them takes level 0),
and the rest, about 10 points, is the light (the 8.8 sum with the unlit faces
dark, the block interpolation of the light grid, the colormap row from
`light & 0xFF00`). With both sides at level 0 the GPU matches to 0.0023 %, so
the remaining 6 unexplained pixels are not mip-related.

## Cost

`TestBenchAgainstBaseline`: a clean clone of the tag v0.6.1 and this tree
alternate round by round (the order swapped every round), each round a browser
run at 1280 x 800 (Chrome for Testing 156, wasm, single thread, Apple GPU, the
16 views, the paths alternating frame by frame) and a native run of
`BenchmarkDrawSoftware` (the same file copied into the clone). 5 rounds kept,
3 discarded for a 1-minute load above 8 (the first attempt waited 40 minutes
for the load to fall and gave up; the kept rounds ran at load 2 to 3).
Medians (min-max).

| per frame, 1280 x 800 | v0.6.1 | this step |
|---|---|---|
| software world, browser | 75.71 ms (73.92-76.49) | **39.71 ms** (39.03-40.62) |
| software world, browser, surface cache emptied before every frame | | 39.04 ms (38.54-40.17) |
| software world, native (M4 Max) | 8.92 ms (8.68-8.97) | **4.49 ms** (4.27-4.64) |
| GPU world, wall to GPU idle | 1.480 ms (1.425-1.495) | **1.425 ms** (1.420-1.440) |
| GPU world, CPU time | 0.180 ms (0.160-0.185) | **0.260 ms** (0.250-0.300) |
| engine's affine world path (not changed) | 9.85 ms | 9.30 ms |

The software world is 1.9 times faster: a texel read from a surface of the
face's own size instead of a per-pixel bilinear lightmap and colormap lookup,
and fewer texels touched at the distant levels. Emptying the cache each frame
costs nothing measurable, because a frame builds a surface only for the
visible faces and the 16 views do not share many. The GPU's wall time did not
get slower (the world shader lost the lightmap bilinear and the colormap
fetch), but its **CPU time is 0.08 ms higher, an open defect, located**: the
draw list now runs `NearZI` on every visible face (a second transform of its
vertices and the frustum clip), 42 microseconds natively against 16 for the
list without the mip choice (`BenchmarkDraws`), which is the browser's
0.08 ms. The remedy is to share the vertex transform with the near-plane test
that `Draws` already makes, and to skip the clip for faces whose vertices are
all inside the frustum (they already are, in one pass). The atlas costs memory
and start-up, not frame time: 34.9 MB of `r8uint` against 1.1 MB before (the
2048 x 320 and 2048 x 207 atlases), built in under 0.1 s natively.

## Update, v0.7.1: the CPU cost of the mip choice

The open defect above is mostly closed. Three changes, none touching fidelity
(the same 16 views: 58 pixels differ, 6 unexplained, 0.0006 %; the planted
negatives still fail at 28.20 %, 13.68 % and 76.55 %):

- `Geometry.Draws` transforms a face's polygon once for the near-plane test and
  the mip choice (it transformed it twice), with no clipping buffer zeroed per
  face (3 KB each before);
- a face whose precomputed bounding sphere lies inside the frustum is answered
  from the z of its vertices alone (`MipView.FastLevel`; 1440 of the 3909
  visible faces of the 16 views), and the test shows it gives the clipped
  path's level for every one of them;
- the level rides on the draw's first instance (`DrawIndexed(..., firstInstance)`,
  read as `@builtin(instance_index)`): the 20 KB per-frame buffer upload and its
  5149-entry loop are gone.

Interleaved against a clean clone of v0.7.0, 5 rounds, load 5.6 to 6.2: GPU
world CPU time **0.280 ms (0.260-0.300) to 0.205 ms (0.200-0.210)**; wall
1.450 to 1.420 ms; the software world is the same code (39.9 and 39.9 ms in the
browser, 4.52 and 4.55 ms natively). v0.6.1 was 0.180 ms, so 0.025 ms of the
0.100 ms remains, the sphere test and the transform of the faces that do not
pass it. Moving the engine's own world loop (`runner`) onto the surface cache is
not small (its per-frame face walk, animated textures and light styles are its
own) and is filed as an issue.

## Update, v0.8.0: the engine's own world loop on the cache

`runner/render.go` no longer bakes one full-size surface per face
(`render.BakeSurface`, lightmap bilinear, no mip, no eviction, faces with a
non-resting light style or a `+` texture on the per-pixel fill). Its lit faces go
through `runner/worldsurf.go` (`worldSurfaces`), the same sequence as
`worldview.DrawSoftware`: the mip level from the face's clipped nearest `1 / z`
(`worldview.MipView.Level`, which uses `NearZIOf`), one `render.SurfaceCache` of
`SurfaceCacheForRes` bytes (remade when the frame size changes, `SetupFrame` each
frame), a `render.SurfaceDesc` per face (`worldview.NewSurfaceDesc`, shared with
the reference), `D_CacheSurface`, and `FillPerspectiveCachedPolygon` at
`CU = LmS * 16 / 2^mip`. What the runner adds to the fixed views: the light styles
are `d_lightstylevalue` at `cl.time` (`lightStyleBrightness`, so a flickering
style rebuilds its surface in place), and an animated `+` texture
(`R_TextureAnimation`, 5 Hz) is a changed texture for `D_CacheSurface`, which
rebuilds the block in place. A face whose miptex did not decode (the checker
texture) or that the cache cannot hold keeps the per-pixel lightmapped fill.

Equality (`TestWorldSurfacesMatchReference`, needs `QUAKE_PAK`): the 16 e1m1 views
at 320 x 200, the runner's pass against `worldview.DrawSoftware` with the resting
light styles and the same face order: **0 of 1,024,000 pixels differ**; cache
builds 2174, hits 3988, no thrash frame.

**A finding about the reference (fixed in v0.9.0, #74).** The runner drew the
walk's list in reverse (back to front); `worldview.DrawSoftware` and the GPU
drew it forward with no depth test, so in an overlap the **far** face won (the
list is front to back: in view 0 the first face is at z = -62, the last at
z = 1046). The same lit faces in the runner's order differed from the reference
in 195,866 of the 1,024,000 pixels (19.1 %); the reference and the GPU agreed
with each other (what the step 6 checks measure), not with the engine's frame.
All numbers quoted in this document and in the 2026-11-02 to 2026-11-04 pages
(the 23.86 %, 0.006 %, the ablation rows) were measured in that forward order.

Cost: not measured here (the machine's 1-minute load was 16 to 20, above the
limit of 8); the before and after of the runner's frame against a clean clone of
v0.7.1 is left to a quiet machine.

## Update, v0.9.0: the world is drawn in Quake's order

What the sources say (`reference/common`, opened): `R_RenderFace` (`r_draw.c:480`)
gives each surface `key = r_currentkey++` in the order `R_RecursiveWorldNode`
emits faces (near child first); the edge sorter keeps the active surfaces ordered
by key and `R_LeadingEdge` (`r_edge.c:437`) makes a new surface the top only when
`surf->key < top->key`. At each pixel the surface drawn is the **first one the
walk emitted**. With no span buffer that is the walk's list drawn in reverse.
`DrawSoftware`, `DrawSoftwareMip`, `DrawLightmapped`, `DrawEngine` and
`gpuworld.Geometry.Draws` now draw back to front (the GPU has no depth
attachment, `gpuworld_js.go:188-190`; it issues the draws in list order,
`gpuworld_js.go:225-227`, so reversing the list is the same rule).

Not reproduced: back faces are not culled in the reference (the runner culls and
still matches to 0 pixels on the 16 views); sky and water are drawn after all lit
faces, not in key order; the fill rules at shared polygon edges are the polygon
filler's, not the span sorter's.

`TestNearestFaceWins`: each visible face is drawn alone (over two backgrounds, to
find the pixels it writes) and the frame must show, at every pixel, the first walk
face that covers it. On the v0.8.0 order 195,866 of the 1,024,000 pixels of the
16 views fail (499,433 face overlaps with a different value); now 0. The runner's
frame equals the reference's in 0 of 1,024,000 pixels in its own order; the walk's
order differs from it in 195,866.

World check in the new order (`cmd/quake-worldcheck`, Chrome 156 headless on the
Apple GPU, the 16 views, tolerance 2.5 %): `TestGPUWorldMatchesTheSoftwareWorld`
**45 of 1,024,000 pixels differ, 3 unexplained** by a one-pixel shift (0.0003 %);
the damaged-colormap, wrong-mip and no-mip controls still fail (77.4 %, 30.2 %,
11.3 % unexplained). Sky, water and alias checks unchanged in kind (0.02 %,
0.10 %, 0.69 % unexplained). The ablation rows and timings of the earlier pages
were not re-run: the 1-minute load was above 8.

## What remains

Dynamic lights
(`R_AddDynamicLights`); light styles animating at their real values in the fixed
views (a face whose style changes re-bakes on the CPU and would need a
sub-rectangle upload on the GPU); the GPU atlas is 33 MB where Quake's cache
is 0.9 MB at 320 x 200 (a GPU cache with the rover is the alternative); the
recursive drawer for far alias models; `r_aclip.c`.
