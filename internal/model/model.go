package model

import "time"

type Journal struct {
	ID        int64     `json:"id"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"created_at"`
}

type JournalInsight struct {
	ID           int64     `json:"id"`
	JournalID    int64     `json:"journal_id"`
	Status       string    `json:"status"`
	Summary      string    `json:"summary"`
	Moods        []string  `json:"moods"`
	Events       []string  `json:"events"`
	Achievements []string  `json:"achievements"`
	Goals        []string  `json:"goals"`
	Gratitude    []string  `json:"gratitude"`
	ErrorMessage string    `json:"error_message,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type Memory struct {
	ID             int64     `json:"id"`
	Content        string    `json:"content"`
	Category       string    `json:"category,omitempty"`
	Status         string    `json:"status"`
	SupersededByID *int64    `json:"superseded_by_id,omitempty"`
	IsPinned       bool      `json:"is_pinned"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type JournalWithInsight struct {
	Journal Journal        `json:"journal"`
	Insight JournalInsight `json:"insight"`
}

// ChatMessage 是一条聊天记录。
//
// Status 与 ClientID 只在展示查询里填充：展示需要知道「这条回复是不是没写完」，
// 也需要拿到 client_id 才能让刷新后的重试仍然复用原一轮。
// 送给模型的上下文查询不填充它们——模型上下文只由完整的轮次构成。
type ChatMessage struct {
	ID        int64     `json:"id"`
	Role      string    `json:"role"`
	Content   string    `json:"content"`
	Status    string    `json:"status,omitempty"`
	ClientID  string    `json:"client_id,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// ChatTurn 是一次用户发送对应的落库状态。
//
// Status 描述「助手回复」的完成情况：pending / ok / failed / canceled。
// FormationStatus 描述「记忆写入」的完成情况：none / pending / running / applied / failed。
// 两者独立：回复断流不代表记忆没有被写入。
type ChatTurn struct {
	ID              int64
	Content         string
	Status          string
	FormationStatus string
}

// 助手回复的完成状态。
const (
	// TurnStatusOK 回复完整结束。
	TurnStatusOK = "ok"
	// TurnStatusFailed 生成过程中失败，可能已经输出了部分内容。
	TurnStatusFailed = "failed"
	// TurnStatusCanceled 客户端断开或整体超时。
	TurnStatusCanceled = "canceled"
)

// 记忆（Formation）的完成状态。
const (
	// FormationNone 历史行与 assistant 行，不参与 Formation。
	FormationNone = "none"
	// FormationPending 新建的 user 行，尚未尝试。
	FormationPending = "pending"
	// FormationRunning 已被某次请求认领。
	FormationRunning = "running"
	// FormationApplied 已执行完毕，包含「没有任何 action」。
	FormationApplied = "applied"
	// FormationFailed 模型调用失败，或整批写入回滚，允许再次尝试。
	FormationFailed = "failed"
)

// 记忆写入操作的类型。它是「已经通过校验、准备执行」的操作，
// 与模型原始输出的区别在于：引用已经解析、无效项已经被剔除。
const (
	OpForm           = "form"
	OpReinforce      = "reinforce"
	OpSupersede      = "supersede"
	OpCorrectRewrite = "correct_rewrite"
	OpCorrectDrop    = "correct_drop"
)

// MemoryOp 是一条准备执行的记忆写入。
//
// 一批 MemoryOp 必须在同一个事务里全部成功或全部回滚：
// 它们描述的是同一条消息带来的同一次记忆更新。
type MemoryOp struct {
	Kind string
	// MemoryID 是 reinforce / supersede / correct 指向的已有记忆。
	MemoryID int64
	// Content / Category 仅 form / supersede / correct_rewrite 使用。
	Content  string
	Category string
	// ChatMessageID 是这条更新的证据消息，只能是当前这一轮的 user 消息。
	ChatMessageID int64
}

type MemorySource struct {
	SourceType string    `json:"source_type"`
	Content    string    `json:"content"`
	CreatedAt  time.Time `json:"created_at"`
}
