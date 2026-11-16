# TE_EXPLOSION2 and the entity dynamic-light term (engine#106, missing pieces)

Two pieces `2026-11-16-dynamic-lights.md` left out.

- `TE_EXPLOSION2` (`reference/NQ/cl_tent.c:260-271`) allocates a light with `CL_AllocDlight(0)`:
  350 units at the point, `die = cl.time + 0.5`, `decay = 300`. The runner's temp-entity sink
  now makes it (`explosionDLight`, shared with `TE_EXPLOSION`). Its 512 colour-mapped particles
  (`R_ParticleExplosion2`) came afterwards: `render.ParticleExplosion2` (512 `pt_blob`
  particles, 0.3 s, colour `colorStart + i % colorLength`) through a new
  `State.EmitExplosion2(origin, colorStart, colorLength)` sink, tests pinned on the source
  (`TestParticleExplosion2`, `TestApplyExplosion2Sink`, `TestExplosion2Particles`).
- `R_AliasSetupLighting` (`reference/common/r_alias.c:684-691`): for each light with `die >=
  cl.time`, `lightlevel = (int)(radius - |origin - light origin|)`, added to `r_ambientlight`
  when positive; the clamps that follow were already in `render.NewAliasLighting`, which took
  the levels but was never given any. `render.AliasDLightLevels` computes them;
  `AliasEntityInterp.DLights` and `worldview.AliasEnt.DLights` carry them to the CPU draw
  (`DrawAliasInterpQuake`), the runner (`runner.aliasDLights`, over `cl_dlights`) and the GPU
  alias pass (`gpuworld.AliasSet.Entities` puts the lighting in the entity record).

**A difference from tyrquake's source.** `r_alias.c:684` is
`for (i = 0, dlight = cl_dlights; i < MAX_DLIGHTS; i++)` and nothing advances `dlight`, so
tyrquake reads slot 0 on every turn (32 times when it is alive, never slots 1 to 31). id's
Quake visits each slot (`cl_dlights[lnum]`). The port visits each slot; a test pins it
(`TestAliasDLightLevels`, last case).

Tests: values worked from the source (`TestAliasDLightLevels`: 350, 215 for a 3-4-5, 201 for
201.5, a level of 0 or less not added, `die < now` skipped and `die == now` not;
`TestAliasDLightsLightTheEntity`: ambient 128 and shade 40 for light 40 plus 551, and the
front triangle drawn one colormap row brighter), the runner's lights
(`TestAliasDLights`), and the sink (`TestExplosionDLights`, on the real map).

`quake-worldcheck --part alias --dlights` (the dynamic-light scenario of `--dlights` around
each of the 16 alias views, on both sides), Apple GPU: PASS, 361 of 51,891 pixels unexplained
(0.0070, tolerance 0.03); `--noAliasDLights` (the GPU lights the entities without the lights)
FAILs at 0.4856.
