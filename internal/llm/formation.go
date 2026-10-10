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
	LifeRecords      []LifeRecord     `json:"life_records"`
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

type LifeRecord struct {
	Kind          string     `json:"kind"`
	State         string     `json:"state"`
	Basis         string     `json:"basis"`
	Subject       string     `json:"subject"`
	Modality      string     `json:"modality"`
	Content       string     `json:"content"`
	EvidenceQuote string     `json:"evidence_quote"`
	When          RecordWhen `json:"when"`
	// Corrects 指向被纠正的旧生活记录，形如 life:31；没有则为空。
	Corrects string `json:"corrects"`
}

// RecordWhen 表达「事情发生在哪一天」，不是「消息哪天说的」；相对时间只给 offset_days，由系统换算。
type RecordWhen struct {
	Kind       string `json:"kind"`
	Text       string `json:"text"`
	OffsetDays int    `json:"offset_days"`
	Date       string `json:"date"`
	Month      int    `json:"month"`
	Day        int    `json:"day"`
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

const formationSystemPrompt = `你是 Memora 的记忆形成模块。你会收到最近对话（带 role 标记）、当前消息、已有生活记录和已有长期记忆：消息写作 "chat:编号 [role] | 内容"，生活记录写作 "life:编号 | [类型/状态] 内容（日期）"，长期记忆写作 "memory:编号 | 内容"。你的任务是产生一个结构化"理解"，用于帮助生成回复，并判断需要留下哪些记录。assistant 消息只用于理解上下文，不能作为任何记录的证据。

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
  "life_records": [
    {"kind": "activity|mood|goal|date",
     "state": "done|planned|negated|wish|none",
     "basis": "explicit|inferred",
     "subject": "user|other|unclear",
     "modality": "actual|hypothetical",
     "content": "这条记录本身，用第三人称陈述",
     "evidence_quote": "用户消息里逐字出现的片段",
     "when": {"kind": "relative|absolute|month_day|unknown", "text": "原话里的时间说法", "offset_days": 0, "date": "2026-05-08", "month": 5, "day": 8},
     "corrects": "life:31"}
  ],
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
7. thread_signals 只是检测到可能属于持续过程的信号，不代表创建正式 Thread。
8. ref 和 evidence_refs 只能引用本次输入里出现过的编号，不要自己编造。evidence_refs 只指向 user message，不指向 assistant message。

life_records 记录的是"这条消息里关于用户生活的、有出处的一次性事实与线索"，不是今日总结，更不是人格结论。这里存的是记录本身，不是从记录推出的判断。它与长期记忆分工明确：不是每一次运动、每一次情绪波动都值得写成长期记忆，但都值得作为一条生活记录留下来。

9. kind 分类：activity 用户在某一天做过、打算做或明确否认做过的一件具体的事（跑步、游泳、做了帕梅拉）；mood 情绪与状态线索；goal 目标、愿望与计划；date 重要日期及其对应事件。没有就留空数组。
10. activity 必须是具体到一天、能单独核对的行为。对一段时间的概括或长期状态不属于 activity，不要记："忙完这一阵""最近一直很忙""看书老是被我搁置""经历了一段忙碌的时期"都不是某一天做过的事，negated 同样不适用。这类内容确有长期价值的，交给 goal 或长期记忆；没有就留空。
11. 必须区分完成、计划和否定：state=done 是已经发生，planned 是打算做，"今天没运动"这种明确否认是 negated。三者不能混。kind 与 state 要配套：activity 用 done/planned/negated，goal 用 wish/planned，mood 和 date 一律 none。
12. subject 必须是用户本人（user）。别人做的事（"朋友今天去跑步了"）标 other，不确定的标 unclear——不要当成用户自己的活动。
13. 假设、举例、转述、引用一律 modality=hypothetical，不要写成发生过的事实。
14. basis 只有两种：用户直接说出感受或状态的（"今天很焦虑""我终于松了口气"）是 explicit；用户只讲了事件、情绪得由你来猜的（"项目终于上线了"→ 推测他松了一口气）是 inferred。判据是这句话有没有直接表达感受，不是它读起来含不含蓄——"松了口气"是直接表达，标 explicit。inferred 只允许用在 mood 上，事实类记录必须是 explicit。信息不足以形成 mood 线索时就不输出，不要默认推断成开心或平静。
15. goal 里，"我想成为更自律的人"是 wish，不要因此认为用户已经自律；"下个月准备参加比赛"是 planned，不要当成已经参加。
16. date 只在用户明确给出日期时记录。只知道月日就填 when.kind=month_day，不要补年份；日期不完整或有歧义时用 unknown 并保留原话，不要擅自补齐。
17. when 描述"事情发生在哪一天"，不是"消息在哪一天说的"——后者由系统按原始消息时间算，你不用管。相对时间只提供 offset_days：今天 0、昨天 -1、明天 1、前天 -2、后天 2；用户给出了完整日期时用 absolute 和 "YYYY-MM-DD"；其余情况一律 unknown，不要自己推算。无论能否确定日期，都把用户原话里的时间说法填进 when.text——系统会原样保留它。
18. 一次完成的活动不等于建立了习惯：只记这一次发生了什么，不要写"用户有运动习惯""最近很自律"这类结论，也不算连续天数或完成率。
19. evidence_quote 必须是用户这条消息里逐字出现的片段，不能改写、拼接，也不能引用助手说过的话。找不到可引用的原话就不要输出这条记录。
20. corrects 只在用户明确纠正之前说过的事情时填写，引用输入里出现过的生活记录编号。定位不到旧记录就不要猜，把用户这次说的如实记成一条新记录。
21. 只记录这条当前消息带来的内容。助手此前的建议、分析，以及更早的历史消息，都不能被重新包装成这一轮新增的用户事实。

职责边界：你只负责"理解发生了什么"和"留下什么记录"，不负责"接下来怎么解决"。understanding 忠实提取用户明确表达的事实，不解释隐藏动机、不补用户没说的原因；response_guidance 是轻量回应提示——user_mood 只来自明确表达或直接情绪线索，不扩展成深层心理；reply_style 只写粗略方向，不写"拆成几步""建议找朋友"这类解决策略；response_context 只引用当前消息或已有 Memory 里明确成立、且有助于理解本轮的信息，不塞建议、推断或可能性。解决方案留给生成回复的环节。`

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
