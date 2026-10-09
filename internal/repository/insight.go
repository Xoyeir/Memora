package repository

import (
	"context"
	"encoding/json"

	"github.com/Xoyeir/Memora/internal/model"
)

func (r *Repository) CreateInsight(ctx context.Context, journalID int64) error {
	_, err := r.db.ExecContext(ctx,
		"INSERT INTO journal_insights (journal_id, status) VALUES (?, 'pending')",
		journalID)
	return err
}

func (r *Repository) SaveAnalysisResult(ctx context.Context, journalID int64, ins model.JournalInsight, memories []model.Memory) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	moods, _ := json.Marshal(ins.Moods)
	events, _ := json.Marshal(ins.Events)
	achievements, _ := json.Marshal(ins.Achievements)
	goals, _ := json.Marshal(ins.Goals)
	gratitude, _ := json.Marshal(ins.Gratitude)

	_, err = tx.ExecContext(ctx, `
		UPDATE journal_insights
		SET status = 'completed', summary = ?, moods = ?, events = ?, achievements = ?, goals = ?, gratitude = ?
		WHERE journal_id = ?`,
		ins.Summary, moods, events, achievements, goals, gratitude, journalID)
	if err != nil {
		return err
	}

	for _, m := range memories {
		res, err := tx.ExecContext(ctx,
			"INSERT INTO memories (content, category, status) VALUES (?, ?, 'active')",
			m.Content, m.Category)
		if err != nil {
			return err
		}

		memoryID, err := res.LastInsertId()
		if err != nil {
			return err
		}

		_, err = tx.ExecContext(ctx,
			"INSERT INTO memory_sources (memory_id, journal_id) VALUES (?, ?)",
			memoryID, journalID)
		if err != nil {
			return err
		}
	}

	return tx.Commit()
}

func (r *Repository) FailInsight(ctx context.Context, journalID int64, errMsg string) error {
	_, err := r.db.ExecContext(ctx,
		"UPDATE journal_insights SET status = 'failed', error_message = ? WHERE journal_id = ?",
		errMsg, journalID)
	return err
}
