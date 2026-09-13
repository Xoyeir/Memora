package repository

import (
	"context"

	"github.com/Xoyeir/memora/internal/model"
)

func (r *Repository) CreateMemory(ctx context.Context, m model.Memory) error {
	_, err := r.db.ExecContext(ctx,
		"INSERT INTO memories (source_journal_id, content, category, status) VALUES (?, ?, ?, 'active')",
		m.SourceJournalID, m.Content, m.Category)
	return err
}
