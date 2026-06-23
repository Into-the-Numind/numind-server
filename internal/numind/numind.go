package numind

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"numind-server/internal/numind/biz"
	"numind-server/internal/numind/biz/compactv2"
	cbbiz "numind-server/internal/numind/biz/contextbudget"
	"numind-server/internal/numind/biz/credit"
	"numind-server/internal/numind/biz/feishu"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/aiservice"
	"numind-server/internal/pkg/aiservice/adapter"
	"numind-server/internal/pkg/aiservice/capability"
	aimw "numind-server/internal/pkg/aiservice/middleware"
	"numind-server/internal/pkg/aiservice/registry"
	"numind-server/internal/pkg/billing"
	"numind-server/internal/pkg/crypto"
	"numind-server/internal/pkg/langfuse"
	"numind-server/internal/pkg/log"
	mw "numind-server/internal/pkg/middleware"
	"numind-server/internal/pkg/model"
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
	// 服务启动打印banner
	banner := figure.NewColorFigure("Numind Server", "", "green", true)
	banner.Print()

	// 初始化 store 层
	if err := initStore(); err != nil {
		return err
	}

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
	g.MaxMultipartMemory = 256 << 20 // 256MB，支持批量文件上传（最多 5 x 50MB）

	// gin.Recovery() 中间件，用来捕获任何 panic，并恢复
	mws := []gin.HandlerFunc{gin.Recovery(), mw.NoCache, mw.Cors, mw.Secure, mw.RequestID()}

	g.Use(mws...)

	// 初始化计费用量记录器
	billing.InitRecorder(store.S.Billing())

	// 初始化 Langfuse AI 可观测性客户端
	langfuse.Init(langfuse.LoadConfig())

	// feishu-integration: 初始化第三方凭据加密。仅当飞书集成 feature flag 开启时
	// 强制注入 AES-256-GCM 密钥（security.thirdparty_token_key，32 字节 base64）。
	// 缺失/非法 → MustInit panic → 进程启动失败（fail-fast，绝不静默明文存储 token）。
	// prod 默认不开此 flag、密钥由运维另注（禁进 config_prod.yaml），故不受影响。
	if viper.GetBool("features.feishu_integration.enabled") {
		crypto.MustInit(viper.GetString("security.thirdparty_token_key"))
		// 同样对 OAuth state HMAC 密钥（security.feishu_state_key）做启动期 fail-fast
		// 校验：缺失/非法 base64/不足 16 字节 → panic → 进程启动失败。此处只校验、
		// 不构造 StateSigner（后者需要 Redis 支撑的 NonceStore，T7 wiring 时才就绪），
		// 与 crypto.MustInit 对称，确保两个密钥都不留到运行时才暴露错配。
		feishu.MustValidateStateKey(viper.GetString("security.feishu_state_key"))
		log.Infow("feishu-integration: third-party credential cipher initialized; state key validated")
	}

	// 初始化 AI Service Gateway（DB ready 后，早于路由注册和服务器启动，
	// 确保 Default() 在第一个请求到来前已就绪）
	reg := registry.New(store.S.DB())
	gateway := aiservice.Build(aiservice.Deps{Registry: reg})

	// 初始化 capability matrix 查询（V1.5 multimodal routing helper）
	// 必须在 gateway 初始化之后、路由注册之前调用，确保 task 1.3/1.4 可以查询 capability。
	capability.Init(store.S.DB())

	// Wire middleware chain (done here to avoid import cycle: aiservice ↛ middleware).
	usageStore := aimw.NewDBUsageStore(store.S.DB())

	// Wire context budget service (Task 7).
	// The real LLM-backed compressor is wired in Task 12; for now nil is passed
	// which causes ActionSummarize to fall back to dropping the candidates.
	cbStore := store.NewContextBudgetStore(store.S.DB())
	contextBudgetSvc := cbbiz.New(cbStore, cbbiz.Options{
		Compressor: nil, // Task 12 will wire the real compressor
		Clock:      time.Now,
		Logger:     stdLogger{},
	})

	// Wire background summary worker (Task 8).
	// The worker shares cbStore and compressor with the Biz. It is started here
	// and stopped via workerCancel when the server shuts down.
	// Task 9/10 producers obtain the worker via SummaryWorkerInstance().
	workerCtx, workerCancel := context.WithCancel(context.Background())
	defer workerCancel()                                                       // error-path guard only; normal shutdown calls workerCancel() explicitly below
	summaryWorker := cbbiz.NewSummaryWorker(cbStore, nil, cbbiz.WorkerOptions{ // compressor nil until Task 12
		Logger: stdLogger{},
	})
	go summaryWorker.Run(workerCtx)
	SetSummaryWorkerInstance(summaryWorker)
	log.Infow("Context budget summary worker started")

	// Build ContextBudgetCreditService adapter wrapping the ICreditService.
	bizLayer := biz.NewBiz(store.S)

	// 注入 middleware 功能权限检查函数，避免 middleware → biz → salesrag → middleware 循环依赖。
	// biz.B 在 NewBiz 内已初始化，此处安全引用。
	mw.CheckFeaturePermissionFunc = biz.B.Customers().CheckFeaturePermission

	creditFacade := &creditServiceFacade{
		svc:       bizLayer.CreditService(),
		userStore: store.S.Users(),
	}

	mwChain := aimw.BuildDefault(aimw.Deps{
		Langfuse:      langfuse.C,
		UsageStore:    usageStore,
		Resolver:      reg,
		ContextBudget: contextBudgetSvc,
		CreditService: creditFacade,
		// CompletionEstimator replaces the hardcoded policy.ReservedOutputTokens
		// (= max_tokens worst case) inside ContextBudgetCredits.doReserveBudget
		// with a per-(provider, model) 30d historical average. Fallback path is
		// preserved (estimator returns hasData=false → ReservedOutputTokens), so
		// cold-start models and outages cannot regress reservation accuracy.
		CompletionEstimator: credit.NewCompletionEstimator(store.S.DB()),
		// PricingCalc enables Billing middleware to compute cost_cents synchronously
		// and write the result into the finalCostHolder so that ContextBudgetCredits
		// passes the real value to FinalizeReservation (not the EstimatedCredits
		// placeholder). Shares the same ICalculator instance used by biz/sop and
		// biz/credit for consistent cost computation across all callers.
		PricingCalc: bizLayer.Pricing(),
	})
	gateway.SetMiddlewareChain(aimw.AsGatewayChain(mwChain))

	// Register all built-in provider adapters.
	for _, p := range []aiservice.Provider{
		adapter.NewAliAdapter(),
		adapter.NewVolcAdapter(),
		adapter.NewDMXAPIAdapter(),
		adapter.NewBaiduOCRAdapter(),
		adapter.NewBailianFileAdapter(),
		adapter.NewFunASRAdapter(),
		// Provider-native cache adapters (native-cache-adapters T4). ADDITIVE and
		// OPT-IN: nothing routes to these until an admin activates a llm_provider
		// row named exactly "claude-native"/"gemini-native" AND repoints a route at
		// it (a separate manual step; the T8 migration inserts the rows is_active=0).
		// Registering them unconditionally is safe — an unused adapter is inert.
		adapter.NewClaudeNativeAdapter(),
		adapter.NewGeminiNativeAdapter(),
	} {
		gateway.RegisterProvider(p)
	}
	// Register aliases for providers that share the same adapter protocol
	gateway.RegisterProviderAlias("dmxapi-ssvip", "dmxapi")
	gateway.RegisterProviderAlias("aihubmix", "dmxapi")

	aiservice.SetDefault(gateway)
	log.Infow("AI Service Gateway initialised", "adapters", gateway.AdapterNames())

	// Startup registration assertion (native-cache-adapters T4 / finding #1):
	// refuse to start if a native llm_provider row is is_active=true but its
	// adapter is NOT registered in this binary — closing the half-deploy TOCTOU
	// window where the dmxapi prefix fallback would silently route an Anthropic /
	// Gemini body to /chat/completions. No active native rows ⇒ no-op (the default
	// state and the deploy-before-activate window), so zero impact on every
	// existing deploy. Called AFTER SetDefault, BEFORE SyncProviderCredentials.
	if err := assertNativeAdaptersRegistered(gateway, store.S.DB()); err != nil {
		log.Fatalw(err.Error())
	}

	// 同步 provider 凭据（config → llm_provider 表）
	if err := aiservice.SyncProviderCredentials(context.Background(), store.S.DB(), viper.GetViper()); err != nil {
		log.Errorw("Failed to sync AI provider credentials, continuing", "error", err)
		// Non-fatal: service continues; /healthz/ai will show degraded state.
	}

	if err := installNumindRouters(g); err != nil {
		return err
	}

	// 创建并运行 HTTP 服务器
	httpsrv := startInsecureServer(g)

	// bizLayer was created above for context budget wiring; reuse it for cron tasks.

	// V1.5 task 1.2: start attachment fallback worker pool + recover interrupted jobs.
	// The pool drains when workerCtx is cancelled (same context as summaryWorker).
	if fallbackSvc := bizLayer.AttachmentFallback(); fallbackSvc != nil {
		fallbackSvc.Start(workerCtx)
		if err := fallbackSvc.RecoverPending(context.Background()); err != nil {
			log.Warnw("Attachment fallback RecoverPending failed (non-fatal)", "error", err)
		}
		log.Infow("Attachment fallback worker pool started")
	}

	// 启动博主监控 cron 调度器
	go func() {
		if err := bizLayer.Monitor().StartScheduler(context.Background()); err != nil {
			log.Errorw("Failed to start monitor scheduler", "error", err)
		}
	}()

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

	// Agent Mode V1.5 板块 2 task 2.2 — agent_tool_artifact cleanup cron。
	// 每 24h 扫一次过期 artifact（>30天）：物理删文件 + DB 标 is_expired=true。
	// 同 SOP cleanup 模式：启动时立即跑一次，然后 ticker 24h 一轮。
	// TODO: 当前不是严格 03:00 触发；如果运维需要避开高峰，可改成 sleep until next 03:00 模式。
	go func() {
		artifactStore := store.S.ToolArtifact()
		dataDir := viper.GetString("agent.artifact_dir")
		if dataDir == "" {
			// fallback：相对当前工作目录的 data 子目录（生产部署应当显式配置 agent.artifact_dir）
			dataDir = "data"
			log.Warnw("agent.artifact_dir not configured; using fallback 'data/' (set in config_*.yaml for prod)",
				"dataDir", dataDir)
		}
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()

		log.Infow("agent_tool_artifact cleanup task started", "interval", "24 hours", "data_dir", dataDir)

		// 启动时立即跑一次（避免长服务漂移）
		if _, err := compactv2.RunArtifactCleanup(context.Background(), artifactStore, dataDir); err != nil {
			log.Errorw("Initial agent_tool_artifact cleanup failed", "error", err)
		}

		for range ticker.C {
			if _, err := compactv2.RunArtifactCleanup(context.Background(), artifactStore, dataDir); err != nil {
				log.Errorw("Periodic agent_tool_artifact cleanup failed", "error", err)
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
	go sweeper.Run(context.Background())

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

	// 优雅关闭 summary worker（停止接收新 job，等 in-flight job 结束）
	workerCancel()
	log.Infow("Context budget summary worker stopped")

	// 优雅关闭博主监控调度器
	bizLayer.Monitor().StopScheduler()

	// 优雅关闭 Task 3.3 memory extractor（停止 5 worker goroutine + drain queue）。
	// 在 httpsrv.Shutdown 之后调用：HTTP 已经停了，没有新的 agentRunner.Run 会再次
	// 触发 Enqueue；此处 Stop close(jobQueue) + ctx cancel 让 worker 干净退出。
	bizLayer.CloseMemoryExtractor(ctx)

	// 优雅关闭 Task 3.8 memory digest cron (4 个 job: daily/weekly/monthly/quarterly).
	// 等待 in-flight job drain (worker pool + Redis lock release best-effort).
	// 与 CloseMemoryExtractor 同模式: 未调时进程退出 robfig/cron 随之结束;
	// in-flight LLM 调用中断无 DB 一致性风险 (UPSERT 幂等, 下次 cron 重跑覆盖).
	bizLayer.CloseDigestCron(ctx)

	// 优雅关闭 xhs-collector 异步富化 worker pool（close enrichQ + 等所有 worker 退出）。
	// 在 httpsrv.Shutdown 之后调用：HTTP 已停，没有新的 Ingest 会再触发 Enqueue；
	// 此处 drain 让 in-flight 富化 job 跑完，避免 SIGTERM 把笔记永久卡在 enriching。
	bizLayer.CloseXhsEnricher(ctx)

	// 优雅关闭 Langfuse 客户端
	langfuse.C.Stop()

	// 优雅关闭计费记录器，确保所有待写入事件落盘
	billing.R.Stop()

	log.Infow("Server exiting")

	return nil
}

// ----------------------------------------------------------------------------
// Summary worker package-level accessor
// ----------------------------------------------------------------------------

// summaryWorkerInst holds the singleton SummaryWorker once run() has wired it.
// atomic.Pointer makes both Store and Load race-free without a mutex.
var summaryWorkerInst atomic.Pointer[cbbiz.SummaryWorker]

// SetSummaryWorkerInstance stores the worker singleton. Called once by run().
// CompareAndSwap(nil, w) is equivalent to sync.Once — only the first call
// succeeds; subsequent calls (e.g., parallel test instances) are silently
// ignored.
func SetSummaryWorkerInstance(w *cbbiz.SummaryWorker) {
	summaryWorkerInst.CompareAndSwap(nil, w)
}

// SummaryWorkerInstance returns the running SummaryWorker so that SOP/chatbot
// producers (Task 9/10) can enqueue jobs. Returns nil before run() completes,
// which is safe — Enqueue on a nil worker would panic, but Task 9/10 producers
// should guard with a nil check.
func SummaryWorkerInstance() *cbbiz.SummaryWorker {
	return summaryWorkerInst.Load()
}

// ----------------------------------------------------------------------------
// Context budget wiring helpers
// ----------------------------------------------------------------------------

// stdLogger adapts the package-level log functions to the Options.Logger interface.
type stdLogger struct{}

func (stdLogger) Warnw(msg string, kv ...interface{})  { log.Warnw(msg, kv...) }
func (stdLogger) Errorw(msg string, kv ...interface{}) { log.Errorw(msg, kv...) }

// Compile-time assertion: creditServiceFacade must implement ContextBudgetCreditService.
var _ aimw.ContextBudgetCreditService = (*creditServiceFacade)(nil)

// creditServiceFacade adapts credit.ICreditService to satisfy
// middleware.ContextBudgetCreditService.
//
// ICreditService.FinalizeReservation has signature:
//
//	FinalizeReservation(ctx, rsv *Reservation, actualCostCents *int64, opErr *error) error
//
// ContextBudgetCreditService.FinalizeReservation has signature:
//
//	FinalizeReservation(ctx, reservationID uint64, actualCredits int64, reason string) error
//
// The facade bridges the two by calling Reconcile (when no error) or Refund
// (when reason indicates failure), bypassing the wrapper's *error dispatch.
type creditServiceFacade struct {
	svc       credit.ICreditService
	userStore store.UserStore
}

// LoadUser resolves a *model.User by primary key. Required by spec §6.1.2 step 1
// so that CheckAndEstimateBudget / ReserveBudget always have a user struct
// to thread through credit operations. Historical note: P0 nil-deref bug
// during S5 verification when middleware passed nil into credits.
func (f *creditServiceFacade) LoadUser(ctx context.Context, userID uint) (*model.User, error) {
	if f.userStore == nil {
		return nil, fmt.Errorf("creditServiceFacade.LoadUser: userStore not configured")
	}
	user, err := f.userStore.GetUserByID(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("creditServiceFacade.LoadUser id=%d: %w", userID, err)
	}
	return user, nil
}

// CheckAndEstimateBudget delegates to ICreditService.CheckAndEstimateBudget.
func (f *creditServiceFacade) CheckAndEstimateBudget(ctx context.Context, user *model.User, input credit.BudgetPrecheckInput) (*credit.PreCheckResult, error) {
	return f.svc.CheckAndEstimateBudget(ctx, user, input)
}

// ReserveBudget delegates to ICreditService.ReserveBudget.
func (f *creditServiceFacade) ReserveBudget(ctx context.Context, user *model.User, input credit.BudgetReservationInput) (*credit.Reservation, error) {
	return f.svc.ReserveBudget(ctx, user, input)
}

// EnforceModelMembership delegates to ICreditService.EnforceModelMembership,
// bridging the middleware's uint userID to the credit layer's uint64.
func (f *creditServiceFacade) EnforceModelMembership(ctx context.Context, userID uint, provider, model string) error {
	return f.svc.EnforceModelMembership(ctx, uint64(userID), provider, model)
}

// FinalizeReservation bridges the middleware's simple (id, credits, reason)
// signature to ICreditService.Reconcile. When reason indicates a failure/refund
// scenario it delegates to Refund instead.
func (f *creditServiceFacade) FinalizeReservation(ctx context.Context, reservationID uint64, actualCredits int64, reason string) error {
	return f.svc.Reconcile(ctx, reservationID, actualCredits)
}

// Refund delegates to ICreditService.Refund.
func (f *creditServiceFacade) Refund(ctx context.Context, reservationID uint64, reason string) error {
	return f.svc.Refund(ctx, reservationID, reason)
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
	go func() {
		if err := httpsrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalw(err.Error())
		}
	}()

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
