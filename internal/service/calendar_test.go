package service

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/Xoyeir/Memora/internal/llm"
	"github.com/Xoyeir/Memora/internal/model"
	"github.com/Xoyeir/Memora/internal/testsupport"
)

func newServiceWith(t *testing.T, client LLMClient, tz string) (*Service, *sql.DB) {
	t.Helper()
	db, repo := testsupport.Open(t)
	clock, err := NewClock(tz)
	if err != nil {
		t.Fatalf("加载时区失败: %v", err)
	}
	return NewService(repo, client, clock), db
}

// createdAt 按存储时区（Asia/Shanghai）墙上时间写。
func seedTurn(t *testing.T, db *sql.DB, content, createdAt string) int64 {
	t.Helper()
	res, err := db.Exec(`INSERT INTO chat_messages (role, content, status, formation_status, created_at)
		VALUES ('user', ?, 'ok', 'applied', ?)`, content, createdAt)
	if err != nil {
		t.Fatalf("写入用户消息失败: %v", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO chat_messages (role, content, status, reply_to_id, created_at)
		VALUES ('assistant', '嗯。', 'ok', ?, ?)`, id, createdAt); err != nil {
		t.Fatalf("写入助手回复失败: %v", err)
	}
	return id
}

type clueSeed struct {
	kind      string
	state     string
	subject   string
	status    string
	content   string
	mentioned string
	occurred  string
	dateText  string
}

func seedClue(t *testing.T, db *sql.DB, chatID int64, s clueSeed) int64 {
	t.Helper()
	if s.subject == "" {
		s.subject = model.LifeSubjectUser
	}
	if s.status == "" {
		s.status = model.LifeStatusActive
	}

	precision := model.LifeDateUnknown
	var occurred any
	if s.occurred != "" {
		precision = model.LifeDateDay
		occurred = s.occurred
	}

	res, err := db.Exec(`
		INSERT INTO life_records
			(kind, state, basis, subject, content, chat_message_id, evidence_quote,
			 mentioned_on, occurred_on, date_precision, date_text, status)
		VALUES (?, ?, 'explicit', ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		s.kind, s.state, s.subject, s.content, chatID, s.content,
		s.mentioned, occurred, precision, nullText(s.dateText), s.status)
	if err != nil {
		t.Fatalf("写入生活记录失败: %v", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func nullText(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func monthView(t *testing.T, svc *Service, month string) *MonthView {
	t.Helper()
	view, err := svc.CalendarMonth(context.Background(), month)
	if err != nil {
		t.Fatalf("读取 %s 失败: %v", month, err)
	}
	return view
}

func dayOf(t *testing.T, view *MonthView, day string) model.DayState {
	t.Helper()
	for _, d := range view.Days {
		if d.Day == day {
			return d
		}
	}
	t.Fatalf("%s 不在 %s 的日历里", day, view.Month)
	return model.DayState{}
}

func TestCalendarMonthShape(t *testing.T) {
	svc, _, _ := newTestService(t)

	cases := []struct {
		month string
		days  int
	}{
		{"2026-10", 31},
		{"2026-02", 28},
		{"2028-02", 29},
		{"2025-12", 31},
		{"2026-01", 31},
		{"2026-04", 30},
	}
	for _, c := range cases {
		view := monthView(t, svc, c.month)
		if len(view.Days) != c.days {
			t.Errorf("%s 有 %d 天，期望 %d 天", c.month, len(view.Days), c.days)
		}
		if view.Month != c.month {
			t.Errorf("返回的月份是 %q，期望 %q", view.Month, c.month)
		}
		for i, d := range view.Days {
			want := c.month + "-" + pad2(i+1)
			if d.Day != want {
				t.Errorf("%s 第 %d 天是 %q，期望 %q", c.month, i+1, d.Day, want)
			}
			if d.Status != model.DayStatusNoChat {
				t.Errorf("%s 没有聊天，状态却是 %q", d.Day, d.Status)
			}
		}
	}

	if _, err := svc.CalendarMonth(context.Background(), "2026-13"); !errors.Is(err, ErrInvalidCalendarParam) {
		t.Errorf("非法月份的错误是 %v，期望 ErrInvalidCalendarParam", err)
	}
}

func TestCalendarMonthDefaultsToCurrentMonth(t *testing.T) {
	svc, _, _ := newTestService(t)

	view, err := svc.CalendarMonth(context.Background(), "")
	if err != nil {
		t.Fatalf("不带月份时失败: %v", err)
	}

	now := svc.clock.Now()
	if want := now.Format("2006-01"); view.Month != want {
		t.Errorf("默认月份是 %q，期望 %q", view.Month, want)
	}
	if want := svc.clock.LocalDate(now); view.Today != want {
		t.Errorf("today 是 %q，期望 %q", view.Today, want)
	}
	if len(view.Days) == 0 {
		t.Error("默认月份没有返回任何日期")
	}
	if view.Month != view.Days[0].Day[:7] {
		t.Errorf("返回的日期不属于 %q：%q", view.Month, view.Days[0].Day)
	}
}

func pad2(n int) string {
	if n < 10 {
		return "0" + string(rune('0'+n))
	}
	return string(rune('0'+n/10)) + string(rune('0'+n%10))
}

// 日期归属按业务时区算：同一轮对话跨午夜会落到不同的两天。
func TestCalendarDayBoundary(t *testing.T) {
	svc, _, db := newTestService(t)

	seedTurn(t, db, "睡前随便说两句", "2026-10-05 23:30:00")
	seedTurn(t, db, "刚醒", "2026-10-06 00:30:00")

	view := monthView(t, svc, "2026-10")
	if got := dayOf(t, view, "2026-10-05"); got.Status != model.DayStatusNoClue {
		t.Errorf("10-05 的状态是 %q，期望 %q（那天确实说过话）", got.Status, model.DayStatusNoClue)
	}
	if got := dayOf(t, view, "2026-10-06"); got.Status != model.DayStatusNoClue {
		t.Errorf("10-06 的状态是 %q，期望 %q", got.Status, model.DayStatusNoClue)
	}
	if got := dayOf(t, view, "2026-10-07"); got.Status != model.DayStatusNoChat {
		t.Errorf("10-07 没有聊天，状态却是 %q", got.Status)
	}
}

func TestCalendarTimezoneChangesDayAttribution(t *testing.T) {
	fake := &fakeLLM{}
	svc, db := newServiceWith(t, fake, "Asia/Tokyo")

	// 上海时间 10-05 23:30，在东京已经是 10-06 00:30。
	seedTurn(t, db, "深夜的一句", "2026-10-05 23:30:00")

	view := monthView(t, svc, "2026-10")
	if view.Timezone != "Asia/Tokyo" {
		t.Errorf("返回的业务时区是 %q", view.Timezone)
	}
	if got := dayOf(t, view, "2026-10-05"); got.Status != model.DayStatusNoChat {
		t.Errorf("按东京时间 10-05 应该没有聊天，状态是 %q", got.Status)
	}
	if got := dayOf(t, view, "2026-10-06"); got.Status != model.DayStatusNoClue {
		t.Errorf("按东京时间这条消息属于 10-06，状态是 %q", got.Status)
	}
}

// 线索归属：带明确时间说法的记录不算说话当天，计划、别人的事、已纠正的记录都不参与。
func TestCalendarClueAttribution(t *testing.T) {
	svc, _, db := newTestService(t)

	day := seedTurn(t, db, "今天累死了，昨天也很焦虑，明天还要去爬山", "2026-10-05 12:00:00")
	seedClue(t, db, day, clueSeed{
		kind: model.LifeKindMood, state: model.LifeStateNone,
		content: "用户感到疲惫", mentioned: "2026-10-05", occurred: "2026-10-05",
	})
	seedClue(t, db, day, clueSeed{
		kind: model.LifeKindMood, state: model.LifeStateNone,
		content: "用户昨天很焦虑", mentioned: "2026-10-05", occurred: "2026-10-04",
	})
	seedClue(t, db, day, clueSeed{
		kind: model.LifeKindActivity, state: model.LifeStatePlanned,
		content: "用户打算去爬山", mentioned: "2026-10-05", occurred: "2026-10-06",
	})
	seedClue(t, db, day, clueSeed{
		kind: model.LifeKindMood, state: model.LifeStateNone, subject: model.LifeSubjectOther,
		content: "朋友很焦虑", mentioned: "2026-10-05", occurred: "2026-10-05",
	})
	seedClue(t, db, day, clueSeed{
		kind: model.LifeKindActivity, state: model.LifeStateDone, status: model.LifeStatusCorrected,
		content: "用户跑了五公里", mentioned: "2026-10-05", occurred: "2026-10-05",
	})

	view := monthView(t, svc, "2026-10")
	if got := dayOf(t, view, "2026-10-05"); got.ClueCount != 1 {
		t.Errorf("10-05 的有效线索有 %d 条，期望只有「今天很累」这 1 条", got.ClueCount)
	}
	// 「明天要去爬山」是计划，不算 10-06 已发生的事实。
	if got := dayOf(t, view, "2026-10-06"); got.Status != model.DayStatusNoChat {
		t.Errorf("10-06 是计划里的日期，不该有状态，实际 %q", got.Status)
	}
}

// 时间未明确但明显在说当下感受的，算说出口那天。
func TestCalendarPresentTenseMoodWithoutDate(t *testing.T) {
	svc, _, db := newTestService(t)

	day := seedTurn(t, db, "烦死了", "2026-10-05 12:00:00")
	seedClue(t, db, day, clueSeed{
		kind: model.LifeKindMood, state: model.LifeStateNone,
		content: "用户感到烦躁", mentioned: "2026-10-05",
	})

	view := monthView(t, svc, "2026-10")
	if got := dayOf(t, view, "2026-10-05"); got.ClueCount != 1 {
		t.Errorf("说出当下的感受应该算作那天的线索，实际 %d 条", got.ClueCount)
	}
}

func TestCalendarChatWithoutClue(t *testing.T) {
	svc, _, db := newTestService(t)

	day := seedTurn(t, db, "我想学 Rust", "2026-10-05 12:00:00")
	seedClue(t, db, day, clueSeed{
		kind: model.LifeKindGoal, state: model.LifeStateWish,
		content: "用户想学 Rust", mentioned: "2026-10-05",
	})

	view := monthView(t, svc, "2026-10")
	got := dayOf(t, view, "2026-10-05")
	if got.Status != model.DayStatusNoClue {
		t.Errorf("只有目标的那天状态是 %q，期望 %q", got.Status, model.DayStatusNoClue)
	}
	if got.NeedsSummary {
		t.Error("线索不足的日期不应该被排进整理清单")
	}
	if len(view.PendingDays) != 0 {
		t.Errorf("不该有需要整理的日期，实际 %v", view.PendingDays)
	}
}

// 读月份不调用模型，只有需要整理的日期由页面触发：首次 1 次、输入未变 0 次、输入变了 1 次。
func TestDaySummaryModelCallCounts(t *testing.T) {
	svc, fake, db := newTestService(t)
	fake.daySummaryResult = &llm.DaySummaryResult{Label: "疲惫", Summary: "这天说了累。"}

	chatID := seedTurn(t, db, "今天累死了", "2026-10-05 12:00:00")
	clueID := seedClue(t, db, chatID, clueSeed{
		kind: model.LifeKindMood, state: model.LifeStateNone,
		content: "用户感到疲惫", mentioned: "2026-10-05", occurred: "2026-10-05",
	})

	view := monthView(t, svc, "2026-10")
	if n := fake.daySummaryCount(); n != 0 {
		t.Fatalf("读取月份调用了 %d 次模型，期望 0 次", n)
	}
	if len(view.PendingDays) != 1 || view.PendingDays[0] != "2026-10-05" {
		t.Fatalf("待整理清单是 %v", view.PendingDays)
	}

	first, err := svc.SummarizeDay(context.Background(), "2026-10-05")
	if err != nil {
		t.Fatalf("首次整理失败: %v", err)
	}
	if fake.daySummaryCount() != 1 {
		t.Fatalf("首次整理后调用了 %d 次模型，期望 1 次", fake.daySummaryCount())
	}
	if first.Status != model.DayStatusReady || first.Label != "疲惫" || first.Source != "auto" {
		t.Fatalf("首次整理的结果不对: %+v", first)
	}

	// 输入没变，重复整理直接复用。
	again, err := svc.SummarizeDay(context.Background(), "2026-10-05")
	if err != nil {
		t.Fatalf("重复整理失败: %v", err)
	}
	if fake.daySummaryCount() != 1 {
		t.Fatalf("重复访问调用了 %d 次模型，期望仍是 1 次", fake.daySummaryCount())
	}
	if again.Label != "疲惫" {
		t.Fatalf("重复访问的结果变成了 %+v", again)
	}
	if v := monthView(t, svc, "2026-10"); len(v.PendingDays) != 0 {
		t.Fatalf("已经整理过的月份仍有待整理日期: %v", v.PendingDays)
	}

	// 输入变了必须重新整理，缓存不能只靠「条数没变」判断。
	if _, err := db.Exec("UPDATE life_records SET content = ? WHERE id = ?", "用户感到非常疲惫", clueID); err != nil {
		t.Fatal(err)
	}
	fake.daySummaryResult = &llm.DaySummaryResult{Label: "疲惫", Summary: "这天说了两遍累。"}
	if v := monthView(t, svc, "2026-10"); len(v.PendingDays) != 1 {
		t.Fatalf("线索内容变了之后没有排进整理清单: %v", v.PendingDays)
	}
	third, err := svc.SummarizeDay(context.Background(), "2026-10-05")
	if err != nil {
		t.Fatalf("输入变化后整理失败: %v", err)
	}
	if fake.daySummaryCount() != 2 {
		t.Fatalf("输入变化后调用了 %d 次模型，期望 2 次", fake.daySummaryCount())
	}
	if third.Summary != "这天说了两遍累。" {
		t.Fatalf("输入变化后没有用新结果: %+v", third)
	}
}

func TestDaySummarySkipsModelWhenNoClue(t *testing.T) {
	svc, fake, db := newTestService(t)

	if _, err := svc.SummarizeDay(context.Background(), "2026-10-05"); err != nil {
		t.Fatalf("整理空日期失败: %v", err)
	}
	seedTurn(t, db, "我想学 Rust", "2026-10-06 12:00:00")
	got, err := svc.SummarizeDay(context.Background(), "2026-10-06")
	if err != nil {
		t.Fatalf("整理无线索日期失败: %v", err)
	}

	if fake.daySummaryCount() != 0 {
		t.Fatalf("不该调用模型，实际调用了 %d 次", fake.daySummaryCount())
	}
	if got.Status != model.DayStatusNoClue {
		t.Errorf("状态是 %q，期望 %q", got.Status, model.DayStatusNoClue)
	}
}

// 整理失败不覆盖上一次的成功结果，也不会留下永远卡住的「正在整理」。
func TestDaySummaryFailureKeepsPreviousResult(t *testing.T) {
	svc, fake, db := newTestService(t)
	fake.daySummaryResult = &llm.DaySummaryResult{Label: "疲惫", Summary: "这天说了累。"}

	chatID := seedTurn(t, db, "今天累死了", "2026-10-05 12:00:00")
	seedClue(t, db, chatID, clueSeed{
		kind: model.LifeKindMood, state: model.LifeStateNone,
		content: "用户感到疲惫", mentioned: "2026-10-05", occurred: "2026-10-05",
	})
	if _, err := svc.SummarizeDay(context.Background(), "2026-10-05"); err != nil {
		t.Fatalf("首次整理失败: %v", err)
	}

	if _, err := db.Exec("UPDATE life_records SET content = '用户非常疲惫' WHERE chat_message_id = ?", chatID); err != nil {
		t.Fatal(err)
	}
	fake.daySummaryErr = errors.New("模型暂时不可用")

	got, err := svc.SummarizeDay(context.Background(), "2026-10-05")
	if err != nil {
		t.Fatalf("整理失败时不该向上抛错: %v", err)
	}
	if got.Label != "疲惫" || got.Summary != "这天说了累。" {
		t.Fatalf("失败覆盖了上一次的结果: %+v", got)
	}
	if got.Status != model.DayStatusReady || !got.NeedsSummary {
		t.Fatalf("失败之后的状态不对: %+v", got)
	}

	fake.daySummaryErr = nil
	fake.daySummaryResult = &llm.DaySummaryResult{Label: "很疲惫", Summary: "这天说了两次累。"}
	retried, err := svc.SummarizeDay(context.Background(), "2026-10-05")
	if err != nil {
		t.Fatalf("重试失败: %v", err)
	}
	if retried.Label != "很疲惫" {
		t.Fatalf("重试没有写回新结果: %+v", retried)
	}
}

// 从没有成功结果的那一天失败时，界面拿到的是「整理失败」和原因，而不是卡住。
func TestDaySummaryFailureWithoutPreviousResult(t *testing.T) {
	svc, fake, db := newTestService(t)

	chatID := seedTurn(t, db, "今天累死了", "2026-10-05 12:00:00")
	seedClue(t, db, chatID, clueSeed{
		kind: model.LifeKindMood, state: model.LifeStateNone,
		content: "用户感到疲惫", mentioned: "2026-10-05", occurred: "2026-10-05",
	})
	fake.daySummaryErr = errors.New("模型暂时不可用")

	got, err := svc.SummarizeDay(context.Background(), "2026-10-05")
	if err != nil {
		t.Fatalf("整理失败时不该向上抛错: %v", err)
	}
	if got.Status != model.DayStatusFailed || got.ErrorMessage == "" {
		t.Fatalf("失败状态不对: %+v", got)
	}
	if got.NeedsSummary {
		t.Error("失败不应该被排进自动整理清单，否则会一直重试")
	}

	// 失败之后必须还能重新认领，不能卡在「正在整理」。
	if n := countRows(t, db, "SELECT COUNT(*) FROM day_states WHERE attempt_status = 'running'"); n != 0 {
		t.Fatalf("有 %d 条记录卡在 running", n)
	}
	fake.daySummaryErr = nil
	fake.daySummaryResult = &llm.DaySummaryResult{Label: "疲惫", Summary: "这天说了累。"}
	ok, err := svc.SummarizeDay(context.Background(), "2026-10-05")
	if err != nil {
		t.Fatalf("重试失败: %v", err)
	}
	if ok.Status != model.DayStatusReady {
		t.Fatalf("重试之后的状态是 %q", ok.Status)
	}
}

// 手动设置的标签优先，也不会被正在路上的自动结果盖掉。
func TestManualLabelWinsOverLateGeneration(t *testing.T) {
	fake := &fakeLLM{}
	gated := &gatedLLM{fakeLLM: fake, reached: make(chan struct{}), release: make(chan struct{})}
	svc, db := newServiceWith(t, gated, "Asia/Shanghai")
	fake.daySummaryResult = &llm.DaySummaryResult{Label: "疲惫", Summary: "这天说了累。"}

	chatID := seedTurn(t, db, "今天累死了", "2026-10-05 12:00:00")
	seedClue(t, db, chatID, clueSeed{
		kind: model.LifeKindMood, state: model.LifeStateNone,
		content: "用户感到疲惫", mentioned: "2026-10-05", occurred: "2026-10-05",
	})

	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := svc.SummarizeDay(context.Background(), "2026-10-05"); err != nil {
			t.Errorf("整理失败: %v", err)
		}
	}()

	<-gated.reached // 整理已经发出模型调用，还卡在路上

	manual, err := svc.SetDayLabel(context.Background(), "2026-10-05", "还行")
	if err != nil {
		t.Fatalf("设置标签失败: %v", err)
	}
	if manual.Source != "manual" || manual.Label != "还行" {
		t.Fatalf("手动标签没有生效: %+v", manual)
	}

	close(gated.release)
	<-done

	// 迟到的结果写不回去：认领已被手动设置作废。
	var resultDigest string
	if err := db.QueryRow("SELECT result_digest FROM day_states WHERE day = '2026-10-05'").Scan(&resultDigest); err != nil {
		t.Fatal(err)
	}
	if resultDigest != "" {
		t.Errorf("迟到的自动结果仍然写了进去（result_digest=%q）", resultDigest)
	}

	got, err := svc.SummarizeDay(context.Background(), "2026-10-05")
	if err != nil {
		t.Fatal(err)
	}
	if got.Source != "manual" || got.Label != "还行" {
		t.Fatalf("迟到的自动结果盖掉了手动标签: %+v", got)
	}
	if n := fake.daySummaryCount(); n != 1 {
		t.Errorf("手动设置之后又自动整理了一次，模型共调用 %d 次", n)
	}
}

func TestClearManualLabelRestoresAutoResult(t *testing.T) {
	svc, fake, db := newTestService(t)
	fake.daySummaryResult = &llm.DaySummaryResult{Label: "疲惫", Summary: "这天说了累。"}

	chatID := seedTurn(t, db, "今天累死了", "2026-10-05 12:00:00")
	seedClue(t, db, chatID, clueSeed{
		kind: model.LifeKindMood, state: model.LifeStateNone,
		content: "用户感到疲惫", mentioned: "2026-10-05", occurred: "2026-10-05",
	})
	if _, err := svc.SummarizeDay(context.Background(), "2026-10-05"); err != nil {
		t.Fatalf("首次整理失败: %v", err)
	}

	manual, err := svc.SetDayLabel(context.Background(), "2026-10-05", "还行")
	if err != nil {
		t.Fatalf("设置标签失败: %v", err)
	}
	if manual.Source != "manual" || manual.Summary != "这天说了累。" {
		t.Fatalf("手动设置之后应该仍然能看到自动摘要: %+v", manual)
	}

	before := fake.daySummaryCount()
	cleared, err := svc.ClearDayLabel(context.Background(), "2026-10-05")
	if err != nil {
		t.Fatalf("取消标签失败: %v", err)
	}
	if cleared.Source != "auto" || cleared.Label != "疲惫" {
		t.Fatalf("取消之后没有回到自动结果: %+v", cleared)
	}
	if fake.daySummaryCount() != before {
		t.Errorf("取消手动设置不该重新调用模型，多调用了 %d 次", fake.daySummaryCount()-before)
	}

	if _, err := svc.SetDayLabel(context.Background(), "2026-10-05", ""); !errors.Is(err, ErrInvalidCalendarParam) {
		t.Errorf("空标签返回 %v，期望 ErrInvalidCalendarParam", err)
	}
	if _, err := svc.SetDayLabel(context.Background(), "2026-10-05", strings.Repeat("长", maxDayLabelRunes+1)); !errors.Is(err, ErrInvalidCalendarParam) {
		t.Errorf("超长标签返回 %v，期望 ErrInvalidCalendarParam", err)
	}
}

// 标签表示「那天聊过之后的状态」，没有可见聊天的一天直接拒绝挂标签。
func TestManualLabelRejectsDayWithoutChat(t *testing.T) {
	svc, _, db := newTestService(t)

	// 10-06 整天没有可见聊天，只在别的日子留下了线索。
	chatID := seedTurn(t, db, "今天还行", "2026-10-07 12:00:00")
	seedClue(t, db, chatID, clueSeed{
		kind: model.LifeKindMood, state: model.LifeStateNone,
		content: "用户感觉还行", mentioned: "2026-10-07", occurred: "2026-10-07",
	})

	if _, err := svc.SetDayLabel(context.Background(), "2026-10-06", "轻松"); !errors.Is(err, ErrInvalidCalendarParam) {
		t.Fatalf("给没有聊天的日期设标签返回 %v，期望 ErrInvalidCalendarParam", err)
	}

	state, err := svc.dayState(context.Background(), "2026-10-06")
	if err != nil {
		t.Fatalf("读取当天状态失败: %v", err)
	}
	if state.Status != model.DayStatusNoChat || state.Source != "" || state.Label != "" {
		t.Errorf("被拒绝之后那天的状态变了: %+v", state)
	}

	if _, err := svc.SetDayLabel(context.Background(), "2026-10-07", "轻松"); err != nil {
		t.Errorf("有聊天的日期设置标签失败: %v", err)
	}
}

func TestCalendarRejectsFutureDays(t *testing.T) {
	svc, fake, _ := newTestService(t)

	future := svc.clock.Now().AddDate(0, 0, 1).Format("2006-01-02")
	if _, err := svc.SummarizeDay(context.Background(), future); !errors.Is(err, ErrInvalidCalendarParam) {
		t.Errorf("整理未来的日期返回 %v，期望 ErrInvalidCalendarParam", err)
	}
	if fake.daySummaryCount() != 0 {
		t.Error("整理未来的日期不该调用模型")
	}
	if _, err := svc.CalendarMonth(context.Background(), "2026-1-5"); !errors.Is(err, ErrInvalidCalendarParam) {
		t.Errorf("非法月份返回 %v", err)
	}
}

// gatedLLM 把 DaySummary 卡在模型调用中间，用来构造「结果迟到」的时序。
type gatedLLM struct {
	*fakeLLM
	once    sync.Once
	reached chan struct{}
	release chan struct{}
}

func (g *gatedLLM) DaySummary(ctx context.Context, input string) (*llm.DaySummaryResult, error) {
	g.once.Do(func() { close(g.reached) })
	<-g.release
	return g.fakeLLM.DaySummary(ctx, input)
}

// 输入被截断时必须标出来，不能让一份只看了半天聊天结果假装看完了全天。
func TestDaySummaryInputTruncation(t *testing.T) {
	svc, fake, db := newTestService(t)
	fake.daySummaryResult = &llm.DaySummaryResult{Label: "忙", Summary: "这天很忙。"}

	chatID := seedTurn(t, db, "太长的一条消息", "2026-10-05 12:00:00")
	seedClue(t, db, chatID, clueSeed{
		kind: model.LifeKindMood, state: model.LifeStateNone,
		content: "用户很忙", mentioned: "2026-10-05", occurred: "2026-10-05",
	})
	long := strings.Repeat("很长的一段话", 500)
	for i := 0; i < maxDayMessages+2; i++ {
		if _, err := db.Exec(`INSERT INTO chat_messages (role, content, status, formation_status, created_at)
			VALUES ('user', ?, 'ok', 'applied', '2026-10-05 13:00:00')`, long); err != nil {
			t.Fatal(err)
		}
	}

	got, err := svc.SummarizeDay(context.Background(), "2026-10-05")
	if err != nil {
		t.Fatalf("整理失败: %v", err)
	}
	if !got.Truncated {
		t.Error("输入被截断了，结果上却没有标出来")
	}
	if n := fake.daySummaryCount(); n != 1 {
		t.Fatalf("调用了 %d 次模型", n)
	}
	if !strings.Contains(fake.daySummaryInputs[0], "已截断") {
		t.Error("输入里没有说明发生了截断")
	}
}
