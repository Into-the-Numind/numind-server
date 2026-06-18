package numind

import (
	"context"
	"errors"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"numind-server/internal/pkg/aiservice"
	"numind-server/internal/pkg/aiservice/registry"
	"numind-server/internal/pkg/model"
)

// fakeNativeChatProvider is a minimal ChatProvider used to register a native
// adapter name into a test gateway without dragging in the real adapter http
// stack.
type fakeNativeChatProvider struct{ name string }

func (f *fakeNativeChatProvider) Name() string           { return f.name }
func (f *fakeNativeChatProvider) ProviderType() string   { return "fake" }
func (f *fakeNativeChatProvider) Capabilities() []string { return []string{"chat"} }
func (f *fakeNativeChatProvider) Chat(_ context.Context, _ *registry.ResolvedRoute, _ aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
	return nil, errors.New("fake")
}
func (f *fakeNativeChatProvider) ChatStream(_ context.Context, _ *registry.ResolvedRoute, _ aiservice.ChatRequest) (<-chan aiservice.ChatChunk, error) {
	return nil, errors.New("fake")
}

func newNativeAssertTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("db handle: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.AutoMigrate(&model.LLMProvider{}); err != nil {
		t.Fatalf("automigrate llm_provider: %v", err)
	}
	return db
}

func seedProviderRow(t *testing.T, db *gorm.DB, name string, active bool) {
	t.Helper()
	row := &model.LLMProvider{
		Name:        name,
		DisplayName: name,
		BaseURL:     "https://www.dmxapi.cn",
		APIKey:      "x",
		IsActive:    active,
	}
	if err := db.Create(row).Error; err != nil {
		t.Fatalf("seed provider %s: %v", name, err)
	}
	// model.LLMProvider.IsActive carries gorm `default:true`; an explicit false
	// can be skipped by GORM on Create (database.md §6). Force it for the test.
	if !active {
		if err := db.Model(&model.LLMProvider{}).Where("name = ?", name).UpdateColumn("is_active", false).Error; err != nil {
			t.Fatalf("force is_active=false for %s: %v", name, err)
		}
	}
}

// TestAssertNativeAdaptersRegistered covers the finding #1 startup guard:
//
//	(a) no native rows at all                          ⇒ pass (no-op)
//	(b) native row(s) present but is_active=0          ⇒ pass (no-op, deploy-before-activate window)
//	(c) active native row WITH the adapter registered  ⇒ pass
//	(d) active native row WITHOUT the adapter          ⇒ error (would log.Fatalw at boot)
func TestAssertNativeAdaptersRegistered(t *testing.T) {
	t.Run("no native rows passes", func(t *testing.T) {
		db := newNativeAssertTestDB(t)
		gw := aiservice.Build(aiservice.Deps{})
		if err := assertNativeAdaptersRegistered(gw, db); err != nil {
			t.Fatalf("expected nil error when no native rows exist, got %v", err)
		}
	})

	t.Run("inactive native rows are a no-op", func(t *testing.T) {
		db := newNativeAssertTestDB(t)
		seedProviderRow(t, db, "claude-native", false)
		seedProviderRow(t, db, "gemini-native", false)
		gw := aiservice.Build(aiservice.Deps{}) // adapters NOT registered
		if err := assertNativeAdaptersRegistered(gw, db); err != nil {
			t.Fatalf("inactive native rows must be a no-op; got %v", err)
		}
	})

	t.Run("active native row with adapter registered passes", func(t *testing.T) {
		db := newNativeAssertTestDB(t)
		seedProviderRow(t, db, "claude-native", true)
		gw := aiservice.Build(aiservice.Deps{})
		gw.RegisterProvider(&fakeNativeChatProvider{name: "claude-native"})
		if err := assertNativeAdaptersRegistered(gw, db); err != nil {
			t.Fatalf("active row with adapter registered must pass; got %v", err)
		}
	})

	t.Run("active native row WITHOUT adapter fatals", func(t *testing.T) {
		db := newNativeAssertTestDB(t)
		seedProviderRow(t, db, "claude-native", true)
		gw := aiservice.Build(aiservice.Deps{}) // claude-native NOT registered
		err := assertNativeAdaptersRegistered(gw, db)
		if err == nil {
			t.Fatal("active native row without a registered adapter MUST return an error (boot guard)")
		}
		// The dmxapi prefix fallback must NOT mask the gap: even with dmxapi
		// registered, the exact lookup misses ⇒ still an error.
		gw.RegisterProvider(&fakeNativeChatProvider{name: "dmxapi"})
		if err := assertNativeAdaptersRegistered(gw, db); err == nil {
			t.Fatal("dmxapi fallback must NOT satisfy the native assertion (exact-only)")
		}
	})
}
