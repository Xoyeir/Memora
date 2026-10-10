package service

import (
	"context"

	"github.com/Xoyeir/Memora/internal/llm"
)

// LLMClient 抽成接口，是为了让编排逻辑（Agent 循环、失败与取消语义）脱离真实网络被测试。
type LLMClient interface {
	Formation(ctx context.Context, input string) (*llm.FormationResult, error)
	ChatWithTools(ctx context.Context, messages []llm.Message, tools []llm.Tool) (string, []llm.ToolCall, error)
	StreamMessages(ctx context.Context, messages []llm.Message, onDelta func(string)) (string, error)
	ExtractJournal(ctx context.Context, content string) (*llm.ExtractionResult, error)
	// 只在状态日历真的需要整理某一天时调用，不在聊天主链路上。
	DaySummary(ctx context.Context, input string) (*llm.DaySummaryResult, error)
}
