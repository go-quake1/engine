// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package gpuworld

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/go-gfx/webgpu/batch"

	"github.com/go-quake1/engine/bsprender"
	"github.com/go-quake1/engine/render"
	"github.com/go-quake1/engine/worldview"
)

// TestShadersAreBoundedForTheHost: a host that cannot truncate a vertex or
// fragment stage (the wasmcontainers batch host, go-gfx/webgpu#30) runs a
// render module only if every loop in it is a constant-bound leaf loop of at
// most batch.MaxRenderLoop iterations. Every module the world draws with must
// pass the host's own analysis.
func TestShadersAreBoundedForTheHost(t *testing.T) {
	if render.MaxPolyVerts > batch.MaxRenderLoop {
		t.Fatalf("MaxPolyVerts %d is above the host's loop cap %d", render.MaxPolyVerts, batch.MaxRenderLoop)
	}
	lb := batch.LoopBudget{Work: 1 << 20, Group: 3}
	for name, code := range map[string]string{
		"world": ShaderWGSL, "sky": SpecialShaderWGSL, "turb": TurbShaderWGSL(), "alias": aliasShader(),
	} {
		if _, err := lb.Analyze(code); err != nil {
			t.Errorf("%s: %v (unbounded: %v)", name, err, errors.Is(err, batch.ErrUnbounded))
		}
	}
}

// TestEdgeLoopBoundIsMaxPolyVerts: the shaders spell the bound as a literal;
// it must be render.MaxPolyVerts, the largest face Build accepts, and the loop
// must break at the face's vertex count, so that the frame is the unbounded
// loop's.
func TestEdgeLoopBoundIsMaxPolyVerts(t *testing.T) {
	want := fmt.Sprintf("for (var i = 0u; i < %du; i++)", render.MaxPolyVerts)
	for name, code := range map[string]string{"world": ShaderWGSL, "special": SpecialShaderWGSL} {
		i := strings.Index(code, want)
		if i < 0 {
			t.Errorf("%s: no %q", name, want)
			continue
		}
		if end := min(len(code), i+len(want)+160); !strings.Contains(code[i:end], "if (i >= n) { break; }") {
			t.Errorf("%s: the loop does not break at n", name)
		}
		if strings.Count(code, "for (") != 1 {
			t.Errorf("%s: %d loops, want 1", name, strings.Count(code, "for ("))
		}
	}
}

// TestBuildRefusesAFaceAboveMaxPolyVerts: a face with more vertices than the
// shader's edge loop visits is an error, not a polygon with missing edges.
func TestBuildRefusesAFaceAboveMaxPolyVerts(t *testing.T) {
	face := func(n int) *worldview.World {
		return &worldview.World{Faces: []*worldview.Face{{Idx: 0, Verts: bsprender.FaceVerts{NumVerts: n,
			Vert: func(int) [3]float32 { return [3]float32{} }}}}}
	}
	_, err := Build(face(render.MaxPolyVerts+1), nil)
	if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("%d vertices", render.MaxPolyVerts+1)) {
		t.Errorf("65 vertices: %v", err)
	}
}
