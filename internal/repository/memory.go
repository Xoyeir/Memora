package repository

import (
	"context"
	"database/sql"
	"fmt"
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

// applyMemoryOp 在调用方给出的事务里执行一条记忆操作。
//
// 这里不收尾、不开事务、也不吞错：任何失败都往上抛，由 ApplyFormation 决定整批回滚。
func applyMemoryOp(ctx context.Context, tx *sql.Tx, op model.MemoryOp) error {
	switch op.Kind {
	case model.OpForm:
		memoryID, err := insertMemoryTx(ctx, tx, op.Content, op.Category)
		if err != nil {
			return err
		}
		return linkSourceTx(ctx, tx, memoryID, op.ChatMessageID)

	case model.OpReinforce:
		if err := mustExistTx(ctx, tx, op.MemoryID, op.Kind); err != nil {
			return err
		}
		// 同一条消息重复确认同一条记忆时，唯一键会命中；那是幂等，不是错误。
		_, err := tx.ExecContext(ctx, `
			INSERT INTO memory_sources (memory_id, chat_message_id) VALUES (?, ?)
			ON DUPLICATE KEY UPDATE memory_id = memory_id`,
			op.MemoryID, op.ChatMessageID)
		return err

	case model.OpSupersede, model.OpCorrectRewrite:
		newStatus := "superseded"
		if op.Kind == model.OpCorrectRewrite {
			newStatus = "corrected"
		}
		return replaceMemoryTx(ctx, tx, op.MemoryID, newStatus, op.Content, op.Category, op.ChatMessageID)

	case model.OpCorrectDrop:
		if err := mustExistTx(ctx, tx, op.MemoryID, op.Kind); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx,
			"UPDATE memories SET status = 'corrected' WHERE id = ?", op.MemoryID)
		return err

	default:
		return fmt.Errorf("未知的记忆操作 %q", op.Kind)
	}
}

func insertMemoryTx(ctx context.Context, tx *sql.Tx, content, category string) (int64, error) {
	res, err := tx.ExecContext(ctx,
		"INSERT INTO memories (content, category, status) VALUES (?, ?, 'active')",
		content, category)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func linkSourceTx(ctx context.Context, tx *sql.Tx, memoryID, chatMessageID int64) error {
	_, err := tx.ExecContext(ctx,
		"INSERT INTO memory_sources (memory_id, chat_message_id) VALUES (?, ?)",
		memoryID, chatMessageID)
	return err
}

// mustExistTx 在事务内确认记忆存在并锁住它。
// 校验发生在事务外，到写入之间可能已经变了，所以这里不能只信校验结果。
func mustExistTx(ctx context.Context, tx *sql.Tx, memoryID int64, kind string) error {
	var found int
	if err := tx.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM memories WHERE id = ? FOR UPDATE", memoryID).Scan(&found); err != nil {
		return err
	}
	if found == 0 {
		return fmt.Errorf("记忆 %d 不存在，无法执行 %s", memoryID, kind)
	}
	return nil
}

func replaceMemoryTx(ctx context.Context, tx *sql.Tx, oldMemoryID int64, newStatus, content, category string, chatMessageID int64) error {
	if err := mustExistTx(ctx, tx, oldMemoryID, "replace"); err != nil {
		return err
	}

	newID, err := insertMemoryTx(ctx, tx, content, category)
	if err != nil {
		return err
	}

	if _, err := tx.ExecContext(ctx,
		"UPDATE memories SET status = ?, superseded_by_id = ? WHERE id = ?",
		newStatus, newID, oldMemoryID); err != nil {
		return err
	}

	return linkSourceTx(ctx, tx, newID, chatMessageID)
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
