// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

//go:build (js && wasm) || wasip1

package gpuworld

import (
	"fmt"
	"strings"

	"github.com/go-gfx/webgpu"

	"github.com/go-quake1/engine/render"
	"github.com/go-quake1/engine/worldview"
)

// MaxAliasEntities bounds the entities of one frame.
const MaxAliasEntities = 512

type aliasGPU struct {
	set   *AliasSet
	pipe  *webgpu.RenderPipeline
	group *webgpu.BindGroup
	ents  *webgpu.Buffer
	data  []byte
	draws []AliasDraw
	pl    *webgpu.PipelineLayout
}

// aliasEntityShader is the alias shader for the entity layer: a texel of 255
// would read as EntityClear, so the layer's shader writes 254 for it.
func aliasEntityShader() string {
	const ret = "return vec4u(textureLoad(cmap, vec2u(texel, row), 0).r, 0u, 0u, 0u);"
	return strings.Replace(AliasShaderWGSL, ret, "return vec4u(min(textureLoad(cmap, vec2u(texel, row), 0).r, 254u), 0u, 0u, 0u);", 1)
}

// EnableAliasLayer (after [Renderer.EnableAlias]) gives the entities a pass of
// their own, into a second index texture cleared to [EntityClear]: the world's
// pass then has none, and [Renderer.DrawReadLayers] returns both.
func (r *Renderer) EnableAliasLayer() error {
	a := r.alias
	if a == nil {
		return fmt.Errorf("gpuworld: EnableAliasLayer before EnableAlias")
	}
	dev := r.dev
	var err error
	if r.entTarget, err = dev.CreateTexture(webgpu.TextureDescriptor{Label: "entity indices", Size: webgpu.Extent3D{Width: uint32(r.width), Height: uint32(r.height)},
		Format: webgpu.TextureFormatR8Uint, Usage: webgpu.TextureUsageRenderAttachment | webgpu.TextureUsageTextureBinding | webgpu.TextureUsageCopySrc}); err != nil {
		return err
	}
	tv, err := r.entTarget.CreateView(nil)
	if err != nil {
		return err
	}
	r.entPass = webgpu.RenderPassDescriptor{ColorAttachments: []*webgpu.RenderPassColorAttachment{{View: tv, LoadOp: webgpu.LoadOpClear,
		StoreOp: webgpu.StoreOpStore, ClearValue: webgpu.Color{R: EntityClear}}}}
	mod, err := dev.CreateShaderModule(webgpu.ShaderModuleDescriptor{Label: "alias layer", Code: aliasEntityShader()})
	if err != nil {
		return err
	}
	if r.entPipe, err = dev.CreateRenderPipeline(webgpu.RenderPipelineDescriptor{
		Layout:    a.pl,
		Vertex:    webgpu.VertexState{Module: mod, EntryPoint: "vs"},
		Primitive: webgpu.PrimitiveState{Topology: webgpu.PrimitiveTopologyTriangleList, CullMode: webgpu.CullModeNone},
		Fragment:  &webgpu.FragmentState{Module: mod, EntryPoint: "fs", Targets: []webgpu.ColorTargetState{{Format: webgpu.TextureFormatR8Uint}}},
	}); err != nil {
		return err
	}
	r.layered = true
	return nil
}

// recordAliasLayer is the entities' pass of a layered frame.
func (r *Renderer) recordAliasLayer() error {
	p := r.rec.BeginRenderPass(r.entPass)
	if a := r.alias; a != nil && len(a.draws) > 0 {
		p.SetPipeline(r.entPipe)
		p.SetBindGroup(0, a.group)
		for _, d := range a.draws {
			p.Draw(d.Count, 1, d.First, d.Instance)
		}
	}
	return p.End()
}

// EnableAlias uploads the alias models of set and builds the alias
// pipeline; [Renderer.DrawEntities] then draws entities over the world.
func (r *Renderer) EnableAlias(set *AliasSet) error {
	dev := r.dev
	a := &aliasGPU{set: set}
	skins, err := dev.CreateTexture(webgpu.TextureDescriptor{Label: "alias skins", Size: webgpu.Extent3D{Width: uint32(set.Skins.Width), Height: uint32(set.Skins.Height)},
		Format: webgpu.TextureFormatR8Uint, Usage: webgpu.TextureUsageTextureBinding | webgpu.TextureUsageCopyDst})
	if err != nil {
		return err
	}
	if err := r.upload(skins, set.Skins); err != nil {
		return err
	}
	sv, err := skins.CreateView(nil)
	if err != nil {
		return err
	}
	storage := func(label string, data []byte) (*webgpu.Buffer, error) {
		b, err := dev.CreateBuffer(webgpu.BufferDescriptor{Label: label, Size: uint64((len(data) + 3) &^ 3), Usage: webgpu.BufferUsageStorage | webgpu.BufferUsageCopyDst})
		if err != nil {
			return nil, err
		}
		return b, writeBuffer(dev, b, data)
	}
	pb := make([]byte, 4*len(set.Poses))
	for i, v := range set.Poses {
		pb[4*i], pb[4*i+1], pb[4*i+2], pb[4*i+3] = byte(v), byte(v>>8), byte(v>>16), byte(v>>24)
	}
	poses, err := storage("alias poses", pb)
	if err != nil {
		return err
	}
	corners, err := storage("alias corners", set.Corners)
	if err != nil {
		return err
	}
	if a.ents, err = storage("alias entities", make([]byte, MaxAliasEntities*EntRecordSize)); err != nil {
		return err
	}
	ro := func(binding uint32) webgpu.BindGroupLayoutEntry {
		return webgpu.BindGroupLayoutEntry{Binding: binding, Visibility: webgpu.ShaderStageVertex, Buffer: &webgpu.BufferBindingLayout{Type: webgpu.BufferBindingTypeReadOnlyStorage}}
	}
	uint2d := &webgpu.TextureBindingLayout{SampleType: webgpu.TextureSampleTypeUint}
	bgl, err := dev.CreateBindGroupLayout(webgpu.BindGroupLayoutDescriptor{Entries: []webgpu.BindGroupLayoutEntry{
		{Binding: 0, Visibility: webgpu.ShaderStageVertex, Buffer: &webgpu.BufferBindingLayout{Type: webgpu.BufferBindingTypeUniform}},
		{Binding: 1, Visibility: webgpu.ShaderStageFragment, Texture: uint2d},
		{Binding: 2, Visibility: webgpu.ShaderStageFragment, Texture: uint2d},
		ro(3), ro(4), ro(5),
	}})
	if err != nil {
		return err
	}
	pl, err := dev.CreatePipelineLayout(webgpu.PipelineLayoutDescriptor{BindGroupLayouts: []*webgpu.BindGroupLayout{bgl}})
	if err != nil {
		return err
	}
	a.pl = pl
	if a.group, err = dev.CreateBindGroup(webgpu.BindGroupDescriptor{Layout: bgl, Entries: []webgpu.BindGroupEntry{
		{Binding: 0, Buffer: r.ub}, {Binding: 1, TextureView: sv}, {Binding: 2, TextureView: r.cmView},
		{Binding: 3, Buffer: poses}, {Binding: 4, Buffer: corners}, {Binding: 5, Buffer: a.ents},
	}}); err != nil {
		return err
	}
	mod, err := dev.CreateShaderModule(webgpu.ShaderModuleDescriptor{Label: "alias", Code: AliasShaderWGSL})
	if err != nil {
		return err
	}
	if a.pipe, err = dev.CreateRenderPipeline(webgpu.RenderPipelineDescriptor{
		Layout:    pl,
		Vertex:    webgpu.VertexState{Module: mod, EntryPoint: "vs"},
		Primitive: webgpu.PrimitiveState{Topology: webgpu.PrimitiveTopologyTriangleList, CullMode: webgpu.CullModeNone},
		Fragment:  &webgpu.FragmentState{Module: mod, EntryPoint: "fs", Targets: []webgpu.ColorTargetState{{Format: webgpu.TextureFormatR8Uint}}},
	}); err != nil {
		return err
	}
	r.alias = a
	return nil
}

// prepareAlias encodes the frame's entity records (before the pass opens).
func (r *Renderer) prepareAlias(ents []worldview.AliasEnt) error {
	a := r.alias
	if a == nil || len(ents) == 0 {
		if a != nil {
			a.draws = a.draws[:0]
		}
		if a == nil && len(ents) > 0 {
			return fmt.Errorf("gpuworld: entities given but EnableAlias was not called")
		}
		return nil
	}
	if len(ents) > MaxAliasEntities {
		return fmt.Errorf("gpuworld: %d entities, at most %d", len(ents), MaxAliasEntities)
	}
	var err error
	// R_AliasCheckBBox: the entities partly outside the view take r_aclip.c's
	// clipping (the near plane and the four sides of the frame).
	rd := &render.RefDef{ViewAngles: r.cur.Angles, ViewOrigin: r.cur.Origin, FovX: r.w.FOV, FovY: r.w.FOV}
	clip := func(e worldview.AliasEnt) bool {
		ent := render.AliasEntityInterp{
			AliasEntity:  render.AliasEntity{Origin: e.Origin, AnglePitch: e.Angles[0], AngleYaw: e.Angles[1], AngleRoll: e.Angles[2], FrameIdx: e.Frame},
			FrameIdxNext: e.Next, Lerp: e.Lerp,
		}
		draw, accept := render.AliasClassify(r.width, r.height, rd, e.Model.M, ent)
		return draw && accept == render.AliasClipped && !r.NoAliasClip
	}
	if a.data, a.draws, err = a.set.Entities(ents, a.data, a.draws, clip); err != nil {
		return err
	}
	r.rec.WriteBuffer(a.ents, 0, a.data)
	return nil
}

// recordAlias draws the prepared entities in the open pass.
func (r *Renderer) recordAlias(p *webgpu.RenderPassRecorder) {
	a := r.alias
	if a == nil || len(a.draws) == 0 {
		return
	}
	p.SetPipeline(a.pipe)
	p.SetBindGroup(0, a.group)
	for _, d := range a.draws {
		p.Draw(d.Count, 1, d.First, d.Instance)
	}
}
