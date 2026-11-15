package gpuworld

import (
	"os"
	"sort"
	"testing"

	"github.com/go-quake1/engine/assets"
	"github.com/go-quake1/engine/bench"
	"github.com/go-quake1/engine/render"
	"github.com/go-quake1/engine/vfs"
	"github.com/go-quake1/engine/worldview"
)

// TestSurfaceAtlasSizes logs, for every e1 map of the freepak, the surface
// atlas each mip level needs: the pixels of its surfaces, and the height the
// shelf packer gives the levels from 0 to k at the atlas width, against the
// limit (MaxAtlasHeight). Needs QUAKE_PAK.
func TestSurfaceAtlasSizes(t *testing.T) {
	path := os.Getenv("QUAKE_PAK")
	if path == "" {
		t.Skip("QUAKE_PAK not set")
	}
	fsys, _, err := bench.OpenPak(path)
	if err != nil {
		t.Fatal(err)
	}
	v := vfs.New()
	v.Add(fsys)
	cm, err := assets.LoadColorMapFrom(v)
	if err != nil {
		t.Fatal(err)
	}
	limit := SurfAtlasWidth * MaxAtlasHeight
	for _, slug := range []string{"e1m1", "e1m2", "e1m3", "e1m4", "e1m5", "e1m6", "e1m7", "e1m8"} {
		w, err := worldview.LoadMap(fsys, slug)
		if err != nil {
			t.Logf("%s: not in the pak (%v)", slug, err)
			continue
		}
		env := &render.SurfaceEnv{Colormap: cm, HaveLightData: w.HaveLightData}
		var area [render.SurfMips]int
		type item struct{ mip, w, h int }
		var items []item
		for _, f := range w.Faces {
			if f == nil {
				continue
			}
			baked, ok := bakeSurfaces(f, env)
			if !ok {
				continue
			}
			for mip, px := range baked {
				sw := f.Surf.Desc.Extents[0] >> mip
				area[mip] += len(px)
				items = append(items, item{mip, sw, len(px) / sw})
			}
		}
		t.Logf("%s: surface pixels per level %v (limit %d): level 0 %.0f%%, levels 0-1 %.0f%%, 0-2 %.0f%%, 0-3 %.0f%%", slug, area, limit,
			100*float64(area[0])/float64(limit), 100*float64(area[0]+area[1])/float64(limit),
			100*float64(area[0]+area[1]+area[2])/float64(limit), 100*float64(area[0]+area[1]+area[2]+area[3])/float64(limit))
		for k := 0; k < render.SurfMips; k++ {
			var sel []item
			for _, it := range items {
				if it.mip <= k {
					sel = append(sel, it)
				}
			}
			sort.SliceStable(sel, func(i, j int) bool {
				if sel[i].h != sel[j].h {
					return sel[i].h > sel[j].h
				}
				return sel[i].w > sel[j].w
			})
			p := newShelfW(SurfAtlasWidth, 1<<20)
			for i, it := range sel {
				if _, err := p.place(new(int), it.w, it.h, func(*Atlas, Rect) {}); err != nil {
					t.Fatalf("%s: unbounded shelf refused item %d", slug, i)
				}
			}
			t.Logf("%s: levels 0-%d packed %d rows high (limit %d)", slug, k, p.sh.Height(), MaxAtlasHeight)
		}
	}
}
