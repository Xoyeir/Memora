package service

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Xoyeir/Memora/internal/llm"
	"github.com/Xoyeir/Memora/internal/model"
	"github.com/Xoyeir/Memora/internal/repository"
)

func activityRec(quote, content string, offsetDays int) llm.LifeRecord {
	return llm.LifeRecord{
		Kind: "activity", State: "done", Basis: "explicit", Subject: "user",
		Modality: "actual", Content: content, EvidenceQuote: quote,
		When: llm.RecordWhen{Kind: "relative", OffsetDays: offsetDays},
	}
}

const lifeSelect = `SELECT id, kind, state, basis, subject, content, chat_message_id, evidence_quote,
		mentioned_on, occurred_on, occurred_month, occurred_day, date_precision, date_text,
		status, corrected_by_id FROM life_records `

type rowScanner interface{ Scan(dest ...any) error }

func scanLife(t *testing.T, row rowScanner) model.LifeRecord {
	t.Helper()

	var (
		rec                     model.LifeRecord
		mentionedOn, occurredOn sql.NullTime
		month, day              sql.NullInt64
		dateText                sql.NullString
		correctedBy             sql.NullInt64
	)
	err := row.Scan(&rec.ID, &rec.Kind, &rec.State, &rec.Basis, &rec.Subject, &rec.Content,
		&rec.ChatMessageID, &rec.EvidenceQuote, &mentionedOn, &occurredOn, &month, &day,
		&rec.DatePrecision, &dateText, &rec.Status, &correctedBy)
	if err != nil {
		t.Fatalf("读取生活记录失败: %v", err)
	}
	if mentionedOn.Valid {
		rec.MentionedOn = mentionedOn.Time.Format(time.DateOnly)
	}
	if occurredOn.Valid {
		rec.OccurredOn = occurredOn.Time.Format(time.DateOnly)
	}
	rec.OccurredMonth = int(month.Int64)
	rec.OccurredDay = int(day.Int64)
	rec.DateText = dateText.String
	rec.CorrectedByID = correctedBy.Int64
	return rec
}

func loadLifeRecord(t *testing.T, db *sql.DB) model.LifeRecord {
	t.Helper()
	if n := countRows(t, db, "SELECT COUNT(*) FROM life_records"); n != 1 {
		t.Fatalf("生活记录有 %d 条，期望 1 条", n)
	}
	return scanLife(t, db.QueryRow(lifeSelect+"ORDER BY id DESC LIMIT 1"))
}

// 一轮正常聊天之后，生活记录连同长期记忆一起落库，来源指向本轮那条用户消息。
func TestLifeRecordsAreWrittenFromChat(t *testing.T) {
	svc, fake, db := newTestService(t)

	const msg = "今天跑步了，有点焦虑"
	fake.agentReplies = []agentReply{{content: ""}}
	fake.streamText = "嗯，我记下了。"
	fake.formationResult = &llm.FormationResult{
		LifeRecords: []llm.LifeRecord{
			activityRec("今天跑步了", "今天跑步了", 0),
			{
				Kind: "mood", State: "none", Basis: "explicit", Subject: "user",
				Modality: "actual", Content: "有点焦虑", EvidenceQuote: "有点焦虑",
				When: llm.RecordWhen{Kind: "relative", OffsetDays: 0},
			},
		},
	}

	if err := svc.ChatStream(context.Background(), "c-life", msg, noopRecall, noopDelta); err != nil {
		t.Fatalf("ChatStream 失败: %v", err)
	}

	userID, status, formation := turnRow(t, db, "c-life")
	if status != model.TurnStatusOK || formation != model.FormationApplied {
		t.Fatalf("轮次状态 (%q, %q)，期望 (ok, applied)", status, formation)
	}
	if n := countRows(t, db, "SELECT COUNT(*) FROM life_records"); n != 2 {
		t.Fatalf("生活记录有 %d 条，期望 2 条", n)
	}

	// 来源必须是本轮用户消息，而不是模型自选的编号。
	if n := countRows(t, db, "SELECT COUNT(*) FROM life_records WHERE chat_message_id = ?", userID); n != 2 {
		t.Fatalf("只有 %d 条记录的来源指向本轮用户消息", n)
	}

	var createdAt time.Time
	if err := db.QueryRow("SELECT created_at FROM chat_messages WHERE id = ?", userID).Scan(&createdAt); err != nil {
		t.Fatal(err)
	}
	wantDay := svc.clock.LocalDate(createdAt)

	rec := loadNewestOfKind(t, db, model.LifeKindActivity)
	if rec.OccurredOn != wantDay {
		t.Fatalf("活动发生在 %q，期望消息当天 %q", rec.OccurredOn, wantDay)
	}
	if rec.MentionedOn != wantDay {
		t.Fatalf("提及日期是 %q，期望 %q", rec.MentionedOn, wantDay)
	}
	if rec.DatePrecision != model.LifeDateDay || rec.Status != model.LifeStatusActive {
		t.Fatalf("日期精度或状态不对: %+v", rec)
	}
}

func loadNewestOfKind(t *testing.T, db *sql.DB, kind string) model.LifeRecord {
	t.Helper()
	return scanLife(t, db.QueryRow(lifeSelect+"WHERE kind = ? ORDER BY id DESC LIMIT 1", kind))
}

// 第一次整轮失败之后重发同一条消息：只补上缺的那次，不会写出第二份记录。
func TestRetryDoesNotDuplicateLifeRecords(t *testing.T) {
	svc, fake, db := newTestService(t)
	ctx := context.Background()

	const msg = "今天游泳了"
	fake.agentReplies = []agentReply{{content: ""}}
	fake.streamText = "好。"

	// 第一次：整轮失败，回复和记录都没落下。
	fake.formationErr = errors.New("模型超时")
	fake.streamErr = errors.New("连接被重置")
	if err := svc.ChatStream(ctx, "c-retry", msg, noopRecall, noopDelta); err == nil {
		t.Fatal("整轮失败时应当返回错误")
	}
	_, status, formation := turnRow(t, db, "c-retry")
	if status != model.TurnStatusFailed || formation != model.FormationFailed {
		t.Fatalf("第一次的状态是 (%q, %q)，期望 (failed, failed)", status, formation)
	}
	if n := countRows(t, db, "SELECT COUNT(*) FROM life_records"); n != 0 {
		t.Fatalf("失败的那次写下了 %d 条记录", n)
	}

	// 第二次：同样的 client_id，这次成功。
	fake.formationErr = nil
	fake.streamErr = nil
	fake.formationResult = &llm.FormationResult{
		LifeRecords: []llm.LifeRecord{activityRec(msg, msg, 0)},
	}
	if err := svc.ChatStream(ctx, "c-retry", msg, noopRecall, noopDelta); err != nil {
		t.Fatalf("重试失败: %v", err)
	}

	if n := countRows(t, db, "SELECT COUNT(*) FROM chat_messages WHERE client_id = 'c-retry'"); n != 1 {
		t.Fatalf("用户名下留下了 %d 条消息", n)
	}
	if got := loadLifeRecord(t, db); got.Content != msg {
		t.Fatalf("记录内容是 %q", got.Content)
	}

	// 再次请求不重复写：已结束的轮次只重放，不重跑记忆写入。
	if err := svc.ChatStream(ctx, "c-retry", msg, noopRecall, noopDelta); err != nil {
		t.Fatalf("再次重试失败: %v", err)
	}
	if n := countRows(t, db, "SELECT COUNT(*) FROM life_records"); n != 1 {
		t.Fatalf("重复请求之后记录变成了 %d 条", n)
	}
	if _, _, formation := turnRow(t, db, "c-retry"); formation != model.FormationApplied {
		t.Fatalf("记忆写入状态是 %q，期望 applied", formation)
	}
}

// 记忆已经提交、回复却生成失败时，记录必须原样保留，状态也不能被降回去。
func TestLifeRecordsSurviveReplyFailure(t *testing.T) {
	svc, fake, db := newTestService(t)
	ctx := context.Background()

	fake.agentReplies = []agentReply{{content: ""}}
	fake.streamErr = errors.New("生成回复时连接被重置")
	fake.formationResult = &llm.FormationResult{
		MemoryActions: []llm.MemoryAction{
			{Action: "FORM", Candidate: &llm.MemoryCandidate{Content: "用户开始跑步", Category: "habit"}},
		},
		LifeRecords: []llm.LifeRecord{activityRec("今天跑步了", "今天跑步了", 0)},
	}

	if err := svc.ChatStream(ctx, "c-halfdone", "今天跑步了", noopRecall, noopDelta); err == nil {
		t.Fatal("回复失败时应当返回错误")
	}

	_, status, formation := turnRow(t, db, "c-halfdone")
	if status != model.TurnStatusFailed {
		t.Fatalf("轮次状态是 %q，期望 failed", status)
	}
	if formation != model.FormationApplied {
		t.Fatalf("记忆写入状态是 %q，期望保持 applied", formation)
	}
	if n := countRows(t, db, "SELECT COUNT(*) FROM memories"); n != 1 {
		t.Fatalf("记忆有 %d 条，期望 1 条", n)
	}
	if got := loadLifeRecord(t, db); got.Status != model.LifeStatusActive {
		t.Fatalf("记录状态被改成 %q", got.Status)
	}
}

// 同一批里只要有一条写不进去，长期记忆和生活记录一起回滚，不会只提交一半。
func TestFormationRollsBackMemoryAndLifeTogether(t *testing.T) {
	svc, _, db := newTestService(t)
	ctx := context.Background()

	turn, err := svc.repo.EnsureUserMessage(ctx, "c-rollback", "今天跑步了")
	if err != nil {
		t.Fatal(err)
	}
	owner := "rollback-owner"
	if claimed, err := svc.repo.ClaimFormation(ctx, turn.ID, owner, 300); err != nil || !claimed {
		t.Fatalf("认领失败: claimed=%v err=%v", claimed, err)
	}

	ops := []model.MemoryOp{
		{Kind: model.OpForm, Content: "用户开始跑步", Category: "habit", ChatMessageID: turn.ID},
	}
	// 来源指向一条不存在的消息，外键会在事务里拒绝它。
	badLife := []model.LifeRecordOp{{
		Kind: model.LifeKindActivity, State: model.LifeStateDone,
		Basis: model.LifeBasisExplicit, Subject: model.LifeSubjectUser,
		Content: "今天跑步了", ChatMessageID: turn.ID + 9999, EvidenceQuote: "今天跑步了",
		MentionedOn: "2026-10-08", DatePrecision: model.LifeDateUnknown,
	}}

	if err := svc.repo.ApplyFormation(ctx, turn.ID, owner, ops, badLife); err == nil {
		t.Fatal("非法来源应当让整批写入失败")
	}

	if n := countRows(t, db, "SELECT COUNT(*) FROM memories"); n != 0 {
		t.Fatalf("回滚后还剩 %d 条记忆", n)
	}
	if n := countRows(t, db, "SELECT COUNT(*) FROM life_records"); n != 0 {
		t.Fatalf("回滚后还剩 %d 条生活记录", n)
	}
	// 状态更新在同一事务里，不该被单独提交成 applied。
	if n := countRows(t, db, "SELECT COUNT(*) FROM chat_messages WHERE id = ? AND formation_status = 'applied'", turn.ID); n != 0 {
		t.Fatal("写入失败却把轮次标成了 applied")
	}
}

// 失去执行权的旧执行者不能提交自己的那批记录。
func TestOldOwnerCannotCommitLifeRecords(t *testing.T) {
	svc, _, db := newTestService(t)
	ctx := context.Background()

	turn, err := svc.repo.EnsureUserMessage(ctx, "c-life-lease", "今天跑步了")
	if err != nil {
		t.Fatal(err)
	}
	if claimed, err := svc.repo.ClaimFormation(ctx, turn.ID, "old-owner", 300); err != nil || !claimed {
		t.Fatalf("旧执行者认领失败: claimed=%v err=%v", claimed, err)
	}
	// 把认领时刻推到租约之外，让新执行者接手。
	if _, err := db.Exec(
		"UPDATE chat_messages SET formation_started_at = NOW() - INTERVAL 30 MINUTE WHERE id = ?", turn.ID); err != nil {
		t.Fatal(err)
	}
	if claimed, err := svc.repo.ClaimFormation(ctx, turn.ID, "new-owner", 300); err != nil || !claimed {
		t.Fatalf("新执行者认领失败: claimed=%v err=%v", claimed, err)
	}

	newLife := []model.LifeRecordOp{{
		Kind: model.LifeKindActivity, State: model.LifeStateDone,
		Basis: model.LifeBasisExplicit, Subject: model.LifeSubjectUser,
		Content: "新执行者的记录", ChatMessageID: turn.ID, EvidenceQuote: "今天跑步了",
		MentionedOn: "2026-10-08", DatePrecision: model.LifeDateUnknown,
	}}
	if err := svc.repo.ApplyFormation(ctx, turn.ID, "new-owner", nil, newLife); err != nil {
		t.Fatalf("新执行者提交失败: %v", err)
	}

	oldLife := []model.LifeRecordOp{{
		Kind: model.LifeKindActivity, State: model.LifeStateNegated,
		Basis: model.LifeBasisExplicit, Subject: model.LifeSubjectUser,
		Content: "旧执行者的记录", ChatMessageID: turn.ID, EvidenceQuote: "今天跑步了",
		MentionedOn: "2026-10-08", DatePrecision: model.LifeDateUnknown,
	}}
	if err := svc.repo.ApplyFormation(ctx, turn.ID, "old-owner", nil, oldLife); !errors.Is(err, repository.ErrFormationLeaseLost) {
		t.Fatalf("旧执行者应当拿到 ErrFormationLeaseLost，实际是 %v", err)
	}

	got := loadLifeRecord(t, db)
	if got.Content != "新执行者的记录" {
		t.Fatalf("记录被旧执行者覆盖成 %q", got.Content)
	}
	if n := countRows(t, db, "SELECT COUNT(*) FROM memories"); n != 0 {
		t.Fatalf("意外写入了 %d 条记忆", n)
	}
}

// 恢复补写时，「昨天」要以原始消息落库的那一天为准，不是以恢复发生的那一天为准。
func TestRecoveryKeepsOriginalDateAttribution(t *testing.T) {
	svc, fake, db := newTestService(t)
	ctx := context.Background()

	fake.agentReplies = []agentReply{{content: ""}}
	fake.streamText = "好。"

	// 第一次写入失败，留下一个等待补写的轮次。
	fake.formationErr = errors.New("模型超时")
	if err := svc.ChatStream(ctx, "c-recover-life", "昨天跑了五公里", noopRecall, noopDelta); err != nil {
		t.Fatalf("回复不应因此中断: %v", err)
	}

	// 把这条消息的落库时刻往前挪三天，模拟「隔了几天才恢复」。
	if _, err := db.Exec(
		"UPDATE chat_messages SET created_at = created_at - INTERVAL 3 DAY WHERE client_id = 'c-recover-life'"); err != nil {
		t.Fatal(err)
	}

	recovered := activityRec("昨天跑了五公里", "昨天跑了五公里", -1)
	recovered.When.Text = "昨天"

	fake.formationErr = nil
	fake.formationResult = &llm.FormationResult{LifeRecords: []llm.LifeRecord{recovered}}

	got, err := svc.RecoverFormation(ctx, "c-recover-life")
	if err != nil {
		t.Fatalf("恢复失败: %v", err)
	}
	if got != RecoverApplied {
		t.Fatalf("恢复结果是 %q，期望 applied", got)
	}

	var storedDay string
	if err := db.QueryRow(
		"SELECT DATE_FORMAT(created_at, '%Y-%m-%d') FROM chat_messages WHERE client_id = 'c-recover-life'").Scan(&storedDay); err != nil {
		t.Fatal(err)
	}
	wantDay, err := time.Parse(time.DateOnly, storedDay)
	if err != nil {
		t.Fatal(err)
	}

	rec := loadLifeRecord(t, db)
	if rec.MentionedOn != storedDay {
		t.Fatalf("提及日期是 %q，期望原始消息落库那天 %q", rec.MentionedOn, storedDay)
	}
	wantOccurred := wantDay.AddDate(0, 0, -1).Format(time.DateOnly)
	if rec.OccurredOn != wantOccurred {
		t.Fatalf("发生日期是 %q，期望 %q", rec.OccurredOn, wantOccurred)
	}
	t.Logf("原话「昨天跑了五公里」：消息落库日=%s 提及日=%s 发生日=%s 原话时间说法=%q（恢复发生于 %s）",
		storedDay, rec.MentionedOn, rec.OccurredOn, rec.DateText, svc.clock.LocalDate(time.Now()))

	// 不能按恢复时刻算成今天；两者相差三天。
	if today := svc.clock.LocalDate(time.Now()); rec.OccurredOn == today {
		t.Fatalf("发生日期被按恢复时刻算成了今天 %q", today)
	}
}

// 日期算不出来时，原话里的时间说法要经得起一次真实的写入和读取。
func TestUnknownDateKeepsOriginalText(t *testing.T) {
	svc, fake, db := newTestService(t)
	ctx := context.Background()

	const msg = "三年前开始跑步"
	fake.agentReplies = []agentReply{{content: ""}}
	fake.streamText = "好。"
	fake.formationResult = &llm.FormationResult{
		LifeRecords: []llm.LifeRecord{{
			Kind: "activity", State: "done", Basis: "explicit", Subject: "user",
			Modality: "actual", Content: "用户三年前开始跑步", EvidenceQuote: msg,
			// 模型给的是换算不出来的表达：没有 offset_days，也没有具体日期。
			When: llm.RecordWhen{Kind: "unknown", Text: "三年前"},
		}},
	}

	if err := svc.ChatStream(ctx, "c-datetext", msg, noopRecall, noopDelta); err != nil {
		t.Fatalf("ChatStream 失败: %v", err)
	}
	userID, status, formation := turnRow(t, db, "c-datetext")
	if status != model.TurnStatusOK || formation != model.FormationApplied {
		t.Fatalf("轮次状态 (%q, %q)，期望 (ok, applied)", status, formation)
	}

	// 读取走对外查询接口，不是测试自己拼的 SQL。
	got, err := svc.ListLifeRecords(ctx, LifeRecordQuery{Kinds: []string{model.LifeKindActivity}})
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("查到 %d 条记录，期望 1 条", len(got))
	}
	rec := got[0]

	if rec.OccurredOn != "" || rec.DatePrecision != model.LifeDateUnknown {
		t.Fatalf("日期应当仍是未知，实际是 occurred_on=%q precision=%q", rec.OccurredOn, rec.DatePrecision)
	}
	if rec.DateText != "三年前" {
		t.Fatalf("原话里的时间说法是 %q，期望 %q", rec.DateText, "三年前")
	}
	if rec.ChatMessageID != userID {
		t.Fatalf("来源是消息 %d，期望本轮用户消息 %d", rec.ChatMessageID, userID)
	}

	// 另外直接查一次列，确认落库的不是空串也不是 NULL。
	var stored sql.NullString
	if err := db.QueryRow("SELECT date_text FROM life_records WHERE id = ?", rec.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if !stored.Valid || stored.String != "三年前" {
		t.Fatalf("库里存的 date_text 是 %+v，期望 %q", stored, "三年前")
	}

	// 来源关联仍然可用：这条记录能顺着 chat_message_id 回到原话。
	var content string
	if err := db.QueryRow("SELECT content FROM chat_messages WHERE id = ?", rec.ChatMessageID).Scan(&content); err != nil {
		t.Fatal(err)
	}
	if content != msg {
		t.Fatalf("来源消息内容是 %q，期望 %q", content, msg)
	}
}

// 读取入口：按日期、类型、主体筛选，条数受上限约束。
func TestListLifeRecordsFilters(t *testing.T) {
	svc, _, db := newTestService(t)
	ctx := context.Background()

	// 一条来源消息，生活记录的外键要指向它。
	if _, err := db.Exec(
		"INSERT INTO chat_messages (id, role, content, client_id, status, formation_status) VALUES (1, 'user', '证据', 'seed', 'ok', 'applied')"); err != nil {
		t.Fatal(err)
	}

	seed := []struct {
		kind, state, content, occurredOn, subject string
	}{
		{"activity", "done", "10-01 跑步", "2026-10-01", "user"},
		{"activity", "done", "10-05 游泳", "2026-10-05", "user"},
		{"mood", "none", "10-05 焦虑", "2026-10-05", "user"},
		{"activity", "done", "朋友 10-05 跑步", "2026-10-05", "other"},
	}
	for _, s := range seed {
		if _, err := db.Exec(`INSERT INTO life_records
				(kind, state, basis, subject, content, chat_message_id, evidence_quote,
				 mentioned_on, occurred_on, date_precision, status)
			VALUES (?, ?, 'explicit', ?, ?, 1, '证据', '2026-10-09', ?, 'day', 'active')`,
			s.kind, s.state, s.subject, s.content, s.occurredOn); err != nil {
			t.Fatal(err)
		}
	}

	cases := []struct {
		name string
		q    LifeRecordQuery
		want int
	}{
		{"默认只看用户本人的记录", LifeRecordQuery{}, 3},
		{"按发生日期区间", LifeRecordQuery{OccurredFrom: "2026-10-05", OccurredTo: "2026-10-05"}, 2},
		{"按类型", LifeRecordQuery{Kinds: []string{"mood"}}, 1},
		{"类型与区间叠加", LifeRecordQuery{Kinds: []string{"activity"}, OccurredFrom: "2026-10-05"}, 1},
		{"显式放开主体", LifeRecordQuery{Subject: "all"}, 4},
		{"只看别人的记录", LifeRecordQuery{Subject: "other"}, 1},
		{"条数上限", LifeRecordQuery{Limit: 1}, 1},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := svc.ListLifeRecords(ctx, tc.q)
			if err != nil {
				t.Fatalf("查询失败: %v", err)
			}
			if len(got) != tc.want {
				t.Fatalf("返回 %d 条，期望 %d 条: %+v", len(got), tc.want, got)
			}
		})
	}

	if _, err := svc.ListLifeRecords(ctx, LifeRecordQuery{OccurredFrom: "十月一日"}); !errors.Is(err, ErrInvalidLifeQuery) {
		t.Fatalf("非法日期应当返回 ErrInvalidLifeQuery，实际是 %v", err)
	}
	// 两种日期口径混用必须报错，不能随便挑一种生效。
	_, err := svc.ListLifeRecords(ctx, LifeRecordQuery{
		OccurredFrom:  "2026-10-01",
		MentionedFrom: "2026-10-01",
	})
	if !errors.Is(err, ErrInvalidLifeQuery) {
		t.Fatalf("occurred_* 与 mentioned_* 同时给出应当被拒绝，实际是 %v", err)
	}
}

// 召回工具走 executeTool（模型真正会走的那条路）：两种日期口径不能混，未知发生日期不能被算到某天。
func TestRecallLifeRecordsTool(t *testing.T) {
	svc, _, db := newTestService(t)
	ctx := context.Background()

	if _, err := db.Exec(
		"INSERT INTO chat_messages (id, role, content, client_id, status, formation_status) VALUES (1, 'user', '证据', 'seed', 'ok', 'applied')"); err != nil {
		t.Fatal(err)
	}

	// 1 有确切发生日期；2 只说「三年前」；3 是别人的事；4 已被纠正；5~7 用来触发截断。
	seed := []struct {
		id, chatMessageID             int64
		kind, state, subject, content string
		mentionedOn, occurredOn       string
		precision, dateText, status   string
	}{
		{1, 1, "activity", "done", "user", "十月五日跑了五公里", "2026-10-05", "2026-10-05", "day", "", "active"},
		{2, 1, "activity", "done", "user", "三年前看过一部电影", "2026-10-09", "", "unknown", "三年前", "active"},
		{3, 1, "activity", "done", "other", "朋友十月五日来借钱", "2026-10-05", "2026-10-05", "day", "", "active"},
		{4, 1, "goal", "wish", "user", "十月一日定过一个旧目标", "2026-10-01", "2026-10-01", "day", "", "corrected"},
		{5, 1, "mood", "none", "user", "十月五日有点焦虑", "2026-10-05", "2026-10-05", "day", "", "active"},
		{6, 1, "activity", "planned", "user", "十月五日约了牙医", "2026-10-05", "2026-10-05", "day", "", "active"},
		{7, 1, "activity", "negated", "user", "十月五日没有健身", "2026-10-05", "2026-10-05", "day", "", "active"},
	}
	for _, s := range seed {
		if _, err := db.Exec(`INSERT INTO life_records
				(id, kind, state, basis, subject, content, chat_message_id, evidence_quote,
				 mentioned_on, occurred_on, date_precision, date_text, status)
			VALUES (?, ?, ?, 'explicit', ?, ?, ?, '证据', ?, ?, ?, ?, ?)`,
			s.id, s.kind, s.state, s.subject, s.content, s.chatMessageID,
			s.mentionedOn, nullOrNil(s.occurredOn), s.precision, nullOrNil(s.dateText), s.status); err != nil {
			t.Fatal(err)
		}
	}

	call := func(t *testing.T, today, args string) string {
		t.Helper()
		result, memories := svc.executeTool(ctx, llm.ToolCall{
			Name: "recall_life_records", Arguments: args,
		}, map[int64]bool{}, today)
		if len(memories) != 0 {
			t.Fatalf("生活记录不该被当成记忆返回：%+v", memories)
		}
		return result
	}

	t.Run("按发生日期查不到未知发生日期的记录", func(t *testing.T) {
		got := call(t, "2026-10-09", `{"occurred_from":"2026-10-05","occurred_to":"2026-10-05"}`)
		for _, want := range []string{"十月五日跑了五公里", "十月五日有点焦虑", "十月五日约了牙医", "十月五日没有健身"} {
			if !strings.Contains(got, want) {
				t.Fatalf("按发生日期查不到 %q：%s", want, got)
			}
		}
		// 发生日期未知的记录不能因为「用户是10-09说的」就被算成 10-05 发生的。
		if strings.Contains(got, "三年前看过一部电影") {
			t.Fatalf("发生日期未知的记录被算进了某一天：%s", got)
		}
		// 别人的事和已被纠正的记录默认都不在范围里。
		for _, unwanted := range []string{"朋友十月五日来借钱", "十月一日定过一个旧目标"} {
			if strings.Contains(got, unwanted) {
				t.Fatalf("不该出现的记录 %q 进了结果：%s", unwanted, got)
			}
		}
	})

	t.Run("按提及日期能找到日期算不出来的记录", func(t *testing.T) {
		got := call(t, "2026-10-09", `{"mentioned_from":"2026-10-09","mentioned_to":"2026-10-09"}`)
		if !strings.Contains(got, "三年前看过一部电影") {
			t.Fatalf("按提及日期也找不到这条记录：%s", got)
		}
		if strings.Contains(got, "十月五日跑了五公里") {
			t.Fatalf("10-05 提到的记录不该出现在只查 10-09 的结果里：%s", got)
		}
		if !strings.Contains(got, `"occurred_on"`) {
			t.Logf("该记录没有发生日期，结果里也就没有 occurred_on 字段：%s", got)
		}
		if !strings.Contains(got, "三年前") {
			t.Fatalf("无法解析的原始时间表达应当保留：%s", got)
		}
	})

	t.Run("两种日期口径混用被拒绝", func(t *testing.T) {
		got := call(t, "2026-10-09",
			`{"occurred_from":"2026-10-05","mentioned_from":"2026-10-05"}`)
		if !strings.Contains(got, "只能选一种") {
			t.Fatalf("混用两种日期口径没有被拒绝：%s", got)
		}
	})

	t.Run("按内容查", func(t *testing.T) {
		got := call(t, "2026-10-09", `{"query":"电影"}`)
		if !strings.Contains(got, "三年前看过一部电影") {
			t.Fatalf("按内容查不到：%s", got)
		}
		if strings.Contains(got, "十月五日跑了五公里") {
			t.Fatalf("无关记录也返回了：%s", got)
		}
	})

	t.Run("超过条数上限时标记截断", func(t *testing.T) {
		got := call(t, "2026-10-09", `{"query":"十月","limit":2}`)
		if !strings.Contains(got, `"truncated":true`) {
			t.Fatalf("结果被截断了却没有标记：%s", got)
		}
		if !strings.Contains(got, `"count":2`) {
			t.Fatalf("返回条数不受 limit 约束：%s", got)
		}
	})

	t.Run("日期参考取自本轮消息", func(t *testing.T) {
		got := call(t, "2019-03-04", `{"query":"电影"}`)
		if !strings.Contains(got, `"today":"2019-03-04"`) {
			t.Fatalf("工具没有把调用方给的日期参考传下去：%s", got)
		}
	})
}

// nullOrNil 让空串写成 NULL，避免把「不知道」存成空字符串。
func nullOrNil(s string) any {
	if s == "" {
		return nil
	}
	return s
}
