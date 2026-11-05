// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package render

import (
	"errors"
	"fmt"
	"math"
)

// This file is Quake's surface cache with mip levels: the lit, mip-selected
// texture of a BSP face that the span drawer reads (reference/common r_surf.c,
// d_surf.c, d_edge.c, d_init.c). Every function names the C one it ports.

// SurfMips is the number of mip levels of a texture and of a surface (MIPLEVELS).
const SurfMips = 4

// maxBlockLights is blocklights[18 * 18] (r_surf.c): a surface is at most 17 x
// 17 lightmap samples (extents up to 256).
const maxBlockLights = 18 * 18

// surfCacheHeader is offsetof(surfcache_t, data) (d_local.h) on an LP64 host:
// next and owner (16), lightadj[4] (16), dlight, size, width, height (16),
// mipscale (4, padded to 8), texture (8).
const surfCacheHeader = 64

// surfCacheSizeof is sizeof(surfcache_t): the header plus data[4], padded to 8.
const surfCacheSizeof = 72

// surfCacheGuard is GUARDSIZE (d_surf.c).
const surfCacheGuard = 4

// SurfCacheAt320x200 is SURFCACHE_SIZE_AT_320X200 (d_local.h).
const SurfCacheAt320x200 = 320 * 200 * 14

// baseMip is basemip[] (d_init.c).
var baseMip = [SurfMips - 1]float32{1.0, 0.5 * 0.8, 0.25 * 0.8}

// MipScales is d_scalemip: the scales at which a surface drops a mip level.
type MipScales [SurfMips - 1]float32

// NewMipScales is D_SetupFrame's d_scalemip[i] = basemip[i] * d_mipscale.value
// (the cvar's default is 1).
func NewMipScales(mipscale float32) MipScales {
	var m MipScales
	for i := range m {
		m[i] = baseMip[i] * mipscale
	}
	return m
}

// DefaultMipScales is d_scalemip with the default d_mipscale 1: 1, 0.4, 0.2.
var DefaultMipScales = NewMipScales(1)

// Level is D_MipLevelForScale (d_edge.c): the mip level for
// surf->nearzi * scale_for_mip * texinfo->mipadjust, not below minMip
// (d_minmip, the d_mipcap cvar clamped to 0..3; the default is 0).
func (m MipScales) Level(scale float32, minMip int) int {
	var l int
	switch {
	case scale >= m[0]:
		l = 0
	case scale >= m[1]:
		l = 1
	case scale >= m[2]:
		l = 2
	default:
		l = 3
	}
	if l < minMip {
		l = minMip
	}
	return l
}

// ScaleForMip is scale_for_mip (D_ViewChanged, d_modech.c): the larger of
// xscale and yscale, xscale = vrect.width / horizontalFieldOfView with
// horizontalFieldOfView = 2 tan(fov_x / 360 pi) (R_ViewChanged, r_main.c).
// pixelAspect is 1 for the square pixels of this engine, so yscale = xscale.
func ScaleForMip(width int, fovXDeg float32) float32 {
	hfov := float32(2.0 * math.Tan(float64(fovXDeg/360)*math.Pi))
	xscale := float32(width) / hfov
	return xscale
}

// MipAdjust is texinfo->mipadjust (Mod_LoadTexinfo, model.c): 4, 3, 2 or 1
// from the mean length of the two texture axes.
func MipAdjust(axisS, axisT [3]float32) float32 {
	len1 := float32(math.Sqrt(float64(axisS[0]*axisS[0] + axisS[1]*axisS[1] + axisS[2]*axisS[2])))
	len2 := float32(math.Sqrt(float64(axisT[0]*axisT[0] + axisT[1]*axisT[1] + axisT[2]*axisT[2])))
	len1 = (len1 + len2) / 2
	switch {
	case len1 < 0.32:
		return 4
	case len1 < 0.49:
		return 3
	case len1 < 0.99:
		return 2
	}
	return 1
}

// SurfaceTexture is a texture_t as R_DrawSurface reads it: its size and the
// four mip levels (level k is Width>>k by Height>>k).
type SurfaceTexture struct {
	Width, Height int
	Mips          [SurfMips][]byte
}

// SurfaceDesc is the part of msurface_t that R_BuildLightMap and R_DrawSurface
// read.
type SurfaceDesc struct {
	Extents     [2]int // surf->extents: the surface size in texels at mip 0
	TextureMins [2]int // surf->texturemins
	// Samples is surf->samples: Styles' lightmaps, (Extents>>4 + 1)^2 bytes
	// each, one after the other. Nil: the surface has no lightmap.
	Samples []byte
	Styles  [4]byte // surf->styles; 255 ends the list
	Texture *SurfaceTexture
}

// SurfaceEnv is the renderer state R_BuildLightMap reads besides the surface.
type SurfaceEnv struct {
	Colormap *ColorMap
	// Ambient is r_refdef.ambientlight (the r_ambient cvar, default 0).
	Ambient int
	// HaveLightData is cl.worldmodel->lightdata != NULL.
	HaveLightData bool
	// Fullbright is the r_fullbright cvar.
	Fullbright bool
}

// Errors of the surface port.
var (
	ErrSurfaceExtents = errors.New("render: surface extents out of range")
	ErrSurfaceTexture = errors.New("render: surface texture missing or smaller than a block")
	ErrSurfaceDst     = errors.New("render: surface destination too small")
	ErrSurfaceMip     = errors.New("render: bad mip level")
	ErrCacheWidth     = errors.New("render: bad cache width")
	ErrCacheSize      = errors.New("render: bad cache size")
	ErrCacheTooBig    = errors.New("render: surface larger than the cache")
	ErrCacheEnd       = errors.New("render: hit the end of the surface cache")
)

// BuildLightMap is R_BuildLightMap (r_surf.c): the styles' lightmaps summed in
// 8.8 fixed point with the ambient level, then bounded, inverted and shifted
// into colormap rows (t = (255 * 256 - sum) >> (8 - VID_CBITS), not below
// 1 << 6). blocklights gets (Extents[0]>>4 + 1) * (Extents[1]>>4 + 1)
// values. Dynamic lights are not added (R_AddDynamicLights is not ported).
func BuildLightMap(blocklights []uint32, d *SurfaceDesc, lightadj [4]int, env *SurfaceEnv) error {
	smax := (d.Extents[0] >> 4) + 1
	tmax := (d.Extents[1] >> 4) + 1
	size := smax * tmax
	if d.Extents[0] < 0 || d.Extents[1] < 0 || smax > 18 || tmax > 18 || size > len(blocklights) {
		return ErrSurfaceExtents
	}
	if env.Fullbright || !env.HaveLightData {
		for i := 0; i < size; i++ {
			blocklights[i] = 0
		}
		return nil
	}
	for i := 0; i < size; i++ {
		blocklights[i] = uint32(env.Ambient) << 8
	}
	if d.Samples != nil {
		lightmap := d.Samples
		for maps := 0; maps < 4 && d.Styles[maps] != 255; maps++ {
			if len(lightmap) < size {
				return ErrSurfaceExtents
			}
			scale := uint32(lightadj[maps])
			for i := 0; i < size; i++ {
				blocklights[i] += uint32(lightmap[i]) * scale
			}
			lightmap = lightmap[size:]
		}
	}
	for i := 0; i < size; i++ {
		t := (255*256 - int(int32(blocklights[i]))) >> (8 - 6)
		if t < 1<<6 {
			t = 1 << 6
		}
		blocklights[i] = uint32(t)
	}
	return nil
}

// DrawSurface is R_DrawSurface with R_DrawSurfaceBlock8_mip0..3 (r_surf.c):
// it fills dst (Extents[0]>>mip by Extents[1]>>mip texels, rowbytes the
// width) with the surface's texture at that mip level, lit by the grid of
// BuildLightMap interpolated over blocks of 16>>mip texels, through the
// colormap. The texture repeats from TextureMins>>mip.
func DrawSurface(dst []byte, d *SurfaceDesc, lightadj [4]int, mip int, env *SurfaceEnv) error {
	if mip < 0 || mip >= SurfMips {
		return ErrSurfaceMip
	}
	mt := d.Texture
	if mt == nil || env.Colormap == nil {
		return ErrSurfaceTexture
	}
	var blocklights [maxBlockLights]uint32
	if err := BuildLightMap(blocklights[:], d, lightadj, env); err != nil {
		return err
	}
	surfwidth := d.Extents[0] >> mip
	surfheight := d.Extents[1] >> mip
	if surfwidth*surfheight > len(dst) {
		return ErrSurfaceDst
	}
	texwidth := mt.Width >> mip
	smax := texwidth
	tmax := mt.Height >> mip
	blocksize := 16 >> mip
	blockdivshift := uint(4 - mip)
	if smax < blocksize || tmax < blocksize || len(mt.Mips[mip]) < smax*tmax {
		return ErrSurfaceTexture
	}
	source := mt.Mips[mip]
	lightwidth := (d.Extents[0] >> 4) + 1
	numhblocks := surfwidth >> blockdivshift
	numvblocks := surfheight >> blockdivshift
	sourcetstep := texwidth
	stepback := tmax * texwidth
	sourcemax := tmax * smax

	// << 16 components are to guarantee positive values for %
	soffset := ((d.TextureMins[0] >> mip) + (smax << 16)) % smax
	basetptr := (((d.TextureMins[1] >> mip) + (tmax << 16)) % tmax) * texwidth

	cm := env.Colormap
	for u := 0; u < numhblocks; u++ {
		lightptr := u // r_lightptr = blocklights + u
		pbasesource := basetptr + soffset
		prowdest := u * blocksize

		psource := pbasesource
		for v := 0; v < numvblocks; v++ {
			lightleft := int32(blocklights[lightptr])
			lightright := int32(blocklights[lightptr+1])
			lightptr += lightwidth
			// r_lightptr[0] - lightleft is unsigned in C: the shift is logical.
			lightleftstep := int32((blocklights[lightptr] - uint32(lightleft)) >> blockdivshift)
			lightrightstep := int32((blocklights[lightptr+1] - uint32(lightright)) >> blockdivshift)

			for i := 0; i < blocksize; i++ {
				lighttemp := lightleft - lightright
				lightstep := lighttemp >> blockdivshift
				light := lightright
				for b := blocksize - 1; b >= 0; b-- {
					// (light & 0xFF00) >> 8 is at most 63 for the grid's range
					// (64 to 16320 in 8.8); the mask keeps a bad value from
					// leaving the table.
					row := (int(light&0xFF00) >> 8) & (ColorMapRows - 1)
					dst[prowdest+b] = cm[row][source[psource+b]]
					light += lightstep
				}
				psource += sourcetstep
				lightright += lightrightstep
				lightleft += lightleftstep
				prowdest += surfwidth
			}
			if psource >= sourcemax {
				psource -= stepback
			}
		}
		soffset += blocksize
		if soffset >= smax {
			soffset = 0
		}
	}
	return nil
}

// SurfaceBlock is a surfcache_t: a block of the surface cache, free (no
// owner) or holding the lit texture of one surface at one mip level.
type SurfaceBlock struct {
	Off    int // offset of the block in the cache
	Size   int // bytes, including the header
	Width  int
	Height int // DEBUG only in C

	next  *SurfaceBlock
	owner **SurfaceBlock // nil is an empty chunk of memory

	Data     []byte // Width * the surface's height
	LightAdj [4]int // checked for strobe flush
	Dlight   bool
	Texture  *SurfaceTexture // checked for animating textures
	MipScale float32
}

// SurfaceCache is the surface cache allocator of d_surf.c: one arena that is
// carved into blocks in address order, with a rover that hands out the next
// block and frees whatever it overlaps.
type SurfaceCache struct {
	size    int // sc_size
	base    *SurfaceBlock
	rover   *SurfaceBlock
	initial *SurfaceBlock // d_initial_rover
	wrapped bool          // d_roverwrapped
	// Thrash is r_cache_thrash: the cache wrapped more than once in a frame.
	Thrash bool
}

// SurfaceCacheForRes is D_SurfaceCacheForRes (d_surf.c) without the
// -surfcachesize command line: 14 bytes per pixel up to 320 x 200, 6 for the
// pixels above that up to 800 x 600, 3 above.
func SurfaceCacheForRes(width, height int) int {
	size := SurfCacheAt320x200
	pix := width * height
	if pix > 320*200 {
		size += (min(pix, 800*600) - 320*200) * 6
	}
	if pix > 800*600 {
		size += (pix - 800*600) * 3
	}
	return size
}

// NewSurfaceCache is D_InitCaches (d_surf.c): one free block of size - 4
// bytes (the guard).
func NewSurfaceCache(size int) *SurfaceCache {
	c := &SurfaceCache{size: size - surfCacheGuard}
	c.base = &SurfaceBlock{Size: c.size}
	c.rover = c.base
	return c
}

// Size is sc_size.
func (c *SurfaceCache) Size() int { return c.size }

// Blocks lists the blocks in address order (D_SCDump).
func (c *SurfaceCache) Blocks() []*SurfaceBlock {
	var out []*SurfaceBlock
	for b := c.base; b != nil; b = b.next {
		out = append(out, b)
	}
	return out
}

// Rover is sc_rover.
func (c *SurfaceCache) Rover() *SurfaceBlock { return c.rover }

// SetupFrame is the cache part of D_SetupFrame (d_init.c): the wrap
// detection starts again from the rover. It does not clear Thrash (R_ViewChanged
// and the renderer's frame setup own r_cache_thrash).
func (c *SurfaceCache) SetupFrame() {
	c.wrapped = false
	c.initial = c.rover
}

// Flush is D_FlushCaches: every owner forgets its block, and the cache is one
// free block again.
func (c *SurfaceCache) Flush() {
	for b := c.base; b != nil; b = b.next {
		if b.owner != nil {
			*b.owner = nil
		}
	}
	c.rover = c.base
	c.base.next = nil
	c.base.owner = nil
	c.base.Size = c.size
}

// Alloc is D_SCAlloc: a block for width x (size / width) data bytes. size is
// the data size; the block is header + size, rounded up to 4.
func (c *SurfaceCache) Alloc(width, size int) (*SurfaceBlock, error) {
	if width < 0 || width > 256 {
		return nil, fmt.Errorf("%w: %d", ErrCacheWidth, width)
	}
	if size <= 0 || size > 0x10000 {
		return nil, fmt.Errorf("%w: %d", ErrCacheSize, size)
	}
	size = surfCacheHeader + size
	size = (size + 3) &^ 3
	if size > c.size {
		return nil, fmt.Errorf("%w: %d > %d", ErrCacheTooBig, size, c.size)
	}

	// if there is not size bytes after the rover, reset to the start
	wrappedThisTime := false
	if c.rover == nil || c.rover.Off > c.size-size {
		if c.rover != nil {
			wrappedThisTime = true
		}
		c.rover = c.base
	}
	// collect and free surfcache_t blocks until the rover block is large enough
	n := c.rover
	if c.rover.owner != nil {
		*c.rover.owner = nil
	}
	for n.Size < size {
		// free another
		c.rover = c.rover.next
		if c.rover == nil {
			return nil, ErrCacheEnd
		}
		if c.rover.owner != nil {
			*c.rover.owner = nil
		}
		n.Size += c.rover.Size
		n.next = c.rover.next
	}

	// create a fragment out of any leftovers
	if n.Size-size > 256 {
		c.rover = &SurfaceBlock{Off: n.Off + size, Size: n.Size - size, next: n.next}
		n.next = c.rover
		n.Size = size
	} else {
		c.rover = n.next
	}

	n.Width = width
	if width > 0 {
		n.Height = (size - surfCacheSizeof + 4) / width
	}
	n.owner = nil // should be set properly after return
	n.Data = nil
	n.Texture = nil

	if c.wrapped {
		if wrappedThisTime || c.roverAtOrAfterInitial() {
			c.Thrash = true
		}
	} else if wrappedThisTime {
		c.wrapped = true
	}
	return n, nil
}

// roverAtOrAfterInitial is sc_rover >= d_initial_rover: block addresses
// compare as offsets, and a nil rover (the end, a null pointer) is below
// every block, as in C.
func (c *SurfaceCache) roverAtOrAfterInitial() bool {
	if c.rover == nil {
		return false
	}
	if c.initial == nil {
		return true // sc_rover >= NULL
	}
	return c.rover.Off >= c.initial.Off
}

// SurfaceFace is a surface with its cachespots[MIPLEVELS] (msurface_t).
type SurfaceFace struct {
	Desc  SurfaceDesc
	spots [SurfMips]*SurfaceBlock
}

// Spot is surface->cachespots[mip].
func (f *SurfaceFace) Spot(mip int) *SurfaceBlock { return f.spots[mip] }

// Cache is D_CacheSurface (d_surf.c): the lit surface of f at the mip level,
// from the cache when it holds a block with the same texture and light-style
// values, otherwise built into a block (allocated first unless the surface
// already has one). lightadj is d_lightstylevalue[surface->styles[0..3]].
func (c *SurfaceCache) Cache(f *SurfaceFace, mip int, lightadj [4]int, env *SurfaceEnv) (*SurfaceBlock, error) {
	if mip < 0 || mip >= SurfMips {
		return nil, ErrSurfaceMip
	}
	cache := f.spots[mip]
	// dlights are not ported, so surface->dlightframe never equals r_framecount.
	if cache != nil && !cache.Dlight && cache.Texture == f.Desc.Texture && cache.LightAdj == lightadj {
		return cache, nil
	}
	// determine shape of surface
	surfscale := float32(1.0 / float64(int(1)<<mip))
	surfwidth := f.Desc.Extents[0] >> mip
	surfheight := f.Desc.Extents[1] >> mip
	if cache == nil { // if a texture just animated, don't reallocate it
		var err error
		cache, err = c.Alloc(surfwidth, surfwidth*surfheight)
		if err != nil {
			return nil, err
		}
		f.spots[mip] = cache
		cache.owner = &f.spots[mip]
		cache.MipScale = surfscale
	}
	cache.Dlight = false
	if len(cache.Data) != surfwidth*surfheight {
		cache.Data = make([]byte, surfwidth*surfheight)
	}
	cache.Texture = f.Desc.Texture
	cache.LightAdj = lightadj
	if err := DrawSurface(cache.Data, &f.Desc, lightadj, mip, env); err != nil {
		return nil, err
	}
	return f.spots[mip], nil
}
