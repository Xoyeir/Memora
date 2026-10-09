package config

import (
	"strings"
	"testing"
)

func setRequiredEnv(t *testing.T) {
	t.Helper()
	t.Setenv("DB_PASSWORD", "test-password")
	t.Setenv("DB_NAME", "memora")
	t.Setenv("LLM_BASE_URL", "http://127.0.0.1:1")
	t.Setenv("LLM_API_KEY", "sk-test")
}

func TestLoadDefaultsTimezone(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("APP_TIMEZONE", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("加载配置失败: %v", err)
	}
	if cfg.Timezone != "Asia/Shanghai" {
		t.Fatalf("缺省时区是 %q，期望 Asia/Shanghai", cfg.Timezone)
	}
}

func TestLoadRejectsInvalidTimezone(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("APP_TIMEZONE", "Mars/Olympus")

	_, err := Load()
	if err == nil {
		t.Fatal("非法时区应当让加载失败")
	}
	if !strings.Contains(err.Error(), "APP_TIMEZONE") {
		t.Fatalf("报错信息没有点明是哪个变量: %v", err)
	}
}

// 日期归属必须由 APP_TIMEZONE 控制，而不是进程的 TZ。
// TZ 会连带改掉 time.Local 和 DSN 里的 loc=Local，那是历史时间被静默偏移的来源。
func TestTimezoneIgnoresProcessTZ(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("APP_TIMEZONE", "")
	t.Setenv("TZ", "America/New_York")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("加载配置失败: %v", err)
	}
	if cfg.Timezone != "Asia/Shanghai" {
		t.Fatalf("进程 TZ 影响到了日期归属配置，当前是 %q", cfg.Timezone)
	}
}
