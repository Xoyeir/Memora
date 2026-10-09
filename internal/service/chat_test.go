package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/Xoyeir/memora/internal/llm"
	"github.com/Xoyeir/memora/internal/model"
	"github.com/Xoyeir/memora/internal/repository"
	"github.com/Xoyeir/memora/internal/testsupport"
)

// agentReply 是假模型在某一步的返回。
type agentReply struct {
	content string
	calls   []llm.ToolCall
	err     error
	// after 在返回前触发，用来在一轮中间制造取消。
	after func()
}

// fakeLLM 按剧本返回结果，用来把「模型什么时候失败/取消」变成可控输入。
type fakeLLM struct {
	mu sync.Mutex

	formationResult *llm.FormationResult
	formationErr    error
	formationN      int

	agentReplies []agentReply
	agentN       int

	streamText string
	streamErr  error
	streamN    int
}

func (f *fakeLLM) Formation(ctx context.Context, input string) (*llm.FormationResult, error) {
	f.mu.Lock()
	f.formationN++
	res, err := f.formationResult, f.formationErr
	f.mu.Unlock()

	if err != nil {
		return nil, err
	}
	if res == nil {
		return &llm.FormationResult{}, nil
	}
	return res, nil
}

func (f *fakeLLM) ChatWithTools(ctx context.Context, messages []llm.Message, tools []llm.Tool) (string, []llm.ToolCall, error) {
	f.mu.Lock()
	idx := f.agentN
	f.agentN++
	f.mu.Unlock()

	if len(f.agentReplies) == 0 {
		return "", nil, nil
	}
	if idx >= len(f.agentReplies) {
		idx = len(f.agentReplies) - 1
	}
	r := f.agentReplies[idx]
	if r.after != nil {
		r.after()
	}
	return r.content, r.calls, r.err
}

func (f *fakeLLM) StreamMessages(ctx context.Context, messages []llm.Message, onDelta func(string)) (string, error) {
	f.mu.Lock()
	f.streamN++
	text, err := f.streamText, f.streamErr
	f.mu.Unlock()

	if text != "" {
		onDelta(text)
	}
	return text, err
}

func (f *fakeLLM) ExtractJournal(ctx context.Context, content string) (*llm.ExtractionResult, error) {
	return &llm.ExtractionResult{}, nil
}

func (f *fakeLLM) counts() (formation, agent, stream int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.formationN, f.agentN, f.streamN
}

func newTestService(t *testing.T) (*Service, *fakeLLM, *sql.DB) {
	t.Helper()

	db, repo := testsupport.Open(t)
	fake := &fakeLLM{}
	clock, err := NewClock("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	return NewService(repo, fake, clock), fake, db
}

func noopRecall([]model.Memory) {}
func noopDelta(string)          {}

// turnRow 读回一轮的三个完成状态：用户消息、记忆写入、助手回复。
func turnRow(t *testing.T, db *sql.DB, clientID string) (userID int64, status, formationStatus string) {
	t.Helper()
	err := db.QueryRow(
		"SELECT id, status, formation_status FROM chat_messages WHERE client_id = ?", clientID).
		Scan(&userID, &status, &formationStatus)
	if err != nil {
		t.Fatalf("读取轮次失败: %v", err)
	}
	return userID, status, formationStatus
}

func countRows(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatalf("统计失败: %v", err)
	}
	return n
}

func TestChatStreamSavesTurnAndReply(t *testing.T) {
	svc, fake, db := newTestService(t)
	fake.agentReplies = []agentReply{{content: ""}}
	fake.streamText = "我在听。"

	var got strings.Builder
	if err := svc.ChatStream(context.Background(), "c-1", "今天有点累", noopRecall,
		func(d string) { got.WriteString(d) }); err != nil {
		t.Fatalf("ChatStream 失败: %v", err)
	}

	if got.String() != "我在听。" {
		t.Fatalf("收到的增量是 %q", got.String())
	}

	userID, status, formation := turnRow(t, db, "c-1")
	if status != model.TurnStatusOK {
		t.Fatalf("轮次状态是 %q，期望 ok", status)
	}
	if formation != model.FormationApplied {
		t.Fatalf("记忆写入状态是 %q，期望 applied", formation)
	}

	var content string
	if err := db.QueryRow("SELECT content FROM chat_messages WHERE reply_to_id = ?", userID).Scan(&content); err != nil {
		t.Fatalf("读取回复失败: %v", err)
	}
	if content != "我在听。" {
		t.Fatalf("落库的回复是 %q", content)
	}
}

func TestChatStreamRecallsThroughTool(t *testing.T) {
	svc, fake, db := newTestService(t)

	if _, err := db.Exec("INSERT INTO memories (content, category, status) VALUES (?, ?, 'active')", "用户在做 Memora", "project"); err != nil {
		t.Fatal(err)
	}

	fake.agentReplies = []agentReply{
		{calls: []llm.ToolCall{{ID: "call-1", Name: "recall_memories", Arguments: `{"query":"Memora"}`}}},
		{content: "忽略"},
	}
	fake.streamText = "记得，你在做 Memora。"

	var recalled []model.Memory
	if err := svc.ChatStream(context.Background(), "c-2", "你还记得我在做什么吗", func(m []model.Memory) { recalled = m }, noopDelta); err != nil {
		t.Fatalf("ChatStream 失败: %v", err)
	}

	if len(recalled) != 1 || !strings.Contains(recalled[0].Content, "Memora") {
		t.Fatalf("召回结果不符合预期: %+v", recalled)
	}
	if _, agent, _ := fake.counts(); agent != 2 {
		t.Fatalf("Agent 决策了 %d 次，期望 2 次（一次工具、一次收尾）", agent)
	}
}

func TestToolErrorsDoNotBreakReply(t *testing.T) {
	cases := []struct {
		name    string
		args    string
		tool    string
		wantSub string
	}{
		{name: "未知工具", tool: "delete_everything", args: `{}`, wantSub: "未知工具"},
		{name: "参数不是合法 JSON", tool: "recall_memories", args: `{`, wantSub: "参数解析失败"},
		{name: "memory_id 非正", tool: "get_memory_source", args: `{"memory_id":0}`, wantSub: "正整数"},
		{name: "未召回的记忆", tool: "get_memory_source", args: `{"memory_id":999}`, wantSub: "无权访问"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, fake, _ := newTestService(t)
			fake.agentReplies = []agentReply{
				{calls: []llm.ToolCall{{ID: "call-1", Name: tc.tool, Arguments: tc.args}}},
				{content: "收尾"},
			}
			fake.streamText = "好的。"

			if err := svc.ChatStream(context.Background(), "c-tool", "问题", noopRecall, noopDelta); err != nil {
				t.Fatalf("工具报错不应中断整轮: %v", err)
			}

			// 工具错误是以 tool 消息回传给模型的，不是抛给用户的中断。
			_, _, stream := fake.counts()
			if stream != 1 {
				t.Fatalf("流式回复调用了 %d 次，期望 1 次", stream)
			}
		})
	}
}

// 步数耗尽时仍然给出回复，但这一轮不是「正常完成」意义上的无损——
// 报告里明确记为「工具链被截断后继续生成」。
func TestAgentStepExhaustionStillReplies(t *testing.T) {
	svc, fake, db := newTestService(t)
	fake.agentReplies = []agentReply{
		{calls: []llm.ToolCall{{ID: "call-1", Name: "recall_memories", Arguments: `{"query":"a"}`}}},
	}
	fake.streamText = "勉强回复。"

	if err := svc.ChatStream(context.Background(), "c-steps", "一直在调工具", noopRecall, noopDelta); err != nil {
		t.Fatalf("ChatStream 失败: %v", err)
	}

	if _, agent, _ := fake.counts(); agent != maxAgentSteps {
		t.Fatalf("Agent 决策了 %d 次，期望耗尽在 %d 次", agent, maxAgentSteps)
	}
	_, status, _ := turnRow(t, db, "c-steps")
	if status != model.TurnStatusOK {
		t.Fatalf("轮次状态是 %q", status)
	}
}

func TestProviderFailureIsRecordedAndRetryable(t *testing.T) {
	svc, fake, db := newTestService(t)
	fake.streamErr = errors.New("连接被重置")

	err := svc.ChatStream(context.Background(), "c-fail", "在吗", noopRecall, noopDelta)
	if err == nil {
		t.Fatal("模型失败时应当返回错误")
	}

	userID, status, _ := turnRow(t, db, "c-fail")
	if status != model.TurnStatusFailed {
		t.Fatalf("轮次状态是 %q，期望 failed", status)
	}
	if n := countRows(t, db, "SELECT COUNT(*) FROM chat_messages WHERE reply_to_id = ?", userID); n != 0 {
		t.Fatalf("没有任何输出却留下了 %d 条回复行", n)
	}

	// 失败之后重试必须能重新跑，而不是被判成「还在处理中」。
	fake.streamErr = nil
	fake.streamText = "在的。"
	if err := svc.ChatStream(context.Background(), "c-fail", "在吗", noopRecall, noopDelta); err != nil {
		t.Fatalf("失败后重试失败: %v", err)
	}
	_, status, _ = turnRow(t, db, "c-fail")
	if status != model.TurnStatusOK {
		t.Fatalf("重试后状态是 %q，期望 ok", status)
	}
}

// 已经流出去的内容必须落库：否则「回复断流」会被当成「整轮没有发生」。
func TestPartialOutputIsSavedAsPartial(t *testing.T) {
	svc, fake, db := newTestService(t)
	fake.streamText = "说到一半"
	fake.streamErr = errors.New("流被中断")

	err := svc.ChatStream(context.Background(), "c-partial", "讲讲", noopRecall, noopDelta)
	if err == nil {
		t.Fatal("流中断时应当返回错误")
	}

	userID, status, _ := turnRow(t, db, "c-partial")
	if status != model.TurnStatusFailed {
		t.Fatalf("轮次状态是 %q，期望 failed", status)
	}

	var content, replyStatus string
	if err := db.QueryRow(
		"SELECT content, status FROM chat_messages WHERE reply_to_id = ?", userID).
		Scan(&content, &replyStatus); err != nil {
		t.Fatalf("已输出的内容没有落库: %v", err)
	}
	if content != "说到一半" || replyStatus != "partial" {
		t.Fatalf("半成品回复是 (%q, %q)", content, replyStatus)
	}
}

func TestCanceledTurnIsRecordedAsCanceled(t *testing.T) {
	svc, fake, db := newTestService(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	fake.streamText = "前半句"
	fake.streamErr = context.Canceled
	fake.agentReplies = []agentReply{{content: ""}}

	if err := svc.ChatStream(ctx, "c-cancel", "在吗", noopRecall, func(string) { cancel() }); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消应当原样返回 context.Canceled，实际是 %v", err)
	}

	userID, status, _ := turnRow(t, db, "c-cancel")
	if status != model.TurnStatusCanceled {
		t.Fatalf("轮次状态是 %q，期望 canceled", status)
	}

	// 已经流给用户的内容要保留，同时取消原因不能被改写成 failed。
	var content, replyStatus string
	if err := db.QueryRow(
		"SELECT content, status FROM chat_messages WHERE reply_to_id = ?", userID).
		Scan(&content, &replyStatus); err != nil {
		t.Fatalf("取消时已输出的内容没有落库: %v", err)
	}
	if content != "前半句" || replyStatus != "partial" {
		t.Fatalf("取消后的半成品是 (%q, %q)，期望 (前半句, partial)", content, replyStatus)
	}

	if _, _, stream := fake.counts(); stream != 1 {
		t.Fatalf("取消后流式请求被调用了 %d 次，期望 1 次（不得重试）", stream)
	}
}

// 取消发生在 Agent 阶段时，连流式请求都不该发出。
func TestNoModelCallAfterCancellation(t *testing.T) {
	svc, fake, db := newTestService(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	fake.agentReplies = []agentReply{{
		calls: []llm.ToolCall{{ID: "call-1", Name: "recall_memories", Arguments: `{"query":"a"}`}},
		after: cancel,
	}}

	err := svc.ChatStream(ctx, "c-cancel-agent", "在吗", noopRecall, noopDelta)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("期望 context.Canceled，实际是 %v", err)
	}

	_, agent, stream := fake.counts()
	if agent != 1 {
		t.Fatalf("取消后 Agent 又决策了 %d 次", agent)
	}
	if stream != 0 {
		t.Fatalf("取消后仍然发起了 %d 次流式请求", stream)
	}

	_, status, _ := turnRow(t, db, "c-cancel-agent")
	if status != model.TurnStatusCanceled {
		t.Fatalf("轮次状态是 %q，期望 canceled", status)
	}
}

func TestCompletedTurnIsReplayedNotRegenerated(t *testing.T) {
	svc, fake, db := newTestService(t)
	fake.agentReplies = []agentReply{{content: ""}}
	fake.streamText = "第一次的回答"

	if err := svc.ChatStream(context.Background(), "c-replay", "你好", noopRecall, noopDelta); err != nil {
		t.Fatal(err)
	}
	_, agent1, stream1 := fake.counts()

	var replayed strings.Builder
	if err := svc.ChatStream(context.Background(), "c-replay", "你好", noopRecall,
		func(d string) { replayed.WriteString(d) }); err != nil {
		t.Fatalf("重放失败: %v", err)
	}

	if replayed.String() != "第一次的回答" {
		t.Fatalf("重放内容不对: %q", replayed.String())
	}
	_, agent2, stream2 := fake.counts()
	if agent2 != agent1 || stream2 != stream1 {
		t.Fatalf("已完成的轮次又调用了模型：agent %d→%d，stream %d→%d", agent1, agent2, stream1, stream2)
	}

	if n := countRows(t, db, "SELECT COUNT(*) FROM chat_messages"); n != 2 {
		t.Fatalf("重复投递后共有 %d 条消息，期望 2 条（一问一答）", n)
	}
}

func TestSameClientIDWithDifferentContentIsRejected(t *testing.T) {
	svc, fake, db := newTestService(t)
	fake.agentReplies = []agentReply{{content: ""}}
	fake.streamText = "回答"

	if err := svc.ChatStream(context.Background(), "c-conflict", "原文", noopRecall, noopDelta); err != nil {
		t.Fatal(err)
	}

	err := svc.ChatStream(context.Background(), "c-conflict", "被改过的内容", noopRecall, noopDelta)
	if !errors.Is(err, ErrClientIDConflict) {
		t.Fatalf("期望 ErrClientIDConflict，实际是 %v", err)
	}

	if n := countRows(t, db, "SELECT COUNT(*) FROM chat_messages WHERE role = 'user'"); n != 1 {
		t.Fatalf("冲突请求创建了 %d 条用户消息，期望 1 条", n)
	}
}

func TestTurnInProgressIsRejected(t *testing.T) {
	svc, fake, db := newTestService(t)

	if _, err := db.Exec(`
		INSERT INTO chat_messages (role, content, client_id, status, formation_status, formation_started_at)
		VALUES ('user', '处理中', 'c-running', 'pending', 'running', NOW())`); err != nil {
		t.Fatal(err)
	}

	err := svc.ChatStream(context.Background(), "c-running", "处理中", noopRecall, noopDelta)
	if !errors.Is(err, ErrTurnInProgress) {
		t.Fatalf("期望 ErrTurnInProgress，实际是 %v", err)
	}

	if _, agent, stream := fake.counts(); agent != 0 || stream != 0 {
		t.Fatalf("被拒绝的请求仍然调用了模型：agent=%d stream=%d", agent, stream)
	}
}

// 进程在 Formation 阶段崩掉时，留下的是「running + 回复没成功」。
// 这种轮次既不能走恢复入口（回复还没成功），也不该被当成「正在处理中」，
// 否则它永远卡在原地。租约过期后重试必须能重新处理。
func TestStaleRunningDoesNotBlockRetry(t *testing.T) {
	svc, fake, db := newTestService(t)

	res, err := db.Exec(`
		INSERT INTO chat_messages (role, content, client_id, status, formation_status, formation_started_at)
		VALUES ('user', '崩溃现场', 'c-crashed', 'pending', 'running', NOW() - INTERVAL 30 MINUTE)`)
	if err != nil {
		t.Fatal(err)
	}
	turnID, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}

	fake.agentReplies = []agentReply{{content: ""}}
	fake.streamText = "重新回一遍。"

	if err := svc.ChatStream(context.Background(), "c-crashed", "崩溃现场", noopRecall, noopDelta); err != nil {
		t.Fatalf("过期 running 之后的发送被拒绝了: %v", err)
	}

	_, status, formation := turnRow(t, db, "c-crashed")
	if status != model.TurnStatusOK {
		t.Fatalf("重试后轮次状态是 %q，期望 ok", status)
	}
	if formation != model.FormationApplied {
		t.Fatalf("重试后记忆写入状态是 %q，期望 applied", formation)
	}
	if n := countRows(t, db, "SELECT COUNT(*) FROM chat_messages WHERE reply_to_id = ?", turnID); n != 1 {
		t.Fatalf("留下了 %d 条回复，期望 1 条", n)
	}

	// 恢复入口对已经写成的轮次什么都不做，也不会重复写。
	got, err := svc.RecoverFormation(context.Background(), "c-crashed")
	if err != nil {
		t.Fatalf("恢复失败: %v", err)
	}
	if got != RecoverAlreadyApplied {
		t.Fatalf("恢复结果是 %q，期望 already_applied", got)
	}
}

// 记忆写入失败不影响回复；回复也失败时，重试允许再尝试一次记忆写入。
func TestFormationRetriedOnlyAfterReplyAlsoFails(t *testing.T) {
	svc, fake, db := newTestService(t)
	fake.formationErr = errors.New("模型超时")
	fake.streamErr = errors.New("流断了")

	if err := svc.ChatStream(context.Background(), "c-f1", "我最近在学 Go", noopRecall, noopDelta); err == nil {
		t.Fatal("回复失败时应当返回错误")
	}

	_, _, formation := turnRow(t, db, "c-f1")
	if formation != model.FormationFailed {
		t.Fatalf("记忆写入状态是 %q，期望 failed（允许再次尝试）", formation)
	}

	fake.formationErr = nil
	fake.streamErr = nil
	fake.streamText = "好"
	if err := svc.ChatStream(context.Background(), "c-f1", "我最近在学 Go", noopRecall, noopDelta); err != nil {
		t.Fatalf("重试失败: %v", err)
	}

	if n, _, _ := fake.counts(); n != 2 {
		t.Fatalf("Formation 调用了 %d 次，期望 2 次（失败后可再尝试一次）", n)
	}
	_, status, formation := turnRow(t, db, "c-f1")
	if status != model.TurnStatusOK || formation != model.FormationApplied {
		t.Fatalf("重试后状态是 (%q, %q)", status, formation)
	}
}

// 回复成功之后不再重跑 Formation：重跑会把已成功的 action 再做一遍，产生重复记忆。
func TestFormationIsNotRerunAfterSuccessfulReply(t *testing.T) {
	svc, fake, db := newTestService(t)
	fake.formationErr = errors.New("模型超时")
	fake.agentReplies = []agentReply{{content: ""}}
	fake.streamText = "嗯"

	if err := svc.ChatStream(context.Background(), "c-f2", "我最近在学 Go", noopRecall, noopDelta); err != nil {
		t.Fatal(err)
	}
	if err := svc.ChatStream(context.Background(), "c-f2", "我最近在学 Go", noopRecall, noopDelta); err != nil {
		t.Fatal(err)
	}

	if n, _, _ := fake.counts(); n != 1 {
		t.Fatalf("Formation 调用了 %d 次，期望 1 次", n)
	}

	// 重放不会补写：补写要走显式的恢复入口，见 TestRecoverFormationRewritesWithoutDuplication。
	_, _, formation := turnRow(t, db, "c-f2")
	if formation != model.FormationFailed {
		t.Fatalf("记忆写入状态是 %q，期望仍是 failed", formation)
	}
}

// Formation 已经落库之后回复失败：重试不得重复写入记忆。
func TestRetryAfterReplyFailureDoesNotDuplicateMemories(t *testing.T) {
	svc, fake, db := newTestService(t)
	fake.formationResult = &llm.FormationResult{
		MemoryActions: []llm.MemoryAction{{
			Action:    "FORM",
			Candidate: &llm.MemoryCandidate{Content: "用户正在学 Go", Category: "goal"},
		}},
	}
	fake.agentReplies = []agentReply{{content: ""}}
	fake.streamErr = errors.New("流断了")

	if err := svc.ChatStream(context.Background(), "c-dup", "我最近在学 Go", noopRecall, noopDelta); err == nil {
		t.Fatal("回复失败时应当返回错误")
	}
	if n := countRows(t, db, "SELECT COUNT(*) FROM memories"); n != 1 {
		t.Fatalf("第一次写入产生了 %d 条记忆", n)
	}

	fake.streamErr = nil
	fake.streamText = "好，记下了"
	if err := svc.ChatStream(context.Background(), "c-dup", "我最近在学 Go", noopRecall, noopDelta); err != nil {
		t.Fatalf("重试失败: %v", err)
	}

	if n, _, _ := fake.counts(); n != 1 {
		t.Fatalf("Formation 重跑了，共 %d 次", n)
	}
	if n := countRows(t, db, "SELECT COUNT(*) FROM memories"); n != 1 {
		t.Fatalf("重试后记忆变成了 %d 条，期望 1 条", n)
	}
	if n := countRows(t, db, "SELECT COUNT(*) FROM memory_sources"); n != 1 {
		t.Fatalf("重试后证据变成了 %d 条，期望 1 条", n)
	}
}

// 一批记忆操作里只要有任意一条写失败，整批都必须回滚。
//
// 这里用「第二条的证据消息不存在」制造必然失败的外键错误，
// 不依赖 sql_mode，也不依赖字段长度这类会随环境变化的行为。
func TestFormationBatchRollsBackOnAnyFailure(t *testing.T) {
	svc, _, db := newTestService(t)

	turn, err := svc.repo.EnsureUserMessage(context.Background(), "c-batch", "记两件事")
	if err != nil {
		t.Fatal(err)
	}

	const owner = "owner-batch"
	claimed, err := svc.repo.ClaimFormation(context.Background(), turn.ID, owner, 300)
	if err != nil || !claimed {
		t.Fatalf("认领失败: claimed=%v err=%v", claimed, err)
	}

	// 两条都是合法的记忆内容；第二条的证据消息不存在，会在写来源时违反外键。
	ops := []model.MemoryOp{
		{Kind: model.OpForm, Content: "第一条", Category: "goal", ChatMessageID: turn.ID},
		{Kind: model.OpForm, Content: "第二条", Category: "goal", ChatMessageID: turn.ID + 100000},
	}

	if err := svc.repo.ApplyFormation(context.Background(), turn.ID, owner, ops); err == nil {
		t.Fatal("第二条写入失败时 ApplyFormation 应当返回错误")
	}

	if n := countRows(t, db, "SELECT COUNT(*) FROM memories"); n != 0 {
		t.Fatalf("整批应当回滚，却留下了 %d 条记忆", n)
	}
	if n := countRows(t, db, "SELECT COUNT(*) FROM memory_sources"); n != 0 {
		t.Fatalf("整批应当回滚，却留下了 %d 条来源", n)
	}

	// 回滚后不能停留在 applied：状态要退回可重试，恢复入口才有事可做。
	_, _, formation := turnRow(t, db, "c-batch")
	if formation != model.FormationRunning {
		t.Fatalf("回滚后状态是 %q，期望仍是 running（尚未提交）", formation)
	}
	svc.abandonFormation(context.Background(), turn.ID, owner)
	_, _, formation = turnRow(t, db, "c-batch")
	if formation != model.FormationFailed {
		t.Fatalf("放弃后状态是 %q，期望 failed", formation)
	}
}

// 记忆写入失败、回复成功之后，恢复入口补写记忆：不重复写，最后状态正确，回复不变。
func TestRecoverFormationRewritesWithoutDuplication(t *testing.T) {
	svc, fake, db := newTestService(t)

	fake.formationErr = errors.New("模型超时")
	fake.agentReplies = []agentReply{{content: ""}}
	fake.streamText = "记下了。"

	if err := svc.ChatStream(context.Background(), "c-recover", "我最近在学 Go", noopRecall, noopDelta); err != nil {
		t.Fatalf("回复失败不影响整轮: %v", err)
	}

	_, status, formation := turnRow(t, db, "c-recover")
	if status != model.TurnStatusOK || formation != model.FormationFailed {
		t.Fatalf("恢复前状态是 (%q, %q)", status, formation)
	}

	fake.formationErr = nil
	fake.formationResult = &llm.FormationResult{
		MemoryActions: []llm.MemoryAction{
			{Action: "FORM", Candidate: &llm.MemoryCandidate{Content: "用户正在学 Go", Category: "goal"}},
		},
	}

	got, err := svc.RecoverFormation(context.Background(), "c-recover")
	if err != nil {
		t.Fatalf("恢复失败: %v", err)
	}
	if got != RecoverApplied {
		t.Fatalf("恢复结果是 %q，期望 applied", got)
	}

	if n := countRows(t, db, "SELECT COUNT(*) FROM memories"); n != 1 {
		t.Fatalf("恢复后记忆有 %d 条，期望 1 条", n)
	}
	if n := countRows(t, db, "SELECT COUNT(*) FROM memory_sources"); n != 1 {
		t.Fatalf("恢复后来源有 %d 条，期望 1 条", n)
	}
	_, _, formation = turnRow(t, db, "c-recover")
	if formation != model.FormationApplied {
		t.Fatalf("恢复后状态是 %q，期望 applied", formation)
	}

	// 再恢复一次什么都不做，也不会写第二遍。
	got, err = svc.RecoverFormation(context.Background(), "c-recover")
	if err != nil {
		t.Fatalf("重复恢复失败: %v", err)
	}
	if got != RecoverAlreadyApplied {
		t.Fatalf("重复恢复结果是 %q，期望 already_applied", got)
	}
	if n := countRows(t, db, "SELECT COUNT(*) FROM memories"); n != 1 {
		t.Fatalf("重复恢复后记忆有 %d 条，期望 1 条", n)
	}
}

// 补记忆不能碰已经成功的回复，也不能重新生成。
func TestRecoverFormationKeepsReplyIntact(t *testing.T) {
	svc, fake, db := newTestService(t)

	fake.formationErr = errors.New("模型超时")
	fake.agentReplies = []agentReply{{content: ""}}
	fake.streamText = "这是第一次也是唯一一次的回复。"

	if err := svc.ChatStream(context.Background(), "c-recover-reply", "在吗", noopRecall, noopDelta); err != nil {
		t.Fatal(err)
	}

	userID, _, _ := turnRow(t, db, "c-recover-reply")
	var before string
	if err := db.QueryRow("SELECT content FROM chat_messages WHERE reply_to_id = ?", userID).Scan(&before); err != nil {
		t.Fatal(err)
	}

	_, agent1, stream1 := fake.counts()

	fake.formationErr = nil
	if _, err := svc.RecoverFormation(context.Background(), "c-recover-reply"); err != nil {
		t.Fatalf("恢复失败: %v", err)
	}

	var after string
	if err := db.QueryRow("SELECT content FROM chat_messages WHERE reply_to_id = ?", userID).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatalf("补记忆改动了回复: %q → %q", before, after)
	}

	_, agent2, stream2 := fake.counts()
	if agent2 != agent1 || stream2 != stream1 {
		t.Fatalf("补记忆重新生成了回复：agent %d→%d，stream %d→%d", agent1, agent2, stream1, stream2)
	}
	if n := countRows(t, db, "SELECT COUNT(*) FROM chat_messages WHERE reply_to_id = ?", userID); n != 1 {
		t.Fatalf("补记忆留下了 %d 条回复", n)
	}
}

// 请求还没成功结束时，恢复入口必须拒绝：这时候该走正常重试，而不是补记忆。
func TestRecoverFormationRejectsUnfinishedTurn(t *testing.T) {
	svc, fake, db := newTestService(t)
	fake.agentReplies = []agentReply{{content: ""}}
	fake.streamText = "好的"

	if _, err := db.Exec(`
		INSERT INTO chat_messages (role, content, client_id, status, formation_status)
		VALUES ('user', '还没回', 'c-unfinished', 'pending', 'failed')`); err != nil {
		t.Fatal(err)
	}

	if _, err := svc.RecoverFormation(context.Background(), "c-unfinished"); !errors.Is(err, ErrTurnNotComplete) {
		t.Fatalf("期望 ErrTurnNotComplete，实际是 %v", err)
	}
	if _, err := svc.RecoverFormation(context.Background(), "c-nonexistent"); !errors.Is(err, ErrTurnNotFound) {
		t.Fatalf("期望 ErrTurnNotFound，实际是 %v", err)
	}
	if n := countRows(t, db, "SELECT COUNT(*) FROM memories"); n != 0 {
		t.Fatalf("被拒绝的恢复写入了 %d 条记忆", n)
	}
}

// 崩溃留下的 running 超过租约后可以被恢复；租约内的一律不抢。
func TestFormationLeaseSeparatesStaleFromActive(t *testing.T) {
	svc, fake, db := newTestService(t)

	insert := func(clientID, formation, startedAgo string) int64 {
		t.Helper()
		res, err := db.Exec(`
			INSERT INTO chat_messages (role, content, client_id, status, formation_status)
			VALUES ('user', '崩溃现场', ?, 'ok', ?)`, clientID, formation)
		if err != nil {
			t.Fatal(err)
		}
		id, err := res.LastInsertId()
		if err != nil {
			t.Fatal(err)
		}
		if err := svc.repo.CompleteChatTurn(context.Background(), id, "已经回完的回复"); err != nil {
			t.Fatal(err)
		}
		if startedAgo != "" {
			if _, err := db.Exec(
				"UPDATE chat_messages SET formation_started_at = NOW() - INTERVAL "+startedAgo+" WHERE id = ?", id); err != nil {
				t.Fatal(err)
			}
		}
		return id
	}

	insert("c-stale", "running", "30 MINUTE")
	insert("c-active", "running", "10 SECOND")

	fake.formationResult = &llm.FormationResult{
		MemoryActions: []llm.MemoryAction{
			{Action: "FORM", Candidate: &llm.MemoryCandidate{Content: "崩溃前那轮的记忆", Category: "goal"}},
		},
	}

	// 还在租约内的不能被抢走。
	if got, err := svc.RecoverFormation(context.Background(), "c-active"); err != nil {
		t.Fatalf("恢复活跃轮次报错: %v", err)
	} else if got != RecoverInProgress {
		t.Fatalf("活跃轮次的结果是 %q，期望 in_progress", got)
	}
	if n := countRows(t, db, "SELECT COUNT(*) FROM memories"); n != 0 {
		t.Fatalf("抢走了活跃轮次的写入，产生了 %d 条记忆", n)
	}

	// 超过租约的可以恢复。
	if got, err := svc.RecoverFormation(context.Background(), "c-stale"); err != nil {
		t.Fatalf("恢复失效轮次失败: %v", err)
	} else if got != RecoverApplied {
		t.Fatalf("失效轮次的结果是 %q，期望 applied", got)
	}
	if n := countRows(t, db, "SELECT COUNT(*) FROM memories"); n != 1 {
		t.Fatalf("恢复后记忆有 %d 条，期望 1 条", n)
	}
}

// 被接管的旧执行者不能再提交自己的结果。
func TestOldFormationOwnerCannotOverwriteNewResult(t *testing.T) {
	svc, _, db := newTestService(t)
	ctx := context.Background()

	turn, err := svc.repo.EnsureUserMessage(ctx, "c-lease", "被接手的一轮")
	if err != nil {
		t.Fatal(err)
	}

	claimed, err := svc.repo.ClaimFormation(ctx, turn.ID, "old-owner", 300)
	if err != nil || !claimed {
		t.Fatalf("旧执行者认领失败: claimed=%v err=%v", claimed, err)
	}

	// 模拟旧执行者卡住：把认领时刻推到租约之外，让新执行者接手。
	if _, err := db.Exec(
		"UPDATE chat_messages SET formation_started_at = NOW() - INTERVAL 30 MINUTE WHERE id = ?", turn.ID); err != nil {
		t.Fatal(err)
	}

	claimed, err = svc.repo.ClaimFormation(ctx, turn.ID, "new-owner", 300)
	if err != nil || !claimed {
		t.Fatalf("新执行者认领失败: claimed=%v err=%v", claimed, err)
	}

	newOps := []model.MemoryOp{
		{Kind: model.OpForm, Content: "新执行者写下的记忆", Category: "goal", ChatMessageID: turn.ID},
	}
	if err := svc.repo.ApplyFormation(ctx, turn.ID, "new-owner", newOps); err != nil {
		t.Fatalf("新执行者提交失败: %v", err)
	}

	// 旧执行者带着自己的一批操作姗姗来迟。
	oldOps := []model.MemoryOp{
		{Kind: model.OpForm, Content: "旧执行者的记忆", Category: "goal", ChatMessageID: turn.ID},
	}
	if err := svc.repo.ApplyFormation(ctx, turn.ID, "old-owner", oldOps); !errors.Is(err, repository.ErrFormationLeaseLost) {
		t.Fatalf("旧执行者应当拿到 ErrFormationLeaseLost，实际是 %v", err)
	}

	var content string
	if err := db.QueryRow("SELECT content FROM memories").Scan(&content); err != nil {
		t.Fatal(err)
	}
	if content != "新执行者写下的记忆" {
		t.Fatalf("记忆被旧执行者覆盖成 %q", content)
	}
	if n := countRows(t, db, "SELECT COUNT(*) FROM memories"); n != 1 {
		t.Fatalf("留下了 %d 条记忆，期望 1 条", n)
	}

	// 旧执行者失败后也不能把已经提交的 applied 降回 failed。
	if err := svc.repo.FailFormation(ctx, turn.ID, "old-owner"); err != nil {
		t.Fatalf("FailFormation 报错: %v", err)
	}
	_, _, formation := turnRow(t, db, "c-lease")
	if formation != model.FormationApplied {
		t.Fatalf("状态被降回 %q，期望保持 applied", formation)
	}
}

// 完整回复成功之后，迟到的失败、取消、旧执行者都不能覆盖它。
func TestCompletedReplySurvivesLateFailures(t *testing.T) {
	svc, fake, db := newTestService(t)
	ctx := context.Background()

	turn, err := svc.repo.EnsureUserMessage(ctx, "c-late", "先成功")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.repo.CompleteChatTurn(ctx, turn.ID, "完整的回复"); err != nil {
		t.Fatal(err)
	}

	late := []struct {
		name   string
		status string
		reason error
	}{
		{"迟到的失败", model.TurnStatusFailed, errors.New("旧尝试的模型报错")},
		{"迟到的取消", model.TurnStatusCanceled, context.Canceled},
	}

	for _, tc := range late {
		t.Run(tc.name, func(t *testing.T) {
			// 带部分内容的一次迟到写入。
			if err := svc.repo.FailChatTurn(ctx, turn.ID, tc.status, "迟到的一半"); err != nil {
				t.Fatalf("FailChatTurn 报错: %v", err)
			}
			// 不带内容的一次迟到写入。
			if err := svc.repo.FailChatTurn(ctx, turn.ID, tc.status, ""); err != nil {
				t.Fatalf("FailChatTurn 报错: %v", err)
			}

			_, status, _ := turnRow(t, db, "c-late")
			if status != model.TurnStatusOK {
				t.Fatalf("轮次状态被改成 %q", status)
			}
			if n := countRows(t, db, "SELECT COUNT(*) FROM chat_messages WHERE reply_to_id = ?", turn.ID); n != 1 {
				t.Fatalf("回复行变成了 %d 条", n)
			}
			reply, err := svc.repo.GetReply(ctx, turn.ID)
			if err != nil {
				t.Fatal(err)
			}
			if reply != "完整的回复" {
				t.Fatalf("回复被覆盖成 %q", reply)
			}
		})
	}

	// 迟到写入之后，读到的世界必须还是「这一轮成功了」：
	// 历史里这一问一答都在，重发也只重放已存回复。
	history, err := svc.ChatHistory(ctx, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 2 {
		t.Fatalf("历史里有 %d 条，期望 2 条（一问一答）", len(history))
	}
	if history[1].Content != "完整的回复" {
		t.Fatalf("历史里的回复是 %q", history[1].Content)
	}

	// 重放走的是已存回复，不会再去问模型。
	fake.agentReplies = nil
	fake.streamText = "不该被用上的新回复"
	var replayed strings.Builder
	if err := svc.ChatStream(ctx, "c-late", "先成功", noopRecall,
		func(d string) { replayed.WriteString(d) }); err != nil {
		t.Fatalf("重放失败: %v", err)
	}
	if replayed.String() != "完整的回复" {
		t.Fatalf("迟到写入之后重放拿到的是 %q", replayed.String())
	}

	if n := countRows(t, db, "SELECT COUNT(*) FROM chat_messages"); n != 2 {
		t.Fatalf("迟到写入加重放之后共有 %d 条消息，期望 2 条", n)
	}
}

// 成功之后的重试拿回的是同一条稳定回复，不会因为又调了一次模型而变化。
func TestRetryReturnsStableStoredReply(t *testing.T) {
	svc, fake, _ := newTestService(t)
	ctx := context.Background()

	fake.agentReplies = []agentReply{{content: ""}}
	fake.streamText = "稳定的回复"
	if err := svc.ChatStream(ctx, "c-stable", "你好", noopRecall, noopDelta); err != nil {
		t.Fatal(err)
	}

	fake.streamText = "换了内容的回复"
	var got strings.Builder
	if err := svc.ChatStream(ctx, "c-stable", "你好", noopRecall, func(d string) { got.WriteString(d) }); err != nil {
		t.Fatal(err)
	}
	if got.String() != "稳定的回复" {
		t.Fatalf("重试拿到的是 %q", got.String())
	}

	turn, err := svc.repo.FindTurnByClientID(ctx, "c-stable")
	if err != nil {
		t.Fatal(err)
	}
	reply, err := svc.repo.GetReply(ctx, turn.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reply != "稳定的回复" {
		t.Fatalf("落库的回复是 %q", reply)
	}
}

func TestConcurrentDuplicateClientIDCreatesOneMessage(t *testing.T) {
	svc, _, db := newTestService(t)

	const n = 8
	ids := make([]int64, n)
	errs := make([]error, n)

	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			turn, err := svc.repo.EnsureUserMessage(context.Background(), "c-race", "同一句话")
			if err != nil {
				errs[i] = err
				return
			}
			ids[i] = turn.ID
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("第 %d 个并发调用失败: %v", i, err)
		}
		if ids[i] != ids[0] {
			t.Fatalf("并发调用拿到了不同的轮次 id: %d vs %d", ids[i], ids[0])
		}
	}
	if c := countRows(t, db, "SELECT COUNT(*) FROM chat_messages WHERE client_id = 'c-race'"); c != 1 {
		t.Fatalf("并发重复投递创建了 %d 条用户消息，期望 1 条", c)
	}
}

func TestConcurrentFormationClaimHasSingleWinner(t *testing.T) {
	svc, _, db := newTestService(t)

	res, err := db.Exec(`
		INSERT INTO chat_messages (role, content, client_id, status, formation_status)
		VALUES ('user', '认领测试', 'c-claim', 'pending', 'pending')`)
	if err != nil {
		t.Fatal(err)
	}
	userID, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}

	const n = 8
	results := make([]bool, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			claimed, err := svc.repo.ClaimFormation(context.Background(), userID, fmt.Sprintf("owner-%d", i), 300)
			if err != nil {
				t.Errorf("第 %d 个并发认领失败: %v", i, err)
				return
			}
			results[i] = claimed
		}(i)
	}
	wg.Wait()

	winners := 0
	for _, ok := range results {
		if ok {
			winners++
		}
	}
	if winners != 1 {
		t.Fatalf("有 %d 个调用认领成功，期望恰好 1 个", winners)
	}
}

// 一轮只有一条助手回复，而且先成功的那次就是这一轮的稳定结果：
// 之后的重复完成不会把它改掉。
func TestCompleteChatTurnKeepsFirstSuccessfulReply(t *testing.T) {
	svc, _, db := newTestService(t)
	ctx := context.Background()

	turn, err := svc.repo.EnsureUserMessage(ctx, "c-upsert", "你好")
	if err != nil {
		t.Fatal(err)
	}

	for _, text := range []string{"第一版", "第二版"} {
		if err := svc.repo.CompleteChatTurn(ctx, turn.ID, text); err != nil {
			t.Fatalf("写入回复失败: %v", err)
		}
	}

	if n := countRows(t, db, "SELECT COUNT(*) FROM chat_messages WHERE reply_to_id = ?", turn.ID); n != 1 {
		t.Fatalf("留下 %d 条回复，期望 1 条", n)
	}
	reply, err := svc.repo.GetReply(ctx, turn.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reply != "第一版" {
		t.Fatalf("回复是 %q，期望保留第一次成功的结果", reply)
	}
}

// 半成品可以被完整回复升级，这是「partial 不是终态」的保证。
func TestCompleteChatTurnUpgradesPartialReply(t *testing.T) {
	svc, _, db := newTestService(t)
	ctx := context.Background()

	turn, err := svc.repo.EnsureUserMessage(ctx, "c-upgrade", "讲讲")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.repo.FailChatTurn(ctx, turn.ID, model.TurnStatusFailed, "说到一半"); err != nil {
		t.Fatal(err)
	}

	if err := svc.repo.CompleteChatTurn(ctx, turn.ID, "完整的回复"); err != nil {
		t.Fatal(err)
	}

	if n := countRows(t, db, "SELECT COUNT(*) FROM chat_messages WHERE reply_to_id = ?", turn.ID); n != 1 {
		t.Fatalf("留下 %d 条回复，期望 1 条", n)
	}
	reply, err := svc.repo.GetReply(ctx, turn.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reply != "完整的回复" {
		t.Fatalf("回复是 %q，期望被升级成完整内容", reply)
	}
	_, status, _ := turnRow(t, db, "c-upgrade")
	if status != model.TurnStatusOK {
		t.Fatalf("轮次状态是 %q，期望 ok", status)
	}
}

// 完整结束的轮次照常进历史。
func TestHistoryContainsFinishedTurns(t *testing.T) {
	svc, fake, _ := newTestService(t)
	fake.agentReplies = []agentReply{{content: ""}}
	fake.streamText = "好的"

	if err := svc.ChatStream(context.Background(), "c-ok", "成功的", noopRecall, noopDelta); err != nil {
		t.Fatal(err)
	}

	history, err := svc.ChatHistory(context.Background(), 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 2 {
		t.Fatalf("历史里有 %d 条，期望 2 条（一问一答）", len(history))
	}
	for _, m := range history {
		if m.Status != model.TurnStatusOK {
			t.Fatalf("完整轮次的状态是 %q: %+v", m.Status, m)
		}
	}
}

// 一个字都没流出去的失败轮次不进历史。
//
// 界面上留一条永远等不到回复的消息，只是在提醒用户「你失败了」；
// 他要的是重发，不是一条墓碑。
func TestHistoryHidesFailedTurnWithoutOutput(t *testing.T) {
	svc, fake, _ := newTestService(t)
	fake.agentReplies = []agentReply{{content: ""}}
	fake.streamErr = errors.New("连接被重置")

	if err := svc.ChatStream(context.Background(), "c-silent", "失败的", noopRecall, noopDelta); err == nil {
		t.Fatal("期望失败")
	}

	history, err := svc.ChatHistory(context.Background(), 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 0 {
		t.Fatalf("历史里有 %d 条，期望 0 条: %+v", len(history), history)
	}
}

// 流了一半断掉的轮次进历史：原用户消息和那条 partial 回复都在，
// 并且带回 status 与 client_id——刷新后既看得出断在哪，也能原地重试。
func TestHistoryShowsPartialReplyWithClientID(t *testing.T) {
	svc, fake, _ := newTestService(t)
	fake.agentReplies = []agentReply{{content: ""}}
	fake.streamText = "写到一半"
	fake.streamErr = errors.New("流被中断")

	if err := svc.ChatStream(context.Background(), "c-partial", "讲讲", noopRecall, noopDelta); err == nil {
		t.Fatal("期望失败")
	}

	history, err := svc.ChatHistory(context.Background(), 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 2 {
		t.Fatalf("历史里有 %d 条，期望 2 条（用户消息 + 断掉的回复）: %+v", len(history), history)
	}

	user, reply := history[0], history[1]
	if user.Role != "user" || user.Content != "讲讲" {
		t.Fatalf("第一条不是原用户消息: %+v", user)
	}
	if user.ClientID != "c-partial" {
		t.Fatalf("用户消息没有带回 client_id，刷新后无法原地重试: %+v", user)
	}
	if reply.Role != "assistant" || reply.Content != "写到一半" {
		t.Fatalf("第二条不是那条断掉的回复: %+v", reply)
	}
	if reply.Status != "partial" {
		t.Fatalf("断掉的回复状态是 %q，期望 partial", reply.Status)
	}
}

// 展示看得见 partial，送进模型的上下文不行。
//
// 一条没写完的回复被当成完整回答喂回去，模型会把它当作自己已经说过的话
// 承接下去，用户看到的前后文就串了。两条查询刻意分开，就是为了这件事。
func TestModelContextExcludesPartialReply(t *testing.T) {
	svc, fake, _ := newTestService(t)
	fake.agentReplies = []agentReply{{content: ""}}
	fake.streamText = "完整的回答"

	if err := svc.ChatStream(context.Background(), "c-good", "第一轮", noopRecall, noopDelta); err != nil {
		t.Fatal(err)
	}

	fake.streamText = "写到一半"
	fake.streamErr = errors.New("流被中断")
	if err := svc.ChatStream(context.Background(), "c-cut", "第二轮", noopRecall, noopDelta); err == nil {
		t.Fatal("期望失败")
	}

	history, err := svc.ChatHistory(context.Background(), 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 4 {
		t.Fatalf("展示侧有 %d 条，期望 4 条（完整一问一答 + 断掉的一问半答）: %+v", len(history), history)
	}

	contextMessages, err := svc.repo.ListChatMessages(context.Background(), 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(contextMessages) != 2 {
		t.Fatalf("上下文里有 %d 条，期望 2 条（只有完整那一轮）: %+v", len(contextMessages), contextMessages)
	}
	for _, m := range contextMessages {
		if strings.Contains(m.Content, "写到一半") || strings.Contains(m.Content, "第二轮") {
			t.Fatalf("断掉的轮次进了模型上下文: %+v", m)
		}
	}
}

// 无工具调用的一轮，模型一共被调用三次：一次 Formation 写入、一次 Agent 决策、一次流式生成。
// 这个数字是「重复生成」代价的度量：Agent 决策出的是完整答案，
// 流式生成又把它重写了一遍。优化方案在报告里说明，本轮不改。
func TestModelCallCountForPlainTurn(t *testing.T) {
	svc, fake, _ := newTestService(t)
	fake.agentReplies = []agentReply{{content: "决策阶段的完整答案"}}
	fake.streamText = "流式生成的答案"

	if err := svc.ChatStream(context.Background(), "c-count", "你好", noopRecall, noopDelta); err != nil {
		t.Fatal(err)
	}

	formation, agent, stream := fake.counts()
	if formation != 1 || agent != 1 || stream != 1 {
		t.Fatalf("模型调用次数是 formation=%d agent=%d stream=%d，期望各 1 次", formation, agent, stream)
	}
}

func TestParseRef(t *testing.T) {
	cases := []struct {
		ref    string
		prefix string
		wantID int64
		wantOK bool
	}{
		{"memory:12", "memory:", 12, true},
		{"chat:7", "chat:", 7, true},
		{"memory:0", "memory:", 0, false},
		{"memory:012", "memory:", 0, false},
		{"memory:", "memory:", 0, false},
		{"chat:12", "memory:", 0, false},
		{"memory:1a", "memory:", 0, false},
		{"memory:-1", "memory:", 0, false},
		{"", "memory:", 0, false},
	}

	for _, tc := range cases {
		id, ok := parseRef(tc.ref, tc.prefix)
		if ok != tc.wantOK || id != tc.wantID {
			t.Fatalf("parseRef(%q, %q) = (%d, %v)，期望 (%d, %v)",
				tc.ref, tc.prefix, id, ok, tc.wantID, tc.wantOK)
		}
	}
}

func TestBuildResponseSystemPromptWithoutFormation(t *testing.T) {
	got := buildResponseSystemPrompt(nil)
	if got != responseSystemPrompt {
		t.Fatal("没有 Formation 结果时不应附加任何理解内容")
	}
}

func TestBuildResponseSystemPromptIncludesGuidance(t *testing.T) {
	got := buildResponseSystemPrompt(&llm.FormationResult{
		Understanding: llm.Understanding{Summary: "用户在说项目进度"},
		ResponseGuidance: llm.ResponseGuidance{
			UserMood:        []string{"疲惫"},
			ResponseContext: []string{"他在做 Memora"},
		},
	})

	for _, want := range []string{"用户在说项目进度", "疲惫", "他在做 Memora"} {
		if !strings.Contains(got, want) {
			t.Fatalf("系统提示里缺少 %q", want)
		}
	}
}

func TestBuildFormationInputLabelsRefs(t *testing.T) {
	got := buildFormationInput(42, "新消息", []model.ChatMessage{
		{ID: 41, Role: "user", Content: "上一句"},
	}, []model.Memory{
		{ID: 7, Content: "已有记忆", Category: "goal"},
	})

	for _, want := range []string{"chat:42", "chat:41", "memory:7", "goal"} {
		if !strings.Contains(got, want) {
			t.Fatalf("Formation 输入里缺少 %q:\n%s", want, got)
		}
	}
}

func TestChatHistoryLimits(t *testing.T) {
	svc, _, db := newTestService(t)
	for i := 0; i < 5; i++ {
		if _, err := db.Exec(`
			INSERT INTO chat_messages (role, content, client_id, status)
			VALUES ('user', ?, ?, 'ok')`, fmt.Sprintf("第 %d 条", i), fmt.Sprintf("c-h-%d", i)); err != nil {
			t.Fatal(err)
		}
	}

	got, err := svc.ChatHistory(context.Background(), 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("返回 %d 条，期望 3 条", len(got))
	}
	if got[0].Content != "第 2 条" || got[2].Content != "第 4 条" {
		t.Fatalf("返回的不是最近三条: %+v", got)
	}

	if _, err := svc.ChatHistory(context.Background(), 0); err != nil {
		t.Fatalf("limit=0 应当回落到默认值: %v", err)
	}
}
