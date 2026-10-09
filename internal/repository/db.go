package repository

import (
	"database/sql"
	"fmt"
	"net/url"
	"time"

	// 仅导入以注册 MySQL 驱动，代码里不直接用它
	_ "github.com/go-sql-driver/mysql"

	// 内嵌 IANA 时区数据库，避免在缺少 tzdata 的运行环境里 LoadLocation 失败。
	_ "time/tzdata"

	"github.com/Xoyeir/Memora/internal/config"
)

// StorageTimezone 是数据库里 datetime 的时区，也就是这批墙上时间的解释方式。
//
// 它描述的是磁盘上已有数据的语义，不是运行时偏好，所以写成常量：
// 已经存好的上海墙上时间不会因为改配置而变成另一个时刻。
// 换掉它等于重新解释全部历史数据，只能伴随一次明确的数据迁移。
//
// 跟 APP_TIMEZONE 是两件事：那个只负责把真实时刻换算成业务自然日。
const StorageTimezone = "Asia/Shanghai"

type Repository struct {
	db *sql.DB
}

// storageLocation 返回存储时区。
//
// tzdata 已内嵌，加载失败只可能是常量写错了，属于构建期问题。
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

// buildDSN 把配置拼成连接串。
//
// loc 固定为存储时区，与 APP_TIMEZONE 和机器 Local 都无关：
// datetime 存的是墙上时间，用什么时区解读它决定了这批数据指哪个时刻。
//
// time_zone 让驱动在每一条新建立的连接上执行 SET time_zone，
// 所以连接池之后新建的连接同样带着这个会话时区，写入侧不会跑偏；
// 只在某一条连接上设置是不够的，池子会不断替换掉它。
//
// 用固定偏移而不是时区名，是为了不要求服务器预先导入时区表；
// StorageTimezone 不实行夏令时，偏移恒定，这个替换是安全的。
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

// checkSessionTimezone 复核连接实际使用的会话时区与存储语义一致。
//
// 会话时区已经由 DSN 的 time_zone 逐连接设置过了，这里确认它真的生效：
// 服务器不认这个值、或中间层改写了它，写入的墙上时间就会和读取的解释对不上。
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

// Open 按完整 DSN 建立连接，供测试直接指向测试库使用。
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

// FromDB 复用已有的连接池。测试要在同一个池子上同时做仓储调用和原始 SQL 断言。
func FromDB(db *sql.DB) *Repository {
	return &Repository{db: db}
}

func (r *Repository) Close() error {
	return r.db.Close()
}
