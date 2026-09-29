// Command svg2vsdx converts SVG diagrams into Microsoft Visio (.vsdx) files
// made of native, editable shapes, glued connectors and text.
//
// Graphviz output (dot, protodot, ...) is recognised: clusters become
// containers, nodes become shapes with their label text, and edges become
// dynamic connectors glued to connection points at the same spot (record
// row / port) as in the drawing. Other SVGs are mapped primitive by
// primitive. The resulting .vsdx opens in Visio, LibreOffice Draw and
// draw.io, and imports into Miro and Lucidchart as editable diagrams.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/nontechno/experimental/svg2visio/internal/scene"
	"github.com/nontechno/experimental/svg2visio/internal/svgdoc"
	"github.com/nontechno/experimental/svg2visio/internal/vsdx"
)

func main() {
	var (
		out       = flag.String("o", "", "output file (single input only; \"-\" for stdout). Default: input name with .vsdx")
		mode      = flag.String("mode", "detailed", "detailed: each node is a group with its colored cells | compact: one plain shape per node")
		scale     = flag.Float64("scale", 0, "points on the Visio page per SVG unit (0 = auto: undo any shrink-to-fit the SVG applied, e.g. Graphviz size=)")
		fontScale = flag.Float64("font-scale", 1, "extra font size multiplier")
		mono      = flag.String("mono-font", "Courier New", "font for monospace text")
		sans      = flag.String("sans-font", "Arial", "font for sans-serif text")
		serif     = flag.String("serif-font", "Times New Roman", "font for serif text")
		route     = flag.String("route", "original", "connector path: original (as drawn) | straight | curved")
		quiet     = flag.Bool("q", false, "no summary output")
	)
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: svg2vsdx [flags] input.svg [more.svg ...]\n       svg2vsdx [flags] -o out.vsdx input.svg\n       cat in.svg | svg2vsdx -o - - > out.vsdx\n\n")
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() == 0 {
		flag.Usage()
		os.Exit(2)
	}
	if *mode != "compact" && *mode != "detailed" {
		fail("-mode must be compact or detailed")
	}
	if *route != "original" && *route != "straight" && *route != "curved" {
		fail("-route must be original, straight or curved")
	}
	if *out != "" && flag.NArg() > 1 {
		fail("-o can only be used with a single input")
	}
	opt := vsdx.Options{
		Scale: *scale / 72, FontScale: *fontScale, MonoFont: *mono,
		SansFont: *sans, SerifFont: *serif, Routing: *route,
	}
	failed := 0
	for _, in := range flag.Args() {
		dst := *out
		if dst == "" {
			if in == "-" {
				dst = "-"
			} else {
				dst = strings.TrimSuffix(in, filepath.Ext(in)) + ".vsdx"
			}
		}
		if err := convert(in, dst, *mode == "detailed", opt, *quiet); err != nil {
			fmt.Fprintf(os.Stderr, "svg2vsdx: %s: %v\n", in, err)
			failed++
		}
	}
	if failed > 0 {
		os.Exit(1)
	}
}

func convert(in, dst string, detailed bool, opt vsdx.Options, quiet bool) error {
	var r io.Reader = os.Stdin
	if in != "-" {
		fh, err := os.Open(in)
		if err != nil {
			return err
		}
		defer fh.Close()
		r = fh
	}
	root, err := svgdoc.Parse(r)
	if err != nil {
		return err
	}
	s := scene.FromSVG(root, scene.Options{Detailed: detailed})
	if len(s.Shapes)+len(s.Texts)+len(s.Frames) == 0 {
		return fmt.Errorf("nothing convertible found")
	}
	opt.Title = strings.TrimSuffix(filepath.Base(in), filepath.Ext(in))
	if opt.Scale <= 0 { // auto
		opt.Scale = 1.0 / 72
		if s.NativeScale > 0 && (s.NativeScale < 0.99 || s.NativeScale > 1.01) {
			opt.Scale /= s.NativeScale
		}
	}

	var buf bytes.Buffer
	if err := vsdx.Write(&buf, s, opt); err != nil {
		return err
	}
	if dst == "-" {
		_, err = os.Stdout.Write(buf.Bytes())
	} else {
		err = os.WriteFile(dst, buf.Bytes(), 0o644)
	}
	if err != nil {
		return err
	}
	if !quiet {
		fmt.Fprintf(os.Stderr, "%s -> %s (%s): %d containers, %d shapes, %d texts, %d connectors\n",
			in, dst, s.Source, len(s.Frames), len(s.Shapes), len(s.Texts), len(s.Connectors))
		for _, w := range s.Warnings {
			fmt.Fprintln(os.Stderr, "  warning:", w)
		}
	}
	return nil
}

func fail(msg string) {
	fmt.Fprintln(os.Stderr, "svg2vsdx:", msg)
	os.Exit(2)
}
