package service

import (
	"testing"

	"github.com/Xoyeir/Memora/internal/llm"
	"github.com/Xoyeir/Memora/internal/model"
)

func candidate(content string) *llm.MemoryCandidate {
	return &llm.MemoryCandidate{Content: content, Category: "goal"}
}

// 分界点：模型输出被拒绝在这里丢掉；数据库失败发生在事务里、会让整批回滚。
func TestPlanMemoryOps(t *testing.T) {
	const userChatID = 42
	memories := []model.Memory{{ID: 7}, {ID: 8}}

	cases := []struct {
		name   string
		action llm.MemoryAction
		want   []model.MemoryOp
	}{
		{
			name:   "FORM",
			action: llm.MemoryAction{Action: "FORM", Candidate: candidate("用户在做 Memora")},
			want: []model.MemoryOp{{
				Kind: model.OpForm, Content: "用户在做 Memora", Category: "goal", ChatMessageID: userChatID,
			}},
		},
		{
			name:   "FORM 缺少 candidate",
			action: llm.MemoryAction{Action: "FORM"},
		},
		{
			name:   "FORM 的 candidate 是空的",
			action: llm.MemoryAction{Action: "FORM", Candidate: candidate("")},
		},
		{
			name: "REINFORCE 带本轮证据",
			action: llm.MemoryAction{
				Action: "REINFORCE", Ref: "memory:7", EvidenceRefs: []string{"chat:42"},
			},
			want: []model.MemoryOp{{Kind: model.OpReinforce, MemoryID: 7, ChatMessageID: userChatID}},
		},
		{
			name: "REINFORCE 的证据指向别的消息",
			action: llm.MemoryAction{
				Action: "REINFORCE", Ref: "memory:7", EvidenceRefs: []string{"chat:41"},
			},
		},
		{
			name:   "REINFORCE 没有证据",
			action: llm.MemoryAction{Action: "REINFORCE", Ref: "memory:7"},
		},
		{
			name:   "REINFORCE 指向不存在的记忆",
			action: llm.MemoryAction{Action: "REINFORCE", Ref: "memory:999", EvidenceRefs: []string{"chat:42"}},
		},
		{
			name:   "REINFORCE 的引用格式不对",
			action: llm.MemoryAction{Action: "REINFORCE", Ref: "7", EvidenceRefs: []string{"chat:42"}},
		},
		{
			name:   "SUPERSEDE",
			action: llm.MemoryAction{Action: "SUPERSEDE", Ref: "memory:8", Candidate: candidate("换了个说法")},
			want: []model.MemoryOp{{
				Kind: model.OpSupersede, MemoryID: 8, Content: "换了个说法", Category: "goal", ChatMessageID: userChatID,
			}},
		},
		{
			name:   "SUPERSEDE 缺少候选内容",
			action: llm.MemoryAction{Action: "SUPERSEDE", Ref: "memory:8"},
		},
		{
			name:   "CORRECT 带新内容",
			action: llm.MemoryAction{Action: "CORRECT", Ref: "memory:7", Candidate: candidate("其实是在做别的")},
			want: []model.MemoryOp{{
				Kind: model.OpCorrectRewrite, MemoryID: 7, Content: "其实是在做别的", Category: "goal", ChatMessageID: userChatID,
			}},
		},
		{
			name:   "CORRECT 不带内容",
			action: llm.MemoryAction{Action: "CORRECT", Ref: "memory:7"},
			want:   []model.MemoryOp{{Kind: model.OpCorrectDrop, MemoryID: 7}},
		},
		{
			name:   "CORRECT 指向不存在的记忆",
			action: llm.MemoryAction{Action: "CORRECT", Ref: "memory:999"},
		},
		{
			name:   "未知 action",
			action: llm.MemoryAction{Action: "DELETE_EVERYTHING"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := planMemoryOps([]llm.MemoryAction{tc.action}, userChatID, memories)
			if len(got) != len(tc.want) {
				t.Fatalf("得到 %d 条操作，期望 %d 条: %+v", len(got), len(tc.want), got)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("第 %d 条是 %+v，期望 %+v", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// 个别 action 不合法时其余照常保留：被拒绝的是模型输出，不是整批写入。
func TestPlanMemoryOpsKeepsValidOnesInOrder(t *testing.T) {
	actions := []llm.MemoryAction{
		{Action: "FORM", Candidate: candidate("第一条")},
		{Action: "REINFORCE", Ref: "memory:999", EvidenceRefs: []string{"chat:42"}},
		{Action: "DELETE_EVERYTHING"},
		{Action: "REINFORCE", Ref: "memory:7", EvidenceRefs: []string{"chat:42"}},
		{Action: "FORM", Candidate: candidate("最后一条")},
	}

	got := planMemoryOps(actions, 42, []model.Memory{{ID: 7}})
	if len(got) != 3 {
		t.Fatalf("得到 %d 条操作，期望 3 条: %+v", len(got), got)
	}
	if got[0].Kind != model.OpForm || got[0].Content != "第一条" {
		t.Fatalf("第 1 条是 %+v", got[0])
	}
	if got[1].Kind != model.OpReinforce || got[1].MemoryID != 7 {
		t.Fatalf("第 2 条是 %+v", got[1])
	}
	if got[2].Kind != model.OpForm || got[2].Content != "最后一条" {
		t.Fatalf("第 3 条是 %+v", got[2])
	}

	// 证据消息只能来自本轮，不能由模型指定别处。
	for _, op := range got {
		if op.Kind != model.OpCorrectDrop && op.ChatMessageID != 42 {
			t.Fatalf("操作 %+v 的证据不是本轮消息", op)
		}
	}
}

func TestPlanMemoryOpsOnEmptyInput(t *testing.T) {
	if got := planMemoryOps(nil, 42, nil); len(got) != 0 {
		t.Fatalf("没有 action 时应当返回空，实际是 %+v", got)
	}
}
