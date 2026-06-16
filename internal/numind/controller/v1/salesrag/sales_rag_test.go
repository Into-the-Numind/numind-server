// Package salesrag_test contains HTTP-handler level tests for CheckSalesPermission.
// Uses real customerbiz + in-memory SQLite instead of mocking ICustomerBiz
// (17 methods, too noisy to stub). Tests the full controller → biz → store → DB
// path, with user_feature_permission rows seeded to drive each case.
package salesrag_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"numind-server/internal/numind/biz"
	"numind-server/internal/numind/biz/agent"
	agentatt "numind-server/internal/numind/biz/agent/attachment"
	"numind-server/internal/numind/biz/agent/search"
	"numind-server/internal/numind/biz/ali"
	announcementbiz "numind-server/internal/numind/biz/announcement"
	"numind-server/internal/numind/biz/attachment"
	chatbotbiz "numind-server/internal/numind/biz/chatbot"
	"numind-server/internal/numind/biz/config"
	"numind-server/internal/numind/biz/credit"
	customerbiz "numind-server/internal/numind/biz/customer"
	documentbiz "numind-server/internal/numind/biz/document"
	kbbiz "numind-server/internal/numind/biz/knowledgebase"
	"numind-server/internal/numind/biz/llmrouter"
	meetingbiz "numind-server/internal/numind/biz/meeting"
	"numind-server/internal/numind/biz/memory"
	"numind-server/internal/numind/biz/monitor"
	"numind-server/internal/numind/biz/payment"
	"numind-server/internal/numind/biz/salesrag"
	skillbiz "numind-server/internal/numind/biz/skill"
	sopbiz "numind-server/internal/numind/biz/sop"
	"numind-server/internal/numind/biz/user"
	"numind-server/internal/numind/biz/volc"
	salesragctl "numind-server/internal/numind/controller/v1/salesrag"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/model"
	"numind-server/internal/pkg/pricing"
)

// -----------------------------------------------------------------------------
// test DB
// -----------------------------------------------------------------------------

// newTestDB creates in-memory SQLite with minimal schema: hand-rolled `user`
// table (model.User has MySQL ENUMs SQLite rejects) + AutoMigrated
// UserFeaturePermission.
func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })

	require.NoError(t, db.Exec(`
		CREATE TABLE user (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			parent_user_id INTEGER NULL,
			created_at DATETIME, updated_at DATETIME, deleted_at DATETIME
		)
	`).Error)
	require.NoError(t, db.AutoMigrate(&model.UserFeaturePermission{}))
	// sop-salesrag-parent-scope Task 3: 销售智能体 owner tag 表必须存在,
	// 即使父账户也需要 Layer 0 检查 (spec D2 移除父账户硬 bypass)
	require.NoError(t, db.AutoMigrate(&model.SalesAgentOwner{}))
	return db
}

func seedUser(t *testing.T, db *gorm.DB, id uint, parentID *uint) {
	t.Helper()
	var pid interface{}
	if parentID != nil {
		pid = *parentID
	}
	require.NoError(t, db.Exec(`INSERT INTO user (id, parent_user_id) VALUES (?, ?)`, id, pid).Error)
}

func seedFeatureGrant(t *testing.T, db *gorm.DB, parent, sub uint, key string) {
	t.Helper()
	require.NoError(t, db.Create(&model.UserFeaturePermission{
		ParentUserID: parent, SubUserID: sub, FeatureKey: key,
	}).Error)
}

// seedSalesAgentOwner 把父账户加入销售智能体 owner 表 (sop-salesrag-parent-scope Task 3).
// spec D2: 父账户不再硬 bypass, 必须显式存在于 sales_agent_owner 表才能访问销售智能体.
func seedSalesAgentOwner(t *testing.T, db *gorm.DB, parentID uint) {
	t.Helper()
	require.NoError(t, db.Create(&model.SalesAgentOwner{ParentUserID: parentID}).Error)
}

// -----------------------------------------------------------------------------
// realBizOnlyCustomers is a biz.IBiz implementation that only wires Customers().
// All other methods return nil; since the controller under test only touches
// Customers().CheckFeaturePermission, nil returns are safe (panicking if
// misused is acceptable — it exposes test scope creep).
// -----------------------------------------------------------------------------
type realBizOnlyCustomers struct {
	customers customerbiz.ICustomerBiz
}

func (b *realBizOnlyCustomers) Users() user.UserBiz                            { return nil }
func (b *realBizOnlyCustomers) Ali() ali.AliBiz                                { return nil }
func (b *realBizOnlyCustomers) Volc() volc.VolcBiz                             { return nil }
func (b *realBizOnlyCustomers) Configs() config.ConfigBiz                      { return nil }
func (b *realBizOnlyCustomers) Sop() sopbiz.ISopBiz                            { return nil }
func (b *realBizOnlyCustomers) Customers() customerbiz.ICustomerBiz            { return b.customers }
func (b *realBizOnlyCustomers) Announcement() announcementbiz.IAnnouncementBiz { return nil }
func (b *realBizOnlyCustomers) Document() documentbiz.IDocumentService         { return nil }
func (b *realBizOnlyCustomers) Meeting() meetingbiz.IMeetingBiz                { return nil }
func (b *realBizOnlyCustomers) SalesRAG() salesrag.SalesRAGBiz                 { return nil }
func (b *realBizOnlyCustomers) Credit() credit.ICreditBiz                      { return nil }
func (b *realBizOnlyCustomers) CreditService() credit.ICreditService           { return nil }
func (b *realBizOnlyCustomers) Pricing() pricing.ICalculator                   { return nil }
func (b *realBizOnlyCustomers) Payment() payment.IPaymentBiz                   { return nil }
func (b *realBizOnlyCustomers) Monitor() monitor.IMonitorBiz                   { return nil }
func (b *realBizOnlyCustomers) KnowledgeBase() kbbiz.IKnowledgeBaseBiz         { return nil }
func (b *realBizOnlyCustomers) Chatbot() chatbotbiz.IChatbotBiz                { return nil }
func (b *realBizOnlyCustomers) LLMRouter() *llmrouter.Router                   { return nil }
func (b *realBizOnlyCustomers) Agents() agent.AgentRunner                      { return nil }
func (b *realBizOnlyCustomers) AgentTools() agent.AgentToolRegistry            { return nil }
func (b *realBizOnlyCustomers) Skill() skillbiz.Service                        { return nil }
func (b *realBizOnlyCustomers) StudentQuery() *agent.StudentQueryService {
	return nil
}
func (b *realBizOnlyCustomers) StudentRun() *agent.StudentRunService         { return nil }
func (b *realBizOnlyCustomers) Attachment() *attachment.UploadService        { return nil }
func (b *realBizOnlyCustomers) AttachmentFallback() agentatt.FallbackService { return nil }
func (b *realBizOnlyCustomers) MemoryCadence() *memory.CadenceService        { return nil }
func (b *realBizOnlyCustomers) SearchService() search.Service                { return nil }

// compile-time guard: this test struct must satisfy biz.IBiz or tests fail here.
var _ biz.IBiz = (*realBizOnlyCustomers)(nil)

// -----------------------------------------------------------------------------
// helpers
// -----------------------------------------------------------------------------

func setCurrentUserMW(u *model.User) gin.HandlerFunc {
	return func(c *gin.Context) {
		if u != nil {
			c.Set("current_user", u)
		}
		c.Next()
	}
}

func newRouter(t *testing.T, b biz.IBiz, u *model.User) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(setCurrentUserMW(u))
	ctrl := salesragctl.NewSalesRAGController(b, nil) // creditBiz not touched
	r.GET("/sales-rag/check-permission", ctrl.CheckSalesPermission)
	return r
}

func testBiz(t *testing.T, db *gorm.DB) biz.IBiz {
	t.Helper()
	ds := store.NewTestStore(db)
	return &realBizOnlyCustomers{customers: customerbiz.New(ds)}
}

func mustParent(id uint) *model.User {
	u := &model.User{}
	u.ID = id
	return u
}

func mustSub(id, parentID uint) *model.User {
	u := &model.User{ParentUserID: &parentID}
	u.ID = id
	return u
}

// -----------------------------------------------------------------------------
// tests
// -----------------------------------------------------------------------------

// T1: Parent account (ParentUserID IS NULL) with owner tag → has_permission: true
// 语义升级 (sop-salesrag-parent-scope Task 3 spec D2):
// 父账户**必须**在 sales_agent_owner 表中才能访问销售智能体, 不再硬 bypass.
func TestCheckSalesPermission_Parent_True(t *testing.T) {
	db := newTestDB(t)
	seedUser(t, db, 1, nil)       // parent
	seedSalesAgentOwner(t, db, 1) // 父账户 owner tag (Layer 0 必查)
	r := newRouter(t, testBiz(t, db), mustParent(1))

	req := httptest.NewRequest(http.MethodGet, "/sales-rag/check-permission", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	var resp struct {
		Code int `json:"code"`
		Data struct {
			HasPermission bool `json:"has_permission"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, 0, resp.Code)
	assert.True(t, resp.Data.HasPermission, "parent must auto-pass")
}

// T2: Sub-user WITH sales_agent grant + parent has owner tag → has_permission: true
// 子用户双层 AND (sop-salesrag-parent-scope Task 3 spec D2):
//
//	Layer 0: 父账户在 sales_agent_owner 表 ✓
//	Layer 1: 子用户在 user_feature_permission 有 sales_agent 行 ✓
func TestCheckSalesPermission_SubGranted_True(t *testing.T) {
	db := newTestDB(t)
	parentID := uint(1)
	seedUser(t, db, 1, nil)
	seedUser(t, db, 100, &parentID)
	seedSalesAgentOwner(t, db, 1)                               // Layer 0
	seedFeatureGrant(t, db, 1, 100, model.FeatureKeySalesAgent) // Layer 1

	r := newRouter(t, testBiz(t, db), mustSub(100, 1))

	req := httptest.NewRequest(http.MethodGet, "/sales-rag/check-permission", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp struct {
		Data struct {
			HasPermission bool `json:"has_permission"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.True(t, resp.Data.HasPermission, "granted sub must return true")
}

// T3: Sub-user WITHOUT grant (parent has owner) → has_permission: false, HTTP 200 (NOT 403)
// D1 invariant: check-permission must never return 403.
// 父账户 owner tag 存在确保 Layer 0 通过, 由 Layer 1 缺失子用户 grant 拦截.
func TestCheckSalesPermission_SubDenied_FalseNot403(t *testing.T) {
	db := newTestDB(t)
	parentID := uint(1)
	seedUser(t, db, 1, nil)
	seedUser(t, db, 200, &parentID) // no grant
	seedSalesAgentOwner(t, db, 1)   // Layer 0 通过, Layer 1 是真正的拦截点

	r := newRouter(t, testBiz(t, db), mustSub(200, 1))

	req := httptest.NewRequest(http.MethodGet, "/sales-rag/check-permission", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, "D1: must be 200, not 403")
	var resp struct {
		Code int `json:"code"`
		Data struct {
			HasPermission bool `json:"has_permission"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, 0, resp.Code, "business code 0 (success)")
	assert.False(t, resp.Data.HasPermission, "denied sub must return false")
}

// T4: biz layer returns error → 500 surfaced as non-zero business code
// Simulated by closing the underlying sqlDB before the request.
func TestCheckSalesPermission_BizError_Returns500(t *testing.T) {
	db := newTestDB(t)
	parentID := uint(1)
	seedUser(t, db, 1, nil)
	seedUser(t, db, 300, &parentID)

	b := testBiz(t, db)
	r := newRouter(t, b, mustSub(300, 1))

	// Force the DB to fail by closing it mid-test.
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())

	req := httptest.NewRequest(http.MethodGet, "/sales-rag/check-permission", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	// ErrInternalServer maps to HTTP 500; the biz code is also non-zero.
	require.Equal(t, http.StatusInternalServerError, w.Code, "DB failure must return 500; body: %s", w.Body.String())
	var resp struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.NotEqual(t, 0, resp.Code, "biz err → non-zero business code")
}
