package router

import (
	"github.com/gin-gonic/gin"

	"github.com/Xoyeir/memora/internal/handler"
)

func Setup(jh *handler.JournalHandler, ch *handler.ChatHandler) *gin.Engine {
	r := gin.Default()

	api := r.Group("/api")
	{
		api.POST("/journals", jh.Create)
		api.GET("/journals", jh.List)
		api.POST("/chat", ch.Chat)
		api.POST("/chat/stream", ch.ChatStream)
	}

	return r
}
