// Command svg2miro converts an SVG diagram into native, editable Miro items
// (frames, shapes, texts and connectors) through the Miro REST API v2.
//
// Graphviz output (dot, protodot, ...) is recognised: clusters become frames,
// nodes become shapes carrying their label text, and edges become real
// connectors attached at the same point (record port) as in the drawing.
// Other SVGs are mapped primitive by primitive.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/seamia/svg2miro/internal/miro"
	"github.com/seamia/svg2miro/internal/preview"
	"github.com/seamia/svg2miro/internal/scene"
	"github.com/seamia/svg2miro/internal/svgdoc"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "svg2miro:", err)
		os.Exit(1)
	}
}

func run() error {
	l := miro.DefaultLayout()
	var (
		board       = flag.String("board", os.Getenv("MIRO_BOARD"), "target board id (env MIRO_BOARD)")
		newBoard    = flag.String("new-board", "", "create a new board with this name instead of using -board")
		token       = flag.String("token", "", "Miro access token (default: env MIRO_TOKEN)")
		api         = flag.String("api", "https://api.miro.com", "Miro API base URL")
		mode        = flag.String("mode", "compact", "compact: one shape per node | detailed: one shape per record cell")
		dryRun      = flag.Bool("dry-run", false, "do not call Miro; record the API calls instead")
		planOut     = flag.String("plan", "", "write the recorded API calls (JSON) to this file (implies -dry-run)")
		previewOut  = flag.String("preview", "", "write an SVG preview of the board layout to this file")
		conc        = flag.Int("concurrency", 4, "parallel API requests")
		bulk        = flag.Bool("bulk", true, "create shapes/texts with the bulk endpoint (20 per call)")
		parentFrame = flag.Bool("frames-as-parents", true, "attach items to the frame (cluster) that contains them")
		verbose     = flag.Bool("v", false, "verbose progress")
	)
	flag.Float64Var(&l.Scale, "scale", l.Scale, "board units per SVG unit")
	flag.Float64Var(&l.FontScale, "font-scale", l.FontScale, "extra font size multiplier")
	flag.Float64Var(&l.OriginX, "x", 0, "board x of the diagram's top-left corner")
	flag.Float64Var(&l.OriginY, "y", 0, "board y of the diagram's top-left corner")
	flag.StringVar(&l.MonoFont, "mono-font", l.MonoFont, "Miro fontFamily for monospace text")
	flag.StringVar(&l.SansFont, "sans-font", l.SansFont, "Miro fontFamily for sans-serif text")
	flag.StringVar(&l.SerifFont, "serif-font", l.SerifFont, "Miro fontFamily for serif text")
	flag.StringVar(&l.ConnectorShape, "connector", l.ConnectorShape, "connector shape: curved | straight | elbowed")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: svg2miro [flags] input.svg\n\n")
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() != 1 {
		flag.Usage()
		os.Exit(2)
	}
	if *planOut != "" {
		*dryRun = true
	}
	if *mode != "compact" && *mode != "detailed" {
		return fmt.Errorf("-mode must be compact or detailed")
	}
	switch l.ConnectorShape {
	case "curved", "straight", "elbowed":
	default:
		return fmt.Errorf("-connector must be curved, straight or elbowed")
	}

	input := flag.Arg(0)
	f, err := os.Open(input)
	if err != nil {
		return err
	}
	root, err := svgdoc.Parse(f)
	f.Close()
	if err != nil {
		return err
	}
	s := scene.FromSVG(root, scene.Options{Detailed: *mode == "detailed"})
	fmt.Fprintf(os.Stderr, "parsed %s (%s): %d frames, %d shapes, %d texts, %d connectors\n",
		filepath.Base(input), s.Source, len(s.Frames), len(s.Shapes), len(s.Texts), len(s.Connectors))
	for _, w := range s.Warnings {
		fmt.Fprintln(os.Stderr, "  warning:", w)
	}

	if *previewOut != "" {
		pf, err := os.Create(*previewOut)
		if err != nil {
			return err
		}
		if err := preview.Render(pf, s, l); err != nil {
			pf.Close()
			return err
		}
		pf.Close()
		fmt.Fprintf(os.Stderr, "preview written to %s\n", *previewOut)
		if !*dryRun && *board == "" && *newBoard == "" {
			return nil // preview-only run
		}
	}

	client := &miro.Client{BaseURL: strings.TrimRight(*api, "/"), HTTP: &http.Client{Timeout: 60 * time.Second}}
	var rec *miro.Recorder
	if *dryRun {
		rec = &miro.Recorder{}
		client.HTTP = &http.Client{Transport: rec}
		client.Token = "dry-run"
	} else {
		client.Token = *token
		if client.Token == "" {
			client.Token = os.Getenv("MIRO_TOKEN")
		}
		if client.Token == "" {
			return fmt.Errorf("no access token: set MIRO_TOKEN or pass -token (or use -dry-run)")
		}
	}
	if *verbose {
		client.Logf = func(f string, a ...any) { fmt.Fprintf(os.Stderr, f+"\n", a...) }
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	viewLink := ""
	if *newBoard != "" {
		id, link, err := client.CreateBoard(ctx, *newBoard, "Imported from "+filepath.Base(input)+" by svg2miro")
		if err != nil {
			return fmt.Errorf("create board: %w", err)
		}
		*board, viewLink = id, link
		fmt.Fprintf(os.Stderr, "created board %s\n", id)
	}
	if *board == "" {
		if !*dryRun {
			return fmt.Errorf("no board: pass -board ID, set MIRO_BOARD, or use -new-board NAME")
		}
		*board = "dry-board"
	}

	start := time.Now()
	last := ""
	res, err := miro.Publish(ctx, client, *board, s, l, miro.Options{
		Concurrency: *conc, Bulk: *bulk, ParentFrames: *parentFrame,
		Progress: func(stage string, done, total int) {
			if *verbose || done == total {
				line := fmt.Sprintf("  %-10s %d/%d", stage, done, total)
				if line != last {
					fmt.Fprintln(os.Stderr, line)
					last = line
				}
			}
		},
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "done in %s: %d frames, %d shapes, %d texts, %d connectors, %d errors\n",
		time.Since(start).Round(time.Millisecond), res.Frames, res.Shapes, res.Texts, res.Connectors, len(res.Errors))
	for i, e := range res.Errors {
		if i == 20 {
			fmt.Fprintf(os.Stderr, "  ... and %d more\n", len(res.Errors)-20)
			break
		}
		fmt.Fprintln(os.Stderr, "  error:", e)
	}
	if rec != nil {
		fmt.Fprintf(os.Stderr, "dry run: %d API calls recorded\n", len(rec.Calls))
		if *planOut != "" {
			b, _ := json.MarshalIndent(rec.Calls, "", "  ")
			if err := os.WriteFile(*planOut, b, 0o644); err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "plan written to %s\n", *planOut)
		}
	} else if viewLink != "" {
		fmt.Println(viewLink)
	} else {
		fmt.Printf("https://miro.com/app/board/%s/\n", *board)
	}
	if len(res.Errors) > 0 {
		return fmt.Errorf("%d items failed", len(res.Errors))
	}
	return nil
}
