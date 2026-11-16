# Alias triangles are clipped, as r_aclip.c does (engine#106, item 8)

A triangle of an alias model that crossed the near plane was dropped by
`render.DrawAliasInterpQuake`, and one that crossed a side of the frame was cut only by the
pixel loop. A monster or the view weapon close to the camera lost its nearest triangles. The
reference is `reference/common/r_aclip.c` (`R_AliasClipTriangle` 202, `R_AliasClip` 160,
`R_Alias_clip_z` 37, `R_Alias_clip_left` 85 and the three others) with the vertex flags of
`R_AliasPreparePoints` (`reference/common/r_alias.c` 346).

## What was ported

- `render.AliasClipFlags`: the clip flags of a projected vertex (`ALIAS_LEFT_CLIP`, `TOP`,
  `RIGHT`, `BOTTOM`; a vertex nearer than `ALIAS_Z_CLIP_PLANE` has `ALIAS_Z_CLIP` and no
  position). The rectangle is `aliasvrect`: `x + width` and `y + height` are
  `aliasvrectright` and `aliasvrectbottom`, so the first pixel past the frame is not outside.
- `render.AliasClipTriangle` (`alias_clip.go`): the triangle's vertices are copied (a seam
  vertex of a back-facing triangle gets `seamfixupX16` once, here, and its flags are
  cleared at the end so `D_PolysetDraw` does not add it twice, as in the C), clipped against
  the near plane (`R_Alias_clip_z`: the edge's crossing of `z = 5`, interpolated from the
  unclipped vertex, `u` and `v` projected from it, `s`, `t` and the light interpolated and
  truncated), then, if the flags of the first three vertices say so (as the C tests them,
  after the near plane too), against the left, right, bottom and top sides
  (`R_Alias_clip_*`: every component interpolated and rounded with `+ 0.5` in double);
  the polygon's vertices are clamped to the rectangle and drawn as the fan (0, i, i + 1).
  At most 8 vertices (`fv[2][8]`).
- `DrawAliasInterpQuake`: for a model `R_AliasCheckBBox` finds partly outside the view
  (`AliasClipped`) each triangle is looked at as `R_AliasPreparePoints` does: wholly clipped
  (the flags in common) is skipped, partly clipped goes through the clipper, the others are
  drawn whole.

Tests are pinned on clips worked by hand from the source, not produced by the code: a
triangle with one vertex behind the near plane (both edges to it cross `z = 5` at
`(5 - 10) / (1 - 10) = 0.5555556`; the four vertices of the polygon, their `u`, `v`, `s`,
`t` and light, all worked out in the test), one with two vertices behind, one wholly behind,
the left clip (the `0.75` of `(0 - 30) / (-10 - 30)`) and the right one, a triangle larger
than the frame on every side, wholly outside each side, the seam fixup done once, and the
end-to-end picture of a model across the near plane (a triangle 280 pixels wide at row 83
with its apex at row 111, about 3,920 pixels; before, none). Mutation: not drawing the
fan fails the picture test. `render` stays at 100 % coverage.

## The GPU

`render/gpuworld`'s alias vertex shader reproduces `R_AliasPreparePoints` and
`R_AliasClipTriangle` in WGSL (`FV`, `zEdge`, `sideEdge`, `clipStage`; the loops are
bounded at 8, the polygon is kept in a 16-vertex private array, ping-ponged). A vertex
invocation is one corner of one of the six fan triangles the clipped polygon can have
(`FanStride` = 18 vertices for each source triangle), and recomputes the clip of its
triangle; the triangles beyond the polygon's count are degenerate. Only the entities the
CPU classifies as `AliasClipped` (`render.AliasClassify`, the same R_AliasCheckBBox)
take the 18-vertex stride; the others keep 3, their cost unchanged. The entity record
carries the stride (`info.z`) and the model's first corner (`info.w`), because a
non-indexed draw's `vertex_index` starts at the draw's first vertex.

The interpolation is in `f32` where the C has `float` and `double` (the `+ 0.5` of the side
clips): a coordinate within an ulp of a half is rounded the other way, which is a one-pixel
shift, inside the check's measure.

## Check (a real GPU, Apple M4 Max, Chrome, `QUAKE_REQUIRE_GPU=1`, `-count=1`)

`quake-worldcheck --part alias --nearplane`: the 16 alias views with the camera 10, 16, 22
and 28 units from the target's origin (the view weapon follows the camera), so that
37 entities cross the near plane or the frame.

| | alias pixels | differ | unexplained | tolerance |
|---|---|---|---|---|
| near-plane views, GPU clipping | 173,271 | 584 | 286 (0.17 %) | 3 % |
| planted: the GPU drops them (`--noAliasClip`) | 173,457 | 14,408 | 10,976 (6.33 %) | FAIL |
| the 16 ordinary alias views, v0.18.0 (no clipping) | 49,951 | 729 | 346 (0.69 %) | 3 % |
| the same, with the clipping | 50,018 | 653 | 346 (0.69 %) | 3 % |

The ordinary views include the view weapon, which is partly behind the near plane in
every one: the clipped weapon is drawn by both sides and agrees.

Before the GPU used the clipper, the first run failed: 34.7 % on the ordinary views and
58.1 % on the near-plane ones. The cause was the shader taking `vertex_index` as the
index from the start of the model, where a draw's first vertex is its first corner; the
check caught it.

Native: the entity records carry stride 3 and the model's first corner, or `FanStride` for
an entity that is clipped (needs `QUAKE_PAK`).

## Alias pass, before and after

`abbench --max-load 8`, 7 rounds alternating a clean clone of v0.20.0 and this change
(`BenchmarkAliasPass`: the engine's software alias pass and the GPU one alternating frame by
frame on the 16 alias views at 1280x800), Apple M4 Max. The weapon, partly behind the near
plane in every view, is clipped in this change, so every ordinary view has a clipped entity.

| ordinary alias views | v0.20.0 | this change | |
|---|---|---|---|
| GPU alias pass alone, wall to idle (ms) | 0.945 ± 3 % | 1.015 ± 5 % | +7.4 % (p = 0.001) |
| GPU world + alias, wall to idle (ms) | 1.220 ± 3 % | 1.275 ± 7 % | +4.5 % (p = 0.002) |
| same, p95 (ms) | 1.510 | 1.680 | +11 % (p = 0.001) |
| software alias pass (ms) | 0.605 | 0.600 | ~ |

The GPU pays for the fan: an entity that is clipped draws 18 vertices for each triangle
instead of 3, each recomputing the clip. The software pass is not slower on the same views.

With the camera at the near plane of each target (`--nearplane`, this change on both sides,
the ordinary views as the baseline): the GPU alias pass alone 0.875 to 0.990 ms (+13 %, p =
0.016), the frame 1.125 to 1.285 ms (+14 %), p95 1.635 to 1.960 ms (+20 %), and the software
alias pass 0.585 to 1.200 ms (+105 %: the models fill the frame, so there are more pixels
to draw). As in v0.20.0 the GPU alias pass alone is slower than the CPU one at this size
(1.0 ms against 0.6 ms: the GPU time has a floor of about half a millisecond); that gap is not
new, and this change widens it by 0.07 ms on the ordinary views. Fewer vertex invocations for
the clipped entities (a fan of the polygon's size, or the clip run once for the three corners)
is the way to close it and is not done here.
