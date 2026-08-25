package sop

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/model"
)

var errStopAfterRegenerationReset = errors.New("stop after regeneration reset")

type preservationSopStore struct {
	store.ISopStore
	executionContext *store.ExecutionContext
	bookmark         *model.SopNodeBookmark
	cleanupCalls     int
	deletedBookmarks []uint
}

func (f *preservationSopStore) GetExecutionContext(_, _ uint) (*store.ExecutionContext, error) {
	return f.executionContext, nil
}

func (f *preservationSopStore) UpdateNodeRun(_ uint, _ map[string]interface{}) error {
	return nil
}

func (f *preservationSopStore) CleanupDownstreamForRegeneration(_ uint, _ int) error {
	f.cleanupCalls++
	return nil
}

func (f *preservationSopStore) GetNodeRun(_ uint) (*model.SopNodeRun, error) {
	return nil, errStopAfterRegenerationReset
}

func (f *preservationSopStore) GetBookmark(_ uint) (*model.SopNodeBookmark, error) {
	return f.bookmark, nil
}

func (f *preservationSopStore) DeleteBookmark(id uint) error {
	f.deletedBookmarks = append(f.deletedBookmarks, id)
	return nil
}

type preservationCustomerStore struct {
	store.ICustomerStore
}

func (f *preservationCustomerStore) HasTemplatePermission(context.Context, uint, uint) (bool, error) {
	return true, nil
}

type preservationDatastore struct {
	store.IStore
	sop       store.ISopStore
	customers store.ICustomerStore
}

func (f *preservationDatastore) Sop() store.ISopStore {
	return f.sop
}

func (f *preservationDatastore) Customers() store.ICustomerStore {
	return f.customers
}

func TestExecuteNodeStream_RegenerationPreservesDownstreamRecords(t *testing.T) {
	currentNode := model.SopNode{
		Model:      gorm.Model{ID: 11},
		TemplateID: 7,
		Name:       "第一步",
		Sort:       0,
	}
	downstreamNode := model.SopNode{
		Model:      gorm.Model{ID: 12},
		TemplateID: 7,
		Name:       "第二步",
		Sort:       1,
	}
	existingRun := &model.SopNodeRun{
		Model:      gorm.Model{ID: 101},
		RunID:      5365,
		NodeID:     currentNode.ID,
		TemplateID: 7,
		UserID:     450,
		Status:     model.SopStatusSucceeded,
		Input:      "原输入",
		Output:     "原输出",
		Sort:       currentNode.Sort,
	}
	storeStub := &preservationSopStore{
		executionContext: &store.ExecutionContext{
			Run: &model.SopRun{
				Model:          gorm.Model{ID: 5365},
				TemplateID:     7,
				UserID:         450,
				Status:         model.SopStatusRunning,
				ConversationID: "conversation-5365",
			},
			Node:            &currentNode,
			Template:        &model.SopTemplate{Model: gorm.Model{ID: 7}},
			AllNodes:        []model.SopNode{currentNode, downstreamNode},
			AllNodeRuns:     []model.SopNodeRun{*existingRun, {Model: gorm.Model{ID: 102}, NodeID: downstreamNode.ID, Status: model.SopStatusSucceeded, Sort: downstreamNode.Sort}},
			ExistingNodeRun: existingRun,
		},
	}
	biz := &sopBiz{ds: &preservationDatastore{
		sop:       storeStub,
		customers: &preservationCustomerStore{},
	}}

	err := biz.ExecuteNodeStream(context.Background(), 5365, currentNode.ID, "新输入", "", false, func(string, string) error { return nil })

	require.ErrorIs(t, err, errStopAfterRegenerationReset)
	require.Zero(t, storeStub.cleanupCalls, "regenerating an earlier step must not delete downstream saved records")
}

func TestDeleteBookmark_DeletesOnlySelectedBookmark(t *testing.T) {
	storeStub := &preservationSopStore{
		bookmark: &model.SopNodeBookmark{
			Model:  gorm.Model{ID: 201},
			UserID: 450,
		},
	}
	biz := &sopBiz{ds: &preservationDatastore{sop: storeStub}}

	err := biz.DeleteBookmark(context.Background(), 201, 450)

	require.NoError(t, err)
	require.Equal(t, []uint{201}, storeStub.deletedBookmarks)
	require.Zero(t, storeStub.cleanupCalls, "deleting one bookmark must not delete downstream saved records")
}
