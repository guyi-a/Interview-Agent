package approval

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
)

// llmClient is a minimal OpenAI-compatible chat/completions client. Just
// enough to run the approval classifier: one non-streaming request, plain
// JSON body in / JSON string out. Deliberately not exported and not a full
// abstraction — if a second call site appears, promote it to its own file.
//
// We hand-roll instead of pulling in eino's OpenAI adapter because (a) the
// adapter carries the ChatModel / ToolCallingChatModel machinery we don't
// need here, (b) the classifier's endpoint (DeepSeek) is a separate host
// with a separate key from the main model, and (c) 80 lines of net/http
// with no external dep beats another versioned dependency.
type llmClient struct {
	apiKey  string
	baseURL string // e.g. https://api.deepseek.com  (no trailing slash)
	http    *http.Client
}

func newLLMClient(apiKey, baseURL string, timeout time.Duration) *llmClient {
	return &llmClient{
		apiKey:  apiKey,
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    &http.Client{Timeout: timeout},
	}
}

// chatMessage matches the OpenAI wire format (role / content only — we
// don't send tool calls or images).
type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// llmRequest omits fields we never set (top_p, stream, tools, ...).
type llmRequest struct {
	Model          string          `json:"model"`
	Messages       []chatMessage   `json:"messages"`
	MaxTokens      int             `json:"max_tokens"`
	Temperature    float64         `json:"temperature"`
	ResponseFormat *responseFormat `json:"response_format,omitempty"`
}

// responseFormat forces the model to emit valid JSON. DeepSeek + OpenAI
// both honour {"type":"json_object"}.
type responseFormat struct {
	Type string `json:"type"`
}

type llmResponse struct {
	Choices []struct {
		Message chatMessage `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// Distinct error types let the caller distinguish "no key configured" (a
// deployment issue) from "endpoint timed out" (a runtime blip) from "the
// endpoint returned garbage" (probably a protocol version mismatch). Each
// case maps to a different reason string in the classifier's audit log.
var (
	errNoAPIKey    = errors.New("llmclient: no api key configured")
	errTimeout     = errors.New("llmclient: request timed out")
	errBadResponse = errors.New("llmclient: malformed response")
)

// chat runs a single non-streaming completion. Returns the assistant
// message content on success, or one of the sentinel errors above.
func (c *llmClient) chat(ctx context.Context, model string, msgs []chatMessage, maxTokens int, jsonMode bool) (string, error) {
	if c.apiKey == "" {
		return "", errNoAPIKey
	}
	req := llmRequest{
		Model:       model,
		Messages:    msgs,
		MaxTokens:   maxTokens,
		Temperature: 0,
	}
	if jsonMode {
		req.ResponseFormat = &responseFormat{Type: "json_object"}
	}
	body, err := json.Marshal(req)
	if err != nil {
		return "", fmt.Errorf("llmclient: encode request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("llmclient: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.http.Do(httpReq)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || isNetTimeout(err) {
			return "", errTimeout
		}
		return "", fmt.Errorf("llmclient: http: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("llmclient: read body: %w", err)
	}
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("llmclient: status %d: %s", resp.StatusCode, trimForLog(raw, 300))
	}
	var out llmResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", errBadResponse
	}
	if out.Error != nil && out.Error.Message != "" {
		return "", fmt.Errorf("llmclient: api error: %s", out.Error.Message)
	}
	if len(out.Choices) == 0 {
		return "", errBadResponse
	}
	content := strings.TrimSpace(out.Choices[0].Message.Content)
	if content == "" {
		return "", errBadResponse
	}
	return content, nil
}

// isNetTimeout unwraps net-level timeout errors that don't wrap
// context.DeadlineExceeded (e.g. http.Client.Timeout expiration).
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
