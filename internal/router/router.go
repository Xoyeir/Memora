package router

import (
	"github.com/gin-gonic/gin"

	"github.com/Xoyeir/memora/internal/handler"
)

func Setup(h *handler.JournalHandler) *gin.Engine {
	r := gin.Default()

	api := r.Group("/api")
	{
		api.POST("/journals", h.Create)
	}

	return r
}
