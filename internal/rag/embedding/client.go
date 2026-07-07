// Package embedding wraps an OpenAI-compatible /embeddings endpoint so the
// rest of the RAG pipeline never touches HTTP or batching directly.
//
// Default target is Aliyun DashScope in "compatible mode" (same wire shape
// as OpenAI's /v1/embeddings). Any other OpenAI-compatible provider works
// too — swap the base URL and model in EmbeddingConfig.
//
// Hand-rolled net/http rather than an SDK for the same reasons as
// internal/approval/llmclient.go: one endpoint, one call shape, no need
// for the streaming/tool-calling machinery that full SDKs carry.
package embedding

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/guyi-a/Interview-Agent/internal/config"
)

// Client encodes text into vectors. Safe for concurrent use — the
// underlying http.Client is, and no per-call state is kept.
type Client struct {
	apiKey     string
	baseURL    string // no trailing slash
	model      string
	dimensions int // 0 = don't send, let provider return native dim
	batchSize  int
	http       *http.Client
}

// maxBatchSize caps the auto-batching size regardless of config. DashScope
// compatible-mode allows 10 per call for text-embedding-v3; OpenAI's own
// endpoint allows more but we still want a ceiling so a bad env value
// doesn't produce 500-item requests that blow past provider limits or
// stall the whole pipeline on one slow batch.
const maxBatchSize = 100

// New returns nil when cfg.Enabled() is false so callers can gate RAG on
// "was the key configured" without a separate flag. Downstream code should
// treat nil as "RAG disabled" rather than panicking.
func New(cfg config.EmbeddingConfig) *Client {
	if !cfg.Enabled() {
		return nil
	}
	batch := cfg.BatchSize
	if batch <= 0 {
		batch = 10
	}
	if batch > maxBatchSize {
		batch = maxBatchSize
	}
	timeout := time.Duration(cfg.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &Client{
		apiKey:     cfg.APIKey,
		baseURL:    strings.TrimRight(cfg.BaseURL, "/"),
		model:      cfg.Model,
		dimensions: cfg.Dimensions,
		batchSize:  batch,
		http:       &http.Client{Timeout: timeout},
	}
}

// Dim returns the configured output dimensions. Zero means "provider
// decides" — mainly relevant when building the sqlite-vec table schema,
// which needs a fixed dim up front.
func (c *Client) Dim() int { return c.dimensions }

// Model exposes the underlying model id for logging / audit.
func (c *Client) Model() string { return c.model }

// Embed encodes a slice of texts into vectors, preserving input order.
// Long inputs are split into BatchSize-sized requests and stitched back
// together. Returns (nil, nil) for an empty input so callers can pass an
// empty slice without branching.
//
// Any request failure aborts the whole call — we don't return partial
// results. Downstream code that wants resilience should batch itself and
// call Embed per batch.
func (c *Client) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	// Provider quirk: empty strings usually 400. Reject up front rather
	// than sending a doomed request 10 items in.
	for i, t := range texts {
		if strings.TrimSpace(t) == "" {
			return nil, fmt.Errorf("embedding: input %d is empty", i)
		}
	}

	out := make([][]float32, 0, len(texts))
	for start := 0; start < len(texts); start += c.batchSize {
		end := start + c.batchSize
		if end > len(texts) {
			end = len(texts)
		}
		vecs, err := c.embedBatch(ctx, texts[start:end])
		if err != nil {
			// Include the batch range so a failure at input 137 is
			// findable in logs without re-running with more verbosity.
			// Only the range, never the input text — chunks may contain
			// resumes / private files.
			return nil, fmt.Errorf("embedding: batch [%d,%d): %w", start, end, err)
		}
		out = append(out, vecs...)
	}
	return out, nil
}

// EmbedOne is a convenience wrapper for the common single-query path.
func (c *Client) EmbedOne(ctx context.Context, text string) ([]float32, error) {
	vecs, err := c.Embed(ctx, []string{text})
	if err != nil {
		return nil, err
	}
	if len(vecs) != 1 {
		return nil, errBadResponse
	}
	return vecs[0], nil
}

// --- wire types ---------------------------------------------------------

type embedRequest struct {
	Model      string   `json:"model"`
	Input      []string `json:"input"`
	Dimensions int      `json:"dimensions,omitempty"`
	// encoding_format defaults to "float" on both DashScope and OpenAI —
	// omit to avoid needless bytes.
}

type embedResponse struct {
	Data []struct {
		Index     int       `json:"index"`
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
	Model string `json:"model"`
	Error *struct {
		Message string `json:"message"`
		Code    string `json:"code"`
	} `json:"error,omitempty"`
}

var (
	errNoAPIKey    = errors.New("embedding: no api key configured")
	errTimeout     = errors.New("embedding: request timed out")
	errBadResponse = errors.New("embedding: malformed response")
)

func (c *Client) embedBatch(ctx context.Context, batch []string) ([][]float32, error) {
	if c.apiKey == "" {
		return nil, errNoAPIKey
	}
	reqBody := embedRequest{
		Model:      c.model,
		Input:      batch,
		Dimensions: c.dimensions,
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("embedding: encode request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("embedding: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.http.Do(httpReq)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || isNetTimeout(err) {
			return nil, errTimeout
		}
		return nil, fmt.Errorf("embedding: http: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("embedding: read body: %w", err)
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("embedding: status %d: %s", resp.StatusCode, trimForLog(raw, 300))
	}
	var out embedResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, errBadResponse
	}
	if out.Error != nil && out.Error.Message != "" {
		return nil, fmt.Errorf("embedding: api error: %s", out.Error.Message)
	}
	if len(out.Data) != len(batch) {
		return nil, fmt.Errorf("embedding: got %d vectors for %d inputs", len(out.Data), len(batch))
	}
	// DashScope/OpenAI don't guarantee response order matches request
	// order — always reindex by data[i].index.
	vecs := make([][]float32, len(batch))
	for _, d := range out.Data {
		if d.Index < 0 || d.Index >= len(batch) {
			return nil, errBadResponse
		}
		if len(d.Embedding) == 0 {
			return nil, errBadResponse
		}
		// Guard against silent model / config drift: if we asked for a
		// fixed dim (needed to size the sqlite-vec column), a mismatch
		// must fail loudly here rather than later at insert time.
		if c.dimensions > 0 && len(d.Embedding) != c.dimensions {
			return nil, fmt.Errorf("embedding: dim mismatch: got %d, want %d", len(d.Embedding), c.dimensions)
		}
		vecs[d.Index] = d.Embedding
	}
	for i, v := range vecs {
		if v == nil {
			return nil, fmt.Errorf("embedding: missing vector at index %d", i)
		}
	}
	return vecs, nil
}

func isNetTimeout(err error) bool {
	type timeouter interface{ Timeout() bool }
	var t timeouter
	if errors.As(err, &t) && t.Timeout() {
		return true
	}
	return false
}

func trimForLog(b []byte, max int) string {
	if len(b) <= max {
		return string(b)
	}
	return string(b[:max]) + "…"
}
