package stream

import (
	"context"
	"errors"
	"io"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"

	"github.com/guyi-a/Interview-Agent/internal/agent/toolerr"
)

// ConsumeADKEvents drives an ADK Runner's AsyncIterator, translating every
// AgentEvent into SSE frames written to buf, and accumulating data into
// collector for later persistence.
//
// Routing rules (matching docs/adk-api-notes.md §8):
//   - ev.Err != nil                      → "error" frame, return that error
//   - ev.AgentName == rootName           → root path:
//       streaming/Assistant → per-chunk "thinking"/"text"; concat → "tool_call"
//       Tool                → "tool_result"
//       (writes to collector.content / reasoning / tools)
//   - ev.AgentName != rootName           → sub-agent path:
//       same shape of SSE frames, but each frame.agent = ev.AgentName,
//       writes go to collector.subEvents instead so the persisted root
//       message content stays clean. Sub-agent tool events are linked to
//       the root tool_call that triggered them via parent_tool_call_id.
//
// Returns nil on clean stream exhaustion (caller calls FinalizeOK then),
// or the first error encountered (caller calls FinalizeErr then).
// Does NOT emit "done" or finalize the buffer — caller's responsibility.
func ConsumeADKEvents(
	ctx context.Context,
	iter *adk.AsyncIterator[*adk.AgentEvent],
	rootName string,
	buf *StreamBuffer,
	collector *RunCollector,
) error {
	router := &subAgentRouter{rootName: rootName, active: map[string]string{}}

	for {
		// Honor ctx cancel — if the caller cancelled the run, stop draining.
		// The iterator itself doesn't take a ctx, so we check here.
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		ev, ok := iter.Next()
		if !ok {
			return nil
		}

		if ev.Err != nil {
			return ev.Err
		}

		if ev.Output == nil || ev.Output.MessageOutput == nil {
			// Action-only event (Exit / Interrupted / TransferToAgent). First
			// phase ignores these — none should fire in our config.
			continue
		}

		isRoot := ev.AgentName == rootName
		mv := ev.Output.MessageOutput

		switch {
		case mv.IsStreaming:
			if err := drainAssistantStream(isRoot, ev.AgentName, router, mv.MessageStream, buf, collector); err != nil {
				return err
			}

		case mv.Role == schema.Tool:
			if mv.Message == nil {
				continue
			}
			emitToolResult(ctx, isRoot, ev.AgentName, router, mv.ToolName, mv.Message, buf, collector)

		default:
			// Non-streaming assistant message (EnableStreaming=false path).
			// Our service runs with EnableStreaming=true, so this is mostly
			// defensive — but handle it so we don't silently swallow content.
			if mv.Message == nil {
				continue
			}
			emitNonStreamAssistant(isRoot, ev.AgentName, router, mv.Message, buf, collector)
		}
	}
}

// subAgentRouter tracks the linkage between root-level tool_calls and the
// sub-agent events that run inside them. When the root agent calls
// deep_research with id=toolu_X, every subsequent event from
// ev.AgentName="deep_research" is attributed to toolu_X via
// parent_tool_call_id, until the matching root-level tool_result arrives.
//
// Single-threaded (only ConsumeADKEvents touches it) so no lock needed.
type subAgentRouter struct {
	rootName string
	active   map[string]string // sub-agent name → root tool_call_id
}

func (r *subAgentRouter) noteRootToolCall(toolName, callID string) {
	if toolName == "" || callID == "" {
		return
	}
	r.active[toolName] = callID
}

func (r *subAgentRouter) noteRootToolResult(callID string) {
	for name, id := range r.active {
		if id == callID {
			delete(r.active, name)
			return
		}
	}
}

func (r *subAgentRouter) parentForAgent(agentName string) string {
	return r.active[agentName]
}

// agentFieldFor returns "" for root events (so the SSE frame omits the agent
// field and stays backward-compatible) and the sub-agent name otherwise.
func (r *subAgentRouter) agentFieldFor(isRoot bool, agentName string) string {
	if isRoot {
		return ""
	}
	return agentName
}

// drainAssistantStream consumes one streaming assistant event. Per chunk:
// emit text/thinking SSE frames; accumulate chunks for later concat.
// At stream end: ConcatMessages → emit tool_call frames for ToolCalls;
// emit usage frame if ResponseMeta carries token counts.
//
// Root-agent chunks feed collector.content/reasoning; sub-agent chunks
// feed collector.subEvents instead, keeping the root message's persisted
// content free of nested noise.
func drainAssistantStream(
	isRoot bool,
	agentName string,
	router *subAgentRouter,
	sr *schema.StreamReader[*schema.Message],
	buf *StreamBuffer,
	collector *RunCollector,
) error {
	if sr == nil {
		return nil
	}
	defer sr.Close()

	agentField := router.agentFieldFor(isRoot, agentName)
	parentID := ""
	if !isRoot {
		parentID = router.parentForAgent(agentName)
	}

	var chunks []*schema.Message
	for {
		chunk, err := sr.Recv()
		if err != nil {
			if isEOF(err) {
				break
			}
			return err
		}
		if chunk == nil {
			continue
		}
		chunks = append(chunks, chunk)

		if chunk.ReasoningContent != "" {
			buf.Append(Encode(Frame{
				Type:             "thinking",
				Agent:            agentField,
				ParentToolCallID: parentID,
				Content:          chunk.ReasoningContent,
			}))
			if collector != nil {
				if isRoot {
					collector.appendReasoning(chunk.ReasoningContent)
				} else {
					collector.AppendSubEvent(SubAgentEvent{
						Agent:            agentName,
						ParentToolCallID: parentID,
						Type:             "thinking",
						Content:          chunk.ReasoningContent,
					})
				}
			}
		}
		if chunk.Content != "" {
			buf.Append(Encode(Frame{
				Type:             "text",
				Agent:            agentField,
				ParentToolCallID: parentID,
				Content:          chunk.Content,
			}))
			if collector != nil {
				if isRoot {
					collector.appendContent(chunk.Content)
				} else {
					collector.AppendSubEvent(SubAgentEvent{
						Agent:            agentName,
						ParentToolCallID: parentID,
						Type:             "text",
						Content:          chunk.Content,
					})
				}
			}
		}
	}

	if len(chunks) == 0 {
		return nil
	}
	full, cErr := schema.ConcatMessages(chunks)
	if cErr != nil || full == nil {
		return nil
	}

	for _, tc := range full.ToolCalls {
		buf.Append(Encode(Frame{
			Type:             "tool_call",
			Agent:            agentField,
			ParentToolCallID: parentID,
			ID:               tc.ID,
			Name:             tc.Function.Name,
			ArgsJSON:         tc.Function.Arguments,
		}))
		if collector != nil {
			if isRoot {
				collector.startTool(tc.ID, tc.Function.Name, tc.Function.Arguments)
				// Remember which root tool_call started this sub-agent, so
				// the sub-agent's events can attribute themselves.
				router.noteRootToolCall(tc.Function.Name, tc.ID)
			} else {
				collector.AppendSubEvent(SubAgentEvent{
					Agent:            agentName,
					ParentToolCallID: parentID,
					Type:             "tool_call",
					ToolCallID:       tc.ID,
					Name:             tc.Function.Name,
					ArgsJSON:         tc.Function.Arguments,
				})
			}
		}
	}

	if full.ResponseMeta != nil && full.ResponseMeta.Usage != nil {
		u := full.ResponseMeta.Usage
		buf.Append(Encode(Frame{
			Type:             "usage",
			Agent:            agentField,
			ParentToolCallID: parentID,
			Prompt:           u.PromptTokens,
			Reply:            u.CompletionTokens,
			Total:            u.TotalTokens,
		}))
	}

	// Record this turn's structured shape for the service layer's raw-row
	// persistence. Only root events flow into turns — sub-agent internal
	// turns live in collector.subEvents instead.
	if isRoot && collector != nil {
		tcs := make([]ToolCallRecord, 0, len(full.ToolCalls))
		for _, tc := range full.ToolCalls {
			tcs = append(tcs, ToolCallRecord{
				ID:       tc.ID,
				Name:     tc.Function.Name,
				ArgsJSON: tc.Function.Arguments,
			})
		}
		collector.OpenTurn(full.Content, full.ReasoningContent, tcs)
	}
	return nil
}

func emitToolResult(
	ctx context.Context,
	isRoot bool,
	agentName string,
	router *subAgentRouter,
	toolName string,
	msg *schema.Message,
	buf *StreamBuffer,
	collector *RunCollector,
) {
	// Resolve the tool name with a three-tier fallback: ADK's MessageVariant
	// → provider's msg.Name → recorded tool_call by id (some provider SDKs
	// leave the result message's Name empty).
	name := toolName
	if name == "" {
		name = msg.Name
	}
	if name == "" && collector != nil {
		name = collector.ToolNameByID(msg.ToolCallID)
	}

	agentField := router.agentFieldFor(isRoot, agentName)
	parentID := ""
	if !isRoot {
		parentID = router.parentForAgent(agentName)
	}

	// Did toolerr.Middleware rescue this call from a failure? If yes, emit
	// ok=false with the original error message so the UI shows a real
	// failure state, even though ADK saw the rescued ToolOutput as success.
	if errMsg, failed := toolerr.FromContext(ctx).Lookup(msg.ToolCallID); failed {
		buf.Append(Encode(Frame{
			Type:             "tool_result",
			Agent:            agentField,
			ParentToolCallID: parentID,
			ID:               msg.ToolCallID,
			Name:             name,
			OK:               boolPtr(false),
			Error:            errMsg,
		}))
		if collector != nil {
			if isRoot {
				collector.finishLastTool(false, "", errMsg)
				collector.AttachToolResult(ToolResultRecord{
					CallID: msg.ToolCallID,
					Name:   name,
					OK:     false,
					Error:  errMsg,
				})
				router.noteRootToolResult(msg.ToolCallID)
			} else {
				collector.AppendSubEvent(SubAgentEvent{
					Agent:            agentName,
					ParentToolCallID: parentID,
					Type:             "tool_result",
					ToolCallID:       msg.ToolCallID,
					Name:             name,
					OK:               boolPtr(false),
					Error:            errMsg,
				})
			}
		}
		return
	}

	buf.Append(Encode(Frame{
		Type:             "tool_result",
		Agent:            agentField,
		ParentToolCallID: parentID,
		ID:               msg.ToolCallID,
		Name:             name,
		OK:               boolPtr(true),
		Content:          msg.Content,
	}))
	if collector != nil {
		if isRoot {
			collector.finishLastTool(true, msg.Content, "")
			collector.AttachToolResult(ToolResultRecord{
				CallID:  msg.ToolCallID,
				Name:    name,
				OK:      true,
				Content: msg.Content,
			})
			router.noteRootToolResult(msg.ToolCallID)
		} else {
			collector.AppendSubEvent(SubAgentEvent{
				Agent:            agentName,
				ParentToolCallID: parentID,
				Type:             "tool_result",
				ToolCallID:       msg.ToolCallID,
				Name:             name,
				OK:               boolPtr(true),
				Content:          msg.Content,
			})
		}
	}
}

func emitNonStreamAssistant(
	isRoot bool,
	agentName string,
	router *subAgentRouter,
	msg *schema.Message,
	buf *StreamBuffer,
	collector *RunCollector,
) {
	agentField := router.agentFieldFor(isRoot, agentName)
	parentID := ""
	if !isRoot {
		parentID = router.parentForAgent(agentName)
	}

	if msg.ReasoningContent != "" {
		buf.Append(Encode(Frame{Type: "thinking", Agent: agentField, ParentToolCallID: parentID, Content: msg.ReasoningContent}))
		if collector != nil {
			if isRoot {
				collector.appendReasoning(msg.ReasoningContent)
			} else {
				collector.AppendSubEvent(SubAgentEvent{
					Agent: agentName, ParentToolCallID: parentID,
					Type: "thinking", Content: msg.ReasoningContent,
				})
			}
		}
	}
	if msg.Content != "" {
		buf.Append(Encode(Frame{Type: "text", Agent: agentField, ParentToolCallID: parentID, Content: msg.Content}))
		if collector != nil {
			if isRoot {
				collector.appendContent(msg.Content)
			} else {
				collector.AppendSubEvent(SubAgentEvent{
					Agent: agentName, ParentToolCallID: parentID,
					Type: "text", Content: msg.Content,
				})
			}
		}
	}
	for _, tc := range msg.ToolCalls {
		buf.Append(Encode(Frame{
			Type:             "tool_call",
			Agent:            agentField,
			ParentToolCallID: parentID,
			ID:               tc.ID,
			Name:             tc.Function.Name,
			ArgsJSON:         tc.Function.Arguments,
		}))
		if collector != nil {
			if isRoot {
				collector.startTool(tc.ID, tc.Function.Name, tc.Function.Arguments)
				router.noteRootToolCall(tc.Function.Name, tc.ID)
			} else {
				collector.AppendSubEvent(SubAgentEvent{
					Agent: agentName, ParentToolCallID: parentID,
					Type: "tool_call", ToolCallID: tc.ID,
					Name: tc.Function.Name, ArgsJSON: tc.Function.Arguments,
				})
			}
		}
	}
	if msg.ResponseMeta != nil && msg.ResponseMeta.Usage != nil {
		u := msg.ResponseMeta.Usage
		buf.Append(Encode(Frame{
			Type:             "usage",
			Agent:            agentField,
			ParentToolCallID: parentID,
			Prompt:           u.PromptTokens,
			Reply:            u.CompletionTokens,
			Total:            u.TotalTokens,
		}))
	}
}

func isEOF(err error) bool {
	return errors.Is(err, io.EOF)
}
