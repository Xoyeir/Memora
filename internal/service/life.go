package service

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Xoyeir/Memora/internal/llm"
	"github.com/Xoyeir/Memora/internal/model"
	"github.com/Xoyeir/Memora/internal/repository"
)

// ErrInvalidLifeQuery 表示读取参数不合法。调用方据此返回 400，而不是 500。
var ErrInvalidLifeQuery = errors.New("生活记录查询参数不合法")

const (
	// 与 life_records 对应列的列宽一致
	maxRecordContent = 500
	maxEvidenceQuote = 255
	maxDateText      = 64
	// 相对天数的绝对值上限：「三年前」不该被当成相对日期
	maxRelativeOffsetDays  = 366
	maxQueryRangeDays      = 366
	defaultLifeRecordLimit = 50
	maxLifeRecordLimit     = 200
	// 送进 Formation 供引用的最近记录条数
	recentLifeRecordRefs = 20
)

// 只做校验与换算，不碰数据库；mentionedOn 是这条消息所属业务自然日，相对日期以它为基准，重试/恢复不会让「昨天」漂。
func planLifeRecords(records []llm.LifeRecord, turnID int64, turnContent, mentionedOn string,
	known map[int64]bool) []model.LifeRecordOp {

	base, err := time.Parse(time.DateOnly, mentionedOn)
	if err != nil {
		// 基准不合法时相对日期一律记为未知，不编造基准。
		log.Printf("formation: 基准日期 %q 不合法，本轮相对日期一律记为未知", mentionedOn)
	}

	ops := make([]model.LifeRecordOp, 0, len(records))
	for _, rec := range records {
		op, ok := planLifeRecord(rec, turnID, turnContent, mentionedOn, base, known)
		if !ok {
			continue
		}
		ops = append(ops, op)
	}
	return ops
}

func planLifeRecord(rec llm.LifeRecord, turnID int64, turnContent, mentionedOn string,
	base time.Time, known map[int64]bool) (model.LifeRecordOp, bool) {

	if !validKindState(rec.Kind, rec.State) {
		log.Printf("formation: 生活记录的类型与状态不配套（%q/%q），已拒绝", rec.Kind, rec.State)
		return model.LifeRecordOp{}, false
	}
	switch rec.Basis {
	case model.LifeBasisExplicit:
	case model.LifeBasisInferred:
		// 只有 mood 允许推测；事实类若允许推测，主观印象会以事实身份写入。
		if rec.Kind != model.LifeKindMood {
			log.Printf("formation: 只有 mood 允许 inferred，%q 被推测出来，已拒绝", rec.Kind)
			return model.LifeRecordOp{}, false
		}
	default:
		log.Printf("formation: 未知的依据来源 %q，已拒绝", rec.Basis)
		return model.LifeRecordOp{}, false
	}

	if !oneOf(rec.Subject, model.LifeSubjectUser, model.LifeSubjectOther, model.LifeSubjectUnclear) {
		log.Printf("formation: 未知的主体 %q，已拒绝", rec.Subject)
		return model.LifeRecordOp{}, false
	}
	// 假设、举例、转述不能被当成发生过的事实。
	if rec.Modality != "actual" {
		log.Printf("formation: 记录不是已发生的事实（modality=%q），已拒绝", rec.Modality)
		return model.LifeRecordOp{}, false
	}

	content := strings.TrimSpace(rec.Content)
	if content == "" || utf8.RuneCountInString(content) > maxRecordContent {
		log.Printf("formation: 生活记录内容为空或过长，已拒绝")
		return model.LifeRecordOp{}, false
	}

	quote := strings.TrimSpace(rec.EvidenceQuote)
	if quote == "" || utf8.RuneCountInString(quote) > maxEvidenceQuote {
		log.Printf("formation: 生活记录缺少证据片段，已拒绝")
		return model.LifeRecordOp{}, false
	}
	// 证据必须逐字出现在本轮的 user 消息里。改写、拼接、引用助手的话都过不了这一关。
	if !strings.Contains(turnContent, quote) {
		log.Printf("formation: 证据片段不在本轮消息中，已拒绝: %q", quote)
		return model.LifeRecordOp{}, false
	}

	occurredOn, month, day, precision := resolveRecordWhen(rec.When, base)

	// 原话里的时间说法一律留下；算不出日期时宁可留着原话，也不假装知道日期。
	dateText := truncateRunes(strings.TrimSpace(rec.When.Text), maxDateText)

	// 定位不到旧记录就不猜：新记录照常留下，只是不建立纠正关联。
	var correctsID int64
	if rec.Corrects != "" {
		id, ok := parseRef(rec.Corrects, "life:")
		if !ok || !known[id] {
			log.Printf("formation: 纠正引用了不可定位的记录 %q，只保留新记录", rec.Corrects)
		} else {
			correctsID = id
		}
	}

	return model.LifeRecordOp{
		Kind:          rec.Kind,
		State:         rec.State,
		Basis:         rec.Basis,
		Subject:       rec.Subject,
		Content:       content,
		ChatMessageID: turnID,
		EvidenceQuote: quote,
		MentionedOn:   mentionedOn,
		OccurredOn:    occurredOn,
		OccurredMonth: month,
		OccurredDay:   day,
		DatePrecision: precision,
		DateText:      dateText,
		CorrectsID:    correctsID,
	}, true
}

func truncateRunes(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	return string([]rune(s)[:max])
}

// validKindState 与表上的 chk_life_kind_state 是同一条规则。
func validKindState(kind, state string) bool {
	switch kind {
	case model.LifeKindActivity:
		return oneOf(state, model.LifeStateDone, model.LifeStatePlanned, model.LifeStateNegated)
	case model.LifeKindGoal:
		return oneOf(state, model.LifeStateWish, model.LifeStatePlanned)
	case model.LifeKindMood, model.LifeKindDate:
		return state == model.LifeStateNone
	default:
		return false
	}
}

// 只认 relative/absolute/month_day 三种确定表达，其余记为未知并保留原话；base 零值表示基准不可用。
func resolveRecordWhen(w llm.RecordWhen, base time.Time) (occurredOn string, month, day int, precision string) {
	switch w.Kind {
	case "relative":
		if base.IsZero() || w.OffsetDays > maxRelativeOffsetDays || w.OffsetDays < -maxRelativeOffsetDays {
			return "", 0, 0, model.LifeDateUnknown
		}
		return base.AddDate(0, 0, w.OffsetDays).Format(time.DateOnly), 0, 0, model.LifeDateDay

	case "absolute":
		t, err := time.Parse(time.DateOnly, strings.TrimSpace(w.Date))
		if err != nil {
			return "", 0, 0, model.LifeDateUnknown
		}
		return t.Format(time.DateOnly), 0, 0, model.LifeDateDay

	case "month_day":
		if !validMonthDay(w.Month, w.Day) {
			return "", 0, 0, model.LifeDateUnknown
		}
		return "", w.Month, w.Day, model.LifeDateMonthDay

	default:
		return "", 0, 0, model.LifeDateUnknown
	}
}

func validMonthDay(month, day int) bool {
	if month < 1 || month > 12 || day < 1 || day > 31 {
		return false
	}
	t := time.Date(2024, time.Month(month), day, 0, 0, 0, 0, time.UTC)
	return t.Month() == time.Month(month) && t.Day() == day
}

func oneOf(v string, allowed ...string) bool {
	for _, a := range allowed {
		if v == a {
			return true
		}
	}
	return false
}

// Occurred*（事情发生日）与 Mentioned*（消息提及日）两组互斥；算不出发生日的记录只能按 Mentioned* 查到。
type LifeRecordQuery struct {
	OccurredFrom  string
	OccurredTo    string
	MentionedFrom string
	MentionedTo   string
	Query         string // 内容关键词，按字面匹配，不做同义扩展
	Kinds         []string
	Subject       string
	Limit         int
}

func (s *Service) ListLifeRecords(ctx context.Context, q LifeRecordQuery) ([]model.LifeRecord, error) {
	filter, err := normalizeLifeQuery(q)
	if err != nil {
		return nil, err
	}
	return s.repo.ListLifeRecords(ctx, filter)
}

func normalizeLifeQuery(q LifeRecordQuery) (repository.LifeRecordFilter, error) {
	var f repository.LifeRecordFilter

	// 两种日期口径混用会静默变成另一种语义，故直接拒绝。
	if (q.OccurredFrom != "" || q.OccurredTo != "") && (q.MentionedFrom != "" || q.MentionedTo != "") {
		return f, fmt.Errorf("%w: occurred_*（发生日期）与 mentioned_*（提及日期）只能选一种", ErrInvalidLifeQuery)
	}
	if err := checkDateRange("occurred", q.OccurredFrom, q.OccurredTo); err != nil {
		return f, err
	}
	if err := checkDateRange("mentioned", q.MentionedFrom, q.MentionedTo); err != nil {
		return f, err
	}

	for _, k := range q.Kinds {
		if !oneOf(k, model.LifeKindActivity, model.LifeKindMood, model.LifeKindGoal, model.LifeKindDate) {
			return f, fmt.Errorf("%w: 未知的记录类型 %q", ErrInvalidLifeQuery, k)
		}
	}

	// 别人的事记下来是为了区分主体，但不该混进用户自己的生活视图。
	subject := q.Subject
	switch subject {
	case "":
		subject = model.LifeSubjectUser
	case "all":
		subject = ""
	}
	if subject != "" && !oneOf(subject, model.LifeSubjectUser, model.LifeSubjectOther, model.LifeSubjectUnclear) {
		return f, fmt.Errorf("%w: 未知的主体 %q", ErrInvalidLifeQuery, q.Subject)
	}

	limit := q.Limit
	if limit <= 0 {
		limit = defaultLifeRecordLimit
	}
	if limit > maxLifeRecordLimit {
		limit = maxLifeRecordLimit
	}

	f.OccurredFrom = q.OccurredFrom
	f.OccurredTo = q.OccurredTo
	f.MentionedFrom = q.MentionedFrom
	f.MentionedTo = q.MentionedTo
	f.Query = strings.TrimSpace(q.Query)
	f.Kinds = q.Kinds
	f.Subject = subject
	// 被纠正的记录默认不返回，要查得显式指定 status。
	f.Status = model.LifeStatusActive
	f.Limit = limit
	return f, nil
}

// label 只用于错误信息；只给一端表示「某天之后/之前」，跨度上限只用在一个区间上。
func checkDateRange(label, from, to string) error {
	var fromT, toT time.Time
	if from != "" {
		t, err := time.Parse(time.DateOnly, from)
		if err != nil {
			return fmt.Errorf("%w: %s_from 必须是 YYYY-MM-DD", ErrInvalidLifeQuery, label)
		}
		fromT = t
	}
	if to != "" {
		t, err := time.Parse(time.DateOnly, to)
		if err != nil {
			return fmt.Errorf("%w: %s_to 必须是 YYYY-MM-DD", ErrInvalidLifeQuery, label)
		}
		toT = t
	}
	if !fromT.IsZero() && !toT.IsZero() {
		if toT.Before(fromT) {
			return fmt.Errorf("%w: %s_to 不能早于 %s_from", ErrInvalidLifeQuery, label, label)
		}
		if toT.Sub(fromT) > maxQueryRangeDays*24*time.Hour {
			return fmt.Errorf("%w: 日期跨度最多 %d 天", ErrInvalidLifeQuery, maxQueryRangeDays)
		}
	}
	return nil
}

func formatLifeRecordRef(r model.LifeRecord) string {
	when := r.OccurredOn
	if when == "" && r.DatePrecision == model.LifeDateMonthDay {
		when = fmt.Sprintf("%02d-%02d", r.OccurredMonth, r.OccurredDay)
	}
	if when == "" {
		when = "日期未知"
		if r.DateText != "" {
			when = fmt.Sprintf("日期未知（原话：%s）", r.DateText)
		}
	}
	return fmt.Sprintf("life:%d | [%s/%s] %s（%s）", r.ID, r.Kind, r.State, r.Content, when)
}

func ParseLimitParam(raw string) (int, error) {
	if strings.TrimSpace(raw) == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return 0, fmt.Errorf("%w: limit 必须是整数", ErrInvalidLifeQuery)
	}
	return n, nil
}

func SplitKinds(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
