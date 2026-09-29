package svgdoc

import (
	"strings"
)

// TextRun is a positioned, styled piece of text in absolute coordinates.
type TextRun struct {
	Pos       Point // anchor point (baseline)
	Text      string
	Size      float64
	Family    string
	Anchor    string // start | middle | end
	Fill      string
	Bold      bool
	Italic    bool
	Underline bool
}

// Mono reports whether the run uses a monospace family.
func (t TextRun) Mono() bool {
	f := strings.ToLower(t.Family)
	return strings.Contains(f, "mono") || strings.Contains(f, "courier") || strings.Contains(f, "consol")
}

// EstWidth estimates the rendered width of the run in absolute units.
func (t TextRun) EstWidth() float64 {
	k := 0.55
	if t.Mono() {
		k = 0.6
	}
	return float64(len([]rune(t.Text))) * t.Size * k
}

// Bounds estimates the run's bounding box.
func (t TextRun) Bounds() Rect {
	w := t.EstWidth()
	x := t.Pos.X
	switch t.Anchor {
	case "middle":
		x -= w / 2
	case "end":
		x -= w
	}
	return Rect{x, t.Pos.Y - t.Size*0.85, x + w, t.Pos.Y + t.Size*0.25}
}

// TextRuns extracts text runs from a <text> element (including <tspan>s).
func (n *Node) TextRuns() []TextRun {
	if n.Name != "text" {
		return nil
	}
	var runs []TextRun
	base := Point{Num(n.Attr["x"], 0), Num(n.Attr["y"], 0)}
	mk := func(el *Node, pos Point, s string) {
		s = collapseSpace(s)
		if strings.TrimSpace(s) == "" && s != " " {
			return
		}
		scale := el.CTM.ScaleFactor()
		if scale == 0 {
			scale = 1
		}
		weight := el.Inherited("font-weight", "normal")
		runs = append(runs, TextRun{
			Pos:       el.CTM.Apply(pos),
			Text:      s,
			Size:      Num(el.Inherited("font-size", "16"), 16) * scale,
			Family:    el.Inherited("font-family", "sans-serif"),
			Anchor:    el.Inherited("text-anchor", "start"),
			Fill:      el.Inherited("fill", "#000000"),
			Bold:      weight == "bold" || weight == "bolder" || Num(weight, 400) >= 600,
			Italic:    el.Inherited("font-style", "normal") == "italic",
			Underline: strings.Contains(el.Inherited("text-decoration", ""), "underline"),
		})
	}
	if strings.TrimSpace(n.Text) != "" || (n.Text == " " && len(n.Children) == 0) {
		mk(n, base, n.Text)
	}
	cur := base
	for _, c := range n.Children {
		if c.Name != "tspan" {
			continue
		}
		p := cur
		if v, ok := c.Attr["x"]; ok {
			p.X = Num(v, p.X)
		}
		if v, ok := c.Attr["y"]; ok {
			p.Y = Num(v, p.Y)
		}
		p.X += Num(c.Attr["dx"], 0)
		p.Y += Num(c.Attr["dy"], 0)
		mk(c, p, c.AllText())
		cur = p
	}
	return runs
}

func collapseSpace(s string) string {
	s = strings.NewReplacer("\n", " ", "\t", " ", "\r", " ").Replace(s)
	for strings.Contains(s, "  ") {
		s = strings.ReplaceAll(s, "  ", " ")
	}
	return s
}
