package biz

import (
	"context"
	"errors"
	"testing"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/model"
	"numind-server/internal/pkg/retrieval/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// fakeKBDocStore 嵌入 store.KnowledgeDocumentStore 接口（nil），仅覆盖 ListByUser——
// kbDocStore 只调用 ListByUser，其余方法不会被触达。
type fakeKBDocStore struct {
	store.KnowledgeDocumentStore
	docs []*model.KnowledgeDocument
	err  error
}

func (f *fakeKBDocStore) ListByUser(ctx context.Context, userID uint) ([]*model.KnowledgeDocument, error) {
	return f.docs, f.err
}

func kbDoc(id uint, enabled bool, status string) *model.KnowledgeDocument {
	return &model.KnowledgeDocument{Model: gorm.Model{ID: id}, IsEnabled: enabled, Status: status}
}

// TestKBDocStore_ListEnabledDocIDs_FiltersEnabledCompleted 守护 AllEnabled 解析的安全过滤：
// 仅 IsEnabled && Status==COMPLETED 的文档进入 agent kb_search 的"翻全部"范围，
// 禁用/未完成文档必须被排除（防 agent 检索到不该看的文档）。
func TestKBDocStore_ListEnabledDocIDs_FiltersEnabledCompleted(t *testing.T) {
	completed := string(domain.DocStatusCompleted)
	fake := &fakeKBDocStore{docs: []*model.KnowledgeDocument{
		kbDoc(1, true, completed),  // 保留
		kbDoc(2, false, completed), // 排除：禁用
		kbDoc(3, true, "FAILED"),   // 排除：未完成
		kbDoc(4, true, "PENDING"),  // 排除：未完成
		kbDoc(5, true, completed),  // 保留
		nil,                        // nil 守卫：不应 panic
	}}

	ids, err := newKBDocStore(fake).ListEnabledDocIDs(context.Background(), 1)
	require.NoError(t, err)
	assert.ElementsMatch(t, []uint{1, 5}, ids, "仅 IsEnabled && COMPLETED 的文档应入选")
}

// TestKBDocStore_ListEnabledDocIDs_Empty 全部不合格时返回空（非 nil-panic）。
func TestKBDocStore_ListEnabledDocIDs_Empty(t *testing.T) {
	fake := &fakeKBDocStore{docs: []*model.KnowledgeDocument{
		kbDoc(1, false, string(domain.DocStatusCompleted)),
		kbDoc(2, true, "PENDING"),
	}}
	ids, err := newKBDocStore(fake).ListEnabledDocIDs(context.Background(), 1)
	require.NoError(t, err)
	assert.Empty(t, ids)
}

// TestKBDocStore_ListEnabledDocIDs_PropagatesError 底层 store 出错时透传（含 userID 便于诊断）。
func TestKBDocStore_ListEnabledDocIDs_PropagatesError(t *testing.T) {
	fake := &fakeKBDocStore{err: errors.New("db down")}
	_, err := newKBDocStore(fake).ListEnabledDocIDs(context.Background(), 7)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "userID=7")
}
