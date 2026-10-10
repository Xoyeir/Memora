package repository

import (
	"context"
	"database/sql"
	"errors"

	"github.com/Xoyeir/Memora/internal/model"
)

// ErrDayStateLeaseLost：这天的整理已不由本次调用负责（输入变了、用户手动设标签或租约到期被接管），结果已过期不能写回。
var ErrDayStateLeaseLost = errors.New("当天整理的执行归属已经失效")

// DayStateRow 原样读出 day_states，结果列与尝试列分开返回，由 service 判断结果是否还算数；AttemptLive 由数据库时间算出，不用进程时钟。
type DayStateRow struct {
	Day              string
	Timezone         string
	Label            string
	Summary          string
	ResultDigest     string
	InputTruncated   bool
	ManualLabel      string
	AttemptStatus    string
	AttemptDigest    string
	AttemptStartedAt sql.NullTime
	ErrorMessage     string
	AttemptLive      bool
}

// ListStateCluesBetween 只做粗筛（本人的、仍有效的情绪与活动记录，日期在区间附近）；归到哪一天、是否算依据由 service 用同一套规则判断，SQL 里不重抄。
func (r *Repository) ListStateCluesBetween(ctx context.Context, from, to string) ([]model.LifeRecord, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT `+lifeRecordColumns+`
		FROM life_records
		WHERE status = ? AND subject = ? AND kind IN (?, ?)
		  AND (occurred_on BETWEEN ? AND ? OR mentioned_on BETWEEN ? AND ?)
		ORDER BY id ASC`,
		model.LifeStatusActive, model.LifeSubjectUser, model.LifeKindMood, model.LifeKindActivity,
		from, to, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []model.LifeRecord
	for rows.Next() {
		rec, err := scanLifeRecord(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, rec)
	}
	return list, rows.Err()
}

// attempt_live 需要租约秒数，所以它永远排在参数列表最前面。
const dayStateColumns = `day, timezone, label, summary, result_digest, input_truncated,
	manual_label, attempt_status, attempt_digest, attempt_started_at, error_message,
	(attempt_status = 'running' AND attempt_started_at >= NOW() - INTERVAL ? SECOND)`

// ListDayStates 没有整理记录的日期不会出现。
func (r *Repository) ListDayStates(ctx context.Context, from, to string, leaseSeconds int) ([]DayStateRow, error) {
	rows, err := r.db.QueryContext(ctx,
		"SELECT "+dayStateColumns+" FROM day_states WHERE day BETWEEN ? AND ?",
		leaseSeconds, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []DayStateRow
	for rows.Next() {
		row, err := scanDayStateRow(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, row)
	}
	return list, rows.Err()
}

func (r *Repository) GetDayState(ctx context.Context, day string, leaseSeconds int) (*DayStateRow, error) {
	row := r.db.QueryRowContext(ctx,
		"SELECT "+dayStateColumns+" FROM day_states WHERE day = ?", leaseSeconds, day)

	out, err := scanDayStateRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// ClaimDayState 原子认领某天的自动整理：手动标签、已有当前输入的结果、同输入未过期的 running 都拒绝；超租约的 running 可重新认领；digest 为输入指纹，提交时再比一次，输入变了的迟到结果写不回去。
func (r *Repository) ClaimDayState(ctx context.Context, day, owner, digest string, leaseSeconds int) (bool, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()

	var (
		manual, resultDigest, attemptStatus, attemptDigest string
		startedAt                                          sql.NullTime
		live                                               bool
	)
	err = tx.QueryRowContext(ctx, `
		SELECT manual_label, result_digest, attempt_status, attempt_digest, attempt_started_at,
		       (attempt_status = 'running' AND attempt_started_at >= NOW() - INTERVAL ? SECOND)
		FROM day_states WHERE day = ? FOR UPDATE`, leaseSeconds, day,
	).Scan(&manual, &resultDigest, &attemptStatus, &attemptDigest, &startedAt, &live)

	switch {
	case errors.Is(err, sql.ErrNoRows):
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO day_states (day, attempt_status, attempt_owner, attempt_digest, attempt_started_at)
			VALUES (?, 'running', ?, ?, NOW())`, day, owner, digest); err != nil {
			return false, err
		}
	case err != nil:
		return false, err
	default:
		if manual != "" {
			return false, tx.Commit()
		}
		if resultDigest != "" && resultDigest == digest {
			return false, tx.Commit()
		}
		if live && attemptDigest == digest {
			return false, tx.Commit()
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE day_states
			SET attempt_status = 'running', attempt_owner = ?, attempt_digest = ?,
			    attempt_started_at = NOW(), error_message = ''
			WHERE day = ?`, owner, digest, day); err != nil {
			return false, err
		}
	}

	return true, tx.Commit()
}

// ApplyDayState 条件同时带 owner 和 digest：租约被抢或输入中途变了别人重新认领时拿到 0 行 → ErrDayStateLeaseLost，旧结果不盖新的。
func (r *Repository) ApplyDayState(ctx context.Context, day, owner, digest, timezone, label, summary string, truncated bool) error {
	res, err := r.db.ExecContext(ctx, `
		UPDATE day_states
		SET label = ?, summary = ?, result_digest = ?, timezone = ?, input_truncated = ?,
		    attempt_status = 'none', attempt_owner = NULL, attempt_digest = '', error_message = ''
		WHERE day = ? AND attempt_status = 'running' AND attempt_owner = ? AND attempt_digest = ?`,
		label, summary, digest, timezone, truncated, day, owner, digest)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrDayStateLeaseLost
	}
	return nil
}

// FailDayState 只动 attempt_*，上次成功算出的标签摘要留着继续显示；状态离开 running，之后还能被重新认领。
func (r *Repository) FailDayState(ctx context.Context, day, owner, message string) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE day_states
		SET attempt_status = 'failed', attempt_owner = NULL, error_message = ?
		WHERE day = ? AND attempt_status = 'running' AND attempt_owner = ?`,
		truncateRunes(message, 200), day, owner)
	return err
}

// SetManualDayLabel 同时作废正在进行的那次自动整理：用户已给答案，还在路上的自动结果不该再落地。
func (r *Repository) SetManualDayLabel(ctx context.Context, day, label string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO day_states (day, manual_label) VALUES (?, ?)
		ON DUPLICATE KEY UPDATE
			manual_label   = VALUES(manual_label),
			attempt_status = 'none',
			attempt_owner  = NULL,
			attempt_digest = ''`, day, label); err != nil {
		return err
	}
	return tx.Commit()
}

// ClearManualDayLabel 不动自动结果：它还在且输入没变时，取消后立刻就能复用，不必再调模型。
func (r *Repository) ClearManualDayLabel(ctx context.Context, day string) error {
	_, err := r.db.ExecContext(ctx,
		"UPDATE day_states SET manual_label = '' WHERE day = ?", day)
	return err
}

type rowScanner interface {
	Scan(dest ...any) error
}

// truncateRunes 按字符截断，避免把一个多字节字符切成半个。
func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

func scanDayStateRow(s rowScanner) (DayStateRow, error) {
	var (
		out DayStateRow
		// day 是 date 列且连接串开了 parseTime，直接扫进字符串会带时刻和时区、没法当日期键，故扫成时间再格式化。
		day sql.NullTime
	)
	err := s.Scan(
		&day, &out.Timezone, &out.Label, &out.Summary, &out.ResultDigest, &out.InputTruncated,
		&out.ManualLabel, &out.AttemptStatus, &out.AttemptDigest, &out.AttemptStartedAt,
		&out.ErrorMessage, &out.AttemptLive,
	)
	out.Day = formatDate(day)
	return out, err
}
