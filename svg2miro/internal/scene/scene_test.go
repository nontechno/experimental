package scene

import (
	"os"
	"strings"
	"testing"

	"github.com/seamia/svg2miro/internal/svgdoc"
)

func load(t *testing.T, opt Options) *Scene {
	t.Helper()
	f, err := os.Open("../../testdata/protodot.svg")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	root, err := svgdoc.Parse(f)
	if err != nil {
		t.Fatal(err)
	}
	return FromSVG(root, opt)
}

func TestProtodotCompact(t *testing.T) {
	s := load(t, Options{})
	if s.Source != "graphviz" {
		t.Fatalf("source = %s", s.Source)
	}
	t.Logf("frames=%d shapes=%d texts=%d connectors=%d warnings=%v bounds=%+v",
		len(s.Frames), len(s.Shapes), len(s.Texts), len(s.Connectors), s.Warnings, s.Bounds)
	if len(s.Frames) != 5 || len(s.Shapes) != 150 || len(s.Connectors) != 425 {
		t.Errorf("unexpected counts")
	}
	if len(s.Warnings) != 0 {
		t.Errorf("warnings: %v", s.Warnings)
	}
	sh := s.Shape("Node_Ja_153")
	for _, l := range sh.Lines {
		t.Logf("%q", l.Plain())
	}
}

func TestProtodotEdgesAttachAtRecordRows(t *testing.T) {
	s := load(t, Options{})
	byTitle := map[string]Connector{}
	for _, c := range s.Connectors {
		byTitle[c.From+"->"+c.To] = c
	}
	// ListValue.values (second row) -> Value header
	c := byTitle["Node_Ja_103->Node_Ja_101"]
	from, to := s.Shape(c.From), s.Shape(c.To)
	if fy := (c.FromPt.Y - from.Box.MinY) / from.Box.H(); fy < 0.6 || fy > 0.9 {
		t.Errorf("start should be on the lower row, got %.2f", fy)
	}
	if c.FromPt.X != from.Box.MaxX || !c.EndArrow {
		t.Errorf("start %v not on right edge %v / arrow=%v", c.FromPt, from.Box.MaxX, c.EndArrow)
	}
	if fy := (c.ToPt.Y - to.Box.MinY) / to.Box.H(); fy > 0.15 {
		t.Errorf("end should be at header, got %.2f", fy)
	}
	// enum references are grey
	if e := byTitle["Node_Ja_101->Node_Ja_102"]; e.Color != "#737373" {
		t.Errorf("enum edge color %s", e.Color)
	}
}

func TestProtodotDetailed(t *testing.T) {
	s := load(t, Options{Detailed: true})
	cells := 0
	for _, sh := range s.Shapes {
		if !sh.ConnectTarget {
			cells++
		}
	}
	if cells == 0 || len(s.Connectors) != 425 {
		t.Fatalf("cells=%d connectors=%d", cells, len(s.Connectors))
	}
	for _, c := range s.Connectors {
		if !s.Shape(c.From).ConnectTarget || !s.Shape(c.To).ConnectTarget {
			t.Fatalf("connector %s attaches to a cell", c.Key)
		}
	}
}

func TestGenericSVG(t *testing.T) {
	doc := `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 400 200">
	  <defs><marker id="m"><path d="M0,0 L5,5"/></marker></defs>
	  <rect id="a" x="10" y="10" width="100" height="50" rx="6" fill="#ffeeaa" stroke="#333"/>
	  <text x="60" y="40" text-anchor="middle" font-size="14">Start</text>
	  <circle id="b" cx="300" cy="35" r="30" fill="lightblue"/>
	  <text x="300" y="40" text-anchor="middle">End</text>
	  <line x1="110" y1="35" x2="270" y2="35" stroke="black" marker-end="url(#m)"/>
	  <path d="M0 150 Q 50 100 100 150" stroke="red" fill="none"/>
	  <text x="10" y="190">caption</text>
	</svg>`
	root, err := svgdoc.Parse(strings.NewReader(doc))
	if err != nil {
		t.Fatal(err)
	}
	s := FromSVG(root, Options{})
	if s.Source != "generic" || len(s.Shapes) != 2 || len(s.Connectors) != 1 || len(s.Texts) != 1 {
		t.Fatalf("got %s shapes=%d conns=%d texts=%d", s.Source, len(s.Shapes), len(s.Connectors), len(s.Texts))
	}
	a, b := s.Shape("a"), s.Shape("b")
	if a.Kind != "round_rectangle" || a.Lines[0].Plain() != "Start" || b.Kind != "circle" || b.Fill != "#add8e6" {
		t.Errorf("shapes: %+v %+v", a, b)
	}
	if c := s.Connectors[0]; c.From != "a" || c.To != "b" || !c.EndArrow {
		t.Errorf("connector: %+v", c)
	}
	if len(s.Warnings) != 1 {
		t.Errorf("warnings: %v", s.Warnings)
	}
}
