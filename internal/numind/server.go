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
	bizLayer := biz.NewBiz(store.S)
	if err := installNumindRouters(g, bizLayer); err != nil {
		return fmt.Errorf("failed to install routers: %w", err)
	}
	bizLayer.StartExternalResumeReclaimer()

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

	// Lifecycle context for background goroutines (crons, sweepers). Cancelled
	// at the end of startServer so all background loops exit cleanly on shutdown.
	lifecycleCtx, lifecycleCancel := context.WithCancel(context.Background())
	defer lifecycleCancel()

	// Start monitor scheduler
	go func() {
		if err := bizLayer.Monitor().StartScheduler(context.Background()); err != nil {
			log.Printf("Failed to start monitor scheduler: %v", err)
		}
	}()

	// Start hourly cron for order expiry cleanup (credit package lifecycle cron removed in Task 16)
	go func() {
		ticker := time.NewTicker(1 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
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

	// Reservation sweeper: protects against credit loss on server crash mid-Reserve.
	// Audit P0-2 — see docs/superpowers/specs/2026-05-18-credits-system-audit.md.
	// Reserve deducts credits immediately; defer FinalizeReservation handles
	// Reconcile/Refund. If the server dies between those two steps, the
	// credit_reservation row stays status='reserved' forever and the user's
	// credits are silently lost. The sweeper finds rows older than the stale
	// threshold and refunds them via the existing audit-trail path.
	sweeper := credit.NewReservationSweeper(store.S, bizLayer.CreditService(), credit.DefaultReservationSweeperConfig())
	go sweeper.Run(lifecycleCtx)

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
	bizLayer.CloseExternalResumeReclaimer(ctx)
	log.Println("Server exited")
	return nil
}
