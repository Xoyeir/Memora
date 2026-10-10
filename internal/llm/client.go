package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/Xoyeir/Memora/internal/config"
)

const requestTimeout = 30 * time.Second
const streamTimeout = 120 * time.Second

type Client struct {
	baseURL    string
	apiKey     string
	model      string
	httpClient *http.Client
}

func NewClient(cfg *config.Config) *Client {
	return &Client{
		baseURL:    cfg.LLMBaseURL,
		apiKey:     cfg.LLMAPIKey,
		model:      cfg.LLMModel,
		httpClient: &http.Client{},
	}
}

type chatMessage struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	ToolCalls  []toolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

type Message struct {
	Role       string
	Content    string
	ToolCalls  []ToolCall
	ToolCallID string
}

type ToolCall struct {
	ID        string
	Name      string
	Arguments string
}

type Tool struct {
	Type     string       `json:"type"`
	Function ToolFunction `json:"function"`
}

type ToolFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

type toolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type chatRequest struct {
	Model          string          `json:"model"`
	Messages       []chatMessage   `json:"messages"`
	ResponseFormat *responseFormat `json:"response_format,omitempty"`
	Stream         bool            `json:"stream,omitempty"`
	Tools          []Tool          `json:"tools,omitempty"`
}

type responseFormat struct {
	Type string `json:"type"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content   string     `json:"content"`
			ToolCalls []toolCall `json:"tool_calls"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// logTruncation 记录因达到输出上限被截断的回复，靠 finish_reason 与「回复很短」区分。
func logTruncation(where, finishReason string, n int) {
	if finishReason == "length" {
		log.Printf("llm: %s 的输出达到上限被截断（finish_reason=length），已输出 %d 字节", where, n)
	}
}

type retryableError struct {
	statusCode int
	body       string
}

func (e *retryableError) Error() string {
	return fmt.Sprintf("LLM 返回可重试状态码 %d: %s", e.statusCode, e.body)
}

func isRetryable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var re *retryableError
	if errors.As(err, &re) {
		return true
	}
	var ne net.Error
	if errors.As(err, &ne) {
		return true
	}
	return false
}

func waitBackoff(ctx context.Context, attempt int) bool {
	backoff := time.Duration(1<<uint(attempt-1)) * time.Second
	select {
	case <-time.After(backoff):
		return true
	case <-ctx.Done():
		return false
	}
}

func (c *Client) doHTTP(ctx context.Context, reqBody chatRequest) (*http.Response, error) {
	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("构造请求体失败: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("创建请求失败: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("调用 LLM 失败: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			return nil, &retryableError{statusCode: resp.StatusCode, body: string(b)}
		}
		return nil, fmt.Errorf("LLM 返回非 200 状态码 %d: %s", resp.StatusCode, string(b))
	}

	return resp, nil
}

func (c *Client) doChat(ctx context.Context, reqBody chatRequest) (*chatResponse, error) {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			if !waitBackoff(ctx, attempt) {
				return nil, ctx.Err()
			}
		}
		chatResp, err := c.doChatOnce(ctx, reqBody)
		if err == nil {
			return chatResp, nil
		}
		lastErr = err
		if !isRetryable(err) {
			break
		}
	}
	return nil, lastErr
}

func (c *Client) doChatOnce(ctx context.Context, reqBody chatRequest) (*chatResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	resp, err := c.doHTTP(ctx, reqBody)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("读取响应失败: %w", err)
	}

	var chatResp chatResponse
	if err := json.Unmarshal(respBody, &chatResp); err != nil {
		return nil, fmt.Errorf("解析响应失败: %w", err)
	}

	if chatResp.Error != nil {
		return nil, fmt.Errorf("LLM 返回错误: %s", chatResp.Error.Message)
	}

	if len(chatResp.Choices) == 0 {
		return nil, fmt.Errorf("LLM 返回了空结果")
	}

	return &chatResp, nil
}

func (c *Client) chat(ctx context.Context, messages []chatMessage, wantJSON bool) (string, error) {
	reqBody := chatRequest{Model: c.model, Messages: messages}
	if wantJSON {
		reqBody.ResponseFormat = &responseFormat{Type: "json_object"}
	}
	chatResp, err := c.doChat(ctx, reqBody)
	if err != nil {
		return "", err
	}
	logTruncation("非流式回复", chatResp.Choices[0].FinishReason, len(chatResp.Choices[0].Message.Content))
	return chatResp.Choices[0].Message.Content, nil
}

func toChatMessages(messages []Message) []chatMessage {
	out := make([]chatMessage, len(messages))
	for i, m := range messages {
		out[i] = chatMessage{Role: m.Role, Content: m.Content, ToolCallID: m.ToolCallID}
		for _, tc := range m.ToolCalls {
			out[i].ToolCalls = append(out[i].ToolCalls, toolCall{
				ID:   tc.ID,
				Type: "function",
				Function: struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				}{Name: tc.Name, Arguments: tc.Arguments},
			})
		}
	}
	return out
}

func (c *Client) ChatWithTools(ctx context.Context, messages []Message, tools []Tool) (string, []ToolCall, error) {
	reqBody := chatRequest{
		Model:    c.model,
		Messages: toChatMessages(messages),
		Tools:    tools,
	}
	chatResp, err := c.doChat(ctx, reqBody)
	if err != nil {
		return "", nil, err
	}

	msg := chatResp.Choices[0].Message
	logTruncation("工具调用决策", chatResp.Choices[0].FinishReason, len(msg.Content))
	var calls []ToolCall
	for _, tc := range msg.ToolCalls {
		calls = append(calls, ToolCall{ID: tc.ID, Name: tc.Function.Name, Arguments: tc.Function.Arguments})
	}
	return msg.Content, calls, nil
}

func (c *Client) StreamMessages(ctx context.Context, messages []Message, onDelta func(string)) (string, error) {
	reqBody := chatRequest{
		Model:    c.model,
		Messages: toChatMessages(messages),
		Stream:   true,
	}

	// 拿到 2xx 流式响应是分界点：此前可重试，此后中途错误不重试（重试会重复计费与输出）。
	resp, cancel, err := c.doStream(ctx, reqBody)
	if err != nil {
		return "", err
	}
	defer cancel()
	defer resp.Body.Close()

	var full strings.Builder
	// [DONE] 是协议结束标志：对端可能输出一半后干净断开，scanner 不会报错。
	terminated := false
	// finish_reason 只在最后一个 chunk 出现，且那块的 delta 是空的。
	var finishReason string
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			terminated = true
			break
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
				FinishReason string `json:"finish_reason"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		if fr := chunk.Choices[0].FinishReason; fr != "" {
			finishReason = fr
		}
		if chunk.Choices[0].Delta.Content != "" {
			full.WriteString(chunk.Choices[0].Delta.Content)
			onDelta(chunk.Choices[0].Delta.Content)
		}
	}

	// 已流出的内容一并返回，调用方要区分「断流」和「整轮没发生」。
	if err := scanner.Err(); err != nil {
		return full.String(), fmt.Errorf("读取流式响应失败: %w", err)
	}
	if !terminated {
		return full.String(), fmt.Errorf("流式响应没有正常结束：没有收到 %s", "[DONE]")
	}
	logTruncation("流式回复", finishReason, full.Len())
	return full.String(), nil
}

func (c *Client) doStream(ctx context.Context, reqBody chatRequest) (*http.Response, context.CancelFunc, error) {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			if !waitBackoff(ctx, attempt) {
				return nil, nil, ctx.Err()
			}
		}
		attemptCtx, cancel := context.WithTimeout(ctx, streamTimeout)
		resp, err := c.doHTTP(attemptCtx, reqBody)
		if err == nil {
			return resp, cancel, nil
		}
		cancel()
		lastErr = err
		if !isRetryable(err) {
			break
		}
	}
	return nil, nil, lastErr
}
