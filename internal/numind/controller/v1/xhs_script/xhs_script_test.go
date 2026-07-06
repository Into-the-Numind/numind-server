package xhs_script

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	xhsscriptbiz "numind-server/internal/numind/biz/xhsscript"
	"numind-server/internal/numind/store"
	importMw "numind-server/internal/pkg/middleware"
	"numind-server/internal/pkg/model"
)

type apiResponse struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

func TestXhsScriptCompatibilityRoutes(t *testing.T) {
	router, db, token, userID := newXhsScriptControllerTestRouter(t)
	otherUserID := userID + 1

	require.NoError(t, db.Create(&model.XhsScriptQuotaAccount{
		UserID:        userID,
		FreeRemaining: 2,
		PaidRemaining: 5,
	}).Error)
	require.NoError(t, db.Create(&model.XhsScriptQuotaLedger{
		UserID:  userID,
		Delta:   -1,
		Bucket:  model.XhsScriptQuotaBucketFree,
		Reason:  model.XhsScriptLedgerReasonGeneration,
		RefType: model.XhsScriptLedgerRefTypeGeneration,
		RefID:   "generation_1",
	}).Error)
	require.NoError(t, db.Create(&model.XhsScriptNote{
		UserID:           userID,
		SourceNoteID:     "own-video",
		NoteType:         model.XhsScriptNoteTypeVideo,
		Title:            "自己的视频",
		VideoURL:         "https://sns-video.xhscdn.com/own.mp4",
		TranscribeStatus: model.XhsScriptTranscribePending,
		GenerateStatus:   model.XhsScriptGenerateNotReady,
	}).Error)
	require.NoError(t, db.Create(&model.XhsScriptNote{
		UserID:           otherUserID,
		SourceNoteID:     "other-video",
		NoteType:         model.XhsScriptNoteTypeVideo,
		Title:            "别人的视频",
		VideoURL:         "https://sns-video.xhscdn.com/other.mp4",
		TranscribeStatus: model.XhsScriptTranscribePending,
		GenerateStatus:   model.XhsScriptGenerateNotReady,
	}).Error)

	listResp := doXhsScriptRequest(router, http.MethodGet, "/v1/xhs-script/notes?limit=1000&offset=0", token)
	require.Equal(t, http.StatusOK, listResp.Code)
	var listBody apiResponse
	require.NoError(t, json.Unmarshal(listResp.Body.Bytes(), &listBody))
	require.Equal(t, 0, listBody.Code)
	var notes []xhsscriptbiz.NoteDTO
	require.NoError(t, json.Unmarshal(listBody.Data, &notes))
	require.Len(t, notes, 1)
	assert.Equal(t, "own-video", notes[0].SourceNoteID)
	assert.Equal(t, "waiting_transcript", notes[0].State)

	quotaResp := doXhsScriptRequest(router, http.MethodGet, "/v1/xhs-script/quota", token)
	require.Equal(t, http.StatusOK, quotaResp.Code)
	var quotaBody apiResponse
	require.NoError(t, json.Unmarshal(quotaResp.Body.Bytes(), &quotaBody))
	require.Equal(t, 0, quotaBody.Code)
	var quota xhsscriptbiz.QuotaDTO
	require.NoError(t, json.Unmarshal(quotaBody.Data, &quota))
	assert.EqualValues(t, 2, quota.FreeRemaining)
	assert.EqualValues(t, 5, quota.PaidRemaining)
	assert.EqualValues(t, 7, quota.Remaining)
	assert.EqualValues(t, 8, quota.Total)

	unauthQuotaResp := doXhsScriptRequest(router, http.MethodGet, "/v1/xhs-script/quota", "")
	assert.NotEqual(t, http.StatusOK, unauthQuotaResp.Code)

	extTokenResp := doXhsScriptRequest(router, http.MethodPost, "/v1/xhs-script/ext-token", token)
	require.Equal(t, http.StatusOK, extTokenResp.Code)
	var extBody apiResponse
	require.NoError(t, json.Unmarshal(extTokenResp.Body.Bytes(), &extBody))
	require.Equal(t, 0, extBody.Code)
	var extData struct {
		Token string `json:"token"`
	}
	require.NoError(t, json.Unmarshal(extBody.Data, &extData))
	assert.NotEmpty(t, extData.Token)

	badPaginationResp := doXhsScriptRequest(router, http.MethodGet, "/v1/xhs-script/notes?offset=-1", token)
	assert.NotEqual(t, http.StatusOK, badPaginationResp.Code)
}

func TestXhsScriptAdminMetricsAliasRejectsNonAdmin(t *testing.T) {
	router, _, token, _ := newXhsScriptControllerTestRouter(t)

	resp := doXhsScriptRequest(router, http.MethodGet, "/v1/xhs-script/admin/metrics", token)

	assert.NotEqual(t, http.StatusOK, resp.Code)
	var body apiResponse
	require.NoError(t, json.Unmarshal(resp.Body.Bytes(), &body))
	assert.Equal(t, 1, body.Code)
}

func TestXhsScriptProfileExtractText(t *testing.T) {
	router, _, token, userID := newXhsScriptControllerTestRouter(t)
	const path = "/v1/xhs-script/profile/extract-text"
	const profileText = "产品定位：给创业者做短视频增长顾问"

	t.Run("extracts text with product token", func(t *testing.T) {
		resp := doXhsScriptMultipartRequest(t, router, path, token, "intro.txt", []byte(profileText))

		require.Equal(t, http.StatusOK, resp.Code)
		var body apiResponse
		require.NoError(t, json.Unmarshal(resp.Body.Bytes(), &body))
		require.Equal(t, 0, body.Code)
		var data struct {
			Text       string `json:"text"`
			TextLength int    `json:"text_length"`
		}
		require.NoError(t, json.Unmarshal(body.Data, &data))
		assert.Contains(t, data.Text, profileText)
		assert.Greater(t, data.TextLength, 0)
	})

	t.Run("rejects missing token", func(t *testing.T) {
		resp := doXhsScriptMultipartRequest(t, router, path, "", "intro.txt", []byte(profileText))

		assert.NotEqual(t, http.StatusOK, resp.Code)
	})

	t.Run("rejects extension token", func(t *testing.T) {
		extToken := signControllerTestTokenWithScope(t, userID, xhsscriptbiz.ExtTokenScope)
		resp := doXhsScriptMultipartRequest(t, router, path, extToken, "intro.txt", []byte(profileText))

		assert.NotEqual(t, http.StatusOK, resp.Code)
	})

	t.Run("rejects missing file", func(t *testing.T) {
		body := &bytes.Buffer{}
		writer := multipart.NewWriter(body)
		require.NoError(t, writer.Close())
		req := httptest.NewRequest(http.MethodPost, path, body)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", writer.FormDataContentType())
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		assert.NotEqual(t, http.StatusOK, w.Code)
	})

	t.Run("rejects empty file", func(t *testing.T) {
		resp := doXhsScriptMultipartRequest(t, router, path, token, "intro.txt", nil)

		assert.NotEqual(t, http.StatusOK, resp.Code)
	})

	t.Run("rejects unsupported extension before parsing", func(t *testing.T) {
		resp := doXhsScriptMultipartRequest(t, router, path, token, "intro.html", []byte(profileText))

		assert.NotEqual(t, http.StatusOK, resp.Code)
		var body apiResponse
		require.NoError(t, json.Unmarshal(resp.Body.Bytes(), &body))
		assert.Contains(t, body.Message, "仅支持")
		assert.NotContains(t, body.Message, "intro.html")
	})

	t.Run("rejects oversized file", func(t *testing.T) {
		resp := doXhsScriptMultipartRequest(t, router, path, token, "intro.txt", bytes.Repeat([]byte("x"), xhsScriptProfileExtractMaxFileSize+1))

		assert.NotEqual(t, http.StatusOK, resp.Code)
		var body apiResponse
		require.NoError(t, json.Unmarshal(resp.Body.Bytes(), &body))
		assert.Contains(t, body.Message, "文件大小超过限制")
	})

	t.Run("rejects oversized request body before multipart parsing", func(t *testing.T) {
		resp := doXhsScriptMultipartRequest(t, router, path, token, "intro.txt", bytes.Repeat([]byte("x"), xhsScriptProfileExtractMaxBodySize+1))

		assert.NotEqual(t, http.StatusOK, resp.Code)
		var body apiResponse
		require.NoError(t, json.Unmarshal(resp.Body.Bytes(), &body))
		assert.Contains(t, body.Message, "文件大小超过限制")
	})

	t.Run("sanitizes utf8 and truncates text", func(t *testing.T) {
		raw := append([]byte("产品\xef\xbf\xbd"), []byte(strings.Repeat("好", xhsScriptProfileExtractMaxTextSize/3+10))...)
		raw = append(raw, 0xff, 0xfe)
		resp := doXhsScriptMultipartRequest(t, router, path, token, "intro.txt", raw)

		require.Equal(t, http.StatusOK, resp.Code)
		var body apiResponse
		require.NoError(t, json.Unmarshal(resp.Body.Bytes(), &body))
		require.Equal(t, 0, body.Code)
		var data struct {
			Text       string `json:"text"`
			TextLength int    `json:"text_length"`
		}
		require.NoError(t, json.Unmarshal(body.Data, &data))
		assert.True(t, utf8.ValidString(data.Text))
		assert.LessOrEqual(t, len(data.Text), xhsScriptProfileExtractMaxTextSize)
		assert.NotContains(t, data.Text, string(utf8.RuneError))
		assert.Equal(t, len(data.Text), data.TextLength)
	})

	t.Run("does not expose parser errors", func(t *testing.T) {
		resp := doXhsScriptMultipartRequest(t, router, path, token, "broken.pdf", []byte("not a pdf"))

		assert.NotEqual(t, http.StatusOK, resp.Code)
		var body apiResponse
		require.NoError(t, json.Unmarshal(resp.Body.Bytes(), &body))
		assert.Contains(t, body.Message, "文档文本提取失败")
		assert.NotContains(t, body.Message, "broken.pdf")
		assert.NotContains(t, body.Message, "python")
		assert.NotContains(t, body.Message, "/")
	})
}

func newXhsScriptControllerTestRouter(t *testing.T) (*gin.Engine, *gorm.DB, string, uint) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	previousJWTSecret := viper.GetString("jwt.secret")
	viper.Set("jwt.secret", "xhs-script-controller-test-secret")
	t.Cleanup(func() { viper.Set("jwt.secret", previousJWTSecret) })

	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/xhs_script_controller_test.db?_busy_timeout=5000"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&model.User{},
		&model.XhsScriptQuotaAccount{},
		&model.XhsScriptQuotaLedger{},
		&model.XhsScriptNote{},
		&model.XhsScriptGeneration{},
		&model.XhsScriptAnalyticsEvent{},
		&model.Order{},
	))
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	user := model.User{Username: "xhs_controller_user", Nickname: "xhs_controller_user", Status: 1}
	require.NoError(t, db.Create(&user).Error)
	previousStore := store.S
	store.S = store.NewTestDataStore(db)
	t.Cleanup(func() { store.S = previousStore })

	svc := xhsscriptbiz.New(store.NewTestStore(db))
	ctl := NewController(svc, nil)

	router := gin.New()
	group := router.Group("/v1/xhs-script")
	group.GET("/ext-token", ctl.ExtToken)
	group.POST("/ext-token", ctl.ExtToken)
	group.POST("/profile/extract-text", ctl.ExtractProfileText)
	group.GET("/notes", ctl.ListNotes)
	group.GET("/notes/:id", ctl.GetNote)
	group.GET("/quota", ctl.GetQuota)

	adminGroup := router.Group("/v1/xhs-script/admin")
	adminGroup.Use(importMw.AdminAuthMiddleware())
	adminGroup.GET("/metrics", ctl.AdminAnalytics)

	return router, db, signControllerTestToken(t, user.ID), user.ID
}

func doXhsScriptRequest(router *gin.Engine, method, path, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}

func doXhsScriptMultipartRequest(t *testing.T, router *gin.Engine, path, token, filename string, content []byte) *httptest.ResponseRecorder {
	t.Helper()
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, err := writer.CreateFormFile("file", filename)
	require.NoError(t, err)
	_, err = part.Write(content)
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	req := httptest.NewRequest(http.MethodPost, path, body)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}

func signControllerTestToken(t *testing.T, userID uint) string {
	return signControllerTestTokenWithScope(t, userID, "")
}

func signControllerTestTokenWithScope(t *testing.T, userID uint, scope string) string {
	t.Helper()
	claims := jwt.MapClaims{
		"user_id": userID,
		"exp":     time.Now().Add(time.Hour).Unix(),
	}
	if scope != "" {
		claims["scope"] = scope
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString([]byte(viper.GetString("jwt.secret")))
	require.NoError(t, err)
	return signed
}
