# The server's light styles (engine#106)

Until now the world followed a built-in table of the 11 standard styles
(`worldview.LightStyleValue`) and the QC `lightstyle()` builtin was a no-op, so a map's
switchable lights (styles 32 and up, set by `light` entities with a `style` key) stayed at
their steady value and a mod's own strings were ignored, on the CPU and on the GPU path.

## What is ported, from the reference

| piece | reference | here |
|---|---|---|
| `lightstyle(style, string)` builtin | `common/pr_cmds.c:1313-1347` (`PF_lightstyle`) | `server.Server.SetLightStyle`, `host.BuiltinLightStyle` (registered in `runner` and `game`) |
| the message: byte 12, style byte, string | `NQ/protocol.h:229`, `pr_cmds.c:1333-1335` | `server.EncodeLightStyle` |
| all 64 styles at spawn | `NQ/sv_user.c:913-918` (`Host_Spawn_f`) | `server.SendLightStyles`, after the baselines in `runner.Setup`, and after a `changelevel` |
| parse and keep | `NQ/cl_parse.c:1053-1061` (cut to `MAX_STYLESTRING` 64 with its NUL) | `client.DecodedLightStyle`, `applyLightStyle`, `State.LightStyles` |
| animate | `common/r_light.c:42-61` (`R_AnimateLight`) | `client.State.LightStyleValue` |

`PF_lightstyle` keeps the string in `sv.lightstyles` and, only when the server is active,
writes `svc_lightstyle` to every client that is active or spawned; while a map loads (worldspawn
sets styles 0 to 11 then) it only keeps them, and the client is sent all of them when it spawns.
`R_AnimateLight`: an unset style (no letters) is 256, otherwise the letter `map[(int)(cl.time * 10) % length]`
minus `'a'`, times 22. A style number of 64 or more in a message is `Sys_Error` in the C client and
an error here; in the QC call it writes outside `sv.lightstyles` in C and is ignored here.

The runner uses the client's styles (`lightStyleOf`) for the surface cache, the fallback lightmap
plane and the lighting of entities, once the first `svc_lightstyle` has arrived
(`State.HaveLightStyles`); before that, and for a scene with no server, it keeps the default
table. The GPU world takes them through `runner.LightStyler` (`SetLightStyles`, called before
`DrawWorld`, like `SetDLights`), and `gameWorld` puts them in `World.Anim.Style`; the GPU then
re-bakes the surfaces whose styles moved, as it does for the default table.

## Tests

Pinned on the reference, not on the code's output: `TestEncodeLightStyle` (the bytes), `TestSendLightStyles`
(64 messages in order, an unset style as the empty string), `TestSetLightStyle` (active or spawned
clients, not while loading), `TestDecodeLightStyle`, `TestApplyLightStyle` (63 letters kept of 70),
`TestLightStyleValue` (`"abcd"` at 0.35 s is 66, at 0.4 s 0; `"m"` 264; `"z"` 550; unset 256),
`TestBuiltinLightStyle`. On the real progs.dat (needs `QUAKE_PAK`):
`TestWorldspawnSetsTheServersLightStyles` (styles 0 to 2 are id's), `TestServerLightStylesReachTheClient`
(after the signon drains the client holds them, style 1 at 0.15 s is 264 and at 0.25 s 286),
and `TestGameWorldFollowsTheServersLightStyles`.

## Check (a real GPU, Apple M4 Max, Chrome, `QUAKE_REQUIRE_GPU=1`)

`quake-worldcheck --anim --serverStyles`: styles 1 to 11 set to strings of `a` (dark) and `z`
(twice as bright) that the default table does not have, taken through `client.State.LightStyleValue`
on the reference and on the GPU, on the 8 animated views over 64 frames: 144 of 4,096,000 pixels
unexplained (0.0000), PASS. The planted defect, the GPU keeping the default table while the
reference has the server's styles (`--noServerStyles`): 1,473,971 unexplained (36.0 %), FAIL.
