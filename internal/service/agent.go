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
			Name: "recall_memories",
			Description: "按关键词检索用户的长期记忆，用 query 指定要查的主题。" +
				"只在回答确实要引用用户过去说过的事情、偏好、目标或背景时才调用；" +
				"当前这句话本身已经够回答时不要调用。",
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
			Name: "get_memory_source",
			Description: "根据 memory_id 查一条记忆的原始依据（用户当时的原话、日记原文，或用户自己改写过的那句）。" +
				"source_type 是 correction 时，new_content 是用户自己写下的内容，它才是现在这条的依据；" +
				"content 是被替换掉的旧说法，不要拿它当现在这条的证明。用于需要核对记忆来源时调用。",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"memory_id": map[string]any{"type": "integer"},
				},
				"required": []string{"memory_id"},
			},
		},
	},
	{
		Type: "function",
		Function: llm.ToolFunction{
			Name: "recall_life_records",
			Description: "按条件查用户的生活记录（做过的事、情绪、目标、重要日期）。" +
				"只在回答确实需要具体事件或日期时才调用——用户问「我最近做过什么」「上周怎么样」" +
				"属于这种；句子里碰巧提到一件事但那句话本身不需要查记录时不属于。" +
				"query 按内容字面匹配；occurred_from/occurred_to 按事情发生的日期筛，" +
				"mentioned_from/mentioned_to 按「哪天说的」筛，两组不能同时用。" +
				"发生日期未知的记录按 occurred 查不到，只能按 mentioned 查。" +
				"返回的是符合条件的部分记录，不是完整统计。",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query":         map[string]any{"type": "string", "description": "内容关键词，留空表示不限"},
					"kinds":         map[string]any{"type": "array", "items": map[string]any{"type": "string", "enum": []string{"activity", "mood", "goal", "date"}}, "description": "记录类型，留空表示不限"},
					"occurred_from": map[string]any{"type": "string", "description": "发生日期下限，YYYY-MM-DD"},
					"occurred_to":   map[string]any{"type": "string", "description": "发生日期上限，YYYY-MM-DD"},
					"mentioned_from": map[string]any{"type": "string",
						"description": "提及日期下限，YYYY-MM-DD，与 occurred_* 互斥"},
					"mentioned_to": map[string]any{"type": "string", "description": "提及日期上限，YYYY-MM-DD"},
					"limit":        map[string]any{"type": "integer", "description": "返回条数，1~20，默认 10"},
				},
			},
		},
	},
}

// 输出会原样进模型上下文，条数越多越容易让模型把召回结果当成用户的全部活动。
const (
	defaultLifeRecallLimit = 10
	maxLifeRecallLimit     = 20
)

// today 是这一轮所属业务自然日，由落库的原始消息时间换算；用 time.Now() 会让重试/恢复时「今天」漂到另一天。
func (s *Service) runAgent(ctx context.Context, messages []llm.Message, today string) ([]llm.Message, string, []model.Memory) {
	var final string
	var recalled []model.Memory
	seen := make(map[int64]bool)

	for step := 0; step < maxAgentSteps; step++ {
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
			result, found := s.executeTool(ctx, call, seen, today)
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

func (s *Service) executeTool(ctx context.Context, call llm.ToolCall, seen map[int64]bool, today string) (string, []model.Memory) {
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

	case "recall_life_records":
		return s.recallLifeRecords(ctx, call.Arguments, today), nil

	default:
		return toolError(fmt.Errorf("未知工具 %q", call.Name)), nil
	}
}

// 字段挑着给：少任何一个，模型都会把「打算去」读成「去过了」或把日期安错。
type lifeRecallRecord struct {
	LifeID        int64  `json:"life_id"`
	Kind          string `json:"kind"`
	State         string `json:"state"`
	Basis         string `json:"basis"`
	Content       string `json:"content"`
	MentionedOn   string `json:"mentioned_on"`
	OccurredOn    string `json:"occurred_on,omitempty"`
	DatePrecision string `json:"date_precision"`
	DateText      string `json:"date_text,omitempty"`
	EvidenceQuote string `json:"evidence_quote"`
	// 只给引用，不提供按编号读原文的入口。
	Source string `json:"source"`
}

// 范围固定为用户本人且仍然成立的记录；参数校验复用前端读接口同一入口。
func (s *Service) recallLifeRecords(ctx context.Context, rawArgs, today string) string {
	var args struct {
		Query         string   `json:"query"`
		Kinds         []string `json:"kinds"`
		OccurredFrom  string   `json:"occurred_from"`
		OccurredTo    string   `json:"occurred_to"`
		MentionedFrom string   `json:"mentioned_from"`
		MentionedTo   string   `json:"mentioned_to"`
		Limit         int      `json:"limit"`
	}
	if err := json.Unmarshal([]byte(rawArgs), &args); err != nil {
		return toolError(fmt.Errorf("参数解析失败: %w", err))
	}

	limit := args.Limit
	if limit <= 0 || limit > maxLifeRecallLimit {
		limit = defaultLifeRecallLimit
	}

	records, err := s.ListLifeRecords(ctx, LifeRecordQuery{
		Query:         args.Query,
		Kinds:         args.Kinds,
		OccurredFrom:  args.OccurredFrom,
		OccurredTo:    args.OccurredTo,
		MentionedFrom: args.MentionedFrom,
		MentionedTo:   args.MentionedTo,
		Limit:         limit + 1,
	})
	if err != nil {
		return toolError(err)
	}

	truncated := len(records) > limit
	if truncated {
		records = records[:limit]
	}

	out := make([]lifeRecallRecord, len(records))
	for i, r := range records {
		out[i] = lifeRecallRecord{
			LifeID:        r.ID,
			Kind:          r.Kind,
			State:         r.State,
			Basis:         r.Basis,
			Content:       r.Content,
			MentionedOn:   r.MentionedOn,
			OccurredOn:    r.OccurredOn,
			DatePrecision: r.DatePrecision,
			DateText:      r.DateText,
			EvidenceQuote: r.EvidenceQuote,
			Source:        fmt.Sprintf("chat:%d", r.ChatMessageID),
		}
	}

	return toolJSON(map[string]any{
		"today":     today,
		"records":   out,
		"count":     len(out),
		"truncated": truncated,
		"note": "这是按条件召回的部分生活记录，不等于完整统计：没有记录不代表没发生过。" +
			"occurred_on 为空表示发生日期未知，不要替它安一个日期，可以按 mentioned_on 说明是哪天提到的。" +
			"state=planned 是打算做，state=negated 是明确没做，state=done 才是已经发生。",
	})
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
