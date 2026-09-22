package repository

import (
	"context"

	"github.com/Xoyeir/memora/internal/model"
)

func (r *Repository) CreateChatMessage(ctx context.Context, role, content string) (int64, error) {
	res, err := r.db.ExecContext(ctx,
		"INSERT INTO chat_messages (role, content) VALUES (?, ?)",
		role, content)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (r *Repository) ListRecentChatMessages(ctx context.Context, beforeID int64, limit int) ([]model.ChatMessage, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, role, content, created_at
		FROM chat_messages
		WHERE id < ?
		ORDER BY id DESC
		LIMIT ?`, beforeID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []model.ChatMessage
	for rows.Next() {
		var m model.ChatMessage
		if err := rows.Scan(&m.ID, &m.Role, &m.Content, &m.CreatedAt); err != nil {
			return nil, err
		}
		list = append(list, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for i, j := 0, len(list)-1; i < j; i, j = i+1, j-1 {
		list[i], list[j] = list[j], list[i]
	}
	return list, nil
}
