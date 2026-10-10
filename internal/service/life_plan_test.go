package service

import (
	"strings"
	"testing"
	"time"

	"github.com/Xoyeir/Memora/internal/llm"
	"github.com/Xoyeir/Memora/internal/model"
)

// 这些用例固定「消息说出来的那一天」，让相对日期的换算结果可断言。
const planMentionedOn = "2026-10-08"

// 用户在 2026-10-08 说的这句话。
const planMessage = "昨天跑了五公里，今天运动了，今天很焦虑，明天想运动，今天没运动，朋友今天去跑步了，" +
	"假如我今天跑步了，我想成为更自律的人，下个月准备参加比赛，我的生日是五月八日"

func lifeRec(kind, state, basis, subject, quote string) llm.LifeRecord {
	return llm.LifeRecord{
		Kind: kind, State: state, Basis: basis, Subject: subject,
		Modality: "actual", Content: "记录内容", EvidenceQuote: quote,
		When: llm.RecordWhen{Kind: "unknown"},
	}
}

func withWhen(r llm.LifeRecord, w llm.RecordWhen) llm.LifeRecord {
	r.When = w
	return r
}

func planOne(t *testing.T, r llm.LifeRecord, known map[int64]bool) []model.LifeRecordOp {
	t.Helper()
	return planLifeRecords([]llm.LifeRecord{r}, 42, planMessage, planMentionedOn, known)
}

// 固定样例：每条给出一句原话（含时间说法）与它应当被读成的类型、状态和日期。
func TestPlanLifeRecordsOnAcceptanceSamples(t *testing.T) {
	cases := []struct {
		name          string
		rec           llm.LifeRecord
		wantKind      string
		wantState     string
		wantDate      string // 期望的 occurred_on，空表示应为未知
		wantMonth     int
		wantDay       int
		wantSubj      string
		wantPrecision string
		wantText      string
	}{
		{
			name:      "今天运动了：已完成，日期是消息当天",
			rec:       withWhen(lifeRec("activity", "done", "explicit", "user", "今天运动了"), llm.RecordWhen{Kind: "relative", Text: "今天", OffsetDays: 0}),
			wantKind:  model.LifeKindActivity,
			wantState: model.LifeStateDone,
			wantDate:  planMentionedOn,
			wantSubj:  model.LifeSubjectUser,
			wantText:  "今天",
		},
		{
			name:      "明天想运动：计划不是完成",
			rec:       withWhen(lifeRec("activity", "planned", "explicit", "user", "明天想运动"), llm.RecordWhen{Kind: "relative", Text: "明天", OffsetDays: 1}),
			wantKind:  model.LifeKindActivity,
			wantState: model.LifeStatePlanned,
			wantDate:  "2026-10-09",
			wantText:  "明天",
		},
		{
			name:      "今天没运动：是否定，不是完成",
			rec:       withWhen(lifeRec("activity", "negated", "explicit", "user", "今天没运动"), llm.RecordWhen{Kind: "relative", Text: "今天", OffsetDays: 0}),
			wantKind:  model.LifeKindActivity,
			wantState: model.LifeStateNegated,
			wantDate:  planMentionedOn,
			wantText:  "今天",
		},
		{
			name:     "朋友今天去跑步了：主体是别人",
			rec:      lifeRec("activity", "done", "explicit", "other", "朋友今天去跑步了"),
			wantKind: model.LifeKindActivity,
			wantSubj: model.LifeSubjectOther,
		},
		{
			name:      "我想成为更自律的人：是愿望，不是已经做到",
			rec:       lifeRec("goal", "wish", "explicit", "user", "我想成为更自律的人"),
			wantState: model.LifeStateWish,
			wantKind:  model.LifeKindGoal,
		},
		{
			name:      "我的生日是五月八日：保留月日，年份留空",
			rec:       withWhen(lifeRec("date", "none", "explicit", "user", "我的生日是五月八日"), llm.RecordWhen{Kind: "month_day", Text: "五月八日", Month: 5, Day: 8}),
			wantKind:  model.LifeKindDate,
			wantState: model.LifeStateNone,
			wantMonth: 5,
			wantDay:   8,
			wantText:  "五月八日",
		},
		{
			name:      "我昨天跑步了：按原始消息时间往前一天",
			rec:       withWhen(lifeRec("activity", "done", "explicit", "user", "昨天跑了五公里"), llm.RecordWhen{Kind: "relative", Text: "昨天", OffsetDays: -1}),
			wantKind:  model.LifeKindActivity,
			wantDate:  "2026-10-07",
			wantState: model.LifeStateDone,
			wantText:  "昨天",
		},
		{
			name:      "早上焦虑，晚上轻松多了：两条线索各留一条，不合并",
			rec:       lifeRec("mood", "none", "explicit", "user", "今天很焦虑"),
			wantKind:  model.LifeKindMood,
			wantState: model.LifeStateNone,
		},
		{
			name:      "只讲了事件、情绪靠猜：可以标为推测",
			rec:       lifeRec("mood", "none", "inferred", "user", "今天很焦虑"),
			wantKind:  model.LifeKindMood,
			wantState: model.LifeStateNone,
		},
		{
			// 「松了口气」是在直接说感受，标 explicit 是对读法之一，不该被拒绝。
			name:      "松了口气是直接表达：explicit 同样接受",
			rec:       lifeRec("mood", "none", "explicit", "user", "今天很焦虑"),
			wantKind:  model.LifeKindMood,
			wantState: model.LifeStateNone,
		},
		{
			name:          "三年前超出换算范围：日期未知，但原话留下",
			rec:           withWhen(lifeRec("activity", "done", "explicit", "user", "昨天跑了五公里"), llm.RecordWhen{Kind: "relative", Text: "三年前", OffsetDays: -1095}),
			wantKind:      model.LifeKindActivity,
			wantDate:      "",
			wantPrecision: model.LifeDateUnknown,
			wantText:      "三年前",
		},
		{
			name:     "完整年月日：按原样采用",
			rec:      withWhen(lifeRec("activity", "done", "explicit", "user", "昨天跑了五公里"), llm.RecordWhen{Kind: "absolute", Text: "2025年3月15日", Date: "2025-03-15"}),
			wantKind: model.LifeKindActivity,
			wantDate: "2025-03-15",
			wantText: "2025年3月15日",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := planOne(t, tc.rec, nil)
			if len(got) != 1 {
				t.Fatalf("期望留下 1 条记录，实际 %d 条", len(got))
			}
			op := got[0]
			if tc.wantKind != "" && op.Kind != tc.wantKind {
				t.Fatalf("类型是 %q，期望 %q", op.Kind, tc.wantKind)
			}
			if tc.wantState != "" && op.State != tc.wantState {
				t.Fatalf("状态是 %q，期望 %q", op.State, tc.wantState)
			}
			if tc.wantSubj != "" && op.Subject != tc.wantSubj {
				t.Fatalf("主体是 %q，期望 %q", op.Subject, tc.wantSubj)
			}
			if tc.wantSubj == "" && op.Subject != model.LifeSubjectUser {
				t.Fatalf("主体是 %q，期望默认是用户本人", op.Subject)
			}
			if op.OccurredOn != tc.wantDate {
				t.Fatalf("发生日期是 %q，期望 %q", op.OccurredOn, tc.wantDate)
			}
			if op.OccurredMonth != tc.wantMonth || op.OccurredDay != tc.wantDay {
				t.Fatalf("月日是 (%d, %d)，期望 (%d, %d)",
					op.OccurredMonth, op.OccurredDay, tc.wantMonth, tc.wantDay)
			}
			if tc.wantPrecision != "" && op.DatePrecision != tc.wantPrecision {
				t.Fatalf("日期精度是 %q，期望 %q", op.DatePrecision, tc.wantPrecision)
			}
			if op.DateText != tc.wantText {
				t.Fatalf("原话里的时间说法是 %q，期望 %q", op.DateText, tc.wantText)
			}
			if op.MentionedOn != planMentionedOn {
				t.Fatalf("提及日期是 %q，期望 %q", op.MentionedOn, planMentionedOn)
			}
			if op.ChatMessageID != 42 {
				t.Fatalf("证据消息是 %d，期望本轮 42", op.ChatMessageID)
			}
		})
	}
}

// 拒绝发生在事务之前，不会以「写入失败」影响同批的其他记录。
func TestPlanLifeRecordsRejects(t *testing.T) {
	long := strings.Repeat("很", maxRecordContent+1)

	cases := []struct {
		name string
		rec  llm.LifeRecord
	}{
		{
			name: "假如我今天跑步了：假设不是事实",
			rec: func() llm.LifeRecord {
				r := lifeRec("activity", "done", "explicit", "user", "假如我今天跑步了")
				r.Modality = "hypothetical"
				return r
			}(),
		},
		{
			name: "把事实类记录标成推测",
			rec:  lifeRec("activity", "done", "inferred", "user", "今天没运动"),
		},
		{
			name: "类型与状态不配套：情绪不该有完成状态",
			rec:  lifeRec("mood", "done", "explicit", "user", "今天很焦虑"),
		},
		{
			name: "类型与状态不配套：愿望不可能已完成",
			rec:  lifeRec("goal", "done", "explicit", "user", "我想成为更自律的人"),
		},
		{
			name: "未知的类型",
			rec:  lifeRec("dream", "none", "explicit", "user", "今天很焦虑"),
		},
		{
			name: "未知的状态",
			rec:  lifeRec("activity", "maybe", "explicit", "user", "今天没运动"),
		},
		{
			name: "未知的依据来源",
			rec:  lifeRec("mood", "none", "guessed", "user", "今天很焦虑"),
		},
		{
			name: "未知的主体",
			rec:  lifeRec("activity", "done", "explicit", "somebody", "今天没运动"),
		},
		{
			name: "证据片段不在原文里",
			rec:  lifeRec("activity", "done", "explicit", "user", "用户跑了一场马拉松"),
		},
		{
			name: "没有证据片段",
			rec:  lifeRec("activity", "done", "explicit", "user", ""),
		},
		{
			name: "内容为空",
			rec: func() llm.LifeRecord {
				r := lifeRec("activity", "done", "explicit", "user", "今天没运动")
				r.Content = "   "
				return r
			}(),
		},
		{
			name: "内容超过列宽",
			rec: func() llm.LifeRecord {
				r := lifeRec("activity", "done", "explicit", "user", "今天没运动")
				r.Content = long
				return r
			}(),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := planOne(t, tc.rec, nil); len(got) != 0 {
				t.Fatalf("应当被拒绝，却留下了 %+v", got)
			}
		})
	}
}

// 一条不合法的候选被丢掉时，同一批里合法的那些必须全部保留。
func TestPlanLifeRecordsKeepsValidOnes(t *testing.T) {
	records := []llm.LifeRecord{
		lifeRec("activity", "done", "explicit", "user", "今天没运动"),
		lifeRec("mood", "done", "explicit", "user", "今天很焦虑"),     // 类型状态不配套
		lifeRec("activity", "done", "explicit", "user", "编造的证据"), // 证据不在原文
		lifeRec("mood", "none", "explicit", "user", "今天很焦虑"),
	}

	got := planLifeRecords(records, 42, planMessage, planMentionedOn, nil)
	if len(got) != 2 {
		t.Fatalf("期望留下 2 条，实际 %d 条: %+v", len(got), got)
	}
	if got[0].Kind != model.LifeKindActivity || got[1].Kind != model.LifeKindMood {
		t.Fatalf("留下的顺序或内容不对: %+v", got)
	}
}

// 纠正关联只认输入里给过的编号；给不出来就只留新记录，不去猜。
func TestPlanLifeRecordsCorrectionLink(t *testing.T) {
	known := map[int64]bool{31: true}

	linked := planOne(t, func() llm.LifeRecord {
		r := lifeRec("activity", "done", "explicit", "user", "今天没运动")
		r.Corrects = "life:31"
		return r
	}(), known)
	if len(linked) != 1 || linked[0].CorrectsID != 31 {
		t.Fatalf("应当建立对 31 的纠正关联: %+v", linked)
	}

	for _, ref := range []string{"life:99", "memory:31", "31", "life:0"} {
		t.Run("无法定位 "+ref, func(t *testing.T) {
			got := planOne(t, func() llm.LifeRecord {
				r := lifeRec("activity", "done", "explicit", "user", "今天没运动")
				r.Corrects = ref
				return r
			}(), known)
			if len(got) != 1 {
				t.Fatalf("纠正线索不确定时仍应保留新记录，实际 %d 条", len(got))
			}
			if got[0].CorrectsID != 0 {
				t.Fatalf("不该建立纠正关联，实际指向 %d", got[0].CorrectsID)
			}
		})
	}
}

func TestResolveRecordWhen(t *testing.T) {
	base, err := time.Parse(time.DateOnly, planMentionedOn)
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name      string
		when      llm.RecordWhen
		wantOn    string
		wantMonth int
		wantDay   int
		wantClock string
	}{
		{name: "今天", when: llm.RecordWhen{Kind: "relative", OffsetDays: 0}, wantOn: "2026-10-08", wantClock: model.LifeDateDay},
		{name: "昨天", when: llm.RecordWhen{Kind: "relative", OffsetDays: -1}, wantOn: "2026-10-07", wantClock: model.LifeDateDay},
		{name: "明天", when: llm.RecordWhen{Kind: "relative", OffsetDays: 1}, wantOn: "2026-10-09", wantClock: model.LifeDateDay},
		{name: "跨月", when: llm.RecordWhen{Kind: "relative", OffsetDays: -8}, wantOn: "2026-09-30", wantClock: model.LifeDateDay},
		{name: "偏移过大不当作相对日期", when: llm.RecordWhen{Kind: "relative", OffsetDays: 4000}, wantClock: model.LifeDateUnknown},
		{name: "完整日期", when: llm.RecordWhen{Kind: "absolute", Date: "2026-05-08"}, wantOn: "2026-05-08", wantClock: model.LifeDateDay},
		{name: "不是日期的字符串", when: llm.RecordWhen{Kind: "absolute", Date: "五月八日"}, wantClock: model.LifeDateUnknown},
		{name: "只有月日", when: llm.RecordWhen{Kind: "month_day", Month: 5, Day: 8}, wantMonth: 5, wantDay: 8, wantClock: model.LifeDateMonthDay},
		{name: "二月二十九合法", when: llm.RecordWhen{Kind: "month_day", Month: 2, Day: 29}, wantMonth: 2, wantDay: 29, wantClock: model.LifeDateMonthDay},
		{name: "四月三十一日不存在", when: llm.RecordWhen{Kind: "month_day", Month: 4, Day: 31}, wantClock: model.LifeDateUnknown},
		{name: "十三月", when: llm.RecordWhen{Kind: "month_day", Month: 13, Day: 1}, wantClock: model.LifeDateUnknown},
		{name: "未知", when: llm.RecordWhen{Kind: "unknown"}, wantClock: model.LifeDateUnknown},
		{name: "模型编的种类", when: llm.RecordWhen{Kind: "next_friday"}, wantClock: model.LifeDateUnknown},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			on, month, day, precision := resolveRecordWhen(tc.when, base)
			if on != tc.wantOn || month != tc.wantMonth || day != tc.wantDay || precision != tc.wantClock {
				t.Fatalf("得到 (%q, %d, %d, %q)，期望 (%q, %d, %d, %q)",
					on, month, day, precision, tc.wantOn, tc.wantMonth, tc.wantDay, tc.wantClock)
			}
		})
	}
}

// 基准日期不可用时，相对表达一律记为未知，而不是拿零值算出一个荒谬的日期。
func TestResolveRecordWhenWithoutUsableBase(t *testing.T) {
	on, _, _, precision := resolveRecordWhen(llm.RecordWhen{Kind: "relative", OffsetDays: -1}, time.Time{})
	if on != "" || precision != model.LifeDateUnknown {
		t.Fatalf("基准不可用时应当记为未知，实际 (%q, %q)", on, precision)
	}
}

func TestNormalizeLifeQuery(t *testing.T) {
	svc := func(q LifeRecordQuery) (err error) {
		_, err = normalizeLifeQuery(q)
		return err
	}

	if err := svc(LifeRecordQuery{OccurredFrom: "2026/10/01"}); err == nil {
		t.Fatal("非 YYYY-MM-DD 的日期应当被拒绝")
	}
	if err := svc(LifeRecordQuery{OccurredFrom: "2026-10-09", OccurredTo: "2026-10-01"}); err == nil {
		t.Fatal("结束早于开始应当被拒绝")
	}
	if err := svc(LifeRecordQuery{OccurredFrom: "2020-01-01", OccurredTo: "2026-10-01"}); err == nil {
		t.Fatal("跨度过大应当被拒绝")
	}
	if err := svc(LifeRecordQuery{Kinds: []string{"activity", "dream"}}); err == nil {
		t.Fatal("未知类型应当被拒绝")
	}
	if err := svc(LifeRecordQuery{Subject: "somebody"}); err == nil {
		t.Fatal("未知主体应当被拒绝")
	}

	// 默认只返回用户本人的、仍然成立的记录。
	f, err := normalizeLifeQuery(LifeRecordQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if f.Subject != model.LifeSubjectUser || f.Status != model.LifeStatusActive {
		t.Fatalf("默认筛选是 %+v", f)
	}
	if f.Limit != defaultLifeRecordLimit {
		t.Fatalf("默认条数是 %d", f.Limit)
	}

	// 条数有上限，超出的部分被截到上限而不是报错。
	f, err = normalizeLifeQuery(LifeRecordQuery{Limit: 100000})
	if err != nil {
		t.Fatal(err)
	}
	if f.Limit != maxLifeRecordLimit {
		t.Fatalf("超限条数被截成 %d，期望 %d", f.Limit, maxLifeRecordLimit)
	}

	// subject=all 用来显式放开主体筛选。
	f, err = normalizeLifeQuery(LifeRecordQuery{Subject: "all"})
	if err != nil {
		t.Fatal(err)
	}
	if f.Subject != "" {
		t.Fatalf("subject=all 应当不加主体条件，实际是 %q", f.Subject)
	}
}

func TestParseLimitParam(t *testing.T) {
	if n, err := ParseLimitParam(""); err != nil || n != 0 {
		t.Fatalf("空串应当回落到默认值，得到 (%d, %v)", n, err)
	}
	if n, err := ParseLimitParam(" 20 "); err != nil || n != 20 {
		t.Fatalf("得到 (%d, %v)", n, err)
	}
	if _, err := ParseLimitParam("abc"); err == nil {
		t.Fatal("非整数应当被拒绝")
	}
}

func TestSplitKinds(t *testing.T) {
	got := SplitKinds(" activity , mood ,,")
	if len(got) != 2 || got[0] != "activity" || got[1] != "mood" {
		t.Fatalf("解析结果是 %#v", got)
	}
	if got := SplitKinds(""); len(got) != 0 {
		t.Fatalf("空串应当是空切片，实际 %#v", got)
	}
}
