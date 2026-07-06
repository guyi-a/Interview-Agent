package service

import (
	"context"
	"encoding/json"
	"log"
	"strings"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"

	"github.com/guyi-a/Interview-Agent/internal/agent/contextkey"
	"github.com/guyi-a/Interview-Agent/internal/agent/multimodal"
	"github.com/guyi-a/Interview-Agent/internal/agent/toolerr"
	"github.com/guyi-a/Interview-Agent/internal/approval"
	"github.com/guyi-a/Interview-Agent/internal/repository"
	"github.com/guyi-a/Interview-Agent/internal/repository/model"
	"github.com/guyi-a/Interview-Agent/internal/stream"
)

type ChatService struct {
	runner        *adk.Runner
	rootName      string
	manager       *stream.Manager
	convRepo      *repository.ConversationRepo
	msgRepo       *repository.MessageRepo
	projectRepo   *repository.ProjectRepo
	pending       *approval.PendingStore
	approvalModes *approval.ModeStore
	multimodal    bool
}

func NewChatService(
	runner *adk.Runner,
	rootName string,
	manager *stream.Manager,
	convRepo *repository.ConversationRepo,
	msgRepo *repository.MessageRepo,
	projectRepo *repository.ProjectRepo,
	pending *approval.PendingStore,
	approvalModes *approval.ModeStore,
	multimodal bool,
) *ChatService {
	return &ChatService{
		runner:        runner,
		rootName:      rootName,
		manager:       manager,
		convRepo:      convRepo,
		msgRepo:       msgRepo,
		projectRepo:   projectRepo,
		pending:       pending,
		approvalModes: approvalModes,
		multimodal:    multimodal,
	}
}

func (s *ChatService) Get(id string) *stream.StreamBuffer {
	return s.manager.Get(id)
}

func (s *ChatService) IsStreaming(id string) bool {
	return s.manager.IsStreaming(id)
}

func (s *ChatService) Cancel(id string) bool {
	buf := s.manager.Get(id)
	if buf == nil {
		return false
	}
	return buf.Cancel()
}

// Start begins (or continues) a chat turn:
//   - ensures conversation row exists
//   - if projectID is provided AND the conversation is new (or unbound), binds it
//   - loads prior messages as context
//   - persists the new user message
//   - kicks off the ADK Runner in a goroutine, persists assistant reply when done
func (s *ChatService) Start(ctx context.Context, id, userMsg, projectID string) (*stream.StreamBuffer, error) {
	if err := s.convRepo.Upsert(ctx, id); err != nil {
		return nil, err
	}

	if projectID != "" {
		conv, err := s.convRepo.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		// Only bind when conversation has no project yet — silently ignore the
		// query param if it's already bound to a different (or same) project.
		if conv != nil && (conv.ProjectID == nil || *conv.ProjectID == "") {
			if err := s.convRepo.SetProjectID(ctx, id, projectID); err != nil {
				return nil, err
			}
		}
	}

	prior, err := s.msgRepo.List(ctx, id)
	if err != nil {
		return nil, err
	}

	history := toSchemaMessages(id, prior, s.multimodal)
	if workspaceContext := s.workspaceContext(ctx, id); workspaceContext != "" {
		history = append([]*schema.Message{schema.SystemMessage(workspaceContext)}, history...)
	}
	history = append(history, multimodal.BuildUserMessage(userMsg, s.multimodal))

	if err := s.msgRepo.Append(ctx, &model.Message{
		ConversationID: id,
		Role:           string(schema.User),
		Content:        userMsg,
	}); err != nil {
		return nil, err
	}

	if title := truncateForTitle(userMsg); title != "" {
		_ = s.convRepo.SetTitleIfEmpty(ctx, id, title)
	}

	buf := s.manager.Create(id)
	runCtx, cancel := context.WithCancel(context.Background())
	buf.SetCancel(cancel)

	// Any leftover pending approvals from a previous, discarded run would
	// mismatch the checkpoint eino is about to overwrite. Clear now so the
	// only pending items visible to the HTTP layer belong to this run.
	s.pending.Clear(id)

	go s.runAgent(runCtx, id, history, buf)

	return buf, nil
}

// Resume delivers the user's approval decision for an interrupted run and
// re-enters the same conversation's SSE stream with a fresh iterator. Returns
// (found, nil) on success, or (false, nil) if the interrupt id isn't known
// (already acted on, or stale). Errors from the ADK layer bubble up.
func (s *ChatService) Resume(convID, interruptID string, dec approval.Decision) (bool, error) {
	item, ok := s.pending.Take(convID, interruptID)
	if !ok {
		return false, nil
	}

	// The previous SSE buffer was Finish()ed when the interrupt drained the
	// iterator, so a resumed run can't Append into it. Replace with a fresh
	// buffer — the frontend will GET /chat/:id to reconnect and drain it.
	buf := s.manager.Create(convID)
	runCtx, cancel := context.WithCancel(context.Background())
	buf.SetCancel(cancel)

	go s.resumeAgent(runCtx, convID, item.CheckpointID, interruptID, dec, buf)
	return true, nil
}

// PendingApprovals returns in-memory approval requests that are still waiting
// for a user decision. The ADK checkpoint itself lives in SQLite; this list is
// just the UI lookup metadata needed after a page refresh.
func (s *ChatService) PendingApprovals(convID string) []approval.PendingItem {
	if s.pending == nil {
		return nil
	}
	return s.pending.List(convID)
}

// GetApprovalMode returns the per-conversation approval mode, defaulting to
// approval.ModeDefault when the conversation has never explicitly set one
// (including after a server restart, which is intentional — see mode.go).
func (s *ChatService) GetApprovalMode(convID string) approval.Mode {
	return s.approvalModes.Get(convID)
}

// SetApprovalMode validates and stores the mode. Called from the HTTP handler.
func (s *ChatService) SetApprovalMode(convID string, m approval.Mode) error {
	return s.approvalModes.Set(convID, m)
}

func (s *ChatService) workspaceContext(ctx context.Context, convID string) string {
	if s.projectRepo == nil {
		return ""
	}
	conv, err := s.convRepo.Get(ctx, convID)
	if err != nil || conv == nil || conv.ProjectID == nil || *conv.ProjectID == "" {
		return "当前会话未绑定工作区。用户要求读写文件时，先调用 create_workspace 创建工作区。"
	}
	project, err := s.projectRepo.Get(ctx, *conv.ProjectID)
	if err != nil || project == nil {
		return "当前会话绑定的工作区记录不存在。用户要求读写文件时，先说明工作区不可用。"
	}
	return "当前会话已绑定工作区。project_id=" + project.ID + "，project_name=" + project.Name + "，workspace=" + project.Workspace + "。用户询问当前项目/工作区/文件时，直接调用 list_files/read_file/write_file/edit_file/mkdir 等文件工具，不要先询问是否已挂载工作区。"
}

func (s *ChatService) runAgent(ctx context.Context, convID string, msgs []*schema.Message, buf *stream.StreamBuffer) {
	ctx = contextkey.WithConversationID(ctx, convID)
	ctx = contextkey.WithBuffer(ctx, buf)
	ctx = toolerr.WithRegistry(ctx, toolerr.NewRegistry())

	collector := stream.NewRunCollector()
	sink := s.approvalSink(convID)

	_ = s.convRepo.SetAgentStatus(context.Background(), convID, "running")

	// Checkpoint id is stable per conversation — one active run at a time is
	// enforced by manager.Create above, so reusing convID as the eino
	// checkpoint id keeps resume lookups trivial.
	iter := s.runner.Run(ctx, msgs, adk.WithCheckPointID(convID))
	s.consumeAndPersist(ctx, convID, iter, sink, buf, collector, nil)
}

func (s *ChatService) resumeAgent(
	ctx context.Context,
	convID, checkpointID, interruptID string,
	dec approval.Decision,
	buf *stream.StreamBuffer,
) {
	ctx = contextkey.WithConversationID(ctx, convID)
	ctx = contextkey.WithBuffer(ctx, buf)
	ctx = toolerr.WithRegistry(ctx, toolerr.NewRegistry())

	collector := stream.NewRunCollector()
	sink := s.approvalSink(convID)

	_ = s.convRepo.SetAgentStatus(context.Background(), convID, "running")

	// Rebuild the sub-agent router's open-parents map from persisted
	// history so events emitted during resume (e.g. a sub-agent's
	// interrupted write_file completing) can still be attributed to the
	// supervisor-level tool_call that spawned them before the interrupt.
	// See ConsumeADKEvents doc for the rationale.
	priorRows, err := s.msgRepo.List(context.Background(), convID)
	if err != nil {
		log.Printf("resume: load prior rows (conv=%s): %v", convID, err)
	}
	initialRouter := rebuildOpenToolCalls(priorRows)

	iter, err := s.runner.ResumeWithParams(ctx, checkpointID, &adk.ResumeParams{
		Targets: map[string]any{interruptID: dec},
	})
	if err != nil {
		log.Printf("adk resume error (conv=%s): %v", convID, err)
		_ = s.convRepo.SetAgentStatus(context.Background(), convID, "idle")
		stream.FinalizeErr(buf, err)
		return
	}
	s.consumeAndPersist(ctx, convID, iter, sink, buf, collector, initialRouter)
}

// rebuildOpenToolCalls walks the conversation's persisted messages and
// returns the sub-agent tool_calls that are still "open" — declared by an
// assistant row but with no matching Role=Tool result row afterwards. Used
// on resume to seed stream.subAgentRouter so mid-flight sub-agent events
// (e.g. a write_file tool_result arriving after the human approves)
// resolve to the correct parent tool_call_id from before the interrupt.
//
// Keyed by tool name because the router uses the sub-agent's AgentName as
// its lookup key (which equals the tool name for NewAgentTool-wrapped
// sub-agents like deep_research / job_search). If the same tool name is
// called twice in one run, the later id wins — matches the router's
// noteRootToolCall overwrite semantics.
func rebuildOpenToolCalls(rows []model.Message) map[string]string {
	open := map[string]string{} // tool name → tool_call_id, only kept while still un-resolved
	for _, r := range rows {
		if r.Role == string(schema.Assistant) && r.ToolCalls != "" {
			var tcs []stream.ToolCallRecord
			if err := json.Unmarshal([]byte(r.ToolCalls), &tcs); err == nil {
				for _, tc := range tcs {
					open[tc.Name] = tc.ID
				}
			}
			continue
		}
		if r.Role == string(schema.Tool) && r.ToolCallID != "" {
			// Drop any open entry whose id matches this tool_result.
			for name, id := range open {
				if id == r.ToolCallID {
					delete(open, name)
					break
				}
			}
		}
	}
	if len(open) == 0 {
		return nil
	}
	return open
}

// approvalSink wraps the pending store's sink with a side effect: whenever a
// tool call pauses for approval, mark the conversation waiting_approval so
// the sidebar pill lights up. The end-of-run finalizer flips it back.
func (s *ChatService) approvalSink(convID string) stream.InterruptSink {
	inner := s.pending.Record(convID)
	return sinkFunc(func(checkpointID, interruptID string, info any) {
		inner.Record(checkpointID, interruptID, info)
		_ = s.convRepo.SetAgentStatus(context.Background(), convID, "waiting_approval")
	})
}

type sinkFunc func(checkpointID, interruptID string, info any)

func (f sinkFunc) Record(checkpointID, interruptID string, info any) {
	f(checkpointID, interruptID, info)
}

// consumeAndPersist drives the iterator, persists the run's turns, and
// finalises the SSE buffer. Shared between the initial Run and post-approval
// Resume paths so both take the same code path.
func (s *ChatService) consumeAndPersist(
	ctx context.Context,
	convID string,
	iter *adk.AsyncIterator[*adk.AgentEvent],
	sink stream.InterruptSink,
	buf *stream.StreamBuffer,
	collector *stream.RunCollector,
	initialRouterState map[string]string,
) {
	if err := stream.ConsumeADKEvents(ctx, iter, s.rootName, convID, sink, buf, collector, initialRouterState); err != nil {
		log.Printf("adk runner error: %v", err)
		if perr := s.persistRun(convID, collector, false); perr != nil {
			log.Printf("persist run (on error path): %v", perr)
		}
		s.finalizeStatus(convID)
		stream.FinalizeErr(buf, err)
		return
	}

	interrupted := s.pending.HasPending(convID)
	if err := s.persistRun(convID, collector, interrupted); err != nil {
		log.Printf("persist run: %v", err)
	}
	_ = s.convRepo.Upsert(context.Background(), convID)
	s.finalizeStatus(convID)
	stream.FinalizeOK(buf)
}

// finalizeStatus sets the conversation status based on whether there are
// still items awaiting approval. Called after the iterator drains — an
// interrupt causes the iter to end naturally, so this path fires both for
// clean completions and for paused runs.
func (s *ChatService) finalizeStatus(convID string) {
	status := "idle"
	if s.pending.HasPending(convID) {
		status = "waiting_approval"
	}
	_ = s.convRepo.SetAgentStatus(context.Background(), convID, status)
}

// persistRun serialises one completed run into raw per-message rows:
//
//	assistant (with ToolCalls JSON) → tool row × N → next assistant → ...
//
// Each turn's assistant ToolCalls are paired with their tool_result rows in
// the SAME batch, ensuring Claude's strict tool_use ↔ tool_result pairing
// survives on the next replay. If a turn is missing a tool_result (cancel /
// crash / early stream close), a placeholder "[canceled] tool did not run"
// row is inserted so the pairing stays intact.
//
// The entire batch is inserted in a single DB transaction via AppendMany;
// on failure nothing is committed, avoiding partial "half turn" state.
//
// Dual-write for UI compatibility: the last assistant row also carries a
// legacy Extra JSON blob (`tools[]` + `sub_events[]`) matching the pre-fix
// wire format, so the frontend's tool-card and sub-agent rendering paths
// keep working while the handler-side fold logic (see handler.fromModelMessage)
// is being validated.
func (s *ChatService) persistRun(convID string, collector *stream.RunCollector, skipMissingToolPadding bool) error {
	turns := collector.Turns()
	if len(turns) == 0 {
		// Nothing captured (e.g. an early ADK error before any event). Nothing
		// to persist — matches previous behaviour.
		return nil
	}
	subEvents := collector.SubEvents()
	legacyTools := collector.Tools()

	rows := make([]*model.Message, 0, 2*len(turns))
	lastAssistantIdx := len(turns) - 1

	for i, t := range turns {
		padded := t
		if !skipMissingToolPadding {
			padded = padMissingToolResults(t)
		}

		assistantRow := &model.Message{
			ConversationID:   convID,
			Role:             string(schema.Assistant),
			Content:          padded.Assistant.Content,
			ReasoningContent: padded.Assistant.ReasoningContent,
		}
		if len(padded.Assistant.ToolCalls) > 0 {
			if b, err := json.Marshal(padded.Assistant.ToolCalls); err == nil {
				assistantRow.ToolCalls = string(b)
			} else {
				log.Printf("marshal ToolCalls (convID=%s): %v", convID, err)
			}
		}
		// Dual-write: legacy Extra (tools + sub_events) only on the last
		// assistant row of the run. This preserves the pre-fix wire format
		// the frontend currently reads via handler.fromModelMessage; the new
		// fold-based path (step 3f) reads ToolCalls + tool rows and ignores
		// this blob for new data.
		if i == lastAssistantIdx {
			payload := map[string]any{}
			if len(legacyTools) > 0 {
				payload["tools"] = legacyTools
			}
			if len(subEvents) > 0 {
				payload["sub_events"] = subEvents
			}
			if len(payload) > 0 {
				if data, jerr := json.Marshal(payload); jerr == nil {
					assistantRow.Extra = string(data)
				} else {
					log.Printf("marshal extra (convID=%s): %v", convID, jerr)
				}
			}
		}
		rows = append(rows, assistantRow)

		for _, tr := range padded.ToolResults {
			// tool row Content is what the LLM sees on next replay — it must
			// carry enough info for the model to react (success text or error
			// description). We fold Error into Content for failures so the
			// model doesn't lose the reason on replay.
			content := tr.Content
			if !tr.OK {
				if content == "" {
					content = tr.Error
				} else if tr.Error != "" && !strings.Contains(content, tr.Error) {
					content = content + " (" + tr.Error + ")"
				}
				if content == "" {
					content = "[error] tool failed"
				}
			}
			toolRow := &model.Message{
				ConversationID: convID,
				Role:           string(schema.Tool),
				Content:        content,
				ToolCallID:     tr.CallID,
				ToolName:       tr.Name,
			}
			// Extra encodes ok/error precisely for the UI fold path so the
			// frontend can render red-state cards without parsing Content.
			// Successes skip Extra entirely (nil ≡ ok:true default in the
			// handler-side fold).
			if !tr.OK {
				payload := map[string]any{"ok": false}
				if tr.Error != "" {
					payload["error"] = tr.Error
				}
				if b, jerr := json.Marshal(payload); jerr == nil {
					toolRow.Extra = string(b)
				}
			}
			rows = append(rows, toolRow)
		}
	}

	return s.msgRepo.AppendMany(context.Background(), rows)
}

// padMissingToolResults ensures every ToolCall in the turn's assistant
// message has a matching ToolResult. Missing ones (cancel / crash mid-turn)
// get a placeholder result so the persisted history stays a valid
// tool_use ↔ tool_result pairing — otherwise the next replay would 400 from
// Claude with "tool_use ids without matching tool_result".
func padMissingToolResults(t stream.TurnRecord) stream.TurnRecord {
	if len(t.Assistant.ToolCalls) == 0 {
		return t
	}
	seen := make(map[string]bool, len(t.ToolResults))
	for _, r := range t.ToolResults {
		seen[r.CallID] = true
	}
	out := t
	for _, tc := range t.Assistant.ToolCalls {
		if !seen[tc.ID] {
			out.ToolResults = append(out.ToolResults, stream.ToolResultRecord{
				CallID:  tc.ID,
				Name:    tc.Name,
				OK:      false,
				Content: "[canceled] tool did not run",
				Error:   "canceled",
			})
		}
	}
	return out
}

// toSchemaMessages hydrates DB rows into schema.Message with the full
// tool_use / tool_result structure so Claude sees the real prior tool
// invocations, not just the assistant's prose about them.
//
// Old-format rows (no ToolCalls column and no matching Role=tool rows —
// pre-fix data where tools lived only in Extra) fall back to Content-only
// hydration; the model won't see structured tool history for those turns,
// but the alternative — pretending the tool_use blocks existed by fabricating
// ids — would 400 on Anthropic replay.
//
// Orphan-tool_call defence (last line of protection against pairing bugs):
// if an assistant row declares a ToolCalls id with no matching Role=tool row
// in the same list, the whole ToolCalls field is stripped from that message
// and a warn is logged. Claude requires strict tool_use ↔ tool_result
// pairing; a stray tool_use with no tool_result would 400.
func toSchemaMessages(convID string, rows []model.Message, multimodalEnabled bool) []*schema.Message {
	// Pass 1: collect all tool_call_ids we actually have tool_result rows for.
	haveResult := make(map[string]struct{})
	for _, r := range rows {
		if r.Role == string(schema.Tool) && r.ToolCallID != "" {
			haveResult[r.ToolCallID] = struct{}{}
		}
	}

	out := make([]*schema.Message, 0, len(rows))
	for _, r := range rows {
		// User rows may carry [image: /abs/path] markers that need to be
		// expanded into multipart image blocks for the model. Delegate to
		// the same helper Start uses so the wire shape is identical
		// whether a message is being sent for the first time or replayed
		// from history.
		if r.Role == string(schema.User) {
			m := multimodal.BuildUserMessage(r.Content, multimodalEnabled)
			// preserve reasoning if any (rare for user rows but harmless)
			if r.ReasoningContent != "" {
				m.ReasoningContent = r.ReasoningContent
			}
			out = append(out, m)
			continue
		}
		m := &schema.Message{
			Role:             schema.RoleType(r.Role),
			Content:          r.Content,
			ReasoningContent: r.ReasoningContent,
			ToolCallID:       r.ToolCallID,
			ToolName:         r.ToolName,
		}
		if r.ToolCalls != "" {
			var recs []stream.ToolCallRecord
			if err := json.Unmarshal([]byte(r.ToolCalls), &recs); err != nil {
				log.Printf("toSchemaMessages: unmarshal ToolCalls (convID=%s seq=%d): %v",
					convID, r.Seq, err)
			} else if len(recs) > 0 {
				// Orphan defence: if ANY declared tool_call has no matching
				// tool_result row, drop the whole ToolCalls list. Splitting
				// the array would leave a partial tool_use that still 400s.
				orphaned := make([]string, 0)
				for _, rec := range recs {
					if _, ok := haveResult[rec.ID]; !ok {
						orphaned = append(orphaned, rec.ID)
					}
				}
				if len(orphaned) > 0 {
					log.Printf("toSchemaMessages: orphan tool_call detected, stripping ToolCalls "+
						"(convID=%s seq=%d orphan_ids=%v)", convID, r.Seq, orphaned)
				} else {
					tcs := make([]schema.ToolCall, 0, len(recs))
					for _, rec := range recs {
						tcs = append(tcs, schema.ToolCall{
							ID:   rec.ID,
							Type: "function",
							Function: schema.FunctionCall{
								Name:      rec.Name,
								Arguments: rec.ArgsJSON,
							},
						})
					}
					m.ToolCalls = tcs
				}
			}
		}
		out = append(out, m)
	}
	return out
}

func truncateForTitle(s string) string {
	return strings.TrimSpace(s)
}
