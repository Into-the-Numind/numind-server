package memory

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/model"
)

// newCadenceTestStores spins up an in-memory SQLite + auto-migrates the memory
// schema for cadence tests. Matches the extractor_test convention so reviewers
// can compare patterns side-by-side.
func newCadenceTestStores(t *testing.T) (store.IUserMemoryProfileStore, *gorm.DB) {
	t.Helper()
	tmp := t.TempDir()
	dsn := tmp + "/test.db?_busy_timeout=5000&_journal_mode=WAL"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.UserMemoryProfile{}, &model.UserMemoryFact{}))
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	return store.NewUserMemoryProfileStore(db), db
}

// frozen returns a clock function that always returns t.
func frozen(t time.Time) func() time.Time {
	return func() time.Time { return t }
}

// TestShouldRunDialectic covers the spec §设计要点 decision tree boundaries.
//
// Each case constructs a fresh sqlite DB + seeds the profile row directly,
// then asserts ShouldRunDialectic against a frozen clock. The clock is
// injected via withClock (internal helper) so wall-clock progression doesn't
// flake the suite.
func TestShouldRunDialectic(t *testing.T) {
	cfg := DefaultCadenceConfig() // 5min / 30min / 3 facts
	const uid uint = 42
	now := time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name string
		// setupProfile seeds the user_memory_profile row before the test.
		// nil = no row (first-time user).
		setupProfile func(t *testing.T, ps store.IUserMemoryProfileStore)
		want         bool
		wantErr      bool
	}{
		{
			name:         "FirstTime",
			setupProfile: nil, // profile row absent
			want:         true,
		},
		{
			name: "FirstTimeRowExistsButNoInsight",
			// profile row exists (perhaps created by extractor.IncrementExtractionCount)
			// but dialectic never ran → CachedInsightAt == nil → run.
			setupProfile: func(t *testing.T, ps store.IUserMemoryProfileStore) {
				require.NoError(t, ps.Upsert(context.Background(), &model.UserMemoryProfile{
					UserID:     uid,
					TotalFacts: 7,
					// CachedInsightAt: nil (zero value)
				}))
			},
			want: true,
		},
		{
			name: "WithinCooldown", // sinceLast=4min, newFacts=0 → false
			setupProfile: func(t *testing.T, ps store.IUserMemoryProfileStore) {
				at := now.Add(-4 * time.Minute)
				require.NoError(t, ps.Upsert(context.Background(), &model.UserMemoryProfile{
					UserID:                 uid,
					TotalFacts:             10,
					CachedInsightFactCount: 10,
					CachedInsightAt:        &at,
				}))
			},
			want: false,
		},
		{
			name: "EnoughNewFacts", // sinceLast=7min, newFacts=5 → true
			setupProfile: func(t *testing.T, ps store.IUserMemoryProfileStore) {
				at := now.Add(-7 * time.Minute)
				require.NoError(t, ps.Upsert(context.Background(), &model.UserMemoryProfile{
					UserID:                 uid,
					TotalFacts:             15,
					CachedInsightFactCount: 10,
					CachedInsightAt:        &at,
				}))
			},
			want: true,
		},
		{
			name: "ExactlyMinNewFacts", // sinceLast=7min, newFacts=3 (== threshold) → true
			// Boundary pinned by spec §设计要点 "3 new facts 触发":
			// the comparison is `>=`, so exactly 3 new facts must trigger.
			// Off-by-one regression would flip this to false.
			setupProfile: func(t *testing.T, ps store.IUserMemoryProfileStore) {
				at := now.Add(-7 * time.Minute)
				require.NoError(t, ps.Upsert(context.Background(), &model.UserMemoryProfile{
					UserID:                 uid,
					TotalFacts:             13, // 10 cached + 3 new = exact threshold
					CachedInsightFactCount: 10,
					CachedInsightAt:        &at,
				}))
			},
			want: true,
		},
		{
			name: "NotEnoughNewFacts", // sinceLast=7min, newFacts=1 → false
			setupProfile: func(t *testing.T, ps store.IUserMemoryProfileStore) {
				at := now.Add(-7 * time.Minute)
				require.NoError(t, ps.Upsert(context.Background(), &model.UserMemoryProfile{
					UserID:                 uid,
					TotalFacts:             11,
					CachedInsightFactCount: 10,
					CachedInsightAt:        &at,
				}))
			},
			want: false,
		},
		{
			name: "MaxCooldownReached", // sinceLast=35min, newFacts=0 → true (anti-staleness)
			setupProfile: func(t *testing.T, ps store.IUserMemoryProfileStore) {
				at := now.Add(-35 * time.Minute)
				require.NoError(t, ps.Upsert(context.Background(), &model.UserMemoryProfile{
					UserID:                 uid,
					TotalFacts:             10,
					CachedInsightFactCount: 10,
					CachedInsightAt:        &at,
				}))
			},
			want: true,
		},
		{
			name: "ExactlyAtCooldown", // sinceLast=300s (== 5min cooldown), newFacts=0 → false
			// Spec §验证策略 explicitly pins this boundary: at exactly cooldown
			// the gate must remain closed when no new facts have arrived.
			setupProfile: func(t *testing.T, ps store.IUserMemoryProfileStore) {
				at := now.Add(-cfg.DialecticCooldown) // exactly 5min ago
				require.NoError(t, ps.Upsert(context.Background(), &model.UserMemoryProfile{
					UserID:                 uid,
					TotalFacts:             10,
					CachedInsightFactCount: 10,
					CachedInsightAt:        &at,
				}))
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ps, _ := newCadenceTestStores(t)
			if tt.setupProfile != nil {
				tt.setupProfile(t, ps)
			}
			svc := NewCadenceService(ps, cfg).withClock(frozen(now))
			got, err := svc.ShouldRunDialectic(context.Background(), uid)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestShouldRunDialectic_NilReceiver pins the safety contract: a nil-store
// service returns false + an error (never a panic / nil-deref crash).
func TestShouldRunDialectic_NilReceiver(t *testing.T) {
	svc := &CadenceService{} // no store wired
	got, err := svc.ShouldRunDialectic(context.Background(), 1)
	assert.False(t, got)
	require.Error(t, err)
}

// TestShouldRunDialectic_StoreError pins the fail-skip behaviour: if the
// store returns a non-NotFound error, we should return false + the wrapped
// error (caller logs + skips dialectic for that turn). Done with a fake
// store that always errors.
type erroringProfileStore struct {
	store.IUserMemoryProfileStore
	err error
}

func (s *erroringProfileStore) Get(_ context.Context, _ uint) (*model.UserMemoryProfile, error) {
	return nil, s.err
}

func TestShouldRunDialectic_StoreError(t *testing.T) {
	sentinel := errors.New("simulated DB failure")
	svc := NewCadenceService(&erroringProfileStore{err: sentinel}, DefaultCadenceConfig())
	got, err := svc.ShouldRunDialectic(context.Background(), 1)
	assert.False(t, got)
	require.Error(t, err)
	assert.ErrorIs(t, err, sentinel)
}

// TestLoadCadenceConfigFromViper checks the viper key→struct mapping for
// callers that wire CadenceService from production config.
func TestLoadCadenceConfigFromViper(t *testing.T) {
	v := &stubViper{vals: map[string]int{
		"agent.memory.dialectic_cooldown_seconds":     120,
		"agent.memory.dialectic_max_cooldown_seconds": 600,
		"agent.memory.dialectic_min_new_facts":        5,
	}}
	cfg := LoadCadenceConfigFromViper(v)
	assert.Equal(t, 2*time.Minute, cfg.DialecticCooldown)
	assert.Equal(t, 10*time.Minute, cfg.DialecticMaxCooldown)
	assert.Equal(t, 5, cfg.DialecticMinNewFacts)
}

// TestLoadCadenceConfigFromViper_Defaults asserts the helper falls back to
// DefaultCadenceConfig when keys are unset (viper returns 0 for missing int).
func TestLoadCadenceConfigFromViper_Defaults(t *testing.T) {
	cfg := LoadCadenceConfigFromViper(&stubViper{vals: map[string]int{}})
	assert.Equal(t, DefaultDialecticCooldown, cfg.DialecticCooldown)
	assert.Equal(t, DefaultDialecticMaxCooldown, cfg.DialecticMaxCooldown)
	assert.Equal(t, DefaultDialecticMinNewFacts, cfg.DialecticMinNewFacts)
}

// stubViper implements the narrow viperGetter surface used by LoadCadenceConfigFromViper.
type stubViper struct {
	vals map[string]int
}

func (s *stubViper) GetInt(key string) int { return s.vals[key] }
