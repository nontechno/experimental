// Package svgdoc parses SVG into a lightweight element tree with resolved
// transforms, geometry helpers and inherited presentation attributes.
package svgdoc

import (
	"encoding/xml"
	"fmt"
	"io"
	"strings"
)

// Node is one SVG element.
type Node struct {
	Name     string
	Attr     map[string]string
	Children []*Node
	Parent   *Node
	Text     string // character data directly inside this element
	CTM      Matrix // cumulative transform (parent CTM * own transform)
}

// Parse reads an SVG document and returns its root <svg> element.
func Parse(r io.Reader) (*Node, error) {
	d := xml.NewDecoder(r)
	d.Strict = false
	d.AutoClose = xml.HTMLAutoClose
	d.Entity = xml.HTMLEntity

	var root *Node
	var stack []*Node
	for {
		tok, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("svg parse: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			n := &Node{Name: t.Name.Local, Attr: map[string]string{}}
			for _, a := range t.Attr {
				key := a.Name.Local
				if strings.Contains(a.Name.Space, "xlink") || a.Name.Space == "xlink" {
					key = "xlink:" + key
				}
				n.Attr[key] = a.Value
			}
			if len(stack) > 0 {
				p := stack[len(stack)-1]
				n.Parent = p
				p.Children = append(p.Children, n)
			} else if root == nil {
				root = n
			}
			stack = append(stack, n)
		case xml.CharData:
			if len(stack) > 0 {
				stack[len(stack)-1].Text += string(t)
			}
		case xml.EndElement:
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		}
	}
	if root == nil || root.Name != "svg" {
		return nil, fmt.Errorf("svg parse: no <svg> root element")
	}
	resolveCTM(root, Identity())
	return root, nil
}

func resolveCTM(n *Node, parent Matrix) {
	n.CTM = parent
	if t, ok := n.Attr["transform"]; ok {
		n.CTM = parent.Mul(ParseTransform(t))
	}
	for _, c := range n.Children {
		resolveCTM(c, n.CTM)
	}
}

// Walk visits n and its descendants depth-first. Returning false from fn
// skips the node's children.
func (n *Node) Walk(fn func(*Node) bool) {
	if !fn(n) {
		return
	}
	for _, c := range n.Children {
		c.Walk(fn)
	}
}

// Find returns all descendants (including n) matching pred.
func (n *Node) Find(pred func(*Node) bool) []*Node {
	var out []*Node
	n.Walk(func(x *Node) bool {
		if pred(x) {
			out = append(out, x)
		}
		return true
	})
	return out
}

// HasClass reports whether the element's class list contains c.
func (n *Node) HasClass(c string) bool {
	for _, f := range strings.Fields(n.Attr["class"]) {
		if f == c {
			return true
		}
	}
	return false
}

// ChildText returns the text of the first direct child named name.
func (n *Node) ChildText(name string) string {
	for _, c := range n.Children {
		if c.Name == name {
			return strings.TrimSpace(c.AllText())
		}
	}
	return ""
}

// AllText returns the concatenated character data of n and descendants.
func (n *Node) AllText() string {
	var b strings.Builder
	var rec func(*Node)
	rec = func(x *Node) {
		b.WriteString(x.Text)
		for _, c := range x.Children {
			rec(c)
		}
	}
	rec(n)
	return b.String()
}

// Prop returns a presentation property from the element itself, checking
// the style attribute first and then the plain attribute.
func (n *Node) Prop(key string) (string, bool) {
	if s, ok := n.Attr["style"]; ok {
		for _, decl := range strings.Split(s, ";") {
			kv := strings.SplitN(decl, ":", 2)
			if len(kv) == 2 && strings.TrimSpace(kv[0]) == key {
				return strings.TrimSpace(kv[1]), true
			}
		}
	}
	v, ok := n.Attr[key]
	return strings.TrimSpace(v), ok
}

// Inherited returns a property resolved up the ancestor chain, or def.
func (n *Node) Inherited(key, def string) string {
	for x := n; x != nil; x = x.Parent {
		if v, ok := x.Prop(key); ok && v != "inherit" {
			return v
		}
	}
	return def
}

// IsHidden reports display:none / visibility:hidden on n or an ancestor.
func (n *Node) IsHidden() bool {
	for x := n; x != nil; x = x.Parent {
		if v, ok := x.Prop("display"); ok && v == "none" {
			return true
		}
	}
	return n.Inherited("visibility", "visible") == "hidden"
}
