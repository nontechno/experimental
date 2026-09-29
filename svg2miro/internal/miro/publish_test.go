package miro

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/seamia/svg2miro/internal/scene"
	"github.com/seamia/svg2miro/internal/svgdoc"
)

// fakeMiro is a strict stand-in for the Miro REST API v2 that validates
// every payload the publisher sends.
type fakeMiro struct {
	t          *testing.T
	mu         sync.Mutex
	seq        int
	items      map[string]string   // id -> type
	frames     map[string]Geometry // frame id -> geometry
	connectors int
	calls      map[string]int
	throttle   int // number of 429 responses to send first
	rejectBulk int // index (1-based) of a bulk call to reject with 400
	bulkSeen   int
	problems   []string
}

var (
	hexRe  = regexp.MustCompile(`^#[0-9a-f]{6}$`)
	pctRe  = regexp.MustCompile(`^(\d{1,3})%$`)
	shapes = map[string]bool{"rectangle": true, "round_rectangle": true, "circle": true, "triangle": true,
		"rhombus": true, "parallelogram": true, "pentagon": true, "hexagon": true, "octagon": true}
)

func (f *fakeMiro) bad(format string, a ...any) {
	f.problems = append(f.problems, fmt.Sprintf(format, a...))
}

func (f *fakeMiro) id(kind string) string {
	f.seq++
	id := fmt.Sprintf("3458764%09d", f.seq)
	f.items[id] = kind
	return id
}

func (f *fakeMiro) checkItem(kind string, it map[string]any) {
	data, _ := it["data"].(map[string]any)
	style, _ := it["style"].(map[string]any)
	pos, _ := it["position"].(map[string]any)
	if data == nil || pos == nil {
		f.bad("%s: missing data/position", kind)
		return
	}
	if _, ok := pos["x"].(float64); !ok {
		f.bad("%s: position.x not a number", kind)
	}
	if geo, ok := it["geometry"].(map[string]any); ok {
		if w, _ := geo["width"].(float64); w <= 0 {
			f.bad("%s: width %v", kind, geo["width"])
		}
	}
	for _, k := range []string{"fillColor", "borderColor", "color"} {
		if v, ok := style[k]; ok && !hexRe.MatchString(v.(string)) {
			f.bad("%s: %s=%v is not #rrggbb", kind, k, v)
		}
	}
	if v, ok := style["fontSize"]; ok {
		n, err := strconv.Atoi(v.(string))
		if err != nil || n < 10 || n > 288 {
			f.bad("%s: fontSize %v", kind, v)
		}
	}
	switch kind {
	case "shape":
		if !shapes[data["shape"].(string)] {
			f.bad("shape: unknown shape %v", data["shape"])
		}
	case "text":
		if c, _ := data["content"].(string); c == "" {
			f.bad("text: empty content")
		}
	case "frame":
		if data["format"] != "custom" {
			f.bad("frame: format %v", data["format"])
		}
	}
	if content, ok := data["content"].(string); ok && content != "" {
		if strings.Count(content, "<p>") != strings.Count(content, "</p>") || strings.Contains(content, " ") {
			f.bad("%s: malformed content %q", kind, content)
		}
	}
	if par, ok := it["parent"].(map[string]any); ok {
		pid, _ := par["id"].(string)
		g, isFrame := f.frames[pid]
		if !isFrame {
			f.bad("%s: parent %s is not a frame", kind, pid)
		} else {
			x, y := pos["x"].(float64), pos["y"].(float64)
			if x < 0 || y < 0 || x > g.Width || y > g.Height {
				f.bad("%s: relative position (%v,%v) outside parent frame %vx%v", kind, x, y, g.Width, g.Height)
			}
		}
	}
}

func (f *fakeMiro) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	body, _ := io.ReadAll(r.Body)
	if r.Header.Get("Authorization") != "Bearer test-token" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if f.throttle > 0 {
		f.throttle--
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusTooManyRequests)
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/v2/boards/"), "/")
	kind := parts[len(parts)-1]
	f.calls[kind]++
	reply := func(v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(v)
	}
	switch kind {
	case "bulk":
		f.bulkSeen++
		var items []map[string]any
		if err := json.Unmarshal(body, &items); err != nil || len(items) == 0 || len(items) > 20 {
			f.bad("bulk: bad batch (%d items, %v)", len(items), err)
		}
		if f.bulkSeen == f.rejectBulk {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"message":"simulated validation failure"}`))
			return
		}
		var out []map[string]string
		for _, it := range items {
			t, _ := it["type"].(string)
			f.checkItem(t, it)
			out = append(out, map[string]string{"id": f.id(t)})
		}
		reply(map[string]any{"data": out})
	case "frames", "shapes", "texts":
		var it map[string]any
		_ = json.Unmarshal(body, &it)
		if _, has := it["type"]; has {
			f.bad("%s: single-item payload must not carry type", kind)
		}
		t := strings.TrimSuffix(kind, "s")
		f.checkItem(t, it)
		id := f.id(t)
		if kind == "frames" {
			var g struct{ Geometry Geometry }
			_ = json.Unmarshal(body, &g)
			f.frames[id] = g.Geometry
		}
		reply(map[string]string{"id": id})
	case "connectors":
		var c ConnectorPayload
		if err := json.Unmarshal(body, &c); err != nil {
			f.bad("connector: %v", err)
		}
		for _, e := range []Endpoint{c.StartItem, c.EndItem} {
			if f.items[e.ID] != "shape" {
				f.bad("connector: endpoint %s is not a shape (%q)", e.ID, f.items[e.ID])
			}
			x, y := pctRe.FindStringSubmatch(e.Position["x"]), pctRe.FindStringSubmatch(e.Position["y"])
			if x == nil || y == nil {
				f.bad("connector: bad position %v", e.Position)
				continue
			}
			if x[1] != "0" && x[1] != "100" && y[1] != "0" && y[1] != "100" {
				f.bad("connector: attach point %v not on the outline", e.Position)
			}
		}
		if !hexRe.MatchString(c.Style["strokeColor"]) {
			f.bad("connector: strokeColor %q", c.Style["strokeColor"])
		}
		f.connectors++
		reply(map[string]string{"id": f.id("connector")})
	default:
		f.bad("unexpected call %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}
}

func loadScene(t *testing.T, detailed bool) *scene.Scene {
	t.Helper()
	fh, err := os.Open("../../testdata/protodot.svg")
	if err != nil {
		t.Fatal(err)
	}
	defer fh.Close()
	root, err := svgdoc.Parse(fh)
	if err != nil {
		t.Fatal(err)
	}
	return scene.FromSVG(root, scene.Options{Detailed: detailed})
}

func TestPublishProtodotAgainstFakeMiro(t *testing.T) {
	for _, tc := range []struct {
		name     string
		detailed bool
		bulk     bool
	}{
		{"compact-bulk", false, true},
		{"compact-single", false, false},
		{"detailed-bulk", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fm := &fakeMiro{t: t, items: map[string]string{}, frames: map[string]Geometry{}, calls: map[string]int{},
				throttle: 2, rejectBulk: 3}
			srv := httptest.NewServer(fm)
			defer srv.Close()
			s := loadScene(t, tc.detailed)
			c := &Client{BaseURL: srv.URL, Token: "test-token", HTTP: srv.Client()}
			res, err := Publish(context.Background(), c, "uXjVtest=", s, DefaultLayout(),
				Options{Concurrency: 8, Bulk: tc.bulk, ParentFrames: true})
			if err != nil {
				t.Fatal(err)
			}
			if len(res.Errors) > 0 {
				t.Fatalf("publish errors: %v", res.Errors[:min(5, len(res.Errors))])
			}
			for _, p := range fm.problems[:min(10, len(fm.problems))] {
				t.Error(p)
			}
			if res.Frames != len(s.Frames) || res.Shapes != len(s.Shapes) || res.Texts != len(s.Texts) || res.Connectors != len(s.Connectors) {
				t.Errorf("created %d/%d/%d/%d, want %d/%d/%d/%d", res.Frames, res.Shapes, res.Texts, res.Connectors,
					len(s.Frames), len(s.Shapes), len(s.Texts), len(s.Connectors))
			}
			if fm.connectors != 425 {
				t.Errorf("server saw %d connectors, want 425", fm.connectors)
			}
			t.Logf("calls=%v created=%d frames/%d shapes/%d texts/%d connectors",
				fm.calls, res.Frames, res.Shapes, res.Texts, res.Connectors)
		})
	}
}

func TestUnauthorizedAborts(t *testing.T) {
	fm := &fakeMiro{items: map[string]string{}, frames: map[string]Geometry{}, calls: map[string]int{}}
	srv := httptest.NewServer(fm)
	defer srv.Close()
	c := &Client{BaseURL: srv.URL, Token: "wrong", HTTP: srv.Client()}
	_, err := Publish(context.Background(), c, "b", loadScene(t, false), DefaultLayout(), Options{Bulk: true})
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("want 401 error, got %v", err)
	}
}

func TestAttachSnapsToOutline(t *testing.T) {
	box := scene.Rect{MinX: 0, MinY: 0, MaxX: 100, MaxY: 200}
	for _, tc := range []struct {
		p    scene.Point
		x, y string
	}{
		{scene.Point{X: 101, Y: 50}, "100%", "25%"}, // just outside right edge
		{scene.Point{X: 40, Y: -3}, "40%", "0%"},    // above top
		{scene.Point{X: 2, Y: 100}, "0%", "50%"},    // inside, near left
	} {
		x, y, _, _ := Attach(box, tc.p)
		if x != tc.x || y != tc.y {
			t.Errorf("Attach(%v) = %s,%s want %s,%s", tc.p, x, y, tc.x, tc.y)
		}
	}
}

func TestHTMLEscapesAndStyles(t *testing.T) {
	got := HTML([]scene.Line{{{Text: "map<string, "}, {Text: "Value", Bold: true}, {Text: ">"}}})
	want := "<p>map&lt;string,&nbsp;<strong>Value</strong>&gt;</p>"
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}
