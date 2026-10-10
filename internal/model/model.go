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
	Status         string    `json:"status"` // 只有 active 会进入候选与召回
	Origin         string    `json:"origin"`
	SupersededByID *int64    `json:"superseded_by_id,omitempty"`
	IsPinned       bool      `json:"is_pinned"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

const (
	MemoryStatusActive     = "active"
	MemoryStatusSuperseded = "superseded"
	MemoryStatusCorrected  = "corrected"
	MemoryStatusForgotten  = "forgotten"
)

const (
	MemoryOriginAuto          = "auto"
	MemoryOriginUserCorrected = "user_corrected"
)

// MemoryCorrection 是用户改写或忘记的操作记录，不是原文。
type MemoryCorrection struct {
	ID          int64     `json:"id"`
	MemoryID    int64     `json:"memory_id"`
	NewMemoryID *int64    `json:"new_memory_id,omitempty"`
	Action      string    `json:"action"`
	OldContent  string    `json:"old_content"`
	NewContent  string    `json:"new_content,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

const (
	CorrectionActionCorrect = "correct"
	CorrectionActionForget  = "forget"
)

type JournalWithInsight struct {
	Journal Journal        `json:"journal"`
	Insight JournalInsight `json:"insight"`
}

// ChatMessage 的 Status 与 ClientID 只在展示查询里填充，送进模型的上下文不带。
type ChatMessage struct {
	ID        int64     `json:"id"`
	Role      string    `json:"role"`
	Content   string    `json:"content"`
	Status    string    `json:"status,omitempty"`
	ClientID  string    `json:"client_id,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// ChatTurn 的 Status 描述助手回复、FormationStatus 描述记忆写入，两者独立。
type ChatTurn struct {
	ID              int64
	Content         string
	Status          string
	FormationStatus string
	// CreatedAt 只读取、从不改写：重试复用同一行，相对日期以它为准。
	CreatedAt time.Time
}

const (
	TurnStatusOK       = "ok"
	TurnStatusFailed   = "failed"
	TurnStatusCanceled = "canceled"
)

const (
	FormationNone    = "none"
	FormationPending = "pending"
	FormationRunning = "running"
	FormationApplied = "applied"
	FormationFailed  = "failed"
)

const (
	OpForm           = "form"
	OpReinforce      = "reinforce"
	OpSupersede      = "supersede"
	OpCorrectRewrite = "correct_rewrite"
	OpCorrectDrop    = "correct_drop"
)

// MemoryOp 是一批必须在同一事务里全部成功或全部回滚的写入。
type MemoryOp struct {
	Kind          string
	MemoryID      int64
	Content       string
	Category      string
	ChatMessageID int64
}

type MemorySource struct {
	SourceType string    `json:"source_type"`
	Content    string    `json:"content"`
	CreatedAt  time.Time `json:"created_at"`
	Action     string    `json:"action,omitempty"`
	// NewContent 仅 action=correct，是用户自己写下的新说法。
	NewContent string `json:"new_content,omitempty"`
}

const (
	MemorySourceChat       = "chat"
	MemorySourceJournal    = "journal"
	MemorySourceCorrection = "correction"
)

type DayState struct {
	Day          string `json:"day"`
	Status       string `json:"status"`
	Label        string `json:"label"`
	Summary      string `json:"summary"`
	Source       string `json:"source"`
	ClueCount    int    `json:"clue_count"`
	Truncated    bool   `json:"input_truncated"`
	NeedsSummary bool   `json:"needs_summary"`
	ErrorMessage string `json:"error,omitempty"`
}

const (
	DayStatusNoChat  = "no_chat"
	DayStatusNoClue  = "no_clue"
	DayStatusPending = "pending"
	DayStatusReady   = "ready"
	DayStatusFailed  = "failed"
)

const (
	LifeKindActivity = "activity"
	LifeKindMood     = "mood"
	LifeKindGoal     = "goal"
	LifeKindDate     = "date"
)

const (
	LifeStateDone    = "done"
	LifeStatePlanned = "planned"
	LifeStateNegated = "negated"
	LifeStateWish    = "wish"
	LifeStateNone    = "none"
)

const (
	LifeBasisExplicit = "explicit"
	LifeBasisInferred = "inferred" // 只允许用于 mood
)

const (
	LifeSubjectUser    = "user"
	LifeSubjectOther   = "other"
	LifeSubjectUnclear = "unclear"
)

const (
	LifeDateDay      = "day"
	LifeDateMonthDay = "month_day"
	LifeDateUnknown  = "unknown"
)

const (
	LifeStatusActive    = "active"
	LifeStatusCorrected = "corrected"
)

// LifeRecord 是有出处的一次性记录，不是结论；日期字段是不带时刻与时区的业务自然日。
type LifeRecord struct {
	ID            int64     `json:"id"`
	Kind          string    `json:"kind"`
	State         string    `json:"state"`
	Basis         string    `json:"basis"`
	Subject       string    `json:"subject"`
	Content       string    `json:"content"`
	ChatMessageID int64     `json:"chat_message_id"`
	EvidenceQuote string    `json:"evidence_quote"`
	MentionedOn   string    `json:"mentioned_on"`
	OccurredOn    string    `json:"occurred_on,omitempty"`
	OccurredMonth int       `json:"occurred_month,omitempty"`
	OccurredDay   int       `json:"occurred_day,omitempty"`
	DatePrecision string    `json:"date_precision"`
	DateText      string    `json:"date_text,omitempty"` // 原话里的时间说法，算不出日期时也保留
	Status        string    `json:"status"`
	CorrectedByID int64     `json:"corrected_by_id,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
}

// LifeRecordOp 与同一批 MemoryOp 共用一个事务。
type LifeRecordOp struct {
	Kind          string
	State         string
	Basis         string
	Subject       string
	Content       string
	ChatMessageID int64
	EvidenceQuote string
	MentionedOn   string
	OccurredOn    string
	OccurredMonth int
	OccurredDay   int
	DatePrecision string
	DateText      string
	CorrectsID    int64
}
