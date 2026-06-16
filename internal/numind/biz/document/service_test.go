package document

import (
	"context"
	"errors"
	"strings"
	"testing"

	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/model"
	"numind-server/internal/pkg/util"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// fakeDocStore 是 IDocumentStore 的内存实现（返回副本避免别名）。
type fakeDocStore struct {
	rows      map[uint64]*model.Document
	nextID    uint64
	createErr error
	// raceFirstMiss=true 时，首次 GetByUserAndSource 强制 miss（模拟并发两请求同时未命中），
	// 之后正常查找——配合 createErr 触发"Create 冲突→回查命中"的并发 race 路径。
	raceFirstMiss bool
	srcCalls      int
}

func newFakeDocStore() *fakeDocStore { return &fakeDocStore{rows: map[uint64]*model.Document{}} }

func (f *fakeDocStore) GetByUserAndSource(_ context.Context, userID uint, key string) (*model.Document, error) {
	f.srcCalls++
	if f.raceFirstMiss && f.srcCalls == 1 {
		return nil, gorm.ErrRecordNotFound
	}
	for _, d := range f.rows {
		if d.UserID == userID && d.SourceObjectKey == key {
			cp := *d
			return &cp, nil
		}
	}
	return nil, gorm.ErrRecordNotFound
}

func (f *fakeDocStore) GetByID(_ context.Context, id uint64) (*model.Document, error) {
	if d, ok := f.rows[id]; ok {
		cp := *d
		return &cp, nil
	}
	return nil, gorm.ErrRecordNotFound
}

func (f *fakeDocStore) Create(_ context.Context, d *model.Document) error {
	if f.createErr != nil {
		return f.createErr
	}
	f.nextID++
	d.ID = f.nextID
	cp := *d
	f.rows[d.ID] = &cp
	return nil
}

func (f *fakeDocStore) UpdateContent(_ context.Context, id uint64, contentMD, title string) error {
	if d, ok := f.rows[id]; ok {
		d.ContentMD = contentMD
		d.Title = title
		return nil
	}
	return gorm.ErrRecordNotFound
}

// newTestService 构造注入了 fake 依赖的 service（parser=nil：文本路径不触达）。
func newTestService(store *fakeDocStore, dl cosDownloader) *service {
	return &service{store: store, download: dl, parser: nil, fallback: nil}
}

func dlReturns(data []byte, err error) cosDownloader {
	return func(_ context.Context, _ string) ([]byte, error) { return data, err }
}

const cosBase = "https://b.cos.ap-chengdu.myqcloud.com/"

func TestOpen_IDOR_Forbidden(t *testing.T) {
	called := false
	svc := newTestService(newFakeDocStore(), func(_ context.Context, _ string) ([]byte, error) {
		called = true
		return nil, nil
	})
	// 用户 7 传用户 8 的产物 key
	_, err := svc.OpenFromArtifact(context.Background(), 7, nil, OpenReq{
		SourceURL: cosBase + "agent-outputs/8/1-secret.md", Filename: "secret.md", Mime: "text/markdown",
	})
	assert.True(t, errors.Is(err, errno.ErrDocumentSourceForbidden), "跨用户 key 必须返回 ErrDocumentSourceForbidden")
	assert.False(t, called, "越权应在下载前被拦截")
}

func TestOpen_NonAgentOutputs_Forbidden(t *testing.T) {
	svc := newTestService(newFakeDocStore(), dlReturns(nil, nil))
	_, err := svc.OpenFromArtifact(context.Background(), 7, nil, OpenReq{
		SourceURL: cosBase + "avatars/7/x.md", Filename: "x.md", Mime: "text/markdown",
	})
	assert.True(t, errors.Is(err, errno.ErrDocumentSourceForbidden))
}

func TestOpen_NotEditable(t *testing.T) {
	svc := newTestService(newFakeDocStore(), dlReturns(nil, nil))
	_, err := svc.OpenFromArtifact(context.Background(), 7, nil, OpenReq{
		SourceURL: cosBase + "agent-outputs/7/1-chart.png", Filename: "chart.png", Mime: "image/png",
	})
	assert.True(t, errors.Is(err, errno.ErrDocumentNotEditable))
}

func TestOpen_SourceExpired(t *testing.T) {
	svc := newTestService(newFakeDocStore(), dlReturns(nil, util.ErrCOSObjectNotFound))
	_, err := svc.OpenFromArtifact(context.Background(), 7, nil, OpenReq{
		SourceURL: cosBase + "agent-outputs/7/1-gone.md", Filename: "gone.md", Mime: "text/markdown",
	})
	assert.True(t, errors.Is(err, errno.ErrDocumentSourceExpired), "源对象 404 → 410 过期")
}

func TestOpen_Materialize_Then_Hit(t *testing.T) {
	store := newFakeDocStore()
	dl := dlReturns([]byte("# 报告\n初稿"), nil)
	svc := newTestService(store, dl)
	ctx := context.Background()
	req := OpenReq{SourceURL: cosBase + "agent-outputs/7/1-r.md", Filename: "报告.md", Mime: "text/markdown"}

	// 首次：懒建档
	dto, err := svc.OpenFromArtifact(ctx, 7, nil, req)
	require.NoError(t, err)
	assert.NotZero(t, dto.ID)
	assert.Equal(t, "报告", dto.Title)
	assert.Equal(t, "# 报告\n初稿", dto.ContentMD)
	assert.Equal(t, "direct", dto.ParseMethod)

	// 模拟用户编辑后落库
	require.NoError(t, store.UpdateContent(ctx, dto.ID, "# 报告\n已编辑", "报告"))

	// 二次打开：返回上次编辑版（US5），且不应再下载
	dl2Called := false
	svc.download = func(_ context.Context, _ string) ([]byte, error) { dl2Called = true; return nil, nil }
	dto2, err := svc.OpenFromArtifact(ctx, 7, nil, req)
	require.NoError(t, err)
	assert.Equal(t, dto.ID, dto2.ID)
	assert.Equal(t, "# 报告\n已编辑", dto2.ContentMD, "二次打开应返回编辑后内容")
	assert.False(t, dl2Called, "命中已建档不应再次下载源对象")
}

func TestOpen_TooLarge(t *testing.T) {
	big := strings.Repeat("a", maxContentBytes+1)
	svc := newTestService(newFakeDocStore(), dlReturns([]byte(big), nil))
	_, err := svc.OpenFromArtifact(context.Background(), 7, nil, OpenReq{
		SourceURL: cosBase + "agent-outputs/7/1-big.md", Filename: "big.md", Mime: "text/markdown",
	})
	assert.True(t, errors.Is(err, errno.ErrDocumentTooLarge))
}

func TestOpen_ConcurrentRace_RefetchWins(t *testing.T) {
	store := newFakeDocStore()
	// 预置另一个并发请求"已建好"的文档
	require.NoError(t, store.Create(context.Background(), &model.Document{
		UserID: 7, SourceObjectKey: "agent-outputs/7/1-r.md", Title: "报告", ContentMD: "已被并发建好", ParseMethod: "direct",
	}))
	// 首次 GetByUserAndSource 强制 miss（模拟两请求同时未命中）→ 走 materialize → Create 撞唯一键冲突 → 回查命中。
	store.raceFirstMiss = true
	store.createErr = gorm.ErrDuplicatedKey
	svc := newTestService(store, dlReturns([]byte("# 报告\n本请求的解析"), nil))

	dto, err := svc.OpenFromArtifact(context.Background(), 7, nil, OpenReq{
		SourceURL: cosBase + "agent-outputs/7/1-r.md", Filename: "报告.md", Mime: "text/markdown",
	})
	require.NoError(t, err, "Create 冲突后应回查返回已存在文档而非报错")
	assert.Equal(t, "已被并发建好", dto.ContentMD, "应返回并发请求已建好的内容")
}

func TestGet_OwnershipDenied(t *testing.T) {
	store := newFakeDocStore()
	require.NoError(t, store.Create(context.Background(), &model.Document{
		UserID: 7, SourceObjectKey: "agent-outputs/7/1-a.md", Title: "a", ContentMD: "x", ParseMethod: "direct",
	}))
	svc := newTestService(store, dlReturns(nil, nil))

	// 本人可取
	_, err := svc.Get(context.Background(), 7, 1)
	require.NoError(t, err)

	// 他人取 → NotFound（不泄露存在性）
	_, err = svc.Get(context.Background(), 8, 1)
	assert.True(t, errors.Is(err, errno.ErrDocumentNotFound))
}

func TestSave_OwnershipAndUpdate(t *testing.T) {
	store := newFakeDocStore()
	require.NoError(t, store.Create(context.Background(), &model.Document{
		UserID: 7, SourceObjectKey: "agent-outputs/7/1-a.md", Title: "old", ContentMD: "old", ParseMethod: "direct",
	}))
	svc := newTestService(store, dlReturns(nil, nil))
	ctx := context.Background()

	// 他人保存 → NotFound
	_, err := svc.Save(ctx, 8, 1, SaveReq{ContentMD: "hacked"})
	assert.True(t, errors.Is(err, errno.ErrDocumentNotFound))
	got, _ := store.GetByID(ctx, 1)
	assert.Equal(t, "old", got.ContentMD, "越权保存不得改动")

	// 本人保存（不传 title 保持原标题）
	dto, err := svc.Save(ctx, 7, 1, SaveReq{ContentMD: "new body"})
	require.NoError(t, err)
	assert.Equal(t, "new body", dto.ContentMD)
	assert.Equal(t, "old", dto.Title, "不传 title 应保持原标题")
}

func TestSave_TooLarge(t *testing.T) {
	store := newFakeDocStore()
	require.NoError(t, store.Create(context.Background(), &model.Document{
		UserID: 7, SourceObjectKey: "agent-outputs/7/1-a.md", Title: "t", ContentMD: "x", ParseMethod: "direct",
	}))
	svc := newTestService(store, dlReturns(nil, nil))
	_, err := svc.Save(context.Background(), 7, 1, SaveReq{ContentMD: strings.Repeat("a", maxContentBytes+1)})
	assert.True(t, errors.Is(err, errno.ErrDocumentTooLarge))
}
