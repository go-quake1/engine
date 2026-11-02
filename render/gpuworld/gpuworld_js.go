// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

//go:build js && wasm

package gpuworld

import (
	"context"

	"github.com/go-gfx/webgpu"

	"github.com/go-quake1/engine/render"
	"github.com/go-quake1/engine/worldview"
)

// Renderer draws a world into an r8uint texture of palette indices.
type Renderer struct {
	dev      *webgpu.Device
	w        *worldview.World
	geo      *Geometry
	width    int
	height   int
	target   *webgpu.Texture
	pipe     *webgpu.RenderPipeline
	group    *webgpu.BindGroup
	ub, vb   *webgpu.Buffer
	ib       *webgpu.Buffer
	rec      *webgpu.Recorder
	attach   *webgpu.RenderPassColorAttachment
	passDesc webgpu.RenderPassDescriptor
	draws    []Draw
	clear    float64
}

func (r *Renderer) upload(tex *webgpu.Texture, a *Atlas) error {
	return r.dev.Queue().WriteTexture(webgpu.TexelCopyTextureInfo{Texture: tex}, a.Pixels,
		webgpu.TexelCopyBufferLayout{BytesPerRow: uint32(a.Width)}, webgpu.Extent3D{Width: uint32(a.Width), Height: uint32(a.Height)})
}

// New builds the GPU content of w (atlases, colormap, geometry) on dev
// and a width x height index target.
func New(dev *webgpu.Device, w *worldview.World, cm *render.ColorMap, width, height int) (*Renderer, error) {
	geo, err := Build(w)
	if err != nil {
		return nil, err
	}
	r := &Renderer{dev: dev, w: w, geo: geo, width: width, height: height, clear: worldview.ClearIndex}
	tex2d := func(label string, tw, th int, usage webgpu.TextureUsage) (*webgpu.Texture, error) {
		return dev.CreateTexture(webgpu.TextureDescriptor{Label: label, Size: webgpu.Extent3D{Width: uint32(tw), Height: uint32(th)},
			Format: webgpu.TextureFormatR8Uint, Usage: usage})
	}
	texA, err := tex2d("world textures", geo.Tex.Width, geo.Tex.Height, webgpu.TextureUsageTextureBinding|webgpu.TextureUsageCopyDst)
	if err != nil {
		return nil, err
	}
	lmA, err := tex2d("world lightmaps", geo.Lm.Width, geo.Lm.Height, webgpu.TextureUsageTextureBinding|webgpu.TextureUsageCopyDst)
	if err != nil {
		return nil, err
	}
	cmT, err := tex2d("colormap", render.ColorMapCols, render.ColorMapRows, webgpu.TextureUsageTextureBinding|webgpu.TextureUsageCopyDst)
	if err != nil {
		return nil, err
	}
	if r.target, err = tex2d("world indices", width, height,
		webgpu.TextureUsageRenderAttachment|webgpu.TextureUsageTextureBinding|webgpu.TextureUsageCopySrc); err != nil {
		return nil, err
	}
	if err := r.upload(texA, geo.Tex); err != nil {
		return nil, err
	}
	if err := r.upload(lmA, geo.Lm); err != nil {
		return nil, err
	}
	var cmb [render.ColorMapRows * render.ColorMapCols]byte
	for row := range cm {
		copy(cmb[row*render.ColorMapCols:], cm[row][:])
	}
	if err := dev.Queue().WriteTexture(webgpu.TexelCopyTextureInfo{Texture: cmT}, cmb[:],
		webgpu.TexelCopyBufferLayout{BytesPerRow: render.ColorMapCols}, webgpu.Extent3D{Width: render.ColorMapCols, Height: render.ColorMapRows}); err != nil {
		return nil, err
	}

	buf := func(label string, size int, usage webgpu.BufferUsage) (*webgpu.Buffer, error) {
		return dev.CreateBuffer(webgpu.BufferDescriptor{Label: label, Size: uint64((size + 3) &^ 3), Usage: usage | webgpu.BufferUsageCopyDst})
	}
	if r.ub, err = buf("frame uniform", UniformSize, webgpu.BufferUsageUniform); err != nil {
		return nil, err
	}
	if r.vb, err = buf("world vertices", len(geo.Vertices), webgpu.BufferUsageVertex); err != nil {
		return nil, err
	}
	if r.ib, err = buf("world indices", 4*len(geo.Indices), webgpu.BufferUsageIndex); err != nil {
		return nil, err
	}
	if err := dev.Queue().WriteBuffer(r.vb, 0, geo.Vertices); err != nil {
		return nil, err
	}
	ib := make([]byte, 4*len(geo.Indices))
	for i, v := range geo.Indices {
		ib[4*i], ib[4*i+1], ib[4*i+2], ib[4*i+3] = byte(v), byte(v>>8), byte(v>>16), byte(v>>24)
	}
	if err := dev.Queue().WriteBuffer(r.ib, 0, ib); err != nil {
		return nil, err
	}

	uint2d := &webgpu.TextureBindingLayout{SampleType: webgpu.TextureSampleTypeUint}
	bgl, err := dev.CreateBindGroupLayout(webgpu.BindGroupLayoutDescriptor{Entries: []webgpu.BindGroupLayoutEntry{
		{Binding: 0, Visibility: webgpu.ShaderStageVertex, Buffer: &webgpu.BufferBindingLayout{Type: webgpu.BufferBindingTypeUniform}},
		{Binding: 1, Visibility: webgpu.ShaderStageFragment, Texture: uint2d},
		{Binding: 2, Visibility: webgpu.ShaderStageFragment, Texture: uint2d},
		{Binding: 3, Visibility: webgpu.ShaderStageFragment, Texture: uint2d},
	}})
	if err != nil {
		return nil, err
	}
	pl, err := dev.CreatePipelineLayout(webgpu.PipelineLayoutDescriptor{BindGroupLayouts: []*webgpu.BindGroupLayout{bgl}})
	if err != nil {
		return nil, err
	}
	views := make([]*webgpu.TextureView, 3)
	for i, t := range []*webgpu.Texture{texA, lmA, cmT} {
		if views[i], err = t.CreateView(nil); err != nil {
			return nil, err
		}
	}
	if r.group, err = dev.CreateBindGroup(webgpu.BindGroupDescriptor{Layout: bgl, Entries: []webgpu.BindGroupEntry{
		{Binding: 0, Buffer: r.ub}, {Binding: 1, TextureView: views[0]}, {Binding: 2, TextureView: views[1]}, {Binding: 3, TextureView: views[2]},
	}}); err != nil {
		return nil, err
	}
	mod, err := dev.CreateShaderModule(webgpu.ShaderModuleDescriptor{Label: "world", Code: ShaderWGSL})
	if err != nil {
		return nil, err
	}
	if r.pipe, err = dev.CreateRenderPipeline(webgpu.RenderPipelineDescriptor{
		Layout: pl,
		Vertex: webgpu.VertexState{Module: mod, EntryPoint: "vs", Buffers: []webgpu.VertexBufferLayout{{ArrayStride: VertexStride, Attributes: []webgpu.VertexAttribute{
			{Format: webgpu.VertexFormatFloat32x3, Offset: 0, ShaderLocation: 0},
			{Format: webgpu.VertexFormatFloat32x2, Offset: 12, ShaderLocation: 1},
			{Format: webgpu.VertexFormatFloat32x2, Offset: 20, ShaderLocation: 2},
			{Format: webgpu.VertexFormatUint32x4, Offset: 28, ShaderLocation: 3},
			{Format: webgpu.VertexFormatUint32x4, Offset: 44, ShaderLocation: 4},
		}}}},
		Primitive: webgpu.PrimitiveState{Topology: webgpu.PrimitiveTopologyTriangleList, CullMode: webgpu.CullModeNone},
		Fragment:  &webgpu.FragmentState{Module: mod, EntryPoint: "fs", Targets: []webgpu.ColorTargetState{{Format: webgpu.TextureFormatR8Uint}}},
	}); err != nil {
		return nil, err
	}
	tv, err := r.target.CreateView(nil)
	if err != nil {
		return nil, err
	}
	r.attach = &webgpu.RenderPassColorAttachment{View: tv, LoadOp: webgpu.LoadOpClear, StoreOp: webgpu.StoreOpStore,
		ClearValue: webgpu.Color{R: r.clear}}
	r.passDesc = webgpu.RenderPassDescriptor{ColorAttachments: []*webgpu.RenderPassColorAttachment{r.attach}}
	r.rec = dev.NewRecorder()
	return r, nil
}

// Target is the index texture (r8uint) the world is drawn into: a
// presenter can bind it as its index source.
func (r *Renderer) Target() *webgpu.Texture { return r.target }

// Faces is the number of faces on the GPU.
func (r *Renderer) Faces() int { return r.geo.Faces }

// Draw records and submits one frame of the world from v: the uniform
// write and one pass, in one Recorder submit.
func (r *Renderer) Draw(v worldview.View) error {
	var err error
	if r.draws, err = r.geo.Draws(r.w, v, r.width, r.height, r.draws); err != nil {
		return err
	}
	r.rec.Reset()
	r.rec.WriteBuffer(r.ub, 0, Uniform(v, r.w.FOV, r.width, r.height))
	p := r.rec.BeginRenderPass(r.passDesc)
	p.SetPipeline(r.pipe)
	p.SetBindGroup(0, r.group)
	p.SetVertexBuffer(0, r.vb, 0, 0)
	p.SetIndexBuffer(r.ib, webgpu.IndexFormatUint32, 0, 0)
	for _, d := range r.draws {
		p.DrawIndexed(d.Count, 1, d.First, 0, 0)
	}
	if err := p.End(); err != nil {
		return err
	}
	_, err = r.rec.Submit()
	return err
}

// Wait blocks until the GPU has finished every frame submitted.
func (r *Renderer) Wait(ctx context.Context) error {
	return r.dev.WaitSubmitted(ctx, r.dev.Submitted())
}

// Readback returns the target's width*height indices (for tests).
func (r *Renderer) Readback(ctx context.Context) ([]byte, error) {
	return r.dev.ReadTexture(ctx, webgpu.TexelCopyTextureInfo{Texture: r.target}, r.width, r.height, 1)
}
