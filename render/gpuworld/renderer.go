// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

//go:build (js && wasm) || wasip1

package gpuworld

import (
	"context"
	"fmt"
	"time"

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
	readBuf  *webgpu.Buffer // DrawRead's MAP_READ buffer

	// The entity layer ([Renderer.EnableAliasLayer]): the alias entities go in a
	// pass of their own into a second index texture, cleared to EntityClear, so
	// that the caller composites them over what it draws after the world.
	kept      []any // see hold
	layered   bool
	entTarget *webgpu.Texture
	entPipe   *webgpu.RenderPipeline
	entPass   webgpu.RenderPassDescriptor
	entRead   *webgpu.Buffer

	surfTex, surfLowTex *webgpu.Texture // the surface atlases, rewritten where a surface is re-baked
	// NoAnimate leaves the surfaces as baked (the planted defect of the
	// animation check): a frame does not upload the re-baked rectangles.
	NoAnimate bool
	// IgnoreDLights draws the surfaces without the world's dynamic lights (the
	// planted defect of the dynamic light check).
	IgnoreDLights bool
	// NoAliasClip draws the alias triangles without r_aclip.c's clipping: the
	// ones that cross the near plane or a side of the frame are dropped, as
	// before it was ported (the planted defect of the clipping check).
	NoAliasClip bool
	// Uploaded and UploadedBytes are the surface rectangles and bytes the
	// last frame wrote into the atlases.
	Uploaded, UploadedBytes int
	// Writes is the number of WriteTexture commands those took: the surfaces
	// of a shelf near each other go in one ([Geometry.Coalesce]); NoCoalesce
	// writes each surface alone, as before v0.22.0, and CoalesceSlack (0:
	// [DefaultCoalesceSlack]) is the unchanged bytes a write may carry to save
	// another.
	Writes        int
	NoCoalesce    bool
	CoalesceSlack int
	// PlantCoalesceDefect zeroes the texels of the merged writes (the planted
	// defect of the coalescing check).
	PlantCoalesceDefect bool
	writes              []Write
	stage               []byte
	// UploadTime is the time the last frame spent recording those uploads, and
	// the geometry's BakeTime the time it spent baking them.
	UploadTime time.Duration

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
	surfLowA, err := tex2d("world surfaces, levels 1 to 3", geo.SurfLow.Width, geo.SurfLow.Height, webgpu.TextureUsageTextureBinding|webgpu.TextureUsageCopyDst)
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
	r.surfTex, r.surfLowTex = surfA, surfLowA
	if err := r.upload(surfA, geo.Surf); err != nil {
		return nil, err
	}
	if err := r.upload(surfLowA, geo.SurfLow); err != nil {
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
		{Binding: 2, Visibility: webgpu.ShaderStageFragment, Texture: uint2d},
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
	views := make([]*webgpu.TextureView, 3)
	for i, t := range []*webgpu.Texture{surfA, cmT, surfLowA} {
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
		{Binding: 0, Buffer: r.ub}, {Binding: 1, TextureView: views[0]}, {Binding: 2, TextureView: views[2]},
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
	if r.readBuf, err = r.readBuffer("world readback"); err != nil {
		return nil, err
	}
	r.hold(surfA, surfLowA, cmT, bgl, pl, views, faceTab, faceVerts, surfTab, mod, tv)
	return r, nil
}

// hold keeps GPU handles reachable for as long as the Renderer. A remote device
// gives each handle an id when it is created and frees the id when the Go
// object is garbage collected, WITHOUT destroying the object on the host: a
// handle that is only a local (a shader module, a layout, a buffer that only a
// bind group names) is collected sooner or later, its id goes to the next object
// created, and the host refuses that creation ("CREATE 12": the id is still in
// use) and loses the device. It showed when the first frame with entities created
// the read-back buffer of their layer after a collection, on a lane slower than
// the machine it was written on.
func (r *Renderer) hold(handles ...any) { r.kept = append(r.kept, handles...) }

// readBuffer makes a MAP_READ buffer of one padded frame.
func (r *Renderer) readBuffer(label string) (*webgpu.Buffer, error) {
	return r.dev.CreateBuffer(webgpu.BufferDescriptor{Label: label, Size: uint64(Pitch(r.width) * r.height),
		Usage: webgpu.BufferUsageMapRead | webgpu.BufferUsageCopyDst})
}

// createReadBuffers makes the read-back buffers of the world frame and of the
// entity layer now, not at the first frame that reads them (see hold).
func (r *Renderer) createReadBuffers() (err error) {
	if r.readBuf == nil {
		if r.readBuf, err = r.readBuffer("world readback"); err != nil {
			return err
		}
	}
	if r.entRead == nil {
		if r.entRead, err = r.readBuffer("entity layer readback"); err != nil {
			return err
		}
	}
	return nil
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
	if err := r.record(v, ents); err != nil {
		return err
	}
	_, err := r.rec.Submit()
	return err
}

// record fills the Recorder with one frame (the uniform write and the pass)
// without submitting it.
func (r *Renderer) record(v worldview.View, ents []worldview.AliasEnt) error {
	var err error
	r.cur = v
	r.geo.IgnoreDLights = r.IgnoreDLights
	if r.draws, err = r.geo.Draws(r.w, v, r.width, r.height, r.draws, r.Mip); err != nil {
		return err
	}
	r.rec.Reset()
	r.recordUpdates()
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
	if !r.layered {
		r.recordAlias(p)
	}
	if err := p.End(); err != nil {
		return err
	}
	if r.layered {
		return r.recordAliasLayer()
	}
	return nil
}

// BakeTime is the time the last frame spent baking the surfaces it re-baked
// (render.DrawSurface), apart from recording their upload ([Renderer.UploadTime]).
func (r *Renderer) BakeTime() time.Duration { return r.geo.BakeTime }

// recordUpdates writes the surfaces the frame's Draws re-baked (light styles
// and '+' textures that moved) into the atlases, one WriteTexture each, before
// the pass that reads them.
func (r *Renderer) recordUpdates() {
	r.Uploaded, r.UploadedBytes, r.Writes, r.UploadTime = 0, 0, 0, 0
	if r.NoAnimate {
		return
	}
	t0 := time.Now()
	defer func() { r.UploadTime = time.Since(t0) }()
	r.Uploaded = len(r.geo.Updates)
	r.writes = r.writes[:0]
	if r.NoCoalesce {
		for _, u := range r.geo.Updates {
			r.writes = append(r.writes, Write{Low: u.Low, R: u.R, Pix: u.Pix})
		}
	} else {
		slack := r.CoalesceSlack
		if slack == 0 {
			slack = DefaultCoalesceSlack
		}
		r.geo.PlantCoalesceDefect = r.PlantCoalesceDefect
		r.writes, r.stage = r.geo.Coalesce(r.geo.Updates, slack, r.writes, r.stage)
	}
	for _, u := range r.writes {
		tex := r.surfTex
		if u.Low {
			tex = r.surfLowTex
		}
		r.rec.WriteTexture(webgpu.TexelCopyTextureInfo{Texture: tex, Origin: webgpu.Origin3D{X: u.R.X, Y: u.R.Y}},
			u.Pix, webgpu.TexelCopyBufferLayout{BytesPerRow: u.R.W, RowsPerImage: u.R.H},
			webgpu.Extent3D{Width: u.R.W, Height: u.R.H})
		r.Writes++
		r.UploadedBytes += len(u.Pix)
	}
}

// DrawRead draws one frame of the world from v and reads the index frame back
// in the same submit: the pass, the texture-to-buffer copy (rows padded to
// 256 bytes) and the read, one host crossing. The frame must fit the host's read
// window ([ReadWindow]: 640x480 does). It returns the width*height indices.
func (r *Renderer) DrawRead(v worldview.View) ([]byte, error) {
	pitch := Pitch(r.width)
	if pitch*r.height > ReadWindow {
		return nil, fmt.Errorf("gpuworld: a %dx%d frame (%d padded bytes) does not fit the %d-byte read window of one submit", r.width, r.height, pitch*r.height, ReadWindow)
	}
	if r.readBuf == nil {
		b, err := r.dev.CreateBuffer(webgpu.BufferDescriptor{Label: "world readback", Size: uint64(pitch * r.height),
			Usage: webgpu.BufferUsageMapRead | webgpu.BufferUsageCopyDst})
		if err != nil {
			return nil, err
		}
		r.readBuf = b
	}
	if err := r.record(v, nil); err != nil {
		return nil, err
	}
	r.rec.CopyTextureToBuffer(webgpu.TexelCopyTextureInfo{Texture: r.target}, r.readBuf,
		webgpu.TexelCopyBufferLayout{BytesPerRow: uint32(pitch), RowsPerImage: uint32(r.height)},
		webgpu.Extent3D{Width: uint32(r.width), Height: uint32(r.height)})
	rb := r.rec.ReadBuffer(r.readBuf, 0, uint64(pitch*r.height))
	if _, err := r.rec.Submit(); err != nil {
		return nil, err
	}
	b, err := rb.Bytes()
	if err != nil {
		return nil, err
	}
	return Depad(make([]byte, 0, r.width*r.height), b, r.width, pitch, r.height), nil
}

// DrawReadLayers is DrawRead with the alias entities ents in a layer of their
// own ([Renderer.EnableAliasLayer]): the world's index frame and the entity
// layer (EntityClear where nothing was drawn) come back from one submit, so
// that the caller can draw what it keeps on the CPU (the sky and water) between
// them. Both must fit the host's read window together.
func (r *Renderer) DrawReadLayers(v worldview.View, ents []worldview.AliasEnt) (world, layer []byte, err error) {
	if !r.layered {
		return nil, nil, fmt.Errorf("gpuworld: DrawReadLayers without EnableAliasLayer")
	}
	pitch := Pitch(r.width)
	if 2*pitch*r.height > ReadWindow {
		return nil, nil, fmt.Errorf("gpuworld: a %dx%d frame and its entity layer (%d padded bytes) do not fit the %d-byte read window of one submit", r.width, r.height, 2*pitch*r.height, ReadWindow)
	}
	if err := r.createReadBuffers(); err != nil { // made at EnableAliasLayer already
		return nil, nil, err
	}
	if err := r.record(v, ents); err != nil {
		return nil, nil, err
	}
	layout := webgpu.TexelCopyBufferLayout{BytesPerRow: uint32(pitch), RowsPerImage: uint32(r.height)}
	size := webgpu.Extent3D{Width: uint32(r.width), Height: uint32(r.height)}
	r.rec.CopyTextureToBuffer(webgpu.TexelCopyTextureInfo{Texture: r.target}, r.readBuf, layout, size)
	r.rec.CopyTextureToBuffer(webgpu.TexelCopyTextureInfo{Texture: r.entTarget}, r.entRead, layout, size)
	rbW := r.rec.ReadBuffer(r.readBuf, 0, uint64(pitch*r.height))
	rbE := r.rec.ReadBuffer(r.entRead, 0, uint64(pitch*r.height))
	if _, err := r.rec.Submit(); err != nil {
		return nil, nil, err
	}
	bw, err := rbW.Bytes()
	if err != nil {
		return nil, nil, err
	}
	be, err := rbE.Bytes()
	if err != nil {
		return nil, nil, err
	}
	return Depad(make([]byte, 0, r.width*r.height), bw, r.width, pitch, r.height),
		Depad(make([]byte, 0, r.width*r.height), be, r.width, pitch, r.height), nil
}

// Wait blocks until the GPU has finished every frame submitted.
func (r *Renderer) Wait(ctx context.Context) error {
	return r.dev.WaitSubmitted(ctx, r.dev.Submitted())
}
