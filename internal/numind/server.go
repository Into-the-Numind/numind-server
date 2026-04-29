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

	"numind-server/internal/numind/biz"
	"numind-server/internal/numind/biz/credit"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/middleware"
)

func startServer() error {
	// 这里可以初始化数据库、服务、定时任务等
	g := gin.Default()
	// Register maintenance mode middleware before routes (and JWT)
	g.Use(middleware.MaintenanceMode())
	if err := installNumindRouters(g); err != nil {
		return fmt.Errorf("failed to install routers: %w", err)
	}

	port := viper.GetString("server.port")
	if port == "" {
		port = "9091"
	}
	srv := &http.Server{
		Addr:    ":" + port,
		Handler: g,
	}

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	// Start monitor scheduler
	bizLayer := biz.NewBiz(store.S)
	go func() {
		if err := bizLayer.Monitor().StartScheduler(context.Background()); err != nil {
			log.Printf("Failed to start monitor scheduler: %v", err)
		}
	}()

	// Start hourly cron for credit package lifecycle management
	creditBiz := credit.NewCreditBiz(store.S)
	go func() {
		ticker := time.NewTicker(1 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				creditBiz.RunCronTasks(ctx)
				// 关闭过期未支付订单
				if count, err := store.S.Orders().CloseExpiredOrders(ctx); err != nil {
					log.Printf("Close expired orders error: %v", err)
				} else if count > 0 {
					log.Printf("Closed %d expired orders", count)
				}
				cancel()
			case <-quit:
				return
			}
		}
	}()

	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Failed to start server: %v", err)
		}
	}()

	log.Printf("Server started on port %s", port)

	<-quit

	log.Println("Shutting down server...")

	// Stop monitor scheduler
	bizLayer.Monitor().StopScheduler()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Fatal("Server forced to shutdown:", err)
	}
	log.Println("Server exited")
	return nil
}
