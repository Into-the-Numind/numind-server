package meeting

// Ali Paraformer-realtime WebSocket 客户端（meeting-copilot 实时流式 ASR，SPEC §1）。
//
// 本文件实现「我方编排层 ↔ 阿里 DashScope 实时 ASR」的下游 ws 客户端：连接、run-task、
// 转发上层送来的 PCM 二进制帧、解析 result-generated、finish-task 收尾、错误上报。
//
// 设计要点（硬约束）：
//   - 鉴权 key 从 registry `llm_provider.name='ali-dashscope'` 的 api_key 读，禁止硬编码。
//   - gorilla/websocket 并发写会 panic → 所有写（run-task / PCM 帧 / finish-task）统一经
//     单条 writer goroutine 串行化（writeCh + 专用 goroutine），调用方永不直接写 conn。
//   - goroutine 生命周期清晰：1 条 reader（解析事件）+ 1 条 writer（串行写）。任一侧出错或
//     上层 finish → close(done) → 两条 goroutine 都退出 → 关 conn，无泄漏。

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"numind-server/internal/pkg/log"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

// asrStreamEndpoint 是阿里 DashScope 实时推理 ws 端点（SPEC §1）。
const asrStreamEndpoint = "wss://dashscope.aliyuncs.com/api-ws/v1/inference"

// asrModel 是实时 ASR 模型名（SPEC §1，也用于 UsageRecord.model）。
const asrModel = "paraformer-realtime-v2"

// asrHandshakeTimeout 是 ws 握手超时。
const asrHandshakeTimeout = 10 * time.Second

// asrWriteTimeout 是单次写超时（防 writer goroutine 因对端不读而无限阻塞）。
const asrWriteTimeout = 10 * time.Second

// ---------------------------------------------------------------------------
// 阿里协议帧（SPEC §1）
// ---------------------------------------------------------------------------

// asrRunTaskFrame 是 run-task / finish-task 文本帧（SPEC §1.1 / §1.4）。
type asrRunTaskFrame struct {
	Header  asrHeader      `json:"header"`
	Payload asrRunTaskBody `json:"payload"`
}

type asrHeader struct {
	Action    string `json:"action,omitempty"`
	TaskID    string `json:"task_id"`
	Streaming string `json:"streaming,omitempty"`
}

type asrRunTaskBody struct {
	TaskGroup  string         `json:"task_group,omitempty"`
	Task       string         `json:"task,omitempty"`
	Function   string         `json:"function,omitempty"`
	Model      string         `json:"model,omitempty"`
	Parameters *asrParameters `json:"parameters,omitempty"`
	Input      struct{}       `json:"input"`
}

type asrParameters struct {
	Format                       string   `json:"format"`
	SampleRate                   int      `json:"sample_rate"`
	SemanticPunctuationEnabled   bool     `json:"semantic_punctuation_enabled"`
	PunctuationPredictionEnabled bool     `json:"punctuation_prediction_enabled"`
	MaxSentenceSilence           int      `json:"max_sentence_silence"`
	LanguageHints                []string `json:"language_hints"`
}

// asrServerFrame 是阿里下行事件帧（SPEC §1.2/1.3/1.5：task-started / result-generated /
// task-finished / task-failed）。仅解析我方关心的字段。
type asrServerFrame struct {
	Header struct {
		Event        string `json:"event"`
		TaskID       string `json:"task_id"`
		ErrorCode    string `json:"error_code"`
		ErrorMessage string `json:"error_message"`
	} `json:"header"`
	Payload struct {
		Output struct {
			Sentence *asrSentence `json:"sentence"`
		} `json:"output"`
	} `json:"payload"`
}

// asrSentence 是一句转写结果（SPEC §1.3）。begin/end 为相对会议音频起点的毫秒。
type asrSentence struct {
	Text        string `json:"text"`
	BeginTime   *int64 `json:"begin_time"`
	EndTime     *int64 `json:"end_time"`
	SentenceEnd bool   `json:"sentence_end"`
}

const (
	asrEventTaskStarted     = "task-started"
	asrEventResultGenerated = "result-generated"
	asrEventTaskFinished    = "task-finished"
	asrEventTaskFailed      = "task-failed"
)

// ---------------------------------------------------------------------------
// 对外接口（供 realtime.go 编排层使用）
// ---------------------------------------------------------------------------

// asrStreamOptions 是启动一条 dashscope ASR 流所需的鉴权 + 回调（SPEC §1/§5）。
type asrStreamOptions struct {
	// APIKey 阿里 DashScope key（来自 registry ali-dashscope，禁硬编码）。
	APIKey string
	// OnReady 在收到 task-started（阿里就绪、可送音频）时回调一次。
	OnReady func()
	// OnInterim 中间结果（sentence_end=false）：覆盖式更新当前句。
	OnInterim func(text string)
	// OnFinal 句末定稿（sentence_end=true）：text + 该句起止毫秒。
	OnFinal func(text string, beginMs, endMs int64)
	// OnError 不可恢复错误（含 task-failed / 连接断开 / 解析失败）。最多回调一次。
	OnError func(err error)
	// OnClosed 阿里 task-finished 正常收尾时回调一次。
	OnClosed func()
}

// asrStream 是一条活跃的 dashscope ASR 流，暴露 sendPCM / finish 给编排层。
//
// 并发安全：SendPCM / Finish 可被任意 goroutine 调用，内部经单 writer goroutine 串行化。
type asrStream struct {
	conn   *websocket.Conn
	taskID string

	writeCh chan asrWriteMsg // 写请求 → writer goroutine
	done    chan struct{}    // 关闭信号（reader/writer 协同退出）
	closeMu sync.Mutex       // 保护 closed
	closed  bool

	// readyMu 保护 ready 标志与 pendingPCM 预就绪缓冲。
	// 协议要求：dashscope 必须先回 task-started，调用方才能送音频帧；在 task-started 之前到达的
	// PCM 帧不能直接转发（会被 dashscope 丢弃/报错），也不能丢弃（会丢失开头语音）→ 先缓冲，
	// 收到 task-started 时 flush 缓冲帧再置 ready=true，之后的帧直接转发。
	readyMu    sync.Mutex
	ready      bool
	pendingPCM [][]byte

	// finishOnce 保证 finish-task 只发一次。
	// 终态回调（OnError / OnClosed）的"只触发一次"由 readLoop 内的本地 errFired 标志 + readLoop
	// 单 goroutine 结构保证（reader 退出前最多触发一次终态），不依赖 sync.Once。
	finishOnce sync.Once
}

// asrWriteMsg 是一条待写帧：text（JSON）或 binary（PCM）。
type asrWriteMsg struct {
	messageType int
	data        []byte
}

// startASRStream 建立到阿里的 ws、发 run-task，立即返回一条流（**异步就绪**）。
//
// 注意：此函数**不**阻塞等 task-started——它只完成连接与 run-task 投递就返回。dashscope 的
// task-started 在 reader goroutine 收到后异步触发 OnReady。在 task-started 之前到达的 PCM 帧
// 由 SendPCM 内部缓冲（见 asrStream.ready / pendingPCM），收到 task-started 时 flush，确保
// 既不违反"task-started 后才送音频"的协议、又不丢失开头语音。
//
// 生命周期：成功返回后由 reader/writer 两条 goroutine 驱动；调用方用 SendPCM 送帧、Finish
// 收尾。任一侧错误 → OnError + 自动 close。返回 error 表示连接/握手阶段就失败（此时无 goroutine
// 泄漏，已就地清理）。
func startASRStream(ctx context.Context, opts asrStreamOptions) (*asrStream, error) {
	if opts.APIKey == "" {
		return nil, fmt.Errorf("startASRStream: empty ali-dashscope api key")
	}

	dialer := &websocket.Dialer{HandshakeTimeout: asrHandshakeTimeout}
	header := map[string][]string{
		"Authorization":              {"Bearer " + opts.APIKey},
		"X-DashScope-DataInspection": {"disable"},
	}

	conn, _, err := dialer.DialContext(ctx, asrStreamEndpoint, header)
	if err != nil {
		return nil, fmt.Errorf("startASRStream: dial dashscope ws: %w", err)
	}

	s := &asrStream{
		conn:    conn,
		taskID:  uuid.NewString(),
		writeCh: make(chan asrWriteMsg, 64),
		done:    make(chan struct{}),
	}

	// writer goroutine：唯一写 conn 的 goroutine（串行化，规避 gorilla 并发写 panic）。
	go s.writeLoop()

	// 发 run-task。
	if err := s.sendRunTask(); err != nil {
		s.close()
		return nil, fmt.Errorf("startASRStream: send run-task: %w", err)
	}

	// reader goroutine：解析下行事件并触发回调。
	go s.readLoop(opts)

	return s, nil
}

// sendRunTask 投递 run-task 帧到 writer（SPEC §1.1）。
func (s *asrStream) sendRunTask() error {
	frame := asrRunTaskFrame{
		Header: asrHeader{Action: "run-task", TaskID: s.taskID, Streaming: "duplex"},
		Payload: asrRunTaskBody{
			TaskGroup: "audio", Task: "asr", Function: "recognition",
			Model: asrModel,
			Parameters: &asrParameters{
				Format:                       "pcm",
				SampleRate:                   16000,
				SemanticPunctuationEnabled:   true,
				PunctuationPredictionEnabled: true,
				MaxSentenceSilence:           800,
				LanguageHints:                []string{"zh", "en"},
			},
		},
	}
	data, err := json.Marshal(frame)
	if err != nil {
		return fmt.Errorf("marshal run-task: %w", err)
	}
	return s.enqueue(asrWriteMsg{messageType: websocket.TextMessage, data: data})
}

// SendPCM 转发一帧 PCM 音频到阿里（SPEC §1.2）。流已关闭时返回 error，调用方据此停止送帧。
//
// ready 门：dashscope 协议要求收到 task-started 后才能送音频。task-started 之前到达的帧先
// 缓冲到 pendingPCM（不丢弃，避免丢开头语音），由 handleTaskStarted 在置 ready 时按序 flush。
func (s *asrStream) SendPCM(pcm []byte) error {
	if len(pcm) == 0 {
		return nil
	}

	s.readyMu.Lock()
	if !s.ready {
		// 复制一份再缓冲：调用方的 PCM 切片可能复用底层 buffer（ws 读循环常见），不复制会被覆盖。
		buf := make([]byte, len(pcm))
		copy(buf, pcm)
		s.pendingPCM = append(s.pendingPCM, buf)
		s.readyMu.Unlock()
		return nil
	}
	s.readyMu.Unlock()

	return s.enqueue(asrWriteMsg{messageType: websocket.BinaryMessage, data: pcm})
}

// handleTaskStarted 在收到 dashscope task-started 时调用：先按序 flush 预就绪缓冲帧，再置
// ready=true，最后触发上层 OnReady。flush 与置位在锁内完成，保证此后 SendPCM 不会再往
// pendingPCM 追加（也就不会出现缓冲帧晚于直发帧的乱序）。
func (s *asrStream) handleTaskStarted(opts asrStreamOptions) {
	s.readyMu.Lock()
	pending := s.pendingPCM
	s.pendingPCM = nil
	s.ready = true
	s.readyMu.Unlock()

	for _, frame := range pending {
		if err := s.enqueue(asrWriteMsg{messageType: websocket.BinaryMessage, data: frame}); err != nil {
			// 流已关闭：停止 flush（后续帧也送不出）。
			break
		}
	}

	if opts.OnReady != nil {
		opts.OnReady()
	}
}

// Finish 发 finish-task（SPEC §1.4），通知阿里输入结束、等其吐完末句后 task-finished。
// 幂等：多次调用只发一次 finish-task。
func (s *asrStream) Finish() {
	s.finishOnce.Do(func() {
		frame := asrRunTaskFrame{
			Header: asrHeader{Action: "finish-task", TaskID: s.taskID, Streaming: "duplex"},
		}
		data, err := json.Marshal(frame)
		if err != nil {
			log.Errorw("meeting asr: marshal finish-task failed", "error", err)
			return
		}
		if err := s.enqueue(asrWriteMsg{messageType: websocket.TextMessage, data: data}); err != nil {
			log.Warnw("meeting asr: enqueue finish-task failed (stream already closed)", "error", err)
		}
	})
}

// enqueue 把一条写请求投递给 writer goroutine。流已关闭则返回 error（不阻塞）。
func (s *asrStream) enqueue(msg asrWriteMsg) error {
	select {
	case <-s.done:
		return fmt.Errorf("asr stream closed")
	default:
	}
	select {
	case s.writeCh <- msg:
		return nil
	case <-s.done:
		return fmt.Errorf("asr stream closed")
	}
}

// writeLoop 是唯一写 conn 的 goroutine。done 关闭后退出并关 conn。
func (s *asrStream) writeLoop() {
	for {
		select {
		case <-s.done:
			// 协同关闭：尝试发 close 帧（best-effort），关闭底层连接。
			_ = s.conn.WriteControl(websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
				time.Now().Add(time.Second))
			_ = s.conn.Close()
			return
		case msg := <-s.writeCh:
			_ = s.conn.SetWriteDeadline(time.Now().Add(asrWriteTimeout))
			if err := s.conn.WriteMessage(msg.messageType, msg.data); err != nil {
				log.Warnw("meeting asr: ws write failed, closing stream", "error", err)
				s.close() // 触发 done，下次 select 走关闭分支
			}
		}
	}
}

// readLoop 解析阿里下行事件并触发回调。连接断开 / task-failed → OnError + close；
// task-finished → OnClosed + close。
func (s *asrStream) readLoop(opts asrStreamOptions) {
	defer s.close()

	var errFired bool
	fireError := func(err error) {
		if errFired {
			return
		}
		errFired = true
		if opts.OnError != nil {
			opts.OnError(err)
		}
	}

	for {
		select {
		case <-s.done:
			return
		default:
		}

		msgType, data, err := s.conn.ReadMessage()
		if err != nil {
			// done 已关闭 → 是我方主动收尾，不当错误上报。
			select {
			case <-s.done:
				return
			default:
			}
			fireError(fmt.Errorf("asr ws read: %w", err))
			return
		}
		if msgType != websocket.TextMessage {
			// 阿里下行仅文本事件帧；二进制忽略。
			continue
		}

		var frame asrServerFrame
		if err := json.Unmarshal(data, &frame); err != nil {
			log.Warnw("meeting asr: unmarshal server frame failed", "error", err, "raw", string(data))
			continue
		}

		switch frame.Header.Event {
		case asrEventTaskStarted:
			// flush 预就绪缓冲帧 → 置 ready → 触发 OnReady（SPEC §1.2）。
			s.handleTaskStarted(opts)
		case asrEventResultGenerated:
			s.handleResult(frame, opts)
		case asrEventTaskFinished:
			if opts.OnClosed != nil {
				opts.OnClosed()
			}
			return
		case asrEventTaskFailed:
			fireError(fmt.Errorf("dashscope task-failed [%s]: %s",
				frame.Header.ErrorCode, frame.Header.ErrorMessage))
			return
		default:
			// 未知事件忽略（向前兼容阿里新增事件）。
		}
	}
}

// handleResult 把一条 result-generated 的 sentence 分发到 interim / final 回调（SPEC §1.3）。
func (s *asrStream) handleResult(frame asrServerFrame, opts asrStreamOptions) {
	sent := frame.Payload.Output.Sentence
	if sent == nil {
		return
	}
	if sent.SentenceEnd {
		var beginMs, endMs int64
		if sent.BeginTime != nil {
			beginMs = *sent.BeginTime
		}
		if sent.EndTime != nil {
			endMs = *sent.EndTime
		}
		if opts.OnFinal != nil {
			opts.OnFinal(sent.Text, beginMs, endMs)
		}
		return
	}
	if opts.OnInterim != nil {
		opts.OnInterim(sent.Text)
	}
}

// close 关闭流：触发 done（reader/writer 协同退出，writer 负责关 conn）。幂等。
func (s *asrStream) close() {
	s.closeMu.Lock()
	defer s.closeMu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	close(s.done)
}
