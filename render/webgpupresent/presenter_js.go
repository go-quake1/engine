// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

//go:build js && wasm

package webgpupresent

import (
	"context"
	"errors"
	"fmt"
	"syscall/js"

	"github.com/go-gfx/webgpu"

	"github.com/go-quake1/engine/render"
)

// ErrSize is returned by Present for a frame whose size is not the one
// the presenter was built for.
var ErrSize = errors.New("webgpupresent: frame size differs from the presenter's")

// ErrFallbackAdapter is returned by New when only a fallback (software)
// adapter is available and Config.AllowFallback is false: the caller
// keeps the software presenter.
var ErrFallbackAdapter = errors.New("webgpupresent: only a fallback (software) adapter is available")

// Config describes the presenter.
type Config struct {
	// Width and Height are the frame's (the engine framebuffer's) size.
	Width, Height int
	// OutWidth and OutHeight are the canvas size in pixels; zero means
	// the frame size (no scaling).
	OutWidth, OutHeight int
	// Canvas is an OffscreenCanvas from transferControlToOffscreen: the
	// browser then presents it by itself. The zero value makes a
	// standalone surface, whose frames Present moves out as ImageBitmaps
	// (see Readback); that is the mode the correctness check uses.
	Canvas js.Value
	// AllowFallback accepts a fallback (software) adapter such as
	// SwiftShader. Without it New fails with an error that wraps
	// webgpu's no-adapter error or ErrFallbackAdapter.
	AllowFallback bool
	// ReplayURL is where the page serves webgpu.ReplayJS; empty loads the
	// embedded copy from a blob: URL.
	ReplayURL string
}

// Presenter implements [render.Presenter] on WebGPU.
type Presenter struct {
	cfg        Config
	outW, outH int
	dev        *webgpu.Device
	attach     *webgpu.RenderPassColorAttachment
	passDesc   webgpu.RenderPassDescriptor
	surf       *webgpu.Surface
	idx, pal   *webgpu.Texture
	pipe       *webgpu.RenderPipeline
	group      *webgpu.BindGroup
	rec        *webgpu.Recorder
	info       webgpu.AdapterInfo
	palCache   render.Palette
	palValid   bool
	last       js.Value // standalone mode: the latest frame
	frames     uint64
}

var _ render.Presenter = (*Presenter)(nil)

// New builds the device, surface, textures and pipeline. An error means
// the caller should keep the software presenter.
func New(ctx context.Context, cfg Config) (*Presenter, error) {
	if cfg.Width < 1 || cfg.Height < 1 {
		return nil, fmt.Errorf("webgpupresent: frame size %dx%d", cfg.Width, cfg.Height)
	}
	p := &Presenter{cfg: cfg, outW: cfg.OutWidth, outH: cfg.OutHeight}
	if p.outW < 1 {
		p.outW = cfg.Width
	}
	if p.outH < 1 {
		p.outH = cfg.Height
	}
	g, err := webgpu.Navigator()
	if err != nil {
		return nil, fmt.Errorf("webgpupresent: %w", err)
	}
	adapter, err := g.RequestAdapter(ctx, &webgpu.RequestAdapterOptions{AllowFallback: cfg.AllowFallback})
	if err != nil {
		return nil, fmt.Errorf("webgpupresent: %w", err)
	}
	p.info = adapter.Info()
	if p.info.Fallback && !cfg.AllowFallback {
		return nil, ErrFallbackAdapter
	}
	if err := webgpu.InstallReplay(ctx, cfg.ReplayURL); err != nil {
		return nil, fmt.Errorf("webgpupresent: %w", err)
	}
	q := &webgpu.Quota{MemoryBytes: 64 << 20, Pipelines: 2, ShaderBytes: 8 << 10, SurfaceWidth: 8192, SurfaceHeight: 8192}
	if p.dev, err = adapter.RequestDevice(ctx, &webgpu.DeviceDescriptor{Label: "quake present", Quota: q}); err != nil {
		return nil, fmt.Errorf("webgpupresent: %w", err)
	}
	if err := p.build(g); err != nil {
		p.dev.Destroy()
		return nil, fmt.Errorf("webgpupresent: %w", err)
	}
	return p, nil
}

func (p *Presenter) build(g *webgpu.GPU) error {
	d := p.dev
	var err error
	sd := webgpu.SurfaceDescriptor{Width: p.outW, Height: p.outH}
	if p.cfg.Canvas.Truthy() {
		p.cfg.Canvas.Set("width", p.outW)
		p.cfg.Canvas.Set("height", p.outH)
		p.surf, err = d.NewSurfaceFromCanvas(p.cfg.Canvas, sd)
	} else {
		p.surf, err = d.NewSurface(sd)
	}
	if err != nil {
		return err
	}
	if p.idx, err = d.CreateTexture(webgpu.TextureDescriptor{Label: "index", Size: webgpu.Extent3D{Width: uint32(p.cfg.Width), Height: uint32(p.cfg.Height)},
		Format: webgpu.TextureFormatR8Uint, Usage: webgpu.TextureUsageTextureBinding | webgpu.TextureUsageCopyDst}); err != nil {
		return err
	}
	if p.pal, err = d.CreateTexture(webgpu.TextureDescriptor{Label: "palette", Size: webgpu.Extent3D{Width: 256, Height: 1},
		Format: webgpu.TextureFormatRGBA8Unorm, Usage: webgpu.TextureUsageTextureBinding | webgpu.TextureUsageCopyDst}); err != nil {
		return err
	}
	bgl, err := d.CreateBindGroupLayout(webgpu.BindGroupLayoutDescriptor{Entries: []webgpu.BindGroupLayoutEntry{
		{Binding: 0, Visibility: webgpu.ShaderStageFragment, Texture: &webgpu.TextureBindingLayout{SampleType: webgpu.TextureSampleTypeUint}},
		{Binding: 1, Visibility: webgpu.ShaderStageFragment, Texture: &webgpu.TextureBindingLayout{SampleType: webgpu.TextureSampleTypeFloat}},
	}})
	if err != nil {
		return err
	}
	pl, err := d.CreatePipelineLayout(webgpu.PipelineLayoutDescriptor{BindGroupLayouts: []*webgpu.BindGroupLayout{bgl}})
	if err != nil {
		return err
	}
	iv, err := p.idx.CreateView(nil)
	if err != nil {
		return err
	}
	pv, err := p.pal.CreateView(nil)
	if err != nil {
		return err
	}
	if p.group, err = d.CreateBindGroup(webgpu.BindGroupDescriptor{Layout: bgl, Entries: []webgpu.BindGroupEntry{
		{Binding: 0, TextureView: iv}, {Binding: 1, TextureView: pv},
	}}); err != nil {
		return err
	}
	mod, err := d.CreateShaderModule(webgpu.ShaderModuleDescriptor{Label: "present", Code: ShaderWGSL})
	if err != nil {
		return err
	}
	if p.pipe, err = d.CreateRenderPipeline(webgpu.RenderPipelineDescriptor{
		Layout:    pl,
		Vertex:    webgpu.VertexState{Module: mod, EntryPoint: "vs"},
		Primitive: webgpu.PrimitiveState{Topology: webgpu.PrimitiveTopologyTriangleList, CullMode: webgpu.CullModeNone},
		Fragment:  &webgpu.FragmentState{Module: mod, EntryPoint: "fs", Targets: []webgpu.ColorTargetState{{Format: p.surf.Format()}}},
	}); err != nil {
		return err
	}
	p.rec = d.NewRecorder()
	// One pass descriptor for every frame: only the view changes.
	p.attach = &webgpu.RenderPassColorAttachment{LoadOp: webgpu.LoadOpClear, StoreOp: webgpu.StoreOpStore}
	p.passDesc = webgpu.RenderPassDescriptor{ColorAttachments: []*webgpu.RenderPassColorAttachment{p.attach}}
	return nil
}

// Adapter describes the adapter in use: Fallback is true for a software
// adapter (SwiftShader), which is never a GPU measurement.
func (p *Presenter) Adapter() webgpu.AdapterInfo { return p.info }

// Frames is the number of frames presented.
func (p *Presenter) Frames() uint64 { return p.frames }

// Present uploads the frame (and the palette when it changed) and draws
// it: the texture writes and the pass are one Recorder submit (one JS call).
func (p *Presenter) Present(pixels []byte, width, height, pitch int, pal *render.Palette) error {
	if width != p.cfg.Width || height != p.cfg.Height {
		return ErrSize
	}
	p.rec.Reset()
	if !p.palValid || *pal != p.palCache {
		p.rec.WriteTexture(webgpu.TexelCopyTextureInfo{Texture: p.pal}, PaletteRGBA(pal),
			webgpu.TexelCopyBufferLayout{}, webgpu.Extent3D{Width: 256, Height: 1})
		p.palCache, p.palValid = *pal, true
	}
	p.rec.WriteTexture(webgpu.TexelCopyTextureInfo{Texture: p.idx}, pixels,
		webgpu.TexelCopyBufferLayout{BytesPerRow: uint32(pitch)}, webgpu.Extent3D{Width: uint32(width), Height: uint32(height)})
	view, err := p.surf.CurrentView()
	if err != nil {
		return err
	}
	p.attach.View = view
	pass := p.rec.BeginRenderPass(p.passDesc)
	pass.SetPipeline(p.pipe)
	pass.SetBindGroup(0, p.group)
	pass.Draw(3, 1, 0, 0)
	if err := pass.End(); err != nil {
		return err
	}
	if _, err := p.rec.Submit(); err != nil {
		return err
	}
	bm, err := p.surf.Present()
	if err != nil {
		return err
	}
	if bm.Truthy() { // standalone surface: keep the latest frame
		if p.last.Truthy() {
			p.last.Call("close")
		}
		p.last = bm
	}
	p.frames++
	return nil
}

// Wait blocks until the GPU has finished every submitted frame.
func (p *Presenter) Wait(ctx context.Context) error {
	return p.dev.WaitSubmitted(ctx, p.dev.Submitted())
}

// Readback returns the latest frame of a standalone presenter as RGBA8
// rows (outW x outH), read the way a page showing it would see it: the
// ImageBitmap drawn on a 2D canvas.
func (p *Presenter) Readback() (rgba []byte, w, h int, err error) {
	if !p.last.Truthy() {
		return nil, 0, 0, errors.New("webgpupresent: no frame to read back (standalone presenter only)")
	}
	c := js.Global().Get("OffscreenCanvas").New(p.outW, p.outH)
	g := c.Call("getContext", "2d")
	g.Call("drawImage", p.last, 0, 0)
	data := g.Call("getImageData", 0, 0, p.outW, p.outH).Get("data")
	rgba = make([]byte, 4*p.outW*p.outH)
	js.CopyBytesToGo(rgba, data)
	return rgba, p.outW, p.outH, nil
}

// Close releases the device.
func (p *Presenter) Close() {
	if p.last.Truthy() {
		p.last.Call("close")
	}
	p.surf.Destroy()
	p.dev.Destroy()
}
