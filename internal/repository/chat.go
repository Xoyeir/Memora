package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/Xoyeir/Memora/internal/model"
)

// ErrFormationLeaseLost：本次执行已不再持有本轮 Formation；这不是失败而是不该再写，整批回滚且不得把状态降回 failed。
var ErrFormationLeaseLost = errors.New("formation 执行归属已经失效")

// EnsureUserMessage 按 client_id 幂等取回本轮用户消息（重试、重复投递返回同一行），并发安全由 uk_chat_client_id 唯一键保证。
func (r *Repository) EnsureUserMessage(ctx context.Context, clientID, content string) (*model.ChatTurn, error) {
	res, err := r.db.ExecContext(ctx, `
		INSERT INTO chat_messages (role, content, client_id, status, formation_status)
		VALUES ('user', ?, ?, 'pending', 'pending')
		ON DUPLICATE KEY UPDATE id = LAST_INSERT_ID(id)`, content, clientID)
	if err != nil {
		return nil, err
	}

	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}

	return r.findTurn(ctx, id)
}

// FindTurnByClientID 用于恢复入口，只读；没有则返回 (nil, nil)。
func (r *Repository) FindTurnByClientID(ctx context.Context, clientID string) (*model.ChatTurn, error) {
	var id int64
	err := r.db.QueryRowContext(ctx,
		"SELECT id FROM chat_messages WHERE client_id = ? AND role = 'user'", clientID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return r.findTurn(ctx, id)
}

// findTurn 带回 created_at：相对日期以它为准，重试读到的仍是第一次说这句话的时间。
func (r *Repository) findTurn(ctx context.Context, id int64) (*model.ChatTurn, error) {
	var turn model.ChatTurn
	err := r.db.QueryRowContext(ctx,
		"SELECT id, content, status, formation_status, created_at FROM chat_messages WHERE id = ?", id).
		Scan(&turn.ID, &turn.Content, &turn.Status, &turn.FormationStatus, &turn.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &turn, nil
}

// ClaimFormation 原子认领本轮 Formation：只有 pending/failed 和超租约的 running 可认领，applied 永不认领；leaseSeconds 须明显大于一次模型调用的最坏耗时。
func (r *Repository) ClaimFormation(ctx context.Context, userMessageID int64, owner string, leaseSeconds int) (bool, error) {
	res, err := r.db.ExecContext(ctx, `
		UPDATE chat_messages
		SET formation_status = 'running',
		    formation_owner = ?,
		    formation_started_at = NOW()
		WHERE id = ?
		  AND role = 'user'
		  AND (
		        formation_status IN ('pending', 'failed')
		     OR (formation_status = 'running'
		         AND formation_started_at < NOW() - INTERVAL ? SECOND)
		      )`, owner, userMessageID, leaseSeconds)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n == 1, nil
}

// FormationInProgress 只看未过期的 running；把崩溃遗留的 running 当成「处理中」会让这一轮永远走不下去。
func (r *Repository) FormationInProgress(ctx context.Context, userMessageID int64, leaseSeconds int) (bool, error) {
	var n int
	err := r.db.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM chat_messages
		WHERE id = ?
		  AND formation_status = 'running'
		  AND formation_started_at >= NOW() - INTERVAL ? SECOND`,
		userMessageID, leaseSeconds).Scan(&n)
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// ApplyFormation 在同一事务写入记忆操作、生活记录和 applied 状态，任一失败整批回滚；状态更新带 owner 条件，租约被接管时 0 行 → ErrFormationLeaseLost，旧结果不覆盖新结果。
func (r *Repository) ApplyFormation(ctx context.Context, userMessageID int64, owner string, ops []model.MemoryOp, lifeOps []model.LifeRecordOp) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for i, op := range ops {
		if err := applyMemoryOp(ctx, tx, op); err != nil {
			return fmt.Errorf("第 %d 条记忆操作失败: %w", i, err)
		}
	}

	for i, op := range lifeOps {
		if err := applyLifeRecordOp(ctx, tx, op); err != nil {
			return fmt.Errorf("第 %d 条生活记录失败: %w", i, err)
		}
	}

	res, err := tx.ExecContext(ctx, `
		UPDATE chat_messages
		SET formation_status = 'applied', formation_owner = NULL
		WHERE id = ? AND formation_status = 'running' AND formation_owner = ?`,
		userMessageID, owner)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrFormationLeaseLost
	}

	return tx.Commit()
}

// FailFormation 只在仍持有归属且状态还是 running 时生效，已 applied 的不会被降回 failed。
func (r *Repository) FailFormation(ctx context.Context, userMessageID int64, owner string) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE chat_messages
		SET formation_status = 'failed', formation_owner = NULL
		WHERE id = ? AND formation_status = 'running' AND formation_owner = ?`,
		userMessageID, owner)
	return err
}

func (r *Repository) GetReply(ctx context.Context, userMessageID int64) (string, error) {
	var content string
	err := r.db.QueryRowContext(ctx,
		"SELECT content FROM chat_messages WHERE reply_to_id = ? AND status = 'ok'", userMessageID).
		Scan(&content)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return content, err
}

// CompleteChatTurn 靠 reply_to_id 唯一键让重复调用只覆盖同一条回复，已有的 ok 回复不会被改写。
func (r *Repository) CompleteChatTurn(ctx context.Context, userMessageID int64, reply string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO chat_messages (role, content, status, reply_to_id)
		VALUES ('assistant', ?, 'ok', ?)
		ON DUPLICATE KEY UPDATE
			content = IF(status = 'ok', content, VALUES(content)),
			status  = 'ok'`,
		reply, userMessageID); err != nil {
		return err
	}

	if _, err := tx.ExecContext(ctx,
		"UPDATE chat_messages SET status = 'ok' WHERE id = ?", userMessageID); err != nil {
		return err
	}

	return tx.Commit()
}

// FailChatTurn 记录未完成的轮次：partialReply 非空时以 partial 落库，区分「回复断流」与「整轮未发生」；不覆盖已成功的轮次。
func (r *Repository) FailChatTurn(ctx context.Context, userMessageID int64, status, partialReply string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if partialReply != "" {
		// 已经成功过的一轮不会被改成 partial。
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO chat_messages (role, content, status, reply_to_id)
			VALUES ('assistant', ?, 'partial', ?)
			ON DUPLICATE KEY UPDATE
				content = IF(status = 'ok', content, VALUES(content)),
				status  = IF(status = 'ok', status,  VALUES(status))`,
			partialReply, userMessageID); err != nil {
			return err
		}
	} else if _, err := tx.ExecContext(ctx,
		"DELETE FROM chat_messages WHERE reply_to_id = ? AND status <> 'ok'", userMessageID); err != nil {
		return err
	}

	if _, err := tx.ExecContext(ctx,
		"UPDATE chat_messages SET status = ? WHERE id = ? AND status <> 'ok'", status, userMessageID); err != nil {
		return err
	}

	return tx.Commit()
}

// ListChatMessages 是模型上下文查询，只取完整结束的轮次：写到一半的回复混进上下文会被当成说过的完整话。
func (r *Repository) ListChatMessages(ctx context.Context, beforeID int64, limit int) ([]model.ChatMessage, error) {
	where := "WHERE status = 'ok'"
	args := make([]any, 0, 2)
	if beforeID > 0 {
		where += " AND id < ?"
		args = append(args, beforeID)
	}
	args = append(args, limit)

	rows, err := r.db.QueryContext(ctx, `
		SELECT id, role, content, created_at
		FROM chat_messages
		`+where+`
		ORDER BY id DESC
		LIMIT ?`, args...)
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

// ListUserMessagesBetween 取可见轮次里的用户消息，可见规则同 ListConversationForDisplay；区间边界按存储时区算好，属于哪一天回 Go 里按业务时区判断，两种时区不混。
func (r *Repository) ListUserMessagesBetween(ctx context.Context, from, to time.Time) ([]model.ChatMessage, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT m.id, m.content, m.created_at
		FROM chat_messages m
		WHERE m.role = 'user'
		  AND m.created_at >= ? AND m.created_at < ?
		  AND (m.status IN ('ok', 'partial')
		       OR EXISTS (
		            SELECT 1 FROM chat_messages p
		            WHERE p.reply_to_id = m.id AND p.status = 'partial'))
		ORDER BY m.id ASC`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []model.ChatMessage
	for rows.Next() {
		var m model.ChatMessage
		if err := rows.Scan(&m.ID, &m.Content, &m.CreatedAt); err != nil {
			return nil, err
		}
		m.Role = "user"
		list = append(list, m)
	}
	return list, rows.Err()
}

// ListConversationForDisplay 比上下文查询多返回：被打断的回复及上方用户消息，以及 status（标注不完整）和 client_id（让重试落在原轮次）；完全失败的轮次不出现。
func (r *Repository) ListConversationForDisplay(ctx context.Context, limit int) ([]model.ChatMessage, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT m.id, m.role, m.content, m.status, IFNULL(m.client_id, ''), m.created_at
		FROM chat_messages m
		WHERE m.status IN ('ok', 'partial')
		   OR (m.role = 'user' AND EXISTS (
		        SELECT 1 FROM chat_messages p
		        WHERE p.reply_to_id = m.id AND p.status = 'partial'))
		ORDER BY m.id DESC
		LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []model.ChatMessage
	for rows.Next() {
		var m model.ChatMessage
		if err := rows.Scan(&m.ID, &m.Role, &m.Content, &m.Status, &m.ClientID, &m.CreatedAt); err != nil {
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
