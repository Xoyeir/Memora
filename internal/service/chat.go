package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/Xoyeir/memora/internal/model"
)

func (s *Service) Chat(ctx context.Context, userMessage string) (string, error) {
	if _, err := s.repo.CreateChatMessage(ctx, "user", userMessage); err != nil {
		return "", fmt.Errorf("保存用户消息失败: %w", err)
	}

	memories, err := s.repo.ListMemories(ctx, 20)
	if err != nil {
		return "", fmt.Errorf("读取记忆失败: %w", err)
	}

	systemPrompt := buildChatSystemPrompt(memories)

	reply, err := s.llm.Chat(ctx, systemPrompt, userMessage)
	if err != nil {
		return "", err
	}

	if _, err := s.repo.CreateChatMessage(ctx, "assistant", reply); err != nil {
		return "", fmt.Errorf("保存助手回复失败: %w", err)
	}

	return reply, nil
}

func buildChatSystemPrompt(memories []model.Memory) string {
	var sb strings.Builder
	sb.WriteString("你是 Memora，一个陪伴记录型的个人记忆助手。你会在对话中自然地引用用户过去的记忆，让交流更有连续性。\n")

	if len(memories) == 0 {
		sb.WriteString("目前你对用户还没有任何长期记忆。")
		return sb.String()
	}

	sb.WriteString("\n关于用户，你已知以下长期记忆：\n")
	for _, m := range memories {
		if m.Category != "" {
			fmt.Fprintf(&sb, "- %s（%s）\n", m.Content, m.Category)
		} else {
			fmt.Fprintf(&sb, "- %s\n", m.Content)
		}
	}
	sb.WriteString("\n引用记忆时保持自然，只在相关时才提及，不要生硬罗列，也不要编造不确定的信息。")
	return sb.String()
}
