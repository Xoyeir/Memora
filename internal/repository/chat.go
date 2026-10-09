package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/Xoyeir/memora/internal/model"
)

// ErrFormationLeaseLost 表示本次执行已经不再持有这轮的 Formation。
//
// 出现它意味着租约过期后有别处重新认领并提交了结果。
// 此时不是「失败」，而是「不该再写」：整批回滚，并且不得把状态降回 failed。
var ErrFormationLeaseLost = errors.New("formation 执行归属已经失效")

// EnsureUserMessage 按 client_id 幂等地拿到本轮用户消息的行。
//
// 第一次调用会插入；之后同一个 client_id 再来（重试、重复投递、并发重复）只会取回同一行，
// 不会产生第二条用户消息。并发安全由 uk_chat_client_id 唯一键保证。
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

// FindTurnByClientID 只读地取回一轮，用于恢复入口。没有则返回 (nil, nil)。
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

func (r *Repository) findTurn(ctx context.Context, id int64) (*model.ChatTurn, error) {
	var turn model.ChatTurn
	err := r.db.QueryRowContext(ctx,
		"SELECT id, content, status, formation_status FROM chat_messages WHERE id = ?", id).
		Scan(&turn.ID, &turn.Content, &turn.Status, &turn.FormationStatus)
	if err != nil {
		return nil, err
	}
	return &turn, nil
}

// ClaimFormation 原子地认领本轮的 Formation，并写入本次执行的归属标识。
//
// 返回 true 表示本次调用拿到了执行权。可认领的情况只有两种：
//   - 尚未尝试（pending）或上次失败（failed）；
//   - 处于 running 但已经超过租约时长，说明原执行者已经不在了。
//
// 未过期的 running 不会被抢走，所以正常处理中的请求不会被误判为失效。
// applied 永远不可认领，已写入的记忆不会被执行第二遍。
//
// leaseSeconds 必须明显大于一次 Formation 模型调用的最坏耗时，
// 否则正常执行中的会被误判为过期。
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

// FormationInProgress 判断这一轮是否有仍然有效的 Formation 在执行。
//
// 只看没过期的 running。超过租约的 running 说明原执行者已经不在了，
// 不能被当成「正在处理中」——否则一次进程崩溃会让这一轮永远走不下去：
// 回复没成功，恢复入口会因为「回复还没成功」而拒绝；
// 重试又会被当成「正在处理中」而拒绝。
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

// ApplyFormation 把这一批记忆操作和 formation_status=applied 放在同一个事务里提交。
//
// 模型调用已经在事务外完成；这里只有数据库写入。任一写入失败整批回滚，
// 状态由调用方另行标记为 failed，以便重新尝试。
//
// 事务内的状态更新带 owner 条件：如果租约已经过期并被别处接管，
// 这里会拿到 0 行，整批回滚并返回 ErrFormationLeaseLost——
// 旧执行者的结果不会覆盖新执行者的结果。
func (r *Repository) ApplyFormation(ctx context.Context, userMessageID int64, owner string, ops []model.MemoryOp) error {
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

// FailFormation 把本轮标记为可以重新尝试。
//
// 只在仍然持有归属、且状态还是 running 时才生效：
// 已经提交成功的 applied 不会被后续的错误处理降回 failed。
func (r *Repository) FailFormation(ctx context.Context, userMessageID int64, owner string) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE chat_messages
		SET formation_status = 'failed', formation_owner = NULL
		WHERE id = ? AND formation_status = 'running' AND formation_owner = ?`,
		userMessageID, owner)
	return err
}

// GetReply 读取本轮已经成功保存的助手回复。没有则返回空串。
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

// CompleteChatTurn 保存完整回复并把本轮标记为完成。
//
// reply_to_id 上的唯一键让重复调用只会覆盖同一条回复，不会累积多条助手消息。
// 已有的 ok 回复不会被后来的尝试改写：先成功的那次结果就是这一轮的稳定结果。
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

	// 同一轮的重复调用写入的是同一个值，这里不需要额外条件。
	if _, err := tx.ExecContext(ctx,
		"UPDATE chat_messages SET status = 'ok' WHERE id = ?", userMessageID); err != nil {
		return err
	}

	return tx.Commit()
}

// FailChatTurn 记录一次未完成的轮次。
//
// partialReply 非空说明已经向用户输出了部分内容，这部分也会落库（状态 partial），
// 用来区分「回复断流」和「整轮没有发生」。
//
// 它绝不会动一个已经成功的轮次：迟到的失败、取消或旧执行者都不能覆盖完整回复。
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
		// 这一轮没有任何输出，上一次留下的半成品不应该继续存在。
		return err
	}

	if _, err := tx.ExecContext(ctx,
		"UPDATE chat_messages SET status = ? WHERE id = ? AND status <> 'ok'", status, userMessageID); err != nil {
		return err
	}

	return tx.Commit()
}

// ListChatMessages 是给模型用的上下文查询：只有完整结束的轮次。
//
// 断流的轮次不能进这里。一条写到一半的回复被当成完整回答送进上下文，
// 模型会把它当作自己已经说过的完整话承接下去，用户看到的前后文就串了。
//
// 展示用的是 ListConversationForDisplay，两者刻意分开。
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

// ListConversationForDisplay 是给人看的查询，比上下文查询多两类行。
//
// 三种情况：
//   - 完整结束的轮次（ok），和上下文查询一致；
//   - 写到一半被打断的回复（partial），连它上面那条用户消息一起，
//     这样用户刷新后能看见自己说过什么、又断了在哪；
//   - 完全没有助手输出的失败轮次，仍然不出现。
//     一条没有回复的失败消息留在界面上只是在提醒用户「你失败了」，
//     他要的是重发或重试，不是一条墓碑。
//
// 比上下文查询多返回 status 和 client_id：前者用来标注「不完整」，
// 后者让刷新后的重试仍然落在原来那一轮上。
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
