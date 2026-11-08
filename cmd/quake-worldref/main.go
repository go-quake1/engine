// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

// Command quake-worldref draws the fixed e1m1 views of the world-only
// software reference (package worldview) and writes each as a PNG, with
// the SHA-256 of its 8-bit index frame: the figures the GPU world is
// compared with. The views are worldview's AcceptanceViews: generic
// ones (four yaws per origin), then the player-start view (the eye at
// info_player_start), the last number.
//
//	quake-worldref --pak pak0.pak --out <dir outside any repository>
//
// The pak is the shareware pak0.pak; it is never part of a repository.
// The output directory must not be inside a git work tree (a frame is a
// capture of copyrighted art).
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/go-quake1/engine/assets"
	"github.com/go-quake1/engine/bench"
	"github.com/go-quake1/engine/render"
	"github.com/go-quake1/engine/vfs"
	"github.com/go-quake1/engine/worldview"
)

var osExit = os.Exit

func main() {
	if err := RootCmd().Execute(); err != nil {
		osExit(1)
	}
}

// insideGit reports whether dir or one of its parents holds a .git.
func insideGit(dir string) bool {
	d, err := filepath.Abs(dir)
	if err != nil {
		return true
	}
	for {
		if _, err := os.Stat(filepath.Join(d, ".git")); err == nil {
			return true
		}
		p := filepath.Dir(d)
		if p == d {
			return false
		}
		d = p
	}
}

// RootCmd returns the command.
func RootCmd() *cobra.Command {
	var pakPath, out, slug string
	var w, h int
	c := &cobra.Command{
		Use:          "quake-worldref",
		Short:        "draw the fixed views of the world-only software reference",
		SilenceUsage: true,
		Args:         cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if pakPath == "" {
				pakPath = os.Getenv("QUAKE_PAK")
			}
			if pakPath == "" || out == "" {
				return fmt.Errorf("--pak (or QUAKE_PAK) and --out are required")
			}
			if insideGit(out) {
				return fmt.Errorf("--out %s is inside a git work tree: frames are captures of copyrighted art", out)
			}
			fsys, _, err := bench.OpenPak(pakPath)
			if err != nil {
				return err
			}
			wv, err := worldview.LoadMap(fsys, slug)
			if err != nil {
				return err
			}
			v := vfs.New()
			v.Add(fsys)
			cm, err := assets.LoadColorMapFrom(v)
			if err != nil {
				return err
			}
			pal, err := assets.LoadPaletteFrom(v)
			if err != nil {
				return err
			}
			if err := os.MkdirAll(out, 0o755); err != nil {
				return err
			}
			views, err := wv.AcceptanceViews()
			if err != nil {
				return err
			}
			for i, vw := range views {
				fb, err := render.NewFrameBuffer(w, h)
				if err != nil {
					return err
				}
				if err := wv.DrawSoftware(fb, cm, vw); err != nil {
					return err
				}
				img := image.NewRGBA(image.Rect(0, 0, w, h))
				for y := 0; y < h; y++ {
					for x := 0; x < w; x++ {
						c := pal[fb.Pixels[y*fb.Pitch+x]]
						img.Set(x, y, color.RGBA{c[0], c[1], c[2], 255})
					}
				}
				name := filepath.Join(out, fmt.Sprintf("%s-view%02d.png", slug, i))
				f, err := os.Create(name)
				if err != nil {
					return err
				}
				if err := png.Encode(f, img); err != nil {
					f.Close()
					return err
				}
				if err := f.Close(); err != nil {
					return err
				}
				sum := sha256.Sum256(fb.Pixels)
				fmt.Fprintf(cmd.OutOrStdout(), "%s sha256(indices) %s\n", name, hex.EncodeToString(sum[:]))
			}
			return nil
		},
	}
	f := c.Flags()
	f.StringVar(&pakPath, "pak", "", "pak0.pak (default $QUAKE_PAK)")
	f.StringVar(&out, "out", "", "output directory, outside any git work tree")
	f.StringVar(&slug, "map", "e1m1", "map slug")
	f.IntVar(&w, "width", 640, "frame width")
	f.IntVar(&h, "height", 400, "frame height")
	return c
}
