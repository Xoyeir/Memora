package service

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Xoyeir/Memora/internal/config"
	"github.com/Xoyeir/Memora/internal/llm"
	"github.com/Xoyeir/Memora/internal/model"
	"github.com/Xoyeir/Memora/internal/testsupport"
)

// 真实模型核查，默认跳过（MEMORA_REALCHECK=1 MEMORA_TEST_DSN=... go test ./internal/service -run TestReal -v -count=1）；模型输出不稳定，判断是有没有做对而非逐字相同；轨迹数据全为本文件编造。

// 核查用的合成数据，全部写在测试里。
const (
	realcheckSeedMessage = "记一下：十月一日、十月三日、十月五日各跑了一次步，十月七日没去，十月十日打算去爬山。"
	realcheckToday       = "2026-10-09"
)

type realcheckRecord struct {
	id           int64
	kind, state  string
	content      string
	mentionedOn  string
	occurredOn   string
	precision    string
	dateText     string
	evidenceText string
}

var realcheckRecords = []realcheckRecord{
	{1, "activity", "done", "十月一日跑了五公里", "2026-10-01", "2026-10-01", "day", "", "十月一日跑步"},
	{2, "activity", "done", "十月三日跑了三公里", "2026-10-03", "2026-10-03", "day", "", "十月三日跑步"},
	{3, "activity", "done", "十月五日跑了八公里", "2026-10-05", "2026-10-05", "day", "", "十月五日跑步"},
	{4, "activity", "negated", "十月七日没有去跑步", "2026-10-07", "2026-10-07", "day", "", "十月七日没去"},
	{5, "activity", "planned", "打算十月十日去爬山", "2026-10-09", "2026-10-10", "day", "", "十月十日打算去爬山"},
	{6, "activity", "done", "三年前参加过校运会", "2026-10-05", "", "unknown", "三年前", "三年前参加过校运会"},
}

// tracingLLM 包住真实客户端，记录每次请求与工具调用，只记录不改变行为。
type tracingLLM struct {
	LLMClient

	mu        sync.Mutex
	exchanges []agentExchange
}

type agentExchange struct {
	request []llm.Message
	calls   []llm.ToolCall
	content string
}

func (c *tracingLLM) ChatWithTools(ctx context.Context, messages []llm.Message, tools []llm.Tool) (string, []llm.ToolCall, error) {
	content, calls, err := c.LLMClient.ChatWithTools(ctx, messages, tools)

	c.mu.Lock()
	c.exchanges = append(c.exchanges, agentExchange{
		request: append([]llm.Message(nil), messages...),
		calls:   calls,
		content: content,
	})
	c.mu.Unlock()

	return content, calls, err
}

func (c *tracingLLM) calledTools() []string {
	c.mu.Lock()
	defer c.mu.Unlock()

	var names []string
	for _, ex := range c.exchanges {
		for _, call := range ex.calls {
			names = append(names, call.Name)
		}
	}
	return names
}

func (c *tracingLLM) reset() {
	c.mu.Lock()
	c.exchanges = nil
	c.mu.Unlock()
}

func (c *tracingLLM) dump(t *testing.T) {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()

	if len(c.exchanges) == 0 {
		t.Log("  模型没有发起任何 agent 调用")
		return
	}
	for i, ex := range c.exchanges {
		t.Logf("  -- 第 %d 次请求（%d 条消息）", i+1, len(ex.request))
		if len(ex.calls) == 0 {
			t.Logf("     没有工具调用，直接给出了 %d 字的正文", len([]rune(ex.content)))
			continue
		}
		for _, call := range ex.calls {
			t.Logf("     调用 %s 参数=%s", call.Name, call.Arguments)
		}
		for _, m := range ex.request {
			if m.Role == "tool" {
				t.Logf("     返回：%s", truncate(m.Content, 700))
			}
		}
	}
}

func openRealcheckService(t *testing.T, client LLMClient) (*Service, *sql.DB) {
	t.Helper()

	if strings.TrimSpace(os.Getenv(testsupport.EnvDSN)) == "" {
		t.Skipf("未设置 %s，跳过需要真实数据库的核查", testsupport.EnvDSN)
	}
	db, repo := testsupport.Open(t)

	clock, err := NewClock("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	return NewService(repo, client, clock), db
}

func seedRealcheckData(t *testing.T, db *sql.DB) {
	t.Helper()

	if _, err := db.Exec(`INSERT INTO chat_messages
			(id, role, content, client_id, status, formation_status)
		VALUES (1, 'user', ?, 'seed', 'ok', 'applied')`, realcheckSeedMessage); err != nil {
		t.Fatal(err)
	}

	for _, r := range realcheckRecords {
		if _, err := db.Exec(`INSERT INTO life_records
				(id, kind, state, basis, subject, content, chat_message_id, evidence_quote,
				 mentioned_on, occurred_on, date_precision, date_text, status)
			VALUES (?, ?, ?, 'explicit', 'user', ?, 1, ?, ?, ?, ?, ?, 'active')`,
			r.id, r.kind, r.state, r.content, r.evidenceText,
			r.mentionedOn, nullOrNil(r.occurredOn), r.precision, nullOrNil(r.dateText)); err != nil {
			t.Fatal(err)
		}
	}
}

func realcheckClient(t *testing.T) *tracingLLM {
	t.Helper()
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("读取配置失败: %v", err)
	}
	return &tracingLLM{LLMClient: llm.NewClient(cfg)}
}

// 场景一：需要查记录才能回答的问题，模型应当调用召回工具，并且只依据记录说话。
func TestRealAgentRecallsLifeRecords(t *testing.T) {
	if os.Getenv("MEMORA_REALCHECK") != "1" {
		t.Skip("需要 MEMORA_REALCHECK=1 才会调用真实模型")
	}
	client := realcheckClient(t)
	svc, db := openRealcheckService(t, client)
	seedRealcheckData(t, db)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	var reply string
	err := svc.ChatStream(ctx, "real-1", "我最近跑步的情况怎么样？", noopRecall, func(d string) { reply += d })
	if err != nil {
		t.Fatalf("聊天失败: %v", err)
	}

	t.Logf("\n=== 场景一 | 需要工具\n问题：我最近跑步的情况怎么样？")
	client.dump(t)
	t.Logf("  最终回答：%s", reply)

	tools := client.calledTools()
	if !containsTool(tools, "recall_life_records") {
		t.Errorf("问自己的运动记录却没有调用召回工具，实际调用了 %v", tools)
	}
	// 有记录的那几天应当被说出来；明确否认的那天不能被说成跑了。
	for _, want := range []string{"1", "3", "5"} {
		if !strings.Contains(reply, want+"日") && !strings.Contains(reply, want+"号") {
			t.Logf("  回答里没有提到十月 %s 日，人工复核用", want)
		}
	}
	if strings.Contains(reply, "七号") || strings.Contains(reply, "7日") {
		t.Logf("  回答里出现了十月七日，需要人工确认它有没有把「没去」说成「跑了」")
	}
}

// 场景二：不需要记录就能完成的任务不该查库；例句故意带库中真有的词，用来暴露纯凭关键词查库。
func TestRealAgentSkipsToolsWhenUnnecessary(t *testing.T) {
	if os.Getenv("MEMORA_REALCHECK") != "1" {
		t.Skip("需要 MEMORA_REALCHECK=1 才会调用真实模型")
	}

	cases := []struct {
		name    string
		message string
	}{
		{"翻译", "帮我把这句话翻成英文：我明天要去跑步。"},
		{"造句", "用「跑步」这个词造一个句子。"},
		{"润色", "把「今天很累」润色得更书面一些。"},
		{"算数", "帮我算一下 128 乘以 7 等于多少。"},
		{"写代码", "写一个 Go 函数，判断一个字符串是不是回文。"},
	}

	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			client := realcheckClient(t)
			svc, db := openRealcheckService(t, client)
			seedRealcheckData(t, db)

			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			defer cancel()

			var reply string
			err := svc.ChatStream(ctx, fmt.Sprintf("real-2-%d", i), c.message, noopRecall,
				func(d string) { reply += d })
			if err != nil {
				t.Fatalf("聊天失败: %v", err)
			}

			t.Logf("\n=== 场景二 | 不需要工具（%s）\n问题：%s", c.name, c.message)
			client.dump(t)
			t.Logf("  最终回答：%s", reply)

			if tools := client.calledTools(); len(tools) > 0 {
				t.Errorf("这是一个不需要用户历史的自包含任务，却调用了工具 %v。"+
					"回答本身可能仍然是对的，但「当前输入已经足够时不要查历史」没有做到。", tools)
			}
		})
	}
}

// 场景五：必须读历史，但问题里没有出现记录中的关键词。
func TestRealAgentReadsHistoryWithoutKeyword(t *testing.T) {
	if os.Getenv("MEMORA_REALCHECK") != "1" {
		t.Skip("需要 MEMORA_REALCHECK=1 才会调用真实模型")
	}
	client := realcheckClient(t)
	svc, db := openRealcheckService(t, client)
	seedRealcheckData(t, db)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	var reply string
	err := svc.ChatStream(ctx, "real-5", "我最近都在忙些什么？", noopRecall, func(d string) { reply += d })
	if err != nil {
		t.Fatalf("聊天失败: %v", err)
	}

	t.Logf("\n=== 场景五 | 必须查历史，但问题里没有关键词\n问题：我最近都在忙些什么？")
	client.dump(t)
	t.Logf("  最终回答：%s", reply)

	if tools := client.calledTools(); !containsTool(tools, "recall_life_records") {
		t.Errorf("这个问题只能靠记录回答，却没有查记录，实际调用了 %v", tools)
	}
	if !containsAny(reply, "跑", "爬山", "运动") {
		t.Errorf("回答里没有任何记录中的内容，可能是照着印象答的：%s", reply)
	}
}

// 场景三：问一件没有任何记录的事，模型应当去查，查不到就如实说没有记录。
func TestRealAgentSaysNoRecordWhenMissing(t *testing.T) {
	if os.Getenv("MEMORA_REALCHECK") != "1" {
		t.Skip("需要 MEMORA_REALCHECK=1 才会调用真实模型")
	}
	client := realcheckClient(t)
	svc, db := openRealcheckService(t, client)
	seedRealcheckData(t, db)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	var reply string
	err := svc.ChatStream(ctx, "real-3", "我上周去游泳了吗？", noopRecall, func(d string) { reply += d })
	if err != nil {
		t.Fatalf("聊天失败: %v", err)
	}

	t.Logf("\n=== 场景三 | 查不到时的说法\n问题：我上周去游泳了吗？")
	client.dump(t)
	t.Logf("  最终回答：%s", reply)

	if tools := client.calledTools(); !containsTool(tools, "recall_life_records") {
		t.Errorf("问一件具体的事却没有查记录，实际调用了 %v", tools)
	}
	// 「没记录」不等于「没发生」：查不到不能被说成没发生过。
	if !containsAny(reply, "没有记录", "没记", "查不到", "没有相关", "不确定", "没有找到") {
		t.Errorf("查不到时没有说明是缺少记录，回答是：%s", reply)
	}
}

// 场景四：候选里放着那条久远的旧记忆，模型自己会不会作出正确的更新。
func TestRealFormationSupersedesOldMemory(t *testing.T) {
	if os.Getenv("MEMORA_REALCHECK") != "1" {
		t.Skip("需要 MEMORA_REALCHECK=1 才会调用真实模型")
	}
	client := realcheckClient(t)

	candidates := []model.Memory{
		{ID: 1, Content: "用户打算每周跑步三次", Status: model.LifeStatusActive},
	}
	for id := int64(21); id <= 40; id++ {
		candidates = append(candidates, model.Memory{
			ID: id, Content: fmt.Sprintf("琐事条目 %d", id), Status: model.LifeStatusActive,
		})
	}

	const msg = "我改主意了，跑步改成每周一次吧"
	turnID := int64(900)
	input := buildFormationInput(turnID, msg, nil, candidates, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	res, err := client.Formation(ctx, input)
	if err != nil {
		t.Fatalf("调用失败: %v", err)
	}
	ops := planMemoryOps(res.MemoryActions, turnID, candidates)

	t.Logf("\n=== 场景四 | 模型的更新判断\n消息：%s", msg)
	for _, action := range res.MemoryActions {
		t.Logf("  模型输出：action=%s ref=%s 新内容=%v 依据=%s",
			action.Action, action.Ref, action.Candidate, action.Basis)
	}
	if len(ops) == 0 {
		t.Fatalf("模型没有产生任何可执行的操作（候选里有 memory:1）")
	}
	for _, op := range ops {
		t.Logf("  校验后：kind=%s 目标=%d 内容=%q 分类=%q", op.Kind, op.MemoryID, op.Content, op.Category)
	}

	superseded := false
	for _, op := range ops {
		if op.Kind == model.OpSupersede && op.MemoryID == 1 {
			superseded = true
		}
	}
	if !superseded {
		t.Errorf("本轮明确改掉了跑步频率，模型却没有把 memory:1 标成 supersede")
	}
}

func containsTool(names []string, want string) bool {
	for _, n := range names {
		if n == want {
			return true
		}
	}
	return false
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
