package handler

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/Xoyeir/Memora/internal/service"
)

type CalendarHandler struct {
	svc *service.Service
}

func NewCalendarHandler(svc *service.Service) *CalendarHandler {
	return &CalendarHandler{svc: svc}
}

// Month  GET /api/calendar/month?month=2026-10
func (h *CalendarHandler) Month(c *gin.Context) {
	view, err := h.svc.CalendarMonth(c.Request.Context(), c.Query("month"))
	if err != nil {
		writeCalendarError(c, err)
		return
	}
	c.JSON(http.StatusOK, view)
}

// Day  GET /api/calendar/days/2026-10-05
func (h *CalendarHandler) Day(c *gin.Context) {
	detail, err := h.svc.CalendarDay(c.Request.Context(), c.Param("day"))
	if err != nil {
		writeCalendarError(c, err)
		return
	}
	c.JSON(http.StatusOK, detail)
}

// Summarize  POST /api/calendar/days/2026-10-05/summarize
func (h *CalendarHandler) Summarize(c *gin.Context) {
	day, err := h.svc.SummarizeDay(c.Request.Context(), c.Param("day"))
	if err != nil {
		writeCalendarError(c, err)
		return
	}
	c.JSON(http.StatusOK, day)
}

// SetLabel  PUT /api/calendar/days/2026-10-05/label  {"label": "忙碌"}
func (h *CalendarHandler) SetLabel(c *gin.Context) {
	var body struct {
		Label string `json:"label"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求体不合法"})
		return
	}

	day, err := h.svc.SetDayLabel(c.Request.Context(), c.Param("day"), body.Label)
	if err != nil {
		writeCalendarError(c, err)
		return
	}
	c.JSON(http.StatusOK, day)
}

// ClearLabel  DELETE /api/calendar/days/2026-10-05/label
func (h *CalendarHandler) ClearLabel(c *gin.Context) {
	day, err := h.svc.ClearDayLabel(c.Request.Context(), c.Param("day"))
	if err != nil {
		writeCalendarError(c, err)
		return
	}
	c.JSON(http.StatusOK, day)
}

func writeCalendarError(c *gin.Context, err error) {
	if errors.Is(err, service.ErrInvalidCalendarParam) {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
}
