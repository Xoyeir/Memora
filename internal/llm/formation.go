package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

type FormationResult struct {
	Understanding    Understanding    `json:"understanding"`
	ResponseGuidance ResponseGuidance `json:"response_guidance"`
	DailySignals     []DailySignal    `json:"daily_signals"`
	ThreadSignals    []ThreadSignal   `json:"thread_signals"`
	MemoryActions    []MemoryAction   `json:"memory_actions"`
	Observe          []ObserveItem    `json:"observe"`
}

type Understanding struct {
	Summary string  `json:"summary"`
	Claims  []Claim `json:"claims"`
}

type Claim struct {
	Content   string `json:"content"`
	Subject   string `json:"subject"`
	Temporal  string `json:"temporal"`
	Certainty string `json:"certainty"`
}

type ResponseGuidance struct {
	UserMood        []string `json:"user_mood"`
	ReplyStyle      []string `json:"reply_style"`
	ResponseContext []string `json:"response_context"`
}

type DailySignal struct {
	Type    string `json:"type"`
	Content string `json:"content"`
}

type ThreadSignal struct {
	ThreadRef          string `json:"thread_ref"`
	Topic              string `json:"topic"`
	Progress           string `json:"progress"`
	NewThreadCandidate bool   `json:"new_thread_candidate"`
}

type MemoryAction struct {
	Action       string           `json:"action"`
	Ref          string           `json:"ref"`
	Candidate    *MemoryCandidate `json:"candidate"`
	Basis        string           `json:"basis"`
	EvidenceRefs []string         `json:"evidence_refs"`
}

type ObserveItem struct {
	Content      string   `json:"content"`
	EvidenceRefs []string `json:"evidence_refs"`
}

const formationSystemPrompt = `你是 Memora 的记忆形成模块。你会收到用户的当前消息，以及一些上下文（最近对话、已有长期记忆）。你的任务是产生一个结构化"理解"，用于帮助生成回复，并判断是否需要更新长期记忆。

严格只输出一个 JSON 对象，不要有任何解释、前言或代码块标记。格式如下：
{
  "understanding": {
    "summary": "用户当前真正在表达什么（一句话，事实层面）",
    "claims": [
      {"content": "一条事实声明", "subject": "user|other|unclear", "temporal": "past|current|future|hypothetical", "certainty": "fact|decided|undecided|hypothetical"}
    ]
  },
  "response_guidance": {
    "user_mood": ["用户情绪"],
    "reply_style": ["建议的回应方式"],
    "response_context": ["应影响回答但不一定显式说出的上下文"]
  },
  "daily_signals": [{"type": "event|mood|progress", "content": "..."}],
  "thread_signals": [{"thread_ref": "已有线程标识或 null", "topic": "...", "progress": "...", "new_thread_candidate": false}],
  "memory_actions": [
    {"action": "FORM|REINFORCE|SUPERSEDE|CORRECT", "ref": "引用已有记忆（REINFORCE/SUPERSEDE/CORRECT 时）", "candidate": {"content": "...", "category": "..."}, "basis": "user_stated|multiple_consistent_reports", "evidence_refs": ["chat:123"]}
  ],
  "observe": [{"content": "...", "evidence_refs": ["chat:123"]}]
}

规则：
1. 空数组是正常结果，不要为了填满而输出。
2. Memory 判断的是"长期价值"，不是"情绪强弱"。一条信息如果几周/几个月后仍有助于理解用户，才有长期价值。
3. 区分事实与推断：只能基于用户明确表达的事实（或多次一致报告）形成 Memory，不要保存人格/心理推断。
4. subject 必须准确：是用户本人、别人、还是不清楚？temporal 是过去/当前/未来/假设？certainty 是事实/已决定/未决定/假设？
5. FORM = 新的长期认识（不是任何新事实）；REINFORCE = 确认已有认识（默认不加证据，只有跨越时间距离、证明持久性的确认才加）；SUPERSEDE = 旧认识曾经成立但现在变了（旧保留）；CORRECT = 旧认识从一开始就错了。
6. Memory 文本避免"最近/今天/刚刚"等相对时间词。
7. daily_signals 是"这条消息给今天新增了什么"，不是"今日总结"。
8. thread_signals 只是检测到可能属于持续过程的信号，不代表创建正式 Thread。
9. evidence_refs 只指向 user message，不指向 assistant message。`

func (c *Client) Formation(ctx context.Context, input string) (*FormationResult, error) {
	messages := []chatMessage{
		{Role: "system", Content: formationSystemPrompt},
		{Role: "user", Content: input},
	}

	text, err := c.chat(ctx, messages, true)
	if err != nil {
		return nil, err
	}

	text = strings.TrimSpace(text)
	text = strings.TrimPrefix(text, "```json")
	text = strings.TrimPrefix(text, "```")
	text = strings.TrimSuffix(text, "```")
	text = strings.TrimSpace(text)

	var result FormationResult
	if err := json.Unmarshal([]byte(text), &result); err != nil {
		return nil, fmt.Errorf("解析 Formation 输出失败: %w", err)
	}

	return &result, nil
}
