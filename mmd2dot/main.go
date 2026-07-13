// Command mmd2dot converts a Mermaid flowchart (as found standalone or
// embedded in a ```mermaid fenced code block inside a Markdown file) into
// Graphviz DOT source.
//
// Usage:
//
//	mmd2dot input.mmd            // writes input.dot
//	mmd2dot input.md             // extracts first ```mermaid block, writes input.dot
//	mmd2dot input.mmd out.dot    // explicit output path
//	mmd2dot -                    // read stdin, write DOT to stdout
//
// Supported Mermaid flowchart syntax:
//   - `flowchart`/`graph` header with direction (TD, TB, BT, LR, RL)
//   - node shapes: [rect], (rounded), ([stadium]), [[subroutine]],
//     [(cylinder)], ((circle)), {diamond}, {{hexagon}}, >flag]
//   - quoted or bare node labels
//   - edges: -->, ---, -.->, -.-, ==>, ===
//   - edge labels via the `A -->|label| B` syntax
//   - chained edges: `A --> B --> C`
//   - (possibly nested) subgraphs, rendered as Graphviz clusters
//   - `%%` comments
package main

import (
	"bufio"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
)

// ---------------------------------------------------------------------
// Data model
// ---------------------------------------------------------------------

// node is a single flowchart node.
type node struct {
	id      string
	label   string
	shape   string // dot shape/style keyword, see shapeAttrs
	defined bool   // true once an explicit shape/label token has set this node
}

// edge is a connection between two nodes.
type edge struct {
	from, to string
	label    string
	style    string // "solid", "dashed", "bold"
	arrow    bool   // true => arrowhead=normal, false => arrowhead=none
}

// item is one entry inside a subgraph: either a reference to a node id,
// or a nested subgraph. Keeping an ordered list of items (rather than two
// separate slices) preserves the original declaration order on output.
type item struct {
	nodeID string
	sub    *subgraph
}

// subgraph models a Mermaid `subgraph ... end` block, rendered as a
// Graphviz `subgraph cluster_X { ... }`. The implicit top-level subgraph
// (id == "") is rendered without a cluster wrapper.
type subgraph struct {
	id    string
	label string
	items []item
}

// graph is the fully parsed flowchart.
type graph struct {
	direction string // TB, BT, LR, RL
	nodes     map[string]*node
	placed    map[string]bool // node ids already assigned a position in the subgraph tree
	root      *subgraph
	edges     []edge
}

func newGraph() *graph {
	return &graph{
		direction: "TB",
		nodes:     map[string]*node{},
		placed:    map[string]bool{},
		root:      &subgraph{id: ""},
	}
}

// getOrCreateNode returns the node with the given id, creating it (with a
// default box shape and the id as its label) if it doesn't exist yet.
func (g *graph) getOrCreateNode(id string) *node {
	if n, ok := g.nodes[id]; ok {
		return n
	}
	n := &node{id: id, label: id, shape: "box"}
	g.nodes[id] = n
	return n
}

// place records that id belongs, positionally, inside the given subgraph.
// Only the first placement "wins" the position; later references (e.g. an
// edge mentioning a node that was already declared earlier) don't move it.
func (g *graph) place(id string, into *subgraph) {
	if g.placed[id] {
		return
	}
	g.placed[id] = true
	into.items = append(into.items, item{nodeID: id})
}

// ---------------------------------------------------------------------
// Parsing
// ---------------------------------------------------------------------

var (
	headerRe   = regexp.MustCompile(`(?i)^(flowchart|graph)\s+([A-Za-z]{2})\b`)
	subgraphRe = regexp.MustCompile(`(?i)^subgraph\s+(.*)$`)
	endRe      = regexp.MustCompile(`(?i)^end\s*$`)

	// Arrow operators, longest/most-specific first so alternation can't
	// accidentally short-match a prefix of a longer operator.
	arrowRe = regexp.MustCompile(`-\.->|-\.-|==>|===|-->|---`)

	// Node-shape patterns, most specific delimiter pairs first.
	shapePatterns = []struct {
		re    *regexp.Regexp
		shape string
	}{
		{regexp.MustCompile(`^(.+?)\(\[(.*)\]\)$`), "stadium"},
		{regexp.MustCompile(`^(.+?)\[\[(.*)\]\]$`), "subroutine"},
		{regexp.MustCompile(`^(.+?)\[\((.*)\)\]$`), "cylinder"},
		{regexp.MustCompile(`^(.+?)\(\((.*)\)\)$`), "circle"},
		{regexp.MustCompile(`^(.+?)\{\{(.*)\}\}$`), "hexagon"},
		{regexp.MustCompile(`^(.+?)\{(.*)\}$`), "diamond"},
		{regexp.MustCompile(`^(.+?)>(.*)\]$`), "flag"},
		{regexp.MustCompile(`^(.+?)\[(.*)\]$`), "rect"},
		{regexp.MustCompile(`^(.+?)\((.*)\)$`), "rounded"},
	}

	mermaidFenceRe = regexp.MustCompile("(?i)^```\\s*mermaid\\s*$")
	fenceEndRe     = regexp.MustCompile("^```\\s*$")
)

// extractMermaidBlock pulls the first ```mermaid fenced block out of a
// Markdown document. If no fence is found, the input is assumed to
// already be raw Mermaid source and is returned unchanged.
func extractMermaidBlock(src string) string {
	lines := strings.Split(src, "\n")
	inFence := false
	var out []string
	for _, l := range lines {
		if !inFence && mermaidFenceRe.MatchString(strings.TrimSpace(l)) {
			inFence = true
			continue
		}
		if inFence && fenceEndRe.MatchString(strings.TrimSpace(l)) {
			return strings.Join(out, "\n")
		}
		if inFence {
			out = append(out, l)
		}
	}
	if len(out) > 0 {
		return strings.Join(out, "\n")
	}
	return src // no fence found; treat whole input as Mermaid
}

// parse converts Mermaid flowchart source into a graph.
func parse(src string) (*graph, error) {
	g := newGraph()
	stack := []*subgraph{g.root}

	scanner := bufio.NewScanner(strings.NewReader(extractMermaidBlock(src)))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	anonSubgraphN := 0

	for scanner.Scan() {
		line := scanner.Text()

		// Strip mermaid comments and surrounding whitespace.
		if i := strings.Index(line, "%%"); i >= 0 {
			line = line[:i]
		}
		line = strings.TrimSpace(line)
		line = strings.TrimSuffix(line, ";")
		if line == "" {
			continue
		}

		top := stack[len(stack)-1]

		switch {
		case headerRe.MatchString(line):
			m := headerRe.FindStringSubmatch(line)
			g.direction = normalizeDirection(m[2])
			continue

		case endRe.MatchString(line):
			if len(stack) > 1 {
				stack = stack[:len(stack)-1]
			}
			continue

		case subgraphRe.MatchString(line):
			m := subgraphRe.FindStringSubmatch(line)
			rest := strings.TrimSpace(m[1])
			id, label := splitSubgraphHeader(rest)
			if id == "" {
				anonSubgraphN++
				id = fmt.Sprintf("anon%d", anonSubgraphN)
			}
			sg := &subgraph{id: id, label: label}
			top.items = append(top.items, item{sub: sg})
			stack = append(stack, sg)
			continue
		}

		// Otherwise: a node definition line and/or one or more edges.
		if arrowRe.MatchString(line) {
			parseEdgeLine(g, top, line)
		} else {
			parseNodeDefLine(g, top, line)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return g, nil
}

// normalizeDirection maps Mermaid direction keywords to Graphviz rankdir
// values (Mermaid's TB and Graphviz's TB happen to already agree).
func normalizeDirection(d string) string {
	switch strings.ToUpper(d) {
	case "TD", "TB":
		return "TB"
	case "BT":
		return "BT"
	case "LR":
		return "LR"
	case "RL":
		return "RL"
	default:
		return "TB"
	}
}

// splitSubgraphHeader parses the remainder of a `subgraph ...` line into
// an id and a display label. Mermaid allows:
//
//	subgraph id [Label with spaces]
//	subgraph id [Quoted "Label"]
//	subgraph Label            (no separate id; label doubles as id)
//	subgraph "Label"
func splitSubgraphHeader(rest string) (id, label string) {
	rest = strings.TrimSpace(rest)
	if rest == "" {
		return "", ""
	}
	if i := strings.Index(rest, "["); i >= 0 && strings.HasSuffix(rest, "]") {
		id = strings.TrimSpace(rest[:i])
		label = unquote(strings.TrimSpace(rest[i+1 : len(rest)-1]))
		if id == "" {
			id = sanitizeID(label)
		}
		return id, label
	}
	// No bracketed label: the whole token is both id and label.
	label = unquote(rest)
	id = sanitizeID(label)
	return id, label
}

// sanitizeID turns arbitrary text into something safe to use as a
// Graphviz identifier.
func sanitizeID(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	out := b.String()
	if out == "" {
		out = "n"
	}
	return out
}

func unquote(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		if unq, err := strconv.Unquote(s); err == nil {
			return unq
		}
		return s[1 : len(s)-1]
	}
	return s
}

// parseNodeDefLine handles a line that only declares a node (no arrows),
// e.g. `A[Internal API]` or `outer["Outer node"]`.
func parseNodeDefLine(g *graph, top *subgraph, token string) {
	id, label, shape, explicit := parseNodeToken(token)
	if id == "" {
		return
	}
	n := g.getOrCreateNode(id)
	n.label = label
	n.shape = shape
	n.defined = n.defined || explicit
	g.place(id, top)
}

// parseNodeToken splits a single node token such as `A{{Decision}}` into
// its id, display label, and shape keyword. Bare ids (no delimiters) get
// the id itself as the label and a default "rect" shape. explicit reports
// whether the token actually carried shape/label delimiters, as opposed
// to being a bare reference like the `B` in `B --> A`.
func parseNodeToken(token string) (id, label, shape string, explicit bool) {
	token = strings.TrimSpace(token)
	if token == "" {
		return "", "", "", false
	}
	for _, sp := range shapePatterns {
		if m := sp.re.FindStringSubmatch(token); m != nil {
			return strings.TrimSpace(m[1]), unquote(m[2]), sp.shape, true
		}
	}
	// Bare id, no explicit label/shape.
	return token, token, "rect", false
}

// parseEdgeLine handles a line containing one or more arrow operators,
// e.g. `outer --> |aaaaa|A` or `A --> B --> C`.
func parseEdgeLine(g *graph, top *subgraph, line string) {
	arrowMatches := arrowRe.FindAllStringSubmatchIndex(line, -1)
	if len(arrowMatches) == 0 {
		return
	}

	// Split the line into node segments, one before the first arrow and
	// one after every arrow.
	var segments []string
	var arrows []string
	prevEnd := 0
	for _, m := range arrowMatches {
		start, end := m[0], m[1]
		segments = append(segments, strings.TrimSpace(line[prevEnd:start]))
		arrows = append(arrows, line[start:end])
		prevEnd = end
	}
	segments = append(segments, strings.TrimSpace(line[prevEnd:]))

	// Register/place the first node.
	fromID := registerSegment(g, top, segments[0])

	for i, arrow := range arrows {
		seg := segments[i+1]
		edgeLabel := ""
		if strings.HasPrefix(seg, "|") {
			if end := strings.Index(seg[1:], "|"); end >= 0 {
				edgeLabel = seg[1 : 1+end]
				seg = strings.TrimSpace(seg[1+end+1:])
			}
		}
		toID := registerSegment(g, top, seg)

		style, hasArrowhead := edgeStyle(arrow)
		g.edges = append(g.edges, edge{
			from:  fromID,
			to:    toID,
			label: edgeLabel,
			style: style,
			arrow: hasArrowhead,
		})
		fromID = toID
	}
}

// registerSegment parses a node token found inside an edge line, creates
// the node if needed, updates its label/shape if this occurrence carries
// an explicit definition, and places it in the subgraph tree.
func registerSegment(g *graph, top *subgraph, seg string) string {
	id, label, shape, explicit := parseNodeToken(seg)
	n := g.getOrCreateNode(id)
	// A bare reference (no shape delimiters) must never clobber a shape
	// or label an earlier explicit definition already set. An explicit
	// definition always wins, even over an earlier explicit one, so that
	// e.g. a later `A[Real Label]` can still refine an id first seen
	// bare in an edge.
	if explicit || !n.defined {
		n.label = label
		n.shape = shape
		n.defined = n.defined || explicit
	}
	g.place(id, top)
	return id
}

// edgeStyle maps a Mermaid arrow token to a Graphviz line style plus
// whether it should render an arrowhead.
func edgeStyle(arrow string) (style string, hasArrowhead bool) {
	switch arrow {
	case "-->":
		return "solid", true
	case "---":
		return "solid", false
	case "-.->":
		return "dashed", true
	case "-.-":
		return "dashed", false
	case "==>":
		return "bold", true
	case "===":
		return "bold", false
	default:
		return "solid", true
	}
}

// ---------------------------------------------------------------------
// Rendering
// ---------------------------------------------------------------------

// shapeAttrs maps our internal shape keyword to Graphviz node attributes.
var shapeAttrs = map[string]string{
	"rect":       `shape=box`,
	"rounded":    `shape=box, style="rounded,filled", fillcolor=white`,
	"stadium":    `shape=box, style="rounded,filled", fillcolor=white`,
	"subroutine": `shape=box, peripheries=2`,
	"cylinder":   `shape=cylinder`,
	"circle":     `shape=circle`,
	"diamond":    `shape=diamond`,
	"hexagon":    `shape=hexagon`,
	"flag":       `shape=cds`,
}

func dotEscape(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	s = strings.ReplaceAll(s, "\n", `\n`)
	return s
}

func (g *graph) render() string {
	var b strings.Builder
	b.WriteString("digraph G {\n")
	fmt.Fprintf(&b, "    rankdir=%s;\n", g.direction)
	b.WriteString("    compound=true;\n")
	b.WriteString("    fontname=\"Helvetica\";\n")
	b.WriteString("    node [fontname=\"Helvetica\", color=\"#333333\"];\n")
	b.WriteString("    edge [fontname=\"Helvetica\", fontsize=10, color=\"#555555\"];\n\n")

	g.renderSubgraph(&b, g.root, 1)

	if len(g.edges) > 0 {
		b.WriteString("\n")
		for _, e := range g.edges {
			attrs := []string{}
			if e.label != "" {
				attrs = append(attrs, fmt.Sprintf(`label="%s"`, dotEscape(e.label)))
			}
			switch e.style {
			case "dashed":
				attrs = append(attrs, `style=dashed`)
			case "bold":
				attrs = append(attrs, `style=bold`, `penwidth=2`)
			}
			if !e.arrow {
				attrs = append(attrs, `arrowhead=none`)
			}
			indent := "    "
			if len(attrs) > 0 {
				fmt.Fprintf(&b, "%s%s -> %s [%s];\n", indent, e.from, e.to, strings.Join(attrs, ", "))
			} else {
				fmt.Fprintf(&b, "%s%s -> %s;\n", indent, e.from, e.to)
			}
		}
	}

	b.WriteString("}\n")
	return b.String()
}

func (g *graph) renderSubgraph(b *strings.Builder, sg *subgraph, depth int) {
	indent := strings.Repeat("    ", depth)
	isCluster := sg.id != ""

	if isCluster {
		fmt.Fprintf(b, "%ssubgraph cluster_%s {\n", indent, sg.id)
		inner := strings.Repeat("    ", depth+1)
		fmt.Fprintf(b, "%slabel=\"%s\";\n", inner, dotEscape(sg.label))
		b.WriteString(inner + "style=rounded;\n")
		b.WriteString(inner + "color=\"#999999\";\n")
	}

	childDepth := depth
	if isCluster {
		childDepth = depth + 1
	}
	childIndent := strings.Repeat("    ", childDepth)

	for _, it := range sg.items {
		if it.sub != nil {
			g.renderSubgraph(b, it.sub, childDepth)
			continue
		}
		n := g.nodes[it.nodeID]
		attrs := shapeAttrs[n.shape]
		fmt.Fprintf(b, "%s%s [%s, label=\"%s\"];\n", childIndent, n.id, attrs, dotEscape(n.label))
	}

	if isCluster {
		fmt.Fprintf(b, "%s}\n", indent)
	}
}

// ---------------------------------------------------------------------
// main / CLI
// ---------------------------------------------------------------------

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: mmd2dot <input.mmd|input.md|-> [output.dot]")
		os.Exit(2)
	}

	var src []byte
	var err error
	inPath := os.Args[1]
	if inPath == "-" {
		src, err = readAll(os.Stdin)
	} else {
		src, err = os.ReadFile(inPath)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "mmd2dot: %v\n", err)
		os.Exit(1)
	}

	g, err := parse(string(src))
	if err != nil {
		fmt.Fprintf(os.Stderr, "mmd2dot: parse error: %v\n", err)
		os.Exit(1)
	}
	out := g.render()

	if len(os.Args) >= 3 {
		if err := os.WriteFile(os.Args[2], []byte(out), 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "mmd2dot: %v\n", err)
			os.Exit(1)
		}
		return
	}
	if inPath == "-" {
		fmt.Print(out)
		return
	}
	outPath := strings.TrimSuffix(inPath, extOf(inPath)) + ".dot"
	if err := os.WriteFile(outPath, []byte(out), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "mmd2dot: %v\n", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "wrote %s\n", outPath)
}

func extOf(path string) string {
	i := strings.LastIndex(path, ".")
	if i < 0 {
		return ""
	}
	return path[i:]
}

func readAll(f *os.File) ([]byte, error) {
	var b strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := f.Read(buf)
		if n > 0 {
			b.Write(buf[:n])
		}
		if err != nil {
			if err.Error() == "EOF" {
				break
			}
			return []byte(b.String()), nil
		}
	}
	return []byte(b.String()), nil
}
