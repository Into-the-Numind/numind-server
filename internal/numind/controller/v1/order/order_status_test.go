package order_test

// order_status_test.go — HTTP handler tests for GET /v1/orders/:id/status.

import (
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

	orderctl "numind-server/internal/numind/controller/v1/order"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/model"
)

// ---------------------------------------------------------------------------
// Infrastructure
// ---------------------------------------------------------------------------

// newOrderStatusTestDB creates an SQLite in-memory DB with users + payment_order tables.
func newOrderStatusTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)

	// model.User.TableName() == "user". Post-T4: legacy_tier columns dropped.
	require.NoError(t, db.Exec(`CREATE TABLE IF NOT EXISTS "user" (
		id             INTEGER PRIMARY KEY AUTOINCREMENT,
		username       TEXT    NOT NULL DEFAULT '',
		parent_user_id INTEGER,
		created_at     DATETIME,
		updated_at     DATETIME,
		deleted_at     DATETIME
	)`).Error)

	require.NoError(t, db.AutoMigrate(&model.Order{}))

	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

// seedUser inserts a user row.
func seedUser(t *testing.T, db *gorm.DB, id uint, parentID *uint) {
	t.Helper()
	if parentID == nil {
		require.NoError(t, db.Exec(`INSERT INTO "user" (id, created_at, updated_at)
			VALUES (?, datetime('now'), datetime('now'))`, id).Error)
	} else {
		require.NoError(t, db.Exec(`INSERT INTO "user" (id, parent_user_id, created_at, updated_at)
			VALUES (?, ?, datetime('now'), datetime('now'))`, id, *parentID).Error)
	}
}

// seedOrder inserts an order row and returns its ID.
func seedOrder(t *testing.T, db *gorm.DB, userID, payerID uint, status string) uint64 {
	t.Helper()
	o := model.Order{
		OrderNo:     "TEST" + time.Now().Format("150405.000000"),
		UserID:      userID,
		PayerID:     payerID,
		ProductType: model.ProductTypeBooster,
		Months:      2, // quantity stored in Months
		Amount:      5980,
		PayChannel:  model.PayChannelWechat,
		PayStatus:   status,
		ExpiredAt:   time.Now().Add(30 * time.Minute),
	}
	require.NoError(t, db.Create(&o).Error)
	return o.ID
}

// setCurrentUser injects a model.User into gin context.
func setCurrentUser(user *model.User) gin.HandlerFunc {
	return func(c *gin.Context) {
		if user != nil {
			c.Set("current_user", user)
		}
		c.Next()
	}
}

// makeOrderUser returns a minimal *model.User with given ID.
func makeOrderUser(id uint) *model.User {
	u := &model.User{}
	u.ID = id
	return u
}

// newOrderStatusRouter mounts GetOrderStatus under the test route.
func newOrderStatusRouter(t *testing.T, ds store.IStore, user *model.User) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(setCurrentUser(user))
	ctrl := orderctl.New(nil, ds) // paymentBiz=nil, only ds used by GetOrderStatus
	r.GET("/v1/orders/:id/status", ctrl.GetOrderStatus)
	return r
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// TestGetOrderStatus_PayerCanView verifies a payer can check their own order status.
func TestGetOrderStatus_PayerCanView(t *testing.T) {
	db := newOrderStatusTestDB(t)
	ds := store.NewTestStore(db)

	payerID := uint(10)
	userID := uint(20)
	seedUser(t, db, payerID, nil)
	pID := payerID
	seedUser(t, db, userID, &pID) // child of payer

	orderID := seedOrder(t, db, userID, payerID, model.OrderStatusPending)

	r := newOrderStatusRouter(t, ds, makeOrderUser(payerID))
	req := httptest.NewRequest(http.MethodGet, "/v1/orders/"+uint64Str(orderID)+"/status", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())

	var env struct {
		Code int                          `json:"code"`
		Data orderctl.OrderStatusResponse `json:"data"`
	}
	require.NoError(t, json.NewDecoder(w.Body).Decode(&env))

	assert.Equal(t, 0, env.Code)
	assert.Equal(t, orderID, env.Data.OrderID)
	assert.Equal(t, model.OrderStatusPending, env.Data.Status)
	assert.Equal(t, int64(5980), env.Data.AmountCents)
	assert.Equal(t, model.ProductTypeBooster, env.Data.ProductType)
	assert.Equal(t, 2, env.Data.Quantity)
}

// TestGetOrderStatus_BeneficiaryCanView verifies the beneficiary (UserID) can view.
func TestGetOrderStatus_BeneficiaryCanView(t *testing.T) {
	db := newOrderStatusTestDB(t)
	ds := store.NewTestStore(db)

	payerID := uint(11)
	userID := uint(21)
	seedUser(t, db, payerID, nil)
	pID := payerID
	seedUser(t, db, userID, &pID)

	orderID := seedOrder(t, db, userID, payerID, model.OrderStatusPaid)

	r := newOrderStatusRouter(t, ds, makeOrderUser(userID)) // beneficiary token
	req := httptest.NewRequest(http.MethodGet, "/v1/orders/"+uint64Str(orderID)+"/status", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())

	var env struct {
		Code int                          `json:"code"`
		Data orderctl.OrderStatusResponse `json:"data"`
	}
	require.NoError(t, json.NewDecoder(w.Body).Decode(&env))

	assert.Equal(t, 0, env.Code)
	assert.Equal(t, model.OrderStatusPaid, env.Data.Status)
}

// TestGetOrderStatus_ParentCanViewChildOrder verifies parent can view child's order status.
func TestGetOrderStatus_ParentCanViewChildOrder(t *testing.T) {
	db := newOrderStatusTestDB(t)
	ds := store.NewTestStore(db)

	parentID := uint(100)
	childID := uint(200)
	seedUser(t, db, parentID, nil)
	pID := parentID
	seedUser(t, db, childID, &pID) // child of parent

	orderID := seedOrder(t, db, childID, parentID, model.OrderStatusPending)

	r := newOrderStatusRouter(t, ds, makeOrderUser(parentID)) // parent token
	req := httptest.NewRequest(http.MethodGet, "/v1/orders/"+uint64Str(orderID)+"/status", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())

	var env struct {
		Code int                          `json:"code"`
		Data orderctl.OrderStatusResponse `json:"data"`
	}
	require.NoError(t, json.NewDecoder(w.Body).Decode(&env))

	assert.Equal(t, 0, env.Code)
	assert.Equal(t, orderID, env.Data.OrderID)
	assert.Equal(t, model.OrderStatusPending, env.Data.Status)
}

// TestGetOrderStatus_UnrelatedUserForbidden verifies a stranger gets 403.
func TestGetOrderStatus_UnrelatedUserForbidden(t *testing.T) {
	db := newOrderStatusTestDB(t)
	ds := store.NewTestStore(db)

	payerID := uint(12)
	userID := uint(22)
	strangerID := uint(99)
	seedUser(t, db, payerID, nil)
	pID := payerID
	seedUser(t, db, userID, &pID)
	seedUser(t, db, strangerID, nil)

	orderID := seedOrder(t, db, userID, payerID, model.OrderStatusPending)

	r := newOrderStatusRouter(t, ds, makeOrderUser(strangerID))
	req := httptest.NewRequest(http.MethodGet, "/v1/orders/"+uint64Str(orderID)+"/status", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusForbidden, w.Code, "body: %s", w.Body.String())
}

// TestGetOrderStatus_Unauthenticated returns 401.
func TestGetOrderStatus_Unauthenticated(t *testing.T) {
	db := newOrderStatusTestDB(t)
	ds := store.NewTestStore(db)
	seedUser(t, db, 1, nil)
	orderID := seedOrder(t, db, 1, 1, model.OrderStatusPending)

	r := newOrderStatusRouter(t, ds, nil) // no user
	req := httptest.NewRequest(http.MethodGet, "/v1/orders/"+uint64Str(orderID)+"/status", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

// TestGetOrderStatus_InvalidID returns 400.
func TestGetOrderStatus_InvalidID(t *testing.T) {
	db := newOrderStatusTestDB(t)
	ds := store.NewTestStore(db)
	r := newOrderStatusRouter(t, ds, makeOrderUser(1))

	req := httptest.NewRequest(http.MethodGet, "/v1/orders/notanumber/status", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// TestGetOrderStatus_NotFound returns 404 for unknown order.
func TestGetOrderStatus_NotFound(t *testing.T) {
	db := newOrderStatusTestDB(t)
	ds := store.NewTestStore(db)
	r := newOrderStatusRouter(t, ds, makeOrderUser(1))

	req := httptest.NewRequest(http.MethodGet, "/v1/orders/99999/status", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func uint64Str(n uint64) string {
	if n == 0 {
		return "0"
	}
	b := make([]byte, 0, 20)
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
