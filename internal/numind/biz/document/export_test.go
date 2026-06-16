package document

import (
	"context"
	"errors"
	"testing"

	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func seedExportDoc(t *testing.T) (*fakeDocStore, uint64) {
	t.Helper()
	store := newFakeDocStore()
	require.NoError(t, store.Create(context.Background(), &model.Document{
		UserID: 7, SourceObjectKey: "agent-outputs/7/1-a.md", Title: "季度/报告", ContentMD: "# 报告\n正文", ParseMethod: "direct",
	}))
	return store, 1
}

func TestExport_Markdown_NoSandbox(t *testing.T) {
	store, id := seedExportDoc(t)
	svc := &service{store: store, exportGuard: newUserGuard(), pool: nil}

	name, ctype, data, err := svc.Export(context.Background(), 7, id, "md")
	require.NoError(t, err)
	assert.Equal(t, "季度_报告.md", name, "标题中的 / 应清洗为 _")
	assert.Contains(t, ctype, "text/markdown")
	assert.Equal(t, "# 报告\n正文", string(data))
}

func TestExport_OwnershipDenied(t *testing.T) {
	store, id := seedExportDoc(t)
	svc := &service{store: store, exportGuard: newUserGuard(), pool: nil}

	_, _, _, err := svc.Export(context.Background(), 8, id, "md")
	assert.True(t, errors.Is(err, errno.ErrDocumentNotFound), "他人导出应 NotFound")
}

func TestExport_InvalidFormat(t *testing.T) {
	store, id := seedExportDoc(t)
	svc := &service{store: store, exportGuard: newUserGuard(), pool: nil}

	_, _, _, err := svc.Export(context.Background(), 7, id, "txt")
	assert.True(t, errors.Is(err, errno.ErrDocumentExportFormat))
}

func TestExport_PDF_NoPool_Unavailable(t *testing.T) {
	store, id := seedExportDoc(t)
	svc := &service{store: store, exportGuard: newUserGuard(), pool: nil}

	_, _, _, err := svc.Export(context.Background(), 7, id, "pdf")
	assert.True(t, errors.Is(err, errno.ErrDocumentExportUnavailable), "无沙箱 pdf 应优雅降级 Unavailable")
}

func TestExport_ConcurrencyGuard_Busy(t *testing.T) {
	store, id := seedExportDoc(t)
	guard := newUserGuard()
	svc := &service{store: store, exportGuard: guard, pool: nil}

	// 预先占用用户 7 的导出槽，模拟已有导出在跑
	require.True(t, guard.tryAcquire(7))

	_, _, _, err := svc.Export(context.Background(), 7, id, "docx")
	assert.True(t, errors.Is(err, errno.ErrDocumentExportBusy), "同用户并发 pdf/docx 导出应 Busy")

	// md 不受并发守卫限制（不走沙箱）
	_, _, _, err = svc.Export(context.Background(), 7, id, "md")
	assert.NoError(t, err, "md 导出不受并发守卫限制")
}

func TestUserGuard_AcquireRelease(t *testing.T) {
	g := newUserGuard()
	assert.True(t, g.tryAcquire(1))
	assert.False(t, g.tryAcquire(1), "重复占用应失败")
	assert.True(t, g.tryAcquire(2), "不同用户互不影响")
	g.release(1)
	assert.True(t, g.tryAcquire(1), "释放后可再占用")
}
