package repository

import "context"

func (r *Repository) CreateChatMessage(ctx context.Context, role, content string) (int64, error) {
	res, err := r.db.ExecContext(ctx,
		"INSERT INTO chat_messages (role, content) VALUES (?, ?)",
		role, content)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}
