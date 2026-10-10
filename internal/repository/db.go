package repository

import (
	"database/sql"
	"fmt"
	"net/url"
	"time"

	_ "github.com/go-sql-driver/mysql"

	// 内嵌 IANA 时区数据库
	_ "time/tzdata"

	"github.com/Xoyeir/Memora/internal/config"
)

// StorageTimezone 是库里 datetime 的时区语义，改它等于重新解释全部历史时间、须配套数据迁移；APP_TIMEZONE 只把真实时刻换算成业务自然日。
const StorageTimezone = "Asia/Shanghai"

type Repository struct {
	db *sql.DB
}

// storageLocation 加载失败只可能是常量写错了：tzdata 已内嵌。
func storageLocation() (*time.Location, error) {
	loc, err := time.LoadLocation(StorageTimezone)
	if err != nil {
		return nil, fmt.Errorf("加载存储时区 %q 失败: %w", StorageTimezone, err)
	}
	return loc, nil
}

func Connect(cfg *config.Config) (*Repository, error) {
	loc, err := storageLocation()
	if err != nil {
		return nil, err
	}

	dsn, err := buildDSN(cfg, loc)
	if err != nil {
		return nil, err
	}

	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, fmt.Errorf("打开 MySQL 连接失败: %w", err)
	}

	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("连接 MySQL 失败: %w", err)
	}

	if err := checkSessionTimezone(db, loc); err != nil {
		db.Close()
		return nil, err
	}

	return &Repository{db: db}, nil
}

// loc 固定为存储时区，与 APP_TIMEZONE、机器 Local 无关；会话时区用固定偏移（不要求服务器预导入时区表），须在每条新连接上设置（池子会不断换连接）。
func buildDSN(cfg *config.Config, loc *time.Location) (string, error) {
	offset := storageOffset(loc)
	_, summer := time.Date(2026, 7, 1, 0, 0, 0, 0, loc).Zone()
	if offset != summer {
		return "", fmt.Errorf("存储时区 %q 有夏令时，不能用固定偏移设置会话时区", StorageTimezone)
	}

	sign := "+"
	if offset < 0 {
		sign = "-"
		offset = -offset
	}
	sessionTZ := fmt.Sprintf("'%s%02d:%02d'", sign, offset/3600, offset%3600/60)

	return fmt.Sprintf("%s:%s@tcp(%s:%s)/%s?charset=utf8mb4&parseTime=true&loc=%s&time_zone=%s",
		cfg.DBUser, cfg.DBPassword, cfg.DBHost, cfg.DBPort, cfg.DBName,
		url.QueryEscape(loc.String()), url.QueryEscape(sessionTZ)), nil
}

// checkSessionTimezone 确认会话时区真的生效：服务器不认或中间层改写它，写入的墙上时间就会和读取时的解释对不上。
func checkSessionTimezone(db *sql.DB, loc *time.Location) error {
	var offset int
	if err := db.QueryRow("SELECT TIMESTAMPDIFF(SECOND, UTC_TIMESTAMP(), NOW())").Scan(&offset); err != nil {
		return fmt.Errorf("读取 MySQL 会话时区失败: %w", err)
	}

	if want := storageOffset(loc); offset != want {
		return fmt.Errorf(
			"MySQL 会话时区与存储时区 %s 不一致：MySQL 偏移 %+d 秒，期望 %+d 秒。"+
				"写入用的是前者、读取用的是后者，不一致会把时间解读错，因此拒绝启动",
			StorageTimezone, offset, want)
	}
	return nil
}

func storageOffset(loc *time.Location) int {
	_, offset := time.Date(2026, 1, 1, 0, 0, 0, 0, loc).Zone()
	return offset
}

// Open 供测试用完整 DSN 直连测试库。
func Open(dsn string) (*Repository, error) {
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, fmt.Errorf("打开 MySQL 连接失败: %w", err)
	}

	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("连接 MySQL 失败: %w", err)
	}

	return FromDB(db), nil
}

// FromDB 复用已有的连接池：测试要在同一个池子上同时做仓储调用和原始 SQL 断言。
func FromDB(db *sql.DB) *Repository {
	return &Repository{db: db}
}

func (r *Repository) Close() error {
	return r.db.Close()
}
