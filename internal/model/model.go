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
	ID              int64     `json:"id"`
	SourceJournalID int64     `json:"source_journal_id"`
	Content         string    `json:"content"`
	Category        string    `json:"category,omitempty"`
	Status          string    `json:"status"`
	SupersededByID  *int64    `json:"superseded_by_id,omitempty"`
	IsPinned        bool      `json:"is_pinned"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}
