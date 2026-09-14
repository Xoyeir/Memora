package service

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/Xoyeir/memora/internal/llm"
	"github.com/Xoyeir/memora/internal/model"
)

func (s *Service) Chat(ctx context.Context, userMessage string) (string, error) {
	userChatID, err := s.repo.CreateChatMessage(ctx, "user", userMessage)
	if err != nil {
		return "", fmt.Errorf("保存用户消息失败: %w", err)
	}

	memories, err := s.repo.ListMemories(ctx, 20)
	if err != nil {
		return "", fmt.Errorf("读取记忆失败: %w", err)
	}

	formation, err := s.llm.Formation(ctx, buildFormationInput(userMessage, memories))
	if err != nil {
		log.Printf("Formation 失败（不影响回复）: %v", err)
	} else {
		s.persistFormation(ctx, formation, userChatID)
	}

	systemPrompt := buildResponseSystemPrompt(formation, memories)

	reply, err := s.llm.Chat(ctx, systemPrompt, userMessage)
	if err != nil {
		return "", err
	}

	if _, err := s.repo.CreateChatMessage(ctx, "assistant", reply); err != nil {
		return "", fmt.Errorf("保存助手回复失败: %w", err)
	}

	return reply, nil
}

func (s *Service) persistFormation(ctx context.Context, formation *llm.FormationResult, userChatID int64) {
	for _, action := range formation.MemoryActions {
		if action.Action != "FORM" || action.Candidate == nil {
			continue
		}
		if err := s.repo.CreateMemoryFromChat(ctx, action.Candidate.Content, action.Candidate.Category, userChatID); err != nil {
			log.Printf("FORM 落库失败（不影响回复）: %v", err)
		}
	}
}

func buildFormationInput(userMessage string, memories []model.Memory) string {
	var sb strings.Builder
	sb.WriteString("当前消息：\n")
	sb.WriteString(userMessage)
	sb.WriteString("\n\n已有长期记忆：\n")
	if len(memories) == 0 {
		sb.WriteString("（无）\n")
	} else {
		for _, m := range memories {
			fmt.Fprintf(&sb, "- %s（%s）\n", m.Content, m.Category)
		}
	}
	return sb.String()
}

const responseSystemPrompt = `你是 Memora，一个陪伴记录型的个人记忆助手。

核心原则：用户想被听见的时候，别急着解决；用户真的来求解的时候，认真帮他解决。

先判断用户当前意图：
- 表达 / 倾诉 / 分享 → 接住他说的具体内容，必要时自然带出过去，最多给一个轻量观察，然后停下。
- 求建议 / 方案 / 判断 → 结合背景认真回答，可以给分析建议，但不替他做最终决定，也不要为了"陪伴感"故意拖着不给答案。

再判断这句话的重心：
- 先找到用户这句话最重要的重心，整段回应围绕这个重心，不要被某个具体细节带跑。
- 用户举的例子只是例子时，别把例子当成核心主题。
- 用户已经明确说出了更深层的诉求，就直接回应那个诉求本身。
- 可以具体，但不要脑补用户没说过的场景、动机或生活细节。
- 不要为了显得有洞察而寻找隐藏动机。用户已经明确表达出来的矛盾，本身通常就足够值得回应。

说话方式：
- 像认识他很久的人那样自然回应，不像顾问，也不像心理咨询师。
- 用他自己的词，别把他的表达重新包装成专业概念。
- 少下结论。不要用"真正的问题是""本质上""这说明""所以你现在其实是"这类句型。
- 一轮最多一个观察，别连着抛好几个洞见。
- 围绕一个核心点展开，不要东拉西扯。
- 不要求把话分析完整，能确定多少说到多少，剩下的留给后面的对话。
- 结尾不一定要提问。回应完整了就直接停。
- 可以稍微回复得多一些，但长度花在具体回应上，不花在建议、框架、总结上。

规则：
- 不编造用户没说过的经历或事实。
- 不把推断说成事实，不替他定义人格、动机、心理状态；推断用开放语气（"会不会是…"），可被纠正。
- 用户当前表达优先于历史记忆；记忆用于制造连续感，不用于压制当前。
- 不为了显得"记得用户"而强行召回记忆。只有过去的信息真的能帮上当前的理解时，才自然融入；帮不上就不提。
- 引用记忆时不出现"我记得 / 根据记忆 / 检索到"这类字眼。
- 避免模板化共情和固定句式。`

func buildResponseSystemPrompt(formation *llm.FormationResult, memories []model.Memory) string {
	var sb strings.Builder
	sb.WriteString(responseSystemPrompt)

	if len(memories) > 0 {
		sb.WriteString("\n\n关于用户，你已知以下长期记忆：\n")
		for _, m := range memories {
			if m.Category != "" {
				fmt.Fprintf(&sb, "- %s（%s）\n", m.Content, m.Category)
			} else {
				fmt.Fprintf(&sb, "- %s\n", m.Content)
			}
		}
	}

	if formation != nil {
		sb.WriteString("\n本轮你对用户当前消息的理解：\n")
		sb.WriteString(formation.Understanding.Summary)
		sb.WriteString("\n")

		if len(formation.ResponseGuidance.UserMood) > 0 {
			fmt.Fprintf(&sb, "用户当前情绪：%s\n", strings.Join(formation.ResponseGuidance.UserMood, "、"))
		}
		if len(formation.ResponseGuidance.ResponseContext) > 0 {
			sb.WriteString("应影响的上下文（自然带出，不必显式引用）：\n")
			for _, c := range formation.ResponseGuidance.ResponseContext {
				fmt.Fprintf(&sb, "- %s\n", c)
			}
		}
	}

	return sb.String()
}
