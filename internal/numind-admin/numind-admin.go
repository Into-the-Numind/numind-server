package numindadmin

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

var cfgFile string

// NewNumindAdminCommand 创建一个 *cobra.Command 对象
func NewNumindAdminCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "numind-admin",
		Short: "Numind Admin Management System",
		Long:  `Numind Admin Management System, used to manage users, books, images, orders, payments, templates, and system configs.`,

		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			verflag.PrintAndExitIfRequested()

			// 初始化日志
			log.Init(logOptions())
			defer log.Sync()

			return run()
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

// run 函数是实际的业务代码入口函数
func run() error {
	// 服务启动打印banner
	banner := figure.NewColorFigure("Numind Admin", "", "cyan", true)
	banner.Print()

	// 初始化 store 层
	if err := initStore(); err != nil {
		return err
	}

	// 初始化系统配置（从数据库加载）
	if err := initSystemConfigs(); err != nil {
		log.Warnw("Failed to init system configs from database, will use yaml config", "err", err)
	}

	// 初始化 COS（如果已配置）
	InitCOS()

	// 设置 Gin 模式
	gin.SetMode(viper.GetString("runmode"))

	// 创建 Gin 引擎
	g := gin.New()

	// 中间件
	mws := []gin.HandlerFunc{gin.Recovery(), mw.NoCache, mw.Cors, mw.Secure, mw.RequestID()}
	g.Use(mws...)

	if err := installAdminRouters(g); err != nil {
		return err
	}

	// 创建并运行 HTTP 服务器
	httpsrv := startInsecureServer(g)

	// 等待中断信号优雅地关闭服务器
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Infow("Shutting down server ...")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := httpsrv.Shutdown(ctx); err != nil {
		log.Errorw("Server forced to shutdown", "err", err)
		return err
	}

	log.Infow("Server exiting")
	return nil
}

// startInsecureServer 创建并运行 HTTP 服务器
func startInsecureServer(g *gin.Engine) *http.Server {
	addr := viper.GetString("admin.addr")
	if addr == "" {
		addr = ":9099" // 默认后台管理端口
	}

	httpsrv := &http.Server{Addr: addr, Handler: g}

	log.Infow("Start to listening the incoming requests on http address", "addr", addr)
	go func() {
		if err := httpsrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalw(err.Error())
		}
	}()

	return httpsrv
}
