package miro

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"

	"github.com/seamia/svg2miro/internal/scene"
)

// Options control publishing.
type Options struct {
	Concurrency  int  // parallel requests for connectors / single-item mode
	Bulk         bool // use /items/bulk (20 items per call) for shapes & texts
	ParentFrames bool // attach items to the cluster frame that contains them
	Progress     func(stage string, done, total int)
}

// Result summarizes a publish run.
type Result struct {
	Frames, Shapes, Texts, Connectors int
	IDs                               map[string]string // scene key -> Miro item id
	Errors                            []string
}

type pending struct {
	key   string
	kind  string // shapes | texts
	item  Item
	box   scene.Rect
	isTxt bool
}

// fatal reports errors that make further requests pointless.
func fatal(err error) bool {
	var ae *APIError
	if errors.As(err, &ae) {
		return ae.Status == http.StatusUnauthorized || ae.Status == http.StatusForbidden || ae.Status == http.StatusNotFound
	}
	return false
}

// Publish creates the scene on the given board.
func Publish(ctx context.Context, c *Client, board string, s *scene.Scene, l Layout, o Options) (*Result, error) {
	if o.Concurrency <= 0 {
		o.Concurrency = 4
	}
	prog := o.Progress
	if prog == nil {
		prog = func(string, int, int) {}
	}
	l.Bind(s)
	res := &Result{IDs: map[string]string{}}
	var mu sync.Mutex
	fail := func(what string, err error) {
		mu.Lock()
		res.Errors = append(res.Errors, fmt.Sprintf("%s: %v", what, err))
		mu.Unlock()
	}

	// 1. frames (few, sequential, created first so they sit underneath)
	type frameRef struct {
		id  string
		box scene.Rect
		it  Item
	}
	var frames []frameRef
	for i, f := range s.Frames {
		it := l.FramePayload(f)
		it.Type = ""
		id, err := c.Create(ctx, board, "frames", it)
		if err != nil {
			if fatal(err) {
				return res, err
			}
			fail("frame "+f.Title, err)
			continue
		}
		res.IDs["frame:"+f.Key] = id
		res.Frames++
		frames = append(frames, frameRef{id, f.Box, it})
		prog("frames", i+1, len(s.Frames))
	}

	// 2. shapes then texts, in scene (z-)order
	var items []pending
	for _, sh := range s.Shapes {
		items = append(items, pending{key: sh.Key, kind: "shapes", item: l.ShapePayload(sh), box: sh.Box})
	}
	for _, t := range s.Texts {
		items = append(items, pending{key: t.Key, kind: "texts", item: l.TextPayload(t), box: t.Box, isTxt: true})
	}
	if o.ParentFrames {
		for i := range items {
			var best *frameRef
			for j := range frames {
				f := &frames[j]
				if f.box.ContainsRect(items[i].box, 1) && (best == nil || f.box.Area() < best.box.Area()) {
					best = f
				}
			}
			if best != nil {
				fx := best.it.Position.X - best.it.Geometry.Width/2
				fy := best.it.Position.Y - best.it.Geometry.Height/2
				items[i].item.Parent = &Parent{ID: best.id}
				items[i].item.Position.X = round2(items[i].item.Position.X - fx)
				items[i].item.Position.Y = round2(items[i].item.Position.Y - fy)
			}
		}
	}
	record := func(p pending, id string) {
		mu.Lock()
		res.IDs[p.key] = id
		if p.isTxt {
			res.Texts++
		} else {
			res.Shapes++
		}
		mu.Unlock()
	}
	createOne := func(p pending) error {
		it := p.item
		it.Type = ""
		id, err := c.Create(ctx, board, p.kind, it)
		if err != nil {
			fail(p.kind+" "+p.key, err)
			return err
		}
		record(p, id)
		return nil
	}

	done := 0
	if o.Bulk {
		for start := 0; start < len(items); start += 20 {
			end := min(start+20, len(items))
			batch := items[start:end]
			payload := make([]Item, len(batch))
			for i, p := range batch {
				payload[i] = p.item
			}
			ids, err := c.CreateBulk(ctx, board, payload)
			if err != nil {
				if fatal(err) && start == 0 && len(frames) == 0 {
					return res, err
				}
				// transactional failure: retry one by one to isolate bad items
				for _, p := range batch {
					if err := createOne(p); err != nil && fatal(err) {
						return res, err
					}
				}
			} else {
				for i, p := range batch {
					record(p, ids[i])
				}
			}
			done = end
			prog("items", done, len(items))
		}
	} else {
		var firstFatal error
		var once sync.Once
		runParallel(ctx, len(items), o.Concurrency, func(i int) {
			if err := createOne(items[i]); err != nil && fatal(err) {
				once.Do(func() { firstFatal = err })
			}
			mu.Lock()
			done++
			d := done
			mu.Unlock()
			prog("items", d, len(items))
		})
		if firstFatal != nil && res.Shapes+res.Texts == 0 {
			return res, firstFatal
		}
	}

	// 3. connectors (item phase is complete; take a read-only snapshot)
	ids := make(map[string]string, len(res.IDs))
	for k, v := range res.IDs {
		ids[k] = v
	}
	shapes := make(map[string]*scene.Shape, len(s.Shapes))
	for i := range s.Shapes {
		shapes[s.Shapes[i].Key] = &s.Shapes[i]
	}
	done = 0
	runParallel(ctx, len(s.Connectors), o.Concurrency, func(i int) {
		cn := s.Connectors[i]
		fromID, toID := ids[cn.From], ids[cn.To]
		from, to := shapes[cn.From], shapes[cn.To]
		if fromID == "" || toID == "" || from == nil || to == nil {
			fail("connector "+cn.Key, errors.New("endpoint item was not created"))
		} else if id, err := c.Create(ctx, board, "connectors", l.ConnectorPayloadFor(cn, *from, *to, fromID, toID)); err != nil {
			fail("connector "+cn.Key, err)
		} else {
			mu.Lock()
			res.IDs[cn.Key] = id
			res.Connectors++
			mu.Unlock()
		}
		mu.Lock()
		done++
		d := done
		mu.Unlock()
		prog("connectors", d, len(s.Connectors))
	})
	return res, nil
}

func runParallel(ctx context.Context, n, workers int, fn func(i int)) {
	ch := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range ch {
				fn(i)
			}
		}()
	}
	for i := 0; i < n && ctx.Err() == nil; i++ {
		ch <- i
	}
	close(ch)
	wg.Wait()
}
