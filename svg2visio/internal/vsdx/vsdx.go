// Package vsdx writes a scene as a Microsoft Visio (.vsdx) drawing: an
// OPC/zip package that Miro (and Visio, draw.io, LibreOffice) import as
// native, editable shapes, glued connectors and text.
package vsdx

import (
	"archive/zip"
	"encoding/xml"
	"fmt"
	"io"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/nontechno/experimental/svg2visio/internal/scene"
)

// Options control the export.
type Options struct {
	Scale     float64 // drawing inches per SVG unit (default 1/72: 1pt = 1pt)
	FontScale float64 // multiplier for font sizes (default 1)
	Margin    float64 // page margin in inches (default 0.5)
	MonoFont  string  // default "Courier New"
	SansFont  string  // default "Arial"
	SerifFont string  // default "Times New Roman"
	Routing   string  // connector path: "original" (follow the SVG path, default) | "straight" | "curved"
	Title     string
}

func (o *Options) defaults() {
	if o.Scale <= 0 {
		o.Scale = 1.0 / 72
	}
	if o.FontScale <= 0 {
		o.FontScale = 1
	}
	if o.Margin <= 0 {
		o.Margin = 0.5
	}
	if o.MonoFont == "" {
		o.MonoFont = "Courier New"
	}
	if o.SansFont == "" {
		o.SansFont = "Arial"
	}
	if o.Routing == "" {
		o.Routing = "original"
	}
	if o.SerifFont == "" {
		o.SerifFont = "Times New Roman"
	}
}

const (
	nsMain = "http://schemas.microsoft.com/office/visio/2012/main"
	nsRel  = "http://schemas.openxmlformats.org/officeDocument/2006/relationships"
)

// Write renders the scene as a .vsdx package.
func Write(w io.Writer, s *scene.Scene, o Options) error {
	o.defaults()
	b := &builder{s: s, o: o, conn: map[string]*connPoints{}}
	page := b.page()
	pw, ph := b.pageSize()

	z := zip.NewWriter(w)
	parts := []struct{ name, body string }{
		{"[Content_Types].xml", contentTypes},
		{"_rels/.rels", rootRels},
		{"docProps/app.xml", appXML},
		{"docProps/core.xml", fmt.Sprintf(coreXML, esc(o.Title), time.Now().UTC().Format(time.RFC3339))},
		{"visio/document.xml", b.document()},
		{"visio/_rels/document.xml.rels", documentRels},
		{"visio/windows.xml", fmt.Sprintf(windowsXML, f(pw/2), f(ph/2))},
		{"visio/pages/pages.xml", fmt.Sprintf(pagesXML, f(pw), f(ph))},
		{"visio/pages/_rels/pages.xml.rels", pagesRels},
		{"visio/pages/page1.xml", page},
	}
	for _, p := range parts {
		fw, err := z.CreateHeader(&zip.FileHeader{Name: p.name, Method: zip.Deflate, Modified: time.Now()})
		if err != nil {
			return err
		}
		if _, err := io.WriteString(fw, `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>`+"\n"+p.body); err != nil {
			return err
		}
	}
	return z.Close()
}

type connPoints struct {
	pts []scene.Point // absolute scene coords, row IX = index
}

func (c *connPoints) add(p scene.Point) int {
	for i, q := range c.pts {
		if q.Dist(p) < 0.5 {
			return i
		}
	}
	c.pts = append(c.pts, p)
	return len(c.pts) - 1
}

type builder struct {
	s     *scene.Scene
	o     Options
	conn  map[string]*connPoints // shape key -> glue points
	fonts map[string]bool
	local *scene.Rect // when set, coordinates are relative to this group box
}

func (b *builder) pageSize() (w, h float64) {
	r := b.s.Bounds
	return r.W()*b.o.Scale + 2*b.o.Margin, r.H()*b.o.Scale + 2*b.o.Margin
}

// X/Y map scene coords to page inches (Visio's y axis points up).
func (b *builder) X(x float64) float64 {
	if b.local != nil {
		return (x - b.local.MinX) * b.o.Scale
	}
	return (x-b.s.Bounds.MinX)*b.o.Scale + b.o.Margin
}

func (b *builder) Y(y float64) float64 {
	if b.local != nil {
		return (b.local.MaxY - y) * b.o.Scale
	}
	return (b.s.Bounds.MaxY-y)*b.o.Scale + b.o.Margin
}
func (b *builder) L(v float64) float64 { return v * b.o.Scale }

func (b *builder) font(family string) string {
	name := b.o.SansFont
	switch family {
	case "mono":
		name = b.o.MonoFont
	case "serif":
		name = b.o.SerifFont
	}
	if b.fonts == nil {
		b.fonts = map[string]bool{}
	}
	b.fonts[name] = true
	return name
}

func f(v float64) string {
	if math.Abs(v) < 1e-9 {
		return "0"
	}
	s := fmt.Sprintf("%.6f", v)
	s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	return s
}

func esc(s string) string {
	var sb strings.Builder
	_ = xml.EscapeText(&sb, []byte(s))
	// EscapeText encodes \n as &#xA; — keep literal newlines for Visio text.
	return strings.ReplaceAll(sb.String(), "&#xA;", "\n")
}

func cell(sb *strings.Builder, n string, v string, extra ...string) {
	fmt.Fprintf(sb, `<Cell N="%s" V="%s"%s/>`, n, esc(v), strings.Join(extra, ""))
}

func (b *builder) page() string {
	var sb strings.Builder
	fmt.Fprintf(&sb, `<PageContents xmlns="%s" xmlns:r="%s" xml:space="preserve"><Shapes>`, nsMain, nsRel)

	// Glue points must be known before shapes are written.
	for _, c := range b.s.Connectors {
		for _, k := range []string{c.From, c.To} {
			if b.conn[k] == nil {
				b.conn[k] = &connPoints{}
			}
		}
	}
	type glue struct{ from, to int }
	glues := make([]glue, len(b.s.Connectors))
	shapeByKey := map[string]*scene.Shape{}
	for i := range b.s.Shapes {
		shapeByKey[b.s.Shapes[i].Key] = &b.s.Shapes[i]
	}
	for i, c := range b.s.Connectors {
		fs, ts := shapeByKey[c.From], shapeByKey[c.To]
		if fs == nil || ts == nil {
			glues[i] = glue{-1, -1}
			continue
		}
		glues[i] = glue{b.conn[c.From].add(snap(fs.Box, c.FromPt)), b.conn[c.To].add(snap(ts.Box, c.ToPt))}
	}

	id := 0
	ids := map[string]int{}
	for _, fr := range b.s.Frames {
		id++
		b.writeFrame(&sb, id, fr)
	}
	// group members, in scene (z-)order
	memberShapes := map[string][]scene.Shape{}
	memberTexts := map[string][]scene.Text{}
	for _, sh := range b.s.Shapes {
		if sh.Group != "" && sh.Group != sh.Key {
			memberShapes[sh.Group] = append(memberShapes[sh.Group], sh)
		}
	}
	for _, t := range b.s.Texts {
		if t.Group != "" {
			memberTexts[t.Group] = append(memberTexts[t.Group], t)
		}
	}
	for _, sh := range b.s.Shapes {
		switch {
		case sh.Group == "":
			id++
			ids[sh.Key] = id
			b.writeShape(&sb, id, sh, true)
		case sh.Group == sh.Key:
			id++
			ids[sh.Key] = id
			b.writeGroup(&sb, &id, sh, memberShapes[sh.Key], memberTexts[sh.Key])
		}
	}
	for _, t := range b.s.Texts {
		if t.Group == "" {
			id++
			b.writeText(&sb, id, t)
		}
	}
	type connect struct{ conn, from, fromIX, to, toIX int }
	var connects []connect
	for i, c := range b.s.Connectors {
		if glues[i].from < 0 {
			continue
		}
		fs, ts := shapeByKey[c.From], shapeByKey[c.To]
		id++
		b.writeConnector(&sb, id, c, snap(fs.Box, c.FromPt), snap(ts.Box, c.ToPt))
		connects = append(connects, connect{id, ids[c.From], glues[i].from, ids[c.To], glues[i].to})
	}
	sb.WriteString(`</Shapes><Connects>`)
	for _, c := range connects {
		fmt.Fprintf(&sb, `<Connect FromSheet="%d" FromCell="BeginX" FromPart="9" ToSheet="%d" ToCell="Connections.X%d" ToPart="%d"/>`,
			c.conn, c.from, c.fromIX+1, 100+c.fromIX)
		fmt.Fprintf(&sb, `<Connect FromSheet="%d" FromCell="EndX" FromPart="12" ToSheet="%d" ToCell="Connections.X%d" ToPart="%d"/>`,
			c.conn, c.to, c.toIX+1, 100+c.toIX)
	}
	sb.WriteString(`</Connects></PageContents>`)
	return sb.String()
}

// snap projects p onto the nearest side of box.
func snap(box scene.Rect, p scene.Point) scene.Point {
	x := math.Max(box.MinX, math.Min(box.MaxX, p.X))
	y := math.Max(box.MinY, math.Min(box.MaxY, p.Y))
	d := []float64{x - box.MinX, box.MaxX - x, y - box.MinY, box.MaxY - y}
	m := 0
	for i := range d {
		if d[i] < d[m] {
			m = i
		}
	}
	switch m {
	case 0:
		x = box.MinX
	case 1:
		x = box.MaxX
	case 2:
		y = box.MinY
	case 3:
		y = box.MaxY
	}
	return scene.Point{X: x, Y: y}
}

func (b *builder) xform(sb *strings.Builder, r scene.Rect) (w, h float64) {
	w, h = b.L(r.W()), b.L(r.H())
	cell(sb, "PinX", f(b.X(r.Center().X)))
	cell(sb, "PinY", f(b.Y(r.Center().Y)))
	cell(sb, "Width", f(w))
	cell(sb, "Height", f(h))
	cell(sb, "LocPinX", f(w/2), ` F="Width*0.5"`)
	cell(sb, "LocPinY", f(h/2), ` F="Height*0.5"`)
	cell(sb, "Angle", "0")
	return
}

func (b *builder) lineFill(sb *strings.Builder, fill, border string, bw float64) {
	if fill != "" {
		cell(sb, "FillForegnd", fill)
		cell(sb, "FillPattern", "1")
	} else {
		cell(sb, "FillPattern", "0")
	}
	if border != "" {
		cell(sb, "LineColor", border)
		cell(sb, "LinePattern", "1")
		cell(sb, "LineWeight", f(math.Max(b.L(bw), 0.25/72)), ` U="PT"`)
	} else {
		cell(sb, "LinePattern", "0")
	}
}

func margins(sb *strings.Builder, v float64) { marginsTB(sb, v, v) }

func marginsTB(sb *strings.Builder, v, top float64) {
	cell(sb, "LeftMargin", f(v), ` U="PT"`)
	cell(sb, "RightMargin", f(v), ` U="PT"`)
	cell(sb, "TopMargin", f(top), ` U="PT"`)
	cell(sb, "BottomMargin", f(v), ` U="PT"`)
}

// charRow writes a complete Character row. Every cell must be present:
// Visio does not inherit omitted cells of rows IX>=1 from the style sheet
// but treats them as 0, and FontScale=0 collapses the glyph advance to
// nothing (the run renders as an unreadable, ultra-condensed pile).
func charRow(sb *strings.Builder, ix int, font, color string, style int, size string) {
	fmt.Fprintf(sb, `<Row IX="%d">`, ix)
	cell(sb, "Font", font)
	cell(sb, "Color", color)
	cell(sb, "Style", fmt.Sprint(style))
	cell(sb, "Case", "0")
	cell(sb, "Pos", "0")
	cell(sb, "FontScale", "1")
	cell(sb, "Size", size, ` U="PT"`)
	cell(sb, "DblUnderline", "0")
	cell(sb, "Overline", "0")
	cell(sb, "Strikethru", "0")
	cell(sb, "DoubleStrikethrough", "0")
	cell(sb, "Letterspace", "0")
	cell(sb, "ColorTrans", "0")
	cell(sb, "AsianFont", "0")
	cell(sb, "ComplexScriptFont", "0")
	cell(sb, "ComplexScriptSize", "-1")
	cell(sb, "LangID", "en-US")
	cell(sb, "UseVertical", "0")
	sb.WriteString(`</Row>`)
}

// textBody writes Character/Paragraph sections and returns the <Text> element.
func (b *builder) textBody(sb *strings.Builder, lines []scene.Line, family string, size float64, color, alignH string, spLine float64) string {
	type style struct{ bold, italic, under bool }
	var styles []style
	// One Character row per run, in order of appearance: some readers
	// (libvisio) map runs to rows sequentially rather than by IX.
	idx := func(s style) int {
		if n := len(styles); n > 0 && styles[n-1] == s {
			return n - 1
		}
		styles = append(styles, s)
		return len(styles) - 1
	}
	var txt strings.Builder
	cur := -1
	for li, l := range lines {
		for _, sp := range l {
			i := idx(style{sp.Bold, sp.Italic, sp.Underline})
			if i != cur {
				fmt.Fprintf(&txt, `<cp IX="%d"/>`, i)
				cur = i
			}
			// Visio keeps literal spaces, so column padding needs no NBSP
			// (and some importers count styled runs in bytes, not chars).
			txt.WriteString(esc(strings.ReplaceAll(sp.Text, "\u00a0", " ")))
		}
		if li < len(lines)-1 {
			txt.WriteString("\n")
		}
	}
	if len(styles) == 0 {
		return ""
	}
	if size <= 0 {
		size = 12
	}
	fontName := b.font(family)
	sb.WriteString(`<Section N="Character">`)
	for i, st := range styles {
		bits := 0
		if st.bold {
			bits |= 1
		}
		if st.italic {
			bits |= 2
		}
		if st.under {
			bits |= 4
		}
		charRow(sb, i, fontName, orDef(color, "#000000"), bits, f(b.L(size)*b.o.FontScale))
	}
	sb.WriteString(`</Section><Section N="Paragraph"><Row IX="0">`)
	ha := "1"
	switch alignH {
	case "left":
		ha = "0"
	case "right":
		ha = "2"
	}
	cell(sb, "IndFirst", "0")
	cell(sb, "IndLeft", "0")
	cell(sb, "IndRight", "0")
	if spLine > 0 {
		cell(sb, "SpLine", f(spLine), ` U="PT"`) // absolute line pitch
	} else {
		cell(sb, "SpLine", "-1.2")
	}
	cell(sb, "SpBefore", "0")
	cell(sb, "SpAfter", "0")
	cell(sb, "HorzAlign", ha)
	cell(sb, "Bullet", "0")
	cell(sb, "BulletStr", "")
	cell(sb, "BulletFont", "0")
	cell(sb, "BulletFontSize", "-1")
	cell(sb, "TextPosAfterBullet", "0")
	cell(sb, "Flags", "0")
	sb.WriteString(`</Row></Section>`)
	return `<Text><pp IX="0"/>` + txt.String() + `</Text>`
}

func (b *builder) writeFrame(sb *strings.Builder, id int, fr scene.Frame) {
	fmt.Fprintf(sb, `<Shape ID="%d" NameU="Container.%d" Name="%s" Type="Shape" LineStyle="0" FillStyle="0" TextStyle="0">`, id, id, esc(fr.Title))
	b.xform(sb, fr.Box)
	b.lineFill(sb, fr.Fill, fr.Border, fr.BorderWidth)
	if fr.Dashed {
		cell(sb, "LinePattern", "2")
	}
	if fr.TitleBottom {
		cell(sb, "VerticalAlign", "2")
	} else {
		cell(sb, "VerticalAlign", "0")
	}
	margins(sb, b.L(4))
	sb.WriteString(`<Section N="User"><Row N="msvStructureType">`)
	cell(sb, "Value", "Container", ` U="STR"`)
	cell(sb, "Prompt", "", ` U="STR"`)
	sb.WriteString(`</Row></Section>`)
	size, fam := fr.TitleSize, fr.TitleFamily
	if size <= 0 {
		size, fam = 14, "sans"
	}
	text := b.textBody(sb, []scene.Line{{{Text: fr.Title}}}, fam, size, orDef(fr.TitleColor, "#000000"), "center", 0)
	b.rectGeometry(sb, fr.Box, "rectangle")
	sb.WriteString(text + `</Shape>`)
}

// writeGroup emits a node as a Visio group: its container shape, cell shapes
// and row texts as sub-shapes, with the glue points on the group itself.
func (b *builder) writeGroup(sb *strings.Builder, id *int, box scene.Shape, cells []scene.Shape, texts []scene.Text) {
	fmt.Fprintf(sb, `<Shape ID="%d" NameU="%s" Name="%s" Type="Group" LineStyle="0" FillStyle="0" TextStyle="0">`, *id, esc(nameOf(box.Key, *id)), esc(box.Key))
	b.xform(sb, box.Box)
	cell(sb, "LinePattern", "0")
	cell(sb, "FillPattern", "0")
	cell(sb, "SelectMode", "1") // click selects the whole node first
	if box.Tooltip != "" {
		cell(sb, "Comment", box.Tooltip)
	}
	b.writeConn(sb, box.Key, box.Box)
	sb.WriteString(`<Shapes>`)
	r := box.Box
	b.local = &r
	*id++
	b.writeShape(sb, *id, box, false)
	for _, c := range cells {
		*id++
		b.writeShape(sb, *id, c, false)
	}
	for _, t := range texts {
		*id++
		b.writeText(sb, *id, t)
	}
	b.local = nil
	sb.WriteString(`</Shapes></Shape>`)
}

func (b *builder) writeConn(sb *strings.Builder, key string, box scene.Rect) {
	cp := b.conn[key]
	if cp == nil || len(cp.pts) == 0 {
		return
	}
	sb.WriteString(`<Section N="Connection">`)
	for i, p := range cp.pts {
		fmt.Fprintf(sb, `<Row IX="%d">`, i)
		cell(sb, "X", f(b.L(p.X-box.MinX)))
		cell(sb, "Y", f(b.L(box.MaxY-p.Y)))
		cell(sb, "DirX", "0")
		cell(sb, "DirY", "0")
		cell(sb, "Type", "0")
		sb.WriteString(`</Row>`)
	}
	sb.WriteString(`</Section>`)
}

func (b *builder) writeShape(sb *strings.Builder, id int, sh scene.Shape, withConn bool) {
	fmt.Fprintf(sb, `<Shape ID="%d" NameU="%s" Name="%s" Type="Shape" LineStyle="0" FillStyle="0" TextStyle="0">`, id, esc(nameOf(sh.Key, id)), esc(sh.Key))
	b.xform(sb, sh.Box)
	b.lineFill(sb, sh.Fill, sh.Border, sh.BorderWidth)
	if sh.Kind == "round_rectangle" {
		cell(sb, "Rounding", f(math.Min(b.L(sh.Box.W()), b.L(sh.Box.H()))*0.15))
	}
	va := "1"
	switch sh.AlignV {
	case "top":
		va = "0"
	case "bottom":
		va = "2"
	}
	cell(sb, "VerticalAlign", va)
	if sh.Tooltip != "" {
		cell(sb, "Comment", sh.Tooltip)
	}
	// Record-style nodes: pitch text lines exactly on the drawing's rows so
	// glue points line up with the field they belong to.
	spLine := 0.0
	if sh.AlignV == "top" && len(sh.Lines) > 1 {
		pitch := b.L(sh.Box.H()) / float64(len(sh.Lines))
		spLine = pitch
		marginsTB(sb, 2.0/72, math.Max(0, pitch-b.L(sh.FontSize)*b.o.FontScale*1.2)/2)
	} else {
		margins(sb, 2.0/72)
	}
	text := b.textBody(sb, sh.Lines, sh.FontFamily, sh.FontSize, sh.TextColor, sh.AlignH, spLine)
	if withConn {
		b.writeConn(sb, sh.Key, sh.Box)
	}
	if len(sh.Path) > 0 {
		b.pathGeometry(sb, sh.Box, sh.Path, sh.Open)
	} else {
		b.rectGeometry(sb, sh.Box, sh.Kind)
	}
	sb.WriteString(text + `</Shape>`)
}

func (b *builder) writeText(sb *strings.Builder, id int, t scene.Text) {
	fmt.Fprintf(sb, `<Shape ID="%d" NameU="Text.%d" Name="Text.%d" Type="Shape" LineStyle="0" FillStyle="0" TextStyle="0">`, id, id, id)
	r := t.Box
	r.MaxX += r.W() * 0.15 // slack for font metric differences
	b.xform(sb, r)
	b.lineFill(sb, "", "", 0)
	margins(sb, 0)
	text := b.textBody(sb, []scene.Line{t.Line}, t.FontFamily, t.FontSize, t.Color, t.AlignH, 0)
	sb.WriteString(text + `</Shape>`)
}

func (b *builder) writeConnector(sb *strings.Builder, id int, c scene.Connector, from, to scene.Point) {
	bx, by, ex, ey := b.X(from.X), b.Y(from.Y), b.X(to.X), b.Y(to.Y)
	dx, dy := ex-bx, ey-by
	length := math.Hypot(dx, dy)
	fmt.Fprintf(sb, `<Shape ID="%d" NameU="Dynamic connector.%d" Name="%s" Type="Shape" LineStyle="0" FillStyle="0" TextStyle="0">`, id, id, esc(c.Key))
	cell(sb, "PinX", f((bx+ex)/2))
	cell(sb, "PinY", f((by+ey)/2))
	cell(sb, "Width", f(length))
	cell(sb, "Height", "0")
	cell(sb, "LocPinX", f(length/2), ` F="Width*0.5"`)
	cell(sb, "LocPinY", "0")
	cell(sb, "Angle", f(math.Atan2(dy, dx)))
	cell(sb, "BeginX", f(bx))
	cell(sb, "BeginY", f(by))
	cell(sb, "EndX", f(ex))
	cell(sb, "EndY", f(ey))
	cell(sb, "ObjType", "2")
	cell(sb, "OneD", "1")
	switch b.o.Routing {
	case "curved":
		cell(sb, "ConLineRouteExt", "2")
		cell(sb, "ShapeRouteStyle", "16")
	case "straight":
		cell(sb, "ConLineRouteExt", "1")
		cell(sb, "ShapeRouteStyle", "16")
	default: // keep the drawn path; don't let the app re-route it on open
		cell(sb, "ConLineRouteExt", "2")
		cell(sb, "ConFixedCode", "6")
	}
	cell(sb, "LineColor", orDef(c.Color, "#000000"))
	cell(sb, "LineWeight", f(math.Max(b.L(c.Width), 0.25/72)), ` U="PT"`)
	pat := "1"
	if c.Dashed {
		pat = "2"
	}
	cell(sb, "LinePattern", pat)
	arrow := func(on bool) string {
		if on {
			return "4"
		}
		return "0"
	}
	cell(sb, "BeginArrow", arrow(c.StartArrow))
	cell(sb, "EndArrow", arrow(c.EndArrow))
	// Visio arrow sizes are fixed steps; graphviz heads (~10pt) match "very small"/"small"
	asz := "0"
	if c.Width >= 2 {
		asz = "1"
	}
	cell(sb, "BeginArrowSize", asz)
	cell(sb, "EndArrowSize", asz)
	cell(sb, "FillPattern", "0")
	text := ""
	if c.Label != "" {
		text = b.textBody(sb, []scene.Line{{{Text: c.Label}}}, "sans", 10, "#000000", "center", 0)
	}
	sb.WriteString(`<Section N="Geometry" IX="0">`)
	cell(sb, "NoFill", "1")
	cell(sb, "NoLine", "0")
	// Geometry is in the connector's local frame: origin at Begin, x along
	// Begin->End.
	cos, sin := 1.0, 0.0
	if length > 0 {
		cos, sin = dx/length, dy/length
	}
	loc := func(px, py float64) (float64, float64) {
		ux, uy := px-bx, py-by
		return ux*cos + uy*sin, -ux*sin + uy*cos
	}
	pts := [][2]float64{{0, 0}}
	if b.o.Routing == "original" && len(c.Route) > 2 {
		for _, p := range c.Route[1 : len(c.Route)-1] {
			x, y := loc(b.X(p.X), b.Y(p.Y))
			pts = append(pts, [2]float64{x, y})
		}
	}
	pts = append(pts, [2]float64{length, 0})
	for i, p := range pts {
		t := "LineTo"
		if i == 0 {
			t = "MoveTo"
		}
		fmt.Fprintf(sb, `<Row T="%s" IX="%d">`, t, i+1)
		cell(sb, "X", f(p[0]))
		cell(sb, "Y", f(p[1]))
		sb.WriteString(`</Row>`)
	}
	sb.WriteString(`</Section>` + text + `</Shape>`)
}

// pathGeometry writes the exact outline: one Geometry section per subpath
// (Visio fills overlapping sections even-odd, so holes stay holes).
func (b *builder) pathGeometry(sb *strings.Builder, r scene.Rect, path [][]scene.Point, open bool) {
	for gi, sub := range path {
		if len(sub) < 2 {
			continue
		}
		fmt.Fprintf(sb, `<Section N="Geometry" IX="%d">`, gi)
		if open {
			cell(sb, "NoFill", "1")
		} else {
			cell(sb, "NoFill", "0")
		}
		cell(sb, "NoLine", "0")
		pts := sub
		if !open && sub[0].Dist(sub[len(sub)-1]) > 1e-6 {
			pts = append(append([]scene.Point{}, sub...), sub[0])
		}
		for i, p := range pts {
			t := "LineTo"
			if i == 0 {
				t = "MoveTo"
			}
			fmt.Fprintf(sb, `<Row T="%s" IX="%d">`, t, i+1)
			cell(sb, "X", f(b.L(p.X-r.MinX)))
			cell(sb, "Y", f(b.L(r.MaxY-p.Y)))
			sb.WriteString(`</Row>`)
		}
		sb.WriteString(`</Section>`)
	}
}

// rectGeometry writes the outline for a Miro/Visio shape kind in local coords.
func (b *builder) rectGeometry(sb *strings.Builder, r scene.Rect, kind string) {
	w, h := b.L(r.W()), b.L(r.H())
	sb.WriteString(`<Section N="Geometry" IX="0">`)
	cell(sb, "NoFill", "0")
	cell(sb, "NoLine", "0")
	if kind == "circle" {
		sb.WriteString(`<Row T="Ellipse" IX="1">`)
		cell(sb, "X", f(w/2), ` F="Width*0.5"`)
		cell(sb, "Y", f(h/2), ` F="Height*0.5"`)
		cell(sb, "A", f(w), ` F="Width*1"`)
		cell(sb, "B", f(h/2), ` F="Height*0.5"`)
		cell(sb, "C", f(w/2), ` F="Width*0.5"`)
		cell(sb, "D", f(h), ` F="Height*1"`)
		sb.WriteString(`</Row></Section>`)
		return
	}
	var pts [][2]float64 // fractions of width/height
	switch kind {
	case "rhombus":
		pts = [][2]float64{{0.5, 0}, {1, 0.5}, {0.5, 1}, {0, 0.5}}
	case "triangle":
		pts = [][2]float64{{0, 0}, {1, 0}, {0.5, 1}}
	case "parallelogram":
		pts = [][2]float64{{0, 0}, {0.8, 0}, {1, 1}, {0.2, 1}}
	case "pentagon", "hexagon", "octagon":
		n := map[string]int{"pentagon": 5, "hexagon": 6, "octagon": 8}[kind]
		for i := 0; i < n; i++ {
			a := math.Pi/2 + 2*math.Pi*float64(i)/float64(n)
			pts = append(pts, [2]float64{0.5 + 0.5*math.Cos(a), 0.5 + 0.5*math.Sin(a)})
		}
	default:
		pts = [][2]float64{{0, 0}, {1, 0}, {1, 1}, {0, 1}}
	}
	pts = append(pts, pts[0])
	for i, p := range pts {
		t := "LineTo"
		if i == 0 {
			t = "MoveTo"
		}
		fmt.Fprintf(sb, `<Row T="%s" IX="%d">`, t, i+1)
		cell(sb, "X", f(p[0]*w), fmt.Sprintf(` F="Width*%s"`, f(p[0])))
		cell(sb, "Y", f(p[1]*h), fmt.Sprintf(` F="Height*%s"`, f(p[1])))
		sb.WriteString(`</Row>`)
	}
	sb.WriteString(`</Section>`)
}

func (b *builder) document() string {
	var sb strings.Builder
	fmt.Fprintf(&sb, `<VisioDocument xmlns="%s" xmlns:r="%s" xml:space="preserve">`, nsMain, nsRel)
	sb.WriteString(`<DocumentSettings TopPage="0" DefaultTextStyle="0" DefaultLineStyle="0" DefaultFillStyle="0" DefaultGuideStyle="0">`)
	sb.WriteString(`<GlueSettings>9</GlueSettings><SnapSettings>65847</SnapSettings><SnapExtensions>34</SnapExtensions><DynamicGridEnabled>0</DynamicGridEnabled><ProtectStyles>0</ProtectStyles><ProtectShapes>0</ProtectShapes><ProtectMasters>0</ProtectMasters><ProtectBkgnds>0</ProtectBkgnds></DocumentSettings>`)
	sb.WriteString(`<Colors><ColorEntry IX="0" RGB="#000000"/><ColorEntry IX="1" RGB="#FFFFFF"/></Colors>`)
	sb.WriteString(`<FaceNames>`)
	names := []string{}
	for n := range b.fonts {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		fmt.Fprintf(&sb, `<FaceName NameU="%s" UnicodeRanges="-536859905 -1073711037 9 0" CharSets="1073742335 -65536" Panos="2 11 6 4 2 2 2 2 2 4" Flags="325"/>`, esc(n))
	}
	sb.WriteString(`</FaceNames><StyleSheets><StyleSheet ID="0" NameU="No Style" Name="No Style">`)
	for _, kv := range [][2]string{
		{"EnableLineProps", "1"}, {"EnableFillProps", "1"}, {"EnableTextProps", "1"}, {"HideForApply", "0"},
		{"LineWeight", "0.01041666666666667"}, {"LineColor", "#000000"}, {"LinePattern", "1"}, {"Rounding", "0"},
		{"BeginArrow", "0"}, {"EndArrow", "0"}, {"LineCap", "0"}, {"BeginArrowSize", "2"}, {"EndArrowSize", "2"},
		{"LineColorTrans", "0"}, {"FillForegnd", "#FFFFFF"}, {"FillBkgnd", "#FFFFFF"}, {"FillPattern", "1"},
		{"FillForegndTrans", "0"}, {"FillBkgndTrans", "0"}, {"ShdwPattern", "0"},
		{"LeftMargin", "0"}, {"RightMargin", "0"}, {"TopMargin", "0"}, {"BottomMargin", "0"},
		{"VerticalAlign", "1"}, {"TextBkgnd", "0"}, {"DefaultTabStop", "0.5"}, {"TextDirection", "0"},
	} {
		cell(&sb, kv[0], kv[1])
	}
	sb.WriteString(`<Section N="Character">`)
	charRow(&sb, 0, b.o.SansFont, "#000000", 0, "0.1666666666666667")
	sb.WriteString(`</Section>`)
	sb.WriteString(`<Section N="Paragraph"><Row IX="0"><Cell N="IndFirst" V="0"/><Cell N="IndLeft" V="0"/><Cell N="IndRight" V="0"/><Cell N="SpLine" V="-1.2"/><Cell N="SpBefore" V="0"/><Cell N="SpAfter" V="0"/><Cell N="HorzAlign" V="1"/></Row></Section>`)
	sb.WriteString(`</StyleSheet></StyleSheets></VisioDocument>`)
	return sb.String()
}

func nameOf(key string, id int) string {
	if key == "" {
		return fmt.Sprintf("Shape.%d", id)
	}
	return key
}

func orDef(v, d string) string {
	if v == "" {
		return d
	}
	return v
}

const contentTypes = `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">` +
	`<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>` +
	`<Default Extension="xml" ContentType="application/xml"/>` +
	`<Override PartName="/visio/document.xml" ContentType="application/vnd.ms-visio.drawing.main+xml"/>` +
	`<Override PartName="/visio/pages/pages.xml" ContentType="application/vnd.ms-visio.pages+xml"/>` +
	`<Override PartName="/visio/pages/page1.xml" ContentType="application/vnd.ms-visio.page+xml"/>` +
	`<Override PartName="/visio/windows.xml" ContentType="application/vnd.ms-visio.windows+xml"/>` +
	`<Override PartName="/docProps/core.xml" ContentType="application/vnd.openxmlformats-package.core-properties+xml"/>` +
	`<Override PartName="/docProps/app.xml" ContentType="application/vnd.openxmlformats-officedocument.extended-properties+xml"/>` +
	`</Types>`

const rootRels = `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
	`<Relationship Id="rId1" Type="http://schemas.microsoft.com/visio/2010/relationships/document" Target="visio/document.xml"/>` +
	`<Relationship Id="rId2" Type="http://schemas.openxmlformats.org/package/2006/relationships/metadata/core-properties" Target="docProps/core.xml"/>` +
	`<Relationship Id="rId3" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/extended-properties" Target="docProps/app.xml"/>` +
	`</Relationships>`

const documentRels = `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
	`<Relationship Id="rId1" Type="http://schemas.microsoft.com/visio/2010/relationships/pages" Target="pages/pages.xml"/>` +
	`<Relationship Id="rId2" Type="http://schemas.microsoft.com/visio/2010/relationships/windows" Target="windows.xml"/>` +
	`</Relationships>`

const pagesRels = `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
	`<Relationship Id="rId1" Type="http://schemas.microsoft.com/visio/2010/relationships/page" Target="page1.xml"/>` +
	`</Relationships>`

const pagesXML = `<Pages xmlns="` + nsMain + `" xmlns:r="` + nsRel + `" xml:space="preserve">` +
	`<Page ID="0" NameU="Page-1" Name="Page-1" ViewScale="-1" ViewCenterX="0" ViewCenterY="0"><PageSheet LineStyle="0" FillStyle="0" TextStyle="0">` +
	`<Cell N="PageWidth" V="%s"/><Cell N="PageHeight" V="%s"/><Cell N="PageScale" V="1" U="IN"/><Cell N="DrawingScale" V="1" U="IN"/>` +
	`<Cell N="DrawingSizeType" V="0"/><Cell N="DrawingScaleType" V="0"/><Cell N="InhibitSnap" V="0"/><Cell N="PageLockReplace" V="0" U="BOOL"/>` +
	`</PageSheet><Rel r:id="rId1"/></Page></Pages>`

const windowsXML = `<Windows ClientWidth="1600" ClientHeight="900" xmlns="` + nsMain + `" xmlns:r="` + nsRel + `" xml:space="preserve">` +
	`<Window ID="0" WindowType="Drawing" WindowState="1073741824" WindowLeft="0" WindowTop="0" WindowWidth="1600" WindowHeight="900" ContainerType="Page" Page="0" ViewScale="-1" ViewCenterX="%s" ViewCenterY="%s">` +
	`<ShowRulers>1</ShowRulers><ShowGrid>0</ShowGrid><ShowPageBreaks>0</ShowPageBreaks><ShowGuides>1</ShowGuides><ShowConnectionPoints>0</ShowConnectionPoints><GlueSettings>9</GlueSettings><SnapSettings>65847</SnapSettings><SnapExtensions>34</SnapExtensions><DynamicGridEnabled>0</DynamicGridEnabled>` +
	`</Window></Windows>`

const appXML = `<Properties xmlns="http://schemas.openxmlformats.org/officeDocument/2006/extended-properties" xmlns:vt="http://schemas.openxmlformats.org/officeDocument/2006/docPropsVTypes">` +
	`<Application>svg2miro</Application><Template></Template><AppVersion>15.0000</AppVersion></Properties>`

const coreXML = `<cp:coreProperties xmlns:cp="http://schemas.openxmlformats.org/package/2006/metadata/core-properties" xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:dcterms="http://purl.org/dc/terms/" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance">` +
	`<dc:title>%s</dc:title><dc:creator>svg2miro</dc:creator><dcterms:created xsi:type="dcterms:W3CDTF">%s</dcterms:created></cp:coreProperties>`
