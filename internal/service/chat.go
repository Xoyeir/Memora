package service

import (
	"context"
	"fmt"
	"log"
	"strconv"
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

	formation, err := s.llm.Formation(ctx, buildFormationInput(userChatID, userMessage, memories))
	if err != nil {
		log.Printf("Formation 失败（不影响回复）: %v", err)
	} else {
		s.persistFormation(ctx, formation, userChatID, memories)
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

func (s *Service) persistFormation(ctx context.Context, formation *llm.FormationResult, userChatID int64, memories []model.Memory) {
	knownMemories := make(map[int64]bool, len(memories))
	for _, m := range memories {
		knownMemories[m.ID] = true
	}

	for _, action := range formation.MemoryActions {
		switch action.Action {
		case "FORM":
			if action.Candidate == nil {
				log.Printf("FORM 缺少 candidate，跳过")
				continue
			}
			if err := s.repo.CreateMemoryFromChat(ctx, action.Candidate.Content, action.Candidate.Category, userChatID); err != nil {
				log.Printf("FORM 落库失败（不影响回复）: %v", err)
			}

		case "REINFORCE":
			memoryID, ok := parseRef(action.Ref, "memory:")
			if !ok || !knownMemories[memoryID] {
				log.Printf("REINFORCE 引用了无效的记忆：%q", action.Ref)
				continue
			}
			for _, ref := range action.EvidenceRefs {
				chatID, ok := parseRef(ref, "chat:")
				if !ok || chatID != userChatID {
					log.Printf("REINFORCE 引用了无效的证据：%q", ref)
					continue
				}
				if err := s.repo.AddMemoryChatSource(ctx, memoryID, chatID); err != nil {
					log.Printf("REINFORCE 落库失败（不影响回复）: %v", err)
				}
			}

		case "SUPERSEDE":
			memoryID, ok := parseRef(action.Ref, "memory:")
			if !ok || !knownMemories[memoryID] {
				log.Printf("SUPERSEDE 引用了无效的记忆：%q", action.Ref)
				continue
			}
			if action.Candidate == nil || action.Candidate.Content == "" {
				log.Printf("SUPERSEDE 缺少候选内容，跳过：%q", action.Ref)
				continue
			}
			if err := s.repo.ReplaceMemory(ctx, memoryID, "superseded", action.Candidate.Content, action.Candidate.Category, userChatID); err != nil {
				log.Printf("SUPERSEDE 落库失败（不影响回复）: %v", err)
			}

		case "CORRECT":
			memoryID, ok := parseRef(action.Ref, "memory:")
			if !ok || !knownMemories[memoryID] {
				log.Printf("CORRECT 引用了无效的记忆：%q", action.Ref)
				continue
			}
			if action.Candidate != nil && action.Candidate.Content != "" {
				if err := s.repo.ReplaceMemory(ctx, memoryID, "corrected", action.Candidate.Content, action.Candidate.Category, userChatID); err != nil {
					log.Printf("CORRECT 落库失败（不影响回复）: %v", err)
				}
			} else {
				if err := s.repo.SetMemoryStatus(ctx, memoryID, "corrected"); err != nil {
					log.Printf("CORRECT 退出召回失败（不影响回复）: %v", err)
				}
			}
		}
	}
}

func buildFormationInput(chatMessageID int64, userMessage string, memories []model.Memory) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "当前消息：\nchat:%d | %s\n\n已有长期记忆：\n", chatMessageID, userMessage)
	if len(memories) == 0 {
		sb.WriteString("（无）\n")
		return sb.String()
	}
	for _, m := range memories {
		if m.Category != "" {
			fmt.Fprintf(&sb, "memory:%d | %s（%s）\n", m.ID, m.Content, m.Category)
		} else {
			fmt.Fprintf(&sb, "memory:%d | %s\n", m.ID, m.Content)
		}
	}
	return sb.String()
}

func parseRef(ref, prefix string) (int64, bool) {
	num, ok := strings.CutPrefix(ref, prefix)
	if !ok || num == "" || num[0] == '0' {
		return 0, false
	}
	for i := 0; i < len(num); i++ {
		if num[i] < '0' || num[i] > '9' {
			return 0, false
		}
	}
	id, err := strconv.ParseInt(num, 10, 64)
	if err != nil {
		return 0, false
	}
	return id, true
}

const responseSystemPrompt = `你是 Memora，一个长期陪伴和记录用户的个人 AI。

你的目标是自然地回应用户，而不是急着分析他或解决他的问题。

* 用户主要在分享、倾诉、表达情绪或想法时：先听懂并回应，不要自动给建议、做任务拆解或心理分析。
* 用户明确求建议、信息、判断或方案时：认真帮助他解决问题。
* 细节用于理解，整体用于回应。用户一次提到很多细节时，不要逐项分析，抓住这一轮最主要的意思即可。
* 不要替用户解释隐藏动机或心理原因，不要把推断说成事实。
* 可以自然使用已有记忆让交流有连续感，但不要为了展示“记得”而强行提过去；用户当前表达优先于旧记忆。
* Formation 的理解和 guidance 只是辅助参考，不是用户亲口说出的新事实。
* 回复自然、口语化，可以稍微多说一些，但一轮只围绕一个核心点。不要求每轮都有洞见，也不要求每轮都提问。

记住：

用户想被听见时，就认真回应他；
用户真的来求解时，再认真帮他解决。
`

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
