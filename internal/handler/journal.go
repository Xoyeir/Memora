package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/Xoyeir/memora/internal/service"
)

type JournalHandler struct {
	svc *service.Service
}

func NewJournalHandler(svc *service.Service) *JournalHandler {
	return &JournalHandler{svc: svc}
}

type createJournalRequest struct {
	Content string `json:"content"`
}

func (h *JournalHandler) Create(c *gin.Context) {
	var req createJournalRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求体格式错误"})
		return
	}
	if req.Content == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "content 不能为空"})
		return
	}

	result, err := h.svc.CreateJournal(c.Request.Context(), req.Content)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, result)
}

func (h *JournalHandler) List(c *gin.Context) {
	list, err := h.svc.ListJournals(c.Request.Context(), 50)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, list)
}
