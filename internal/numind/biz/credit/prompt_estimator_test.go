package credit_test

import (
	"context"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"numind-server/internal/numind/biz/credit"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/model"
)

// newPromptEstimatorTestDB sets up sop tables needed by the estimator.
func newPromptEstimatorTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })

	require.NoError(t, db.AutoMigrate(
		&model.SopTemplate{},
		&model.SopNode{},
	))

	// SopRun's gorm.Model + foreign keys work, but its `User *User` relation
	// pulls in User which has MySQL ENUM types that SQLite can't parse. Same
	// for SalesSession. Hand-roll the tables we actually query so AutoMigrate
	// doesn't recurse into User.
	require.NoError(t, db.Exec(`
CREATE TABLE IF NOT EXISTS sop_run (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  created_at DATETIME, updated_at DATETIME, deleted_at DATETIME,
  template_id INTEGER NOT NULL,
  user_id INTEGER NOT NULL,
  status TEXT DEFAULT 'pending',
  conversation_id TEXT,
  final_note_id INTEGER,
  counted INTEGER DEFAULT 0,
  started_at DATETIME,
  finished_at DATETIME,
  error_message TEXT
);`).Error)

	require.NoError(t, db.Exec(`
CREATE TABLE IF NOT EXISTS sop_chat_message (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  created_at DATETIME, updated_at DATETIME, deleted_at DATETIME,
  run_id INTEGER, conversation_id TEXT, user_id INTEGER,
  role TEXT, content TEXT, thinking TEXT,
  model_name TEXT, duration_ms INTEGER DEFAULT 0,
  seq INTEGER DEFAULT 0,
  prompt_tokens INTEGER DEFAULT 0,
  completion_tokens INTEGER DEFAULT 0,
  total_tokens INTEGER DEFAULT 0,
  reasoning_tokens INTEGER DEFAULT 0,
  estimated_prompt_tokens INTEGER DEFAULT 0
);`).Error)

	require.NoError(t, db.Exec(`
CREATE TABLE IF NOT EXISTS sales_session (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  created_at DATETIME, updated_at DATETIME, deleted_at DATETIME,
  user_id INTEGER, title TEXT, status TEXT,
  message_count INTEGER DEFAULT 0,
  sales_stage TEXT, document_ids TEXT, product_doc_ids TEXT,
  case_doc_ids TEXT, faq_doc_ids TEXT, opinion_doc_ids TEXT,
  opinion_track_ids TEXT, deep_thinking INTEGER DEFAULT 0,
  customer_profile TEXT, last_query TEXT,
  is_pinned INTEGER DEFAULT 0, pinned_at DATETIME
);`).Error)

	require.NoError(t, db.Exec(`
CREATE TABLE IF NOT EXISTS sales_message (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  created_at DATETIME, updated_at DATETIME, deleted_at DATETIME,
  session_id INTEGER, user_id INTEGER,
  role TEXT, content TEXT, status TEXT DEFAULT 'sent',
  verdict TEXT, thinking TEXT, images TEXT, trace_id TEXT
);`).Error)
	return db
}

// --- Task C.7: IPromptEstimator ---

func TestPromptEstimator_SopRun_SumsAllNodes(t *testing.T) {
	db := newPromptEstimatorTestDB(t)
	ds := store.NewTestStore(db)
	est := credit.NewPromptEstimator(ds)

	tmpl := &model.SopTemplate{Name: "t", Prompt: "pre-prompt", Description: "desc"}
	require.NoError(t, db.Create(tmpl).Error)
	nodes := []model.SopNode{
		{TemplateID: tmpl.ID, Name: "n1", Prompt: "hello world", Description: "d1", ModelName: "qwen-turbo"},
		{TemplateID: tmpl.ID, Name: "n2", Prompt: "你好", Description: "d2", ModelName: "qwen-turbo"},
	}
	for i := range nodes {
		require.NoError(t, db.Create(&nodes[i]).Error)
	}

	chars, modelName, provider, err := est.Estimate(context.Background(), "sop_run",
		strconv.FormatUint(uint64(tmpl.ID), 10))
	require.NoError(t, err)
	// Expected char count:
	//   template.Prompt(10) + template.Description(4)
	// + node1: prompt(11) + desc(2) + name(2) = 15
	// + node2: prompt(2) + desc(2) + name(2) = 6
	// = 10 + 4 + 15 + 6 = 35
	assert.Equal(t, 35, chars)
	assert.Equal(t, "qwen-turbo", modelName)
	assert.Equal(t, "ali", provider)
}

func TestPromptEstimator_SopRun_TemplateNotFound(t *testing.T) {
	db := newPromptEstimatorTestDB(t)
	ds := store.NewTestStore(db)
	est := credit.NewPromptEstimator(ds)

	_, _, _, err := est.Estimate(context.Background(), "sop_run", "99999")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestPromptEstimator_SopRun_InvalidReference(t *testing.T) {
	db := newPromptEstimatorTestDB(t)
	ds := store.NewTestStore(db)
	est := credit.NewPromptEstimator(ds)

	_, _, _, err := est.Estimate(context.Background(), "sop_run", "not-a-number")
	require.Error(t, err)
}

func TestPromptEstimator_SalesragChat_SumsRecentMessages(t *testing.T) {
	db := newPromptEstimatorTestDB(t)
	ds := store.NewTestStore(db)
	est := credit.NewPromptEstimator(ds)

	sess := &model.SalesSession{UserID: 1, Title: "x", Status: "active"}
	require.NoError(t, db.Create(sess).Error)
	msgs := []model.SalesMessage{
		{SessionID: sess.ID, UserID: 1, Role: "user", Content: "你好吗"}, // 3
		{SessionID: sess.ID, UserID: 1, Role: "assistant", Content: "Hi!"}, // 3
	}
	for i := range msgs {
		require.NoError(t, db.Create(&msgs[i]).Error)
	}

	chars, _, _, err := est.Estimate(context.Background(), "salesrag_chat",
		strconv.FormatUint(uint64(sess.ID), 10))
	require.NoError(t, err)
	assert.Equal(t, 6, chars, "sum of 3+3 rune counts")
}

func TestPromptEstimator_SopChat_EmptyFallsBackToTemplate(t *testing.T) {
	db := newPromptEstimatorTestDB(t)
	ds := store.NewTestStore(db)
	est := credit.NewPromptEstimator(ds)

	tmpl := &model.SopTemplate{Name: "t", Prompt: "hi"} // 2 chars
	require.NoError(t, db.Create(tmpl).Error)
	nodes := []model.SopNode{
		{TemplateID: tmpl.ID, Name: "n", Prompt: "abc", Description: "", ModelName: "qwen-plus"},
	}
	require.NoError(t, db.Create(&nodes[0]).Error)

	run := &model.SopRun{TemplateID: tmpl.ID, UserID: 1, Status: "pending"}
	require.NoError(t, db.Create(run).Error)
	// No SopChatMsg rows — fallback to template path.

	chars, modelName, provider, err := est.Estimate(context.Background(), "sop_chat",
		strconv.FormatUint(uint64(run.ID), 10))
	require.NoError(t, err)
	// template.Prompt(2) + node.Prompt(3) + node.Name(1) = 6
	assert.Equal(t, 6, chars)
	assert.Equal(t, "qwen-plus", modelName)
	assert.Equal(t, "ali", provider)
}

func TestPromptEstimator_UnknownOperation(t *testing.T) {
	db := newPromptEstimatorTestDB(t)
	ds := store.NewTestStore(db)
	est := credit.NewPromptEstimator(ds)
	_, _, _, err := est.Estimate(context.Background(), "nonsense_op", "1")
	require.Error(t, err)
}

func TestPromptEstimator_NoopOperations(t *testing.T) {
	db := newPromptEstimatorTestDB(t)
	ds := store.NewTestStore(db)
	est := credit.NewPromptEstimator(ds)
	for _, op := range []string{"profile_analysis", "file_parse", "style_analysis", "ocr"} {
		chars, m, p, err := est.Estimate(context.Background(), op, "1")
		require.NoError(t, err, "op %s should no-op", op)
		assert.Zero(t, chars)
		assert.Empty(t, m)
		assert.Empty(t, p)
	}
}
