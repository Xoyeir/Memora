package repository

import (
	"context"
	"strings"

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

func (r *Repository) SearchMemories(ctx context.Context, query string, limit int) ([]model.Memory, error) {
	words := strings.Fields(query)
	where := "status = 'active'"
	args := make([]any, 0, len(words)+1)
	if len(words) > 0 {
		clauses := make([]string, len(words))
		for i, w := range words {
			clauses[i] = "content LIKE ?"
			args = append(args, "%"+w+"%")
		}
		where += " AND (" + strings.Join(clauses, " OR ") + ")"
	}
	args = append(args, limit)

	rows, err := r.db.QueryContext(ctx, `
		SELECT id, content, COALESCE(category, ''), status, created_at, updated_at
		FROM memories
		WHERE `+where+`
		ORDER BY id DESC
		LIMIT ?`, args...)
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

func (r *Repository) AddMemoryChatSource(ctx context.Context, memoryID, chatMessageID int64) error {
	_, err := r.db.ExecContext(ctx,
		"INSERT INTO memory_sources (memory_id, chat_message_id) VALUES (?, ?)",
		memoryID, chatMessageID)
	return err
}

func (r *Repository) ReplaceMemory(ctx context.Context, oldMemoryID int64, newStatus, content, category string, chatMessageID int64) error {
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

	newID, err := res.LastInsertId()
	if err != nil {
		return err
	}

	if _, err := tx.ExecContext(ctx,
		"UPDATE memories SET status = ?, superseded_by_id = ? WHERE id = ?",
		newStatus, newID, oldMemoryID); err != nil {
		return err
	}

	if _, err := tx.ExecContext(ctx,
		"INSERT INTO memory_sources (memory_id, chat_message_id) VALUES (?, ?)",
		newID, chatMessageID); err != nil {
		return err
	}

	return tx.Commit()
}

func (r *Repository) SetMemoryStatus(ctx context.Context, memoryID int64, status string) error {
	_, err := r.db.ExecContext(ctx,
		"UPDATE memories SET status = ? WHERE id = ?", status, memoryID)
	return err
}

func (r *Repository) ListMemorySources(ctx context.Context, memoryID int64) ([]model.MemorySource, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT 'chat', cm.content, cm.created_at
		FROM memory_sources ms
		JOIN chat_messages cm ON cm.id = ms.chat_message_id
		WHERE ms.memory_id = ?
		UNION ALL
		SELECT 'journal', j.content, j.created_at
		FROM memory_sources ms
		JOIN journals j ON j.id = ms.journal_id
		WHERE ms.memory_id = ?`, memoryID, memoryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []model.MemorySource
	for rows.Next() {
		var m model.MemorySource
		if err := rows.Scan(&m.SourceType, &m.Content, &m.CreatedAt); err != nil {
			return nil, err
		}
		list = append(list, m)
	}
	return list, rows.Err()
}
