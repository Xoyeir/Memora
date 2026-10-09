package service

import (
	"context"
	"fmt"

	"github.com/Xoyeir/Memora/internal/model"
	"github.com/Xoyeir/Memora/internal/repository"
)

type Service struct {
	repo  *repository.Repository
	llm   LLMClient
	clock *Clock
}

func NewService(repo *repository.Repository, llm LLMClient, clock *Clock) *Service {
	return &Service{repo: repo, llm: llm, clock: clock}
}

type CreateJournalResult struct {
	JournalID int64                `json:"journal_id"`
	Insight   model.JournalInsight `json:"insight"`
	Memories  []model.Memory       `json:"memories"`
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

	memories := make([]model.Memory, 0, len(extraction.Memories))
	for _, mc := range extraction.Memories {
		memories = append(memories, model.Memory{
			Content:  mc.Content,
			Category: mc.Category,
			Status:   "active",
		})
	}

	if err := s.repo.SaveAnalysisResult(ctx, journalID, insight, memories); err != nil {
		return nil, fmt.Errorf("保存分析结果失败: %w", err)
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
