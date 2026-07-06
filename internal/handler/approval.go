package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/guyi-a/Interview-Agent/internal/approval"
	"github.com/guyi-a/Interview-Agent/internal/service"
)

// ApprovalHandler mediates one HTTP round-trip: the frontend POSTs the user's
// approve/deny decision for a paused tool call, we hand it to ChatService
// which fires runner.ResumeWithParams — no response body beyond OK/Not-Found;
// the actual continuation streams over the existing SSE connection the
// frontend is already reading.
type ApprovalHandler struct {
	chat *service.ChatService
}

func NewApprovalHandler(chat *service.ChatService) *ApprovalHandler {
	return &ApprovalHandler{chat: chat}
}

func (h *ApprovalHandler) Register(r *gin.Engine) {
	r.GET("/conversations/:id/approvals/pending", h.Pending)
	r.POST("/conversations/:id/approvals/:interrupt_id", h.Decide)
	// Mode routes deliberately live OUTSIDE /approvals/ to avoid the
	// POST /approvals/:interrupt_id catch-all — otherwise POST .../mode
	// would be routed to Decide with interrupt_id="mode".
	r.GET("/conversations/:id/approval-mode", h.GetMode)
	r.POST("/conversations/:id/approval-mode", h.SetMode)
}

type pendingApprovalItem struct {
	InterruptID string `json:"interrupt_id"`
	CallID      string `json:"call_id,omitempty"`
	Tool        string `json:"tool,omitempty"`
	ArgsJSON    string `json:"args_json,omitempty"`
}

func (h *ApprovalHandler) Pending(c *gin.Context) {
	convID := c.Param("id")
	items := h.chat.PendingApprovals(convID)
	out := make([]pendingApprovalItem, 0, len(items))
	for _, it := range items {
		out = append(out, pendingApprovalItem{
			InterruptID: it.InterruptID,
			CallID:      it.CallID,
			Tool:        it.Tool,
			ArgsJSON:    it.Args,
		})
	}
	c.JSON(http.StatusOK, gin.H{"approvals": out})
}

type approvalRequest struct {
	// Decision is "approve" or "deny". Any other value is a 400.
	Decision string `json:"decision" binding:"required"`
	// Reason is optional; only meaningful when Decision == "deny". Surfaced
	// back to the model so it can adjust rather than silently retry.
	Reason string `json:"reason"`
}

func (h *ApprovalHandler) Decide(c *gin.Context) {
	convID := c.Param("id")
	interruptID := c.Param("interrupt_id")

	var req approvalRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	dec := approval.Decision{}
	switch req.Decision {
	case "approve":
		dec.Approved = true
	case "deny":
		dec.Approved = false
		dec.Reason = req.Reason
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": `decision must be "approve" or "deny"`})
		return
	}

	found, err := h.chat.Resume(convID, interruptID, dec)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if !found {
		// Stale approval — either the user clicked twice or the run was
		// cancelled before we got here. 404 lets the frontend clear its
		// stored pending card without treating this as a real error.
		c.Status(http.StatusNotFound)
		return
	}
	c.Status(http.StatusAccepted)
}

// GetMode returns the current approval mode for a conversation. Fresh
// conversations (and conversations that outlived a server restart) report
// "default" — the mode store is in-memory by design.
func (h *ApprovalHandler) GetMode(c *gin.Context) {
	convID := c.Param("id")
	m := h.chat.GetApprovalMode(convID)
	c.JSON(http.StatusOK, gin.H{"mode": string(m)})
}

type approvalModeRequest struct {
	Mode string `json:"mode" binding:"required"`
}

// SetMode changes the approval mode. Any unknown mode returns 400 without
// touching store state — the mode set is closed (default / auto / full_access).
// The change takes effect immediately for subsequent tool calls; pending
// approvals already awaiting a user decision are NOT auto-approved by a
// switch to full_access — the user still has to answer them.
func (h *ApprovalHandler) SetMode(c *gin.Context) {
	convID := c.Param("id")
	var req approvalModeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := h.chat.SetApprovalMode(convID, approval.Mode(req.Mode)); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.Status(http.StatusNoContent)
}
