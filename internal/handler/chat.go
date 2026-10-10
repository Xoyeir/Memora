package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/Xoyeir/Memora/internal/model"
	"github.com/Xoyeir/Memora/internal/service"
)

type ChatHandler struct {
	svc *service.Service
}

func NewChatHandler(svc *service.Service) *ChatHandler {
	return &ChatHandler{svc: svc}
}

type chatRequest struct {
	Message  string `json:"message"`
	ClientID string `json:"client_id"`
}

// checkChatRequest 校验请求并返回 client_id：client_id 是幂等的基础，缺失时两次请求无法区分。
func checkChatRequest(req *chatRequest) (string, string) {
	if req.Message == "" {
		return "", "message 不能为空"
	}
	if req.ClientID == "" {
		return "", "client_id 不能为空"
	}
	if len(req.ClientID) > 64 {
		return "", "client_id 过长"
	}
	return req.ClientID, ""
}

func chatErrorStatus(err error) int {
	if errors.Is(err, service.ErrClientIDConflict) || errors.Is(err, service.ErrTurnInProgress) {
		return http.StatusConflict
	}
	return http.StatusInternalServerError
}

type recoverRequest struct {
	ClientID string `json:"client_id"`
}

// RecoverFormation 补写回复已成功、记忆没写成的那一轮，由客户端显式触发。
func (h *ChatHandler) RecoverFormation(c *gin.Context) {
	var req recoverRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求体格式错误"})
		return
	}
	if req.ClientID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "client_id 不能为空"})
		return
	}

	status, err := h.svc.RecoverFormation(c.Request.Context(), req.ClientID)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrTurnNotFound):
			c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		case errors.Is(err, service.ErrTurnNotComplete):
			c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		default:
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		}
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": status})
}

func (h *ChatHandler) Chat(c *gin.Context) {
	var req chatRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求体格式错误"})
		return
	}
	clientID, msg := checkChatRequest(&req)
	if msg != "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": msg})
		return
	}

	reply, err := h.svc.Chat(c.Request.Context(), clientID, req.Message)
	if err != nil {
		c.JSON(chatErrorStatus(err), gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"reply": reply})
}

func (h *ChatHandler) History(c *gin.Context) {
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))

	messages, err := h.svc.ChatHistory(c.Request.Context(), limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"messages": messages})
}

func (h *ChatHandler) ChatStream(c *gin.Context) {
	var req chatRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求体格式错误"})
		return
	}
	clientID, msg := checkChatRequest(&req)
	if msg != "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": msg})
		return
	}

	// 流开始前不写 SSE 头，标识冲突等错误才能返回真正的 HTTP 状态码。
	started := false
	send := func(payload any) {
		if !started {
			started = true
			c.Header("Content-Type", "text/event-stream")
			c.Header("Cache-Control", "no-cache")
			c.Header("Connection", "keep-alive")
		}
		data, _ := json.Marshal(payload)
		fmt.Fprintf(c.Writer, "data: %s\n\n", data)
		c.Writer.Flush()
	}

	err := h.svc.ChatStream(c.Request.Context(), clientID, req.Message,
		func(memories []model.Memory) { send(map[string]any{"recalled": memories}) },
		func(delta string) { send(map[string]string{"content": delta}) })
	if err == nil {
		return
	}

	if c.Request.Context().Err() != nil {
		return
	}
	if !started {
		c.JSON(chatErrorStatus(err), gin.H{"error": err.Error()})
		return
	}
	send(map[string]string{"error": err.Error()})
}
