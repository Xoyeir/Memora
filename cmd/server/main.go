package main

import (
	"log"

	"github.com/Xoyeir/Memora/internal/config"
	"github.com/Xoyeir/Memora/internal/handler"
	"github.com/Xoyeir/Memora/internal/llm"
	"github.com/Xoyeir/Memora/internal/repository"
	"github.com/Xoyeir/Memora/internal/router"
	"github.com/Xoyeir/Memora/internal/service"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("加载配置失败: %v", err)
	}

	repo, err := repository.Connect(cfg)
	if err != nil {
		log.Fatalf("连接数据库失败: %v", err)
	}
	defer repo.Close()

	clock, err := service.NewClock(cfg.Timezone)
	if err != nil {
		log.Fatalf("初始化时区失败: %v", err)
	}

	llmClient := llm.NewClient(cfg)
	svc := service.NewService(repo, llmClient, clock)

	journalHandler := handler.NewJournalHandler(svc)
	chatHandler := handler.NewChatHandler(svc)
	r := router.Setup(journalHandler, chatHandler)

	if err := r.Run(":" + cfg.ServerPort); err != nil {
		log.Fatalf("启动服务失败: %v", err)
	}
}
