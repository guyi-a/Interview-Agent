package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/guyi-a/Interview-Agent/internal/agent"
	"github.com/guyi-a/Interview-Agent/internal/agent/llm"
	"github.com/guyi-a/Interview-Agent/internal/agent/prompts"
	"github.com/guyi-a/Interview-Agent/internal/agent/tools"
	"github.com/guyi-a/Interview-Agent/internal/config"
	"github.com/guyi-a/Interview-Agent/internal/handler"
	"github.com/guyi-a/Interview-Agent/internal/repository"
	"github.com/guyi-a/Interview-Agent/internal/service"
	"github.com/guyi-a/Interview-Agent/internal/stream"
)

const (
	dbPath          = "interview.db"
	addr            = ":9001"
	shutdownTimeout = 5 * time.Second
)

func corsMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")
		if origin == "http://localhost:5173" || origin == "http://127.0.0.1:5173" {
			c.Header("Access-Control-Allow-Origin", origin)
			c.Header("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
			c.Header("Access-Control-Allow-Headers", "Content-Type")
			c.Header("Access-Control-Max-Age", "600")
		}
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config.Load: %v", err)
	}
	log.Printf("LLM cfg: model=%s thinking=%v", cfg.LLM.Model, cfg.LLM.EnableThinking)

	ctx := context.Background()

	db, err := repository.NewDB(dbPath)
	if err != nil {
		log.Fatalf("repository.NewDB: %v", err)
	}
	convRepo := repository.NewConversationRepo(db)
	msgRepo := repository.NewMessageRepo(db)

	cm, err := llm.NewChatModel(ctx, cfg.LLM)
	if err != nil {
		log.Fatalf("llm.NewChatModel: %v", err)
	}

	ts, err := tools.Builtin(ctx)
	if err != nil {
		log.Fatalf("tools.Builtin: %v", err)
	}

	ag, err := agent.NewReActAgent(ctx, cm, ts, prompts.General)
	if err != nil {
		log.Fatalf("agent.NewReActAgent: %v", err)
	}

	manager := stream.NewManager()
	chatService := service.NewChatService(ag, manager, convRepo, msgRepo)
	convService := service.NewConversationService(convRepo, msgRepo, manager)

	chatHandler := handler.NewChatHandler(chatService)
	convHandler := handler.NewConversationHandler(convService)

	r := gin.Default()
	r.Use(corsMiddleware())
	chatHandler.Register(r)
	convHandler.Register(r)

	srv := &http.Server{Addr: addr, Handler: r}

	serverErr := make(chan error, 1)
	go func() {
		log.Printf("listening on %s db=%s", addr, dbPath)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
		close(serverErr)
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	select {
	case err := <-serverErr:
		if err != nil {
			log.Fatalf("server error: %v", err)
		}
	case sig := <-quit:
		log.Printf("received %s, shutting down...", sig)
	}

	manager.ShutdownAll()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("http shutdown: %v", err)
	}

	if sqlDB, err := db.DB(); err == nil {
		_ = sqlDB.Close()
	}

	log.Print("shutdown complete")
}
