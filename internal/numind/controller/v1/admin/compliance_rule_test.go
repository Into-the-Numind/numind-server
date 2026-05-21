package admin_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"numind-server/internal/numind/biz/compliance"
	adminctl "numind-server/internal/numind/controller/v1/admin"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/model"
)

// ---------------------------------------------------------------------------
// Test DB + router helpers
// ---------------------------------------------------------------------------

func newCRTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.ComplianceRule{}))
	t.Cleanup(func() {
		sqlDB, _ := db.DB()
		if sqlDB != nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

func newCRRouter(t *testing.T) (*gin.Engine, *gorm.DB) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db := newCRTestDB(t)
	s := store.NewTestStore(db)
	cache := compliance.NewTTLCache(10, 5*time.Minute)
	svc := compliance.NewAdminService(s.Compliance(), cache)
	ctrl := adminctl.NewComplianceRuleController(svc)

	r := gin.New()
	v1 := r.Group("/v1/admin")
	v1.GET("/compliance-rules", ctrl.List)
	v1.POST("/compliance-rules", ctrl.Create)
	v1.GET("/compliance-rules/:id", ctrl.Get)
	v1.PATCH("/compliance-rules/:id", ctrl.Patch)
	v1.DELETE("/compliance-rules/:id", ctrl.Delete)
	return r, db
}

// doRequest fires a JSON request and returns the recorder.
func doRequest(r *gin.Engine, method, path string, body interface{}) *httptest.ResponseRecorder {
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// ---------------------------------------------------------------------------
// TestComplianceRule_Create_Happy
// ---------------------------------------------------------------------------

func TestComplianceRule_Create_Happy(t *testing.T) {
	r, _ := newCRRouter(t)

	payload := map[string]interface{}{
		"parent_user_id": 1,
		"rule_type":      model.ComplianceRuleTypeForbidBrand,
		"rule_text":      "CompetitorX",
	}
	w := doRequest(r, http.MethodPost, "/v1/admin/compliance-rules", payload)
	assert.Equal(t, http.StatusOK, w.Code)

	var resp map[string]interface{}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, float64(0), resp["code"])
	data := resp["data"].(map[string]interface{})
	assert.Equal(t, "CompetitorX", data["rule_text"])
	assert.True(t, data["is_active"].(bool))
}

// ---------------------------------------------------------------------------
// TestComplianceRule_Create_MissingRequiredField_422
// ---------------------------------------------------------------------------

func TestComplianceRule_Create_MissingRequiredField_422(t *testing.T) {
	r, _ := newCRRouter(t)

	// Missing parent_user_id.
	payload := map[string]interface{}{
		"rule_type": model.ComplianceRuleTypeForbidBrand,
		"rule_text": "X",
	}
	w := doRequest(r, http.MethodPost, "/v1/admin/compliance-rules", payload)
	// ShouldBindJSON returns 400 via ErrBind.
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// ---------------------------------------------------------------------------
// TestComplianceRule_Get_NotFound_404
// ---------------------------------------------------------------------------

func TestComplianceRule_Get_NotFound_404(t *testing.T) {
	r, _ := newCRRouter(t)

	w := doRequest(r, http.MethodGet, "/v1/admin/compliance-rules/999999", nil)
	assert.Equal(t, http.StatusNotFound, w.Code)
}

// ---------------------------------------------------------------------------
// TestComplianceRule_List_Happy
// ---------------------------------------------------------------------------

func TestComplianceRule_List_Happy(t *testing.T) {
	r, db := newCRRouter(t)

	// Seed two rules directly in DB.
	for _, text := range []string{"RuleA", "RuleB"} {
		require.NoError(t, db.Create(&model.ComplianceRule{
			ParentUserID: 1,
			RuleType:     model.ComplianceRuleTypeForbidPhrase,
			RuleText:     text,
			Priority:     100,
			IsActive:     true,
			CreatedAt:    time.Now(),
			UpdatedAt:    time.Now(),
		}).Error)
	}

	w := doRequest(r, http.MethodGet, "/v1/admin/compliance-rules?parent_user_id=1&page=1&page_size=20", nil)
	assert.Equal(t, http.StatusOK, w.Code)

	var resp map[string]interface{}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, float64(0), resp["code"])
	data := resp["data"].(map[string]interface{})
	assert.Equal(t, float64(2), data["total"])
	list := data["list"].([]interface{})
	assert.Len(t, list, 2)
}

// ---------------------------------------------------------------------------
// TestComplianceRule_Patch_Happy
// ---------------------------------------------------------------------------

func TestComplianceRule_Patch_Happy(t *testing.T) {
	r, db := newCRRouter(t)

	rule := &model.ComplianceRule{
		ParentUserID: 2,
		RuleType:     model.ComplianceRuleTypeForbidBrand,
		RuleText:     "Before",
		Priority:     100,
		IsActive:     true,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}
	require.NoError(t, db.Create(rule).Error)

	newActive := false
	payload := map[string]interface{}{
		"rule_text": "After",
		"is_active": newActive,
	}
	path := "/v1/admin/compliance-rules/" + itoa(rule.ID)
	w := doRequest(r, http.MethodPatch, path, payload)
	assert.Equal(t, http.StatusOK, w.Code)

	var resp map[string]interface{}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	data := resp["data"].(map[string]interface{})
	assert.Equal(t, "After", data["rule_text"])
	assert.False(t, data["is_active"].(bool))
}

// ---------------------------------------------------------------------------
// TestComplianceRule_Delete_Happy
// ---------------------------------------------------------------------------

func TestComplianceRule_Delete_Happy(t *testing.T) {
	r, db := newCRRouter(t)

	rule := &model.ComplianceRule{
		ParentUserID: 3,
		RuleType:     model.ComplianceRuleTypeForbidPhrase,
		RuleText:     "ToDelete",
		Priority:     100,
		IsActive:     true,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}
	require.NoError(t, db.Create(rule).Error)

	path := "/v1/admin/compliance-rules/" + itoa(rule.ID)
	w := doRequest(r, http.MethodDelete, path, nil)
	assert.Equal(t, http.StatusOK, w.Code)

	// Confirm soft-delete: rule still exists but is_active=false.
	var row model.ComplianceRule
	require.NoError(t, db.First(&row, rule.ID).Error)
	assert.False(t, row.IsActive)
}

// ---------------------------------------------------------------------------
// Helper
// ---------------------------------------------------------------------------

func itoa(id uint64) string {
	return json.Number(func() string {
		b, _ := json.Marshal(id)
		return string(b)
	}()).String()
}
