package scene

import (
	"fmt"

	"github.com/seamia/svg2miro/internal/svgdoc"
)

// fromGeneric maps arbitrary SVG: closed filled primitives become shapes,
// text becomes labels of the shape it sits on (or free text), and lines /
// open paths whose endpoints touch two shapes become connectors. Anything
// else is reported as a warning.
func fromGeneric(root *svgdoc.Node) *Scene {
	s := &Scene{Source: "generic"}
	type textItem struct {
		key  string
		runs []svgdoc.TextRun
	}
	var texts []textItem
	var lines []geom
	skippedPaths, seq := 0, 0

	root.Walk(func(n *svgdoc.Node) bool {
		if skipContainers[n.Name] || n.IsHidden() {
			return false
		}
		switch n.Name {
		case "text":
			if runs := n.TextRuns(); len(runs) > 0 {
				seq++
				texts = append(texts, textItem{fmt.Sprintf("text%d", seq), runs})
			}
			return false
		case "image", "use", "foreignObject":
			s.Warnings = append(s.Warnings, fmt.Sprintf("<%s> is not supported and was skipped", n.Name))
			return false
		}
		g, ok := geomOf(n)
		if !ok {
			return true
		}
		seq++
		key := n.Attr["id"]
		if key == "" {
			key = fmt.Sprintf("%s%d", n.Name, seq)
		}
		switch {
		case g.closed && (g.fill != "" || g.stroke != ""):
			if g.box.W() < 1 && g.box.H() < 1 {
				return true
			}
			kind := classify(g)
			if n.Name == "path" && kind == "rectangle" && len(g.pts) > 4 {
				s.Warnings = append(s.Warnings, fmt.Sprintf("path %s approximated by its bounding box", key))
			}
			s.Shapes = append(s.Shapes, Shape{
				Key: key, Kind: kind, Box: g.box, Fill: g.fill, Border: g.stroke,
				BorderWidth: g.strokeWidth, AlignH: "center", AlignV: "middle",
				ConnectTarget: true, Tooltip: n.ChildText("title"),
			})
		case !g.closed && len(g.pts) >= 2 && g.stroke != "":
			lines = append(lines, g)
		default:
			skippedPaths++
		}
		return true
	})

	// Attach texts to the smallest shape that contains them.
	for _, t := range texts {
		tb := svgdoc.EmptyRect()
		for _, r := range t.runs {
			tb = tb.Union(r.Bounds())
		}
		var best *Shape
		for i := range s.Shapes {
			sh := &s.Shapes[i]
			if sh.Box.Contains(tb.Center(), 0) && sh.Box.W() >= tb.W()*0.8 &&
				(best == nil || sh.Box.Area() < best.Box.Area()) {
				best = sh
			}
		}
		if best == nil {
			addFreeText(s, t.key, t.runs)
			continue
		}
		ls, _, align, size := layoutLines(t.runs, best.Box)
		best.Lines = append(best.Lines, ls...)
		best.AlignH, best.FontSize = align, size
		best.FontFamily, best.TextColor = familyOf(t.runs[0]), textColor(t.runs)
	}

	// Lines touching two shapes become connectors.
	hit := func(p Point) *Shape {
		var best *Shape
		for i := range s.Shapes {
			sh := &s.Shapes[i]
			if sh.Box.Contains(p, 3) && (best == nil || sh.Box.Area() < best.Box.Area()) {
				best = sh
			}
		}
		return best
	}
	for i, g := range lines {
		a, b := hit(g.pts[0]), hit(g.pts[len(g.pts)-1])
		if a == nil || b == nil || a == b {
			skippedPaths++
			continue
		}
		_, markerEnd := g.el.Attr["marker-end"]
		_, markerStart := g.el.Attr["marker-start"]
		s.Connectors = append(s.Connectors, Connector{
			Key: fmt.Sprintf("line%d", i), From: a.Key, To: b.Key,
			FromPt: g.pts[0], ToPt: g.pts[len(g.pts)-1], Route: g.pts,
			Color: g.stroke, Width: g.strokeWidth, Dashed: g.dashed,
			EndArrow: markerEnd, StartArrow: markerStart,
		})
	}
	if skippedPaths > 0 {
		s.Warnings = append(s.Warnings, fmt.Sprintf("%d free-form lines/paths have no native Miro equivalent and were skipped", skippedPaths))
	}
	return s
}
