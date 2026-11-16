# Dynamic lights, on the CPU and the GPU (engine#106, item 7)

`R_PushDlights`, `R_MarkLights` and the dynamic light term of `R_BuildLightMap` were not
ported: the software world, the engine's runner and `render/gpuworld` drew every surface
as if no light moved. They are now, from the reference source in `reference/` (tyrquake):
`common/r_light.c` (`R_MarkLights` 72, `R_PushDlights` 117), `common/r_surf.c`
(`R_AddDynamicLights` 65, `R_BuildLightMap` 133), `common/d_surf.c` (`D_CacheSurface` 282),
`NQ/cl_main.c` (`CL_AllocDlight` 441, `CL_DecayLights` 484, `CL_RelinkEntities`, the
effects at 656 to 737) and `NQ/cl_tent.c` (`TE_EXPLOSION` 206).

## What was ported

- `bsprender.MarkLights` and `PushDlights` (`dlight.go`): the walk of the BSP from the
  root, descending the side of each splitting plane the light's sphere reaches (both when
  `dist == radius` or less), marking the faces of each node it passes; a light that has
  died (`die < now`) or has no radius is skipped. The caller's mark does what the C does
  to the `msurface_t`: clear `dlightbits` when `dlightframe` is another frame.
- `render.AddDynamicLights` (`dlight_surf.go`) and the `BuildLightMap` call: `rad = radius -
  |dist to the plane|`, skipped below `minlight`; the impact point in texture space
  relative to `texturemins`; for each 16-texel sample the larger axis distance plus half
  the smaller, under `rad - minlight`, adds `(rad - dist) * 256`; `td` and `sd` are
  truncated toward zero as the C's `int td = float`, and the sum is made in float, as
  `unsigned += float`. `SurfaceDesc` carries `Plane`, `TexVecs`, `DLightFrame`,
  `DLightBits`; `SurfaceEnv` carries `DLights` and `FrameCount` (`r_framecount`).
- `D_CacheSurface` (`SurfaceCache.Cache`): a surface lit in this frame is rebuilt, its
  `cache->dlight` is set, and the next frame rebuilds it once more without the light.
- The client side the lights come from (`runner/dlights.go`): `CL_AllocDlight` (key match,
  first dead slot, else slot 0), `CL_DecayLights`, the muzzle flash (200 + `rand & 31`
  units, minlight 32, 0.1 s, 18 forward and 16 up of the entity), the bright (400 + ...)
  and dim (200 + ...) lights, red and blue, the rocket's light (200 units, 0.01 s) and
  the light of `TE_EXPLOSION` (350 units, 0.5 s, decaying 300 a second). Not made: the
  light of `TE_EXPLOSION2` (the runner does not handle that temp entity at all), and the
  lighting of entities from the lights (`R_AliasSetupLighting`'s dlight loop: the alias
  path still passes no lights).

Tests are pinned on values worked from the source and not produced by the code: the
light grids of `AddDynamicLights` on a 3 x 2 sample surface (a light over the middle, to
the side, with a minlight that cuts the far samples, with `rad < minlight`, truncation of
`td`, the plane's distance, a light behind the plane), `BuildLightMap` with and without
the frame's marks (and the floor of `1 << 6`), `D_CacheSurface`'s rebuild frames, the
two-node tree of `MarkLights` (every case of `dist > radius`, `dist < -radius` and the
boundary) and `PushDlights` (die, radius, bits), and the effects of `CL_RelinkEntities`
(origin of a flash from the entity's angles, the radii, the decay). A mutation of the
larger-axis-distance branch fails them. `runner` and `worldview` draw the same pixels with the
same lights on the 16 views (0 of 1,024,000 differ), and the lights change 688,771 of
them.

## The GPU

`worldview.World.DLights` and `Now` are the lights the next frame draws with;
`World.Visible` marks the faces (`R_PushDlights`) and `World.LightEnv` gives the surface
env of the frame. `gpuworld.Geometry` keeps, per face and mip level, whether the surface
was baked with a light on it; a drawn surface a light reaches is re-baked with it in
every frame it is reached, and once more after (D_CacheSurface's `cache->dlight`), into
the frame's upload buffer, and uploaded with the same `WriteTexture` of the animation
and under the same 4 MiB budget. A face that the walk lists several times (once per leaf
it is attached to) is baked once per frame. `cmd/quake-wctr` implements
`runner.DynamicLighter` (`SetDLights`) so that the container's GPU world sees the same
lights as the software pass.

## Check (a real GPU, Apple M4 Max, Chrome, `QUAKE_REQUIRE_GPU=1`)

`quake-worldcheck --dlights`: on each of the 16 fixed e1m1 views, the lights of a player
who fires a rocket launcher around the view (a muzzle flash 220 units, minlight 32; the
rocket's light 200; the explosion 350), on the reference and on the GPU.

| | differ | unexplained | tolerance |
|---|---|---|---|
| GPU with the lights | 46 of 1,024,000 | 7 (0.0007 %) | 2.5 % |
| planted: GPU bakes without them (`--noDlights`) | 889,851 | 832,620 (81.3 %) | FAIL |

The lights change 86.9 % of the reference's pixels against the unlit frame, so the views
show the feature. The 16 frames upload 1,939 surfaces (3.2 MB) in all and at most 390 KB
in a frame; the 4 MiB budget is not reached. The animation check of
[2026-11-16-light-styles-textures.md](2026-11-16-light-styles-textures.md) and the 16
fixed views at rest are unchanged (3 of 1,024,000 unexplained).

Native (needs `QUAKE_PAK`): after a frame with a light, every drawn face's atlas
rectangle is what `DrawSurface` builds with the light; the same light a frame later
re-bakes the same surfaces; a frame after it dies re-bakes them once to take it off, the
next re-bakes nothing; with `IgnoreDLights` the atlas holds the unlit surfaces.

## World pass, before and after

`abbench --max-load 8`, 7 rounds alternating a clean clone of v0.18.0 and this change
(`BenchmarkWorldPass`, 1280x800, the 16 fixed views, 1-minute load under 8), Apple M4 Max.

| | v0.18.0 | this change | |
|---|---|---|---|
| no light: GPU world CPU time (ms) | 0.200 ± 8 % | 0.195 ± 8 % | ~ (p = 0.20) |
| no light: GPU world wall to idle (ms) | 1.415 | 1.385 | -2.1 % (p = 0.02) |
| no light: software frame (ms) | 39.8 | 39.9 | ~ |
| with the three lights (`--dlights`): GPU world CPU time (ms) | 0.225 ± 7 % | 1.715 ± 2 % | +662 % (p = 0.001) |
| same, p95 (ms) | 0.325 | 3.445 | +960 % |
| same, wall to idle (ms) | 1.410 | 3.720 | +164 % (p = 0.001) |
| same, wall p95 (ms) | 1.650 | 6.150 | +273 % |

(The second measurement, after the bake went straight into the upload buffer: 0.200 to
1.780 ms CPU and 1.390 to 3.770 ms wall before it, so the change is within the noise.)

With the three lights around every one of the 16 views, a frame re-bakes on average 120
surfaces (about 200 KB, 390 KB at most) and uploads each with a `WriteTexture`: 1.5 ms
more CPU time and 2.3 ms more wall time than the unlit frame. That is the heavy case, a
muzzle flash, a rocket and an explosion at once around the player: a light reaches the
surfaces within its radius, and the software reference re-builds the same ones in every
frame it is lit as well (its frame is 40 ms, and 41 ms with the lights). The GPU frame
with all three lights is still about a tenth of the software one. Cheaper uploads
(one `WriteTexture` per run of adjacent rectangles) are not done: they are the next thing
to look at if a game shows it.
