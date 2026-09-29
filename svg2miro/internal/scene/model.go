// Package scene holds a backend-neutral description of a whiteboard diagram
// (frames, shapes, texts, connectors) and converters that build it from SVG.
package scene

import (
	"github.com/seamia/svg2miro/internal/svgdoc"
)

type (
	Point = svgdoc.Point
	Rect  = svgdoc.Rect
)

// Span is a styled run of text inside a line.
type Span struct {
	Text      string
	Bold      bool
	Italic    bool
	Underline bool
}

// Line is one paragraph of text.
type Line []Span

// Plain returns the unstyled text of the line.
func (l Line) Plain() string {
	s := ""
	for _, sp := range l {
		s += sp.Text
	}
	return s
}

// Frame is a titled container (Graphviz cluster).
type Frame struct {
	Key   string
	Title string
	Box   Rect
	Fill  string
}

// Shape is a filled geometric item, optionally carrying text.
type Shape struct {
	Key           string
	Kind          string // Miro shape name: rectangle, round_rectangle, circle, triangle, rhombus, ...
	Box           Rect
	Fill          string // "" = transparent
	Border        string // "" = no border
	BorderWidth   float64
	Lines         []Line
	FontSize      float64
	FontFamily    string // "mono" | "sans" | "serif"
	TextColor     string
	AlignH        string // left | center | right
	AlignV        string // top | middle | bottom
	Tooltip       string
	ConnectTarget bool // connectors may attach here
}

// Text is a free-standing text item.
type Text struct {
	Key        string
	Box        Rect
	Line       Line
	FontSize   float64
	FontFamily string
	Color      string
	AlignH     string
}

// Connector links two shapes. From/To points are absolute attach points
// (they are converted to relative positions by the publisher).
type Connector struct {
	Key        string
	From, To   string // Shape keys
	FromPt     Point
	ToPt       Point
	Route      []Point // original path (preview only)
	Color      string
	Width      float64
	StartArrow bool
	EndArrow   bool
	Dashed     bool
	Label      string
}

// Scene is the full converted diagram in SVG user units.
type Scene struct {
	Source     string // "graphviz" | "generic"
	Bounds     Rect
	Frames     []Frame
	Shapes     []Shape
	Texts      []Text
	Connectors []Connector
	Warnings   []string
}

// Shape returns the shape with key k.
func (s *Scene) Shape(k string) *Shape {
	for i := range s.Shapes {
		if s.Shapes[i].Key == k {
			return &s.Shapes[i]
		}
	}
	return nil
}

func (s *Scene) computeBounds() {
	b := svgdoc.EmptyRect()
	for _, f := range s.Frames {
		b = b.Union(f.Box)
	}
	for _, sh := range s.Shapes {
		b = b.Union(sh.Box)
	}
	for _, t := range s.Texts {
		b = b.Union(t.Box)
	}
	if b.Empty() {
		b = Rect{}
	}
	s.Bounds = b
}

// Options control conversion.
type Options struct {
	// Detailed renders every filled cell of a record node as its own
	// shape (visually faithful, many more items). Compact (default) makes
	// one shape per node with the rows as text lines.
	Detailed bool
}

// FromSVG converts a parsed SVG into a scene, using Graphviz-aware mapping
// when the document looks like Graphviz output.
func FromSVG(root *svgdoc.Node, opt Options) *Scene {
	var s *Scene
	if IsGraphviz(root) {
		s = fromGraphviz(root, opt)
	} else {
		s = fromGeneric(root)
	}
	s.computeBounds()
	return s
}
