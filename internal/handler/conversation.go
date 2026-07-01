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
	ID        string  `json:"id"`
	ProjectID *string `json:"project_id,omitempty"`
	Title     string  `json:"title"`
	UpdatedAt string  `json:"updated_at"`
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
			ID:        it.ID,
			ProjectID: it.ProjectID,
			Title:     it.Title,
			UpdatedAt: it.UpdatedAt.Format(time.RFC3339),
		})
	}
	c.JSON(http.StatusOK, gin.H{"conversations": out})
}

type toolEventItem struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	ArgsJSON string `json:"args_json,omitempty"`
	OK       bool   `json:"ok"`
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
	out := make([]messageItem, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, fromModelMessage(m))
	}
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
			for _, t := range payload.Tools {
				item.Tools = append(item.Tools, toolEventItem{
					ID:       t.ID,
					Name:     t.Name,
					ArgsJSON: t.ArgsJSON,
					OK:       t.OK,
					Content:  t.Content,
					Error:    t.Error,
				})
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
