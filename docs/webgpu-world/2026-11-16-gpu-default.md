# The GPU draws the world by default (item 5 of engine#106)

Until v0.21.0 `quake-wctr` drew its world on the CPU unless `--gpuworld` was given. Now the
game loop asks the batch device first (`openGPUWorld`, set by `gpuworld_wasip1.go`, which is
`batchsink.Open`): when the container was run with `--gpu` the lit world faces are drawn on the
GPU; when no GPU is granted (the open fails) or the device refuses the map
(`gpuworld.ErrAtlasFull`) the game prints `QUAKE: GPU world unavailable for <map> (...): the
CPU draws its world` on stderr and plays on the CPU.

| flag | behaviour |
| --- | --- |
| none | GPU when granted, else CPU (logged) |
| `--cpuworld` | CPU, the device is not opened |
| `--gpuworld` | GPU, an error when none is granted |
| `--gpuworld-check --frames N` | as `--gpuworld`, every frame also drawn on the CPU and compared |

`--cpuworld` excludes `--gpuworld` and `--gpuworld-check`. `--worldref` is unchanged: it draws
on the GPU only with `--gpuworld`, so the playground's hashes stay the software frame's.
`TestRunGameDefaultsToTheGPUWorld` pins the four rows with a fake device.
