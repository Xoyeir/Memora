package repository

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"

	"github.com/Xoyeir/Memora/internal/config"
)

func testConfig(tz string) *config.Config {
	return &config.Config{
		DBHost: "127.0.0.1", DBPort: "3306",
		DBUser: "root", DBPassword: "pw", DBName: "memora",
		Timezone: tz,
	}
}

func mustBuildDSN(t *testing.T, cfg *config.Config) string {
	t.Helper()
	loc, err := storageLocation()
	if err != nil {
		t.Fatal(err)
	}
	dsn, err := buildDSN(cfg, loc)
	if err != nil {
		t.Fatal(err)
	}
	return dsn
}

// 读库用的时区是存储语义，跟 APP_TIMEZONE 无关。
//
// APP_TIMEZONE 只回答「哪一刻算哪一天」。如果它也参与解读 datetime，
// 那么改一次日期归属的配置就会顺手把全部历史时间重新解释一遍。
func TestBuildDSNIgnoresAppTimezone(t *testing.T) {
	for _, appTZ := range []string{"Asia/Shanghai", "America/New_York", "Australia/Sydney", "UTC"} {
		t.Run(appTZ, func(t *testing.T) {
			dsn := mustBuildDSN(t, testConfig(appTZ))

			if strings.Contains(dsn, "loc=Local") {
				t.Fatalf("解析时区交给了进程: %s", dsn)
			}

			parsed, err := mysql.ParseDSN(dsn)
			if err != nil {
				t.Fatalf("生成的 DSN 自己都解析不了: %v", err)
			}
			if parsed.Loc.String() != StorageTimezone {
				t.Fatalf("APP_TIMEZONE=%s 时解析时区变成了 %q，期望 %q",
					appTZ, parsed.Loc.String(), StorageTimezone)
			}
			// 名字相同也可能是各自 LoadLocation 出来的副本，这里只要求语义一致。
			wall := time.Date(2026, 10, 8, 20, 0, 0, 0, parsed.Loc)
			if !wall.Equal(time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)) {
				t.Fatalf("APP_TIMEZONE=%s 时墙上时间的解释变了: %s", appTZ, wall.UTC())
			}
		})
	}
}

// 会话时区必须逐连接设置：连接池会不停替换物理连接，
// 只在某一条上 SET 一次，之后新建的连接就漏掉了。
func TestBuildDSNPinsSessionTimezonePerConnection(t *testing.T) {
	dsn := mustBuildDSN(t, testConfig("America/New_York"))

	parsed, err := mysql.ParseDSN(dsn)
	if err != nil {
		t.Fatal(err)
	}

	// 驱动会把 DSN 里的 time_zone 拼进每条连接的初始化语句。
	want := "'+08:00'"
	if got := parsed.Params["time_zone"]; got != want {
		t.Fatalf("DSN 里的会话时区是 %q，期望 %q", got, want)
	}
}

// 存储时区必须没有夏令时：会话时区是用固定偏移表达的，
// 一旦这个时区会随日期换偏移，那个固定值就会在半年里写错时间。
func TestStorageTimezoneHasNoDST(t *testing.T) {
	loc, err := storageLocation()
	if err != nil {
		t.Fatal(err)
	}

	_, jan := time.Date(2026, 1, 15, 12, 0, 0, 0, loc).Zone()
	_, jul := time.Date(2026, 7, 15, 12, 0, 0, 0, loc).Zone()
	if jan != jul {
		t.Fatalf("存储时区 %s 有夏令时（1 月 %d 秒、7 月 %d 秒），固定偏移的会话时区不再安全",
			StorageTimezone, jan, jul)
	}
}

// 存储时区是数据在磁盘上的语义，与机器 Local 无关。
func TestStorageTimezoneIgnoresProcessLocal(t *testing.T) {
	loc, err := storageLocation()
	if err != nil {
		t.Fatal(err)
	}
	if loc == time.Local {
		t.Fatal("存储时区退化成了 time.Local")
	}
	if loc.String() != StorageTimezone {
		t.Fatalf("存储时区是 %q", loc.String())
	}
}

// 换一台时区不同的机器时，历史数据必须仍然指向同一个时刻。
func TestStoredWallTimeKeepsSameInstant(t *testing.T) {
	loc, err := storageLocation()
	if err != nil {
		t.Fatal(err)
	}

	// 数据库里存的就是这串墙上时间，代表 2026-10-08 20:00 上海时间。
	wall := time.Date(2026, 10, 8, 20, 0, 0, 0, loc)
	want := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	if !wall.Equal(want) {
		t.Fatalf("墙上时间被解读成 %s，期望 %s", wall.UTC(), want)
	}
}

// 从环境里的测试库连接串取出地址和凭据，时间语义交给代码。
func testConfigFromEnv(t *testing.T, appTZ string) *config.Config {
	t.Helper()

	dsn := os.Getenv("MEMORA_TEST_DSN")
	if strings.TrimSpace(dsn) == "" {
		t.Skip("未设置 MEMORA_TEST_DSN，跳过需要真实数据库的测试")
	}
	parsed, err := mysql.ParseDSN(dsn)
	if err != nil {
		t.Fatalf("MEMORA_TEST_DSN 不是合法的 DSN: %v", err)
	}
	host, port, found := strings.Cut(parsed.Addr, ":")
	if !found {
		t.Fatalf("连接串里的地址缺少端口: %q", parsed.Addr)
	}
	if !strings.HasSuffix(parsed.DBName, "_test") {
		t.Fatalf("只在独立测试库上跑，当前库名是 %q", parsed.DBName)
	}

	return &config.Config{
		DBHost: host, DBPort: port,
		DBUser: parsed.User, DBPassword: parsed.Passwd, DBName: parsed.DBName,
		Timezone: appTZ,
	}
}

// 探针：写入一个固定的墙上时间，再用不同 APP_TIMEZONE 的连接读回同一个时刻。
//
// 这条断言的是「真实时刻不随配置变化」。日期怎么随目标时区变化由
// service 侧的 TestStoredRecordDateFollowsAppTimezone 覆盖。
func TestStoredDateTimeKeepsInstantWhenAppTimezoneChanges(t *testing.T) {
	const wallLiteral = "2026-10-08 20:00:00"
	want := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)

	read := func(appTZ string) time.Time {
		t.Helper()

		cfg := testConfigFromEnv(t, appTZ)
		db, err := sql.Open("mysql", mustBuildDSN(t, cfg))
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()

		if _, err := db.Exec("DELETE FROM chat_messages"); err != nil {
			t.Fatalf("清理测试表失败: %v", err)
		}
		if _, err := db.Exec(
			"INSERT INTO chat_messages (role, content, status, created_at) VALUES ('user', '历史记录', 'ok', ?)",
			wallLiteral); err != nil {
			t.Fatalf("写入历史记录失败: %v", err)
		}

		repo := FromDB(db)
		msgs, err := repo.ListChatMessages(context.Background(), 0, 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(msgs) != 1 {
			t.Fatalf("读回 %d 条记录", len(msgs))
		}
		return msgs[0].CreatedAt
	}

	for _, appTZ := range []string{"Asia/Shanghai", "America/New_York", "UTC", "Pacific/Auckland"} {
		got := read(appTZ)
		if !got.Equal(want) {
			t.Fatalf("APP_TIMEZONE=%s 时同一条历史记录被解读成 %s，期望 %s",
				appTZ, got.UTC().Format(time.RFC3339), want.Format(time.RFC3339))
		}
		if _, offset := got.Zone(); offset != 8*60*60 {
			t.Fatalf("APP_TIMEZONE=%s 时读回的偏移是 %d 秒，期望 +08:00", appTZ, offset)
		}
	}
}

// 连接池里每一条物理连接都必须带着存储时区，而不只是第一条。
func TestEveryPooledConnectionCarriesStorageSessionTimezone(t *testing.T) {
	cfg := testConfigFromEnv(t, "America/New_York")

	db, err := sql.Open("mysql", mustBuildDSN(t, cfg))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	const poolSize = 4
	db.SetMaxOpenConns(poolSize)
	db.SetMaxIdleConns(0)

	ctx := context.Background()

	// 同时持有 poolSize 条连接，它们必然是 poolSize 条不同的物理连接。
	conns := make([]*sql.Conn, 0, poolSize)
	for i := 0; i < poolSize; i++ {
		c, err := db.Conn(ctx)
		if err != nil {
			t.Fatalf("取第 %d 条连接失败: %v", i, err)
		}
		defer c.Close()
		conns = append(conns, c)
	}

	seen := map[int64]bool{}
	for i, c := range conns {
		var id int64
		if err := c.QueryRowContext(ctx, "SELECT CONNECTION_ID()").Scan(&id); err != nil {
			t.Fatalf("第 %d 条连接读连接号失败: %v", i, err)
		}
		seen[id] = true

		var offset int
		if err := c.QueryRowContext(ctx,
			"SELECT TIMESTAMPDIFF(SECOND, UTC_TIMESTAMP(), NOW())").Scan(&offset); err != nil {
			t.Fatalf("第 %d 条连接读会话时区失败: %v", i, err)
		}
		if offset != 8*60*60 {
			t.Fatalf("第 %d 条连接（连接号 %d）的会话偏移是 %d 秒，期望 +08:00", i, id, offset)
		}
	}
	if len(seen) != poolSize {
		t.Fatalf("只看到 %d 条不同的物理连接，期望 %d 条", len(seen), poolSize)
	}

	// 释放之后重新取连接，新建立的连接同样要带上。
	if err := conns[0].Close(); err != nil {
		t.Fatal(err)
	}
	var offset int
	if err := db.QueryRowContext(ctx,
		"SELECT TIMESTAMPDIFF(SECOND, UTC_TIMESTAMP(), NOW())").Scan(&offset); err != nil {
		t.Fatal(err)
	}
	if offset != 8*60*60 {
		t.Fatalf("之后新建的连接会话偏移是 %d 秒，期望 +08:00", offset)
	}
}
