package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/Xoyeir/Memora/internal/model"
	"github.com/Xoyeir/Memora/internal/repository"
)

// ErrInvalidMemoryParam 表示参数不合法，调用方据此返回 400。
var ErrInvalidMemoryParam = errors.New("记忆参数不合法")

const (
	defaultMemoryListLimit = 50
	maxMemoryListLimit     = 200
	maxMemoryContentRunes  = 1000
)

type MemoryList struct {
	Memories  []model.Memory `json:"memories"`
	Status    string         `json:"status"`
	Limit     int            `json:"limit"`
	Truncated bool           `json:"truncated"`
}

type MemorySourceView struct {
	Memory  model.Memory         `json:"memory"`
	Sources []model.MemorySource `json:"sources"`
}

// status=all 可看被取代/纠正/忘记的记录，它们仍在库里但不参与召回。
func (s *Service) ListMemoriesForManagement(ctx context.Context, status string, limit int) (*MemoryList, error) {
	status = strings.TrimSpace(status)

	query := ""
	switch status {
	case "", "active":
		status = model.MemoryStatusActive
		query = model.MemoryStatusActive
	case "all":
		status = "all"
	case model.MemoryStatusSuperseded, model.MemoryStatusCorrected, model.MemoryStatusForgotten:
		query = status
	default:
		return nil, fmt.Errorf("%w: 未知的记忆状态 %q", ErrInvalidMemoryParam, status)
	}

	if limit <= 0 {
		limit = defaultMemoryListLimit
	}
	if limit > maxMemoryListLimit {
		limit = maxMemoryListLimit
	}

	list, err := s.repo.ListMemoriesByStatus(ctx, query, limit+1)
	if err != nil {
		return nil, fmt.Errorf("读取记忆失败: %w", err)
	}
	out := &MemoryList{Status: status, Limit: limit}
	if len(list) > limit {
		out.Truncated = true
		list = list[:limit]
	}
	if list == nil {
		list = []model.Memory{}
	}
	out.Memories = list
	return out, nil
}

// 被改写过的记忆没有聊天来源，其依据是那次改写本身，不补造也不用旧原文充当新内容的证据。
func (s *Service) MemorySources(ctx context.Context, id int64) (*MemorySourceView, error) {
	if id <= 0 {
		return nil, fmt.Errorf("%w: 记忆编号不合法", ErrInvalidMemoryParam)
	}
	m, err := s.repo.GetMemory(ctx, id)
	if err != nil {
		if errors.Is(err, repository.ErrMemoryNotFound) {
			return nil, err
		}
		return nil, fmt.Errorf("读取记忆失败: %w", err)
	}
	sources, err := s.repo.ListMemorySources(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("读取记忆来源失败: %w", err)
	}
	if sources == nil {
		sources = []model.MemorySource{}
	}
	return &MemorySourceView{Memory: *m, Sources: sources}, nil
}

// 不就地改动原记忆，而是标记为已被用户纠正并指向新记录，原话与出处都保留。
func (s *Service) CorrectMemory(ctx context.Context, id int64, content string) (*model.Memory, error) {
	if id <= 0 {
		return nil, fmt.Errorf("%w: 记忆编号不合法", ErrInvalidMemoryParam)
	}
	content = strings.TrimSpace(content)
	if content == "" {
		return nil, fmt.Errorf("%w: 内容不能为空", ErrInvalidMemoryParam)
	}
	if utf8.RuneCountInString(content) > maxMemoryContentRunes {
		return nil, fmt.Errorf("%w: 内容最多 %d 个字", ErrInvalidMemoryParam, maxMemoryContentRunes)
	}

	m, err := s.repo.CorrectMemory(ctx, id, content)
	if err != nil {
		if errors.Is(err, repository.ErrMemoryNotFound) || errors.Is(err, repository.ErrMemoryNotActive) {
			return nil, err
		}
		return nil, fmt.Errorf("纠正记忆失败: %w", err)
	}
	return m, nil
}

// 只让记忆不再参与候选/召回/来源读取，不物理清除相关数据；以后再说起会形成新记忆。
func (s *Service) ForgetMemory(ctx context.Context, id int64) (*model.Memory, error) {
	if id <= 0 {
		return nil, fmt.Errorf("%w: 记忆编号不合法", ErrInvalidMemoryParam)
	}
	if _, err := s.repo.ForgetMemory(ctx, id); err != nil {
		if errors.Is(err, repository.ErrMemoryNotFound) {
			return nil, err
		}
		return nil, fmt.Errorf("忘记记忆失败: %w", err)
	}
	return s.repo.GetMemory(ctx, id)
}
