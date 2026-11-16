# Where the GPU alias pass's time goes (engine#106)

`2026-11-16-alias-clip.md` left the GPU alias pass "slower than the CPU's": 1.015 ms of wall
time to idle, alone, against 0.60 ms for the software pass. The two numbers are not the same
thing, and measuring the parts shows it.

## What the numbers measured

The alone figure is a whole submit of a pass with only the alias entities in it (the world
skipped), then a wait until the GPU is idle. A submit and a wait cost something whatever the
pass holds; the software pass has no such floor. `BenchmarkAliasPass` now also times the same
pass with no entity and no face (`gpu-floor-wall-ms`), the CPU time of the call that draws the world
and of the call that draws the world with the entities (`gpu-world-cpu-ms`, `gpu-frame-cpu-ms`),
and the world alone (`gpu-world-wall-ms`).

`abbench --max-load 8`, 7 rounds kept (2 discarded for load), the same binary on both sides
so that the 14 samples are of one build (the sides differ by nothing: p = 0.3 to 0.8 on every row),
the 16 alias views at 1280x800, Apple M4 Max, medians:

| | ms |
|---|---|
| software alias pass (the CPU, on the main thread) | 0.59 |
| GPU alias pass alone, wall to idle | 0.97 |
| the same pass with nothing in it: the floor of a submit and a wait | 0.68 |
| GPU alias pass alone, less the floor | 0.29 |
| GPU world alone, wall to idle | 1.35 |
| GPU world with the alias pass in the same pass, wall to idle | 1.23 |
| CPU time of the call that draws the world | 0.19 |
| CPU time of the call that draws the world and the entities | 0.17 |

So 0.68 ms of the 0.97 ms is the floor, which is not alias work; what the alias pass adds
is 0.29 ms of GPU time when it is alone. In the frame the game draws, the entities go in the same
pass as the world, and a frame with them takes no longer than the world alone (1.23 against
1.35 ms: the difference is within the noise of the order the passes run in, and negative, so
the pass costs less than the measurement can see, under 0.1 ms), and the CPU time of the call is
no higher (0.17 against 0.19 ms: the entity records are 128 bytes each and one buffer write).
The CPU alias pass costs 0.59 ms of the main thread's time in every frame; on the GPU path
that time is not spent.

## Conclusion

On the figure the fleet rule uses (the cost the frame pays, against the reference tool's) the GPU
alias pass is not slower than the CPU's: it takes the 0.59 ms of CPU time away and adds at most the
noise of the measurement to the frame's wall time. The 1.015 against 0.60 compared a pass that pays
a submit's floor with one that does not. The shader does recompute the three vertices of a triangle in
each of the triangle's three vertex invocations, and each triangle covers its bounding box; those
are real redundancies, but removing them cannot show in the frame at this size (under 0.1 ms of
GPU time to gain), so they are not changed here.
