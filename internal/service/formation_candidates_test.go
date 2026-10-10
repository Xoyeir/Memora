package service

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/Xoyeir/Memora/internal/llm"
)

// 久远但相关的记忆必须进 Formation 输入、无关的不能塞进去；模型是否会替换由真实模型核查覆盖。
func TestFormationSeesOldRelatedMemory(t *testing.T) {
	svc, fake, db := newTestService(t)
	ctx := context.Background()

	const (
		oldRelatedID   = 1
		oldUnrelatedID = 2
		newestSeededID = 60
	)

	if _, err := db.Exec("INSERT INTO memories (id, content, status) VALUES (?, ?, 'active')",
		oldRelatedID, "用户打算每周跑步三次"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO memories (id, content, status) VALUES (?, ?, 'active')",
		oldUnrelatedID, "用户小时候住在南方"); err != nil {
		t.Fatal(err)
	}
	// 灌入填充记忆，把上面两条挤出「最近 12 条」窗口。
	for id := int64(21); id <= newestSeededID; id++ {
		if _, err := db.Exec("INSERT INTO memories (id, content, status) VALUES (?, ?, 'active')",
			id, fmt.Sprintf("琐事条目 %d", id)); err != nil {
			t.Fatal(err)
		}
	}

	const msg = "我改主意了，跑步改成每周一次吧"
	fake.agentReplies = []agentReply{{content: ""}}
	fake.streamText = "好。"
	fake.formationResult = &llm.FormationResult{
		MemoryActions: []llm.MemoryAction{{
			Action:    "SUPERSEDE",
			Ref:       fmt.Sprintf("memory:%d", oldRelatedID),
			Candidate: &llm.MemoryCandidate{Content: "用户改成每周跑步一次", Category: "habit"},
		}},
	}

	if err := svc.ChatStream(ctx, "c-cand", msg, noopRecall, noopDelta); err != nil {
		t.Fatalf("ChatStream 失败: %v", err)
	}

	input := fake.formationInput
	if !strings.Contains(input, fmt.Sprintf("memory:%d ", oldRelatedID)) {
		t.Fatalf("本轮消息与 memory:%d 相关，它却没有进 Formation 输入：\n%s", oldRelatedID, input)
	}
	if strings.Contains(input, fmt.Sprintf("memory:%d ", oldUnrelatedID)) {
		t.Fatalf("与本轮无关的 memory:%d 也进了输入", oldUnrelatedID)
	}

	if n := countRows(t, db, "SELECT COUNT(*) FROM memories WHERE status = 'superseded' AND id = ?",
		oldRelatedID); n != 1 {
		t.Fatalf("旧记忆没有被替换（命中 %d 次），说明它没进入候选", n)
	}
	if n := countRows(t, db,
		"SELECT COUNT(*) FROM memories WHERE status = 'active' AND content = '用户改成每周跑步一次'"); n != 1 {
		t.Fatalf("新事实没有落库（命中 %d 次）", n)
	}

	// 输入有条数上限，不会把整库都塞进去。
	if n := countRows(t, db, "SELECT COUNT(*) FROM memories"); n != 43 {
		t.Fatalf("评测数据本身不对：库里 %d 条记忆", n)
	}
	if lines := countMemoryRefs(input); lines > recentMemoryRefs+relatedMemoryRefs {
		t.Fatalf("送进 Formation 的记忆有 %d 条，超过上限 %d", lines, recentMemoryRefs+relatedMemoryRefs)
	}
}

func countMemoryRefs(input string) int {
	n := 0
	for _, line := range strings.Split(input, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "memory:") {
			n++
		}
	}
	return n
}
