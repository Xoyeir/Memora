package handler

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/Xoyeir/Memora/internal/service"
)

func TestCheckChatRequest(t *testing.T) {
	tests := []struct {
		name     string
		req      chatRequest
		wantID   string
		wantFail bool
	}{
		{name: "正常", req: chatRequest{Message: "你好", ClientID: "c-1"}, wantID: "c-1"},
		{name: "缺少消息", req: chatRequest{ClientID: "c-1"}, wantFail: true},
		{name: "缺少请求标识", req: chatRequest{Message: "你好"}, wantFail: true},
		{name: "请求标识过长", req: chatRequest{Message: "你好", ClientID: strings.Repeat("x", 65)}, wantFail: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			id, msg := checkChatRequest(&tc.req)
			if tc.wantFail {
				if msg == "" {
					t.Fatal("期望校验失败")
				}
				return
			}
			if msg != "" {
				t.Fatalf("不应失败: %s", msg)
			}
			if id != tc.wantID {
				t.Fatalf("返回的标识是 %q，期望 %q", id, tc.wantID)
			}
		})
	}
}

func TestChatErrorStatus(t *testing.T) {
	if got := chatErrorStatus(service.ErrClientIDConflict); got != http.StatusConflict {
		t.Fatalf("标识冲突返回 %d，期望 409", got)
	}
	if got := chatErrorStatus(service.ErrTurnInProgress); got != http.StatusConflict {
		t.Fatalf("处理中返回 %d，期望 409", got)
	}
	if got := chatErrorStatus(errors.New("随便一个错误")); got != http.StatusInternalServerError {
		t.Fatalf("其他错误返回 %d，期望 500", got)
	}
}
