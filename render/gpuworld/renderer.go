// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

//go:build (js && wasm) || wasip1

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
	draws    []FaceDraw
	clear    float64
	cmView   *webgpu.TextureView
	alias    *aliasGPU
	sky      *specialGPU
	cur      worldview.View

	// Mip is how a frame picks the mip level of a face; the zero value of the
	// field in a Renderer made by [New] is Quake's ([Chosen]).
	Mip MipPolicy

	// SkipWorld leaves the lit faces out of the pass (the target is still
	// cleared): to time what is drawn after them on its own.
	SkipWorld bool
	// Time is the time in seconds the sky scroll is evaluated at.
	Time float32
	// DrawSky switches the sky pass (see [Renderer.EnableSky]) on or off.
	DrawSky bool
	// DrawTurb switches the water pass (see [Renderer.EnableTurb]) on or off.
	DrawTurb bool
	turb     *specialGPU
}

func (r *Renderer) upload(tex *webgpu.Texture, a *Atlas) error {
	return uploadTexture(r.dev, tex, a.Pixels, a.Width, a.Height)
}

// New builds the GPU content of w (atlases, colormap, geometry) on dev
// and a width x height index target.
func New(dev *webgpu.Device, w *worldview.World, cm *render.ColorMap, width, height int) (*Renderer, error) {
	geo, err := Build(w, cm)
	if err != nil {
		return nil, err
	}
	r := &Renderer{dev: dev, w: w, geo: geo, width: width, height: height, clear: worldview.ClearIndex, Mip: Chosen}
	tex2d := func(label string, tw, th int, usage webgpu.TextureUsage) (*webgpu.Texture, error) {
		return dev.CreateTexture(webgpu.TextureDescriptor{Label: label, Size: webgpu.Extent3D{Width: uint32(tw), Height: uint32(th)},
			Format: webgpu.TextureFormatR8Uint, Usage: usage})
	}
	surfA, err := tex2d("world surfaces", geo.Surf.Width, geo.Surf.Height, webgpu.TextureUsageTextureBinding|webgpu.TextureUsageCopyDst)
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
	if err := r.upload(surfA, geo.Surf); err != nil {
		return nil, err
	}
	var cmb [render.ColorMapRows * render.ColorMapCols]byte
	for row := range cm {
		copy(cmb[row*render.ColorMapCols:], cm[row][:])
	}
	if err := uploadTexture(dev, cmT, cmb[:], render.ColorMapCols, render.ColorMapRows); err != nil {
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
	if err := writeBuffer(dev, r.vb, geo.Vertices); err != nil {
		return nil, err
	}
	ib := make([]byte, 4*len(geo.Indices))
	for i, v := range geo.Indices {
		ib[4*i], ib[4*i+1], ib[4*i+2], ib[4*i+3] = byte(v), byte(v>>8), byte(v>>16), byte(v>>24)
	}
	if err := writeBuffer(dev, r.ib, ib); err != nil {
		return nil, err
	}

	uint2d := &webgpu.TextureBindingLayout{SampleType: webgpu.TextureSampleTypeUint}
	bgl, err := dev.CreateBindGroupLayout(webgpu.BindGroupLayoutDescriptor{Entries: []webgpu.BindGroupLayoutEntry{
		{Binding: 0, Visibility: webgpu.ShaderStageVertex | webgpu.ShaderStageFragment, Buffer: &webgpu.BufferBindingLayout{Type: webgpu.BufferBindingTypeUniform}},
		{Binding: 1, Visibility: webgpu.ShaderStageFragment, Texture: uint2d},
		{Binding: 4, Visibility: webgpu.ShaderStageFragment, Buffer: &webgpu.BufferBindingLayout{Type: webgpu.BufferBindingTypeReadOnlyStorage}},
		{Binding: 5, Visibility: webgpu.ShaderStageFragment, Buffer: &webgpu.BufferBindingLayout{Type: webgpu.BufferBindingTypeReadOnlyStorage}},
		{Binding: 6, Visibility: webgpu.ShaderStageFragment, Buffer: &webgpu.BufferBindingLayout{Type: webgpu.BufferBindingTypeReadOnlyStorage}},
	}})
	if err != nil {
		return nil, err
	}
	pl, err := dev.CreatePipelineLayout(webgpu.PipelineLayoutDescriptor{BindGroupLayouts: []*webgpu.BindGroupLayout{bgl}})
	if err != nil {
		return nil, err
	}
	views := make([]*webgpu.TextureView, 2)
	for i, t := range []*webgpu.Texture{surfA, cmT} {
		if views[i], err = t.CreateView(nil); err != nil {
			return nil, err
		}
	}
	r.cmView = views[1]
	storage := func(label string, data []byte) (*webgpu.Buffer, error) {
		if len(data) == 0 {
			data = make([]byte, 16)
		}
		b, err := dev.CreateBuffer(webgpu.BufferDescriptor{Label: label, Size: uint64((len(data) + 3) &^ 3), Usage: webgpu.BufferUsageStorage | webgpu.BufferUsageCopyDst})
		if err != nil {
			return nil, err
		}
		return b, writeBuffer(dev, b, data)
	}
	faceTab, err := storage("world faces", geo.FaceTab)
	if err != nil {
		return nil, err
	}
	faceVerts, err := storage("world face vertices", geo.FaceVerts)
	if err != nil {
		return nil, err
	}
	surfTab, err := storage("world surface rects", geo.SurfTab)
	if err != nil {
		return nil, err
	}
	if r.group, err = dev.CreateBindGroup(webgpu.BindGroupDescriptor{Layout: bgl, Entries: []webgpu.BindGroupEntry{
		{Binding: 0, Buffer: r.ub}, {Binding: 1, TextureView: views[0]},
		{Binding: 4, Buffer: faceTab}, {Binding: 5, Buffer: faceVerts}, {Binding: 6, Buffer: surfTab},
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
			{Format: webgpu.VertexFormatUint32, Offset: 12, ShaderLocation: 1},
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
func (r *Renderer) Draw(v worldview.View) error { return r.DrawEntities(v, nil) }

// DrawEntities is Draw followed, in the same pass, by the alias entities
// ents (see [Renderer.EnableAlias]); with no entities it is Draw.
func (r *Renderer) DrawEntities(v worldview.View, ents []worldview.AliasEnt) error {
	var err error
	r.cur = v
	if r.draws, err = r.geo.Draws(r.w, v, r.width, r.height, r.draws, r.Mip); err != nil {
		return err
	}
	r.rec.Reset()
	r.rec.WriteBuffer(r.ub, 0, UniformAt(v, r.w.FOV, r.width, r.height, r.Time))
	if err := r.prepareAlias(ents); err != nil {
		return err
	}
	p := r.rec.BeginRenderPass(r.passDesc)
	p.SetPipeline(r.pipe)
	p.SetBindGroup(0, r.group)
	p.SetVertexBuffer(0, r.vb, 0, 0)
	p.SetIndexBuffer(r.ib, webgpu.IndexFormatUint32, 0, 0)
	if !r.SkipWorld {
		for _, d := range r.draws {
			p.DrawIndexed(d.Count, 1, d.First, 0, d.Mip)
		}
	}
	r.recordSpecial(p)
	r.recordAlias(p)
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
