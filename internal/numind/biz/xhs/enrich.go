package xhs

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/spf13/viper"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/middleware"
	"numind-server/internal/pkg/model"
)

// 富化框架的默认参数（无对应 viper 配置时兜底）。
const (
	defaultEnrichWorkers = 5               // 富化 worker 数量
	defaultEnrichQueue   = 256             // enrichQueue 缓冲容量
	defaultFFmpegWorkers = 2               // ffmpeg/ASR 并发上限（视频转写本地资源密集）
	enrichJobTimeout     = 5 * time.Minute // 单个富化 job 的 detached ctx 超时
)

// enrichJob 是投递给富化 worker pool 的单元。
//
// 只携带 (userID, noteID) 而非整个 model：worker 在执行时按主键重新读取最新行，
// 既保证 enrich_status 的并发二次保护读到最新值，也避免长队列里持有陈旧快照。
type enrichJob struct {
	userID uint
	noteID uint64
}

// Enricher 是小红书选题库的异步富化框架（worker pool）。
//
// 设计：
//   - enrichQueue 为带缓冲 channel，Ingest 落库置 pending 后投递 job。
//   - StartWorkers 拉起 N 个 worker（viper "xhs.enrich_workers"，默认 5）消费队列。
//   - ffmpegSem 是本包内声明的 ffmpeg/ASR 并发信号量（默认 2），限制视频转写的本地
//     资源占用（不引用 monitor 包的私有 sem，保持解耦）。
//   - 每个 job 在独立的 detached ctx（5min 超时）中执行，从原 ctx 抽 userID 透传，
//     避免请求 ctx 取消（HTTP 连接关闭）误杀后台富化。
//   - 每个 job 用 defer recover 兜底 panic：单个 job 崩溃不影响 worker 与其它 job，
//     并把该笔记置为 failed，不让它永久卡在 pending。
//   - 执行前重新读取该笔记，仅当 enrich_status 仍为 pending 才继续（并发二次保护：
//     同一笔记被重复投递时只富化一次）。
//
// 真实的 LLM/ASR 富化逻辑由 T4/T5 在 enrichOne 中填充，本 task 仅搭好框架与状态机。
type Enricher struct {
	store     store.IXhsTopicStore
	enrichQ   chan enrichJob
	workers   int
	ffmpegSem chan struct{}
	wg        sync.WaitGroup // 跟踪 worker goroutine，Stop 时 Wait 等其全部退出
	stopOnce  sync.Once      // 保证 Stop 幂等（close channel 只发生一次）
}

// NewEnricher 创建富化框架。worker 数取 viper "xhs.enrich_workers"，
// 未配置或非正值兜底为 defaultEnrichWorkers。
func NewEnricher(s store.IXhsTopicStore) *Enricher {
	workers := viper.GetInt("xhs.enrich_workers")
	if workers <= 0 {
		workers = defaultEnrichWorkers
	}
	ffmpeg := viper.GetInt("xhs.ffmpeg_workers")
	if ffmpeg <= 0 {
		ffmpeg = defaultFFmpegWorkers
	}
	queueSize := viper.GetInt("xhs.enrich_queue_size")
	if queueSize <= 0 {
		queueSize = defaultEnrichQueue
	}

	return &Enricher{
		store:     s,
		enrichQ:   make(chan enrichJob, queueSize),
		workers:   workers,
		ffmpegSem: make(chan struct{}, ffmpeg),
	}
}

// StartWorkers 拉起 worker pool。幂等性由调用方保证（应用启动时调用一次）。
func (e *Enricher) StartWorkers() {
	e.wg.Add(e.workers)
	for i := 0; i < e.workers; i++ {
		go e.worker()
	}
	log.Infow("xhs enricher started", "workers", e.workers, "queue_size", cap(e.enrichQ), "ffmpeg_workers", cap(e.ffmpegSem))
}

// Stop 优雅关闭富化框架：close enrichQ 让 worker 消费完缓冲队列后退出 range 循环，
// 再 Wait 等所有 worker goroutine 真正退出，避免 SIGTERM 时 in-flight job 被强杀
// 把笔记永久卡在 enriching 状态。
//
// 与 biz 层其它有状态后台服务（complianceAudit / memoryExtractor / memoryDigestCron）
// 同 shutdown 模式：在 HTTP 已 Shutdown（不再有新 Enqueue）之后调用。stopOnce 保证
// 幂等——重复调用不会二次 close 已关闭的 channel 而 panic。
func (e *Enricher) Stop() {
	e.stopOnce.Do(func() {
		close(e.enrichQ)
	})
	e.wg.Wait()
}

// Enqueue 把一条待富化笔记投递到队列。
//
// 非阻塞：队列满时丢弃并告警而非阻塞调用方（Ingest 在 HTTP 请求路径上，不能被
// 后台富化背压拖住）。被丢弃的笔记仍是 pending 状态，后续 ListPendingEnrich
// 扫描兜底（T5 重试端点 / 定时扫描）会重新捡起，不丢数据。
func (e *Enricher) Enqueue(userID uint, noteID uint64) {
	job := enrichJob{userID: userID, noteID: noteID}
	select {
	case e.enrichQ <- job:
	default:
		log.Warnw("xhs enrich queue full, dropping job (will be retried by pending scan)", "user_id", userID, "note_id", noteID)
	}
}

// worker 持续消费 enrichQueue，逐个处理 job。enrichQ 被 Stop close 后 range 退出，
// wg.Done 通知 Stop 该 worker 已干净退出。
func (e *Enricher) worker() {
	defer e.wg.Done()
	for job := range e.enrichQ {
		e.processJob(job)
	}
}

// processJob 处理单个富化 job：detached ctx + panic 兜底 + pending 二次保护 + enrichOne。
//
// 任何 panic 都被 recover 捕获并把笔记置为 failed，避免单个 job 崩溃拖垮 worker
// 或让笔记永久卡在 pending。
func (e *Enricher) processJob(job enrichJob) {
	// detached ctx：脱离请求 ctx（避免 HTTP 连接关闭取消后台富化），带 5min 超时，
	// 并把 userID 透传进去供下游（aiservice 计费 / Langfuse trace）使用。
	ctx, cancel := context.WithTimeout(context.Background(), enrichJobTimeout)
	defer cancel()
	ctx = middleware.NewContextWithUserID(ctx, job.userID)

	defer func() {
		if r := recover(); r != nil {
			log.Errorw("xhs enrich job panicked", "user_id", job.userID, "note_id", job.noteID, "panic", r)
			// recover 闭包后于 cancel 注册，故按 defer LIFO 先于 cancel 执行：panic 触发
			// 时 ctx 仍有效（cancel 尚未运行），直接用原 ctx 置 failed 即可。
			if err := e.store.UpdateEnrichStatus(ctx, job.noteID, model.XhsEnrichFailed); err != nil {
				log.Errorw("xhs enrich mark-failed after panic failed", "note_id", job.noteID, "error", err)
			}
		}
	}()

	// 并发二次保护（原子抢占）：CAS 把 enrich_status 从 pending 置为 enriching。
	// 同一笔记被重复投递（如插件重发 / 重试端点）或被多个 worker 同时取到时，
	// 只有一个 worker 抢占成功（claimed=true）真正富化，其余 claimed=false 跳过，
	// 杜绝 read-then-update 的 TOCTOU 窗口导致重复富化重复扣分。
	claimed, err := e.store.ClaimForEnrich(ctx, job.noteID)
	if err != nil {
		log.Errorw("xhs enrich claim failed", "user_id", job.userID, "note_id", job.noteID, "error", err)
		return
	}
	if !claimed {
		// 笔记已被抢占（非 pending）或不存在，跳过（并发二次保护命中）。
		return
	}

	// 抢占成功后再做所有权校验：确认笔记确属该用户（多租户隔离）。
	notes, err := e.store.GetByIDs(ctx, job.userID, []uint64{job.noteID})
	if err != nil {
		log.Errorw("xhs enrich load note failed", "user_id", job.userID, "note_id", job.noteID, "error", err)
		// 已抢占置 enriching 但读取失败，回退置 failed 避免卡在 enriching。
		if uErr := e.store.UpdateEnrichStatus(ctx, job.noteID, model.XhsEnrichFailed); uErr != nil {
			log.Errorw("xhs enrich mark-failed after load error failed", "note_id", job.noteID, "error", uErr)
		}
		return
	}
	if len(notes) == 0 {
		// 笔记不属于该用户（异常：claim 成功但 user 不匹配）；置 failed 不卡 enriching。
		log.Warnw("xhs enrich note ownership mismatch, marking failed", "user_id", job.userID, "note_id", job.noteID)
		if uErr := e.store.UpdateEnrichStatus(ctx, job.noteID, model.XhsEnrichFailed); uErr != nil {
			log.Errorw("xhs enrich mark-failed on ownership mismatch failed", "note_id", job.noteID, "error", uErr)
		}
		return
	}
	note := &notes[0]

	if err := e.enrichOne(ctx, job.userID, note); err != nil {
		log.Errorw("xhs enrich one failed", "user_id", job.userID, "note_id", job.noteID, "error", err)
		// enrichOne 内部负责状态机流转；这里兜底确保不卡在 enriching。
		if uErr := e.store.UpdateEnrichStatus(ctx, job.noteID, model.XhsEnrichFailed); uErr != nil {
			log.Errorw("xhs enrich mark-failed failed", "note_id", job.noteID, "error", uErr)
		}
	}
}

// enrichOne 对单条笔记执行真实富化。调用方（processJob）已通过 ClaimForEnrich
// 把状态原子置为 enriching，故此处不再重复置 enriching。
//
// 本 task（T3b）为 stub：仅把状态收尾为 done，真实的 LLM 6 字段分析
// （aiservice.Chat）与视频 ASR 转写（aiservice.ASR，受 ffmpegSem 限流）由 T4/T5 填充。
//
// 状态机契约（供 T4/T5 实现时遵守）：
//   - 进入时已是 enriching（由 ClaimForEnrich 抢占置位，避免并发重复富化）。
//   - 成功置 done；部分成功（如视频直链失效）置 partial；失败返回 error 由
//     processJob 置 failed；积分不足置 insufficient_credits 并保留原始采集数据。
func (e *Enricher) enrichOne(ctx context.Context, _ uint, note *model.XhsTopicNote) error {
	// TODO(T4/T5): 在此调用 aiservice.Chat 生成 6 个 AI 分析字段；
	// note_type==video 时获取 ffmpegSem 后走 aiservice.ASR 转写 video_transcript；
	// 计费 Reserve/Reconcile；积分不足置 insufficient_credits。
	// 当前 stub 直接置 done。

	if err := e.store.UpdateEnrichResult(ctx, &model.XhsTopicNote{
		ID:              note.ID,
		EnrichStatus:    model.XhsEnrichDone,
		VideoTranscript: note.VideoTranscript,
	}); err != nil {
		return fmt.Errorf("enrichOne: write result: %w", err)
	}
	return nil
}
