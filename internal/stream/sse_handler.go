package stream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
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

	// Agent identifies which ADK agent produced this frame. Empty (or equal
	// to the run's root agent name) means the supervisor itself. A non-root
	// value (e.g. "deep_research") tells the UI to render this event as a
	// nested sub-agent activity.
	Agent string `json:"agent,omitempty"`

	// ParentToolCallID links a sub-agent frame back to the root agent's
	// tool_call that triggered the sub-agent run. Only set when Agent is
	// non-empty. Mirrors the persisted SubAgentEvent.ParentToolCallID so
	// the live stream and the message-history replay carry the same shape.
	ParentToolCallID string `json:"parent_tool_call_id,omitempty"`

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

// ToolEventRecord is the persisted shape of one tool call within an agent run.
// Stored as a JSON array on message.Extra so the frontend can re-render tool
// cards when the conversation is reloaded from history.
type ToolEventRecord struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	ArgsJSON string `json:"args_json,omitempty"`
	OK       bool   `json:"ok"`
	Content  string `json:"content,omitempty"`
	Error    string `json:"error,omitempty"`
}

// SubAgentEvent is one persisted event from a sub-agent's internal run.
//
// Stored as an ordered array on message.Extra so the frontend can replay the
// nested deep_research / sub-agent timeline after the conversation reloads.
// `ParentToolCallID` links the sub-event back to the root agent's tool_call
// that triggered the sub-agent, which keeps the door open for future nested
// UI (collapse/expand under that tool card) without needing to re-derive
// attribution from timing.
type SubAgentEvent struct {
	Seq              int    `json:"seq"`
	Agent            string `json:"agent"`
	ParentToolCallID string `json:"parent_tool_call_id,omitempty"`
	Type             string `json:"type"` // thinking | text | tool_call | tool_result | error

	// Optional payload by Type
	Content    string `json:"content,omitempty"`
	ToolCallID string `json:"tool_call_id,omitempty"`
	Name       string `json:"name,omitempty"`
	ArgsJSON   string `json:"args_json,omitempty"`
	OK         *bool  `json:"ok,omitempty"`
	Error      string `json:"error,omitempty"`
}

// RunCollector accumulates the full record of a single agent run so the
// service layer can persist it. It captures:
//   - root agent's ChatModel content + reasoning (concatenated)
//   - root agent's tool call / result events in order
//   - sub-agent events kept separately so they don't pollute the root
//     agent's final message content
//
// Thread safety: a single mutex guards all fields. The internal WaitGroup
// tracks pending OnEndWithStreamOutputFn goroutines so callers can Wait()
// for the run to fully drain before persisting.
type RunCollector struct {
	mu        sync.Mutex
	wg        sync.WaitGroup
	content   strings.Builder
	reasoning strings.Builder
	tools     []ToolEventRecord
	subEvents []SubAgentEvent
}

func NewRunCollector() *RunCollector {
	return &RunCollector{}
}

func (c *RunCollector) Wait() {
	c.wg.Wait()
}

func (c *RunCollector) Content() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.content.String()
}

func (c *RunCollector) Reasoning() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.reasoning.String()
}

func (c *RunCollector) Tools() []ToolEventRecord {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.tools) == 0 {
		return nil
	}
	out := make([]ToolEventRecord, len(c.tools))
	copy(out, c.tools)
	return out
}

// SubEvents returns the recorded sub-agent timeline in arrival order.
func (c *RunCollector) SubEvents() []SubAgentEvent {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.subEvents) == 0 {
		return nil
	}
	out := make([]SubAgentEvent, len(c.subEvents))
	copy(out, c.subEvents)
	return out
}

// ToolNameByID looks up the tool name we previously recorded for the given
// tool_call ID. Used as a last-resort fallback when an event arrives with
// neither a MessageVariant.ToolName nor a populated msg.Name (some provider
// SDKs leave tool result names empty).
func (c *RunCollector) ToolNameByID(id string) string {
	if id == "" {
		return ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, t := range c.tools {
		if t.ID == id {
			return t.Name
		}
	}
	for _, e := range c.subEvents {
		if e.ToolCallID == id && e.Name != "" {
			return e.Name
		}
	}
	return ""
}

// AppendSubEvent records one sub-agent event. Seq is assigned by the
// collector based on the current length so order is monotonic. Callers
// fill in the rest of the fields.
func (c *RunCollector) AppendSubEvent(ev SubAgentEvent) {
	c.mu.Lock()
	defer c.mu.Unlock()
	ev.Seq = len(c.subEvents) + 1
	c.subEvents = append(c.subEvents, ev)
}

func (c *RunCollector) appendContent(s string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.content.WriteString(s)
}

func (c *RunCollector) appendReasoning(s string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reasoning.WriteString(s)
}

func (c *RunCollector) startTool(id, name, args string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tools = append(c.tools, ToolEventRecord{ID: id, Name: name, ArgsJSON: args})
}

func (c *RunCollector) finishLastTool(ok bool, content, errMsg string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.tools) == 0 {
		return
	}
	last := &c.tools[len(c.tools)-1]
	last.OK = ok
	last.Content = content
	last.Error = errMsg
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
// If collector is non-nil, the same events are also accumulated into the
// collector so the caller can persist a full record of the run after
// collector.Wait() returns.
//
// Buf lifecycle (finish / error semantics) is managed by the caller; this
// handler only appends frames.
func NewSSEHandler(buf *StreamBuffer, collector *RunCollector) callbacks.Handler {
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
			if collector != nil {
				collector.startTool(id, info.Name, args)
			}
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
			if collector != nil {
				collector.finishLastTool(true, content, "")
			}
			return ctx
		}).
		OnEndWithStreamOutputFn(func(ctx context.Context, info *callbacks.RunInfo, sr *schema.StreamReader[callbacks.CallbackOutput]) context.Context {
			if info.Component != components.ComponentOfChatModel {
				sr.Close()
				return ctx
			}
			if collector != nil {
				collector.wg.Add(1)
			}
			go func() {
				defer func() {
					if collector != nil {
						collector.wg.Done()
					}
				}()
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
						if collector != nil {
							collector.appendReasoning(mo.Message.ReasoningContent)
						}
					}
					if mo.Message.Content != "" {
						buf.Append(Encode(Frame{Type: "text", Content: mo.Message.Content}))
						if collector != nil {
							collector.appendContent(mo.Message.Content)
						}
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
				if collector != nil {
					collector.finishLastTool(false, "", err.Error())
				}
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
