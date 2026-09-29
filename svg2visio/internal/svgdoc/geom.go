package svgdoc

import (
	"math"
	"regexp"
	"strconv"
	"strings"
)

// Point is a 2D point.
type Point struct{ X, Y float64 }

// Dist returns the euclidean distance between p and q.
func (p Point) Dist(q Point) float64 { return math.Hypot(p.X-q.X, p.Y-q.Y) }

// Rect is an axis-aligned bounding box.
type Rect struct{ MinX, MinY, MaxX, MaxY float64 }

// EmptyRect returns a rect that any Add will replace.
func EmptyRect() Rect {
	return Rect{math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)}
}

func (r Rect) Empty() bool   { return r.MinX > r.MaxX || r.MinY > r.MaxY }
func (r Rect) W() float64    { return r.MaxX - r.MinX }
func (r Rect) H() float64    { return r.MaxY - r.MinY }
func (r Rect) Area() float64 { return r.W() * r.H() }
func (r Rect) Center() Point { return Point{(r.MinX + r.MaxX) / 2, (r.MinY + r.MaxY) / 2} }
func (r Rect) Add(p Point) Rect {
	return Rect{math.Min(r.MinX, p.X), math.Min(r.MinY, p.Y), math.Max(r.MaxX, p.X), math.Max(r.MaxY, p.Y)}
}

// Union merges two rects.
func (r Rect) Union(o Rect) Rect {
	if o.Empty() {
		return r
	}
	if r.Empty() {
		return o
	}
	return Rect{math.Min(r.MinX, o.MinX), math.Min(r.MinY, o.MinY), math.Max(r.MaxX, o.MaxX), math.Max(r.MaxY, o.MaxY)}
}

// Contains reports whether p lies within r grown by tol.
func (r Rect) Contains(p Point, tol float64) bool {
	return p.X >= r.MinX-tol && p.X <= r.MaxX+tol && p.Y >= r.MinY-tol && p.Y <= r.MaxY+tol
}

// ContainsRect reports whether o lies within r grown by tol.
func (r Rect) ContainsRect(o Rect, tol float64) bool {
	return o.MinX >= r.MinX-tol && o.MaxX <= r.MaxX+tol && o.MinY >= r.MinY-tol && o.MaxY <= r.MaxY+tol
}

// BoundsOf returns the bounding box of pts.
func BoundsOf(pts []Point) Rect {
	r := EmptyRect()
	for _, p := range pts {
		r = r.Add(p)
	}
	return r
}

// Matrix is an affine transform [a c e; b d f; 0 0 1].
type Matrix struct{ A, B, C, D, E, F float64 }

func Identity() Matrix { return Matrix{A: 1, D: 1} }

// Mul returns m*n (n is applied first).
func (m Matrix) Mul(n Matrix) Matrix {
	return Matrix{
		A: m.A*n.A + m.C*n.B,
		B: m.B*n.A + m.D*n.B,
		C: m.A*n.C + m.C*n.D,
		D: m.B*n.C + m.D*n.D,
		E: m.A*n.E + m.C*n.F + m.E,
		F: m.B*n.E + m.D*n.F + m.F,
	}
}

// Apply transforms a point.
func (m Matrix) Apply(p Point) Point {
	return Point{m.A*p.X + m.C*p.Y + m.E, m.B*p.X + m.D*p.Y + m.F}
}

// ScaleFactor is the geometric-mean scale (used for font sizes / strokes).
func (m Matrix) ScaleFactor() float64 { return math.Sqrt(math.Abs(m.A*m.D - m.B*m.C)) }

var transformRe = regexp.MustCompile(`([a-zA-Z]+)\s*\(([^)]*)\)`)

// ParseTransform parses an SVG transform list.
func ParseTransform(s string) Matrix {
	m := Identity()
	for _, g := range transformRe.FindAllStringSubmatch(s, -1) {
		v := Numbers(g[2])
		get := func(i int, def float64) float64 {
			if i < len(v) {
				return v[i]
			}
			return def
		}
		var t Matrix
		switch g[1] {
		case "matrix":
			if len(v) < 6 {
				continue
			}
			t = Matrix{v[0], v[1], v[2], v[3], v[4], v[5]}
		case "translate":
			t = Matrix{A: 1, D: 1, E: get(0, 0), F: get(1, 0)}
		case "scale":
			sx := get(0, 1)
			t = Matrix{A: sx, D: get(1, sx)}
		case "rotate":
			a := get(0, 0) * math.Pi / 180
			cx, cy := get(1, 0), get(2, 0)
			r := Matrix{A: math.Cos(a), B: math.Sin(a), C: -math.Sin(a), D: math.Cos(a)}
			t = Matrix{A: 1, D: 1, E: cx, F: cy}.Mul(r).Mul(Matrix{A: 1, D: 1, E: -cx, F: -cy})
		case "skewX":
			t = Matrix{A: 1, D: 1, C: math.Tan(get(0, 0) * math.Pi / 180)}
		case "skewY":
			t = Matrix{A: 1, D: 1, B: math.Tan(get(0, 0) * math.Pi / 180)}
		default:
			continue
		}
		m = m.Mul(t)
	}
	return m
}

var numRe = regexp.MustCompile(`[-+]?(?:\d+\.?\d*|\.\d+)(?:[eE][-+]?\d+)?`)

// Numbers extracts all numbers from s.
func Numbers(s string) []float64 {
	var out []float64
	for _, t := range numRe.FindAllString(s, -1) {
		if f, err := strconv.ParseFloat(t, 64); err == nil {
			out = append(out, f)
		}
	}
	return out
}

// Num parses a length attribute (ignoring units); def if missing/invalid.
func Num(s string, def float64) float64 {
	v := Numbers(s)
	if len(v) == 0 {
		return def
	}
	return v[0]
}

// Points returns the transformed geometry of a shape element as a polyline
// (closed shapes are not explicitly closed). ok is false for non-geometry.
func (n *Node) Points() (pts []Point, closed bool, ok bool) {
	a := n.Attr
	switch n.Name {
	case "rect":
		x, y := Num(a["x"], 0), Num(a["y"], 0)
		w, h := Num(a["width"], 0), Num(a["height"], 0)
		pts = []Point{{x, y}, {x + w, y}, {x + w, y + h}, {x, y + h}}
		closed = true
	case "polygon", "polyline":
		v := Numbers(a["points"])
		for i := 0; i+1 < len(v); i += 2 {
			pts = append(pts, Point{v[i], v[i+1]})
		}
		closed = n.Name == "polygon"
		// drop explicit closing duplicate
		if closed && len(pts) > 1 && pts[0].Dist(pts[len(pts)-1]) < 1e-6 {
			pts = pts[:len(pts)-1]
		}
	case "line":
		pts = []Point{{Num(a["x1"], 0), Num(a["y1"], 0)}, {Num(a["x2"], 0), Num(a["y2"], 0)}}
	case "circle", "ellipse":
		cx, cy := Num(a["cx"], 0), Num(a["cy"], 0)
		rx, ry := Num(a["r"], 0), Num(a["r"], 0)
		if n.Name == "ellipse" {
			rx, ry = Num(a["rx"], 0), Num(a["ry"], 0)
		}
		for i := 0; i < 16; i++ {
			t := float64(i) / 16 * 2 * math.Pi
			pts = append(pts, Point{cx + rx*math.Cos(t), cy + ry*math.Sin(t)})
		}
		closed = true
	case "path":
		pts, closed = ParsePath(a["d"])
	default:
		return nil, false, false
	}
	for i := range pts {
		pts[i] = n.CTM.Apply(pts[i])
	}
	return pts, closed, len(pts) > 0
}

// ParsePath flattens an SVG path into points (curves are sampled).
func ParsePath(d string) ([]Point, bool) {
	pts, closed, _ := parsePath(d)
	return pts, closed
}

// ParseSubpaths splits a path into its subpaths (one per moveto), flattened.
func ParseSubpaths(d string) [][]Point {
	pts, _, starts := parsePath(d)
	var out [][]Point
	for i, st := range starts {
		end := len(pts)
		if i+1 < len(starts) {
			end = starts[i+1]
		}
		if end-st >= 2 {
			out = append(out, pts[st:end])
		}
	}
	return out
}

// Subpaths returns the transformed outline(s) of a shape element: one per
// subpath for <path>, a single one for other geometry.
func (n *Node) Subpaths() [][]Point {
	if n.Name != "path" {
		pts, _, ok := n.Points()
		if !ok {
			return nil
		}
		return [][]Point{pts}
	}
	sub := ParseSubpaths(n.Attr["d"])
	for _, sp := range sub {
		for i := range sp {
			sp[i] = n.CTM.Apply(sp[i])
		}
	}
	return sub
}

func parsePath(d string) ([]Point, bool, []int) {
	var starts []int
	toks := tokenizePath(d)
	var pts []Point
	var cur, start, lastCtrl Point
	var cmd byte
	closed := false
	i := 0
	num := func() (float64, bool) {
		if i < len(toks) && !isCmd(toks[i]) {
			f, err := strconv.ParseFloat(toks[i], 64)
			i++
			return f, err == nil
		}
		return 0, false
	}
	pt := func(rel bool) (Point, bool) {
		x, ok1 := num()
		y, ok2 := num()
		if !ok1 || !ok2 {
			return Point{}, false
		}
		if rel {
			return Point{cur.X + x, cur.Y + y}, true
		}
		return Point{x, y}, true
	}
	cubic := func(p0, p1, p2, p3 Point) {
		// sample density follows the control-polygon length (~1 point per 6 units)
		n := int((p0.Dist(p1) + p1.Dist(p2) + p2.Dist(p3)) / 6)
		n = max(8, min(n, 96))
		for k := 1; k <= n; k++ {
			t := float64(k) / float64(n)
			u := 1 - t
			pts = append(pts, Point{
				u*u*u*p0.X + 3*u*u*t*p1.X + 3*u*t*t*p2.X + t*t*t*p3.X,
				u*u*u*p0.Y + 3*u*u*t*p1.Y + 3*u*t*t*p2.Y + t*t*t*p3.Y,
			})
		}
	}
	for i < len(toks) {
		if isCmd(toks[i]) {
			cmd = toks[i][0]
			i++
		} else if cmd == 0 {
			i++
			continue
		}
		rel := cmd >= 'a' && cmd <= 'z'
		switch cmd {
		case 'M', 'm':
			p, ok := pt(rel)
			if !ok {
				break
			}
			cur, start = p, p
			starts = append(starts, len(pts))
			pts = append(pts, p)
			if rel {
				cmd = 'l'
			} else {
				cmd = 'L'
			}
			lastCtrl = cur
			continue
		case 'L', 'l':
			p, ok := pt(rel)
			if !ok {
				break
			}
			cur = p
			pts = append(pts, p)
		case 'H', 'h':
			x, ok := num()
			if !ok {
				break
			}
			if rel {
				x += cur.X
			}
			cur = Point{x, cur.Y}
			pts = append(pts, cur)
		case 'V', 'v':
			y, ok := num()
			if !ok {
				break
			}
			if rel {
				y += cur.Y
			}
			cur = Point{cur.X, y}
			pts = append(pts, cur)
		case 'C', 'c':
			p1, ok1 := pt(rel)
			p2, ok2 := pt(rel)
			p3, ok3 := pt(rel)
			if !(ok1 && ok2 && ok3) {
				break
			}
			cubic(cur, p1, p2, p3)
			lastCtrl, cur = p2, p3
			continue
		case 'S', 's':
			p2, ok1 := pt(rel)
			p3, ok2 := pt(rel)
			if !(ok1 && ok2) {
				break
			}
			p1 := Point{2*cur.X - lastCtrl.X, 2*cur.Y - lastCtrl.Y}
			cubic(cur, p1, p2, p3)
			lastCtrl, cur = p2, p3
			continue
		case 'Q', 'q', 'T', 't':
			var c Point
			ok := true
			if cmd == 'Q' || cmd == 'q' {
				c, ok = pt(rel)
			} else {
				c = Point{2*cur.X - lastCtrl.X, 2*cur.Y - lastCtrl.Y}
			}
			p, ok2 := pt(rel)
			if !(ok && ok2) {
				break
			}
			p1 := Point{cur.X + 2.0/3*(c.X-cur.X), cur.Y + 2.0/3*(c.Y-cur.Y)}
			p2 := Point{p.X + 2.0/3*(c.X-p.X), p.Y + 2.0/3*(c.Y-p.Y)}
			cubic(cur, p1, p2, p)
			lastCtrl, cur = c, p
			continue
		case 'A', 'a':
			// rx ry rot large sweep x y — approximate by endpoint
			for k := 0; k < 5; k++ {
				num()
			}
			p, ok := pt(rel)
			if !ok {
				break
			}
			cur = p
			pts = append(pts, p)
		case 'Z', 'z':
			closed = true
			cur = start
			cmd = 0
		default:
			i++
		}
		lastCtrl = cur
	}
	return pts, closed, starts
}

func isCmd(t string) bool {
	return len(t) == 1 && strings.ContainsAny(t, "MmLlHhVvCcSsQqTtAaZz")
}

var pathTokRe = regexp.MustCompile(`[MmLlHhVvCcSsQqTtAaZz]|[-+]?(?:\d+\.?\d*|\.\d+)(?:[eE][-+]?\d+)?`)

func tokenizePath(d string) []string { return pathTokRe.FindAllString(d, -1) }
