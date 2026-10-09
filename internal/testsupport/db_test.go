package testsupport

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func readAllMigrations(t *testing.T, dir string) string {
	t.Helper()

	files, err := filepath.Glob(filepath.Join(dir, "*.sql"))
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(files)

	var sb strings.Builder
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		sb.Write(raw)
		sb.WriteString("\n")
	}
	return sb.String()
}

// 测试库的入口只认 MEMORA_TEST_DSN：没有配置时跳过，配置不合法时拒绝执行，
// 绝不回退到应用的开发库配置。
func TestParseTestDSNRejectsUnsafeTargets(t *testing.T) {
	cases := []struct {
		name string
		dsn  string
	}{
		{"空串", ""},
		{"只有空格", "   "},
		{"缺少库名", "root:pw@tcp(127.0.0.1:3306)/?charset=utf8mb4"},
		{"指向开发库", "root:pw@tcp(127.0.0.1:3306)/memora?charset=utf8mb4"},
		{"库名只是后缀", "root:pw@tcp(127.0.0.1:3306)/_test?charset=utf8mb4"},
		{"库名后缀不对", "root:pw@tcp(127.0.0.1:3306)/memora_dev?charset=utf8mb4"},
		{"库名含非法字符", "root:pw@tcp(127.0.0.1:3306)/memora_test`;DROP DATABASE memora;--?charset=utf8mb4"},
		{"不是合法 DSN", "这不是一个 DSN"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := ParseTestDSN(tc.dsn)
			if err == nil {
				t.Fatalf("应当被拒绝，却解析出了库 %q", cfg.DBName)
			}
			if cfg != nil {
				t.Fatalf("被拒绝时不应返回配置")
			}
		})
	}
}

func TestParseTestDSNAcceptsTestDatabase(t *testing.T) {
	const dsn = "root:pw@tcp(127.0.0.1:3306)/memora_test?charset=utf8mb4&parseTime=true"

	cfg, err := ParseTestDSN(dsn)
	if err != nil {
		t.Fatalf("合法的测试库被拒绝: %v", err)
	}
	if cfg.DBName != "memora_test" {
		t.Fatalf("解析出的库名是 %q", cfg.DBName)
	}
}

// 指向开发库时必须在做任何事之前就失败——不能先连上去、更不能先建库。
func TestConnectRefusesDevDatabaseBeforeTouchingAnything(t *testing.T) {
	// 故意指向一个连不上的地址：如果校验发生在连接之后，报错会是连接错误而不是库名错误。
	const dsn = "root:pw@tcp(127.0.0.1:1)/memora?charset=utf8mb4"

	db, repo, err := Connect(dsn)
	if err == nil {
		t.Fatal("指向开发库的 DSN 应当被拒绝")
	}
	if db != nil || repo != nil {
		t.Fatal("被拒绝时不应返回连接")
	}
	if !strings.Contains(err.Error(), "_test") {
		t.Fatalf("报错应当说明库名不符合测试库规则，实际是: %v", err)
	}
}

// 迁移里的 USE / CREATE DATABASE 不能被执行：连接必须一直停在测试库上。
func TestSplitStatementsDropsDatabaseSwitching(t *testing.T) {
	statements := splitStatements(`
-- 注释行
create database if not exists memora
    default character set utf8mb4;

use memora;

CREATE DATABASE other;

create table if not exists t (id bigint);
insert into t values (1);
`)

	for _, stmt := range statements {
		lower := strings.ToLower(stmt)
		if strings.HasPrefix(lower, "use ") {
			t.Fatalf("USE 语句没有被丢弃: %q", stmt)
		}
		if strings.HasPrefix(lower, "create database") {
			t.Fatalf("CREATE DATABASE 语句没有被丢弃: %q", stmt)
		}
	}

	if len(statements) != 2 {
		t.Fatalf("应当只剩 2 条语句，实际是 %d 条: %#v", len(statements), statements)
	}
	if !strings.HasPrefix(statements[0], "create table") {
		t.Fatalf("第一条是 %q", statements[0])
	}
}

// 真实仓库里的迁移也必须过这一关：001 里的 use memora; 一旦被执行，
// 后面所有语句都会落到开发库上。
func TestRealMigrationsNeverSwitchDatabase(t *testing.T) {
	dir, err := migrationsDir()
	if err != nil {
		t.Fatal(err)
	}
	if dir == "" {
		t.Fatal("没有找到 migrations 目录")
	}

	// 直接检查语句文本，不需要数据库。
	raw := readAllMigrations(t, dir)
	for _, stmt := range splitStatements(raw) {
		lower := strings.ToLower(strings.TrimSpace(stmt))
		if strings.HasPrefix(lower, "use ") || strings.HasPrefix(lower, "create database") {
			t.Fatalf("迁移里残留了切库语句: %q", stmt)
		}
	}
}
