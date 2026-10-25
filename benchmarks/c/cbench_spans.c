/* SPDX-License-Identifier: GPL-2.0-or-later
 * D_DrawSpans8 below is Copyright (C) 1996-1997 Id Software, Inc., via tyrquake.
 */
/* Standalone micro-benchmark of tyrquake@6531579's D_DrawSpans8
 * (common/d_scan.c) -- the Quake perspective-correct textured span
 * rasterizer. The D_DrawSpans8 body below is COPIED VERBATIM from
 * tyrquake; only the surrounding globals/structs are provided locally
 * (they are plain externs in tyrquake) so it compiles standalone.
 *
 * This is the C reference for go-quake1's
 * render.FillPerspectiveTexturedPolygon (same 8-px subdivide / 1/z
 * divide / 16.16 fixed-point sampling algorithm).
 */
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>
#include <stdint.h>

typedef unsigned char byte;
typedef unsigned char pixel_t;
typedef int fixed16_t;

typedef struct espan_s {
    int u, v, count;
    struct espan_s *pnext;
} espan_t;

/* ---- globals D_DrawSpans8 reads (tyrquake externs) ---- */
float d_sdivzstepu, d_tdivzstepu, d_zistepu;
float d_sdivzstepv, d_tdivzstepv, d_zistepv;
float d_sdivzorigin, d_tdivzorigin, d_ziorigin;
fixed16_t sadjust, tadjust;
fixed16_t bbextents, bbextentt;
pixel_t *cacheblock;
int cachewidth;
pixel_t *d_viewbuffer;
int screenwidth;

/* ===================== VERBATIM from tyrquake common/d_scan.c ===================== */
void
D_DrawSpans8(espan_t *pspan)
{
    int count, spancount;
    unsigned char *pbase, *pdest;
    fixed16_t s, t, snext, tnext, sstep, tstep;
    float sdivz, tdivz, zi, z, du, dv, spancountminus1;
    float sdivz8stepu, tdivz8stepu, zi8stepu;

    sstep = 0;
    tstep = 0;

    pbase = (unsigned char *)cacheblock;

    sdivz8stepu = d_sdivzstepu * 8;
    tdivz8stepu = d_tdivzstepu * 8;
    zi8stepu = d_zistepu * 8;

    do {
	pdest = (unsigned char *)((byte *)d_viewbuffer + (screenwidth * pspan->v) + pspan->u);
	count = pspan->count;

	du = (float)pspan->u;
	dv = (float)pspan->v;

	sdivz = d_sdivzorigin + dv * d_sdivzstepv + du * d_sdivzstepu;
	tdivz = d_tdivzorigin + dv * d_tdivzstepv + du * d_tdivzstepu;
	zi = d_ziorigin + dv * d_zistepv + du * d_zistepu;
	z = (float)0x10000 / zi;

	s = (int)(sdivz * z) + sadjust;
	if (s > bbextents) s = bbextents;
	else if (s < 0) s = 0;

	t = (int)(tdivz * z) + tadjust;
	if (t > bbextentt) t = bbextentt;
	else if (t < 0) t = 0;

	do {
	    if (count >= 8) spancount = 8;
	    else spancount = count;

	    count -= spancount;

	    if (count) {
		sdivz += sdivz8stepu;
		tdivz += tdivz8stepu;
		zi += zi8stepu;
		z = (float)0x10000 / zi;

		snext = (int)(sdivz * z) + sadjust;
		if (snext > bbextents) snext = bbextents;
		else if (snext < 8) snext = 8;

		tnext = (int)(tdivz * z) + tadjust;
		if (tnext > bbextentt) tnext = bbextentt;
		else if (tnext < 8) tnext = 8;

		sstep = (snext - s) >> 3;
		tstep = (tnext - t) >> 3;
	    } else {
		spancountminus1 = (float)(spancount - 1);
		sdivz += d_sdivzstepu * spancountminus1;
		tdivz += d_tdivzstepu * spancountminus1;
		zi += d_zistepu * spancountminus1;
		z = (float)0x10000 / zi;
		snext = (int)(sdivz * z) + sadjust;
		if (snext > bbextents) snext = bbextents;
		else if (snext < 8) snext = 8;

		tnext = (int)(tdivz * z) + tadjust;
		if (tnext > bbextentt) tnext = bbextentt;
		else if (tnext < 8) tnext = 8;

		if (spancount > 1) {
		    sstep = (snext - s) / (spancount - 1);
		    tstep = (tnext - t) / (spancount - 1);
		}
	    }

	    do {
		*pdest++ = *(pbase + (s >> 16) + (t >> 16) * cachewidth);
		s += sstep;
		t += tstep;
	    } while (--spancount > 0);

	    s = snext;
	    t = tnext;

	} while (count > 0);

    } while ((pspan = pspan->pnext) != NULL);
}
/* ================================================================================== */

#define SW 320
#define SH 200
#define TEXW 64
#define TEXH 64

static double now_ns(void) {
    struct timespec ts;
    clock_gettime(CLOCK_MONOTONIC, &ts);
    return (double)ts.tv_sec * 1e9 + (double)ts.tv_nsec;
}

int main(int argc, char **argv) {
    int frames = (argc > 1) ? atoi(argv[1]) : 2000;

    /* Texture + framebuffer */
    static pixel_t tex[TEXW * TEXH];
    static pixel_t fb[SW * SH];
    for (int i = 0; i < TEXW * TEXH; i++) tex[i] = (pixel_t)((i * 37 + 11) & 0xff);

    cacheblock = tex;
    cachewidth = TEXW;
    d_viewbuffer = fb;
    screenwidth = SW;

    /* One perspective-correct textured surface covering the whole
     * 320x200 view: gradients in s/z, t/z, 1/z so D_DrawSpans8 does a
     * real divide every 8 px. Mirror these EXACTLY in the Go bench. */
    bbextents = (TEXW - 1) << 16;
    bbextentt = (TEXH - 1) << 16;
    sadjust = 0;
    tadjust = 0;

    /* Choose a slanted plane: 1/z varies left->right and top->bottom. */
    d_ziorigin   = 0.10f;     /* at u=0,v=0 */
    d_zistepu    = 0.0009f;   /* per screen-x */
    d_zistepv    = 0.0006f;   /* per screen-y */
    /* s/z, t/z chosen so s=(s/z)*z stays in texture range over the plane */
    d_sdivzorigin = 0.10f * 8.0f;
    d_sdivzstepu  = 0.0009f * 30.0f;
    d_sdivzstepv  = 0.0006f * 4.0f;
    d_tdivzorigin = 0.10f * 6.0f;
    d_tdivzstepu  = 0.0009f * 5.0f;
    d_tdivzstepv  = 0.0006f * 28.0f;

    /* Build one espan per scanline covering full width, chained. */
    static espan_t spans[SH];
    for (int y = 0; y < SH; y++) {
        spans[y].u = 0;
        spans[y].v = y;
        spans[y].count = SW;
        spans[y].pnext = (y + 1 < SH) ? &spans[y + 1] : NULL;
    }

    /* Warmup */
    for (int f = 0; f < 50; f++) D_DrawSpans8(&spans[0]);

    double t0 = now_ns();
    for (int f = 0; f < frames; f++) D_DrawSpans8(&spans[0]);
    double t1 = now_ns();

    double total_ns = t1 - t0;
    long pixels = (long)SW * SH * frames;
    double ns_per_frame = total_ns / frames;
    double ns_per_pixel = total_ns / (double)pixels;

    /* checksum to prevent dead-code elimination */
    unsigned long sum = 0;
    for (int i = 0; i < SW * SH; i++) sum += fb[i];

    printf("c_drawspans8 frames=%d res=%dx%d\n", frames, SW, SH);
    printf("ms_per_frame=%.6f\n", ns_per_frame / 1e6);
    printf("ns_per_pixel=%.4f\n", ns_per_pixel);
    printf("fps_equiv=%.1f\n", 1e9 / ns_per_frame);
    printf("checksum=%lu\n", sum);
    return 0;
}
