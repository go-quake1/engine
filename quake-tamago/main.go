// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later
//
// quake-tamago is the bare-metal Quake-on-TamaGo entry point. It boots
// in QEMU as a `-kernel` ELF, probes the virtio PCI bus for gpu / input /
// sound devices, wires them through backend/virtio/realdev into the
// engine's backend.Backend contract, and hands control to runner.Setup
// which builds the shared real-pak bring-up (host + client + loopback +
// renderer + menu + attract demo + audio mix). RunUntilQuit drives the
// full per-tic pipeline: virtio-input -> client.Tick (clc_move) ->
// host.Frame (SV_Physics) -> Pre2DDraw (BSP walk + rasterize) ->
// Compose2D (console + notify) -> virtio-gpu PresentFrame.
//
// The bulk of the bring-up code that USED to live here moved to package
// [github.com/go-quake1/engine/runner] in 2026-06-28 so both this
// bare-metal entry AND cmd/quake-wasmbox share the same wiring. What
// stays here is the platform-specific layer: the PCI probe for virtio
// devices + the backend.Backend construction over them + the bare-metal
// halt() error handler.
//
// Rationale (project-driver quote): "on a fait les pilote virtio pour
// eprouver tamago" -- the go-virtio drivers exist so this binary can
// exercise the full pure-Go bare-metal stack end-to-end.
package main

import (
	"fmt"

	_ "github.com/go-virtio/validate/board"
	"github.com/go-virtio/validate/transport"

	"github.com/usbarmory/tamago/soc/intel/pci"

	"github.com/go-virtio/common"
	"github.com/go-virtio/gpu"
	"github.com/go-virtio/input"
	"github.com/go-virtio/sound"

	"github.com/go-quake1/engine/backend/virtio"
	"github.com/go-quake1/engine/backend/virtio/realdev"
	"github.com/go-quake1/engine/embedpak"
	"github.com/go-quake1/engine/quake-tamago/concharsfont"
	"github.com/go-quake1/engine/runner"
	enginesound "github.com/go-quake1/engine/sound"
)

// fbWidth / fbHeight are the framebuffer dimensions handed to
// virtio-gpu's SetupFramebuffer. 320x240 matches vanilla DOS Quake so
// the software span rasterizer's per-pixel work stays affordable in
// QEMU TCG.
const (
	fbWidth  = 320
	fbHeight = 240
)

func main() {
	if err := run(); err != nil {
		fmt.Printf("QUAKE: FAIL %v\n", err)
		halt()
	}
}

// run is main's testability seam (mirrors the validate harness shape).
// It returns errors instead of halting so the QEMU serial log carries
// the failure reason; main then halts on receipt.
func run() error {
	// 1. Open virtio-gpu via PCI. Mandatory: without a framebuffer the
	//    engine has nowhere to render.
	gpuDev := pci.Probe(0, common.PCIVendorID, common.PCIDeviceIDModernGPU)
	if gpuDev == nil {
		return fmt.Errorf("no virtio-gpu-pci device found")
	}
	g, err := gpu.OpenVirtioGPU(transport.New(gpuDev))
	if err != nil {
		return fmt.Errorf("OpenVirtioGPU: %w", err)
	}
	fmt.Printf("QUAKE: GPU=%#04x:%#04x scanouts=%d features=%#x\n",
		gpuDev.Vendor, gpuDev.Device, g.NumScanouts, g.NegotiatedFeatures)
	fb, err := g.SetupFramebuffer(0, fbWidth, fbHeight)
	if err != nil {
		return fmt.Errorf("SetupFramebuffer: %w", err)
	}
	fmt.Printf("QUAKE: framebuffer %dx%d resource=%d\n",
		fb.Width, fb.Height, fb.ResourceID)

	// 2. Open virtio-input (best-effort).
	var inputDev virtio.InputDevice
	inDev := pci.Probe(0, common.PCIVendorID, input.PCIDeviceIDModernInput)
	if inDev != nil {
		vi, err := input.OpenVirtioInput(transport.New(inDev))
		if err != nil {
			return fmt.Errorf("OpenVirtioInput: %w", err)
		}
		inputDev = realdev.WrapInput(vi)
		fmt.Printf("QUAKE: input=%#04x:%#04x\n", inDev.Vendor, inDev.Device)
	} else {
		fmt.Printf("QUAKE: no virtio-input device; engine runs without input\n")
	}

	// 3. Open virtio-snd (best-effort).
	var audioDev virtio.AudioDevice
	sndDev := pci.Probe(0, common.PCIVendorID, sound.PCIDeviceIDModernSound)
	if sndDev != nil {
		vs, err := sound.OpenVirtioSound(transport.New(sndDev))
		if err != nil {
			return fmt.Errorf("OpenVirtioSound: %w", err)
		}
		if infos, ierr := vs.PCMInfo(); ierr == nil {
			for i, e := range infos {
				fmt.Printf("QUAKE: snd-stream[%d] dir=%d rates=%#x formats=%#x ch=%d-%d\n",
					i, e.Direction, e.Rates, e.Formats, e.ChannelsMin, e.ChannelsMax)
			}
		} else {
			fmt.Printf("QUAKE: snd-PCMInfo err=%v\n", ierr)
		}
		res, serr := realdev.SetupAudio(vs, realdev.DefaultAudioStreamConfig)
		if serr != nil {
			fmt.Printf("QUAKE: SetupAudio err=%v (engine runs silent)\n", serr)
		} else {
			audioDev = realdev.WrapAudio(vs, res.StreamID, enginesound.DefaultSampleRate)
			fmt.Printf("QUAKE: sound=%#04x:%#04x stream=%d device-rate=%dHz mixer-rate=%dHz ch=%d fmt=%d (virtio-snd stream started)\n",
				sndDev.Vendor, sndDev.Device, res.StreamID, res.Rate, enginesound.DefaultSampleRate, res.Channels, res.Format)
		}
	} else {
		fmt.Printf("QUAKE: no virtio-snd device; engine runs silent\n")
	}

	// 4. Build the backend over the (up to three) devices.
	be, err := virtio.New(realdev.WrapFramebuffer(fb), inputDev, audioDev, nil)
	if err != nil {
		return fmt.Errorf("virtio.New: %w", err)
	}

	// 5. Open the embedded pak. nil means the placeholder is still
	//    installed; runner.Setup's BSP loader has a synthbsp fallback
	//    so the binary still boots and renders something.
	pakFS, pakErr := embedpak.OpenAsFS()
	if pakErr != nil {
		return fmt.Errorf("embedpak.OpenAsFS: %w", pakErr)
	}

	// 6. Hand off to the shared runner. Setup wires the full pipeline
	//    (real host, loopback, sound pool, music, particles, sprites,
	//    beams, alias preload, sbar, menu, attract demo, Pre2DDraw)
	//    and returns the configured *runloop.Runner ready to drive.
	r, err := runner.Setup(runner.SetupOpts{
		Backend:                     be,
		PakFS:                       pakFS,
		FBWidth:                     fbWidth,
		FBHeight:                    fbHeight,
		MapSlug:                     "start",
		ConCharsLump:                concharsfont.Build(0xDC, 0x67),
		DemoOrbit:                   true,
		DemoOrbitAutoDisableOnInput: true,
	})
	if err != nil {
		return fmt.Errorf("runner.Setup: %w", err)
	}

	return r.RunUntilQuit()
}

// halt is the tamago "spin forever after a fatal error" primitive. The
// board package's Exit handler also halts; this one covers the pre-board
// failure window + the in-engine return path.
func halt() {
	for {
	}
}
