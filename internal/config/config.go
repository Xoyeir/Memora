package config

import (
	"fmt"
	"os"

	"github.com/joho/godotenv"
)

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
