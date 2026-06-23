package xhs

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/model"
)

// enrichMockStore 是 enrich 测试专用的内存 store，按主键跟踪 enrich_status，
// 并提供可注入的 hook 用于模拟 panic / 阻塞 / 并发计数。
type enrichMockStore struct {
	mu     sync.Mutex
	rows   map[uint64]*model.XhsTopicNote
	userOf map[uint64]uint

	// 富化次数：每次 ClaimForEnrich 抢占成功计一次（验证同一 id 只富化一次）。
	enrichCount int32

	// hooks（可选）。
	onUpdateStatus func(id uint64, status string) // 在 UpdateEnrichStatus 真正写入前调用
	onClaim        func(id uint64)                // 在 ClaimForEnrich 抢占成功后调用
	onUpdateResult func(id uint64, status string) // 在 UpdateEnrichResult 真正写入前调用
}

func newEnrichMockStore() *enrichMockStore {
	return &enrichMockStore{
		rows:   map[uint64]*model.XhsTopicNote{},
		userOf: map[uint64]uint{},
	}
}

func (m *enrichMockStore) seed(userID uint, id uint64, status string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rows[id] = &model.XhsTopicNote{ID: id, UserID: userID, EnrichStatus: status}
	m.userOf[id] = userID
}

func (m *enrichMockStore) statusOf(id uint64) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r, ok := m.rows[id]; ok {
		return r.EnrichStatus
	}
	return ""
}

func (m *enrichMockStore) GetByIDs(_ context.Context, userID uint, ids []uint64) ([]model.XhsTopicNote, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []model.XhsTopicNote
	for _, id := range ids {
		if r, ok := m.rows[id]; ok && r.UserID == userID {
			out = append(out, *r)
		}
	}
	return out, nil
}

func (m *enrichMockStore) UpdateEnrichStatus(_ context.Context, id uint64, status string) error {
	if m.onUpdateStatus != nil {
		m.onUpdateStatus(id, status)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if r, ok := m.rows[id]; ok {
		r.EnrichStatus = status
	}
	return nil
}

// ClaimForEnrich 原子 CAS pending→enriching，复刻真实 store 语义。
func (m *enrichMockStore) ClaimForEnrich(_ context.Context, id uint64) (bool, error) {
	m.mu.Lock()
	r, ok := m.rows[id]
	if !ok || r.EnrichStatus != model.XhsEnrichPending {
		m.mu.Unlock()
		return false, nil
	}
	r.EnrichStatus = model.XhsEnrichEnriching
	atomic.AddInt32(&m.enrichCount, 1)
	m.mu.Unlock()
	if m.onClaim != nil {
		m.onClaim(id)
	}
	return true, nil
}

func (m *enrichMockStore) UpdateEnrichResult(_ context.Context, n *model.XhsTopicNote) error {
	if m.onUpdateResult != nil {
		m.onUpdateResult(n.ID, n.EnrichStatus)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if r, ok := m.rows[n.ID]; ok {
		r.EnrichStatus = n.EnrichStatus
	}
	return nil
}

// 其余接口方法本测试不使用。
func (m *enrichMockStore) UpsertByUserNote(context.Context, *model.XhsTopicNote) (bool, error) {
	return false, nil
}
func (m *enrichMockStore) ListNotes(context.Context, uint, store.XhsNoteFilter, int, int) ([]model.XhsTopicNote, int64, error) {
	return nil, 0, nil
}
func (m *enrichMockStore) ListPendingEnrich(context.Context, int) ([]model.XhsTopicNote, error) {
	return nil, nil
}
func (m *enrichMockStore) GetNote(context.Context, uint, uint64) (*model.XhsTopicNote, error) {
	return nil, nil
}
func (m *enrichMockStore) DeleteNote(context.Context, uint, uint64) error { return nil }

var _ store.IXhsTopicStore = (*enrichMockStore)(nil)

// waitFor 轮询 cond 直到为真或超时（避免在并发测试里 sleep 固定时长导致脆弱）。
func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	require.True(t, cond(), "等待条件超时")
}

// TestNewEnricher_WorkerCountFromViper 验证 worker 数从 viper 读取，无配置兜底默认值。
func TestNewEnricher_WorkerCountFromViper(t *testing.T) {
	t.Run("default when unset", func(t *testing.T) {
		viper.Reset()
		e := NewEnricher(newEnrichMockStore())
		assert.Equal(t, defaultEnrichWorkers, e.workers)
		assert.Equal(t, defaultFFmpegWorkers, cap(e.ffmpegSem))
	})
	t.Run("from config", func(t *testing.T) {
		viper.Reset()
		viper.Set("xhs.enrich_workers", 3)
		viper.Set("xhs.ffmpeg_workers", 1)
		defer viper.Reset()
		e := NewEnricher(newEnrichMockStore())
		assert.Equal(t, 3, e.workers)
		assert.Equal(t, 1, cap(e.ffmpegSem))
	})
}

// TestEnricher_WorkerPoolBounded 验证并发 in-flight job 数不超过配置的 worker 数。
// 用阻塞的 onUpdateStatus 把 job 卡在 enriching 阶段，统计同时在飞的 job 峰值。
func TestEnricher_WorkerPoolBounded(t *testing.T) {
	viper.Reset()
	viper.Set("xhs.enrich_workers", 3)
	defer viper.Reset()

	m := newEnrichMockStore()

	const userID = uint(1)
	const total = 12
	for i := 1; i <= total; i++ {
		m.seed(userID, uint64(i), model.XhsEnrichPending)
	}

	var inFlight int32
	var maxInFlight int32
	release := make(chan struct{})
	// 抢占成功后阻塞，制造 in-flight 峰值，统计同时在飞的 job 数。
	m.onClaim = func(_ uint64) {
		cur := atomic.AddInt32(&inFlight, 1)
		for {
			old := atomic.LoadInt32(&maxInFlight)
			if cur <= old || atomic.CompareAndSwapInt32(&maxInFlight, old, cur) {
				break
			}
		}
		<-release // 阻塞，制造 in-flight 峰值
		atomic.AddInt32(&inFlight, -1)
	}

	e := NewEnricher(m)
	e.StartWorkers()
	for i := 1; i <= total; i++ {
		e.Enqueue(userID, uint64(i))
	}

	// 等到 worker 把并发拉满（peak 应等于 worker 数 3）。
	waitFor(t, 2*time.Second, func() bool {
		return atomic.LoadInt32(&inFlight) >= 3
	})
	peak := atomic.LoadInt32(&maxInFlight)
	close(release) // 放行所有 job
	assert.LessOrEqual(t, peak, int32(3), "同时在飞 job 数不应超过 worker 数")
	assert.Equal(t, int32(3), peak, "应跑满 3 个 worker")
}

// TestEnricher_JobPanic_DoesNotCrash_AndMarksFailed 验证单个 job panic 被 recover，
// worker 不崩溃且把笔记置为 failed（不卡在 pending）。
func TestEnricher_JobPanic_DoesNotCrash_AndMarksFailed(t *testing.T) {
	viper.Reset()
	defer viper.Reset()

	m := newEnrichMockStore()
	const userID = uint(2)
	m.seed(userID, 100, model.XhsEnrichPending) // 会 panic 的笔记
	m.seed(userID, 101, model.XhsEnrichPending) // 正常笔记，验证 worker 仍存活

	// 笔记 100 在富化结果写回阶段 panic，验证 worker 不崩且置 failed。
	m.onUpdateResult = func(id uint64, _ string) {
		if id == 100 {
			panic("boom in enrichOne")
		}
	}

	e := NewEnricher(m)
	e.StartWorkers()
	e.Enqueue(userID, 100)
	e.Enqueue(userID, 101)

	// panic 笔记应被置 failed；正常笔记应被置 done（证明 worker pool 仍存活）。
	waitFor(t, 2*time.Second, func() bool {
		return m.statusOf(100) == model.XhsEnrichFailed && m.statusOf(101) == model.XhsEnrichDone
	})
	assert.Equal(t, model.XhsEnrichFailed, m.statusOf(100), "panic 笔记应被置 failed")
	assert.Equal(t, model.XhsEnrichDone, m.statusOf(101), "panic 后 worker 应仍存活并处理后续 job")
}

// TestEnricher_DoubleEnqueueSameID_EnrichesOnce 验证同一 id 并发投递两次，
// enrich_status 二次保护使其只真正富化一次。
func TestEnricher_DoubleEnqueueSameID_EnrichesOnce(t *testing.T) {
	viper.Reset()
	viper.Set("xhs.enrich_workers", 4)
	defer viper.Reset()

	m := newEnrichMockStore()
	const userID = uint(3)
	m.seed(userID, 500, model.XhsEnrichPending)

	// 抢占成功后短暂逗留，放大并发窗口：若 CAS 二次保护失效，第二个 job 也会
	// 在此期间抢到 pending 并使 enrichCount==2，从而暴露竞态。
	m.onClaim = func(_ uint64) {
		time.Sleep(20 * time.Millisecond)
	}

	e := NewEnricher(m)
	e.StartWorkers()

	// 并发投递同一 id 两次。
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			e.Enqueue(userID, 500)
		}()
	}
	wg.Wait()

	waitFor(t, 2*time.Second, func() bool {
		return m.statusOf(500) == model.XhsEnrichDone
	})

	assert.Equal(t, model.XhsEnrichDone, m.statusOf(500))
	assert.Equal(t, int32(1), atomic.LoadInt32(&m.enrichCount), "同一 id 应只富化一次（ClaimForEnrich 原子二次保护）")
}

// TestEnricher_NoteNotPending_Skips 验证投递的笔记已非 pending（如已 done）时跳过，不重复富化。
func TestEnricher_NoteNotPending_Skips(t *testing.T) {
	viper.Reset()
	defer viper.Reset()

	m := newEnrichMockStore()
	const userID = uint(4)
	m.seed(userID, 700, model.XhsEnrichDone) // 已富化

	e := NewEnricher(m)
	e.StartWorkers()
	e.Enqueue(userID, 700)

	// 给 worker 时间处理，状态应保持 done，富化次数为 0。
	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, model.XhsEnrichDone, m.statusOf(700))
	assert.Equal(t, int32(0), atomic.LoadInt32(&m.enrichCount), "非 pending 笔记不应被富化")
}
