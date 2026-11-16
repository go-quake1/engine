# The alias entities on the GPU in the game loop (engine#122)

Until v0.24.0 `quake-wctr` drew every alias model (monsters, items, the player's weapon pickups)
with the software pass, whatever the world was drawn with: `gpuworld.Renderer.DrawEntities` existed and
`quake-worldcheck` compared it, but the game loop did not call it. Now, with the world on the GPU (the
default when one is granted; not with `--cpuworld`), the GPU draws the entities too.

## How

- `runner.EntityDrawer` (implemented by `quake-wctr`'s `gameWorld`): the runner gives it the models
  once (`SetAliasModels`), then each frame the entities it chose (`SetEntities`), calls `DrawWorld`, and after
  the CPU has drawn the sky and water over the GPU frame calls `CompositeEntities`.
- Layering. The CPU draws the sky and water after the GPU's world, and the software pass draws entities
  over everything (it has no depth test), so the GPU entities cannot share the world's target: they go
  in a pass of their own into a second index texture cleared to 255 (`gpuworld.EntityClear`; the entity
  shader writes 254 for a texel of 255), in the same submit and the same read-back
  (`Renderer.DrawReadLayers`: two copies, 2 x 256 KB at 640x400, under the host's 1 MiB read window).
  The runner copies the layer's texels that an entity drew over the frame (`gpuworld.CompositeLayer`).
- The CPU keeps: the entities `R_AliasCheckBBox` draws by recursive subdivision (far models, drawn as
  points by the software pass, which the GPU does not reproduce), models with animated frame groups
  (torches, flames: the GPU's poses are sub-frame 0, the CPU's follow the time), and the view model.
- The entity layer costs a second pass and a second read-back whatever it holds, so a frame with fewer
  than `MinGPUEntities` (3; `--min-gpu-entities`) eligible entities keeps them on the CPU. With
  `--cpuworld` the CPU draws all of them, as before. If the device refuses the models the log says so
  and the CPU draws them.

## Check (a real GPU, Apple M4 Max, the playground's `TestQuake`, `--gpuworld-check`, 120 frames of e1m1)

The whole-frame comparison passes as before (worst frame 0.0105 unexplained against 2.5 %). Entities
are a few percent of a frame, so a whole-frame tolerance cannot fail a wrong skin; the check therefore
also compares the pixels the entities change on either side (the GPU's layer, or what the software
pass drew over the CPU's frame; `AliasTolerance` 3 %):

| | pixels changed by an entity | differ | unexplained |
|---|---|---|---|
| GPU entities | 18,174 | 0 | 0 (0.0000) |
| planted: the GPU's skins shifted by 16 (`--corrupt-gpu-skins`) | 18,663 | 18,585 | 15,981 (0.8563): FAIL |

76 to 98 entity draws over the 120 frames (one or two in view).

## Frame time (abbench `--max-load 8`, 7 rounds, v0.23.0 against this change, the quake image, e1m1, 300 frames)

With the layer used for every entity (`--min-gpu-entities 1`, first version): the software alias pass
drops 0.197 to 0.149 ms (-24 %, p = 0.001) but the world's CPU phase rises 1.128 to 1.434 ms and the
GPU phase 1.723 to 2.017 ms (second pass, second read-back and the composite), a net +0.49 ms of
phases for the one entity of the first view (the frame itself is 16.8 ms, paced at 60 Hz). Hence the
threshold. With the default (3): no difference, since e1m1's first view never has three:

| ms | v0.23.0 | this change | |
|---|---|---|---|
| frame | 16.85 | 16.88 | ~ (p = 0.71) |
| software alias pass | 0.201 | 0.208 | ~ |
| GPU phase | 1.775 | 1.763 | ~ |
| world phase | 1.139 | 1.162 | ~ |

Where there are many entities the GPU wins: on the 16 alias views (several entities each) the GPU
pass adds under 0.1 ms to the frame against 0.59 ms for the software pass
([2026-11-16-alias-pass-cost.md](2026-11-16-alias-pass-cost.md)). So the GPU path is never slower in a
frame than the CPU's (below the threshold it is the CPU's) and takes 0.5 ms off it where entities
crowd the view. The threshold 3 is the break-even worked from those two figures (about 0.3 ms of
layer against 0.2 ms for one entity in the game), not a measurement of a frame with three: no
first-view scene of the freepak has that many in view, and a measurement of a crowded game frame is
open.
