package web

import (
	"net/http"
	"time"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"

	"talk-to-ugur-back/web/handlers"
	"talk-to-ugur-back/web/middleware"
)

func (s *Server) makeRoutes() *gin.Engine {
	eng := gin.New()
	eng.Use(gin.Recovery())
	eng.Use(gin.Logger())
	eng.Use(s.getCorsMiddleware())

	eng.Static("/assets", "./assets")
	eng.Static("/emotions", "./assets/emotions")

	eng.GET("/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"status":  "ok",
			"uptime":  time.Since(s.startTime).String(),
			"version": "v1",
		})
	})
	eng.GET("/ready", func(c *gin.Context) {
		if s.ready.Load() {
			c.JSON(http.StatusOK, gin.H{
				"status":  "ready",
				"uptime":  time.Since(s.startTime).String(),
				"version": "v1",
			})
			return
		}
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"status": "not_ready",
		})
	})

	apiV1 := eng.Group("/api/v1")
	apiV1.Use(middleware.RateLimitMiddleware(s.limiter))
	chatHandlers := handlers.NewChatHandler(s.dbQueries, s.aiClient, s.cfg)
	chatGroup := apiV1.Group("/chat")
	apiV1.POST("/visitors", chatHandlers.HandleCreateVisitor)
	chatGroup.POST("/messages", chatHandlers.HandleSendMessage)
	chatGroup.GET("/threads/:thread_id/messages", chatHandlers.HandleGetMessages)

	return eng
}

func (s *Server) getCorsMiddleware() gin.HandlerFunc {
	cfg := cors.DefaultConfig()
	cfg.AllowOrigins = s.cfg.AllowedCorsOrigins
	cfg.AllowHeaders = []string{
		"Accept",
		"Authorization",
		"Content-Type",
		"User-Agent",
		"x-requested-with",
		"X-Visitor-Id",
	}
	cfg.ExposeHeaders = []string{
		"X-Visitor-Id",
	}
	return cors.New(cfg)
}
