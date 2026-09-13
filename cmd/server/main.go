package main

import (
	"log"

	"github.com/Xoyeir/memora/internal/config"
	"github.com/Xoyeir/memora/internal/handler"
	"github.com/Xoyeir/memora/internal/llm"
	"github.com/Xoyeir/memora/internal/repository"
	"github.com/Xoyeir/memora/internal/router"
	"github.com/Xoyeir/memora/internal/service"
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

	llmClient := llm.NewClient(cfg)
	svc := service.NewService(repo, llmClient)

	journalHandler := handler.NewJournalHandler(svc)
	r := router.Setup(journalHandler)

	if err := r.Run(":" + cfg.ServerPort); err != nil {
		log.Fatalf("启动服务失败: %v", err)
	}
}
