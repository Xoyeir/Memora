package service

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/Xoyeir/Memora/internal/llm"
	"github.com/Xoyeir/Memora/internal/model"
	"github.com/Xoyeir/Memora/internal/repository"
)

func seedMemory(t *testing.T, db *sql.DB, content, category string) (memoryID, chatID int64) {
	t.Helper()
	chatID = seedTurn(t, db, content, "2026-10-05 12:00:00")
	res, err := db.Exec("INSERT INTO memories (content, category, status) VALUES (?, ?, 'active')",
		content, category)
	if err != nil {
		t.Fatalf("写入记忆失败: %v", err)
	}
	memoryID, err = res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO memory_sources (memory_id, chat_message_id) VALUES (?, ?)",
		memoryID, chatID); err != nil {
		t.Fatalf("写入记忆来源失败: %v", err)
	}
	return memoryID, chatID
}

func listMemories(t *testing.T, svc *Service, status string, limit int) *MemoryList {
	t.Helper()
	list, err := svc.ListMemoriesForManagement(context.Background(), status, limit)
	if err != nil {
		t.Fatalf("列出记忆失败: %v", err)
	}
	return list
}

// 默认只看仍然有效的记忆；失效的那些要显式查才看得到，但一直都在库里。
func TestMemoryListDefaultsToActive(t *testing.T) {
	svc, _, db := newTestService(t)

	activeID, _ := seedMemory(t, db, "用户在做 Memora", "project")
	forgottenID, _ := seedMemory(t, db, "用户住在长沙", "profile")
	if _, err := svc.ForgetMemory(context.Background(), forgottenID); err != nil {
		t.Fatalf("忘记失败: %v", err)
	}

	def := listMemories(t, svc, "", 0)
	if len(def.Memories) != 1 || def.Memories[0].ID != activeID {
		t.Fatalf("默认列表是 %+v", def.Memories)
	}
	if def.Status != model.MemoryStatusActive {
		t.Errorf("默认状态是 %q", def.Status)
	}

	all := listMemories(t, svc, "all", 0)
	if len(all.Memories) != 2 {
		t.Fatalf("all 应该看到两条，实际 %d 条", len(all.Memories))
	}

	forgotten := listMemories(t, svc, model.MemoryStatusForgotten, 0)
	if len(forgotten.Memories) != 1 || forgotten.Memories[0].ID != forgottenID {
		t.Fatalf("按 forgotten 过滤的结果是 %+v", forgotten.Memories)
	}

	if _, err := svc.ListMemoriesForManagement(context.Background(), "不存在的状态", 0); !errors.Is(err, ErrInvalidMemoryParam) {
		t.Errorf("未知状态返回 %v", err)
	}
}

// 超过条数上限时必须说明还有更多，不能假装已经展示全部。
func TestMemoryListMarksTruncation(t *testing.T) {
	svc, _, db := newTestService(t)
	for i := 0; i < 5; i++ {
		seedMemory(t, db, "第 "+string(rune('一'+i))+" 条记忆", "profile")
	}

	list := listMemories(t, svc, "", 3)
	if len(list.Memories) != 3 {
		t.Fatalf("返回了 %d 条，期望 3 条", len(list.Memories))
	}
	if !list.Truncated {
		t.Error("还有更多条却没有标出来")
	}
	if full := listMemories(t, svc, "", 50); full.Truncated {
		t.Error("全都在里面却标了截断")
	}
}

// 自动形成的记忆，来源就是那条聊天原文。
func TestMemorySourcesOfAutoMemory(t *testing.T) {
	svc, _, db := newTestService(t)
	id, _ := seedMemory(t, db, "用户在做 Memora", "project")

	view, err := svc.MemorySources(context.Background(), id)
	if err != nil {
		t.Fatalf("读取来源失败: %v", err)
	}
	if len(view.Sources) != 1 || view.Sources[0].SourceType != model.MemorySourceChat {
		t.Fatalf("来源是 %+v", view.Sources)
	}
	if view.Memory.Origin != model.MemoryOriginAuto {
		t.Errorf("来源类型是 %q，期望 %q", view.Memory.Origin, model.MemoryOriginAuto)
	}

	if _, err := svc.MemorySources(context.Background(), 99999); !errors.Is(err, repository.ErrMemoryNotFound) {
		t.Errorf("不存在时返回 %v", err)
	}
}

// 用户改写之后：新内容以自己的操作为依据，旧聊天原文不再给它当证明。
func TestCorrectMemoryKeepsEvidenceHonest(t *testing.T) {
	svc, _, db := newTestService(t)
	id, chatID := seedMemory(t, db, "用户养了一只猫", "profile")

	updated, err := svc.CorrectMemory(context.Background(), id, "用户养了一只狗")
	if err != nil {
		t.Fatalf("纠正失败: %v", err)
	}
	if updated.Content != "用户养了一只狗" || updated.Origin != model.MemoryOriginUserCorrected {
		t.Fatalf("纠正后的记忆是 %+v", updated)
	}
	if updated.Category != "profile" {
		t.Errorf("纠正不该丢掉分类，实际 %q", updated.Category)
	}

	// 旧记忆被标为已纠正并指向新记录，不被就地改写。
	old, err := svc.MemorySources(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if old.Memory.Status != model.MemoryStatusCorrected {
		t.Errorf("旧记忆的状态是 %q", old.Memory.Status)
	}
	if old.Memory.Content != "用户养了一只猫" {
		t.Errorf("旧记忆的内容被改成了 %q", old.Memory.Content)
	}
	if old.Memory.SupersededByID == nil || *old.Memory.SupersededByID != updated.ID {
		t.Errorf("旧记忆没有指向新的那条: %+v", old.Memory.SupersededByID)
	}

	// 新记忆的来源是这次用户操作，不是那条旧聊天——旧原文只证明了旧内容。
	newSources, err := svc.MemorySources(context.Background(), updated.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(newSources.Sources) != 1 {
		t.Fatalf("新记忆的来源有 %d 条，期望只有那条纠正记录", len(newSources.Sources))
	}
	src := newSources.Sources[0]
	if src.SourceType != model.MemorySourceCorrection {
		t.Fatalf("新记忆的来源类型是 %q，期望 %q", src.SourceType, model.MemorySourceCorrection)
	}
	if src.Content != "用户养了一只猫" {
		t.Errorf("纠正记录里记的原内容是 %q", src.Content)
	}
	if src.Action != model.CorrectionActionCorrect {
		t.Errorf("纠正记录的 action 是 %q，期望 %q", src.Action, model.CorrectionActionCorrect)
	}
	// 依据要同时返回用户新写的那句和被换掉的旧原话，读的人才能分清哪句算数。
	if src.NewContent != "用户养了一只狗" {
		t.Errorf("纠正记录里用户自己写的那句是 %q", src.NewContent)
	}
	if src.CreatedAt.IsZero() {
		t.Error("纠正记录没有带上时间")
	}
	if n := countRows(t, db,
		"SELECT COUNT(*) FROM memory_sources WHERE memory_id = ? AND chat_message_id = ?",
		updated.ID, chatID); n != 0 {
		t.Error("新记忆被挂上了旧聊天原文当来源")
	}

	// Agent 那条路径读的是同一个入口，所以来源工具也得能拿到用户自己写的那句和时间。
	seen := map[int64]bool{updated.ID: true}
	out, _ := svc.executeTool(context.Background(), llm.ToolCall{
		ID: "t1", Name: "get_memory_source",
		Arguments: `{"memory_id":` + strconv.FormatInt(updated.ID, 10) + `}`,
	}, seen, "2026-10-06")
	for _, want := range []string{
		`"action":"correct"`,
		`"new_content":"用户养了一只狗"`,
		`"content":"用户养了一只猫"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("来源工具的输出里没有 %s: %s", want, out)
		}
	}
	if strings.Contains(out, `"created_at":"0001-01-01`) {
		t.Errorf("来源工具没带上纠正的时间: %s", out)
	}

	// 对已经失效的记忆再纠正一次要明确拒绝，不能悄悄改掉一条已经作废的记录。
	if _, err := svc.CorrectMemory(context.Background(), id, "再改一次"); !errors.Is(err, repository.ErrMemoryNotActive) {
		t.Errorf("再次纠正返回 %v，期望 ErrMemoryNotActive", err)
	}
	if _, err := svc.CorrectMemory(context.Background(), updated.ID, "   "); !errors.Is(err, ErrInvalidMemoryParam) {
		t.Errorf("空内容返回 %v，期望 ErrInvalidMemoryParam", err)
	}
}

// 忘记之后不再进入候选、召回和来源工具，但原始聊天和其他记录都还在。
func TestForgetMemoryRemovesFromRecall(t *testing.T) {
	svc, _, db := newTestService(t)

	keepID, _ := seedMemory(t, db, "用户在做 Memora 项目", "project")
	goneID, chatID := seedMemory(t, db, "用户养了一只猫叫豆豆", "profile")

	if _, err := svc.ForgetMemory(context.Background(), goneID); err != nil {
		t.Fatalf("忘记失败: %v", err)
	}

	found, err := svc.repo.SearchMemories(context.Background(), "豆豆", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 0 {
		t.Fatalf("忘记之后仍然召回到了 %+v", found)
	}

	// Formation 的候选里也没有它，模型没有机会再把它强化回来。
	refs, err := svc.formationMemories(context.Background(),
		&model.ChatTurn{Content: "猫 豆豆"})
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range refs {
		if m.ID == goneID {
			t.Fatalf("忘记的 %d 仍然进入了 Formation 候选", goneID)
		}
	}
	if len(refs) == 0 || refs[0].ID != keepID {
		t.Fatalf("候选里应该还有那条有效记忆，实际 %+v", refs)
	}

	if n := countRows(t, db, "SELECT COUNT(*) FROM chat_messages WHERE id = ?", chatID); n != 1 {
		t.Error("忘记记忆连带删掉了原始聊天")
	}
	if n := countRows(t, db, "SELECT COUNT(*) FROM memories"); n != 2 {
		t.Error("忘记记忆删掉了库里的记录，应该只改状态")
	}

	// 忘记的原文本身还能看（内容仍在库里），只是不再被用来召回。
	view, err := svc.MemorySources(context.Background(), goneID)
	if err != nil {
		t.Fatalf("忘记之后仍然应该能查看这条记录: %v", err)
	}
	if view.Memory.Status != model.MemoryStatusForgotten {
		t.Errorf("状态是 %q", view.Memory.Status)
	}

	// 重复忘记是幂等的，不再产生第二笔操作记录。
	before := countRows(t, db, "SELECT COUNT(*) FROM memory_corrections")
	if _, err := svc.ForgetMemory(context.Background(), goneID); err != nil {
		t.Fatalf("重复忘记报错: %v", err)
	}
	if after := countRows(t, db, "SELECT COUNT(*) FROM memory_corrections"); after != before {
		t.Errorf("重复忘记又记了一笔：%d → %d", before, after)
	}

	// 忘记的记录不会再被 Agent 的来源工具读到：它不在本轮召回的集合里。
	seen := map[int64]bool{}
	out, _ := svc.executeTool(context.Background(), llm.ToolCall{
		ID: "t1", Name: "get_memory_source",
		Arguments: `{"memory_id":` + strconv.FormatInt(goneID, 10) + `}`,
	}, seen, "2026-10-05")
	if !strings.Contains(out, "未在本轮召回") {
		t.Errorf("来源工具读到了本轮没有召回过的记忆: %s", out)
	}
}

// 用户纠正或忘记一条记忆时，正在并发执行的那一轮 Formation 不能把它重新激活。
func TestFormationSkipsMemoriesTheUserJustChanged(t *testing.T) {
	svc, _, db := newTestService(t)

	chatID := seedTurn(t, db, "我不养猫了", "2026-10-06 12:00:00")
	res, err := db.Exec("INSERT INTO memories (content, category, status) VALUES (?, ?, 'active')",
		"用户养了一只猫", "profile")
	if err != nil {
		t.Fatal(err)
	}
	memoryID, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}

	ref := "memory:" + strconv.FormatInt(memoryID, 10)
	chatRef := "chat:" + strconv.FormatInt(chatID, 10)
	actions := []llm.MemoryAction{
		{Action: "REINFORCE", Ref: ref, Basis: "user_stated", EvidenceRefs: []string{chatRef}},
		{Action: "SUPERSEDE", Ref: ref,
			Candidate: &llm.MemoryCandidate{Content: "用户不养猫了", Category: "profile"},
			Basis:     "user_stated"},
	}

	// 校验阶段已经看过这条记忆，之后用户才把它忘记——模拟两者并发。
	known := []model.Memory{{ID: memoryID, Content: "用户养了一只猫"}}
	if _, err := svc.ForgetMemory(context.Background(), memoryID); err != nil {
		t.Fatalf("忘记失败: %v", err)
	}

	// 这一轮的 Formation 已经认领在跑，租约还没过期。
	if _, err := db.Exec(`UPDATE chat_messages
		SET formation_status = 'running', formation_owner = 'owner-x' WHERE id = ?`, chatID); err != nil {
		t.Fatal(err)
	}

	ops := planMemoryOps(actions, chatID, known)
	if len(ops) != 2 {
		t.Fatalf("两条操作都应该通过校验，实际 %d 条", len(ops))
	}
	if err := svc.repo.ApplyFormation(context.Background(), chatID, "owner-x", ops, nil); err != nil {
		t.Fatalf("整批写入失败: %v", err)
	}

	var status string
	if err := db.QueryRow("SELECT status FROM memories WHERE id = ?", memoryID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != model.MemoryStatusForgotten {
		t.Errorf("记忆状态变成了 %q，期望仍然是 %q", status, model.MemoryStatusForgotten)
	}
	if n := countRows(t, db, "SELECT COUNT(*) FROM memory_sources WHERE memory_id = ?", memoryID); n != 0 {
		t.Error("被忘记的记忆仍然被加上了新的证据")
	}
	if n := countRows(t, db, "SELECT COUNT(*) FROM memories"); n != 1 {
		t.Error("基于已失效记忆生成了新的记忆条目")
	}
}

// Formation 引用了已经不存在的记忆时跳过，而不是让整批回滚。
func TestFormationSkipsMissingMemory(t *testing.T) {
	svc, _, db := newTestService(t)
	chatID := seedTurn(t, db, "随便说说", "2026-10-06 12:00:00")
	if _, err := db.Exec(`UPDATE chat_messages
		SET formation_status = 'running', formation_owner = 'owner-y' WHERE id = ?`, chatID); err != nil {
		t.Fatal(err)
	}

	ops := []model.MemoryOp{
		{Kind: model.OpReinforce, MemoryID: 99999, ChatMessageID: chatID},
		{Kind: model.OpForm, Content: "用户在做 Memora", Category: "project", ChatMessageID: chatID},
	}
	if err := svc.repo.ApplyFormation(context.Background(), chatID, "owner-y", ops, nil); err != nil {
		t.Fatalf("缺少目标不该让整批失败: %v", err)
	}
	if n := countRows(t, db, "SELECT COUNT(*) FROM memories"); n != 1 {
		t.Errorf("同一批里合法的操作也应该写成，实际 %d 条", n)
	}
}
