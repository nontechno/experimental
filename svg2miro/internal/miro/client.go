// Package miro publishes a scene to a Miro board through the REST API v2.
package miro

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"time"
)

// Client is a minimal Miro REST API v2 client with retry on rate limits.
type Client struct {
	BaseURL    string // default https://api.miro.com
	Token      string
	HTTP       *http.Client
	MaxRetries int
	Logf       func(format string, args ...any)
}

// APIError is a non-2xx response.
type APIError struct {
	Status int
	Method string
	Path   string
	Body   string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("miro %s %s: HTTP %d: %s", e.Method, e.Path, e.Status, e.Body)
}

func (c *Client) logf(f string, a ...any) {
	if c.Logf != nil {
		c.Logf(f, a...)
	}
}

// Do sends a JSON request and decodes the JSON response into out.
func (c *Client) Do(ctx context.Context, method, path string, in, out any) error {
	base := c.BaseURL
	if base == "" {
		base = "https://api.miro.com"
	}
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 60 * time.Second}
	}
	var body []byte
	if in != nil {
		var err error
		if body, err = json.Marshal(in); err != nil {
			return err
		}
	}
	retries := c.MaxRetries
	if retries == 0 {
		retries = 6
	}
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, method, base+path, bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+c.Token)
		req.Header.Set("Accept", "application/json")
		if in != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := hc.Do(req)
		if err != nil {
			if attempt < retries && ctx.Err() == nil {
				c.wait(ctx, attempt, "")
				continue
			}
			return err
		}
		data, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			if attempt < retries {
				c.logf("miro %s %s: HTTP %d, retrying", method, path, resp.StatusCode)
				c.wait(ctx, attempt, resp.Header.Get("Retry-After"))
				continue
			}
		}
		if resp.StatusCode/100 != 2 {
			return &APIError{Status: resp.StatusCode, Method: method, Path: path, Body: truncate(string(data), 600)}
		}
		if out != nil && len(data) > 0 {
			if err := json.Unmarshal(data, out); err != nil {
				return fmt.Errorf("miro %s %s: decode: %w", method, path, err)
			}
		}
		return nil
	}
}

func (c *Client) wait(ctx context.Context, attempt int, retryAfter string) {
	d := time.Duration(math.Min(30, math.Pow(2, float64(attempt)))) * time.Second
	if s, err := strconv.Atoi(retryAfter); err == nil && s > 0 {
		d = time.Duration(s) * time.Second
	}
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

type idResp struct {
	ID    string `json:"id"`
	Links struct {
		Self string `json:"self"`
	} `json:"links"`
	ViewLink string `json:"viewLink"`
}

// CreateBoard creates a new board and returns its id and view link.
func (c *Client) CreateBoard(ctx context.Context, name, description string) (id, viewLink string, err error) {
	var r idResp
	err = c.Do(ctx, http.MethodPost, "/v2/boards", map[string]any{"name": name, "description": description}, &r)
	return r.ID, r.ViewLink, err
}

// Create posts one item to /v2/boards/{board}/{kind} (frames|shapes|texts|connectors).
func (c *Client) Create(ctx context.Context, board, kind string, payload any) (string, error) {
	var r idResp
	if err := c.Do(ctx, http.MethodPost, "/v2/boards/"+board+"/"+kind, payload, &r); err != nil {
		return "", err
	}
	if r.ID == "" {
		return "", fmt.Errorf("miro create %s: empty id in response", kind)
	}
	return r.ID, nil
}

// CreateBulk posts up to 20 items in one transactional call.
func (c *Client) CreateBulk(ctx context.Context, board string, items []Item) ([]string, error) {
	var r struct {
		Data []idResp `json:"data"`
	}
	if err := c.Do(ctx, http.MethodPost, "/v2/boards/"+board+"/items/bulk", items, &r); err != nil {
		return nil, err
	}
	if len(r.Data) != len(items) {
		return nil, fmt.Errorf("miro bulk: got %d ids for %d items", len(r.Data), len(items))
	}
	ids := make([]string, len(items))
	for i, d := range r.Data {
		ids[i] = d.ID
	}
	return ids, nil
}
