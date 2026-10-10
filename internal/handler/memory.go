package handler

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/Xoyeir/Memora/internal/repository"
	"github.com/Xoyeir/Memora/internal/service"
)

type MemoryHandler struct {
	svc *service.Service
}

func NewMemoryHandler(svc *service.Service) *MemoryHandler {
	return &MemoryHandler{svc: svc}
}

// List 列出记忆，默认只看仍然有效的那些。
func (h *MemoryHandler) List(c *gin.Context) {
	limit, err := strconv.Atoi(c.DefaultQuery("limit", "0"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "limit 必须是整数"})
		return
	}

	list, err := h.svc.ListMemoriesForManagement(c.Request.Context(), c.Query("status"), limit)
	if err != nil {
		writeMemoryError(c, err)
		return
	}
	c.JSON(http.StatusOK, list)
}

func (h *MemoryHandler) Sources(c *gin.Context) {
	id, ok := parseMemoryID(c)
	if !ok {
		return
	}
	view, err := h.svc.MemorySources(c.Request.Context(), id)
	if err != nil {
		writeMemoryError(c, err)
		return
	}
	c.JSON(http.StatusOK, view)
}

func (h *MemoryHandler) Correct(c *gin.Context) {
	id, ok := parseMemoryID(c)
	if !ok {
		return
	}

	var body struct {
		Content string `json:"content"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求体不合法"})
		return
	}

	updated, err := h.svc.CorrectMemory(c.Request.Context(), id, body.Content)
	if err != nil {
		writeMemoryError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"memory": updated})
}

// Forget 让记忆不再参与候选和来源读取。
func (h *MemoryHandler) Forget(c *gin.Context) {
	id, ok := parseMemoryID(c)
	if !ok {
		return
	}

	m, err := h.svc.ForgetMemory(c.Request.Context(), id)
	if err != nil {
		writeMemoryError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"memory": m})
}

func parseMemoryID(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "记忆编号不合法"})
		return 0, false
	}
	return id, true
}

func writeMemoryError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrInvalidMemoryParam):
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	case errors.Is(err, repository.ErrMemoryNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "这条记忆已经不存在了"})
	case errors.Is(err, repository.ErrMemoryNotActive):
		// 409 而非 404：记录还在，只是已不是有效记忆。
		c.JSON(http.StatusConflict, gin.H{"error": "这条记忆已经被纠正或忘记了，刷新后再看"})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
	}
}
