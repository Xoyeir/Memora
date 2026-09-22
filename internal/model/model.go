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

type ChatMessage struct {
	ID        int64     `json:"id"`
	Role      string    `json:"role"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"created_at"`
}

type MemorySource struct {
	SourceType string    `json:"source_type"`
	Content    string    `json:"content"`
	CreatedAt  time.Time `json:"created_at"`
}
