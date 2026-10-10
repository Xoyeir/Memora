package config

import (
	"fmt"
	"os"
	"time"

	"github.com/joho/godotenv"
)

const defaultTimezone = "Asia/Shanghai"

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
	// Timezone 只把真实时刻换算成业务自然日，不参与 datetime 读写（那是 repository.StorageTimezone）。
	Timezone string
}

func Load() (*Config, error) {
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
