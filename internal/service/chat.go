package service

import (
	"context"
	"encoding/json"
	"log"
	"strings"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"

	"github.com/guyi-a/Interview-Agent/internal/agent/contextkey"
	"github.com/guyi-a/Interview-Agent/internal/agent/toolerr"
	"github.com/guyi-a/Interview-Agent/internal/repository"
	"github.com/guyi-a/Interview-Agent/internal/repository/model"
	"github.com/guyi-a/Interview-Agent/internal/stream"
)

type ChatService struct {
	runner      *adk.Runner
	rootName    string
	manager     *stream.Manager
	convRepo    *repository.ConversationRepo
	msgRepo     *repository.MessageRepo
	projectRepo *repository.ProjectRepo
}

func NewChatService(
	runner *adk.Runner,
	rootName string,
	manager *stream.Manager,
	convRepo *repository.ConversationRepo,
	msgRepo *repository.MessageRepo,
	projectRepo *repository.ProjectRepo,
) *ChatService {
	return &ChatService{
		runner:      runner,
		rootName:    rootName,
		manager:     manager,
		convRepo:    convRepo,
		msgRepo:     msgRepo,
		projectRepo: projectRepo,
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

	history := toSchemaMessages(id, prior)
	if workspaceContext := s.workspaceContext(ctx, id); workspaceContext != "" {
		history = append([]*schema.Message{schema.SystemMessage(workspaceContext)}, history...)
	}
	history = append(history, schema.UserMessage(userMsg))

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

	go s.runAgent(runCtx, id, history, buf)

	return buf, nil
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
	// Per-run registry so the tool-error middleware and the SSE handler agree
	// on which tool calls were rescued from a failure. SSE emits ok=false
	// for those, ok=true otherwise.
	ctx = toolerr.WithRegistry(ctx, toolerr.NewRegistry())

	collector := stream.NewRunCollector()

	iter := s.runner.Run(ctx, msgs)
	if err := stream.ConsumeADKEvents(ctx, iter, s.rootName, buf, collector); err != nil {
		log.Printf("adk runner error: %v", err)
		// Still attempt to persist whatever the collector already captured, so
		// the user's history reflects the assistant text they saw. persistRun
		// pads missing tool_result rows so the tool_use/tool_result pairing
		// stays valid for the next replay.
		if perr := s.persistRun(convID, collector); perr != nil {
			log.Printf("persist run (on error path): %v", perr)
		}
		stream.FinalizeErr(buf, err)
		return
	}

	if err := s.persistRun(convID, collector); err != nil {
		log.Printf("persist run: %v", err)
	}
	_ = s.convRepo.Upsert(context.Background(), convID)
	stream.FinalizeOK(buf)
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
func (s *ChatService) persistRun(convID string, collector *stream.RunCollector) error {
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
		padded := padMissingToolResults(t)

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
func toSchemaMessages(convID string, rows []model.Message) []*schema.Message {
	// Pass 1: collect all tool_call_ids we actually have tool_result rows for.
	haveResult := make(map[string]struct{})
	for _, r := range rows {
		if r.Role == string(schema.Tool) && r.ToolCallID != "" {
			haveResult[r.ToolCallID] = struct{}{}
		}
	}

	out := make([]*schema.Message, 0, len(rows))
	for _, r := range rows {
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
