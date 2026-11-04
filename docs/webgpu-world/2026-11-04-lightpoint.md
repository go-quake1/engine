# R_LightPoint: entity light from the lightmap, 2026-11-04

Step 6, item 1. Until now every entity was lit at a constant 76
(`render.AliasStandInLight`). The engine now samples the lightmap under each
entity as Quake does, on both paths: `bsprender.LightSampler`.

## Bibliography (what was opened)

- `reference/common/r_main.c` `R_LightPoint` (line 754): 255 without
  lighting data, 0 when no lit surface is below, otherwise a 2x2 sample at
  `(floor(s), floor(t))` clamped to `surfwidth - 2` / `surfheight - 2`, each
  lightstyle's map scaled by `d_lightstylevalue / 256` and added, bilinear
  weights from `s - ds` and `t - dt` (so past the clamp the fraction exceeds 1,
  and the weights go negative: reproduced), then `floorf`.
- `reference/common/r_light.c` `R_LightSurfPoint` (line 240: the line from the
  point to `point.z - (8192 + 2)`), `RecursiveLightPoint` (line 179: the plane
  test per node type, the split point, front side first, then the node's own
  faces, then the back side) and `R_FillLightPoint` (line 139: skip
  `SURF_DRAWTILED`, `ds`/`dt` from `texturemins`, reject `ds < 0`, `dt < 0`
  and `> extents >> 4`, stop at a face without samples).
- `reference/common/model.c` `CalcSurfaceExtents` (line 1138, the
  `long double` accumulation) and `Mod_ProcessSurface` (line 1231): which
  surfaces are `DRAWTILED` (`TEX_SPECIAL`, `sky*`, `*` liquids).
- `r_alias.c` `R_AliasSetupLighting` (line 672) for where the value goes
  (already ported in step 5).

## What changed

- `bsprender/lightpoint.go`: `NewLightSampler(bm)`, `LightPoint(point,
  styles)`, `SurfPoint` (`R_LightSurfPoint`), `NormalLightStyle`.
  Float32 products are rounded one by one (no fused multiply-add), as the C
  does on x86.
- `worldview.AliasEnt.Light` is set once per entity (`World.LightEnt`) and
  read by both the software reference and the GPU entity record; the engine's
  `runner` and `game` render loops call the sampler with the live light style
  values (`runner`) or the resting value (`game`); a scene without a readable
  map keeps the stand-in.
- Not changed: dynamic lights (`dlights` stays empty, as before); style
  animation in `worldview` (resting value 264 for every style at the fixed
  views).

## Pinned tests

`bsprender/lightpoint_test.go`, on `synthbsp.BuildLit` (a face with a 4 x 4
lightmap of `10 * i`), values worked by hand from the formulas above:
38.671875 -> 38 at (20, 10); 61.875 -> 61 at (56, 10), which is the clamped
case with weights `-0.1875 0.5625 -0.3125 0.9375`; 88 for two styles
(264 and 128); 0 off the lightmap, below the plane, and on `sky`, `*water`,
`*lava`; 255 without a lighting lump. `worldview.TestEntityLightsE1M1`: the
tree walk against a brute force over the face polygons on the 54 alias
entities of e1m1 (52 agree; the two others sit on the edge between coplanar
faces, where the C tests the lightmap rectangle and the brute force the
polygon), 42 distinct lights, and six regression pins (values of this port).

## Acceptance

Same 16 views, 320 x 200, Apple GPU (`apple metal-3`, not a fallback),
Chrome for Testing 156. **Stated tolerance, before the first measurement:
3 % unexplained** (as steps 4 and 5).

| | region px | differ | unexplained |
|---|---|---|---|
| step 5 (constant 76) | 49,479 | 0.23 % | 0.10 % |
| **R_LightPoint** | 48,514 | 0.21 % | **0.10 %** (47 px), against 3 % |
| both sides back at 76 (`--standInLight`) | 49,124 | 0.23 % | 0.10 % |
| every skin one flat colour | 49,603 | 0.01 % | 0.00 % |
| every vertex normal 0 | 49,742 | 0.20 % | 0.09 % |
| no interpolation | 48,449 | 0.22 % | 0.10 % |
| no view weapon | 37,719 | 0.27 % | 0.13 % |

Planted negatives: skins shifted by 16 on the GPU side only, 89.90 %
unexplained; **the GPU lighting every entity at 76 while the reference uses
R_LightPoint (`TestWrongLightFails`), 56.03 %**: the check sees the light.
The match is by construction as in step 5 (the GPU reads the same
`Entity.Light`); what the figure says is that the GPU shader's lighting
follows a per-entity input across 54 different values, not only 76.

## Cost

`BenchmarkLightPoint` (Apple M4 Max, native): 0.4 to 0.5 microseconds per
entity, 0 allocations. Against an alias pass of 0.585 ms for 16 views this is
below 2 %. Wall-clock browser timing is in the step 6 summary
(2026-11-04.md), taken together for all four items at 1-minute load below 8.
