package stream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync/atomic"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

// Frame is the wire shape of one SSE event. Field omitempty so each frame
// type only carries what it needs. New frame types should add new fields
// here rather than overloading existing ones.
type Frame struct {
	Type string `json:"type"`

	Content string `json:"content,omitempty"` // text / thinking / tool_result(ok=true)
	Message string `json:"message,omitempty"` // error.message

	// Tool call / result fields
	ID       string `json:"id,omitempty"`        // tc-N, links tool_call ↔ tool_result
	Name     string `json:"name,omitempty"`      // tool name
	ArgsJSON string `json:"args_json,omitempty"` // tool_call arguments JSON
	OK       *bool  `json:"ok,omitempty"`        // tool_result success flag (pointer so we can emit even when false)
	Error    string `json:"error,omitempty"`     // tool_result(ok=false) error message

	// Project bound (emitted by create_workspace tool, not by this handler)
	ProjectID     string `json:"project_id,omitempty"`
	ProjectName   string `json:"project_name,omitempty"`
	WorkspacePath string `json:"workspace_path,omitempty"`

	// Usage
	Prompt int `json:"prompt,omitempty"`
	Reply  int `json:"completion,omitempty"`
	Total  int `json:"total,omitempty"`
}

// Encode serializes a Frame into an SSE-formatted `data: ...\n\n` byte slice.
// Exported so other packages (tools) can push frames directly into a buffer.
func Encode(f Frame) []byte {
	data, _ := json.Marshal(f)
	out := make([]byte, 0, len(data)+8)
	out = append(out, []byte("data: ")...)
	out = append(out, data...)
	out = append(out, '\n', '\n')
	return out
}

// boolPtr is a tiny helper for the OK field above.
func boolPtr(b bool) *bool { return &b }

// NewSSEHandler builds an eino callback handler that translates model + tool
// events into SSE frames written to buf. Tool call IDs are allocated by a
// per-handler counter (one handler per agent run / per stream).
//
// Buf lifecycle (finish / error semantics) is managed by the caller; this
// handler only appends frames.
func NewSSEHandler(buf *StreamBuffer) callbacks.Handler {
	var toolCounter int64
	var lastToolID atomic.Value // string — id of the in-flight tool call

	return callbacks.NewHandlerBuilder().
		OnStartFn(func(ctx context.Context, info *callbacks.RunInfo, input callbacks.CallbackInput) context.Context {
			if info.Component != components.ComponentOfTool {
				return ctx
			}
			n := atomic.AddInt64(&toolCounter, 1)
			id := fmt.Sprintf("tc-%d", n)
			lastToolID.Store(id)
			args := ""
			if ti := tool.ConvCallbackInput(input); ti != nil {
				args = ti.ArgumentsInJSON
			}
			buf.Append(Encode(Frame{
				Type:     "tool_call",
				ID:       id,
				Name:     info.Name,
				ArgsJSON: args,
			}))
			return ctx
		}).
		OnEndFn(func(ctx context.Context, info *callbacks.RunInfo, output callbacks.CallbackOutput) context.Context {
			if info.Component != components.ComponentOfTool {
				return ctx
			}
			to := tool.ConvCallbackOutput(output)
			content := ""
			if to != nil {
				content = to.Response
			}
			id, _ := lastToolID.Load().(string)
			buf.Append(Encode(Frame{
				Type:    "tool_result",
				ID:      id,
				Name:    info.Name,
				OK:      boolPtr(true),
				Content: content,
			}))
			return ctx
		}).
		OnEndWithStreamOutputFn(func(ctx context.Context, info *callbacks.RunInfo, sr *schema.StreamReader[callbacks.CallbackOutput]) context.Context {
			if info.Component != components.ComponentOfChatModel {
				sr.Close()
				return ctx
			}
			go func() {
				defer sr.Close()
				for {
					raw, err := sr.Recv()
					if errors.Is(err, io.EOF) {
						return
					}
					if err != nil {
						buf.Append(Encode(Frame{Type: "error", Message: err.Error()}))
						return
					}
					mo := model.ConvCallbackOutput(raw)
					if mo == nil || mo.Message == nil {
						continue
					}
					if mo.Message.ReasoningContent != "" {
						buf.Append(Encode(Frame{Type: "thinking", Content: mo.Message.ReasoningContent}))
					}
					if mo.Message.Content != "" {
						buf.Append(Encode(Frame{Type: "text", Content: mo.Message.Content}))
					}
					if mo.TokenUsage != nil {
						buf.Append(Encode(Frame{
							Type:   "usage",
							Prompt: mo.TokenUsage.PromptTokens,
							Reply:  mo.TokenUsage.CompletionTokens,
							Total:  mo.TokenUsage.TotalTokens,
						}))
					}
				}
			}()
			return ctx
		}).
		OnErrorFn(func(ctx context.Context, info *callbacks.RunInfo, err error) context.Context {
			if info.Component == components.ComponentOfTool {
				id, _ := lastToolID.Load().(string)
				buf.Append(Encode(Frame{
					Type:  "tool_result",
					ID:    id,
					Name:  info.Name,
					OK:    boolPtr(false),
					Error: err.Error(),
				}))
				return ctx
			}
			buf.Append(Encode(Frame{Type: "error", Message: err.Error()}))
			return ctx
		}).
		Build()
}

// FinalizeOK closes the stream successfully — appends a `done` frame
// and marks the buffer complete.
func FinalizeOK(buf *StreamBuffer) {
	buf.Append(Encode(Frame{Type: "done"}))
	buf.Finish()
}

// FinalizeErr closes the stream with an error.
func FinalizeErr(buf *StreamBuffer, err error) {
	buf.Append(Encode(Frame{Type: "error", Message: err.Error()}))
	buf.Finish()
}
