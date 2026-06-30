package handler

import (
	"log"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/guyi-a/Interview-Agent/internal/service"
	"github.com/guyi-a/Interview-Agent/internal/stream"
)

type ChatHandler struct {
	chat *service.ChatService
}

func NewChatHandler(chat *service.ChatService) *ChatHandler {
	return &ChatHandler{chat: chat}
}

func (h *ChatHandler) Register(r *gin.Engine) {
	r.POST("/chat/:id", h.Chat)
	r.GET("/chat/:id", h.Resume)
	r.POST("/chat/:id/cancel", h.Cancel)
}

type chatRequest struct {
	Message string `json:"message" binding:"required"`
}

func (h *ChatHandler) Chat(c *gin.Context) {
	id := c.Param("id")

	if h.chat.IsStreaming(id) {
		writeSSE(c, h.chat.Get(id))
		return
	}

	var req chatRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	buf, err := h.chat.Start(c.Request.Context(), id, req.Message, c.Query("project_id"))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	writeSSE(c, buf)
}

func (h *ChatHandler) Resume(c *gin.Context) {
	id := c.Param("id")
	buf := h.chat.Get(id)
	if buf == nil {
		c.Status(http.StatusNoContent)
		return
	}
	writeSSE(c, buf)
}

func (h *ChatHandler) Cancel(c *gin.Context) {
	id := c.Param("id")
	if !h.chat.Cancel(id) {
		c.Status(http.StatusNoContent)
		return
	}
	c.Status(http.StatusOK)
}

func writeSSE(c *gin.Context, buf *stream.StreamBuffer) {
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
	c.Writer.WriteHeader(http.StatusOK)

	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		log.Print("response writer is not a flusher")
		return
	}
	flusher.Flush()

	ch := buf.StreamAll(c.Request.Context())
	for frame := range ch {
		if _, err := c.Writer.Write(frame); err != nil {
			return
		}
		flusher.Flush()
	}
}
