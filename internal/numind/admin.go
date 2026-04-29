package numind

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"numind-server/internal/pkg/log"
	mw "numind-server/internal/pkg/middleware"
	"numind-server/pkg/version/verflag"

	"github.com/common-nighthawk/go-figure"
)

// NewAdminCommand 创建管理后台 *cobra.Command 对象
func NewAdminCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "numind-admin",
		Short: "Numind Admin Server",
		Long:  "Numind Admin Server - 管理后台API服务",

		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			verflag.PrintAndExitIfRequested()

			log.Init(logOptions())
			defer log.Sync()

			return runAdmin()
		},
		Args: func(cmd *cobra.Command, args []string) error {
			for _, arg := range args {
				if len(arg) > 0 {
					return fmt.Errorf("%q does not take any arguments, got %q", cmd.CommandPath(), args)
				}
			}
			return nil
		},
	}

	cobra.OnInitialize(initConfig)

	cmd.PersistentFlags().StringVarP(&cfgFile, "config", "c", "", "The path to the numind configuration file. Empty string for no configuration file.")
	cmd.Flags().BoolP("toggle", "t", false, "Help message for toggle")

	verflag.AddFlags(cmd.PersistentFlags())

	return cmd
}

// runAdmin 管理后台服务入口
func runAdmin() error {
	banner := figure.NewColorFigure("Numind Admin", "", "green", true)
	banner.Print()

	// 初始化 store 层
	if err := initStore(); err != nil {
		return err
	}

	// Task 13: validate billing.b2b_cutover_date is configured.
	// Without this date the B2B billing report falls back to legacy_only for all
	// months, which is incorrect once the credits-system is live.
	if viper.GetString("billing.b2b_cutover_date") == "" {
		log.Warnw("billing.b2b_cutover_date is not set — B2B billing report will use legacy_only source for ALL months; configure this in config.yaml once membership_event is the authoritative source")
	}

	// 设置 Gin 模式
	gin.SetMode(viper.GetString("runmode"))

	// 创建 Gin 引擎
	g := gin.New()

	mws := []gin.HandlerFunc{gin.Recovery(), mw.NoCache, mw.Cors, mw.Secure, mw.RequestID()}
	g.Use(mws...)

	if err := installAdminRouters(g); err != nil {
		return err
	}

	// 获取管理后台监听地址
	addr := viper.GetString("admin.addr")
	if addr == "" {
		addr = ":9099"
	}

	httpsrv := &http.Server{
		Addr:         addr,
		Handler:      g,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	log.Infow("Start to listening the incoming requests on admin http address", "addr", addr)
	go func() {
		if err := httpsrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalw(err.Error())
		}
	}()

	// 等待中断信号优雅关闭
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Infow("Shutting down admin server ...")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := httpsrv.Shutdown(ctx); err != nil {
		log.Errorw("Admin server forced to shutdown", "err", err)
		return err
	}

	log.Infow("Admin server exiting")
	return nil
}
