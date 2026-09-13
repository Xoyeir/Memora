package repository

import "context"

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
