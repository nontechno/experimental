package scene

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/nontechno/experimental/svg2visio/internal/svgdoc"
)

// geom is a drawable primitive extracted from an element.
type geom struct {
	el          *svgdoc.Node
	pts         []Point
	closed      bool
	box         Rect
	fill        string
	stroke      string
	strokeWidth float64
	dashed      bool
}

var skipContainers = map[string]bool{
	"defs": true, "script": true, "style": true, "title": true, "desc": true,
	"metadata": true, "clipPath": true, "mask": true, "marker": true,
	"symbol": true, "pattern": true, "linearGradient": true, "radialGradient": true,
	"filter": true,
}

func geomOf(el *svgdoc.Node) (geom, bool) {
	if el.IsHidden() {
		return geom{}, false
	}
	pts, closed, ok := el.Points()
	if !ok {
		return geom{}, false
	}
	g := geom{el: el, pts: pts, closed: closed, box: svgdoc.BoundsOf(pts)}
	if el.Name == "circle" || el.Name == "ellipse" {
		g.closed = true
	}
	// paths that end where they start are regions even without "Z"
	if !g.closed && el.Name == "path" && len(pts) > 2 &&
		pts[0].Dist(pts[len(pts)-1]) <= 0.01*math.Hypot(g.box.W(), g.box.H())+0.5 {
		g.closed = true
	}
	defFill := "#000000"
	if el.Name == "line" || el.Name == "polyline" {
		defFill = "none"
	}
	if c, ok := svgdoc.Color(el.Inherited("fill", defFill)); ok && el.Inherited("fill-opacity", "1") != "0" {
		g.fill = c
	}
	if c, ok := svgdoc.Color(el.Inherited("stroke", "none")); ok && el.Inherited("stroke-opacity", "1") != "0" {
		g.stroke = c
	}
	g.strokeWidth = svgdoc.Num(el.Inherited("stroke-width", "1"), 1) * el.CTM.ScaleFactor()
	d := el.Inherited("stroke-dasharray", "none")
	g.dashed = d != "none" && d != "" && d != "0"
	return g, true
}

// classify maps a polygon to the closest Miro shape name.
func classify(g geom) string {
	switch g.el.Name {
	case "circle", "ellipse":
		return "circle"
	case "rect":
		if svgdoc.Num(g.el.Attr["rx"], 0) > 0 || svgdoc.Num(g.el.Attr["ry"], 0) > 0 {
			return "round_rectangle"
		}
		return "rectangle"
	}
	b := g.box
	tol := math.Max(b.W(), b.H())*0.02 + 0.5
	onCorner := func(p Point) bool {
		return (math.Abs(p.X-b.MinX) < tol || math.Abs(p.X-b.MaxX) < tol) &&
			(math.Abs(p.Y-b.MinY) < tol || math.Abs(p.Y-b.MaxY) < tol)
	}
	onMid := func(p Point) bool {
		c := b.Center()
		return (math.Abs(p.X-c.X) < tol && (math.Abs(p.Y-b.MinY) < tol || math.Abs(p.Y-b.MaxY) < tol)) ||
			(math.Abs(p.Y-c.Y) < tol && (math.Abs(p.X-b.MinX) < tol || math.Abs(p.X-b.MaxX) < tol))
	}
	n := len(g.pts)
	if g.el.Name == "path" && n > 12 {
		return "rectangle"
	}
	switch n {
	case 3:
		return "triangle"
	case 4:
		all := func(f func(Point) bool) bool {
			for _, p := range g.pts {
				if !f(p) {
					return false
				}
			}
			return true
		}
		if all(onCorner) {
			return "rectangle"
		}
		if all(onMid) {
			return "rhombus"
		}
		return "parallelogram"
	case 5:
		return "pentagon"
	case 6:
		return "hexagon"
	case 8:
		return "octagon"
	}
	return "rectangle"
}

func familyOf(r svgdoc.TextRun) string {
	f := strings.ToLower(r.Family)
	switch {
	case r.Mono():
		return "mono"
	case strings.Contains(f, "times") || strings.Contains(f, "serif") && !strings.Contains(f, "sans"):
		return "serif"
	}
	return "sans"
}

func textColor(runs []svgdoc.TextRun) string {
	for _, r := range runs {
		if c, ok := svgdoc.Color(r.Fill); ok {
			return c
		}
	}
	return "#000000"
}

// exactOutlineNeeded reports whether a primitive's outline differs from
// what the plain shape kind would draw (paths, irregular polygons).
func exactOutlineNeeded(g geom, kind string) bool {
	switch g.el.Name {
	case "rect", "circle", "ellipse":
		return false
	case "path":
		return true
	}
	return kind != "rectangle" && kind != "rhombus" && kind != "triangle"
}

// IsGraphviz reports whether the document looks like Graphviz SVG output.
func IsGraphviz(root *svgdoc.Node) bool {
	graph, nodes := false, false
	root.Walk(func(n *svgdoc.Node) bool {
		if n.Name == "g" {
			graph = graph || n.HasClass("graph")
			nodes = nodes || n.HasClass("node") || n.HasClass("edge")
		}
		return !(graph && nodes)
	})
	return graph && nodes
}

func fromGraphviz(root *svgdoc.Node, opt Options) *Scene {
	s := &Scene{Source: "graphviz", NativeScale: 1}
	for _, g := range root.Find(func(n *svgdoc.Node) bool { return n.Name == "g" && n.HasClass("graph") }) {
		if f := g.CTM.ScaleFactor(); f > 0 {
			s.NativeScale = f
		}
		break
	}
	var edges []*svgdoc.Node
	textSeq := 0
	root.Walk(func(n *svgdoc.Node) bool {
		if skipContainers[n.Name] {
			return false
		}
		if n.Name == "g" {
			switch {
			case n.HasClass("cluster"):
				gvCluster(s, n)
				return false
			case n.HasClass("node"):
				gvNode(s, n, opt)
				return false
			case n.HasClass("edge"):
				edges = append(edges, n)
				return false
			}
		}
		if n.Name == "text" {
			runs := n.TextRuns()
			if len(runs) > 0 {
				textSeq++
				addFreeText(s, fmt.Sprintf("text%d", textSeq), runs)
			}
			return false
		}
		return true
	})
	for _, e := range edges {
		gvEdge(s, e)
	}
	return s
}

func addFreeText(s *Scene, key string, runs []svgdoc.TextRun) { addGroupText(s, key, "", runs) }

func addGroupText(s *Scene, key, group string, runs []svgdoc.TextRun) {
	box := svgdoc.EmptyRect()
	for _, r := range runs {
		box = box.Union(r.Bounds())
	}
	lines, _, align, size := layoutLines(runs, box)
	for i, l := range lines {
		k := key
		if len(lines) > 1 {
			k = fmt.Sprintf("%s.%d", key, i)
		}
		s.Texts = append(s.Texts, Text{
			Key: k, Box: box, Line: l, FontSize: size,
			FontFamily: familyOf(runs[0]), Color: textColor(runs), AlignH: align, Group: group,
		})
	}
}

func tooltipOf(n *svgdoc.Node) string {
	t := ""
	n.Walk(func(x *svgdoc.Node) bool {
		if v, ok := x.Attr["xlink:title"]; ok && t == "" {
			t = v
		}
		return t == ""
	})
	return t
}

func collect(n *svgdoc.Node) (geoms []geom, runs []svgdoc.TextRun) {
	n.Walk(func(x *svgdoc.Node) bool {
		if skipContainers[x.Name] {
			return false
		}
		if x.Name == "text" {
			runs = append(runs, x.TextRuns()...)
			return false
		}
		if g, ok := geomOf(x); ok {
			geoms = append(geoms, g)
		}
		return true
	})
	return
}

func gvCluster(s *Scene, n *svgdoc.Node) {
	geoms, runs := collect(n)
	box := svgdoc.EmptyRect()
	fr := Frame{Key: n.ChildText("title"), BorderWidth: 1}
	for i, g := range geoms {
		box = box.Union(g.box)
		if i == 0 {
			fr.Fill, fr.Border, fr.BorderWidth, fr.Dashed = g.fill, g.stroke, g.strokeWidth, g.dashed
		}
	}
	if box.Empty() {
		return
	}
	fr.Box = box
	title := ""
	for _, r := range runs {
		title += r.Text
	}
	if len(runs) > 0 {
		fr.TitleSize, fr.TitleFamily, fr.TitleColor = runs[0].Size, familyOf(runs[0]), textColor(runs)
		fr.TitleBottom = runs[0].Pos.Y > box.Center().Y
	}
	fr.Title = strings.TrimSpace(title)
	s.Frames = append(s.Frames, fr)
}

func gvNode(s *Scene, n *svgdoc.Node, opt Options) {
	id := n.ChildText("title")
	geoms, runs := collect(n)
	if len(geoms) == 0 {
		if len(runs) > 0 { // plaintext node without outline
			addFreeText(s, id, runs)
		}
		return
	}
	box := svgdoc.EmptyRect()
	for _, g := range geoms {
		box = box.Union(g.box)
	}

	// Container fill = largest filled primitive; border = first stroked one
	// (records draw the outline last as fill="none").
	fillIdx, borderIdx := -1, -1
	for i, g := range geoms {
		if g.fill != "" && (fillIdx < 0 || g.box.Area() > geoms[fillIdx].box.Area()+0.5) {
			fillIdx = i
		}
	}
	for i := len(geoms) - 1; i >= 0; i-- {
		if geoms[i].stroke != "" && geoms[i].closed {
			borderIdx = i
			break
		}
	}
	kind := "rectangle"
	outline := borderIdx
	if outline < 0 {
		outline = fillIdx
	}
	var outlinePath [][]Point
	if outline >= 0 {
		kind = classify(geoms[outline])
		if exactOutlineNeeded(geoms[outline], kind) {
			outlinePath = geoms[outline].el.Subpaths()
		}
	}

	sh := Shape{
		Key: id, Kind: kind, Box: box, Path: outlinePath, Tooltip: tooltipOf(n), ConnectTarget: true,
		AlignV: "middle", TextColor: textColor(runs), BorderWidth: 1,
	}
	if fillIdx >= 0 {
		sh.Fill = geoms[fillIdx].fill
	}
	if borderIdx >= 0 {
		sh.Border = geoms[borderIdx].stroke
		sh.BorderWidth = geoms[borderIdx].strokeWidth
	}
	if len(runs) > 0 {
		sh.FontFamily = familyOf(runs[0])
	}

	if !opt.Detailed || len(geoms) <= 2 {
		lines, mono, align, size := layoutLines(runs, box)
		sh.Lines, sh.AlignH, sh.FontSize = lines, align, size
		if mono || len(lines) > 1 {
			sh.AlignV = "top"
		}
		s.Shapes = append(s.Shapes, sh)
		return
	}

	// Detailed: container + one shape per filled cell, texts go to the
	// smallest cell that contains them; the rest become free text rows.
	// All parts share Group=id so writers can keep them together.
	sh.Group = id
	s.Shapes = append(s.Shapes, sh)
	type cell struct {
		g    geom
		runs []svgdoc.TextRun
	}
	var cells []*cell
	for i, g := range geoms {
		if i == fillIdx || i == borderIdx || g.fill == "" || !g.closed {
			continue
		}
		cells = append(cells, &cell{g: g})
	}
	var loose []svgdoc.TextRun
	for _, r := range runs {
		c := r.Bounds().Center()
		var best *cell
		for _, cl := range cells {
			if cl.g.box.Contains(c, 0.5) && (best == nil || cl.g.box.Area() < best.g.box.Area()) {
				best = cl
			}
		}
		if best != nil {
			best.runs = append(best.runs, r)
		} else {
			loose = append(loose, r)
		}
	}
	for i, cl := range cells {
		cs := Shape{
			Key: fmt.Sprintf("%s#c%d", id, i), Kind: classify(cl.g), Box: cl.g.box,
			Fill: cl.g.fill, AlignV: "middle", TextColor: textColor(cl.runs), Group: id,
		}
		if len(cl.runs) > 0 {
			lines, _, align, size := layoutLines(cl.runs, cl.g.box)
			cs.Lines, cs.AlignH, cs.FontSize = lines, align, size
			cs.FontFamily = familyOf(cl.runs[0])
			tb := svgdoc.EmptyRect()
			for _, r := range cl.runs {
				tb = tb.Union(r.Bounds())
			}
			if len(lines) == 1 {
				lg, rg := tb.MinX-cl.g.box.MinX, cl.g.box.MaxX-tb.MaxX
				switch {
				case math.Abs(lg-rg) < cl.g.box.W()*0.15:
					cs.AlignH = "center"
				case lg > rg:
					cs.AlignH = "right"
				default:
					cs.AlignH = "left"
				}
			}
		}
		s.Shapes = append(s.Shapes, cs)
	}
	// loose runs, one text item per row
	sort.SliceStable(loose, func(i, j int) bool { return loose[i].Pos.Y < loose[j].Pos.Y })
	var row []svgdoc.TextRun
	flush := func() {
		if len(row) > 0 {
			addGroupText(s, fmt.Sprintf("%s#t%.0f", id, row[0].Pos.Y), id, row)
			row = nil
		}
	}
	for _, r := range loose {
		if len(row) > 0 && math.Abs(row[0].Pos.Y-r.Pos.Y) > r.Size*0.4 {
			flush()
		}
		row = append(row, r)
	}
	flush()
}

func parseEdgeTitle(t string) (from, to string, ok bool) {
	for _, sep := range []string{"->", "--"} {
		if i := strings.Index(t, sep); i > 0 {
			from, to = t[:i], t[i+len(sep):]
			strip := func(v string) string {
				v = strings.TrimSpace(v)
				// strip record port / compass point: node:port or node:port:ne
				if j := strings.Index(v, ":"); j > 0 {
					v = v[:j]
				}
				return v
			}
			return strip(from), strip(to), true
		}
	}
	return "", "", false
}

func gvEdge(s *Scene, n *svgdoc.Node) {
	title := n.ChildText("title")
	from, to, ok := parseEdgeTitle(title)
	if !ok {
		s.Warnings = append(s.Warnings, "edge with unparseable title: "+title)
		return
	}
	fs, ts := s.Shape(from), s.Shape(to)
	if fs == nil || ts == nil {
		s.Warnings = append(s.Warnings, fmt.Sprintf("edge %s: endpoint node not found", title))
		return
	}
	geoms, runs := collect(n)
	var route []Point
	var heads []geom
	c := Connector{Key: fmt.Sprintf("%s#%s", title, n.Attr["id"]), From: from, To: to, Color: "#000000", Width: 1}
	for _, g := range geoms {
		if g.el.Name == "path" || g.el.Name == "polyline" || g.el.Name == "line" {
			if len(route) > 0 {
				continue // parallel stroke of a multi-color edge: same route
			}
			route = append(route, g.pts...)
			if g.stroke != "" {
				c.Color = g.stroke
			}
			c.Width, c.Dashed = g.strokeWidth, g.dashed
		} else if g.closed {
			heads = append(heads, g)
		}
	}
	if len(route) < 2 {
		s.Warnings = append(s.Warnings, fmt.Sprintf("edge %s: no path", title))
		return
	}
	c.Route = route
	c.FromPt, c.ToPt = route[0], route[len(route)-1]
	for _, h := range heads {
		ctr := h.box.Center()
		atEnd := ctr.Dist(route[len(route)-1]) <= ctr.Dist(route[0])
		ref := route[0]
		if atEnd {
			ref = route[len(route)-1]
		}
		tip, far := ref, -1.0
		for _, p := range h.pts {
			if d := p.Dist(ref); d > far {
				tip, far = p, d
			}
		}
		if atEnd {
			c.EndArrow, c.ToPt = true, tip
		} else {
			c.StartArrow, c.FromPt = true, tip
		}
	}
	for _, r := range runs {
		c.Label = strings.TrimSpace(c.Label + " " + r.Text)
	}
	s.Connectors = append(s.Connectors, c)
}
