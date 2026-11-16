# The GPU world follows the light styles and the `+` textures (engine#106, item 6)

Until v0.17.0 the surface atlases of `render/gpuworld` were baked once, at the resting
light styles and the first frame of every `+` texture; the CPU path
(`runner/worldsurf.go`) animated both. The container's `--gpuworld` therefore showed a
still world.

## Design

`worldview.World.Anim` is what moves: `Style` (`d_lightstylevalue`) and `Frame`
(`R_TextureAnimation`). The zero value is the resting world, which the fixed views and
every earlier check draw. `World.AnimationAt(t)` is the animation of the runner's CPU
path: `worldview.LightStyleValue` (the 11 default styles at 10 Hz, moved here from
`runner`, which keeps a one-line wrapper) and the `+` chains (`MiptexChains`, moved here
too, `runner` aliases it) at 5 Hz. `World.SurfaceState(face)` gives a face's
`(lightadj, texture)`: what `D_CacheSurface` compares.

`gpuworld.Geometry.Draws` keeps, per face and mip level, the state its atlas rectangle
was baked with. For every face the frame draws, at the mip level the frame chooses, it
compares that state with `SurfaceState`; on a difference it re-bakes the one rectangle
(`render.DrawSurface`, the code the CPU uses), writes it into the CPU copy of the atlas
and lists it in `Geometry.Updates`. `Renderer` uploads the list with one
`Recorder.WriteTexture` per rectangle, in the frame's own submit, before the pass. This
is D_CacheSurface's reuse test, lazily and per level: a surface that is not drawn is not
re-baked, and a level a frame does not use keeps its old state until it is drawn.

Budget: a frame re-bakes at most `MaxUpdateBytes` (4 MiB, half of `MaxBandBytes`, far
under the host's 32 MiB frame); the surfaces left over are drawn stale for a frame
or more (`Geometry.Deferred` counts them) and done by the next frames. A single surface
larger than the budget is done alone in its frame, so it is never starved.

`cmd/quake-wctr` sets `World.Anim = World.AnimationAt(t)` in `DrawWorld`; nothing else
changes there.

What stays as the CPU has it: the CPU world path draws the 11 default styles and not the
styles the server sends (`lightstyle` is a no-op builtin in `runner`), so a map's
switchable lights (styles 32 and up) are at their steady value on both. The GPU matches
the CPU, not a different animation.

## Check (a real GPU, Apple M4 Max, Chrome, `QUAKE_REQUIRE_GPU=1`)

`quake-worldcheck --anim`: the light styles and `+` frames of eight times
(`0.05 + 0.37 k` seconds) on both the reference and the GPU, on 8 views that look at an
animated face from close by (`World.AnimatedViews`: the largest faces of an animated
light style or `+` chain, alternately), 64 frames of 320x200. The animation moves 9.6 %
of the reference's pixels against the resting world.

| | differ | unexplained | tolerance |
|---|---|---|---|
| GPU following the animation | 1,680 of 4,096,000 | 0 (0.0000 %) | 2.5 % |
| planted: GPU keeps its baked atlas (`--noAnimate`) | 393,758 | 293,715 (7.17 %) | FAIL |

The 16 fixed views at rest, unchanged: 45 differ, 3 unexplained of 1,024,000.

Native, pinned on `DrawSurface` (needs `QUAKE_PAK`): after a frame at time t every drawn
face's atlas rectangle equals the surface built for that time; the same frame again
re-bakes nothing; the resting world re-bakes nothing; a 1 KiB budget defers and converges.
Mutation: comparing the light styles but not the texture frame in `refresh` fails
`TestSurfacesFollowTheAnimation`.

## Per-frame upload cost

Four animated views of e1m1, 1280x800, 300 frames (5 s at 60 frames per second) at the
game's times (`TestUploadCostPerFrame`). Styles move at 10 Hz and textures at 5 Hz, so
most frames upload nothing:

| view | faces | frames that upload | rectangles | mean | p95 | max |
|---|---|---|---|---|---|---|
| 0 | 252 | 25 / 300 | 256 | 4.4 KB/frame | 41 KB | 330 KB |
| 1 | 34 | 32 / 300 | 308 | 8.0 KB/frame | 73 KB | 122 KB |
| 2 | 264 | 25 / 300 | 190 | 6.2 KB/frame | 66 KB | 279 KB |
| 3 | 65 | 32 / 300 | 468 | 8.0 KB/frame | 74 KB | 79 KB |

The checked frames, each at a new time (the worst case: every frame has something to
re-bake), upload at most 418 KB. The budget (4 MiB) is not reached.

## World pass, before and after

`abbench --max-load 8`, 7 rounds alternating a clean clone of v0.17.0 and this tree
(`BenchmarkWorldPass`, `go test -c` once, `-wc.dir` picks the program), 1280x800, the
16 fixed views, 1-minute load 3 to 5, Apple M4 Max. The GPU frame's CPU time is the walk,
the mip choice, the re-bake check and the submit.

| | base | this tree | |
|---|---|---|---|
| GPU world, CPU time, resting (ms) | 0.190 ± 8 % | 0.200 ± 13 % | ~ (p = 0.69) |
| same, p95 (ms) | 0.280 | 0.300 | +7.1 % (p = 0.013) |
| GPU world, wall to GPU idle (ms) | 1.410 | 1.410 | ~ |
| GPU world, CPU time, `--anim` (ms) | 0.175 ± 17 % | 0.210 ± 17 % | ~ (p = 0.066) |
| same, p95 (ms) | 0.280 | 0.325 | +16 % (p = 0.001) |
| GPU world, wall, `--anim` (ms) | 1.425 | 1.450 | ~ (p = 0.22); p95 +3.4 % |

The check costs about 10 to 35 microseconds of CPU per frame at p95 (one `SurfaceState`
per drawn face); the GPU time does not move. In this benchmark the animation time
advances for each of the 16 views, so more states move between two visits of a view than
for a camera that stands still.
