package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/Xoyeir/Memora/internal/llm"
	"github.com/Xoyeir/Memora/internal/model"
)

const maxAgentSteps = 3

var agentTools = []llm.Tool{
	{
		Type: "function",
		Function: llm.ToolFunction{
			Name:        "recall_memories",
			Description: "按关键词检索用户的长期记忆。当回复需要引用用户过去说过的事情、偏好、目标、背景时调用，用 query 指定要查的主题。",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query": map[string]any{"type": "string", "description": "检索关键词"},
					"limit": map[string]any{"type": "integer", "description": "返回条数，1~10，默认 10"},
				},
				"required": []string{"query"},
			},
		},
	},
	{
		Type: "function",
		Function: llm.ToolFunction{
			Name:        "get_memory_source",
			Description: "根据 memory_id 查一条记忆的原始依据（用户当时的原话或日记原文）。用于需要核对记忆来源时调用。",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"memory_id": map[string]any{"type": "integer"},
				},
				"required": []string{"memory_id"},
			},
		},
	},
}

func (s *Service) runAgent(ctx context.Context, messages []llm.Message) ([]llm.Message, string, []model.Memory) {
	var final string
	var recalled []model.Memory
	seen := make(map[int64]bool)

	for step := 0; step < maxAgentSteps; step++ {
		// 已被取消/超时就不再发起模型请求，也不做任何业务重试。
		if ctx.Err() != nil {
			break
		}

		content, calls, err := s.llm.ChatWithTools(ctx, messages, agentTools)
		if err != nil {
			log.Printf("Agent 决策失败（step %d）: %v", step, err)
			break
		}
		if len(calls) == 0 {
			final = content
			break
		}

		messages = append(messages, llm.Message{Role: "assistant", ToolCalls: calls})
		for _, call := range calls {
			start := time.Now()
			result, found := s.executeTool(ctx, call, seen)
			for _, m := range found {
				if !seen[m.ID] {
					seen[m.ID] = true
					recalled = append(recalled, m)
				}
			}
			log.Printf("trace step=%d tool=%s args=%s elapsed=%s result=%s",
				step, call.Name, call.Arguments, time.Since(start), truncate(result, 200))
			messages = append(messages, llm.Message{Role: "tool", ToolCallID: call.ID, Content: result})
		}
	}
	return messages, final, recalled
}

func (s *Service) executeTool(ctx context.Context, call llm.ToolCall, seen map[int64]bool) (string, []model.Memory) {
	switch call.Name {
	case "recall_memories":
		var args struct {
			Query string `json:"query"`
			Limit int    `json:"limit"`
		}
		if err := json.Unmarshal([]byte(call.Arguments), &args); err != nil {
			return toolError(fmt.Errorf("参数解析失败: %w", err)), nil
		}
		if args.Limit <= 0 || args.Limit > 10 {
			args.Limit = 10
		}
		memories, err := s.repo.SearchMemories(ctx, args.Query, args.Limit)
		if err != nil {
			return toolError(err), nil
		}
		return toolJSON(memories), memories

	case "get_memory_source":
		var args struct {
			MemoryID int64 `json:"memory_id"`
		}
		if err := json.Unmarshal([]byte(call.Arguments), &args); err != nil {
			return toolError(fmt.Errorf("参数解析失败: %w", err)), nil
		}
		if args.MemoryID <= 0 {
			return toolError(fmt.Errorf("memory_id 必须是正整数")), nil
		}
		if !seen[args.MemoryID] {
			return toolError(fmt.Errorf("memory_id %d 未在本轮召回，无权访问", args.MemoryID)), nil
		}
		sources, err := s.repo.ListMemorySources(ctx, args.MemoryID)
		if err != nil {
			return toolError(err), nil
		}
		return toolJSON(map[string]any{"memory_id": args.MemoryID, "sources": sources}), nil

	default:
		return toolError(fmt.Errorf("未知工具 %q", call.Name)), nil
	}
}

func toolJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return toolError(err)
	}
	return string(b)
}

func toolError(err error) string {
	return fmt.Sprintf(`{"error": %q}`, err.Error())
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
