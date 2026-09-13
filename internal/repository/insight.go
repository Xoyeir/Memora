package repository

import (
	"context"
	"encoding/json"

	"github.com/Xoyeir/memora/internal/model"
)

func (r *Repository) CreateInsight(ctx context.Context, journalID int64) error {
	_, err := r.db.ExecContext(ctx,
		"INSERT INTO journal_insights (journal_id, status) VALUES (?, 'pending')",
		journalID)
	return err
}

func (r *Repository) CompleteInsight(ctx context.Context, journalID int64, ins model.JournalInsight) error {
	moods, _ := json.Marshal(ins.Moods)
	events, _ := json.Marshal(ins.Events)
	achievements, _ := json.Marshal(ins.Achievements)
	goals, _ := json.Marshal(ins.Goals)
	gratitude, _ := json.Marshal(ins.Gratitude)

	_, err := r.db.ExecContext(ctx,
		`UPDATE journal_insights
		 SET status = 'completed', summary = ?, moods = ?, events = ?,
		     achievements = ?, goals = ?, gratitude = ?
		 WHERE journal_id = ?`,
		ins.Summary, moods, events, achievements, goals, gratitude, journalID)
	return err
}

func (r *Repository) FailInsight(ctx context.Context, journalID int64, errMsg string) error {
	_, err := r.db.ExecContext(ctx,
		"UPDATE journal_insights SET status = 'failed', error_message = ? WHERE journal_id = ?",
		errMsg, journalID)
	return err
}
