package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/middleware"
	"numind-server/internal/pkg/model"
)

func newXhsNoteListTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/xhs-note-list.db?_busy_timeout=5000&_journal_mode=WAL"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.XhsTopicNote{}))
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

func seedAgentXhsNote(t *testing.T, db *gorm.DB, userID uint, i int) model.XhsTopicNote {
	t.Helper()
	collected := time.Date(2026, 7, 20, 8, i, 0, 0, time.FixedZone("CST", 8*3600))
	transcript := fmt.Sprintf("文字稿-%d", i)
	note := model.XhsTopicNote{
		UserID:          userID,
		XhsNoteID:       fmt.Sprintf("note-%d", i),
		ContentHash:     fmt.Sprintf("hash-%d-%d", userID, i),
		NoteType:        model.XhsNoteTypeVideo,
		Title:           fmt.Sprintf("标题-%d", i),
		Content:         fmt.Sprintf("正文-%d", i),
		VideoTranscript: &transcript,
		LikeCount:       i,
		CollectCount:    i + 10,
		CommentCount:    i + 20,
		Comments: datatypes.JSON(fmt.Sprintf(
			`[{"author":"不能暴露","text":"评论-%d","replies":[{"author":"也不能暴露","text":"回复-%d","replies":[{"text":"二层回复不能返回"}]}]}]`, i, i,
		)),
		NoteURL:     fmt.Sprintf("https://www.xiaohongshu.com/explore/%d", i),
		CollectedAt: &collected,
		CrawledAt:   collected,
	}
	require.NoError(t, db.Create(&note).Error)
	return note
}

func executeXhsNoteList(t *testing.T, tool *xhsNoteListTool, ctx context.Context, input string) map[string]any {
	t.Helper()
	result, err := tool.Execute(ctx, json.RawMessage(input))
	require.NoError(t, err)
	var out map[string]any
	require.NoError(t, json.Unmarshal(result, &out))
	return out
}

func requireXhsSoftError(t *testing.T, tool *xhsNoteListTool, ctx context.Context, input string) {
	t.Helper()
	out := executeXhsNoteList(t, tool, ctx, input)
	errText, ok := out["error"].(string)
	require.True(t, ok, "soft error output must contain an error string: %#v", out)
	assert.NotEmpty(t, errText)
}

func TestXhsNoteList_InputSchemaAndMetadata(t *testing.T) {
	tool := &xhsNoteListTool{}
	assert.Equal(t, "xhs_note_list", tool.Name())
	assert.True(t, tool.IsReadOnly())
	assert.True(t, tool.IsSearchOrReadCommand())
	assert.True(t, tool.AlwaysLoad())

	var schema map[string]any
	require.NoError(t, json.Unmarshal(tool.InputSchema(), &schema))
	assert.Equal(t, false, schema["additionalProperties"])
	properties := schema["properties"].(map[string]any)
	assert.NotContains(t, properties, "user_id")
	limit := properties["limit"].(map[string]any)
	assert.EqualValues(t, 1, limit["minimum"])
	assert.EqualValues(t, 100, limit["maximum"])
	assert.EqualValues(t, 100, limit["default"])
}

func TestXhsNoteList_IndexFullProjectionAndCurrentUserIsolation(t *testing.T) {
	db := newXhsNoteListTestDB(t)
	owned1 := seedAgentXhsNote(t, db, 11, 1)
	owned2 := seedAgentXhsNote(t, db, 11, 2)
	_ = seedAgentXhsNote(t, db, 22, 3)
	tool := &xhsNoteListTool{store: store.NewXhsStore(db)}
	ctx := middleware.NewContextWithUserID(context.Background(), 11)

	index := executeXhsNoteList(t, tool, ctx, `{"projection":"index","limit":1}`)
	assert.Equal(t, "xhs-note-list/v1", index["schema_version"])
	assert.Equal(t, "index", index["projection"])
	assert.EqualValues(t, 2, index["snapshot_total"])
	assert.EqualValues(t, 1, index["returned_count"])
	assert.Equal(t, true, index["has_more"])
	assert.NotEmpty(t, index["next_cursor"])
	indexItem := index["items"].([]any)[0].(map[string]any)
	assert.Equal(t, owned1.XhsNoteID, indexItem["xhs_note_id"])
	assert.NotContains(t, indexItem, "title")
	assert.NotContains(t, indexItem, "content")
	assert.NotEqual(t, "note-3", indexItem["xhs_note_id"])

	full := executeXhsNoteList(t, tool, ctx, fmt.Sprintf(`{"projection":"full","xhs_note_ids":[%q],"limit":100}`, owned2.XhsNoteID))
	assert.Equal(t, "full", full["projection"])
	assert.Equal(t, "stored_capture_value_presence_unknown", full["count_semantics"])
	items := full["items"].([]any)
	require.Len(t, items, 1)
	item := items[0].(map[string]any)
	assert.Equal(t, "标题-2", item["title"])
	assert.Equal(t, "正文-2", item["content"])
	assert.Equal(t, "文字稿-2", item["video_transcript"])
	assert.EqualValues(t, 2, item["like_count"])
	assert.EqualValues(t, 12, item["collect_count"])
	assert.EqualValues(t, 22, item["comment_count"])
	assert.Equal(t, []any{"评论-2", "回复-2"}, item["comment_texts"])
	encoded, err := json.Marshal(item)
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), "不能暴露")
	assert.NotContains(t, string(encoded), "二层回复不能返回")
	assert.NotContains(t, item, "enrich_status")
	assert.NotContains(t, item, "ai_topic_angle")
}

func TestXhsNoteList_NullsMalformedCommentsAndStableCursor(t *testing.T) {
	db := newXhsNoteListTestDB(t)
	for i := 1; i <= 3; i++ {
		seedAgentXhsNote(t, db, 9, i)
	}
	require.NoError(t, db.Model(&model.XhsTopicNote{}).Where("user_id = ? AND xhs_note_id = ?", 9, "note-1").Updates(map[string]any{
		"note_type":        "",
		"title":            "",
		"content":          "",
		"video_transcript": nil,
		"note_url":         "",
		"collected_at":     nil,
		"comments":         datatypes.JSON(`not-json`),
	}).Error)
	tool := &xhsNoteListTool{store: store.NewXhsStore(db)}
	ctx := middleware.NewContextWithUserID(context.Background(), 9)

	first := executeXhsNoteList(t, tool, ctx, `{"projection":"full","limit":1}`)
	item := first["items"].([]any)[0].(map[string]any)
	assert.Nil(t, item["note_type"])
	assert.Nil(t, item["title"])
	assert.Nil(t, item["content"])
	assert.Nil(t, item["video_transcript"])
	assert.Nil(t, item["note_url"])
	assert.Nil(t, item["collected_at"])
	assert.Equal(t, []any{}, item["comment_texts"])

	cursor := first["next_cursor"].(string)
	late := seedAgentXhsNote(t, db, 9, 99)
	second := executeXhsNoteList(t, tool, ctx, fmt.Sprintf(`{"projection":"full","limit":1,"cursor":%q}`, cursor))
	assert.Equal(t, true, second["has_more"])
	thirdCursor := second["next_cursor"].(string)
	third := executeXhsNoteList(t, tool, ctx, fmt.Sprintf(`{"projection":"full","limit":1,"cursor":%q}`, thirdCursor))
	assert.Equal(t, false, third["has_more"])
	assert.Nil(t, third["next_cursor"])
	for _, page := range []map[string]any{first, second, third} {
		for _, raw := range page["items"].([]any) {
			assert.NotEqualValues(t, late.ID, raw.(map[string]any)["id"])
		}
	}
}

func TestXhsNoteList_ValidationAndCursorFilterBindingSoftErrors(t *testing.T) {
	db := newXhsNoteListTestDB(t)
	for i := 1; i <= 3; i++ {
		seedAgentXhsNote(t, db, 7, i)
	}
	tool := &xhsNoteListTool{store: store.NewXhsStore(db)}
	ctx := middleware.NewContextWithUserID(context.Background(), 7)
	defaults := executeXhsNoteList(t, tool, ctx, `{}`)
	assert.Equal(t, "index", defaults["projection"])
	assert.EqualValues(t, 3, defaults["returned_count"])
	assert.Equal(t, false, defaults["has_more"])

	for _, input := range []string{
		`{"limit":0}`,
		`{"limit":101}`,
		`{"user_id":7}`,
		`{"projection":"bogus"}`,
		`{"xhs_note_ids":["note-1","note-1"]}`,
		`{"xhs_note_ids":[""]}`,
		`{"keyword":""}`,
		`{"collected_from":"bad-time"}`,
		`{"collected_from":"2026-07-21T00:00:00Z","collected_to":"2026-07-20T00:00:00Z"}`,
		`{"cursor":"broken"}`,
	} {
		requireXhsSoftError(t, tool, ctx, input)
	}
	requireXhsSoftError(t, tool, context.Background(), `{}`)

	tooManyIDs := make([]string, 101)
	for i := range tooManyIDs {
		tooManyIDs[i] = fmt.Sprintf("id-%d", i)
	}
	encodedIDs, err := json.Marshal(tooManyIDs)
	require.NoError(t, err)
	requireXhsSoftError(t, tool, ctx, `{"xhs_note_ids":`+string(encodedIDs)+`}`)

	first := executeXhsNoteList(t, tool, ctx, `{"projection":"index","keyword":"标题","limit":1}`)
	cursor := first["next_cursor"].(string)
	requireXhsSoftError(t, tool, ctx, fmt.Sprintf(`{"projection":"full","keyword":"标题","limit":1,"cursor":%q}`, cursor))
	requireXhsSoftError(t, tool, ctx, fmt.Sprintf(`{"projection":"index","keyword":"正文","limit":1,"cursor":%q}`, cursor))
}

func TestXhsNoteList_StoreFailureIsGoError(t *testing.T) {
	db := newXhsNoteListTestDB(t)
	tool := &xhsNoteListTool{store: store.NewXhsStore(db)}
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())

	result, execErr := tool.Execute(middleware.NewContextWithUserID(context.Background(), 1), json.RawMessage(`{}`))
	require.Error(t, execErr)
	assert.Nil(t, result)
	assert.Contains(t, strings.ToLower(execErr.Error()), "snapshot")
}

func TestXhsNoteList_CursorCanonicalRoundTrip(t *testing.T) {
	left, err := normalizeXhsNoteListInput(json.RawMessage(`{"projection":"full","xhs_note_ids":["b","a"],"keyword":" 标题 "}`))
	require.NoError(t, err)
	right, err := normalizeXhsNoteListInput(json.RawMessage(`{"projection":"full","xhs_note_ids":["a","b"],"keyword":"标题"}`))
	require.NoError(t, err)
	assert.Equal(t, left.filterSHA256, right.filterSHA256, "semantically identical filters must have one canonical hash")

	want := xhsNoteListCursor{
		Version:       xhsNoteListCursorVersion,
		AfterID:       12,
		SnapshotMaxID: 99,
		FilterSHA256:  left.filterSHA256,
		Projection:    "full",
	}
	encoded, err := encodeXhsNoteListCursor(want)
	require.NoError(t, err)
	got, err := decodeXhsNoteListCursor(encoded)
	require.NoError(t, err)
	assert.Equal(t, want, *got)
	reencoded, err := encodeXhsNoteListCursor(*got)
	require.NoError(t, err)
	assert.Equal(t, encoded, reencoded)
}
