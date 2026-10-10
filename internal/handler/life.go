package handler

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/Xoyeir/Memora/internal/service"
)

type LifeHandler struct {
	svc *service.Service
}

func NewLifeHandler(svc *service.Service) *LifeHandler {
	return &LifeHandler{svc: svc}
}

// List 的 occurred_*（事情发生）与 mentioned_*（哪天说的）两组日期只能给一组，同时给会被拒绝。
func (h *LifeHandler) List(c *gin.Context) {
	limit, err := service.ParseLimitParam(c.Query("limit"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	q := service.LifeRecordQuery{
		OccurredFrom:  c.Query("occurred_from"),
		OccurredTo:    c.Query("occurred_to"),
		MentionedFrom: c.Query("mentioned_from"),
		MentionedTo:   c.Query("mentioned_to"),
		Query:         c.Query("q"),
		Kinds:         service.SplitKinds(c.Query("kind")),
		Subject:       c.Query("subject"),
		Limit:         limit,
	}

	records, err := h.svc.ListLifeRecords(c.Request.Context(), q)
	if err != nil {
		if errors.Is(err, service.ErrInvalidLifeQuery) {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"records": records})
}
