package biz

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"numind-server/internal/numind/store"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestNewBizWiresStudentRunAttachmentStore(t *testing.T) {
	viper.Set("salesrag.vector_store.type", "memory")
	viper.Set("features.feishu_integration.enabled", false)
	viper.Set("agent.memory.digest.enabled", false)

	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "biz-wiring.db")), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)

	b := NewBiz(store.NewTestStore(db))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		b.CloseComplianceAudit(ctx)
		b.CloseMemoryExtractor(ctx)
		b.CloseDigestCron(ctx)
		b.CloseXhsEnricher(ctx)
		_ = b.CloseExternalResumeLifecycle(ctx)
	})

	require.NotNil(t, b.studentRunSvc)
	require.True(t, studentRunAttachmentStoreConfigured(b.studentRunSvc),
		"student runs must resolve attachment_ids into file_read references")
}

func studentRunAttachmentStoreConfigured(service any) bool {
	v := reflect.ValueOf(service)
	if v.Kind() == reflect.Pointer {
		v = v.Elem()
	}
	if !v.IsValid() {
		return false
	}
	field := v.FieldByName("attachmentStore")
	return field.IsValid() && !field.IsNil()
}
