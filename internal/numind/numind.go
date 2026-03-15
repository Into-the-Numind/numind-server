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

	"numind-server/internal/numind/biz"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/billing"
	"numind-server/internal/pkg/langfuse"
	"numind-server/internal/pkg/log"
	mw "numind-server/internal/pkg/middleware"
	"numind-server/pkg/version/verflag"

	"github.com/common-nighthawk/go-figure"
)

var cfgFile string

// NewNumindCommand 创建一个 *cobra.Command 对象. 之后，可以使用 Command 对象的 Execute 方法来启动应用程序.
func NewNumindCommand() *cobra.Command {
	cmd := &cobra.Command{
		// 指定命令的名字，该名字会出现在帮助信息中
		Use: "numind",
		// 命令的简短描述
		Short: "A good Go practical project",
		// 命令的详细描述
		Long: `A good Go practical project, used to create user with basic information.`,

		// 命令出错时，不打印帮助信息。不需要打印帮助信息，设置为 true 可以保持命令出错时一眼就能看到错误信息
		SilenceUsage: true,
		// 指定调用 cmd.Execute() 时，执行的 Run 函数，函数执行失败会返回错误信息
		RunE: func(cmd *cobra.Command, args []string) error {
			// 如果 `--version=true`，则打印版本并退出
			verflag.PrintAndExitIfRequested()

			// 初始化日志
			log.Init(logOptions())
			defer log.Sync() // Sync 将缓存中的日志刷新到磁盘文件中

			return run()
		},
		// 这里设置命令运行时，不需要指定命令行参数
		Args: func(cmd *cobra.Command, args []string) error {
			for _, arg := range args {
				if len(arg) > 0 {
					return fmt.Errorf("%q does not take any arguments, got %q", cmd.CommandPath(), args)
				}
			}

			return nil
		},
	}

	// 以下设置，使得 initConfig 函数在每个命令运行时都会被调用以读取配置
	cobra.OnInitialize(initConfig)

	// 在这里您将定义标志和配置设置。

	// Cobra 支持持久性标志(PersistentFlag)，该标志可用于它所分配的命令以及该命令下的每个子命令
	cmd.PersistentFlags().StringVarP(&cfgFile, "config", "c", "", "The path to the numind configuration file. Empty string for no configuration file.")

	// Cobra 也支持本地标志，本地标志只能在其所绑定的命令上使用
	cmd.Flags().BoolP("toggle", "t", false, "Help message for toggle")

	// 添加 --version 标志
	verflag.AddFlags(cmd.PersistentFlags())

	return cmd
}

// run 函数是实际的业务代码入口函数.
func run() error {
	// #region agent log
	log.Infow("[DEBUG] run() function entry", "hypothesisId", "A", "location", "numind.go:79", "runId", "startup")
	// #endregion
	// 服务启动打印banner
	banner := figure.NewColorFigure("Numind Server", "", "green", true)
	banner.Print()

	// 初始化 store 层
	// #region agent log
	log.Infow("[DEBUG] Before initStore", "hypothesisId", "A", "location", "numind.go:85", "runId", "startup")
	// #endregion
	if err := initStore(); err != nil {
		// #region agent log
		log.Errorw("[DEBUG] initStore failed", "hypothesisId", "A", "location", "numind.go:86", "runId", "startup", "error", err)
		// #endregion
		return err
	}
	// #region agent log
	log.Infow("[DEBUG] After initStore success", "hypothesisId", "A", "location", "numind.go:87", "runId", "startup")
	// #endregion

	// 初始化上传目录 - 暂时注释掉，避免权限问题
	// if err := initUploadDirectories(); err != nil {
	// 	return err
	// }

	// 设置 token 包的签发密钥，用于 token 包 token 的签发和解析
	//token.Init(viper.GetString("jwt.secret"), known.XUsernameKey)

	// 初始化 COS（如果已配置）
	InitCOS()

	// 设置 Gin 模式
	gin.SetMode(viper.GetString("runmode"))

	// 创建 Gin 引擎
	g := gin.New()

	// gin.Recovery() 中间件，用来捕获任何 panic，并恢复
	mws := []gin.HandlerFunc{gin.Recovery(), mw.NoCache, mw.Cors, mw.Secure, mw.RequestID()}

	g.Use(mws...)

	// #region agent log
	log.Infow("[DEBUG] Before installNumindRouters", "hypothesisId", "C", "location", "numind.go:111", "runId", "startup")
	// #endregion
	if err := installNumindRouters(g); err != nil {
		// #region agent log
		log.Errorw("[DEBUG] installNumindRouters failed", "hypothesisId", "C", "location", "numind.go:112", "runId", "startup", "error", err)
		// #endregion
		return err
	}
	// #region agent log
	log.Infow("[DEBUG] After installNumindRouters success", "hypothesisId", "C", "location", "numind.go:113", "runId", "startup")
	// #endregion

	// 创建并运行 HTTP 服务器
	// #region agent log
	log.Infow("[DEBUG] Before startInsecureServer", "hypothesisId", "C", "location", "numind.go:116", "runId", "startup")
	// #endregion
	httpsrv := startInsecureServer(g)
	// #region agent log
	log.Infow("[DEBUG] After startInsecureServer", "hypothesisId", "C", "location", "numind.go:117", "runId", "startup", "serverCreated", true)
	// #endregion

	// 初始化计费用量记录器
	billing.InitRecorder(store.S.Billing())

	// 初始化 Langfuse AI 可观测性客户端
	langfuse.Init(langfuse.LoadConfig())

	// 启动 SOP draft 清理任务（每2小时清理一次超过8小时的草稿）
	bizLayer := biz.NewBiz(store.S)
	go func() {
		ticker := time.NewTicker(2 * time.Hour)
		defer ticker.Stop()

		log.Infow("SOP draft cleanup task started", "interval", "2 hours", "timeout", "8 hours")

		// 启动时立即执行一次清理
		if err := bizLayer.Sop().CleanupDraftRuns(context.Background(), 8*time.Hour); err != nil {
			log.Errorw("Initial draft cleanup failed", "error", err)
		}

		// 然后定期执行
		for range ticker.C {
			if err := bizLayer.Sop().CleanupDraftRuns(context.Background(), 8*time.Hour); err != nil {
				log.Errorw("Draft cleanup failed", "error", err)
			}
		}
	}()

	// 创建并运行 HTTPS 服务器
	//httpssrv := startSecureServer(g)

	// 创建并运行 GRPC 服务器
	//grpcsrv := startGRPCServer()

	// 等待中断信号优雅地关闭服务器（10 秒超时)。
	quit := make(chan os.Signal, 1)
	// kill 默认会发送 syscall.SIGTERM 信号
	// kill -2 发送 syscall.SIGINT 信号，我们常用的 CTRL + C 就是触发系统 SIGINT 信号
	// kill -9 发送 syscall.SIGKILL 信号，但是不能被捕获，所以不需要添加它
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM) // 此处不会阻塞
	<-quit                                               // 阻塞在此，当接收到上述两种信号时才会往下执行
	log.Infow("Shutting down server ...")

	// 创建 ctx 用于通知服务器 goroutine, 它有 10 秒时间完成当前正在处理的请求
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// 10 秒内优雅关闭服务（将未处理完的请求处理完再关闭服务），超过 10 秒就超时退出
	if err := httpsrv.Shutdown(ctx); err != nil {
		log.Errorw("Insecure Server forced to shutdown", "err", err)
		return err
	}

	// if err := httpssrv.Shutdown(ctx); err != nil {
	// 	log.Errorw("Secure Server forced to shutdown", "err", err)
	// 	return err
	// }

	//grpcsrv.GracefulStop()

	// 优雅关闭 Langfuse 客户端
	langfuse.C.Stop()

	// 优雅关闭计费记录器，确保所有待写入事件落盘
	billing.R.Stop()

	log.Infow("Server exiting")

	return nil
}

// startInsecureServer 创建并运行 HTTP 服务器.
func startInsecureServer(g *gin.Engine) *http.Server {
	// 创建 HTTP Server 实例
	// 对于 SSE 流式响应，需要设置较长的超时时间
	httpsrv := &http.Server{
		Addr:         viper.GetString("addr"),
		Handler:      g,
		ReadTimeout:  600 * time.Second,  // 10分钟读取超时（支持长流式响应）
		WriteTimeout: 1200 * time.Second, // 20分钟写入超时（支持长流式响应）
		IdleTimeout:  120 * time.Second,  // 2分钟空闲超时
	}

	// 运行 HTTP 服务器。在 goroutine 中启动服务器，它不会阻止下面的正常关闭处理流程
	// 打印一条日志，用来提示 HTTP 服务已经起来，方便排障
	log.Infow("Start to listening the incoming requests on http address", "addr", viper.GetString("addr"))
	// #region agent log
	log.Infow("[DEBUG] Before ListenAndServe goroutine", "hypothesisId", "C", "location", "numind.go:267", "runId", "startup", "addr", viper.GetString("addr"))
	// #endregion
	go func() {
		// #region agent log
		log.Infow("[DEBUG] ListenAndServe goroutine started", "hypothesisId", "C", "location", "numind.go:270", "runId", "startup")
		// #endregion
		if err := httpsrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			// #region agent log
			log.Errorw("[DEBUG] ListenAndServe error", "hypothesisId", "C", "location", "numind.go:272", "runId", "startup", "error", err)
			// #endregion
			log.Fatalw(err.Error())
		}
	}()
	// #region agent log
	log.Infow("[DEBUG] After ListenAndServe goroutine", "hypothesisId", "C", "location", "numind.go:275", "runId", "startup")
	// #endregion

	return httpsrv
}

// startGRPCServer 创建并运行 GRPC 服务器.
// func startGRPCServer() *grpc.Server {
// 	lis, err := net.Listen("tcp", viper.GetString("grpc.addr"))
// 	if err != nil {
// 		log.Fatalw("Failed to listen", "err", err)
// 	}

// 	// 创建 GRPC Server 实例
// 	grpcsrv := grpc.NewServer()
// 	pb.RegisterMiniBlogServer(grpcsrv, user.New(store.S, nil))

// 	// 运行 GRPC 服务器。在 goroutine 中启动服务器，它不会阻止下面的正常关闭处理流程
// 	// 打印一条日志，用来提示 GRPC 服务已经起来，方便排障
// 	log.Infow("Start to listening the incoming requests on grpc address", "addr", viper.GetString("grpc.addr"))
// 	go func() {
// 		if err := grpcsrv.Serve(lis); err != nil {
// 			log.Fatalw(err.Error())
// 		}
// 	}()

// 	return grpcsrv
// }
