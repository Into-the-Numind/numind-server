package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
	"gorm.io/gorm"

	"numind-server/internal/pkg/model"
)

// emptyQA is the minimal valid JSON for questionnaire_answers (NOT NULL in schema).
var emptyQA = datatypes.JSON(`[]`)

// seedSkillTemplate creates a SkillTemplate directly via DB (ISkillTemplateStore is read-only).
// Handles the default:true is_active fixup pattern for inactive templates.
func seedSkillTemplate(t *testing.T, db *gorm.DB, tpl *model.SkillTemplate) {
	t.Helper()
	if tpl.QuestionnaireAnswers == nil {
		tpl.QuestionnaireAnswers = emptyQA
	}
	wantActive := tpl.IsActive
	require.NoError(t, db.Create(tpl).Error)
	// database.md §6: is_active default:1 — GORM Create skips false zero-value.
	if !wantActive && tpl.IsActive {
		require.NoError(t, db.Model(tpl).UpdateColumn("is_active", false).Error)
		tpl.IsActive = false
	}
}

// TestStore_TemplateList_returnsActiveOnly 验证 List 只返回 is_active=1 的模板，且按 display_order ASC 排序。
func TestStore_TemplateList_returnsActiveOnly(t *testing.T) {
	db := newTestDB(t, &model.SkillTemplate{})
	s := newSkillTemplateStore(db)
	ctx := context.Background()

	// 直接通过 DB 种入（ISkillTemplateStore 是只读接口，无 Create 方法）
	seedSkillTemplate(t, db, &model.SkillTemplate{Name: "Alpha", IsActive: true, DisplayOrder: 20})
	seedSkillTemplate(t, db, &model.SkillTemplate{Name: "Beta", IsActive: false, DisplayOrder: 10})
	seedSkillTemplate(t, db, &model.SkillTemplate{Name: "Gamma", IsActive: true, DisplayOrder: 5})

	list, err := s.List(ctx)
	require.NoError(t, err)
	require.Len(t, list, 2, "only active templates should be returned")

	// 按 display_order ASC：Gamma(5) → Alpha(20)
	assert.Equal(t, "Gamma", list[0].Name)
	assert.Equal(t, "Alpha", list[1].Name)
}

// TestStore_TemplateGetByID 验证 GetByID 返回正确模板（不过滤 is_active）。
func TestStore_TemplateGetByID(t *testing.T) {
	db := newTestDB(t, &model.SkillTemplate{})
	s := newSkillTemplateStore(db)
	ctx := context.Background()

	tpl := &model.SkillTemplate{Name: "Sales", IsActive: false, DisplayOrder: 1}
	seedSkillTemplate(t, db, tpl)

	got, err := s.GetByID(ctx, tpl.ID)
	require.NoError(t, err)
	assert.Equal(t, "Sales", got.Name)

	// 不存在的 ID 应返回错误
	_, err = s.GetByID(ctx, 99999)
	require.Error(t, err)
}
