// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package render

// AddDynamicLights is R_AddDynamicLights (reference/common/r_surf.c): it adds,
// to the 8.8 light grid of a surface, the light of every dynamic light in the
// surface's DLightBits. blocklights holds (Extents[0]>>4 + 1) values per row,
// as BuildLightMap fills it; lights is cl_dlights.
//
// For each light: the radius left on the surface's plane is rad = radius -
// |distance to the plane|; a light with rad below its minlight adds nothing;
// the light's impact point on the plane is put in texture space (relative to
// texturemins); and every grid sample (16 texels apart) whose distance to the
// impact, measured as the larger of the two axis distances plus half the
// smaller, is under rad - minlight, gets (rad - distance) * 256.
func AddDynamicLights(blocklights []uint32, d *SurfaceDesc, lights []DLight) {
	smax := (d.Extents[0] >> 4) + 1
	tmax := (d.Extents[1] >> 4) + 1
	for lnum := 0; lnum < MaxDLights && lnum < len(lights); lnum++ {
		if d.DLightBits&(1<<uint(lnum)) == 0 {
			continue // not lit by this light
		}
		l := &lights[lnum]
		rad := l.Radius
		dist := l.Origin[0]*d.Plane.Normal[0] + l.Origin[1]*d.Plane.Normal[1] + l.Origin[2]*d.Plane.Normal[2] - d.Plane.Dist
		if dist < 0 {
			rad -= -dist
		} else {
			rad -= dist
		}
		minlight := l.MinLight
		if rad < minlight {
			continue
		}
		minlight = rad - minlight

		var impact [3]float32
		for i := range impact {
			impact[i] = l.Origin[i] - d.Plane.Normal[i]*dist
		}
		v0, v1 := &d.TexVecs[0], &d.TexVecs[1]
		local0 := impact[0]*v0[0] + impact[1]*v0[1] + impact[2]*v0[2] + v0[3]
		local1 := impact[0]*v1[0] + impact[1]*v1[1] + impact[2]*v1[2] + v1[3]
		local0 -= float32(d.TextureMins[0])
		local1 -= float32(d.TextureMins[1])

		for t := 0; t < tmax; t++ {
			td := int(local1 - float32(t*16)) // int td = float: truncated toward zero
			if td < 0 {
				td = -td
			}
			for s := 0; s < smax; s++ {
				sd := int(local0 - float32(s*16))
				if sd < 0 {
					sd = -sd
				}
				var dd float32
				if sd > td {
					dd = float32(sd + (td >> 1))
				} else {
					dd = float32(td + (sd >> 1))
				}
				if dd < minlight {
					// unsigned += float: the sum is made in float and truncated
					blocklights[t*smax+s] = uint32(float32(blocklights[t*smax+s]) + (rad-dd)*256)
				}
			}
		}
	}
}
