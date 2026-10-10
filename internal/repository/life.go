package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Xoyeir/Memora/internal/model"
)

// LifeRecordFilter 日期有两个口径：Occurred* 是事情发生日、Mentioned* 是消息被说出的日子，同一查询只允许一种（service 保证）；发生日未知的记录只能按 Mentioned 查。
type LifeRecordFilter struct {
	OccurredFrom  string // YYYY-MM-DD，空表示不限
	OccurredTo    string
	MentionedFrom string
	MentionedTo   string
	Query         string // 按字面匹配，不做同义扩展
	Kinds         []string
	Subject       string
	Status        string
	Limit         int
}

const lifeRecordColumns = `id, kind, state, basis, subject, content, chat_message_id, evidence_quote,
	mentioned_on, occurred_on, occurred_month, occurred_day,
	date_precision, date_text, status, corrected_by_id, created_at`

// ListLifeRecords 只读；默认只返回仍成立的记录，Status 传 corrected 可显式查出被纠正的。
func (r *Repository) ListLifeRecords(ctx context.Context, f LifeRecordFilter) ([]model.LifeRecord, error) {
	where := make([]string, 0, 5)
	args := make([]any, 0, 8)

	if f.Status != "" {
		where = append(where, "status = ?")
		args = append(args, f.Status)
	}
	if f.Subject != "" {
		where = append(where, "subject = ?")
		args = append(args, f.Subject)
	}
	if len(f.Kinds) > 0 {
		placeholders := make([]string, len(f.Kinds))
		for i, k := range f.Kinds {
			placeholders[i] = "?"
			args = append(args, k)
		}
		where = append(where, "kind IN ("+strings.Join(placeholders, ", ")+")")
	}
	if f.OccurredFrom != "" {
		where = append(where, "occurred_on >= ?")
		args = append(args, f.OccurredFrom)
	}
	if f.OccurredTo != "" {
		where = append(where, "occurred_on <= ?")
		args = append(args, f.OccurredTo)
	}
	if f.MentionedFrom != "" {
		where = append(where, "mentioned_on >= ?")
		args = append(args, f.MentionedFrom)
	}
	if f.MentionedTo != "" {
		where = append(where, "mentioned_on <= ?")
		args = append(args, f.MentionedTo)
	}
	if strings.TrimSpace(f.Query) != "" {
		terms := searchTerms(f.Query)
		if len(terms) == 0 {
			// 没有可匹配的字词时明确返回空，而不是退化成「不限内容」把整库倒出来。
			return nil, nil
		}
		clauses := make([]string, len(terms))
		for i, term := range terms {
			clauses[i] = `content LIKE ? ESCAPE '\\'`
			args = append(args, "%"+escapeLike(term)+"%")
		}
		where = append(where, "("+strings.Join(clauses, " OR ")+")")
	}

	sqlText := "SELECT " + lifeRecordColumns + " FROM life_records"
	if len(where) > 0 {
		sqlText += " WHERE " + strings.Join(where, " AND ")
	}
	sqlText += " ORDER BY id DESC LIMIT ?"
	args = append(args, f.Limit)

	rows, err := r.db.QueryContext(ctx, sqlText, args...)
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

// ListRecentLifeRecords 供 Formation 引用，只含本人、仍成立的记录：别人的和被推翻的不进列表，模型也没机会去「纠正」它们。
func (r *Repository) ListRecentLifeRecords(ctx context.Context, limit int) ([]model.LifeRecord, error) {
	return r.ListLifeRecords(ctx, LifeRecordFilter{
		Subject: model.LifeSubjectUser,
		Status:  model.LifeStatusActive,
		Limit:   limit,
	})
}

func scanLifeRecord(rows *sql.Rows) (model.LifeRecord, error) {
	var (
		rec                        model.LifeRecord
		mentionedOn, occurredOn    sql.NullTime
		occurredMonth, occurredDay sql.NullInt64
		dateText                   sql.NullString
		correctedByID              sql.NullInt64
	)
	if err := rows.Scan(
		&rec.ID, &rec.Kind, &rec.State, &rec.Basis, &rec.Subject, &rec.Content,
		&rec.ChatMessageID, &rec.EvidenceQuote,
		&mentionedOn, &occurredOn, &occurredMonth, &occurredDay,
		&rec.DatePrecision, &dateText, &rec.Status, &correctedByID, &rec.CreatedAt,
	); err != nil {
		return rec, err
	}

	rec.MentionedOn = formatDate(mentionedOn)
	rec.OccurredOn = formatDate(occurredOn)
	rec.OccurredMonth = int(occurredMonth.Int64)
	rec.OccurredDay = int(occurredDay.Int64)
	rec.DateText = dateText.String
	rec.CorrectedByID = correctedByID.Int64
	return rec, nil
}

func formatDate(d sql.NullTime) string {
	if !d.Valid {
		return ""
	}
	return d.Time.Format(time.DateOnly)
}

// applyLifeRecordOp 在调用方事务内写入生活记录；纠正关联只信数据库当前状态，不照搬模型给的编号（旧记录可能已被别的纠正推翻）。
func applyLifeRecordOp(ctx context.Context, tx *sql.Tx, op model.LifeRecordOp) error {
	res, err := tx.ExecContext(ctx, `
		INSERT INTO life_records
			(kind, state, basis, subject, content, chat_message_id, evidence_quote,
			 mentioned_on, occurred_on, occurred_month, occurred_day, date_precision, date_text, status)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'active')`,
		op.Kind, op.State, op.Basis, op.Subject, op.Content, op.ChatMessageID, op.EvidenceQuote,
		op.MentionedOn, nullString(op.OccurredOn), nullInt(op.OccurredMonth), nullInt(op.OccurredDay),
		op.DatePrecision, nullString(op.DateText))
	if err != nil {
		return err
	}
	if op.CorrectsID == 0 {
		return nil
	}

	newID, err := res.LastInsertId()
	if err != nil {
		return err
	}

	var status string
	err = tx.QueryRowContext(ctx,
		"SELECT status FROM life_records WHERE id = ? FOR UPDATE", op.CorrectsID).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if status != model.LifeStatusActive {
		// 已被别的纠正推翻过，不重复修改，新记录照常留下。
		return nil
	}

	_, err = tx.ExecContext(ctx,
		"UPDATE life_records SET status = ?, corrected_by_id = ? WHERE id = ?",
		model.LifeStatusCorrected, newID, op.CorrectsID)
	if err != nil {
		return fmt.Errorf("标记被纠正的记录 %d 失败: %w", op.CorrectsID, err)
	}
	return nil
}

func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullInt(n int) any {
	if n == 0 {
		return nil
	}
	return n
}
