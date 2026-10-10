package llm

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// sseServer 起一个受控流式服务：按脚本写 body 后关连接，abort 时中途掐断模拟对端进程消失。
func sseServer(t *testing.T, body string, abort bool) *Client {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, body)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		if abort {
			panic(http.ErrAbortHandler)
		}
	}))
	t.Cleanup(srv.Close)

	return &Client{baseURL: srv.URL, apiKey: "test", model: "test", httpClient: &http.Client{}}
}

func chunk(content string) string {
	return fmt.Sprintf("data: {\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", content)
}

// 只有收到 [DONE] 才算完整生成，对端输出一半后干净断流时 scanner 不报错。
func TestStreamMessagesTreatsMissingDoneAsFailure(t *testing.T) {
	cases := []struct {
		name     string
		body     string
		abort    bool
		wantText string
		wantErr  bool
	}{
		{
			name:     "正常收到 DONE",
			body:     chunk("我") + chunk("在听") + "data: [DONE]\n\n",
			wantText: "我在听",
		},
		{
			name:     "输出一半后干净断开",
			body:     chunk("说到") + chunk("一半"),
			wantText: "说到一半",
			wantErr:  true,
		},
		{
			name:     "一个字都没有就连上了又断开",
			body:     "",
			wantText: "",
			wantErr:  true,
		},
		{
			name:     "输出一半后连接被掐断",
			body:     chunk("说") + chunk("到一半"),
			abort:    true,
			wantText: "说到一半",
			wantErr:  true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := sseServer(t, tc.body, tc.abort)

			var got strings.Builder
			text, err := c.StreamMessages(context.Background(),
				[]Message{{Role: "user", Content: "讲讲"}},
				func(d string) { got.WriteString(d) })

			if tc.wantErr && err == nil {
				t.Fatal("流没有正常结束，却当成了成功")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("正常结束的流报了错: %v", err)
			}
			if text != tc.wantText {
				t.Fatalf("返回的内容是 %q，期望 %q", text, tc.wantText)
			}
			if got.String() != tc.wantText {
				t.Fatalf("增量累计是 %q，期望 %q", got.String(), tc.wantText)
			}
		})
	}
}

// 断流时不能重试：那是重复计费加重复输出。
func TestStreamMessagesDoesNotRetryAfterStreamStarts(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, chunk("半句"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}))
	defer srv.Close()

	c := &Client{baseURL: srv.URL, apiKey: "test", model: "test", httpClient: &http.Client{}}
	if _, err := c.StreamMessages(context.Background(), []Message{{Role: "user", Content: "在吗"}}, func(string) {}); err == nil {
		t.Fatal("期望报错")
	}
	if hits != 1 {
		t.Fatalf("流开始之后重试了，服务端共收到 %d 次请求", hits)
	}
}
