// Package testsupport 提供测试用的真实数据库准备逻辑。
//
// 这些测试刻意连真实 MySQL，因为本轮要验证的是唯一键、条件 UPDATE 这类
// 数据库行为——用替身测出来的是替身的行为，不是 MySQL 的行为。
//
// 测试库的隔离靠三条硬规则，全部在任何写操作之前完成：
//  1. 只认 MEMORA_TEST_DSN，不存在任何回退到 DB_NAME / DB_PASSWORD 的路径；
//  2. 库名必须符合测试库命名规则，否则直接拒绝，不做猜测；
//  3. 迁移里的 USE / CREATE DATABASE 被丢弃，连接始终停在测试库上。
package testsupport

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"

	"github.com/Xoyeir/memora/internal/repository"
)

// EnvDSN 是测试库的连接串。未设置时所有依赖数据库的测试都会被跳过。
const EnvDSN = "MEMORA_TEST_DSN"

var (
	setupOnce sync.Once
	setupErr  error
	setupDSN  string
)

// tables 是本轮测试会写到的表，按外键依赖倒序排列，便于清理。
var tables = []string{"memory_sources", "memories", "chat_messages"}

// Open 是测试入口。未配置测试库时跳过，配置不合法时直接失败。
func Open(t *testing.T) (*sql.DB, *repository.Repository) {
	t.Helper()

	dsn := os.Getenv(EnvDSN)
	if strings.TrimSpace(dsn) == "" {
		t.Skipf("未设置 %s，跳过需要真实数据库的测试", EnvDSN)
	}

	db, repo, err := Connect(dsn)
	if err != nil {
		t.Fatalf("准备测试库失败: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	Truncate(t, db)
	return db, repo
}

// Connect 校验连接串、重建测试库并跑完迁移。
//
// 校验全部发生在建库、迁移和任何写操作之前；任一步不通过都返回 error，
// 不会降级到应用的开发库，也不会去猜一个「差不多」的库名。
func Connect(dsn string) (*sql.DB, *repository.Repository, error) {
	cfg, err := ParseTestDSN(dsn)
	if err != nil {
		return nil, nil, err
	}

	setupOnce.Do(func() {
		setupDSN = dsn
		setupErr = setup(cfg)
	})
	if setupErr != nil {
		return nil, nil, setupErr
	}
	if setupDSN != dsn {
		return nil, nil, fmt.Errorf("%s 在一次测试运行中发生了变化，测试库只重建一次", EnvDSN)
	}

	// 用规范化后的连接串：时间语义是在 ParseTestDSN 里补上的，
	// 直接拿原始串连库会漏掉它。
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		return nil, nil, fmt.Errorf("连接测试库失败: %w", err)
	}

	var current string
	if err := db.QueryRow("SELECT DATABASE()").Scan(&current); err != nil {
		db.Close()
		return nil, nil, fmt.Errorf("读取当前库失败: %w", err)
	}
	if current != cfg.DBName {
		db.Close()
		return nil, nil, fmt.Errorf("连接停在库 %q 上，期望 %q", current, cfg.DBName)
	}

	return db, repository.FromDB(db), nil
}

// ParseTestDSN 解析并校验测试库连接串。
//
// 这里刻意不接受空串、不接受缺库名的 DSN、不接受不以 _test 结尾的库名：
// 测试会 DROP 并重建目标库，任何一次误判都意味着删掉真实数据。
func ParseTestDSN(dsn string) (*mysql.Config, error) {
	if strings.TrimSpace(dsn) == "" {
		return nil, fmt.Errorf("%s 未设置", EnvDSN)
	}

	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		return nil, fmt.Errorf("%s 不是合法的 DSN: %w", EnvDSN, err)
	}
	if err := checkTestDBName(cfg.DBName); err != nil {
		return nil, err
	}

	// 时间语义由代码决定，不由连接串决定：测试库和生产库必须用同一套解释方式，
	// 否则测出来的时间行为跟线上不一致。连接串只提供地址和凭据。
	loc, err := time.LoadLocation(repository.StorageTimezone)
	if err != nil {
		return nil, fmt.Errorf("加载存储时区失败: %w", err)
	}
	cfg.Loc = loc
	if cfg.Params == nil {
		cfg.Params = map[string]string{}
	}
	cfg.Params["time_zone"] = formatSessionTimezone(loc)

	return cfg, nil
}

// formatSessionTimezone 把存储时区写成一个固定偏移的会话时区值。
func formatSessionTimezone(loc *time.Location) string {
	_, offset := time.Date(2026, 1, 1, 0, 0, 0, 0, loc).Zone()
	sign := "+"
	if offset < 0 {
		sign = "-"
		offset = -offset
	}
	return fmt.Sprintf("'%s%02d:%02d'", sign, offset/3600, offset%3600/60)
}

// checkTestDBName 是测试库的命名规则：非空，且以 _test 结尾。
//
// 库名会被拼进 DROP / CREATE DATABASE 语句，所以除了后缀规则，
// 还要确保它只含标识符安全字符——不能只依赖 DSN 解析器的宽容。
func checkTestDBName(name string) error {
	if name == "" {
		return fmt.Errorf("%s 缺少库名：必须显式指向一个以 _test 结尾的测试库", EnvDSN)
	}
	if len(name) <= len("_test") || !strings.HasSuffix(name, "_test") {
		return fmt.Errorf("%s 的库名 %q 不符合测试库规则：必须以 _test 结尾，例如 memora_test", EnvDSN, name)
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		ok := c == '_' ||
			(c >= '0' && c <= '9') ||
			(c >= 'a' && c <= 'z') ||
			(c >= 'A' && c <= 'Z')
		if !ok {
			return fmt.Errorf("%s 的库名 %q 含非法字符", EnvDSN, name)
		}
	}
	return nil
}

func setup(cfg *mysql.Config) error {
	rootCfg := *cfg
	rootCfg.DBName = ""

	root, err := sql.Open("mysql", rootCfg.FormatDSN())
	if err != nil {
		return err
	}
	defer root.Close()

	if _, err := root.Exec("DROP DATABASE IF EXISTS `" + cfg.DBName + "`"); err != nil {
		return err
	}
	if _, err := root.Exec("CREATE DATABASE `" + cfg.DBName + "` CHARACTER SET utf8mb4"); err != nil {
		return err
	}
	root.Close()

	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		return err
	}
	defer db.Close()

	dir, err := migrationsDir()
	if err != nil {
		return err
	}
	if err := applyMigrations(db, dir); err != nil {
		return err
	}

	// 迁移里万一有语句把连接切走了，这里必须立刻发现，而不是等到某个测试写坏别的库。
	var current string
	if err := db.QueryRow("SELECT DATABASE()").Scan(&current); err != nil {
		return err
	}
	if current != cfg.DBName {
		return fmt.Errorf("迁移结束后连接停在库 %q 上，期望 %q", current, cfg.DBName)
	}
	return nil
}

// migrationsDir 从当前测试文件向上找到仓库根目录下的 migrations。
func migrationsDir() (string, error) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "", os.ErrNotExist
	}
	for dir := filepath.Dir(file); ; dir = filepath.Dir(dir) {
		candidate := filepath.Join(dir, "migrations")
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return candidate, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", os.ErrNotExist
		}
	}
}

func applyMigrations(db *sql.DB, dir string) error {
	files, err := filepath.Glob(filepath.Join(dir, "*.sql"))
	if err != nil {
		return err
	}
	sort.Strings(files)

	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			return err
		}
		for _, stmt := range splitStatements(string(raw)) {
			if _, err := db.Exec(stmt); err != nil {
				return fmt.Errorf("%s: %w", filepath.Base(f), err)
			}
		}
	}
	return nil
}

// splitStatements 去掉 -- 注释行后按分号切分。
// 这些迁移文件里没有字符串字面量含分号，够用。
//
// USE 和 CREATE DATABASE 会被丢掉：连接已经停在测试库上，
// 执行 001 里的 use memora; 会把后续语句整体切到开发库上。
func splitStatements(sqlText string) []string {
	var cleaned []string
	for _, line := range strings.Split(sqlText, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "--") {
			continue
		}
		cleaned = append(cleaned, line)
	}

	var out []string
	for _, stmt := range strings.Split(strings.Join(cleaned, "\n"), ";") {
		s := strings.TrimSpace(stmt)
		if s == "" {
			continue
		}
		lower := strings.ToLower(s)
		if strings.HasPrefix(lower, "use ") || strings.HasPrefix(lower, "create database") {
			continue
		}
		out = append(out, s)
	}
	return out
}

// Truncate 清空测试用到的表，让每个测试从空库开始。
func Truncate(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, table := range tables {
		if _, err := db.Exec("DELETE FROM " + table); err != nil {
			t.Fatalf("清理表 %s 失败: %v", table, err)
		}
	}
}
