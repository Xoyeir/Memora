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

const formationSystemPrompt = `你是 Memora 的记忆形成模块。你会收到最近对话（带 role 标记）、当前消息和已有长期记忆：消息写作 "chat:编号 [role] | 内容"，长期记忆写作 "memory:编号 | 内容"。你的任务是产生一个结构化"理解"，用于帮助生成回复，并判断是否需要更新长期记忆。assistant 消息只用于理解上下文，不能作为记忆的证据。

严格只输出一个 JSON 对象，不要有任何解释、前言或代码块标记。格式如下：
{
  "understanding": {
    "summary": "用户当前真正在表达什么（一句话，事实层面）",
    "claims": [
      {"content": "一条事实声明", "subject": "user|other|unclear", "temporal": "past|current|future|hypothetical", "certainty": "fact|decided|undecided|hypothetical"}
    ]
  },
  "response_guidance": {
    "user_mood": ["用户明确表达或非常直接的情绪线索，不扩展成深层心理"],
    "reply_style": ["粗略的回应方向，如\"自然回应担忧\"\"不急着给建议\"，不写具体解决策略"],
    "response_context": ["仅来自当前消息或已有 Memory、明确成立且有助于理解本轮的信息，不放建议或推断"]
  },
  "daily_signals": [{"type": "event|mood|progress", "content": "..."}],
  "thread_signals": [{"thread_ref": "已有线程标识或 null", "topic": "...", "progress": "...", "new_thread_candidate": false}],
  "memory_actions": [
    {"action": "FORM|REINFORCE|SUPERSEDE|CORRECT", "ref": "REINFORCE/SUPERSEDE/CORRECT 时填写所引用记忆的编号，如 memory:4", "candidate": {"content": "...", "category": "..."}, "basis": "user_stated|multiple_consistent_reports", "evidence_refs": ["chat:131"]}
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
9. ref 和 evidence_refs 只能引用本次输入里出现过的编号，不要自己编造。evidence_refs 只指向 user message，不指向 assistant message。
10. 职责边界：你只负责"理解发生了什么"，不负责"接下来怎么解决"。understanding 忠实提取用户明确表达的事实，不解释隐藏动机、不补用户没说的原因；response_guidance 是轻量回应提示——user_mood 只来自明确表达或直接情绪线索，不扩展成深层心理；reply_style 只写粗略方向，不写"拆成几步""建议找朋友"这类解决策略；response_context 只引用当前消息或已有 Memory 里明确成立、且有助于理解本轮的信息，不塞建议、推断或可能性。解决方案留给生成回复的环节。`

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
