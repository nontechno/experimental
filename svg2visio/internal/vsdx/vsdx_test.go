package vsdx

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/nontechno/experimental/svg2visio/internal/scene"
	"github.com/nontechno/experimental/svg2visio/internal/svgdoc"
)

func build(t *testing.T, detailed bool) []byte {
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
	s := scene.FromSVG(root, scene.Options{Detailed: detailed})
	var buf bytes.Buffer
	if err := Write(&buf, s, Options{Title: "protodot"}); err != nil {
		t.Fatal(err)
	}
	if out := os.Getenv("VSDX_OUT"); out != "" && !detailed {
		_ = os.WriteFile(out, buf.Bytes(), 0o644)
	}
	return buf.Bytes()
}

func TestPackageIsWellFormed(t *testing.T) {
	for _, detailed := range []bool{false, true} {
		data := build(t, detailed)
		zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			t.Fatal(err)
		}
		want := map[string]bool{"[Content_Types].xml": false, "_rels/.rels": false, "visio/document.xml": false,
			"visio/pages/pages.xml": false, "visio/pages/page1.xml": false, "visio/pages/_rels/pages.xml.rels": false}
		for _, zf := range zr.File {
			rc, _ := zf.Open()
			body, _ := io.ReadAll(rc)
			rc.Close()
			d := xml.NewDecoder(bytes.NewReader(body))
			for {
				if _, err := d.Token(); err == io.EOF {
					break
				} else if err != nil {
					t.Fatalf("%s: not well-formed XML: %v", zf.Name, err)
				}
			}
			if _, ok := want[zf.Name]; ok {
				want[zf.Name] = true
			}
			if zf.Name == "visio/pages/page1.xml" {
				p := string(body)
				if n := strings.Count(p, "<Connect "); n != 850 {
					t.Errorf("detailed=%v: %d Connect elements, want 850", detailed, n)
				}
				if n := strings.Count(p, `NameU="Dynamic connector.`); n != 425 {
					t.Errorf("connectors: %d", n)
				}
				if strings.Contains(p, `ToPart="99"`) || strings.Contains(p, "NaN") {
					t.Error("bad glue part or NaN value")
				}
			}
		}
		for k, ok := range want {
			if !ok {
				t.Errorf("missing part %s", k)
			}
		}
	}
}

// Visio treats cells missing from Character rows IX>=1 as 0; FontScale=0
// collapses every glyph onto the same x, so each row must be complete.
func TestCharacterRowsComplete(t *testing.T) {
	data := build(t, true)
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	required := []string{"Font", "Color", "Style", "Size", "FontScale", "Letterspace", "Pos", "Case"}
	rows, multi := 0, 0
	for _, zf := range zr.File {
		if zf.Name != "visio/pages/page1.xml" && zf.Name != "visio/document.xml" {
			continue
		}
		rc, _ := zf.Open()
		body, _ := io.ReadAll(rc)
		rc.Close()
		for _, sec := range strings.Split(string(body), `<Section N="Character">`)[1:] {
			sec = sec[:strings.Index(sec, "</Section>")]
			for _, row := range strings.Split(sec, "<Row ")[1:] {
				rows++
				if !strings.HasPrefix(row, `IX="0"`) {
					multi++
				}
				for _, c := range required {
					if !strings.Contains(row, `N="`+c+`"`) {
						t.Fatalf("%s: Character row missing %s: %.120s", zf.Name, c, row)
					}
				}
				if !strings.Contains(row, `<Cell N="FontScale" V="1"/>`) {
					t.Fatalf("FontScale must be 1: %.200s", row)
				}
			}
		}
	}
	if rows == 0 || multi == 0 {
		t.Fatalf("expected multi-row Character sections, got rows=%d IX>=1 rows=%d", rows, multi)
	}
}
