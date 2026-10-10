package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"unicode"

	"github.com/Xoyeir/Memora/internal/model"
)

var (
	ErrMemoryNotFound  = errors.New("记忆不存在")
	ErrMemoryNotActive = errors.New("这条记忆已经不是有效记忆")
)

const memoryColumns = `id, content, COALESCE(category, ''), status, origin,
	superseded_by_id, is_pinned, created_at, updated_at`

func (r *Repository) ListMemories(ctx context.Context, limit int) ([]model.Memory, error) {
	return r.ListMemoriesByStatus(ctx, model.MemoryStatusActive, limit)
}

func (r *Repository) ListMemoriesByStatus(ctx context.Context, status string, limit int) ([]model.Memory, error) {
	where := ""
	args := make([]any, 0, 2)
	if status != "" {
		where = "WHERE status = ?"
		args = append(args, status)
	}
	args = append(args, limit)

	rows, err := r.db.QueryContext(ctx, `
		SELECT `+memoryColumns+`
		FROM memories
		`+where+`
		ORDER BY id DESC
		LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []model.Memory
	for rows.Next() {
		m, err := scanMemory(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, m)
	}

	return list, rows.Err()
}

// GetMemory 不限状态，被纠正、被忘记的也能查出。
func (r *Repository) GetMemory(ctx context.Context, id int64) (*model.Memory, error) {
	row := r.db.QueryRowContext(ctx, "SELECT "+memoryColumns+" FROM memories WHERE id = ?", id)
	m, err := scanMemory(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrMemoryNotFound
	}
	if err != nil {
		return nil, err
	}
	return &m, nil
}

func scanMemory(s rowScanner) (model.Memory, error) {
	var (
		m            model.Memory
		supersededBy sql.NullInt64
	)
	err := s.Scan(
		&m.ID, &m.Content, &m.Category, &m.Status, &m.Origin,
		&supersededBy, &m.IsPinned, &m.CreatedAt, &m.UpdatedAt,
	)
	if supersededBy.Valid {
		v := supersededBy.Int64
		m.SupersededByID = &v
	}
	return m, err
}

// maxSearchTerms 限制一次查询拆出的词数，避免超长输入拼出几百个 LIKE。
const maxSearchTerms = 16

// searchCandidateLimit 是粗筛阶段取回候选的上限；粗筛按字面命中而非时间，相关的旧记忆不会被提前丢掉。
const searchCandidateLimit = 200

// SearchMemories 检索 active 记忆：相关度 = 命中的不同查询词数，同分新的在前；只做字面重合，换个说法问同一件事会返回空，不拿最近的记忆凑数。
func (r *Repository) SearchMemories(ctx context.Context, query string, limit int) ([]model.Memory, error) {
	if limit <= 0 {
		return nil, nil
	}
	terms := searchTerms(query)
	if len(terms) == 0 {
		return nil, nil
	}

	clauses := make([]string, len(terms))
	args := make([]any, 0, len(terms)+1)
	for i, term := range terms {
		clauses[i] = `content LIKE ? ESCAPE '\\'`
		args = append(args, "%"+escapeLike(term)+"%")
	}
	args = append(args, searchCandidateLimit)

	rows, err := r.db.QueryContext(ctx, `
		SELECT `+memoryColumns+`
		FROM memories
		WHERE status = 'active' AND (`+strings.Join(clauses, " OR ")+`)
		ORDER BY id DESC
		LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var candidates []model.Memory
	for rows.Next() {
		m, err := scanMemory(rows)
		if err != nil {
			return nil, err
		}
		candidates = append(candidates, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return rankMemories(candidates, terms, limit), nil
}

func rankMemories(candidates []model.Memory, terms []string, limit int) []model.Memory {
	type scored struct {
		memory model.Memory
		score  int
	}
	ranked := make([]scored, 0, len(candidates))
	for _, m := range candidates {
		haystack := strings.ToLower(m.Content)
		score := 0
		for _, term := range terms {
			if strings.Contains(haystack, term) {
				score++
			}
		}
		if score > 0 {
			ranked = append(ranked, scored{memory: m, score: score})
		}
	}

	// 依赖候选已按 id 倒序，稳定排序后同分即「新的在前」。
	sort.SliceStable(ranked, func(i, j int) bool { return ranked[i].score > ranked[j].score })

	if len(ranked) > limit {
		ranked = ranked[:limit]
	}
	out := make([]model.Memory, len(ranked))
	for i, s := range ranked {
		out[i] = s.memory
	}
	return out
}

func searchTerms(query string) []string {
	var (
		terms []string
		cjk   []rune
		word  []rune
	)
	flushWord := func() {
		if len(word) > 0 {
			terms = append(terms, strings.ToLower(string(word)))
			word = word[:0]
		}
	}
	flushCJK := func() {
		switch len(cjk) {
		case 0:
		case 1:
			terms = append(terms, string(cjk))
		default:
			for i := 0; i+1 < len(cjk); i++ {
				terms = append(terms, string(cjk[i:i+2]))
			}
		}
		cjk = cjk[:0]
	}

	for _, r := range query {
		switch {
		case isCJK(r):
			flushWord()
			cjk = append(cjk, r)
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			flushCJK()
			word = append(word, r)
		default:
			flushCJK()
			flushWord()
		}
	}
	flushCJK()
	flushWord()

	return dedupeTerms(terms)
}

func isCJK(r rune) bool {
	return unicode.Is(unicode.Han, r)
}

func dedupeTerms(terms []string) []string {
	seen := make(map[string]bool, len(terms))
	out := make([]string, 0, len(terms))
	for _, t := range terms {
		if t == "" || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
		if len(out) == maxSearchTerms {
			break
		}
	}
	return out
}

// escapeLike 转义 % 和 _，使查询里的通配符只当普通字符（注入由占位符挡掉）。
func escapeLike(s string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return replacer.Replace(s)
}

// applyMemoryOp 在调用方事务内执行一条操作，不吞错，失败整批回滚由 ApplyFormation 决定。
func applyMemoryOp(ctx context.Context, tx *sql.Tx, op model.MemoryOp) error {
	switch op.Kind {
	case model.OpForm:
		memoryID, err := insertMemoryTx(ctx, tx, op.Content, op.Category)
		if err != nil {
			return err
		}
		return linkSourceTx(ctx, tx, memoryID, op.ChatMessageID)

	case model.OpReinforce:
		if skip, err := skipIfNotActive(ctx, tx, op.MemoryID, op.Kind); err != nil || skip {
			return err
		}
		// 同一条消息重复确认同一条记忆时，唯一键会命中；那是幂等，不是错误。
		_, err := tx.ExecContext(ctx, `
			INSERT INTO memory_sources (memory_id, chat_message_id) VALUES (?, ?)
			ON DUPLICATE KEY UPDATE memory_id = memory_id`,
			op.MemoryID, op.ChatMessageID)
		return err

	case model.OpSupersede, model.OpCorrectRewrite:
		if skip, err := skipIfNotActive(ctx, tx, op.MemoryID, op.Kind); err != nil || skip {
			return err
		}
		newStatus := model.MemoryStatusSuperseded
		if op.Kind == model.OpCorrectRewrite {
			newStatus = model.MemoryStatusCorrected
		}
		return replaceMemoryTx(ctx, tx, op.MemoryID, newStatus, op.Content, op.Category, op.ChatMessageID)

	case model.OpCorrectDrop:
		if skip, err := skipIfNotActive(ctx, tx, op.MemoryID, op.Kind); err != nil || skip {
			return err
		}
		_, err := tx.ExecContext(ctx,
			"UPDATE memories SET status = ? WHERE id = ?", model.MemoryStatusCorrected, op.MemoryID)
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

// skipIfNotActive 事务内 FOR UPDATE；返回 true 表示记忆不存在或已非 active，应跳过（期间可能已被纠正、忘记或取代，跳过不是错误）。
func skipIfNotActive(ctx context.Context, tx *sql.Tx, memoryID int64, kind string) (bool, error) {
	var status string
	err := tx.QueryRowContext(ctx,
		"SELECT status FROM memories WHERE id = ? FOR UPDATE", memoryID).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		log.Printf("记忆 %d 已不存在，跳过 %s 操作", memoryID, kind)
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if status != model.MemoryStatusActive {
		log.Printf("记忆 %d 当前为 %s，跳过 %s 操作", memoryID, status, kind)
		return true, nil
	}
	return false, nil
}

func replaceMemoryTx(ctx context.Context, tx *sql.Tx, oldMemoryID int64, newStatus, content, category string, chatMessageID int64) error {
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

// CorrectMemory 以改写后的内容新建 origin=user_corrected 记忆并取代旧的；新记忆不挂 memory_sources，它成立的理由是这次改写，挂旧来源等于张冠李戴。
func (r *Repository) CorrectMemory(ctx context.Context, id int64, content string) (*model.Memory, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var (
		oldContent  string
		oldCategory sql.NullString
		status      string
	)
	err = tx.QueryRowContext(ctx,
		"SELECT content, category, status FROM memories WHERE id = ? FOR UPDATE", id).
		Scan(&oldContent, &oldCategory, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrMemoryNotFound
	}
	if err != nil {
		return nil, err
	}
	if status != model.MemoryStatusActive {
		return nil, ErrMemoryNotActive
	}
	res, err := tx.ExecContext(ctx,
		"INSERT INTO memories (content, category, status, origin) VALUES (?, ?, ?, ?)",
		content, oldCategory, model.MemoryStatusActive, model.MemoryOriginUserCorrected)
	if err != nil {
		return nil, err
	}
	newID, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}

	if _, err := tx.ExecContext(ctx,
		"UPDATE memories SET status = ?, superseded_by_id = ? WHERE id = ?",
		model.MemoryStatusCorrected, newID, id); err != nil {
		return nil, err
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO memory_corrections (memory_id, new_memory_id, action, old_content, new_content)
		VALUES (?, ?, ?, ?, ?)`,
		id, newID, model.CorrectionActionCorrect, oldContent, content); err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return r.GetMemory(ctx, newID)
}

// ForgetMemory 软删除：不再参与候选、召回和来源读取，内容留在库里；重复操作不报错、返回 false，且不留第二笔记录。
func (r *Repository) ForgetMemory(ctx context.Context, id int64) (bool, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()

	var (
		content string
		status  string
	)
	err = tx.QueryRowContext(ctx,
		"SELECT content, status FROM memories WHERE id = ? FOR UPDATE", id).Scan(&content, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrMemoryNotFound
	}
	if err != nil {
		return false, err
	}
	if status != model.MemoryStatusActive {
		return false, tx.Commit()
	}

	if _, err := tx.ExecContext(ctx,
		"UPDATE memories SET status = ? WHERE id = ?", model.MemoryStatusForgotten, id); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO memory_corrections (memory_id, action, old_content) VALUES (?, ?, ?)`,
		id, model.CorrectionActionForget, content); err != nil {
		return false, err
	}

	return true, tx.Commit()
}

// ListMemorySources 读一条记忆的出处（聊天原文、日记原文、用户改写）；改写里 new_content 是新说法、content 是被换掉的旧说法。
func (r *Repository) ListMemorySources(ctx context.Context, memoryID int64) ([]model.MemorySource, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT 'chat', cm.content, cm.created_at, NULL, NULL
		FROM memory_sources ms
		JOIN chat_messages cm ON cm.id = ms.chat_message_id
		WHERE ms.memory_id = ?
		UNION ALL
		SELECT 'journal', j.content, j.created_at, NULL, NULL
		FROM memory_sources ms
		JOIN journals j ON j.id = ms.journal_id
		WHERE ms.memory_id = ?
		UNION ALL
		SELECT 'correction', mc.old_content, mc.created_at, mc.action, mc.new_content
		FROM memory_corrections mc
		WHERE mc.new_memory_id = ?
		ORDER BY 3 DESC`, memoryID, memoryID, memoryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []model.MemorySource
	for rows.Next() {
		var (
			m       model.MemorySource
			action  sql.NullString
			newText sql.NullString
		)
		if err := rows.Scan(&m.SourceType, &m.Content, &m.CreatedAt, &action, &newText); err != nil {
			return nil, err
		}
		m.Action = action.String
		m.NewContent = newText.String
		list = append(list, m)
	}
	return list, rows.Err()
}
