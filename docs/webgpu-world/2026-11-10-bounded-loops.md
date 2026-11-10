# Bounded loops in the render shaders, E2 of engine#88

A fragment stage cannot write the host's truncation flag, so the wasmcontainers
batch host (go-gfx/webgpu `batch.LoopBudget.Analyze`, v0.16.0 onward) accepts a
render module only if each of its loops is a leaf loop with literal bounds and
step of at most `batch.MaxRenderLoop` (64) iterations; a `break` on a runtime
count is fine, it only shortens the loop. Anything else is `ErrUnbounded`: the
module is not run.

Two loops in the world's modules were `for (var i = 0u; i < n; i++)` over the
polygon's edges:

- `fs` in `ShaderWGSL` (`gpuworld.go`), the scanline run of a face with its
  perspective ends;
- `runOf` in `SpecialShaderWGSL` (`special.go`), the run of a sky or water face.

Both are now `for (var i = 0u; i < 64u; i++) { if (i >= n) { break; } ... }`, 64
being `render.MaxPolyVerts`. The iterations executed are exactly the old loop's
(n of them), so the frame does not change. `Build` refuses a face with more than
`MaxPolyVerts` vertices with an error naming it, rather than drawing it with its
last edges missing. (`bsprender` and the software path already refuse such a
face.) The alias shader has no loop.

## Tests

- `TestShadersAreBoundedForTheHost` runs the host's own analysis on the four
  modules (world, sky, water, alias). On the old shaders it fails for the world,
  sky and water modules with `ErrUnbounded`.
- `TestEdgeLoopBoundIsMaxPolyVerts` pins the literal to `render.MaxPolyVerts`
  and the `break` at `n`, and that each module has exactly one loop.
- `TestBuildRefusesAFaceAboveMaxPolyVerts`: 65 vertices is an error.

Break check, on the shaders and `Build` of the previous commit: all 3 new tests
fail.

## The frame is the same

`cmd/quake-worldcheck`, Google Chrome for Testing 1254, Apple GPU, freepak
v0.2.0 e1m1 (the pak of go-quake1/freepak, sha256 d0867090...), the 16 views.
`TestGPUWorldMatchesTheSoftwareWorld`: 11 of 256000 pixels differ, 0 unexplained,
on v0.11.0 and with this change alike. Sky 4 differ, water 1427 differ (175
unexplained), alias 309 differ (150 unexplained): all within their tolerances,
and the planted negatives (damaged colormap, wrong mip, corrupted skin, sky and
water, wrong light, wrong walk) still fail. One test fails on both v0.11.0 and
this change: `TestNoMipFails` (the GPU without mip levels differs from the
reference by the same 11 pixels, 0 unexplained, on the freepak e1m1, whose
textures do not tell the levels apart); it is not touched here.
Not run: the loop on a real batch host, which needs a go-wasmcontainers/gpu
release on go-gfx/webgpu v0.16.0 or later (none exists; its main pins v0.15.0).
