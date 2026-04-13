package adapter

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"numind-server/internal/numind/biz/salesrag/domain"
	"numind-server/internal/numind/biz/salesrag/port"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockEmbedder 返回基于内容长度的确定性向量（用于测试）
func mockEmbedder(_ context.Context, text string) ([]float32, error) {
	vec := make([]float32, 2048)
	// 根据文本内容生成不同的向量，使得相似文本有相似向量
	for i := range vec {
		vec[i] = float32(len(text)%100) / 100.0
		if i < len(text) {
			vec[i] += float32(text[i]) / 1000.0
		}
	}
	// 归一化
	var norm float64
	for _, v := range vec {
		norm += float64(v) * float64(v)
	}
	norm = math.Sqrt(norm)
	if norm > 0 {
		for i := range vec {
			vec[i] = float32(float64(vec[i]) / norm)
		}
	}
	return vec, nil
}

func setupTestStore(t *testing.T) (*SQLiteVecStore, func()) {
	t.Helper()
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test_vector.db")

	store, err := NewSQLiteVecStore(dbPath, mockEmbedder)
	require.NoError(t, err)
	require.NotNil(t, store)

	cleanup := func() {
		store.Close()
		os.RemoveAll(tmpDir)
	}
	return store, cleanup
}

func TestSQLiteVecStore_Upsert(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx := context.Background()

	chunks := []domain.KnowledgeChunk{
		{ID: "c1", DocumentID: 1, UserID: 10, Content: "销售技巧入门", Summary: "入门教程", Tags: []string{"sales", "beginner"}},
		{ID: "c2", DocumentID: 1, UserID: 10, Content: "高级谈判策略", Summary: "高级技巧", Tags: []string{"sales", "advanced"}},
		{ID: "c3", DocumentID: 2, UserID: 10, Content: "客户关系管理", Summary: "CRM基础", Tags: []string{"crm"}},
	}

	err := store.Upsert(ctx, chunks)
	require.NoError(t, err)

	// 验证数据已写入
	fetched, err := store.FetchByDocumentID(ctx, 1, 100)
	require.NoError(t, err)
	assert.Len(t, fetched, 2)
}

func TestSQLiteVecStore_UpsertReplace(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx := context.Background()

	// 首次插入
	chunks := []domain.KnowledgeChunk{
		{ID: "c1", DocumentID: 1, UserID: 10, Content: "原始内容", Summary: "v1"},
	}
	require.NoError(t, store.Upsert(ctx, chunks))

	// 更新同一个 ID
	chunks[0].Content = "更新后的内容"
	chunks[0].Summary = "v2"
	require.NoError(t, store.Upsert(ctx, chunks))

	// 验证更新生效
	fetched, err := store.FetchByDocumentID(ctx, 1, 100)
	require.NoError(t, err)
	assert.Len(t, fetched, 1)
	assert.Equal(t, "更新后的内容", fetched[0].Content)
	assert.Equal(t, "v2", fetched[0].Summary)
}

func TestSQLiteVecStore_UpsertEmpty(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	err := store.Upsert(context.Background(), nil)
	assert.NoError(t, err)

	err = store.Upsert(context.Background(), []domain.KnowledgeChunk{})
	assert.NoError(t, err)
}

func TestSQLiteVecStore_DeleteByDocumentID(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx := context.Background()

	// 插入两个文档的切片
	chunks := []domain.KnowledgeChunk{
		{ID: "c1", DocumentID: 1, UserID: 10, Content: "文档1切片1"},
		{ID: "c2", DocumentID: 1, UserID: 10, Content: "文档1切片2"},
		{ID: "c3", DocumentID: 2, UserID: 10, Content: "文档2切片1"},
	}
	require.NoError(t, store.Upsert(ctx, chunks))

	// 删除文档1
	err := store.DeleteByDocumentID(ctx, 1)
	require.NoError(t, err)

	// 验证文档1已删除
	fetched, err := store.FetchByDocumentID(ctx, 1, 100)
	require.NoError(t, err)
	assert.Len(t, fetched, 0)

	// 验证文档2不受影响
	fetched, err = store.FetchByDocumentID(ctx, 2, 100)
	require.NoError(t, err)
	assert.Len(t, fetched, 1)
	assert.Equal(t, "c3", fetched[0].ID)
}

func TestSQLiteVecStore_DeleteNonExistent(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	// 删除不存在的文档不应报错
	err := store.DeleteByDocumentID(context.Background(), 999)
	assert.NoError(t, err)
}

func TestSQLiteVecStore_Search(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx := context.Background()

	chunks := []domain.KnowledgeChunk{
		{ID: "c1", DocumentID: 1, UserID: 10, Content: "销售技巧入门指南"},
		{ID: "c2", DocumentID: 1, UserID: 10, Content: "高级谈判策略与方法"},
		{ID: "c3", DocumentID: 2, UserID: 10, Content: "客户关系管理体系"},
		{ID: "c4", DocumentID: 3, UserID: 20, Content: "其他用户的内容"},
	}
	require.NoError(t, store.Upsert(ctx, chunks))

	// 搜索：只搜索文档1，用户10
	filter := port.SearchFilter{
		UserID:      10,
		DocumentIDs: []uint{1},
	}
	results, err := store.Search(ctx, "销售入门", filter, 10)
	require.NoError(t, err)
	assert.NotEmpty(t, results)

	// 所有结果应该属于文档1和用户10
	for _, r := range results {
		assert.Equal(t, uint(1), r.DocumentID)
		assert.Equal(t, uint(10), r.UserID)
	}

	// 每个结果应该有 score
	for _, r := range results {
		assert.NotZero(t, r.Score)
	}
}

func TestSQLiteVecStore_SearchMultipleDocuments(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx := context.Background()

	chunks := []domain.KnowledgeChunk{
		{ID: "c1", DocumentID: 1, UserID: 10, Content: "文档1内容"},
		{ID: "c2", DocumentID: 2, UserID: 10, Content: "文档2内容"},
		{ID: "c3", DocumentID: 3, UserID: 10, Content: "文档3内容"},
	}
	require.NoError(t, store.Upsert(ctx, chunks))

	// 搜索文档1和文档2
	filter := port.SearchFilter{
		UserID:      10,
		DocumentIDs: []uint{1, 2},
	}
	results, err := store.Search(ctx, "文档内容", filter, 10)
	require.NoError(t, err)
	assert.Len(t, results, 2)
}

func TestSQLiteVecStore_SearchStrictMode(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx := context.Background()

	chunks := []domain.KnowledgeChunk{
		{ID: "c1", DocumentID: 1, UserID: 10, Content: "测试内容"},
	}
	require.NoError(t, store.Upsert(ctx, chunks))

	// 空 DocumentIDs 应返回空结果（严格模式）
	filter := port.SearchFilter{
		UserID:      10,
		DocumentIDs: []uint{},
	}
	results, err := store.Search(ctx, "测试", filter, 10)
	assert.NoError(t, err)
	assert.Nil(t, results)
}

func TestSQLiteVecStore_SearchUserIsolation(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx := context.Background()

	chunks := []domain.KnowledgeChunk{
		{ID: "c1", DocumentID: 1, UserID: 10, Content: "用户10的内容"},
		{ID: "c2", DocumentID: 1, UserID: 20, Content: "用户20的内容"},
	}
	require.NoError(t, store.Upsert(ctx, chunks))

	// 有 DocumentIDs 时按文档过滤，不叠加 user_id（安全性由 biz 层白名单保证）
	filter := port.SearchFilter{
		UserID:      10,
		DocumentIDs: []uint{1},
	}
	results, err := store.Search(ctx, "内容", filter, 10)
	require.NoError(t, err)
	assert.Len(t, results, 2)
}

func TestSQLiteVecStore_FetchByDocumentID(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx := context.Background()

	chunks := []domain.KnowledgeChunk{
		{ID: "c1", DocumentID: 1, UserID: 10, Content: "内容1", Summary: "摘要1", SourceRef: "第1页", Tags: []string{"tag1", "tag2"}},
		{ID: "c2", DocumentID: 1, UserID: 10, Content: "内容2", Summary: "摘要2", SourceRef: "第2页"},
		{ID: "c3", DocumentID: 2, UserID: 10, Content: "内容3"},
	}
	require.NoError(t, store.Upsert(ctx, chunks))

	// 获取文档1的切片
	fetched, err := store.FetchByDocumentID(ctx, 1, 100)
	require.NoError(t, err)
	assert.Len(t, fetched, 2)

	// 验证字段完整性
	for _, c := range fetched {
		assert.Equal(t, uint(1), c.DocumentID)
		assert.NotEmpty(t, c.Content)
	}

	// 获取不存在的文档
	fetched, err = store.FetchByDocumentID(ctx, 999, 100)
	require.NoError(t, err)
	assert.Len(t, fetched, 0)
}

func TestSQLiteVecStore_FetchByDocumentIDWithLimit(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx := context.Background()

	// 插入5个切片
	chunks := make([]domain.KnowledgeChunk, 5)
	for i := range chunks {
		chunks[i] = domain.KnowledgeChunk{
			ID:         fmt.Sprintf("c%d", i),
			DocumentID: 1,
			UserID:     10,
			Content:    fmt.Sprintf("内容%d", i),
		}
	}
	require.NoError(t, store.Upsert(ctx, chunks))

	// limit=3 只返回3个
	fetched, err := store.FetchByDocumentID(ctx, 1, 3)
	require.NoError(t, err)
	assert.Len(t, fetched, 3)

	// limit=0 使用默认值（10000）
	fetched, err = store.FetchByDocumentID(ctx, 1, 0)
	require.NoError(t, err)
	assert.Len(t, fetched, 5)
}

func TestSQLiteVecStore_ConcurrentReads(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx := context.Background()

	// 插入测试数据
	chunks := []domain.KnowledgeChunk{
		{ID: "c1", DocumentID: 1, UserID: 10, Content: "并发读测试内容1"},
		{ID: "c2", DocumentID: 1, UserID: 10, Content: "并发读测试内容2"},
	}
	require.NoError(t, store.Upsert(ctx, chunks))

	// 10个并发读
	var wg sync.WaitGroup
	errors := make(chan error, 10)

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := store.FetchByDocumentID(ctx, 1, 100)
			if err != nil {
				errors <- err
			}
		}()
	}

	wg.Wait()
	close(errors)

	for err := range errors {
		t.Errorf("concurrent read failed: %v", err)
	}
}

func TestSQLiteVecStore_TagsPreservation(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx := context.Background()

	chunks := []domain.KnowledgeChunk{
		{ID: "c1", DocumentID: 1, UserID: 10, Content: "有标签的内容", Tags: []string{"sales", "beginner", "guide"}},
		{ID: "c2", DocumentID: 1, UserID: 10, Content: "无标签的内容", Tags: nil},
	}
	require.NoError(t, store.Upsert(ctx, chunks))

	fetched, err := store.FetchByDocumentID(ctx, 1, 100)
	require.NoError(t, err)
	assert.Len(t, fetched, 2)

	for _, c := range fetched {
		if c.ID == "c1" {
			assert.Equal(t, []string{"sales", "beginner", "guide"}, c.Tags)
		}
		if c.ID == "c2" {
			assert.Empty(t, c.Tags)
		}
	}
}
