package miro

import (
	"fmt"
	"html"
	"math"
	"strings"

	"github.com/seamia/svg2miro/internal/scene"
)

// Layout maps scene (SVG user units) coordinates and styles onto the board.
type Layout struct {
	Scale          float64 // board units per SVG unit
	FontScale      float64 // extra multiplier for font sizes
	OriginX        float64 // board position of the scene's top-left corner
	OriginY        float64
	MonoFont       string // Miro fontFamily for monospace text
	SansFont       string
	SerifFont      string
	ConnectorShape string // straight | elbowed | curved

	bounds scene.Rect
}

// DefaultLayout returns sensible defaults.
func DefaultLayout() Layout {
	return Layout{
		Scale: 2, FontScale: 0.85, MonoFont: "roboto_mono", SansFont: "arial",
		SerifFont: "noto_serif", ConnectorShape: "curved",
	}
}

// Bind fixes the scene bounds used as the coordinate origin.
func (l *Layout) Bind(s *scene.Scene) { l.bounds = s.Bounds }

// Point maps an absolute scene point to board coordinates.
func (l Layout) Point(p scene.Point) (float64, float64) {
	return round2((p.X-l.bounds.MinX)*l.Scale + l.OriginX), round2((p.Y-l.bounds.MinY)*l.Scale + l.OriginY)
}

// Box returns board center and size of a scene rect.
func (l Layout) Box(r scene.Rect) (cx, cy, w, h float64) {
	cx, cy = l.Point(r.Center())
	return cx, cy, round2(math.Max(r.W()*l.Scale, 8)), round2(math.Max(r.H()*l.Scale, 8))
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }

func (l Layout) font(family string) string {
	switch family {
	case "mono":
		return l.MonoFont
	case "serif":
		return l.SerifFont
	}
	return l.SansFont
}

func (l Layout) fontSize(size float64) string {
	if size <= 0 {
		size = 14
	}
	v := math.Round(size * l.Scale * l.FontScale)
	return fmt.Sprintf("%d", int(math.Max(10, math.Min(288, v))))
}

// HTML renders lines as Miro rich-text content.
func HTML(lines []scene.Line) string {
	var b strings.Builder
	for _, l := range lines {
		b.WriteString("<p>")
		for _, sp := range l {
			t := html.EscapeString(sp.Text)
			t = strings.ReplaceAll(t, " ", "&nbsp;")
			if sp.Underline {
				t = "<u>" + t + "</u>"
			}
			if sp.Italic {
				t = "<em>" + t + "</em>"
			}
			if sp.Bold {
				t = "<strong>" + t + "</strong>"
			}
			b.WriteString(t)
		}
		b.WriteString("</p>")
	}
	return b.String()
}

// ---- API payloads ----

type Position struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Origin string  `json:"origin,omitempty"`
}

type Geometry struct {
	Width  float64 `json:"width,omitempty"`
	Height float64 `json:"height,omitempty"`
}

type Parent struct {
	ID string `json:"id"`
}

// Item is the create payload for frames, shapes and texts. Type is only
// sent to the bulk endpoint.
type Item struct {
	Type     string         `json:"type,omitempty"`
	Data     map[string]any `json:"data"`
	Style    map[string]any `json:"style,omitempty"`
	Position Position       `json:"position"`
	Geometry *Geometry      `json:"geometry,omitempty"`
	Parent   *Parent        `json:"parent,omitempty"`
}

// FramePayload builds a frame item.
func (l Layout) FramePayload(f scene.Frame) Item {
	cx, cy, w, h := l.Box(f.Box)
	it := Item{
		Type:     "frame",
		Data:     map[string]any{"title": f.Title, "format": "custom", "type": "freeform", "showContent": true},
		Position: Position{cx, cy, "center"},
		Geometry: &Geometry{w, h},
	}
	if f.Fill != "" {
		it.Style = map[string]any{"fillColor": f.Fill}
	}
	return it
}

// ShapePayload builds a shape item.
func (l Layout) ShapePayload(sh scene.Shape) Item {
	cx, cy, w, h := l.Box(sh.Box)
	style := map[string]any{
		"fillColor":         "#ffffff",
		"fillOpacity":       "1.0",
		"borderColor":       "#1a1a1a",
		"borderOpacity":     "1.0",
		"borderWidth":       "1.0",
		"borderStyle":       "normal",
		"fontFamily":        l.font(sh.FontFamily),
		"fontSize":          l.fontSize(sh.FontSize),
		"color":             orDefault(sh.TextColor, "#1a1a1a"),
		"textAlign":         orDefault(sh.AlignH, "center"),
		"textAlignVertical": orDefault(sh.AlignV, "middle"),
	}
	if sh.Fill != "" {
		style["fillColor"] = sh.Fill
	} else {
		style["fillOpacity"] = "0.0"
	}
	if sh.Border != "" {
		style["borderColor"] = sh.Border
		style["borderWidth"] = fmt.Sprintf("%.1f", math.Max(1, math.Min(24, sh.BorderWidth*l.Scale)))
	} else {
		style["borderOpacity"] = "0.0"
	}
	return Item{
		Type:     "shape",
		Data:     map[string]any{"shape": orDefault(sh.Kind, "rectangle"), "content": HTML(sh.Lines)},
		Style:    style,
		Position: Position{cx, cy, "center"},
		Geometry: &Geometry{w, h},
	}
}

// TextPayload builds a text item.
func (l Layout) TextPayload(t scene.Text) Item {
	cx, cy, w, _ := l.Box(t.Box)
	return Item{
		Type: "text",
		Data: map[string]any{"content": HTML([]scene.Line{t.Line})},
		Style: map[string]any{
			"color":      orDefault(t.Color, "#1a1a1a"),
			"fontFamily": l.font(t.FontFamily),
			"fontSize":   l.fontSize(t.FontSize),
			"textAlign":  orDefault(t.AlignH, "left"),
		},
		Position: Position{cx, cy, "center"},
		Geometry: &Geometry{Width: math.Max(w*1.15, 20)}, // slack for font metric differences
	}
}

// Attach converts an absolute point into a relative attach position on the
// nearest border of box, formatted the way the connector API expects.
func Attach(box scene.Rect, p scene.Point) (x, y string, px, py float64) {
	fx := clamp01((p.X - box.MinX) / nz(box.W()))
	fy := clamp01((p.Y - box.MinY) / nz(box.H()))
	// snap to the closest side so the connector starts on the outline
	d := []float64{fx, 1 - fx, fy, 1 - fy}
	m := 0
	for i := range d {
		if d[i] < d[m] {
			m = i
		}
	}
	switch m {
	case 0:
		fx = 0
	case 1:
		fx = 1
	case 2:
		fy = 0
	case 3:
		fy = 1
	}
	return fmt.Sprintf("%.0f%%", fx*100), fmt.Sprintf("%.0f%%", fy*100), fx, fy
}

type Endpoint struct {
	ID       string            `json:"id"`
	Position map[string]string `json:"position,omitempty"`
	SnapTo   string            `json:"snapTo,omitempty"`
}

type ConnectorPayload struct {
	StartItem Endpoint          `json:"startItem"`
	EndItem   Endpoint          `json:"endItem"`
	Shape     string            `json:"shape"`
	Style     map[string]string `json:"style"`
	Captions  []map[string]any  `json:"captions,omitempty"`
}

// ConnectorPayloadFor builds a connector between two created items.
func (l Layout) ConnectorPayloadFor(c scene.Connector, from, to scene.Shape, fromID, toID string) ConnectorPayload {
	sx, sy, _, _ := Attach(from.Box, c.FromPt)
	ex, ey, _, _ := Attach(to.Box, c.ToPt)
	capOf := func(b bool) string {
		if b {
			return "arrow"
		}
		return "none"
	}
	stroke := "normal"
	if c.Dashed {
		stroke = "dashed"
	}
	p := ConnectorPayload{
		StartItem: Endpoint{ID: fromID, Position: map[string]string{"x": sx, "y": sy}},
		EndItem:   Endpoint{ID: toID, Position: map[string]string{"x": ex, "y": ey}},
		Shape:     l.ConnectorShape,
		Style: map[string]string{
			"strokeColor":    orDefault(c.Color, "#1a1a1a"),
			"strokeWidth":    fmt.Sprintf("%.1f", math.Max(1, math.Min(24, c.Width*l.Scale/2))),
			"strokeStyle":    stroke,
			"startStrokeCap": capOf(c.StartArrow),
			"endStrokeCap":   capOf(c.EndArrow),
		},
	}
	if c.Label != "" {
		p.Captions = []map[string]any{{"content": html.EscapeString(c.Label)}}
	}
	return p
}

func orDefault(v, d string) string {
	if v == "" {
		return d
	}
	return v
}

func clamp01(v float64) float64 { return math.Max(0, math.Min(1, v)) }

func nz(v float64) float64 {
	if v == 0 {
		return 1
	}
	return v
}
