package credit_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/model"
)

// 注：本文件在 package credit_test，经 svc(ICreditService) + 既有 helper 间接用被测包，
// 不直接 import biz/credit（无 credit.X 裸引用，避免 unused import 编译错误）。

func i64p(v int64) *int64 { return &v }

// 直接 seed credit_reservation 行，验证 biz 映射 / 过滤 / 分页归一化。
func TestListConsumptionLog_MappingFilterPaging(t *testing.T) {
	ctx := context.Background()
	db := newCreditReserveTestDB(t)
	ds := store.NewTestStore(db)
	svc := newCreditServiceWithMembership(ds, db, nil)

	base := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)
	seed := []model.CreditReservation{
		{UserID: 7, Operation: "sop_run", Status: "reconciled", ReservedCredits: 20, Delta: i64p(-2), ActualCostCents: i64p(18), CreatedAt: base},
		{UserID: 7, Operation: "weird_new_op", Status: "reconciled", ActualCostCents: i64p(5), CreatedAt: base.Add(time.Hour)},
		{UserID: 7, Operation: "sop_run", Status: "reserved", ActualCostCents: nil, CreatedAt: base.Add(2 * time.Hour)},
	}
	for i := range seed {
		require.NoError(t, db.Create(&seed[i]).Error)
	}

	items, total, err := svc.ListConsumptionLog(ctx, 7, 1, 20)
	require.NoError(t, err)
	assert.Equal(t, int64(2), total)
	require.Len(t, items, 2)
	// created_at DESC：weird_new_op 在前（未知 operation 回退裸值）
	assert.Equal(t, "weird_new_op", items[0].Action)
	assert.Equal(t, "weird_new_op", items[0].ActionLabel)
	assert.Equal(t, int64(5), items[0].Credits)
	// sop_run → 中文名，credits = actual_cost_cents
	assert.Equal(t, "sop_run", items[1].Action)
	assert.Equal(t, "SOP 执行", items[1].ActionLabel)
	assert.Equal(t, int64(18), items[1].Credits)

	// 分页归一化：page=0 / pageSize=0 → 视为 1 / 20，不报错
	_, _, err = svc.ListConsumptionLog(ctx, 7, 0, 0)
	require.NoError(t, err)
	// pageSize 上限 100：传 9999 不应报错
	_, _, err = svc.ListConsumptionLog(ctx, 7, 1, 9999)
	require.NoError(t, err)
}

// seedDetailNameDB extends newCreditReserveTestDB with the entity lookup tables
// (sop_template, sop_node, sop_run, sales_session, chatbot_session, chatbot_config).
// These tables have MySQL-specific column types (ENUMs, etc.) so we hand-roll
// minimal SQLite-compatible DDL that only includes the columns queried by
// enrichDetailNames (id, name/title, template_id, chatbot_id, user_id).
func seedDetailNameDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := newCreditReserveTestDB(t)

	ddl := []string{
		`CREATE TABLE IF NOT EXISTS sop_template (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			name       TEXT NOT NULL,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			deleted_at DATETIME
		)`,
		`CREATE TABLE IF NOT EXISTS sop_node (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			template_id INTEGER NOT NULL,
			name        TEXT NOT NULL,
			created_at  DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at  DATETIME DEFAULT CURRENT_TIMESTAMP,
			deleted_at  DATETIME
		)`,
		`CREATE TABLE IF NOT EXISTS sop_run (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			template_id INTEGER NOT NULL,
			user_id     INTEGER NOT NULL,
			created_at  DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at  DATETIME DEFAULT CURRENT_TIMESTAMP,
			deleted_at  DATETIME
		)`,
		`CREATE TABLE IF NOT EXISTS sales_session (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id    INTEGER NOT NULL,
			title      TEXT NOT NULL DEFAULT '',
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			deleted_at DATETIME
		)`,
		`CREATE TABLE IF NOT EXISTS chatbot_session (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id    INTEGER NOT NULL,
			chatbot_id INTEGER NOT NULL,
			title      TEXT NOT NULL DEFAULT '',
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			deleted_at DATETIME
		)`,
		`CREATE TABLE IF NOT EXISTS chatbot_config (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id    INTEGER NOT NULL,
			name       TEXT NOT NULL,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			deleted_at DATETIME
		)`,
	}
	for _, stmt := range ddl {
		require.NoError(t, db.Exec(stmt).Error, "exec detail-name DDL")
	}
	return db
}

// TestListConsumptionLog_DetailName verifies that each log item's DetailName
// is correctly resolved from credit_reservation.reference_id:
//   - sop_run:<runID>:<nodeID>   → "<template.Name> · <node.Name>"
//   - sop_chat:<runID>           → "<template.Name>"
//   - sales_session:<id>         → session.Title (scoped by user_id)
//   - chatbot_session:<id>       → chatbot config Name
//   - empty / unknown ref        → ""
//   - cross-user sales_session   → "" (越权防护)
func TestListConsumptionLog_DetailName(t *testing.T) {
	ctx := context.Background()
	db := seedDetailNameDB(t)
	ds := store.NewTestStore(db)
	svc := newCreditServiceWithMembership(ds, db, nil)

	const currentUser = uint(600)
	const otherUser = uint(601)

	// insertRow executes an INSERT and returns the last-insert-id as uint.
	insertRow := func(sql string, args ...interface{}) uint {
		t.Helper()
		require.NoError(t, db.Exec(sql, args...).Error)
		var lastID uint
		require.NoError(t, db.Raw("SELECT last_insert_rowid()").Scan(&lastID).Error)
		return lastID
	}

	// ---- seed entity rows ----
	tplAID := insertRow("INSERT INTO sop_template (name) VALUES (?)", "模板A")
	tplBID := insertRow("INSERT INTO sop_template (name) VALUES (?)", "模板B")

	nodeXID := insertRow("INSERT INTO sop_node (template_id, name) VALUES (?, ?)", tplAID, "节点X")
	nodeYID := insertRow("INSERT INTO sop_node (template_id, name) VALUES (?, ?)", tplBID, "节点Y")

	// Two sop_run rows (tests batch: different templates)
	runAID := insertRow("INSERT INTO sop_run (template_id, user_id) VALUES (?, ?)", tplAID, currentUser)
	runBID := insertRow("INSERT INTO sop_run (template_id, user_id) VALUES (?, ?)", tplBID, currentUser)

	// sales_session owned by currentUser
	saleOwnID := insertRow("INSERT INTO sales_session (user_id, title) VALUES (?, ?)", currentUser, "客户ABC会话")

	// sales_session owned by DIFFERENT user (越权 test)
	saleOtherID := insertRow("INSERT INTO sales_session (user_id, title) VALUES (?, ?)", otherUser, "他人会话")

	// chatbot_config + chatbot_session
	cbCfgID := insertRow("INSERT INTO chatbot_config (user_id, name) VALUES (?, ?)", currentUser, "智能助手Omega")
	cbSessID := insertRow("INSERT INTO chatbot_session (user_id, chatbot_id, title) VALUES (?, ?, ?)", currentUser, cbCfgID, "某session")

	// ---- seed credit_reservation rows ----
	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	type resRow struct {
		op  string
		ref string
		at  time.Duration
	}
	rows := []resRow{
		// sop_run → 模板A · 节点X
		{"sop_run", fmt.Sprintf("sop_run:%v:%v", runAID, nodeXID), 0},
		// sop_run with different template → 模板B · 节点Y (batch test)
		{"sop_run", fmt.Sprintf("sop_run:%v:%v", runBID, nodeYID), time.Minute},
		// sop_chat → 模板A
		{"sop_chat", fmt.Sprintf("sop_chat:%v", runAID), 2 * time.Minute},
		// sop_chat legacy format (sop_chat:<run>:<seq>) — should still resolve
		{"sop_chat", fmt.Sprintf("sop_chat:%v:42", runBID), 3 * time.Minute},
		// sales_session owned by currentUser
		{"salesrag_chat", fmt.Sprintf("sales_session:%v", saleOwnID), 4 * time.Minute},
		// sales_session owned by OTHER user (越权)
		{"salesrag_chat", fmt.Sprintf("sales_session:%v", saleOtherID), 5 * time.Minute},
		// chatbot_session
		{"chatbot_chat", fmt.Sprintf("chatbot_session:%v", cbSessID), 6 * time.Minute},
		// empty reference_id
		{"sop_run", "", 7 * time.Minute},
		// unknown prefix
		{"sop_run", "unknown:999", 8 * time.Minute},
	}
	for _, r := range rows {
		rsvRow := model.CreditReservation{
			UserID:          currentUser,
			Operation:       r.op,
			ReferenceType:   r.op,
			ReferenceID:     r.ref,
			Status:          "reconciled",
			ReservedCredits: 10,
			ActualCostCents: i64p(10),
			CreatedAt:       base.Add(r.at),
		}
		require.NoError(t, db.Create(&rsvRow).Error)
	}

	items, total, err := svc.ListConsumptionLog(ctx, currentUser, 1, 100)
	require.NoError(t, err)
	require.Equal(t, int64(len(rows)), total)
	require.Len(t, items, len(rows))

	// Build a map[referenceID]DetailName for assertions
	// (order from DB is created_at DESC — assert by reference_id via a helper)
	type wantCase struct {
		ref        string
		detailName string
	}
	wants := []wantCase{
		{fmt.Sprintf("sop_run:%v:%v", runAID, nodeXID), "模板A · 节点X"},
		{fmt.Sprintf("sop_run:%v:%v", runBID, nodeYID), "模板B · 节点Y"},
		{fmt.Sprintf("sop_chat:%v", runAID), "模板A"},
		{fmt.Sprintf("sop_chat:%v:42", runBID), "模板B"},
		{fmt.Sprintf("sales_session:%v", saleOwnID), "客户ABC会话"},
		{fmt.Sprintf("sales_session:%v", saleOtherID), ""}, // 越权 → 不解析
		{fmt.Sprintf("chatbot_session:%v", cbSessID), "智能助手Omega"},
		{"", ""},
		{"unknown:999", ""},
	}

	// Build lookup: referenceID → DetailName by fetching DB rows in same
	// created_at DESC order as the log and zipping with returned items.
	var dbRows []model.CreditReservation
	require.NoError(t, db.Where("user_id = ? AND status = 'reconciled' AND actual_cost_cents > 0", currentUser).
		Order("created_at DESC").Find(&dbRows).Error)
	require.Len(t, dbRows, len(items))

	refToDetail := make(map[string]string, len(items))
	for i := range items {
		refToDetail[dbRows[i].ReferenceID] = items[i].DetailName
	}

	for _, w := range wants {
		actual := refToDetail[w.ref]
		assert.Equal(t, w.detailName, actual,
			"DetailName mismatch for ref=%q", w.ref)
	}
}

// TestListConsumptionLog_DetailName_NodeNoTemplate verifies the UX fallback:
// when a sop_run reference_id names a node that exists but whose template is
// absent (never seeded / soft-deleted), DetailName must equal the node's own
// Name rather than the empty string that would discard the known step name.
func TestListConsumptionLog_DetailName_NodeNoTemplate(t *testing.T) {
	ctx := context.Background()
	db := seedDetailNameDB(t)
	ds := store.NewTestStore(db)
	svc := newCreditServiceWithMembership(ds, db, nil)

	const currentUser = uint(700)

	insertRow := func(sql string, args ...interface{}) uint {
		t.Helper()
		require.NoError(t, db.Exec(sql, args...).Error)
		var lastID uint
		require.NoError(t, db.Raw("SELECT last_insert_rowid()").Scan(&lastID).Error)
		return lastID
	}

	// Seed a node whose template_id (999) has NO corresponding sop_template row.
	const missingTplID = uint(999)
	orphanNodeID := insertRow("INSERT INTO sop_node (template_id, name) VALUES (?, ?)", missingTplID, "孤儿节点Z")

	// Seed a sop_run also pointing at the missing template.
	orphanRunID := insertRow("INSERT INTO sop_run (template_id, user_id) VALUES (?, ?)", missingTplID, currentUser)

	rsvRow := model.CreditReservation{
		UserID:          currentUser,
		Operation:       "sop_run",
		ReferenceType:   "sop_run",
		ReferenceID:     fmt.Sprintf("sop_run:%v:%v", orphanRunID, orphanNodeID),
		Status:          "reconciled",
		ReservedCredits: 5,
		ActualCostCents: i64p(5),
		CreatedAt:       time.Date(2026, 6, 1, 14, 0, 0, 0, time.UTC),
	}
	require.NoError(t, db.Create(&rsvRow).Error)

	items, total, err := svc.ListConsumptionLog(ctx, currentUser, 1, 20)
	require.NoError(t, err)
	require.Equal(t, int64(1), total)
	require.Len(t, items, 1)
	// Template is absent — fall back to node.Name (step name) rather than "".
	assert.Equal(t, "孤儿节点Z", items[0].DetailName,
		"should fall back to node.Name when template is missing")
}

// 跑真实 Reserve→Reconcile，断言展示 credits == actual_cost_cents == 账本净扣减绝对值。
func TestListConsumptionLog_LedgerTruth(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	svc, ds, rsv := setupReservation(t, 505, 120, []seedPackage{
		{Type: model.CreditTypeSubscription, TotalCredits: 1000, RemainCredits: 1000,
			ActivatedAt: now, ExpiresAt: now.Add(24 * time.Hour)},
	})
	require.NoError(t, svc.Reconcile(ctx, rsv.ID, 95)) // actual 95 < reserved 120 → 退 25；净 95

	items, total, err := svc.ListConsumptionLog(ctx, 505, 1, 20)
	require.NoError(t, err)
	require.Equal(t, int64(1), total)
	require.Len(t, items, 1)
	assert.Equal(t, int64(95), items[0].Credits, "展示额 = actual_cost_cents (= reserved+delta)")

	var ledgerSum int64
	require.NoError(t, ds.DB().Model(&model.CreditTransaction{}).
		Where("user_id = ?", 505).
		Select("COALESCE(SUM(amount),0)").Scan(&ledgerSum).Error)
	assert.Equal(t, items[0].Credits, -ledgerSum, "展示额必须等于账本真实净扣减")
}
