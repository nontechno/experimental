package svgdoc

import (
	"math"
	"strings"
	"testing"
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-6 }

func TestTransformChain(t *testing.T) {
	m := ParseTransform("scale(1 1) rotate(0) translate(4 14248.5)")
	p := m.Apply(Point{244, -13460.5})
	if !near(p.X, 248) || !near(p.Y, 788) {
		t.Fatalf("got %v", p)
	}
	r := ParseTransform("rotate(90 10 10)").Apply(Point{20, 10})
	if !near(r.X, 10) || !near(r.Y, 20) {
		t.Fatalf("rotate about center: %v", r)
	}
}

func TestParsePathEndpoints(t *testing.T) {
	pts, closed := ParsePath("M392,-13469.5C579.62,-13469.5 623.37,-13537.96 805.79,-13540.43")
	if closed || len(pts) < 9 {
		t.Fatalf("pts=%d closed=%v", len(pts), closed)
	}
	if pts[0] != (Point{392, -13469.5}) || !near(pts[len(pts)-1].X, 805.79) || !near(pts[len(pts)-1].Y, -13540.43) {
		t.Fatalf("endpoints %v %v", pts[0], pts[len(pts)-1])
	}
	rel, closed := ParsePath("m10 10 h 20 v 5 l -20 0 z")
	if !closed || rel[len(rel)-1] != (Point{10, 15}) {
		t.Fatalf("relative path: %v closed=%v", rel, closed)
	}
}

func TestParseTextRunsAndInheritance(t *testing.T) {
	doc := `<svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink">
	<g font-family="monospace" font-size="12" transform="translate(10 20)">
	  <a xlink:title="tip"><text x="1" y="2" font-weight="bold">A&amp;B</text></a>
	  <text x="0" y="0"><tspan x="5" y="6" font-style="italic">it</tspan><tspan dx="3">x</tspan></text>
	</g></svg>`
	root, err := Parse(strings.NewReader(doc))
	if err != nil {
		t.Fatal(err)
	}
	texts := root.Find(func(n *Node) bool { return n.Name == "text" })
	r := texts[0].TextRuns()
	if len(r) != 1 || r[0].Text != "A&B" || !r[0].Bold || !r[0].Mono() || r[0].Pos != (Point{11, 22}) || r[0].Size != 12 {
		t.Fatalf("run: %+v", r)
	}
	if texts[0].Parent.Attr["xlink:title"] != "tip" {
		t.Fatalf("xlink attr: %v", texts[0].Parent.Attr)
	}
	r = texts[1].TextRuns()
	if len(r) != 2 || !r[0].Italic || r[1].Pos != (Point{18, 26}) {
		t.Fatalf("tspans: %+v", r)
	}
}

func TestColor(t *testing.T) {
	for in, want := range map[string]string{"#ABC": "#aabbcc", "black": "#000000", "rgb(255, 0, 16)": "#ff0010", "#737373": "#737373"} {
		if got, ok := Color(in); !ok || got != want {
			t.Errorf("Color(%q)=%q,%v", in, got, ok)
		}
	}
	for _, in := range []string{"none", "transparent", "url(#g)", ""} {
		if _, ok := Color(in); ok {
			t.Errorf("Color(%q) should be unset", in)
		}
	}
}
