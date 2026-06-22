package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	"github.com/aidenai-com/aidenai-go-chat/internal"
)

func main() {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	rdb := redis.NewClient(&redis.Options{
		Addr:     getEnv("REDIS_ADDR", "localhost:6379"),
		Password: getEnv("REDIS_PASSWORD", ""),
	})
	if err := rdb.Ping(context.Background()).Err(); err != nil {
		logger.Warn("Redis unavailable — history disabled", zap.Error(err))
		rdb = nil
	}

	hub := internal.NewHub(rdb, logger)
	go hub.Run()

	if getEnv("GIN_MODE", "debug") == "release" {
		gin.SetMode(gin.ReleaseMode)
	}

	r := gin.Default()
	r.Use(corsMiddleware())

	r.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "healthy", "ts": time.Now().UTC()})
	})
	r.GET("/ws/:roomID", func(c *gin.Context) {
		internal.ServeWS(hub, c.Param("roomID"), c.Writer, c.Request, logger)
	})
	r.GET("/rooms/:roomID/history", func(c *gin.Context) {
		if rdb == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "history unavailable"})
			return
		}
		msgs, err := internal.GetHistory(context.Background(), rdb, c.Param("roomID"), 50)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"room": c.Param("roomID"), "messages": msgs})
	})

	srv := &http.Server{Addr: ":" + getEnv("PORT", "8080"), Handler: r}
	go func() {
		logger.Info("Chat service starting", zap.String("addr", srv.Addr))
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	logger.Info("Shutting down gracefully...")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	srv.Shutdown(ctx) //nolint:errcheck
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func corsMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Writer.Header().Set("Access-Control-Allow-Origin",  "*")
		c.Writer.Header().Set("Access-Control-Allow-Methods", "GET,POST,OPTIONS")
		c.Writer.Header().Set("Access-Control-Allow-Headers", "Content-Type,Authorization")
		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}
