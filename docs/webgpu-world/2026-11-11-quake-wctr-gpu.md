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

## Not done

The game loop. The runner's world pass is interleaved with entities, light and HUD
on the CPU and does not use `worldview.World`; moving it onto `gpuworld` is a
separate piece. Frame time against level 1 was not measured: the 1-minute load was
above 8.
