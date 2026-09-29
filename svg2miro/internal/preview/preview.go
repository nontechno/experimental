// Package preview renders a scene as the board will lay it out, so the
// conversion can be inspected without a Miro account.
package preview

import (
	"fmt"
	"html"
	"io"
	"math"
	"strings"

	"github.com/seamia/svg2miro/internal/miro"
	"github.com/seamia/svg2miro/internal/scene"
)

// Render writes an SVG approximation of the board.
func Render(w io.Writer, s *scene.Scene, l miro.Layout) error {
	l.Bind(s)
	x0, y0 := l.Point(scene.Point{X: s.Bounds.MinX, Y: s.Bounds.MinY})
	x1, y1 := l.Point(scene.Point{X: s.Bounds.MaxX, Y: s.Bounds.MaxY})
	pad := 40.0
	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="%.0f %.0f %.0f %.0f" width="%.0f" height="%.0f">`+"\n",
		x0-pad, y0-pad-30, x1-x0+2*pad, y1-y0+2*pad+30, (x1-x0+2*pad)/2, (y1-y0+2*pad+30)/2)
	b.WriteString(`<defs><marker id="arr" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse"><path d="M0,0 L10,5 L0,10 z" fill="context-stroke"/></marker></defs>` + "\n")
	fmt.Fprintf(&b, `<rect x="%.0f" y="%.0f" width="%.0f" height="%.0f" fill="#f7f7f5"/>`+"\n", x0-pad, y0-pad-30, x1-x0+2*pad, y1-y0+2*pad+30)

	fontCSS := func(f string, size string) string {
		fam := "Arial, sans-serif"
		switch f {
		case "mono":
			fam = "'Roboto Mono', 'DejaVu Sans Mono', Menlo, monospace"
		case "serif":
			fam = "Georgia, serif"
		}
		return fmt.Sprintf(`font-family="%s" font-size="%s"`, fam, size)
	}
	fs := func(size float64) string {
		v := math.Max(10, math.Round(size*l.Scale*l.FontScale))
		return fmt.Sprintf("%.0f", v)
	}

	for _, f := range s.Frames {
		cx, cy, w, h := l.Box(f.Box)
		fill := f.Fill
		if fill == "" {
			fill = "#ffffff"
		}
		fmt.Fprintf(&b, `<rect x="%.1f" y="%.1f" width="%.1f" height="%.1f" fill="%s" stroke="#b0b0b0"/>`+"\n", cx-w/2, cy-h/2, w, h, fill)
		fmt.Fprintf(&b, `<text x="%.1f" y="%.1f" font-family="Arial" font-size="20" fill="#555">%s</text>`+"\n", cx-w/2, cy-h/2-8, html.EscapeString(f.Title))
	}
	for _, sh := range s.Shapes {
		cx, cy, w, h := l.Box(sh.Box)
		fill, fop := sh.Fill, "1"
		if fill == "" {
			fill, fop = "#ffffff", "0"
		}
		stroke, sw := sh.Border, math.Max(1, sh.BorderWidth*l.Scale)
		if stroke == "" {
			stroke, sw = "none", 0
		}
		attrs := fmt.Sprintf(`fill="%s" fill-opacity="%s" stroke="%s" stroke-width="%.1f"`, fill, fop, stroke, sw)
		switch sh.Kind {
		case "circle":
			fmt.Fprintf(&b, `<ellipse cx="%.1f" cy="%.1f" rx="%.1f" ry="%.1f" %s/>`+"\n", cx, cy, w/2, h/2, attrs)
		case "rhombus":
			fmt.Fprintf(&b, `<polygon points="%.1f,%.1f %.1f,%.1f %.1f,%.1f %.1f,%.1f" %s/>`+"\n", cx, cy-h/2, cx+w/2, cy, cx, cy+h/2, cx-w/2, cy, attrs)
		default:
			rx := 0.0
			if sh.Kind == "round_rectangle" {
				rx = math.Min(w, h) * 0.15
			}
			fmt.Fprintf(&b, `<rect x="%.1f" y="%.1f" width="%.1f" height="%.1f" rx="%.1f" %s/>`+"\n", cx-w/2, cy-h/2, w, h, rx, attrs)
		}
		if len(sh.Lines) == 0 {
			continue
		}
		size := fs(sh.FontSize)
		lh := math.Max(10, math.Round(sh.FontSize*l.Scale*l.FontScale)) * 1.4
		tx, anchor := cx, "middle"
		switch sh.AlignH {
		case "left":
			tx, anchor = cx-w/2+6, "start"
		case "right":
			tx, anchor = cx+w/2-6, "end"
		}
		total := lh * float64(len(sh.Lines))
		ty := cy - total/2 + lh*0.75
		if sh.AlignV == "top" {
			ty = cy - h/2 + lh*0.75 + 2
		}
		fmt.Fprintf(&b, `<text xml:space="preserve" %s fill="%s" text-anchor="%s">`, fontCSS(sh.FontFamily, size), orDef(sh.TextColor, "#1a1a1a"), anchor)
		for i, ln := range sh.Lines {
			content := spans(ln)
			if anchor != "start" { // some renderers mis-anchor nested tspans
				content = html.EscapeString(ln.Plain())
			}
			fmt.Fprintf(&b, `<tspan x="%.1f" y="%.1f">%s</tspan>`, tx, ty+float64(i)*lh, content)
		}
		b.WriteString("</text>\n")
	}
	for _, t := range s.Texts {
		cx, cy, w, _ := l.Box(t.Box)
		tx, anchor := cx, "middle"
		if t.AlignH == "left" {
			tx, anchor = cx-w/2, "start"
		}
		fmt.Fprintf(&b, `<text xml:space="preserve" x="%.1f" y="%.1f" %s fill="%s" text-anchor="%s" dominant-baseline="middle">%s</text>`+"\n",
			tx, cy, fontCSS(t.FontFamily, fs(t.FontSize)), orDef(t.Color, "#1a1a1a"), anchor, spans(t.Line))
	}
	for _, c := range s.Connectors {
		from, to := s.Shape(c.From), s.Shape(c.To)
		if from == nil || to == nil {
			continue
		}
		ax, ay := attachAbs(l, from.Box, c.FromPt)
		bx, by := attachAbs(l, to.Box, c.ToPt)
		d := math.Max(60, math.Abs(bx-ax)/2)
		ms := ""
		if c.EndArrow {
			ms += ` marker-end="url(#arr)"`
		}
		if c.StartArrow {
			ms += ` marker-start="url(#arr)"`
		}
		dash := ""
		if c.Dashed {
			dash = ` stroke-dasharray="8 6"`
		}
		fmt.Fprintf(&b, `<path d="M%.1f,%.1f C%.1f,%.1f %.1f,%.1f %.1f,%.1f" fill="none" stroke="%s" stroke-width="1.5" stroke-opacity="0.75"%s%s/>`+"\n",
			ax, ay, ax+d*dir(from.Box, c.FromPt), ay, bx+d*dir(to.Box, c.ToPt), by, bx, by, orDef(c.Color, "#1a1a1a"), dash, ms)
	}
	b.WriteString("</svg>\n")
	_, err := io.WriteString(w, b.String())
	return err
}

func attachAbs(l miro.Layout, box scene.Rect, p scene.Point) (float64, float64) {
	_, _, fx, fy := miro.Attach(box, p)
	cx, cy, w, h := l.Box(box)
	return cx - w/2 + fx*w, cy - h/2 + fy*h
}

// dir gives the horizontal tangent sign for a curve leaving a box side.
func dir(box scene.Rect, p scene.Point) float64 {
	_, _, fx, _ := miro.Attach(box, p)
	switch fx {
	case 0:
		return -1
	case 1:
		return 1
	}
	return 0
}

func spans(l scene.Line) string {
	var b strings.Builder
	for _, sp := range l {
		t := html.EscapeString(sp.Text)
		a := ""
		if sp.Bold {
			a += ` font-weight="bold"`
		}
		if sp.Italic {
			a += ` font-style="italic"`
		}
		if sp.Underline {
			a += ` text-decoration="underline"`
		}
		if a == "" {
			b.WriteString(t)
		} else {
			fmt.Fprintf(&b, "<tspan%s>%s</tspan>", a, t)
		}
	}
	return b.String()
}

func orDef(v, d string) string {
	if v == "" {
		return d
	}
	return v
}
