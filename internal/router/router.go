package router

import (
	"github.com/gin-gonic/gin"

	"github.com/Xoyeir/Memora/internal/handler"
)

func Setup(jh *handler.JournalHandler, ch *handler.ChatHandler, lh *handler.LifeHandler,
	calh *handler.CalendarHandler, mh *handler.MemoryHandler) *gin.Engine {
	r := gin.Default()

	api := r.Group("/api")
	{
		api.POST("/journals", jh.Create)
		api.GET("/journals", jh.List)
		api.GET("/chat/messages", ch.History)
		api.POST("/chat", ch.Chat)
		api.POST("/chat/stream", ch.ChatStream)
		api.POST("/chat/formation/recover", ch.RecoverFormation)
		api.GET("/life/records", lh.List)

		api.GET("/calendar/month", calh.Month)
		api.GET("/calendar/days/:day", calh.Day)
		api.POST("/calendar/days/:day/summarize", calh.Summarize)
		api.PUT("/calendar/days/:day/label", calh.SetLabel)
		api.DELETE("/calendar/days/:day/label", calh.ClearLabel)

		api.GET("/memories", mh.List)
		api.GET("/memories/:id/sources", mh.Sources)
		api.PUT("/memories/:id/content", mh.Correct)
		api.DELETE("/memories/:id", mh.Forget)
	}

	return r
}
