package service

import (
	"context"
	"fmt"

	"github.com/Xoyeir/memora/internal/llm"
	"github.com/Xoyeir/memora/internal/model"
	"github.com/Xoyeir/memora/internal/repository"
)

type Service struct {
	repo *repository.Repository
	llm  *llm.Client
}

func NewService(repo *repository.Repository, llm *llm.Client) *Service {
	return &Service{repo: repo, llm: llm}
}

type CreateJournalResult struct {
	JournalID int64              `json:"journal_id"`
	Insight   model.JournalInsight `json:"insight"`
	Memories  []model.Memory     `json:"memories"`
}

func (s *Service) CreateJournal(ctx context.Context, content string) (*CreateJournalResult, error) {
	journalID, err := s.repo.CreateJournal(ctx, content)
	if err != nil {
		return nil, fmt.Errorf("保存日记失败: %w", err)
	}

	if err := s.repo.CreateInsight(ctx, journalID); err != nil {
		return nil, fmt.Errorf("创建分析任务失败: %w", err)
	}

	extraction, err := s.llm.ExtractJournal(ctx, content)
	if err != nil {
		if failErr := s.repo.FailInsight(ctx, journalID, err.Error()); failErr != nil {
			return nil, fmt.Errorf("标记分析失败时出错: %w", failErr)
		}
		return nil, fmt.Errorf("LLM 提取失败: %w", err)
	}

	insight := model.JournalInsight{
		JournalID:    journalID,
		Status:       "completed",
		Summary:      extraction.Summary,
		Moods:        extraction.Moods,
		Events:       extraction.Events,
		Achievements: extraction.Achievements,
		Goals:        extraction.Goals,
		Gratitude:    extraction.Gratitude,
	}

	if err := s.repo.CompleteInsight(ctx, journalID, insight); err != nil {
		return nil, fmt.Errorf("保存分析结果失败: %w", err)
	}

	memories := make([]model.Memory, 0, len(extraction.Memories))
	for _, mc := range extraction.Memories {
		m := model.Memory{
			SourceJournalID: journalID,
			Content:         mc.Content,
			Category:        mc.Category,
			Status:          "active",
		}
		if err := s.repo.CreateMemory(ctx, m); err != nil {
			return nil, fmt.Errorf("保存记忆失败: %w", err)
		}
		memories = append(memories, m)
	}

	return &CreateJournalResult{
		JournalID: journalID,
		Insight:   insight,
		Memories:  memories,
	}, nil
}

func (s *Service) ListJournals(ctx context.Context, limit int) ([]model.JournalWithInsight, error) {
	return s.repo.ListJournals(ctx, limit)
}