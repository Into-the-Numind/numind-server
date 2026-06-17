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

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/aiservice"
	"numind-server/internal/pkg/aiservice/capability"
	"numind-server/internal/pkg/aiservice/registry"
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

	// 初始化 AI Service Gateway 单例。
	// admin server 历史上不需要 AI（纯管理 API），故从不调 SetDefault；
	// rag-eval 检索调试端点是首个会走检索栈（embed → 向量检索 → rerank）的 admin 路由，
	// 触发 aiservice.Default() → panic("called before SetDefault")，且 embed 在 parallelSearch
	// 的 goroutine 里跑，gin.Recovery 兜不住 → 整个进程崩。
	// 这里挂一个最小 gateway：registry + providers + SetDefault，**不挂** billing/budget/langfuse
	// middleware（gateway 对 nil chain 有兜底）。后果：admin 进程里发出的 AI 调用既不计费、不预扣
	// 积分，也不进 Langfuse trace —— 这对 rag-eval（只读评估工具）是预期且正确的，但若将来有人在
	// admin 端加面向用户的 AI 流程，务必改走带 middleware 的完整 gateway，否则会漏计费/漏追踪。
	// provider 凭据由 user server 启动时已同步进 llm_provider 表，registry 从 DB 读取；
	// 此处仍非致命地 sync 一次保证 admin 自洽（SetDefault 是进程内单例，与 user server 互不影响）。
	{
		capability.Init(store.S.DB()) // 与 user server 一致；GetCapabilities 在未 Init 时返回 error 不 panic，此处为防御性对齐
		reg := registry.New(store.S.DB())
		gw := aiservice.Build(aiservice.Deps{Registry: reg})
		registerAIProviders(gw)
		aiservice.SetDefault(gw)
		log.Infow("AI Service Gateway initialised (admin, no-billing-middleware)", "adapters", gw.AdapterNames())
		if err := aiservice.SyncProviderCredentials(context.Background(), store.S.DB(), viper.GetViper()); err != nil {
			log.Errorw("Failed to sync AI provider credentials (admin), continuing", "error", err)
		}
	}

	// Task 13 / T9: validate billing.b2b_cutover_date is configured.
	// Post-T9 the B2B billing report always uses new_only source regardless of
	// cutover_date; this warning is retained as a config-hygiene reminder.
	if viper.GetString("billing.b2b_cutover_date") == "" {
		log.Warnw("billing.b2b_cutover_date is not set — B2B billing report defaults to new_only (T9); set this in config.yaml for observability/audit clarity")
	}

	// 设置 Gin 模式
	gin.SetMode(viper.GetString("runmode"))

	// 创建 Gin 引擎
	g := gin.New()

	mws := []gin.HandlerFunc{gin.Recovery(), mw.MaintenanceMode(), mw.NoCache, mw.Cors, mw.Secure, mw.RequestID()}
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
