package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Xoyeir/Memora/internal/model"
	"github.com/Xoyeir/Memora/internal/repository"
)

// ErrInvalidCalendarParam 表示日历参数不合法，调用方据此返回 400。
var ErrInvalidCalendarParam = errors.New("日历参数不合法")

const (
	// daySummaryLease 是某天整理任务的执行租约；超时未完成的会被重新认领。
	daySummaryLease = 2 * time.Minute

	// maxDayClues 是送进整理的有效线索条数上限，超出部分按时间从早到晚截断。
	maxDayClues = 40
	// maxDayMessages / maxDayMessageRunes / maxDaySummaryInputRunes 控制输入长度；截断会被标出来。
	maxDayMessages          = 12
	maxDayMessageRunes      = 400
	maxDaySummaryInputRunes = 6000

	// maxDayLabelRunes / maxDaySummaryRunes 与列宽一致，且都留有余量。
	maxDayLabelRunes   = 12
	maxDaySummaryRunes = 200
)

type MonthView struct {
	Month    string           `json:"month"`
	Timezone string           `json:"timezone"`
	Today    string           `json:"today"`
	Days     []model.DayState `json:"days"`
	// PendingDays 是 Days 里 NeedsSummary 为真的日期，含输入已变、仍显示旧结果的那几天。
	PendingDays []string `json:"pending_days"`
}

// CalendarMonth 只读已有数据，不在这里发起模型调用。
func (s *Service) CalendarMonth(ctx context.Context, month string) (*MonthView, error) {
	// 空月份表示当月，按当前业务时区补上，不算参数错误。
	if strings.TrimSpace(month) == "" {
		month = s.clock.Now().Format("2006-01")
	}
	year, mon, err := parseMonth(month)
	if err != nil {
		return nil, err
	}

	loc := s.clock.Location()
	first := time.Date(year, mon, 1, 0, 0, 0, 0, loc)
	next := first.AddDate(0, 1, 0)
	firstDay := first.Format(time.DateOnly)
	lastDay := next.AddDate(0, 0, -1).Format(time.DateOnly)

	msgs, err := s.repo.ListUserMessagesBetween(ctx, first, next)
	if err != nil {
		return nil, fmt.Errorf("读取聊天记录失败: %w", err)
	}
	clues, err := s.repo.ListStateCluesBetween(ctx, firstDay, lastDay)
	if err != nil {
		return nil, fmt.Errorf("读取状态线索失败: %w", err)
	}

	tz := s.timezone()
	rows, err := s.repo.ListDayStates(ctx, firstDay, lastDay, int(daySummaryLease.Seconds()))
	if err != nil {
		return nil, fmt.Errorf("读取状态日历失败: %w", err)
	}
	rowsByDay := make(map[string]repository.DayStateRow, len(rows))
	for _, r := range rows {
		rowsByDay[r.Day] = r
	}

	msgsByDay := groupMessagesByDay(msgs, loc)
	cluesByDay := groupCluesByDay(clues, firstDay, lastDay)

	today := s.clock.LocalDate(s.clock.Now())
	view := &MonthView{
		Month:    fmt.Sprintf("%04d-%02d", year, int(mon)),
		Timezone: tz,
		Today:    today,
		// 无待整理日期时也要序列化成 []：前端读字段长度，null 会让整页报错。
		PendingDays: []string{},
	}
	for d := first; d.Before(next); d = d.AddDate(0, 0, 1) {
		day := d.Format(time.DateOnly)
		var row *repository.DayStateRow
		if r, ok := rowsByDay[day]; ok {
			row = &r
		}
		st := buildDayState(day, tz, msgsByDay[day], cluesByDay[day], row)
		if st.NeedsSummary {
			view.PendingDays = append(view.PendingDays, day)
		}
		view.Days = append(view.Days, st)
	}
	return view, nil
}

type DayDetail struct {
	Day   string             `json:"day"`
	State model.DayState     `json:"state"`
	Clues []model.LifeRecord `json:"clues"`
	// 当天可见轮次里的用户原话，按时间先后。
	Messages []model.ChatMessage `json:"messages"`
}

func (s *Service) CalendarDay(ctx context.Context, day string) (*DayDetail, error) {
	if err := checkDay(day); err != nil {
		return nil, err
	}
	materials, err := s.dayMaterials(ctx, day)
	if err != nil {
		return nil, err
	}
	row, err := s.repo.GetDayState(ctx, day, int(daySummaryLease.Seconds()))
	if err != nil {
		return nil, fmt.Errorf("读取状态日历失败: %w", err)
	}

	detail := &DayDetail{
		Day:      day,
		State:    buildDayState(day, s.timezone(), materials.msgs, materials.clues, row),
		Clues:    materials.clues,
		Messages: materials.msgs,
	}
	if detail.Clues == nil {
		detail.Clues = []model.LifeRecord{}
	}
	if detail.Messages == nil {
		detail.Messages = []model.ChatMessage{}
	}
	return detail, nil
}

// SummarizeDay 在无聊天/缺线索、已有结果、或同一份输入正在整理时不调用模型。
func (s *Service) SummarizeDay(ctx context.Context, day string) (*model.DayState, error) {
	if err := checkDay(day); err != nil {
		return nil, err
	}

	// 未来的日期不能整理：这一天还没到，任何标签都是替用户提前下结论。
	if day > s.clock.LocalDate(s.clock.Now()) {
		return nil, fmt.Errorf("%w: 不能整理还没有到来的日期", ErrInvalidCalendarParam)
	}

	materials, err := s.dayMaterials(ctx, day)
	if err != nil {
		return nil, err
	}
	if len(materials.msgs) == 0 {
		return &model.DayState{Day: day, Status: model.DayStatusNoChat}, nil
	}
	if len(materials.clues) == 0 {
		return &model.DayState{Day: day, Status: model.DayStatusNoClue}, nil
	}

	tz := s.timezone()
	digest := dayDigest(tz, day, materials)

	owner := newExecOwner()
	claimed, err := s.repo.ClaimDayState(ctx, day, owner, digest, int(daySummaryLease.Seconds()))
	if err != nil {
		return nil, fmt.Errorf("认领当天整理失败: %w", err)
	}
	if !claimed {
		// 已经有人手动设过标签、已经有结果、或同一份输入正在算：如实返回当前状态。
		return s.dayState(ctx, day)
	}

	input, truncated := buildDaySummaryInput(day, tz, materials)
	result, err := s.llm.DaySummary(ctx, input)
	if err != nil {
		s.failDay(ctx, day, owner, "整理失败："+err.Error())
		return s.dayState(ctx, day)
	}

	label := truncateRunes(strings.TrimSpace(result.Label), maxDayLabelRunes)
	summary := truncateRunes(strings.TrimSpace(result.Summary), maxDaySummaryRunes)
	if label == "" {
		s.failDay(ctx, day, owner, "整理失败：模型没有给出标签")
		return s.dayState(ctx, day)
	}

	if err := s.repo.ApplyDayState(ctx, day, owner, digest, tz, label, summary, truncated); err != nil {
		if errors.Is(err, repository.ErrDayStateLeaseLost) {
			// 算的过程中输入变了、或者用户自己设了标签，这次结果不再作数。
			return s.dayState(ctx, day)
		}
		return nil, fmt.Errorf("写回当天状态失败: %w", err)
	}
	return s.dayState(ctx, day)
}

// 手动标签优先，不会被迟到的生成结果盖掉；只改显示，不动原始数据。
func (s *Service) SetDayLabel(ctx context.Context, day, label string) (*model.DayState, error) {
	if err := checkDay(day); err != nil {
		return nil, err
	}
	label = strings.TrimSpace(label)
	if label == "" {
		return nil, fmt.Errorf("%w: 标签不能为空", ErrInvalidCalendarParam)
	}
	if utf8.RuneCountInString(label) > maxDayLabelRunes {
		return nil, fmt.Errorf("%w: 标签最多 %d 个字", ErrInvalidCalendarParam, maxDayLabelRunes)
	}

	// 没有可见聊天的一天本来就没有状态，挂标签会让人以为那天聊过，直接拒绝。
	materials, err := s.dayMaterials(ctx, day)
	if err != nil {
		return nil, err
	}
	if len(materials.msgs) == 0 {
		return nil, fmt.Errorf("%w: 这一天没有聊天记录，不能设置状态", ErrInvalidCalendarParam)
	}

	if err := s.repo.SetManualDayLabel(ctx, day, label); err != nil {
		return nil, fmt.Errorf("保存标签失败: %w", err)
	}
	return s.dayState(ctx, day)
}

// ClearDayLabel 取消手动设置，回到自动整理；输入没变时旧结果立刻可用，不必重新调用模型。
func (s *Service) ClearDayLabel(ctx context.Context, day string) (*model.DayState, error) {
	if err := checkDay(day); err != nil {
		return nil, err
	}
	if err := s.repo.ClearManualDayLabel(ctx, day); err != nil {
		return nil, fmt.Errorf("取消标签失败: %w", err)
	}
	return s.dayState(ctx, day)
}

type dayMaterials struct {
	msgs  []model.ChatMessage
	clues []model.LifeRecord
}

func (s *Service) dayMaterials(ctx context.Context, day string) (*dayMaterials, error) {
	t, err := time.Parse(time.DateOnly, day)
	if err != nil {
		return nil, fmt.Errorf("%w: 日期必须是 YYYY-MM-DD", ErrInvalidCalendarParam)
	}
	from := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, s.clock.Location())
	to := from.AddDate(0, 0, 1)

	msgs, err := s.repo.ListUserMessagesBetween(ctx, from, to)
	if err != nil {
		return nil, fmt.Errorf("读取聊天记录失败: %w", err)
	}
	clues, err := s.repo.ListStateCluesBetween(ctx, day, day)
	if err != nil {
		return nil, fmt.Errorf("读取状态线索失败: %w", err)
	}

	m := &dayMaterials{msgs: msgs}
	for _, rec := range clues {
		if clueDay(rec) == day {
			m.clues = append(m.clues, rec)
		}
	}
	if len(m.clues) > maxDayClues {
		m.clues = m.clues[:maxDayClues]
	}
	return m, nil
}

func (s *Service) dayState(ctx context.Context, day string) (*model.DayState, error) {
	materials, err := s.dayMaterials(ctx, day)
	if err != nil {
		return nil, err
	}
	row, err := s.repo.GetDayState(ctx, day, int(daySummaryLease.Seconds()))
	if err != nil {
		return nil, fmt.Errorf("读取状态日历失败: %w", err)
	}
	st := buildDayState(day, s.timezone(), materials.msgs, materials.clues, row)
	return &st, nil
}

// 脱离请求取消后落库失败状态，否则这一天会一直停在「正在整理」。
func (s *Service) failDay(ctx context.Context, day, owner, message string) {
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), writeTimeout)
	defer cancel()
	if err := s.repo.FailDayState(writeCtx, day, owner, message); err != nil {
		log.Printf("记录 %s 的整理失败状态时出错: %v", day, err)
	}
}

func (s *Service) timezone() string {
	return s.clock.Location().String()
}

// 优先级：手动设置 > 自动结果 > 正在整理 > 缺少线索 > 没有聊天；过期结果照显并标 needs_summary。
func buildDayState(day, tz string, msgs []model.ChatMessage, clues []model.LifeRecord, row *repository.DayStateRow) model.DayState {
	st := model.DayState{Day: day, ClueCount: len(clues)}

	if row != nil && row.ManualLabel != "" {
		st.Status = model.DayStatusReady
		st.Source = "manual"
		st.Label = row.ManualLabel
		st.Summary = row.Summary
		st.Truncated = row.InputTruncated
		return st
	}

	if len(msgs) == 0 {
		st.Status = model.DayStatusNoChat
		return st
	}
	if len(clues) == 0 {
		st.Status = model.DayStatusNoClue
		return st
	}

	digest := dayDigest(tz, day, &dayMaterials{msgs: msgs, clues: clues})
	hasResult := row != nil && row.ResultDigest != ""
	stale := hasResult && row.ResultDigest != digest

	switch {
	case hasResult:
		st.Status = model.DayStatusReady
		st.Source = "auto"
		st.Label = row.Label
		st.Summary = row.Summary
		st.Truncated = row.InputTruncated
		st.NeedsSummary = stale

	case row != nil && row.AttemptStatus == "running" && row.AttemptLive && row.AttemptDigest == digest:
		st.Status = model.DayStatusPending

	case row != nil && row.AttemptStatus == "failed" && row.AttemptDigest == digest:
		// 失败不自动重试，避免模型持续不可用时一直烧调用；界面上给出原因和重试入口。
		st.Status = model.DayStatusFailed
		st.ErrorMessage = row.ErrorMessage

	default:
		st.Status = model.DayStatusPending
		st.NeedsSummary = true
	}
	return st
}

// OccurredOn 是事情发生日、MentionedOn 是说出口日：情绪有发生日算那天，日期未知且原话无时间说法才算提及日；活动仅已完成/明确否认算当天事实。
func clueDay(rec model.LifeRecord) string {
	switch rec.Kind {
	case model.LifeKindMood:
		if rec.OccurredOn != "" {
			return rec.OccurredOn
		}
		if rec.DatePrecision == model.LifeDateUnknown && strings.TrimSpace(rec.DateText) == "" {
			return rec.MentionedOn
		}
	case model.LifeKindActivity:
		if rec.State == model.LifeStateDone || rec.State == model.LifeStateNegated {
			return rec.OccurredOn
		}
	}
	return ""
}

func groupMessagesByDay(msgs []model.ChatMessage, loc *time.Location) map[string][]model.ChatMessage {
	out := make(map[string][]model.ChatMessage)
	for _, m := range msgs {
		day := m.CreatedAt.In(loc).Format(time.DateOnly)
		out[day] = append(out[day], m)
	}
	return out
}

func groupCluesByDay(recs []model.LifeRecord, from, to string) map[string][]model.LifeRecord {
	out := make(map[string][]model.LifeRecord)
	for _, rec := range recs {
		day := clueDay(rec)
		if day == "" || day < from || day > to {
			continue
		}
		out[day] = append(out[day], rec)
	}
	return out
}

// 结果输入指纹，含业务时区；线索的状态与内容变化都会让旧结果过期。
func dayDigest(tz, day string, m *dayMaterials) string {
	h := sha256.New()
	fmt.Fprintf(h, "tz=%s\nday=%s\n", tz, day)
	for _, msg := range m.msgs {
		fmt.Fprintf(h, "msg:%d\n", msg.ID)
	}
	for _, c := range m.clues {
		fmt.Fprintf(h, "clue:%d|%s|%s|%s\n", c.ID, c.Status, clueDay(c), c.Content)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func buildDaySummaryInput(day, tz string, m *dayMaterials) (string, bool) {
	var b strings.Builder
	var truncated bool

	fmt.Fprintf(&b, "日期：%s（业务时区 %s）\n\n", day, tz)

	fmt.Fprintf(&b, "当天的聊天原文：\n")
	msgs := m.msgs
	if len(msgs) > maxDayMessages {
		msgs = msgs[:maxDayMessages]
		truncated = true
	}
	for _, msg := range msgs {
		content := strings.TrimSpace(msg.Content)
		if utf8.RuneCountInString(content) > maxDayMessageRunes {
			content = truncateRunes(content, maxDayMessageRunes) + "…（已截断）"
			truncated = true
		}
		fmt.Fprintf(&b, "chat:%d | %s\n", msg.ID, strings.ReplaceAll(content, "\n", " "))
	}

	fmt.Fprintf(&b, "\n按规则筛出的有效状态线索（只包含发生在这一天、或明确在说当下感受的内容）：\n")
	if len(m.clues) == 0 {
		b.WriteString("（无）\n")
	}
	for _, c := range m.clues {
		fmt.Fprintf(&b, "life:%d | [%s/%s/%s] %s", c.ID, c.Kind, c.State, c.Basis, c.Content)
		if c.OccurredOn != "" {
			fmt.Fprintf(&b, "（发生在 %s）", c.OccurredOn)
		}
		if c.EvidenceQuote != "" {
			fmt.Fprintf(&b, " 原话：%q", c.EvidenceQuote)
		}
		b.WriteString("\n")
	}

	out := b.String()
	if utf8.RuneCountInString(out) > maxDaySummaryInputRunes {
		out = truncateRunes(out, maxDaySummaryInputRunes) + "\n…（输入已截断）"
		truncated = true
	}
	return out, truncated
}

func parseMonth(month string) (int, time.Month, error) {
	t, err := time.Parse("2006-01", strings.TrimSpace(month))
	if err != nil {
		return 0, 0, fmt.Errorf("%w: month 必须是 YYYY-MM", ErrInvalidCalendarParam)
	}
	return t.Year(), t.Month(), nil
}

func checkDay(day string) error {
	if _, err := time.Parse(time.DateOnly, strings.TrimSpace(day)); err != nil {
		return fmt.Errorf("%w: day 必须是 YYYY-MM-DD", ErrInvalidCalendarParam)
	}
	return nil
}
