package miro

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
)

// RecordedCall is one request captured by the Recorder.
type RecordedCall struct {
	Method string          `json:"method"`
	Path   string          `json:"path"`
	Body   json.RawMessage `json:"body,omitempty"`
}

// Recorder is an http.RoundTripper that answers every Miro call locally
// with synthetic ids. It powers --dry-run and produces a replayable plan.
type Recorder struct {
	mu    sync.Mutex
	seq   int
	Calls []RecordedCall
}

func (r *Recorder) nextID() string {
	r.seq++
	return fmt.Sprintf("dry-%06d", r.seq)
}

// RoundTrip implements http.RoundTripper.
func (r *Recorder) RoundTrip(req *http.Request) (*http.Response, error) {
	var body []byte
	if req.Body != nil {
		body, _ = io.ReadAll(req.Body)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Calls = append(r.Calls, RecordedCall{req.Method, req.URL.Path, json.RawMessage(body)})

	var out any
	switch {
	case strings.HasSuffix(req.URL.Path, "/items/bulk"):
		var items []json.RawMessage
		_ = json.Unmarshal(body, &items)
		data := make([]map[string]string, len(items))
		for i := range items {
			data[i] = map[string]string{"id": r.nextID()}
		}
		out = map[string]any{"data": data}
	case req.URL.Path == "/v2/boards":
		out = map[string]string{"id": "dry-board", "viewLink": "https://miro.com/app/board/dry-board/"}
	default:
		out = map[string]string{"id": r.nextID()}
	}
	b, _ := json.Marshal(out)
	return &http.Response{
		StatusCode: http.StatusCreated,
		Header:     http.Header{"Content-Type": {"application/json"}},
		Body:       io.NopCloser(bytes.NewReader(b)),
		Request:    req,
	}, nil
}
