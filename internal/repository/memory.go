package repository

import (
	"context"

	"github.com/Xoyeir/memora/internal/model"
)

func (r *Repository) ListMemories(ctx context.Context, limit int) ([]model.Memory, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, content, COALESCE(category, ''), status, created_at, updated_at
		FROM memories
		WHERE status = 'active'
		ORDER BY id DESC
		LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []model.Memory
	for rows.Next() {
		var m model.Memory
		if err := rows.Scan(
			&m.ID, &m.Content, &m.Category, &m.Status,
			&m.CreatedAt, &m.UpdatedAt,
		); err != nil {
			return nil, err
		}
		list = append(list, m)
	}

	return list, rows.Err()
}

func (r *Repository) CreateMemoryFromChat(ctx context.Context, content, category string, chatMessageID int64) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx,
		"INSERT INTO memories (content, category, status) VALUES (?, ?, 'active')",
		content, category)
	if err != nil {
		return err
	}

	memoryID, err := res.LastInsertId()
	if err != nil {
		return err
	}

	_, err = tx.ExecContext(ctx,
		"INSERT INTO memory_sources (memory_id, chat_message_id) VALUES (?, ?)",
		memoryID, chatMessageID)
	if err != nil {
		return err
	}

	return tx.Commit()
}
