package skill

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"numind-server/internal/pkg/model"
)

// mockTemplateStore implements ISkillTemplateStore for unit testing.
type mockTemplateStore struct {
	listResult []model.SkillTemplate
	listErr    error
	getResult  *model.SkillTemplate
	getErr     error
}

func (m *mockTemplateStore) List(_ context.Context) ([]model.SkillTemplate, error) {
	return m.listResult, m.listErr
}

func (m *mockTemplateStore) GetByID(_ context.Context, _ uint64) (*model.SkillTemplate, error) {
	return m.getResult, m.getErr
}

func TestTemplateService_List_returnsActiveOnly(t *testing.T) {
	expected := []model.SkillTemplate{
		{ID: 1, Name: "学员爆款分析师", IsActive: true, DisplayOrder: 10},
		{ID: 2, Name: "周度复盘报告助手", IsActive: true, DisplayOrder: 20},
	}
	svc := NewTemplateService(&mockTemplateStore{listResult: expected})

	got, err := svc.List(context.Background())

	require.NoError(t, err)
	assert.Len(t, got, 2)
	assert.Equal(t, "学员爆款分析师", got[0].Name)
	assert.Equal(t, "周度复盘报告助手", got[1].Name)
}

func TestTemplateService_List_storeError_wrapped(t *testing.T) {
	storeErr := errors.New("db connection lost")
	svc := NewTemplateService(&mockTemplateStore{listErr: storeErr})

	got, err := svc.List(context.Background())

	assert.Nil(t, got)
	require.Error(t, err)
	assert.ErrorIs(t, err, storeErr, "original error must be wrapped and unwrappable")
	assert.Contains(t, err.Error(), "TemplateService.List")
}

func TestTemplateService_GetByID_returnsTemplate(t *testing.T) {
	tmpl := &model.SkillTemplate{ID: 3, Name: "选题创意助手", IsActive: true}
	svc := NewTemplateService(&mockTemplateStore{getResult: tmpl})

	got, err := svc.GetByID(context.Background(), 3)

	require.NoError(t, err)
	assert.Equal(t, uint64(3), got.ID)
	assert.Equal(t, "选题创意助手", got.Name)
}

func TestTemplateService_GetByID_storeError_wrapped(t *testing.T) {
	storeErr := errors.New("record not found")
	svc := NewTemplateService(&mockTemplateStore{getErr: storeErr})

	got, err := svc.GetByID(context.Background(), 999)

	assert.Nil(t, got)
	require.Error(t, err)
	assert.ErrorIs(t, err, storeErr, "original error must be wrapped and unwrappable")
	assert.Contains(t, err.Error(), "TemplateService.GetByID(id=999)")
}
