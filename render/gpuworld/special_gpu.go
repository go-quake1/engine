// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

//go:build (js && wasm) || wasip1

package gpuworld

import (
	"github.com/go-gfx/webgpu"

	"github.com/go-quake1/engine/worldview"
)

// specialGPU is one kind of special face on the device: its geometry, its
// texture atlas and its pipeline.
type specialGPU struct {
	geo    *Special
	pipe   *webgpu.RenderPipeline
	group  *webgpu.BindGroup
	vb, ib *webgpu.Buffer
	draws  []Draw
}

func (r *Renderer) newSpecial(geo *Special, entry, label, code string) (*specialGPU, error) {
	dev := r.dev
	s := &specialGPU{geo: geo}
	tex, err := dev.CreateTexture(webgpu.TextureDescriptor{Label: label + " textures", Size: webgpu.Extent3D{Width: uint32(geo.Tex.Width), Height: uint32(geo.Tex.Height)},
		Format: webgpu.TextureFormatR8Uint, Usage: webgpu.TextureUsageTextureBinding | webgpu.TextureUsageCopyDst})
	if err != nil {
		return nil, err
	}
	if err := r.upload(tex, geo.Tex); err != nil {
		return nil, err
	}
	tv, err := tex.CreateView(nil)
	if err != nil {
		return nil, err
	}
	buf := func(name string, data []byte, usage webgpu.BufferUsage) (*webgpu.Buffer, error) {
		b, err := dev.CreateBuffer(webgpu.BufferDescriptor{Label: name, Size: uint64((len(data) + 3) &^ 3), Usage: usage | webgpu.BufferUsageCopyDst})
		if err != nil {
			return nil, err
		}
		return b, writeBuffer(dev, b, data)
	}
	vdata, idata := geo.Vertices, make([]byte, 4*len(geo.Indices))
	for i, v := range geo.Indices {
		idata[4*i], idata[4*i+1], idata[4*i+2], idata[4*i+3] = byte(v), byte(v>>8), byte(v>>16), byte(v>>24)
	}
	if len(vdata) == 0 { // a map without such faces: keep the buffers valid
		vdata, idata = make([]byte, SpecialStride), make([]byte, 4)
	}
	if s.vb, err = buf(label+" vertices", vdata, webgpu.BufferUsageVertex); err != nil {
		return nil, err
	}
	if s.ib, err = buf(label+" indices", idata, webgpu.BufferUsageIndex); err != nil {
		return nil, err
	}
	uint2d := &webgpu.TextureBindingLayout{SampleType: webgpu.TextureSampleTypeUint}
	bgl, err := dev.CreateBindGroupLayout(webgpu.BindGroupLayoutDescriptor{Entries: []webgpu.BindGroupLayoutEntry{
		{Binding: 0, Visibility: webgpu.ShaderStageVertex | webgpu.ShaderStageFragment, Buffer: &webgpu.BufferBindingLayout{Type: webgpu.BufferBindingTypeUniform}},
		{Binding: 1, Visibility: webgpu.ShaderStageFragment, Texture: uint2d},
		{Binding: 2, Visibility: webgpu.ShaderStageFragment, Texture: uint2d},
		{Binding: 3, Visibility: webgpu.ShaderStageFragment, Buffer: &webgpu.BufferBindingLayout{Type: webgpu.BufferBindingTypeReadOnlyStorage}},
		{Binding: 4, Visibility: webgpu.ShaderStageFragment, Buffer: &webgpu.BufferBindingLayout{Type: webgpu.BufferBindingTypeReadOnlyStorage}},
	}})
	if err != nil {
		return nil, err
	}
	pl, err := dev.CreatePipelineLayout(webgpu.PipelineLayoutDescriptor{BindGroupLayouts: []*webgpu.BindGroupLayout{bgl}})
	if err != nil {
		return nil, err
	}
	ftab, fverts := geo.FaceTab, geo.FaceVerts
	if len(ftab) == 0 {
		ftab = make([]byte, FaceTabStride)
	}
	if len(fverts) == 0 {
		fverts = make([]byte, 16)
	}
	fb, err := buf(label+" faces", ftab, webgpu.BufferUsageStorage)
	if err != nil {
		return nil, err
	}
	fvb, err := buf(label+" face vertices", fverts, webgpu.BufferUsageStorage)
	if err != nil {
		return nil, err
	}
	if s.group, err = dev.CreateBindGroup(webgpu.BindGroupDescriptor{Layout: bgl, Entries: []webgpu.BindGroupEntry{
		{Binding: 0, Buffer: r.ub}, {Binding: 1, TextureView: tv}, {Binding: 2, TextureView: r.cmView}, {Binding: 3, Buffer: fb}, {Binding: 4, Buffer: fvb},
	}}); err != nil {
		return nil, err
	}
	mod, err := dev.CreateShaderModule(webgpu.ShaderModuleDescriptor{Label: label, Code: code})
	if err != nil {
		return nil, err
	}
	s.pipe, err = dev.CreateRenderPipeline(webgpu.RenderPipelineDescriptor{
		Layout: pl,
		Vertex: webgpu.VertexState{Module: mod, EntryPoint: "vs", Buffers: []webgpu.VertexBufferLayout{{ArrayStride: SpecialStride, Attributes: []webgpu.VertexAttribute{
			{Format: webgpu.VertexFormatFloat32x3, Offset: 0, ShaderLocation: 0},
			{Format: webgpu.VertexFormatFloat32x2, Offset: 12, ShaderLocation: 1},
			{Format: webgpu.VertexFormatUint32x4, Offset: 20, ShaderLocation: 2},
			{Format: webgpu.VertexFormatUint32, Offset: 36, ShaderLocation: 3},
		}}}},
		Primitive: webgpu.PrimitiveState{Topology: webgpu.PrimitiveTopologyTriangleList, CullMode: webgpu.CullModeNone},
		Fragment:  &webgpu.FragmentState{Module: mod, EntryPoint: entry, Targets: []webgpu.ColorTargetState{{Format: webgpu.TextureFormatR8Uint}}},
	})
	return s, err
}

// EnableSky uploads the map's sky faces and builds the sky pipeline; with
// [Renderer.DrawSky] set, [Renderer.DrawEntities] draws them after the lit
// faces and before the entities, at [Renderer.Time].
func (r *Renderer) EnableSky() error {
	geo, err := BuildSpecial(r.w, worldview.KindSky)
	if err != nil {
		return err
	}
	if r.sky, err = r.newSpecial(geo, "fsSky", "sky", SpecialShaderWGSL); err != nil {
		return err
	}
	r.DrawSky = true
	return nil
}

// EnableTurb uploads the map's water, slime, lava and teleporter faces
// ("*" textures) and builds the warp pipeline; with [Renderer.DrawTurb] set,
// [Renderer.DrawEntities] draws them after the sky, at [Renderer.Time].
func (r *Renderer) EnableTurb() error {
	geo, err := BuildSpecial(r.w, worldview.KindTurb)
	if err != nil {
		return err
	}
	if r.turb, err = r.newSpecial(geo, "fsTurb", "turb", TurbShaderWGSL()); err != nil {
		return err
	}
	r.DrawTurb = true
	return nil
}

// TurbFaces is the number of water faces on the GPU.
func (r *Renderer) TurbFaces() int {
	if r.turb == nil {
		return 0
	}
	return r.turb.geo.Faces
}

// SkyFaces is the number of sky faces on the GPU.
func (r *Renderer) SkyFaces() int {
	if r.sky == nil {
		return 0
	}
	return r.sky.geo.Faces
}

// recordSpecial draws the enabled sky and water faces of the view in the
// open pass (the world's Visible call has selected them).
func (r *Renderer) recordSpecial(p *webgpu.RenderPassRecorder) {
	if r.sky != nil && r.DrawSky {
		r.sky.record(r, p)
	}
	if r.turb != nil && r.DrawTurb {
		r.turb.record(r, p)
	}
}

func (s *specialGPU) record(r *Renderer, p *webgpu.RenderPassRecorder) {
	s.draws = s.geo.Draws(r.w, r.cur, s.draws)
	if len(s.draws) == 0 {
		return
	}
	p.SetPipeline(s.pipe)
	p.SetBindGroup(0, s.group)
	p.SetVertexBuffer(0, s.vb, 0, 0)
	p.SetIndexBuffer(s.ib, webgpu.IndexFormatUint32, 0, 0)
	for _, d := range s.draws {
		p.DrawIndexed(d.Count, 1, d.First, 0, 0)
	}
}
