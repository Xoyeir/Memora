package repository

import (
	"context"
	"encoding/json"

	"github.com/Xoyeir/Memora/internal/model"
)

func (r *Repository) CreateJournal(ctx context.Context, content string) (int64, error) {
	res, err := r.db.ExecContext(ctx, "INSERT INTO journals (content) VALUES (?)", content)
	if err != nil {
		return 0, err
	}

	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}

	return id, nil
}

func (r *Repository) ListJournals(ctx context.Context, limit int) ([]model.JournalWithInsight, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT j.id, j.content, j.created_at,
		       COALESCE(i.status, 'pending'),
		       COALESCE(i.summary, ''),
		       COALESCE(i.moods, '[]'),
		       COALESCE(i.events, '[]'),
		       COALESCE(i.achievements, '[]'),
		       COALESCE(i.goals, '[]'),
		       COALESCE(i.gratitude, '[]')
		FROM journals j
		LEFT JOIN journal_insights i ON i.journal_id = j.id
		ORDER BY j.id DESC
		LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []model.JournalWithInsight
	for rows.Next() {
		var jw model.JournalWithInsight
		var moods, events, achievements, goals, gratitude []byte

		if err := rows.Scan(
			&jw.Journal.ID, &jw.Journal.Content, &jw.Journal.CreatedAt,
			&jw.Insight.Status, &jw.Insight.Summary,
			&moods, &events, &achievements, &goals, &gratitude,
		); err != nil {
			return nil, err
		}

		jw.Insight.JournalID = jw.Journal.ID
		jw.Insight.Moods = unmarshalStringList(moods)
		jw.Insight.Events = unmarshalStringList(events)
		jw.Insight.Achievements = unmarshalStringList(achievements)
		jw.Insight.Goals = unmarshalStringList(goals)
		jw.Insight.Gratitude = unmarshalStringList(gratitude)

		list = append(list, jw)
	}

	return list, rows.Err()
}

func unmarshalStringList(b []byte) []string {
	if len(b) == 0 {
		return []string{}
	}
	var s []string
	if err := json.Unmarshal(b, &s); err != nil {
		return []string{}
	}
	return s
}
