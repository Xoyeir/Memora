package repository

import (
	"database/sql"
	"fmt"

	// 仅导入以注册 MySQL 驱动，代码里不直接用它
	_ "github.com/go-sql-driver/mysql"

	"github.com/Xoyeir/memora/internal/config"
)

type Repository struct {
	db *sql.DB
}

func Connect(cfg *config.Config) (*Repository, error) {
	dsn := fmt.Sprintf("%s:%s@tcp(%s:%s)/%s?charset=utf8mb4&parseTime=true&loc=Local",
		cfg.DBUser, cfg.DBPassword, cfg.DBHost, cfg.DBPort, cfg.DBName)

	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, fmt.Errorf("打开 MySQL 连接失败: %w", err)
	}

	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("连接 MySQL 失败: %w", err)
	}

	return &Repository{db: db}, nil
}

func (r *Repository) Close() error {
	return r.db.Close()
}
