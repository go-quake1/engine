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
(`runner/render.go`) still bakes one full-size surface per face with
`render.BakeSurface`; moving it onto `render.SurfaceCache` is the next step.

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

COSTS

## What remains

`runner/render.go`'s world loop on `render.SurfaceCache`; dynamic lights
(`R_AddDynamicLights`); light styles animating at their real values in the fixed
views (a face whose style changes re-bakes on the CPU and would need a
sub-rectangle upload on the GPU); the GPU atlas is 33 MB where Quake's cache
is 0.9 MB at 320 x 200 (a GPU cache with the rover is the alternative); the
recursive drawer for far alias models; `r_aclip.c`.
