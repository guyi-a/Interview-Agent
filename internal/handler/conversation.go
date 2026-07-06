package handler

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/guyi-a/Interview-Agent/internal/repository/model"
	"github.com/guyi-a/Interview-Agent/internal/service"
	"github.com/guyi-a/Interview-Agent/internal/stream"
)

type ConversationHandler struct {
	svc *service.ConversationService
}

func NewConversationHandler(svc *service.ConversationService) *ConversationHandler {
	return &ConversationHandler{svc: svc}
}

func (h *ConversationHandler) Register(r *gin.Engine) {
	r.GET("/conversations", h.List)
	r.GET("/conversations/:id/messages", h.Messages)
	r.DELETE("/conversations/:id", h.Delete)
}

type conversationListItem struct {
	ID          string  `json:"id"`
	ProjectID   *string `json:"project_id,omitempty"`
	Title       string  `json:"title"`
	AgentStatus string  `json:"agent_status,omitempty"`
	UpdatedAt   string  `json:"updated_at"`
}

func (h *ConversationHandler) List(c *gin.Context) {
	limit := 50
	if v := c.Query("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	items, err := h.svc.List(c.Request.Context(), limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	out := make([]conversationListItem, 0, len(items))
	for _, it := range items {
		out = append(out, conversationListItem{
			ID:          it.ID,
			ProjectID:   it.ProjectID,
			Title:       it.Title,
			AgentStatus: it.AgentStatus,
			UpdatedAt:   it.UpdatedAt.Format(time.RFC3339),
		})
	}
	c.JSON(http.StatusOK, gin.H{"conversations": out})
}

type toolEventItem struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	ArgsJSON string `json:"args_json,omitempty"`
	OK       *bool  `json:"ok,omitempty"`
	Status   string `json:"status,omitempty"`
	Content  string `json:"content,omitempty"`
	Error    string `json:"error,omitempty"`
}

type subAgentEventItem struct {
	Seq              int    `json:"seq"`
	Agent            string `json:"agent"`
	ParentToolCallID string `json:"parent_tool_call_id,omitempty"`
	Type             string `json:"type"`
	Content          string `json:"content,omitempty"`
	ToolCallID       string `json:"tool_call_id,omitempty"`
	Name             string `json:"name,omitempty"`
	ArgsJSON         string `json:"args_json,omitempty"`
	OK               *bool  `json:"ok,omitempty"`
	Error            string `json:"error,omitempty"`
}

type messageItem struct {
	Seq              int                 `json:"seq"`
	Role             string              `json:"role"`
	Content          string              `json:"content"`
	ReasoningContent string              `json:"reasoning_content,omitempty"`
	Tools            []toolEventItem     `json:"tools,omitempty"`
	SubEvents        []subAgentEventItem `json:"sub_events,omitempty"`
	CreatedAt        string              `json:"created_at"`
}

func (h *ConversationHandler) Messages(c *gin.Context) {
	id := c.Param("id")
	msgs, err := h.svc.Messages(c.Request.Context(), id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	out := foldMessages(msgs)
	c.JSON(http.StatusOK, gin.H{"messages": out})
}

func (h *ConversationHandler) Delete(c *gin.Context) {
	id := c.Param("id")
	if err := h.svc.Delete(c.Request.Context(), id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.Status(http.StatusNoContent)
}

func fromModelMessage(m model.Message) messageItem {
	item := messageItem{
		Seq:              m.Seq,
		Role:             m.Role,
		Content:          m.Content,
		ReasoningContent: m.ReasoningContent,
		CreatedAt:        m.CreatedAt.Format(time.RFC3339),
	}
	if m.Extra != "" {
		var payload struct {
			Tools     []stream.ToolEventRecord `json:"tools"`
			SubEvents []stream.SubAgentEvent   `json:"sub_events"`
		}
		if err := json.Unmarshal([]byte(m.Extra), &payload); err == nil {
			// Only hydrate Tools from Extra for LEGACY assistant rows (no
			// ToolCalls column). New rows have their tool structure in
			// ToolCalls + separate Role=tool rows, and foldMessages fills
			// item.Tools from those. Reading Extra.tools here would produce
			// duplicates.
			if m.ToolCalls == "" {
				for _, t := range payload.Tools {
					status := "error"
					if t.OK {
						status = "ok"
					}
					item.Tools = append(item.Tools, toolEventItem{
						ID:       t.ID,
						Name:     t.Name,
						ArgsJSON: t.ArgsJSON,
						OK:       boolPtr(t.OK),
						Status:   status,
						Content:  t.Content,
						Error:    t.Error,
					})
				}
			}
			for _, e := range payload.SubEvents {
				item.SubEvents = append(item.SubEvents, subAgentEventItem{
					Seq:              e.Seq,
					Agent:            e.Agent,
					ParentToolCallID: e.ParentToolCallID,
					Type:             e.Type,
					Content:          e.Content,
					ToolCallID:       e.ToolCallID,
					Name:             e.Name,
					ArgsJSON:         e.ArgsJSON,
					OK:               e.OK,
					Error:            e.Error,
				})
			}
		}
	}
	return item
}

// foldMessages transforms raw per-message DB rows into the flat per-turn
// wire format the frontend expects (one assistant entry per user turn,
// with tools[] + sub_events[] nested inside).
//
// Two folds happen in parallel:
//
//  1. assistant + subsequent tool rows: an assistant with ToolCalls seeds
//     Tools[] placeholders (id/name/args), then following Role=tool rows fill
//     each placeholder's ok/content/error by matching ToolCallID.
//
//  2. Multiple assistant rows in the same turn (a ReAct loop emits one
//     assistant per iteration — first "I'll call X", then after tool result
//     "here's the answer" — that's ≥ 2 rows) are merged into a SINGLE UI
//     entry. The frontend renders one Interviewer block per turn; splitting
//     them would show the same timestamp twice with the tool card floating
//     between two halves of the reply.
//
// The merge chain resets on any user / system row so a new user turn always
// starts a fresh assistant entry.
//
// Legacy rows (assistant with no ToolCalls, tools embedded in Extra) still
// flow through fromModelMessage unchanged; the merge logic treats them the
// same way — a single legacy assistant row is just a chain of length 1.
func foldMessages(msgs []model.Message) []messageItem {
	out := make([]messageItem, 0, len(msgs))
	lastAssistantIdx := -1

	for _, m := range msgs {
		switch m.Role {
		case "tool":
			if lastAssistantIdx < 0 {
				// Orphan tool row (no preceding assistant to fold into) —
				// shouldn't happen with well-formed data. Skip rather than
				// emit a bare tool card the UI wasn't designed for.
				continue
			}
			ok := true
			errMsg := ""
			if m.Extra != "" {
				var p struct {
					OK    *bool  `json:"ok"`
					Error string `json:"error"`
				}
				if json.Unmarshal([]byte(m.Extra), &p) == nil {
					if p.OK != nil {
						ok = *p.OK
					}
					errMsg = p.Error
				}
			}
			merged := false
			for i := range out[lastAssistantIdx].Tools {
				t := &out[lastAssistantIdx].Tools[i]
				if t.ID == m.ToolCallID {
					t.OK = boolPtr(ok)
					if ok {
						t.Status = "ok"
						t.Content = m.Content
					} else {
						t.Status = "error"
						t.Error = errMsg
						if t.Error == "" {
							t.Error = m.Content
						}
					}
					merged = true
					break
				}
			}
			if !merged {
				// tool row with no matching placeholder in the last
				// assistant's Tools[] (rare: assistant.ToolCalls missing this
				// id, or orphan tool row from data drift). Append as a
				// standalone tool entry so nothing is silently dropped.
				out[lastAssistantIdx].Tools = append(out[lastAssistantIdx].Tools, toolEventItem{
					ID:      m.ToolCallID,
					Name:    m.ToolName,
					OK:      boolPtr(ok),
					Status:  statusFromOK(ok),
					Content: m.Content,
					Error:   errMsg,
				})
			}
		case "assistant":
			item := fromModelMessage(m)
			// If this row has structured ToolCalls (new format), rebuild
			// item.Tools as placeholders from ToolCalls — subsequent tool
			// rows will fill their ok/content/error. Skip this block for
			// legacy rows (no ToolCalls) so item.Tools keeps whatever
			// fromModelMessage hydrated from Extra.tools.
			if m.ToolCalls != "" {
				var recs []stream.ToolCallRecord
				if err := json.Unmarshal([]byte(m.ToolCalls), &recs); err == nil && len(recs) > 0 {
					placeholders := make([]toolEventItem, 0, len(recs))
					for _, rec := range recs {
						placeholders = append(placeholders, toolEventItem{
							ID:       rec.ID,
							Name:     rec.Name,
							ArgsJSON: rec.ArgsJSON,
							Status:   "pending",
							// OK/Content/Error left empty — filled when the
							// matching tool rows fold in below. Until then this
							// represents an approval-pending tool call.
						})
					}
					item.Tools = placeholders
				}
			}

			// Merge with the previous assistant entry if this belongs to the
			// same user turn (no user/system row broke the chain since).
			if lastAssistantIdx >= 0 {
				prev := &out[lastAssistantIdx]
				prev.Content = joinAssistantContent(prev.Content, item.Content)
				prev.ReasoningContent = joinAssistantContent(prev.ReasoningContent, item.ReasoningContent)
				// Append Tools with id-based dedupe: the last assistant row
				// of a new-format turn carries the legacy Extra.tools list
				// (dual-write) which repeats every tool_call already seeded
				// by earlier ToolCalls-driven placeholders. Merging blindly
				// would render every tool card twice.
				seen := make(map[string]struct{}, len(prev.Tools))
				for _, t := range prev.Tools {
					if t.ID != "" {
						seen[t.ID] = struct{}{}
					}
				}
				for _, t := range item.Tools {
					if t.ID != "" {
						if _, dup := seen[t.ID]; dup {
							continue
						}
						seen[t.ID] = struct{}{}
					}
					prev.Tools = append(prev.Tools, t)
				}
				prev.SubEvents = append(prev.SubEvents, item.SubEvents...)
				// Use the latest row's seq/timestamp so the turn shows the
				// completion moment (matches the pre-fix single-row behaviour).
				prev.Seq = item.Seq
				prev.CreatedAt = item.CreatedAt
				continue
			}

			out = append(out, item)
			lastAssistantIdx = len(out) - 1
		default:
			// user / system — resets the assistant merge chain.
			out = append(out, fromModelMessage(m))
			lastAssistantIdx = -1
		}
	}
	return out
}

// joinAssistantContent concatenates two chunks of the same assistant turn's
// content/reasoning. A single blank line separator is inserted between
// non-empty halves so ReAct intermediate remarks ("Let me check…") stay
// visually distinct from the final answer.
func joinAssistantContent(a, b string) string {
	if a == "" {
		return b
	}
	if b == "" {
		return a
	}
	return a + "\n\n" + b
}

func boolPtr(v bool) *bool {
	return &v
}

func statusFromOK(ok bool) string {
	if ok {
		return "ok"
	}
	return "error"
}
