package numind

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/spf13/viper"
)

func startServer() error {
	// 这里可以初始化数据库、服务、定时任务等
	g := gin.Default()
	if err := installNumindRouters(g); err != nil {
		return fmt.Errorf("failed to install routers: %w", err)
	}

	port := viper.GetString("server.port")
	if port == "" {
		port = "8000"
	}
	srv := &http.Server{
		Addr:    ":" + port,
		Handler: g,
	}

	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Failed to start server: %v", err)
		}
	}()

	log.Printf("Server started on port %s", port)

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("Shutting down server...")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Fatal("Server forced to shutdown:", err)
	}
	log.Println("Server exited")
	return nil
}
