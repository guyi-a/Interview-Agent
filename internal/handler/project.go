package handler

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/guyi-a/Interview-Agent/internal/service"
)

type ProjectHandler struct {
	svc *service.ProjectService
}

func NewProjectHandler(svc *service.ProjectService) *ProjectHandler {
	return &ProjectHandler{svc: svc}
}

func (h *ProjectHandler) Register(r *gin.Engine) {
	r.GET("/projects", h.List)
	r.GET("/projects/:id", h.Get)
}

type projectItem struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Workspace string `json:"workspace"`
	UpdatedAt string `json:"updated_at"`
}

func (h *ProjectHandler) List(c *gin.Context) {
	items, err := h.svc.List(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	out := make([]projectItem, 0, len(items))
	for _, p := range items {
		out = append(out, projectItem{
			ID:        p.ID,
			Name:      p.Name,
			Workspace: p.Workspace,
			UpdatedAt: p.UpdatedAt.Format(time.RFC3339),
		})
	}
	c.JSON(http.StatusOK, gin.H{"projects": out})
}

func (h *ProjectHandler) Get(c *gin.Context) {
	id := c.Param("id")
	p, err := h.svc.Get(c.Request.Context(), id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if p == nil {
		c.Status(http.StatusNotFound)
		return
	}
	c.JSON(http.StatusOK, projectItem{
		ID:        p.ID,
		Name:      p.Name,
		Workspace: p.Workspace,
		UpdatedAt: p.UpdatedAt.Format(time.RFC3339),
	})
}
