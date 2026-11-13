# quake-wctr draws the world on the batch device, E3 of engine#88

`quake-wctr --worldref N --gpuworld` (GOOS=wasip1, `wctr run --gpu`):

1. the software frame of view N is drawn and its two hashes printed first, as
   before (the figures the playground's `TestQuake` compares are unchanged);
2. `render/gpuworld` builds the map on `batchsink.Open().Device(...)` and draws the
   view; `Renderer.DrawRead` records the pass, `Recorder.CopyTextureToBuffer`
   (rows padded to 256 bytes) and `ReadBuffer` in one submit, so a frame is one
   host crossing; the frame must fit the host's 1 MiB read window (640x480 is
   368 640 padded bytes);
3. the index frame is compared with the software one by `gpuworld.Compare`, the
   measure of `cmd/quake-worldcheck`: pixels that differ and that no one-pixel
   shift of the reference explains, against `gpuworld.Tolerance` (2.5 %). Over it
   the run fails. The GPU frame's own SHA-256 is printed for information; it is not
   a check, because a GPU frame is not hash-equal to the CPU's (worldcheck on the
   Apple GPU: 11 of 256 000 pixels differ on the freepak e1m1);
4. the GPU frame is presented through `wasipresent` (level 1's presenter).

The issue's premise, "the playground's TestQuake hash is unchanged" and "the GPU
frame hash equals the CPU reference", holds for the first only: TestQuake's
goldens stay the software frame's.

## Frame time

`--worldref N --frames K` (software) and `--worldref N --gpuworld --frames K` redraw
view N K+1 times. The first frame is printed apart as the warm-up (for the GPU, a
device of its own: open, map build, atlas upload); the other K give the mean, median,
min and max, in milliseconds of wall-clock time per frame, present excluded. The GPU
frame is timed from the start of the draw to the index frame in memory (one submit
with the read-back).

## The game loop on the GPU (engine v0.15.0)

`quake-wctr --gpuworld` (without `--worldref`) plays with the lit world faces drawn by
`gpuworld`. `runner.SetupOpts.WorldDraw` is a `WorldDrawer`: the runner calls it once a
frame, before the rest of its pass, and it fills the frame buffer with the GPU's index
frame (one submit, read-back in it). What is composited where:

- GPU: the lit world faces (`gpuworld`, base textures and lightmaps through the colormap).
- CPU, over it, in the software pass's own order: sky and water faces (the GPU renderer
  draws the engine's polygon sky, not Quake's dome, so they stay on the CPU), the entities
  (alias models, beams, sprites), the particles, the view model, the status bar, the menu.
- Not reproduced on the GPU: light styles other than their resting value and the animation
  of `+` textures (the atlas is baked once); the CPU path animates both.

The device is opened and the atlases uploaded once, before the first frame. The map is
fixed for a run of `quake-wctr`, so there is no level change to rebuild for. A map whose
surfaces do not fit the 8192 x 8192 surface atlas (`ErrAtlasFull`: freepak e1m2, e1m3,
e1m6) plays on the CPU, with a log line.

Draw quota of the host (1 Mi vertices per draw, 16 Mi per stream): a frame draws each face
at most once, so the whole index buffer bounds the stream. Freepak, `TestDrawQuotaHoldsForEveryMap`:
e1m1 25,671 indices, e1m4 30,102, e1m5 30,093, e1m7 24,882, e1m8 38,571; largest draw 27.

`--gpuworld-check --frames N` draws each frame whole on the CPU as well and compares the two
worlds (sky and water included, no entities) at worldcheck's measure. In the playground's
`TestQuake` on the Apple GPU, 30 frames: 18,045 of 7,680,000 pixels differ, 17,616
unexplained, worst frame 0.49 % (tolerance 2.5 %). Planted fault (the pitch off by 20
degrees): worst frame 70.97 %, the test fails.
