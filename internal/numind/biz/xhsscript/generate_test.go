package xhsscript

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/aiservice"
	"numind-server/internal/pkg/aiservice/profile"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/model"
)

func TestBuildGenerationPromptOmitsMetricsAndHotComments(t *testing.T) {
	transcript := "这是一段核心逐字稿，应该作为仿写主体。"
	note := &model.XhsScriptNote{
		Title:           "爆款标题",
		Description:     "爆款描述",
		LikeCount:       9876,
		CollectCount:    5432,
		CommentCount:    321,
		HotComments:     mustJSON([]Comment{{Content: "这条评论不应该进入生成提示词"}}),
		VideoTranscript: &transcript,
	}

	prompt := buildGenerationPrompt("我的产品定位", note)

	assert.Contains(t, prompt, "我的产品定位")
	assert.Contains(t, prompt, "爆款标题")
	assert.Contains(t, prompt, "爆款描述")
	assert.Contains(t, prompt, transcript)
	assert.NotContains(t, prompt, "数据：")
	assert.NotContains(t, prompt, "高赞评论")
	assert.NotContains(t, prompt, "点赞 9876")
	assert.NotContains(t, prompt, "收藏 5432")
	assert.NotContains(t, prompt, "评论 321")
	assert.NotContains(t, prompt, "这条评论不应该进入生成提示词")
}

func TestBuildGenerationPromptUsesInternalDeconstructionWorkflow(t *testing.T) {
	transcript := "第一句先制造反差。第二句抛出痛点。第三句给出一个具体解决动作。"
	note := &model.XhsScriptNote{
		Title:           "一个反常识开场",
		Description:     "用短句和转折把用户留住",
		VideoTranscript: &transcript,
	}

	prompt := buildGenerationPrompt("服务创业者的内容增长产品", note)

	assert.Contains(t, prompt, "内部工作流")
	assert.Contains(t, prompt, "爆款结构拆解")
	assert.Contains(t, prompt, "产品转译映射")
	assert.Contains(t, prompt, "1:1 结构仿写")
	assert.Contains(t, prompt, "2-3 句话")
	assert.Contains(t, prompt, "自然段数量")
	assert.Contains(t, prompt, "按指定格式输出")
	assert.Contains(t, prompt, "不要输出拆解过程")
	assert.NotContains(t, prompt, "[STAGE_3_CACHE]")
	assert.NotContains(t, prompt, "【原文】")
	assert.NotContains(t, prompt, "【爆款因子分析】")
	assert.NotContains(t, prompt, "【1:1仿写】")
}

func TestBuildGenerationPromptRequiresTitleBodyAndTagsOutput(t *testing.T) {
	transcript := "这里是一段参考视频逐字稿。"
	note := &model.XhsScriptNote{
		Title:           "参考标题",
		Description:     "参考描述",
		VideoTranscript: &transcript,
	}

	prompt := buildGenerationPrompt("产品定位", note)

	assert.Contains(t, prompt, "参考描述")
	assert.Contains(t, prompt, "描述：参考描述")
	assert.Contains(t, prompt, "【标题】")
	assert.Contains(t, prompt, "【描述】")
	assert.Contains(t, prompt, "【标签】")
	assert.Contains(t, prompt, "【口播文稿】")
	assert.Contains(t, prompt, "3-8 个小红书标签")
	assert.NotContains(t, prompt, "【正文】")
	assert.NotContains(t, prompt, "只输出完整口播稿正文")
}

func TestBuildGenerationPromptUsesApprovedOutputCopy(t *testing.T) {
	transcript := "这里是一段参考视频逐字稿。"
	note := &model.XhsScriptNote{
		Title:           "参考标题",
		Description:     "参考描述",
		VideoTranscript: &transcript,
	}

	prompt := buildGenerationPrompt("产品定位", note)

	assert.Contains(t, prompt, "不要输出拆解过程、拆解小节标签、Markdown、解释或任何分析")
	assert.Contains(t, prompt, "用 2-4 句话概括核心价值，可以自然引导收藏或评论")
	assert.Contains(t, prompt, "语气自然，短句多，有停顿感，适合小红书视频")
	assert.NotContains(t, prompt, "适用人群和观看理由")
	assert.NotContains(t, prompt, "45-90 秒")
	assert.NotContains(t, prompt, "不要输出拆解过程、小节标签")
}

func TestGenerateScriptUsesProductizedRewriteSystemPrompt(t *testing.T) {
	ctx := context.Background()
	db := newXhsScriptGenerateTestDB(t)
	svc := New(store.NewTestStore(db))
	userID := uint(206)
	note := createGenerateReadyNote(t, db, userID)
	var capturedTaskID string
	var capturedReq aiservice.ChatRequest

	withGenerateTestChat(t, func(_ context.Context, taskID string, req aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
		capturedTaskID = taskID
		capturedReq = req
		return &aiservice.ChatResponse{Content: "这是生成后的口播稿"}, nil
	})

	_, err := svc.GenerateScript(ctx, userID, note.ID)
	require.NoError(t, err)
	require.Len(t, capturedReq.Messages, 2)
	systemPrompt := capturedReq.Messages[0].Content.Text

	assert.Equal(t, profile.XhsNoteAnalyze, capturedTaskID)
	assert.Contains(t, systemPrompt, "爆款结构解剖")
	assert.Contains(t, systemPrompt, "产品转译")
	assert.Contains(t, systemPrompt, "最终只按固定格式输出标题、描述、标签和口播文稿")
	assert.Contains(t, systemPrompt, "不输出分析")
	assert.True(t, capturedReq.Thinking, "xhs script generation should enable thinking mode")
}

func TestGenerateScriptFailuresPersistSafeLastErrorCategories(t *testing.T) {
	t.Run("llm raw error is not persisted", func(t *testing.T) {
		ctx := context.Background()
		db := newXhsScriptGenerateTestDB(t)
		svc := New(store.NewTestStore(db))
		userID := uint(201)
		note := createGenerateReadyNote(t, db, userID)
		rawErr := errors.New("llm provider leaked prompt: 我的完整产品定位和视频转写")

		withGenerateTestChat(t, func(context.Context, string, aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
			return nil, rawErr
		})

		dto, err := svc.GenerateScript(ctx, userID, note.ID)
		require.Error(t, err)
		assert.ErrorIs(t, err, errno.ErrInternalServer)
		assert.NotErrorIs(t, err, rawErr)
		assert.NotContains(t, err.Error(), "llm provider")
		assert.NotContains(t, err.Error(), "完整产品定位")
		assert.NotContains(t, err.Error(), "视频转写")
		assert.Nil(t, dto)

		got := loadGenerateTestNote(t, db, userID, note.ID)
		assert.Equal(t, model.XhsScriptGenerateFailed, got.GenerateStatus)
		assert.Equal(t, "generation_failed", got.LastError)
		assert.NotContains(t, got.LastError, "完整产品定位")
		assert.NotContains(t, got.LastError, "视频转写")
	})

	t.Run("empty llm response uses stable category", func(t *testing.T) {
		ctx := context.Background()
		db := newXhsScriptGenerateTestDB(t)
		svc := New(store.NewTestStore(db))
		userID := uint(202)
		note := createGenerateReadyNote(t, db, userID)

		withGenerateTestChat(t, func(context.Context, string, aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
			return &aiservice.ChatResponse{Content: " \n\t "}, nil
		})

		_, err := svc.GenerateScript(ctx, userID, note.ID)
		require.Error(t, err)
		assert.ErrorIs(t, err, errno.ErrInternalServer)
		assert.NotContains(t, err.Error(), "xhs_script_quota_ledger")
		assert.NotContains(t, err.Error(), "no such table")

		got := loadGenerateTestNote(t, db, userID, note.ID)
		assert.Equal(t, model.XhsScriptGenerateFailed, got.GenerateStatus)
		assert.Equal(t, "generation_empty", got.LastError)
		assert.NotContains(t, got.LastError, "生成结果为空")
	})

	t.Run("quota insufficient uses stable category", func(t *testing.T) {
		ctx := context.Background()
		db := newXhsScriptGenerateTestDB(t)
		svc := New(store.NewTestStore(db))
		userID := uint(203)
		note := createGenerateReadyNote(t, db, userID)
		require.NoError(t, db.Model(&model.XhsScriptQuotaAccount{}).
			Where("user_id = ?", userID).
			Updates(map[string]interface{}{"free_remaining": 0, "paid_remaining": 0}).Error)

		withGenerateTestChat(t, func(context.Context, string, aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
			t.Fatal("chat should not be called when quota is insufficient")
			return nil, nil
		})

		_, err := svc.GenerateScript(ctx, userID, note.ID)
		require.Error(t, err)

		got := loadGenerateTestNote(t, db, userID, note.ID)
		assert.Equal(t, model.XhsScriptGenerateFailed, got.GenerateStatus)
		assert.Equal(t, "quota_insufficient", got.LastError)
	})

	t.Run("commit raw error is not persisted", func(t *testing.T) {
		ctx := context.Background()
		db := newXhsScriptGenerateTestDB(t)
		svc := New(store.NewTestStore(db))
		userID := uint(204)
		note := createGenerateReadyNote(t, db, userID)
		require.NoError(t, db.Migrator().DropTable(&model.XhsScriptQuotaLedger{}))

		withGenerateTestChat(t, func(context.Context, string, aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
			return &aiservice.ChatResponse{Content: "安全口播稿"}, nil
		})

		_, err := svc.GenerateScript(ctx, userID, note.ID)
		require.Error(t, err)

		got := loadGenerateTestNote(t, db, userID, note.ID)
		assert.Equal(t, model.XhsScriptGenerateFailed, got.GenerateStatus)
		assert.Equal(t, "generation_commit_failed", got.LastError)
		assert.NotContains(t, got.LastError, "xhs_script_quota_ledger")
		assert.NotContains(t, got.LastError, "no such table")
	})

	t.Run("quota account failure uses stage category", func(t *testing.T) {
		ctx := context.Background()
		db := newXhsScriptGenerateTestDB(t)
		svc := New(store.NewTestStore(db))
		userID := uint(205)
		note := createGenerateReadyNote(t, db, userID)
		require.NoError(t, db.Migrator().DropTable(&model.XhsScriptQuotaAccount{}))

		withGenerateTestChat(t, func(context.Context, string, aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
			t.Fatal("chat should not be called when quota account load fails")
			return nil, nil
		})

		_, err := svc.GenerateScript(ctx, userID, note.ID)
		require.Error(t, err)
		assert.ErrorIs(t, err, errno.ErrInternalServer)
		assert.NotContains(t, err.Error(), "xhs_script_quota_account")

		got := loadGenerateTestNote(t, db, userID, note.ID)
		assert.Equal(t, model.XhsScriptGenerateFailed, got.GenerateStatus)
		assert.Equal(t, "quota_account_failed", got.LastError)
	})
}

func withGenerateTestChat(t *testing.T, chat func(context.Context, string, aiservice.ChatRequest) (*aiservice.ChatResponse, error)) {
	t.Helper()
	prev := xhsScriptChatFn
	xhsScriptChatFn = chat
	t.Cleanup(func() { xhsScriptChatFn = prev })
}

func newXhsScriptGenerateTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/xhs_script_generate_test.db?_busy_timeout=5000"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&model.XhsScriptUserProfile{},
		&model.XhsScriptQuotaAccount{},
		&model.XhsScriptQuotaLedger{},
		&model.XhsScriptNote{},
		&model.XhsScriptGeneration{},
		&model.XhsScriptAnalyticsEvent{},
	))
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

func createGenerateReadyNote(t *testing.T, db *gorm.DB, userID uint) *model.XhsScriptNote {
	t.Helper()
	transcript := "这是一段可用于生成的安全转写"
	require.NoError(t, db.Create(&model.XhsScriptUserProfile{
		UserID:      userID,
		ProfileText: "产品定位",
	}).Error)
	require.NoError(t, db.Create(&model.XhsScriptQuotaAccount{
		UserID:        userID,
		FreeRemaining: 1,
	}).Error)
	note := &model.XhsScriptNote{
		UserID:           userID,
		SourceNoteID:     "generate-ready",
		NoteType:         model.XhsScriptNoteTypeVideo,
		Title:            "可生成视频",
		VideoURL:         "https://sns-video.xhscdn.com/generate-ready.mp4",
		VideoTranscript:  &transcript,
		TranscribeStatus: model.XhsScriptTranscribeReady,
		GenerateStatus:   model.XhsScriptGenerateReady,
	}
	require.NoError(t, db.Create(note).Error)
	return note
}

func loadGenerateTestNote(t *testing.T, db *gorm.DB, userID uint, noteID uint64) model.XhsScriptNote {
	t.Helper()
	var note model.XhsScriptNote
	require.NoError(t, db.Where("user_id = ? AND id = ?", userID, noteID).First(&note).Error)
	return note
}
