package repository_test

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/Xoyeir/Memora/internal/model"
	"github.com/Xoyeir/Memora/internal/testsupport"
)

// 检索评测：合成固定数据、编号写死，不依赖真实记录（MEMORA_TEST_DSN=... go test ./internal/repository -run TestSearchMemoriesEval -v -count=1）。

// Hit@K/Recall@K 的 K，评测固定值，与召回工具默认返回条数无关。
const evalK = 5

type evalMemory struct {
	id      int64
	content string
	status  string
}

// 1~12 有内容，13~52 无关填充，53~58 只命中查询一部分的干扰项。
var evalMemories = func() []evalMemory {
	list := []evalMemory{
		{1, "用户喜欢在周末跑步，一次大概五公里", model.LifeStatusActive},
		{2, "用户养了一只猫，名字叫豆豆", model.LifeStatusActive},
		{3, "用户正在学习 Go 语言，准备写一个小服务", model.LifeStatusActive},
		{4, "用户的英语是 CET-6 水平", model.LifeStatusActive},
		{5, "用户想在今年通过软考中级", model.LifeStatusActive},
		{6, "用户习惯早上六点半起床", model.LifeStatusActive},
		{7, "用户上一份实习做的是 Python 后端", model.LifeStatusActive},
		{8, "用户不喜欢吃香菜", model.LifeStatusActive},
		{9, "用户的目标是拿到大厂 offer", model.LifeStatusActive},
		{10, "用户已经改用 VS Code 写代码", model.LifeStatusActive},
		{11, "用户以前用 PyCharm 写代码", "superseded"},
		{12, "用户每天喝两杯咖啡", model.LifeStatusActive},
	}
	for i := int64(13); i <= 52; i++ {
		list = append(list, evalMemory{i, fmt.Sprintf("琐事条目 %d", i), model.LifeStatusActive})
	}
	return append(list, []evalMemory{
		{53, "用户买了一双跑步鞋", model.LifeStatusActive},
		{54, "用户问过周末要不要加班", model.LifeStatusActive},
		{55, "用户的跑步手表没电了", model.LifeStatusActive},
		{56, "用户周末想去爬山", model.LifeStatusActive},
		{57, "用户在网上看跑步教程", model.LifeStatusActive},
		{58, "用户说周末睡不够", model.LifeStatusActive},
	}...)
}()

type evalCase struct {
	name  string
	query string
	// want 是相关记忆编号，空表示本就不该有相关结果，不进召回率分母。
	want []int64
	// known 标注预期失败的样例，只报告不断言。
	known string
}

var evalCases = []evalCase{
	{name: "中文完整问句", query: "我周末喜欢跑步这件事你还记得吗", want: []int64{1}},
	{name: "中文完整问句无空格", query: "我学的Go最近怎么样了", want: []int64{3}},
	{name: "简短关键词", query: "咖啡", want: []int64{12}},
	{name: "单字查询", query: "猫", want: []int64{2}},
	{name: "多词查询", query: "早上 起床 习惯", want: []int64{6}},
	{name: "中英文混合", query: "VS Code 代码", want: []int64{10}},
	{name: "较旧的相关记忆与较新的无关记忆", query: "跑步 周末", want: []int64{1}},
	{name: "重复词", query: "跑步 跑步 跑步", want: []int64{1, 53, 55, 57}},
	{name: "已失效记忆", query: "PyCharm", want: nil},
	{name: "无相关结果", query: "量子力学 拓扑绝缘体", want: nil},
	{name: "无字面重合的同义表达", query: "我养的那只宠物现在怎么样了", want: []int64{2}, known: "字面零重合，预期召回不到"},
	{name: "通配符查询", query: "%", want: nil},
}

func TestSearchMemoriesEval(t *testing.T) {
	db, repo := testsupport.Open(t)
	seedEvalMemories(t, db)

	ctx := context.Background()

	var (
		targeted                           int
		oldHit, newHit                     int
		oldRecall, newRecall               float64
		falsePositiveOld, falsePositiveNew int
	)

	for _, tc := range evalCases {
		oldResult, err := legacySearch(ctx, db, tc.query, evalK)
		if err != nil {
			t.Fatalf("%s: 旧实现出错: %v", tc.name, err)
		}
		newResult, err := repo.SearchMemories(ctx, tc.query, evalK)
		if err != nil {
			t.Fatalf("%s: 新实现出错: %v", tc.name, err)
		}

		oldIDs := memoryIDs(oldResult)
		newIDs := memoryIDs(newResult)

		t.Logf("%-22s query=%-18q 旧=%v 新=%v", tc.name, tc.query, oldIDs, newIDs)

		// 硬性约束：返回非 active 的记忆即缺陷。
		for _, m := range newResult {
			if m.Status != model.LifeStatusActive {
				t.Fatalf("%s: 返回了非 active 的记忆 %d（status=%s）", tc.name, m.ID, m.Status)
			}
		}

		if len(tc.want) == 0 {
			if len(oldResult) > 0 {
				falsePositiveOld++
			}
			if len(newResult) > 0 {
				falsePositiveNew++
			}
			if tc.known == "" {
				if len(newResult) > 0 {
					t.Errorf("%s: 没有相关结果却返回了 %v", tc.name, newIDs)
				}
			}
			continue
		}

		targeted++
		if hit(oldIDs, tc.want) {
			oldHit++
		}
		if hit(newIDs, tc.want) {
			newHit++
		}
		oldRecall += recall(oldIDs, tc.want)
		newRecall += recall(newIDs, tc.want)
	}

	t.Logf("K=%d，有相关目标的查询 %d 条（空目标查询 %d 条单独统计误召回，不进召回率分母）",
		evalK, targeted, len(evalCases)-targeted)
	t.Logf("Hit@%d   旧 %d/%d   新 %d/%d", evalK, oldHit, targeted, newHit, targeted)
	t.Logf("Recall@%d 旧 %.2f   新 %.2f", evalK, oldRecall/float64(targeted), newRecall/float64(targeted))
	t.Logf("空目标查询里出现误召回的条数：旧 %d，新 %d", falsePositiveOld, falsePositiveNew)

	// 中文整句与相关度排序必须有可验证的改善。
	if newHit <= oldHit {
		t.Errorf("新检索没有比旧检索命中更多查询：旧 %d，新 %d", oldHit, newHit)
	}
}

func seedEvalMemories(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, m := range evalMemories {
		if _, err := db.Exec("INSERT INTO memories (id, content, status) VALUES (?, ?, ?)",
			m.id, m.content, m.status); err != nil {
			t.Fatalf("写入评测数据 %d 失败: %v", m.id, err)
		}
	}
}

// 旧检索基线：空白切词、LIKE 粗匹配、按 id 倒序取前 limit 条，不做相关度排序；LIKE 中 % 与 _ 未转义。
func legacySearch(ctx context.Context, db *sql.DB, query string, limit int) ([]model.Memory, error) {
	words := strings.Fields(query)
	where := "status = 'active'"
	args := make([]any, 0, len(words)+1)
	if len(words) > 0 {
		clauses := make([]string, len(words))
		for i, w := range words {
			clauses[i] = "content LIKE ?"
			args = append(args, "%"+w+"%")
		}
		where += " AND (" + strings.Join(clauses, " OR ") + ")"
	}
	args = append(args, limit)

	rows, err := db.QueryContext(ctx, `
		SELECT id, content, COALESCE(category, ''), status, created_at, updated_at
		FROM memories
		WHERE `+where+`
		ORDER BY id DESC
		LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []model.Memory
	for rows.Next() {
		var m model.Memory
		if err := rows.Scan(&m.ID, &m.Content, &m.Category, &m.Status, &m.CreatedAt, &m.UpdatedAt); err != nil {
			return nil, err
		}
		list = append(list, m)
	}
	return list, rows.Err()
}

func memoryIDs(list []model.Memory) []int64 {
	ids := make([]int64, len(list))
	for i, m := range list {
		ids[i] = m.ID
	}
	return ids
}

func hit(got []int64, want []int64) bool {
	for _, g := range got {
		for _, w := range want {
			if g == w {
				return true
			}
		}
	}
	return false
}

func recall(got, want []int64) float64 {
	found := 0
	for _, w := range want {
		for _, g := range got {
			if g == w {
				found++
				break
			}
		}
	}
	return float64(found) / float64(len(want))
}
