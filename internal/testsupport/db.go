// Package testsupport 提供测试库准备：只认 MEMORA_TEST_DSN，库名须 _test 结尾，迁移里的 USE / CREATE DATABASE 被丢弃。
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

	"github.com/Xoyeir/Memora/internal/repository"
)

// EnvDSN 未设置时依赖数据库的测试会被跳过。
const EnvDSN = "MEMORA_TEST_DSN"

var (
	setupOnce sync.Once
	setupErr  error
	setupDSN  string
)

// 按外键依赖倒序排列，便于清理。
var tables = []string{
	"memory_corrections", "life_records", "memory_sources", "memories",
	"chat_messages", "day_states",
}

// Open 是测试入口：未配置测试库时跳过，配置不合法时直接失败。
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

// Prepare 只建测试库，给不经过 Open、自己开连接的测试包用。
func Prepare() error {
	dsn := os.Getenv(EnvDSN)
	if strings.TrimSpace(dsn) == "" {
		return nil
	}
	cfg, err := ParseTestDSN(dsn)
	if err != nil {
		return err
	}
	setupOnce.Do(func() {
		setupDSN = dsn
		setupErr = setup(cfg)
	})
	return setupErr
}

// Connect 校验连接串、重建测试库并跑迁移；校验先于任何写操作，不降级到开发库。
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

	// 用规范化后的连接串：时间语义在 ParseTestDSN 里补上。
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

// ParseTestDSN 拒绝空串、缺库名、库名不以 _test 结尾的 DSN：测试会 DROP 重建目标库。
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

	// 时间语义由代码决定，连接串只提供地址和凭据。
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

func formatSessionTimezone(loc *time.Location) string {
	_, offset := time.Date(2026, 1, 1, 0, 0, 0, 0, loc).Zone()
	sign := "+"
	if offset < 0 {
		sign = "-"
		offset = -offset
	}
	return fmt.Sprintf("'%s%02d:%02d'", sign, offset/3600, offset%3600/60)
}

// checkTestDBName 要求库名非空、以 _test 结尾且只含安全字符：它会被拼进 DROP / CREATE DATABASE。
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

	// 迁移若把连接切走了，这里必须立刻发现。
	var current string
	if err := db.QueryRow("SELECT DATABASE()").Scan(&current); err != nil {
		return err
	}
	if current != cfg.DBName {
		return fmt.Errorf("迁移结束后连接停在库 %q 上，期望 %q", current, cfg.DBName)
	}
	return nil
}

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

// splitStatements 去掉 -- 注释行后按分号切分，并丢弃 USE / CREATE DATABASE：连接必须停在测试库上。
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

func Truncate(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, table := range tables {
		if _, err := db.Exec("DELETE FROM " + table); err != nil {
			t.Fatalf("清理表 %s 失败: %v", table, err)
		}
	}
}
