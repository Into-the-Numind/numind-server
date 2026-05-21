package compliance

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/model"
)

// fakeStore implements only WriteAuditLog for AuditLogger tests
type fakeStore struct {
	mu      sync.Mutex
	written []*model.ComplianceAuditLog
	delay   time.Duration // optional artificial delay
	err     error         // optional error to return
}

func (f *fakeStore) WriteAuditLog(ctx context.Context, entry *model.ComplianceAuditLog) error {
	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	f.mu.Lock()
	f.written = append(f.written, entry)
	f.mu.Unlock()
	return f.err
}

// remaining IComplianceStore methods — return nil/empty for unused interfaces
func (f *fakeStore) ListRulesByParent(ctx context.Context, parentUserID uint, activeOnly bool) ([]*model.ComplianceRule, error) {
	return nil, nil
}
func (f *fakeStore) GetRule(ctx context.Context, id uint64) (*model.ComplianceRule, error) {
	return nil, nil
}
func (f *fakeStore) CreateRule(ctx context.Context, r *model.ComplianceRule) error { return nil }
func (f *fakeStore) UpdateRule(ctx context.Context, id uint64, u map[string]interface{}) error {
	return nil
}
func (f *fakeStore) SoftDeleteRule(ctx context.Context, id uint64) error { return nil }
func (f *fakeStore) ListRulesAdmin(_ context.Context, _ store.ListAdminOpts) ([]*model.ComplianceRule, int64, error) {
	return nil, 0, nil
}

func (f *fakeStore) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.written)
}

func TestAuditLogger_StartStopWrite(t *testing.T) {
	fs := &fakeStore{}
	l := NewAuditLogger(fs)
	l.Start()
	for i := 0; i < 5; i++ {
		l.Write(&model.ComplianceAuditLog{RuleLayer: "L0"})
	}
	require.NoError(t, l.Stop(context.Background()))
	assert.Equal(t, 5, fs.count())
}

func TestAuditLogger_StopTimeout(t *testing.T) {
	fs := &fakeStore{delay: 100 * time.Millisecond}
	l := NewAuditLogger(fs)
	l.Start()
	// fill the queue with entries that will be slow to drain
	for i := 0; i < 100; i++ {
		l.Write(&model.ComplianceAuditLog{RuleLayer: "L0"})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	err := l.Stop(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "audit logger stop timeout")
}

func TestAuditLogger_QueueFullDrops(t *testing.T) {
	fs := &fakeStore{delay: 10 * time.Second} // consumer effectively blocked
	l := NewAuditLogger(fs)
	l.Start()
	// auditChanCap=1000; consumer is blocked on first entry, so 1001+ writes drop
	for i := 0; i < 2000; i++ {
		l.Write(&model.ComplianceAuditLog{RuleLayer: "L0"})
	}
	assert.Greater(t, l.DropCount(), uint64(0))
	// best-effort stop (will timeout but that's ok for this test)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_ = l.Stop(ctx)
}

func TestAuditLogger_DropCountMonotonic(t *testing.T) {
	fs := &fakeStore{delay: 10 * time.Second}
	l := NewAuditLogger(fs)
	l.Start()
	for i := 0; i < 1500; i++ {
		l.Write(&model.ComplianceAuditLog{RuleLayer: "L0"})
	}
	c1 := l.DropCount()
	for i := 0; i < 1500; i++ {
		l.Write(&model.ComplianceAuditLog{RuleLayer: "L1"})
	}
	c2 := l.DropCount()
	assert.GreaterOrEqual(t, c2, c1, "drop count must be monotonic")
}

func TestAuditLogger_WriteNilEntry(t *testing.T) {
	fs := &fakeStore{}
	l := NewAuditLogger(fs)
	l.Start()
	l.Write(nil) // should be no-op
	require.NoError(t, l.Stop(context.Background()))
	assert.Equal(t, 0, fs.count())
}

func TestAuditLogger_WriteOnNilLogger(t *testing.T) {
	var l *AuditLogger
	l.Write(&model.ComplianceAuditLog{}) // should not panic
}

func TestAuditLogger_StoreError_DoesNotPanic(t *testing.T) {
	fs := &fakeStore{err: errors.New("db down")}
	l := NewAuditLogger(fs)
	l.Start()
	l.Write(&model.ComplianceAuditLog{RuleLayer: "L0"})
	require.NoError(t, l.Stop(context.Background()))
	assert.Equal(t, 1, fs.count(), "entry recorded even though store errored (best-effort)")
}

// Drop count atomic
func TestAuditLogger_ConcurrentWrites_RaceFree(t *testing.T) {
	fs := &fakeStore{}
	l := NewAuditLogger(fs)
	l.Start()
	var wg sync.WaitGroup
	const n = 1000
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			l.Write(&model.ComplianceAuditLog{RuleLayer: "L0"})
		}()
	}
	wg.Wait()
	require.NoError(t, l.Stop(context.Background()))
	// Some may have been dropped if queue filled briefly, but total = written + drop
	total := uint64(fs.count()) + l.DropCount()
	assert.Equal(t, uint64(n), total)
}
