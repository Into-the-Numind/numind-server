package meeting

// 会议副驾实时流式 ASR 的 WebSocket 端点（SPEC §2）。
//
// 端点：GET /v1/meetings/:id/asr-stream（websocket 升级）。
//
// 鉴权（SPEC §2）：浏览器 ws 无法设 Authorization 头 → 用 query 参数 ?token=<user_jwt>，
// **升级前**复用 middleware.ValidateToken（与 AuthMiddleware 同一套 JWT 校验，不另写）拿 userID；
// 失败直接 HTTP 401 拒绝升级（不开 ws）。会话归属 + status=active 校验由 biz.StartRealtimeASR
// 统一负责（与分段路径同一 getOwnedSession 守卫）：若失败，此时 ws 已升级，按 §2 回 error 帧后关闭。
// feature flag features.meeting_copilot.enabled 由路由组 FeatureFlag 中间件在升级前拦截（off→404）。
//
// 双向中继（SPEC §2）：
//   - 前端二进制帧（raw PCM 16bit LE 16kHz 单声道）→ biz.SendAudio → 转发 dashscope；
//   - 前端文本帧 {"action":"finish"} → biz.Finish（转 dashscope finish-task）；
//   - dashscope onReady/onInterim/onFinal/onError/onClosed → 经 handlers 回调 → 写前端
//     {"type":"ready"} / {"type":"interim",...} / {"type":"final","segment":...} /
//     {"type":"error",...} / {"type":"closed"}。
//
// 并发写串行化（硬约束）：gorilla 同一 conn 并发写会 panic。本端点有两个写来源——
//   (a) reader goroutine（读前端帧时遇错/收尾要回 error/closed）；
//   (b) dashscope 回调 goroutine（onReady/interim/final/...）。
// 故所有写前端 conn 统一经单条 writer goroutine（writeCh + 专用 goroutine）串行化，任何路径都
// 不直接 conn.WriteMessage。
//
// goroutine 生命周期：升级后启动 1 条 writer goroutine + 1 条 reader goroutine（读前端帧）。
// 主 handler 阻塞等 reader 结束（前端关/读错/收到 finish 后对端关）→ 收尾 biz（Finish/Close）→
// close(done) 让 writer 退出 → 关 conn。无泄漏。

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"

	meetingbiz "numind-server/internal/numind/biz/meeting"
	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/middleware"
)

// asrWSWriteTimeout 单次写前端超时（防 writer 因前端不读而无限阻塞）。
const asrWSWriteTimeout = 10 * time.Second

// asrUpgrader 是 gin → websocket 升级器。
//
// CheckOrigin：本端点已用 ?token= JWT 鉴权（升级前校验），不依赖 Origin 做 CSRF 防护，故放行
// 所有 Origin（与浏览器跨端访问一致）。读 buffer 偏大以容纳 ~100ms PCM 帧（3200 bytes）。
var asrUpgrader = websocket.Upgrader{
	ReadBufferSize:  8192,
	WriteBufferSize: 4096,
	CheckOrigin:     func(_ *http.Request) bool { return true },
}

// clientWSEvent 是回前端的 JSON 文本帧（SPEC §2 后端→前端）。
// segment 仅 final 事件携带；text 仅 interim 携带；message 仅 error 携带。
type clientWSEvent struct {
	Type    string                 `json:"type"`
	Text    string                 `json:"text,omitempty"`
	Segment *meetingbiz.SegmentDTO `json:"segment,omitempty"`
	Message string                 `json:"message,omitempty"`
}

// asrWSConn 包装前端 conn，串行化所有写（单 writer goroutine）。
type asrWSConn struct {
	conn    *websocket.Conn
	writeCh chan []byte
	done    chan struct{}
	closeMu sync.Mutex
	closed  bool
}

func newASRWSConn(conn *websocket.Conn) *asrWSConn {
	w := &asrWSConn{
		conn:    conn,
		writeCh: make(chan []byte, 64),
		done:    make(chan struct{}),
	}
	go w.writeLoop()
	return w
}

// send 序列化并投递一个事件到 writer goroutine（非阻塞于 conn）。conn 已关则丢弃。
func (w *asrWSConn) send(ev clientWSEvent) {
	data, err := json.Marshal(ev)
	if err != nil {
		log.Errorw("meeting asr ws: marshal client event failed", "type", ev.Type, "error", err)
		return
	}
	select {
	case <-w.done:
		return
	default:
	}
	select {
	case w.writeCh <- data:
	case <-w.done:
	}
}

// writeLoop 是唯一写前端 conn 的 goroutine。done 关闭后**先 flush 缓冲中的待写帧**（确保
// close 前发出的 error/closed 帧不被丢弃），再发 close 帧并关 conn。
func (w *asrWSConn) writeLoop() {
	for {
		select {
		case <-w.done:
			// 排空 writeCh：close() 常紧跟在 send(error/closed) 之后，select 可能先选中 done
			// 分支而漏掉刚入队的帧 → 这里在关闭前把已缓冲的帧全部写出。
			w.drain()
			_ = w.conn.WriteControl(websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
				time.Now().Add(time.Second))
			_ = w.conn.Close()
			return
		case data := <-w.writeCh:
			if !w.writeFrame(data) {
				w.close()
			}
		}
	}
}

// drain 把 writeCh 中已缓冲的帧全部写出（best-effort）。仅在 writeLoop 收尾时调用。
func (w *asrWSConn) drain() {
	for {
		select {
		case data := <-w.writeCh:
			if !w.writeFrame(data) {
				return
			}
		default:
			return
		}
	}
}

// writeFrame 写一个文本帧到前端 conn，返回 false 表示写失败（调用方据此收尾）。
func (w *asrWSConn) writeFrame(data []byte) bool {
	_ = w.conn.SetWriteDeadline(time.Now().Add(asrWSWriteTimeout))
	if err := w.conn.WriteMessage(websocket.TextMessage, data); err != nil {
		log.Warnw("meeting asr ws: write to client failed, closing", "error", err)
		return false
	}
	return true
}

// close 触发 done（writer 退出并关 conn）。幂等。
func (w *asrWSConn) close() {
	w.closeMu.Lock()
	defer w.closeMu.Unlock()
	if w.closed {
		return
	}
	w.closed = true
	close(w.done)
}

// AsrStream 实时流式 ASR ws 端点。GET /v1/meetings/:id/asr-stream?token=<user_jwt>
//
// 注意：本路由组**不**挂 AuthMiddleware（浏览器 ws 无法带 Authorization 头），鉴权在此 handler
// 内用 ?token= 完成；feature flag 由路由组 FeatureFlag 中间件保证。
func (ctl *Controller) AsrStream(c *gin.Context) {
	// 1) 升级前鉴权：从 query ?token= 取 user_jwt，复用 middleware.ValidateToken。
	token := c.Query("token")
	if token == "" {
		core.WriteResponse(c, errno.ErrTokenInvalid.SetMessage("缺少 token 查询参数"), nil)
		return
	}
	user, err := middleware.ValidateToken(c.Request.Context(), token)
	if err != nil || user == nil {
		core.WriteResponse(c, errno.ErrTokenInvalid.SetMessage("无效的认证令牌"), nil)
		return
	}
	userID := user.ID

	// 2) 解析 session id。
	sessionID, ok := parseSessionID(c)
	if !ok {
		core.WriteResponse(c, errno.ErrInvalidParameter, nil)
		return
	}

	// 3) 升级到 websocket。升级失败 gorilla 已自行写 HTTP 响应。
	conn, err := asrUpgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		log.C(c).Warnw("meeting asr ws: upgrade failed", "error", err)
		return
	}

	client := newASRWSConn(conn)
	defer client.close()

	// 4) 开 dashscope 编排会话。回调写前端（经串行 writer）。
	//    注意：用 context.Background() 派生——ws 升级后 gin 请求 ctx 在 handler 返回时取消，
	//    会误杀仍在收尾的 dashscope 流；转写持久化不应被打断（与 realtime.go handleFinal 一致）。
	asr, err := ctl.biz.StartRealtimeASR(context.Background(), userID, sessionID, meetingbiz.RealtimeASRHandlers{
		OnReady: func() {
			client.send(clientWSEvent{Type: "ready"})
		},
		OnInterim: func(text string) {
			client.send(clientWSEvent{Type: "interim", Text: text})
		},
		OnFinal: func(seg meetingbiz.SegmentDTO) {
			s := seg
			client.send(clientWSEvent{Type: "final", Segment: &s})
		},
		OnError: func(e error) {
			client.send(clientWSEvent{Type: "error", Message: e.Error()})
			// dashscope 出错/task-failed → 关前端连接（与 OnClosed 路径一致），让 relay 循环退出、
			// 不留悬挂连接。close 幂等，writer 会先 flush 上面的 error 帧再发 close 帧。
			client.close()
		},
		OnClosed: func() {
			client.send(clientWSEvent{Type: "closed"})
			// dashscope 正常收尾 → 关前端连接，结束本端点。
			client.close()
		},
	})
	if err != nil {
		// 归属/状态校验失败或 dashscope 握手失败：ws 已升级，按 §2 回 error 帧后关闭。
		log.C(c).Infow("meeting asr ws: start realtime asr failed", "user_id", userID, "session_id", sessionID, "error", err)
		client.send(clientWSEvent{Type: "error", Message: "实时转写启动失败"})
		return
	}
	// 任何退出路径都兜底关 dashscope 流并记 usage（幂等）。
	defer asr.Close()

	// 5) reader 循环：读前端帧 → PCM 转发 / finish。读错或对端关 → 退出，进入收尾。
	ctl.relayClientFrames(client, asr)
}

// clientControlFrame 是前端文本控制帧（SPEC §2：{"action":"finish"}）。
type clientControlFrame struct {
	Action string `json:"action"`
}

// relayClientFrames 在主 handler goroutine 内读前端帧并中继到 dashscope，直到前端关闭/读错。
// 收到 finish → biz.Finish（dashscope 异步吐完末句后 OnClosed 关连接）；前端直接关 → biz.Finish
// 优雅收尾。函数返回即代表前端侧读路径结束。
func (ctl *Controller) relayClientFrames(client *asrWSConn, asr meetingbiz.IRealtimeASR) {
	for {
		select {
		case <-client.done:
			return
		default:
		}

		msgType, data, err := client.conn.ReadMessage()
		if err != nil {
			// 前端断开（正常关 / 异常关 / 读超时）→ 给 dashscope 发 finish-task 收尾。
			asr.Finish()
			return
		}

		switch msgType {
		case websocket.BinaryMessage:
			// PCM 帧 → 转发 dashscope。转发失败（dashscope 流已关）→ 结束读路径。
			if sendErr := asr.SendAudio(data); sendErr != nil {
				log.Warnw("meeting asr ws: forward pcm to dashscope failed", "error", sendErr)
				asr.Finish()
				return
			}
		case websocket.TextMessage:
			var ctrl clientControlFrame
			if jsonErr := json.Unmarshal(data, &ctrl); jsonErr != nil {
				log.Warnw("meeting asr ws: bad control frame", "raw", string(data))
				continue
			}
			if ctrl.Action == "finish" {
				// 用户结束：转 dashscope finish-task；继续读直到 dashscope OnClosed 关连接或前端关。
				asr.Finish()
			}
		default:
			// 忽略 ping/pong/close 由 gorilla 内部处理。
		}
	}
}
