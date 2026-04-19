package admin_sop

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"gorm.io/gorm"

	"numind-server/internal/pkg/model"
)

// TestToAdminRunItem_FullyPopulated 确认 Template 和 User 都非 nil 时
// 嵌套对象正确映射，所有 json 字段是 lowercase。
func TestToAdminRunItem_FullyPopulated(t *testing.T) {
	started := time.Date(2026, 4, 19, 10, 0, 0, 0, time.UTC)
	finished := time.Date(2026, 4, 19, 10, 5, 0, 0, time.UTC)
	created := time.Date(2026, 4, 19, 9, 55, 0, 0, time.UTC)

	run := &model.SopRun{
		Model:        gorm.Model{ID: 42, CreatedAt: created},
		TemplateID:   7,
		UserID:       13,
		Status:       "succeeded",
		StartedAt:    &started,
		FinishedAt:   &finished,
		ErrorMessage: "",
		Template: &model.SopTemplate{
			Model: gorm.Model{ID: 7},
			Name:  "周报模板",
		},
		User: &model.User{
			Model:    gorm.Model{ID: 13},
			Nickname: "测试用户",
		},
	}

	item := toAdminRunItem(run, 1234, 567)

	assert.Equal(t, uint(42), item.ID)
	assert.Equal(t, uint(7), item.TemplateID)
	assert.Equal(t, uint(13), item.UserID)
	assert.Equal(t, "succeeded", item.Status)
	assert.Equal(t, &started, item.StartedAt)
	assert.Equal(t, &finished, item.FinishedAt)
	assert.Equal(t, created, item.CreatedAt)
	assert.Equal(t, int64(1234), item.TotalTokens)
	assert.Equal(t, int64(567), item.CostCents)

	require := assert.New(t)
	require.NotNil(item.Template, "template should be non-nil when preload populates it")
	assert.Equal(t, uint(7), item.Template.ID)
	assert.Equal(t, "周报模板", item.Template.Name)

	require.NotNil(item.User, "user should be non-nil when preload populates it")
	assert.Equal(t, uint(13), item.User.ID)
	assert.Equal(t, "测试用户", item.User.Nickname)
}

// TestToAdminRunItem_NilTemplate 覆盖 soft-delete 或 preload miss 的场景:
// SopRun.Template 为 nil 时，DTO 的 Template 字段也应为 nil（配合 omitempty 省略）。
func TestToAdminRunItem_NilTemplate(t *testing.T) {
	run := &model.SopRun{
		Model:      gorm.Model{ID: 1},
		TemplateID: 7,
		UserID:     13,
		Status:     "running",
		Template:   nil,
		User:       &model.User{Model: gorm.Model{ID: 13}, Nickname: "u"},
	}

	item := toAdminRunItem(run, 0, 0)

	assert.Nil(t, item.Template, "Template 应为 nil 以触发 omitempty 省略")
	assert.NotNil(t, item.User)
}

// TestToAdminRunItem_NilUser 对称场景: User 为 nil。
func TestToAdminRunItem_NilUser(t *testing.T) {
	run := &model.SopRun{
		Model:      gorm.Model{ID: 1},
		TemplateID: 7,
		UserID:     13,
		Status:     "running",
		Template:   &model.SopTemplate{Model: gorm.Model{ID: 7}, Name: "t"},
		User:       nil,
	}

	item := toAdminRunItem(run, 0, 0)

	assert.NotNil(t, item.Template)
	assert.Nil(t, item.User, "User 应为 nil 以触发 omitempty 省略")
}

// TestToAdminNodeRunItem 确认节点执行记录所有字段按 lowercase json tag 映射。
func TestToAdminNodeRunItem(t *testing.T) {
	nr := &model.SopNodeRun{
		Model:     gorm.Model{ID: 100},
		NodeID:    5,
		Status:    "succeeded",
		Input:     "input text",
		Output:    "output text",
		Thinking:  "thinking text",
		LatencyMs: 2500,
		Sort:      3,
	}

	item := toAdminNodeRunItem(nr)

	assert.Equal(t, uint(100), item.ID)
	assert.Equal(t, uint(5), item.NodeID)
	assert.Equal(t, "succeeded", item.Status)
	assert.Equal(t, "input text", item.Input)
	assert.Equal(t, "output text", item.Output)
	assert.Equal(t, "thinking text", item.Thinking)
	assert.Equal(t, int64(2500), item.LatencyMs)
	assert.Equal(t, 3, item.Sort)
}
