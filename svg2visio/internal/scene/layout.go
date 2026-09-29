package scene

import (
	"math"
	"sort"
	"strings"

	"github.com/nontechno/experimental/svg2visio/internal/svgdoc"
)

const nbsp = " "

// layoutLines groups text runs into visual rows (top to bottom) and turns
// each row into a Line. For monospace text the horizontal positions are
// preserved with non-breaking-space padding so columns stay aligned.
func layoutLines(runs []svgdoc.TextRun, box Rect) (lines []Line, mono bool, alignH string, size float64) {
	if len(runs) == 0 {
		return nil, false, "center", 0
	}
	sort.SliceStable(runs, func(i, j int) bool {
		if math.Abs(runs[i].Pos.Y-runs[j].Pos.Y) > runs[i].Size*0.4 {
			return runs[i].Pos.Y < runs[j].Pos.Y
		}
		return runs[i].Pos.X < runs[j].Pos.X
	})
	var rows [][]svgdoc.TextRun
	for _, r := range runs {
		n := len(rows)
		if n > 0 && math.Abs(rows[n-1][0].Pos.Y-r.Pos.Y) <= r.Size*0.4 {
			rows[n-1] = append(rows[n-1], r)
		} else {
			rows = append(rows, []svgdoc.TextRun{r})
		}
	}

	mono = true
	anchors := map[string]int{}
	for _, r := range runs {
		mono = mono && r.Mono()
		anchors[r.Anchor]++
		size = math.Max(size, r.Size)
	}
	switch {
	case mono:
		alignH = "left"
	case anchors["middle"] >= anchors["start"] && anchors["middle"] >= anchors["end"]:
		alignH = "center"
	case anchors["end"] > anchors["start"]:
		alignH = "right"
	default:
		alignH = "left"
	}

	if !mono {
		for _, row := range rows {
			var l Line
			for i, r := range row {
				t := r.Text
				if i > 0 && !strings.HasSuffix(row[i-1].Text, " ") && !strings.HasPrefix(t, " ") {
					prevEnd := row[i-1].Bounds().MaxX
					if r.Bounds().MinX-prevEnd > r.Size*0.2 {
						t = " " + t
					}
				}
				l = appendSpan(l, Span{t, r.Bold, r.Italic, r.Underline})
			}
			lines = append(lines, trimLine(l))
		}
		return lines, false, alignH, size
	}

	// Monospace: estimate glyph advance from adjacent runs, then map x to columns.
	charW := size * 0.63 // typical monospace advance (Courier/Menlo ~0.6-0.63em)
	best := math.Inf(1)
	for _, row := range rows {
		for i := 0; i+1 < len(row); i++ {
			n := len([]rune(row[i].Text))
			if n == 0 || row[i].Anchor != "start" || row[i+1].Anchor != "start" {
				continue
			}
			if w := (row[i+1].Pos.X - row[i].Pos.X) / float64(n); w > 0 && w < best {
				best = w
			}
		}
	}
	if best >= size*0.58 && best <= size*0.68 {
		charW = best
	}

	type placed struct {
		col int
		r   svgdoc.TextRun
	}
	var prows [][]placed
	minCol := math.MaxInt32
	for _, row := range rows {
		var pr []placed
		for _, r := range row {
			x := r.Bounds().MinX
			c := int(math.Round((x - box.MinX) / charW))
			if c < 0 {
				c = 0
			}
			pr = append(pr, placed{c, r})
		}
		if len(pr) > 0 && pr[0].col < minCol {
			minCol = pr[0].col
		}
		prows = append(prows, pr)
	}
	for _, pr := range prows {
		var l Line
		cur := 0
		for i, p := range pr {
			col := p.col - minCol
			// separate runs that ended up in touching columns (e.g. a
			// long field name next to its type) keep one space between them
			if i > 0 && col <= cur && !strings.HasSuffix(pr[i-1].r.Text, " ") && !strings.HasPrefix(p.r.Text, " ") &&
				p.r.Pos.X-(pr[i-1].r.Pos.X+float64(len([]rune(pr[i-1].r.Text)))*charW) > charW*0.25 {
				col = cur + 1
			}
			if col > cur {
				l = appendSpan(l, Span{Text: strings.Repeat(nbsp, col-cur)})
				cur = col
			}
			t := strings.ReplaceAll(p.r.Text, " ", nbsp)
			l = appendSpan(l, Span{t, p.r.Bold, p.r.Italic, p.r.Underline})
			cur += len([]rune(t))
		}
		lines = append(lines, trimLine(l))
	}
	return lines, true, alignH, size
}

func appendSpan(l Line, s Span) Line {
	if s.Text == "" {
		return l
	}
	if n := len(l); n > 0 {
		p := l[n-1]
		if p.Bold == s.Bold && p.Italic == s.Italic && p.Underline == s.Underline {
			l[n-1].Text += s.Text
			return l
		}
		// whitespace carries no visible style; merge it into the previous span
		if strings.Trim(s.Text, " "+nbsp) == "" {
			l[n-1].Text += s.Text
			return l
		}
	}
	return append(l, s)
}

func trimLine(l Line) Line {
	for len(l) > 0 {
		last := &l[len(l)-1]
		last.Text = strings.TrimRight(last.Text, " "+nbsp)
		if last.Text != "" {
			break
		}
		l = l[:len(l)-1]
	}
	return l
}
