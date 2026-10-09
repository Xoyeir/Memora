package config

import (
	"fmt"
	"os"
	"time"

	"github.com/joho/godotenv"
)

// defaultTimezone 是 APP_TIMEZONE 未配置时的缺省时区。
const defaultTimezone = "Asia/Shanghai"

// Config 保存应用运行所需的全部配置，全部来自环境变量。
type Config struct {
	ServerPort string
	DBHost     string
	DBPort     string
	DBUser     string
	DBPassword string
	DBName     string
	LLMBaseURL string
	LLMAPIKey  string
	LLMModel   string
	// Timezone 只负责一件事：把一个真实时刻换算成业务意义上的自然日。
	//
	// 它不参与 datetime 的读或写——那是 repository.StorageTimezone 的事。
	// 两者分开之后，改这个值只会让「哪一刻算哪一天」变化，
	// 不会重新解释已经存好的时间。
	//
	// 之所以叫 APP_TIMEZONE 而不是 TZ：TZ 是 Go 进程自己会读的环境变量，
	// 设了它会连带改掉 time.Local，是历史时间被静默偏移的来源。
	Timezone string
}

// Load 读取 .env 文件并组装配置。任何必填项缺失都会直接返回 error。
func Load() (*Config, error) {
	// 把 .env 文件加载进进程环境变量。
	// 忽略错误：如果 .env 不存在
	// 下面的必填项校验会兜底，给出明确报错。
	_ = godotenv.Load()

	cfg := &Config{
		ServerPort: getEnv("SERVER_PORT", "8080"),
		DBHost:     getEnv("DB_HOST", "127.0.0.1"),
		DBPort:     getEnv("DB_PORT", "3306"),
		DBUser:     getEnv("DB_USER", "root"),
		DBPassword: os.Getenv("DB_PASSWORD"),
		DBName:     os.Getenv("DB_NAME"),
		LLMBaseURL: os.Getenv("LLM_BASE_URL"),
		LLMAPIKey:  os.Getenv("LLM_API_KEY"),
		LLMModel:   getEnv("LLM_MODEL", "deepseek-chat"),
		Timezone:   getEnv("APP_TIMEZONE", defaultTimezone),
	}

	if cfg.DBPassword == "" {
		return nil, fmt.Errorf("缺少必填环境变量 DB_PASSWORD")
	}
	if cfg.DBName == "" {
		return nil, fmt.Errorf("缺少必填环境变量 DB_NAME")
	}
	if cfg.LLMBaseURL == "" {
		return nil, fmt.Errorf("缺少必填环境变量 LLM_BASE_URL")
	}
	if cfg.LLMAPIKey == "" {
		return nil, fmt.Errorf("缺少必填环境变量 LLM_API_KEY")
	}
	if _, err := time.LoadLocation(cfg.Timezone); err != nil {
		return nil, fmt.Errorf("APP_TIMEZONE=%q 不是合法的 IANA 时区: %w", cfg.Timezone, err)
	}

	return cfg, nil
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func (c *Config) MaskedAPIKey() string {
	k := c.LLMAPIKey
	if len(k) <= 8 {
		return "****"
	}
	return k[:4] + "****" + k[len(k)-4:]
}
