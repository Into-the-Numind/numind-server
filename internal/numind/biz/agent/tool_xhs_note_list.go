package agent

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/spf13/viper"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/middleware"
	"numind-server/internal/pkg/model"
)

var xhsNoteListFallbackCursorKey = func() []byte {
	key := make([]byte, sha256.Size)
	if _, err := rand.Read(key); err != nil {
		panic("xhs_note_list: cannot initialize cursor signing key")
	}
	return key
}()

const (
	xhsNoteListSchemaVersion = "xhs-note-list/v1"
	xhsNoteListCursorVersion = 1
	xhsNoteListDefaultLimit  = 100
	xhsNoteListMaxLimit      = 100
	xhsNoteListMaxCursorLen  = 4096
	xhsCountSemantics        = "stored_capture_value_presence_unknown"
)

type xhsNoteListInput struct {
	Projection    string   `json:"projection"`
	Cursor        string   `json:"cursor"`
	Limit         *int     `json:"limit"`
	XhsNoteIDs    []string `json:"xhs_note_ids"`
	Keyword       *string  `json:"keyword"`
	CollectedFrom *string  `json:"collected_from"`
	CollectedTo   *string  `json:"collected_to"`
}

type xhsNoteListCursor struct {
	Version       int    `json:"v"`
	AfterID       uint64 `json:"after_id"`
	SnapshotMaxID uint64 `json:"snapshot_max_id"`
	SnapshotTotal int64  `json:"snapshot_total"`
	FilterSHA256  string `json:"filter_sha256"`
	Projection    string `json:"projection"`
}

type xhsNoteListFilterFingerprint struct {
	XhsNoteIDs    []string `json:"xhs_note_ids,omitempty"`
	Keyword       string   `json:"keyword,omitempty"`
	CollectedFrom string   `json:"collected_from,omitempty"`
	CollectedTo   string   `json:"collected_to,omitempty"`
}

type normalizedXhsNoteListInput struct {
	projection   store.XhsSnapshotProjection
	limit        int
	filter       store.XhsSnapshotFilter
	filterSHA256 string
	cursor       *xhsNoteListCursor
}

type xhsNoteListOutput struct {
	SchemaVersion  string `json:"schema_version"`
	Projection     string `json:"projection"`
	Items          []any  `json:"items"`
	SnapshotTotal  int64  `json:"snapshot_total"`
	ReturnedCount  int    `json:"returned_count"`
	HasMore        bool   `json:"has_more"`
	NextCursor     string `json:"next_cursor,omitempty"`
	CountSemantics string `json:"count_semantics"`
}

type xhsNoteListIndexItem struct {
	ID          uint64  `json:"id"`
	XhsNoteID   string  `json:"xhs_note_id"`
	CollectedAt *string `json:"collected_at"`
}

type xhsNoteListFullItem struct {
	xhsNoteListIndexItem
	NoteType        *string  `json:"note_type"`
	Title           *string  `json:"title"`
	Content         *string  `json:"content"`
	VideoTranscript *string  `json:"video_transcript"`
	LikeCount       int      `json:"like_count"`
	CollectCount    int      `json:"collect_count"`
	CommentCount    int      `json:"comment_count"`
	CommentTexts    []string `json:"comment_texts"`
	NoteURL         *string  `json:"note_url"`
}

type xhsNoteListComment struct {
	Text    string               `json:"text"`
	Replies []xhsNoteListComment `json:"replies"`
}

type xhsNoteListTool struct {
	BaseTool
	store store.IXhsTopicStore
}

// NewXhsNoteListTool creates the current-user-only XHS library reader.
func NewXhsNoteListTool(xhsStore store.IXhsTopicStore) FullTool {
	return &xhsNoteListTool{store: xhsStore}
}

var _ FullTool = (*xhsNoteListTool)(nil)

func (t *xhsNoteListTool) Name() string { return "xhs_note_list" }
func (t *xhsNoteListTool) Description() string {
	return "Read the current authenticated user's captured Xiaohongshu notes with a stable cursor. " +
		"Use projection=index to discover business keys and projection=full only for notes that need tagging."
}
func (t *xhsNoteListTool) UserFacingName() string      { return "读取小红书选题库" }
func (t *xhsNoteListTool) NarrationVerb() string       { return "读取小红书选题库" }
func (t *xhsNoteListTool) IsSearchOrReadCommand() bool { return true }
func (t *xhsNoteListTool) AlwaysLoad() bool            { return true }

func (t *xhsNoteListTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"additionalProperties": false,
		"properties": {
			"projection": {"type": "string", "enum": ["index", "full"], "default": "index", "description": "index only discovers and compares note keys; full returns the raw fields required by the tagging agent"},
			"cursor": {"type": "string", "description": "Opaque cursor returned by the previous page"},
			"limit": {"type": "integer", "minimum": 1, "maximum": 100, "default": 100},
			"xhs_note_ids": {"type": "array", "maxItems": 100, "uniqueItems": true, "items": {"type": "string", "minLength": 1, "maxLength": 128}},
			"keyword": {"type": "string", "minLength": 1, "maxLength": 100, "description": "Match an explicitly requested historical scope in title or content"},
			"collected_from": {"type": "string", "format": "date-time"},
			"collected_to": {"type": "string", "format": "date-time"}
		}
	}`)
}

func (t *xhsNoteListTool) returnSoftError(format string, args ...any) (ToolResult, error) {
	out, _ := json.Marshal(struct {
		SchemaVersion string `json:"schema_version"`
		Error         string `json:"error"`
	}{
		SchemaVersion: xhsNoteListSchemaVersion,
		Error:         "ERROR: " + fmt.Sprintf(format, args...),
	})
	return out, nil
}

// Execute reads one current-user XHS snapshot page. Validation failures are
// soft errors so the model can correct its call; store failures terminate the run.
func (t *xhsNoteListTool) Execute(ctx context.Context, input ToolInput) (ToolResult, error) {
	userID, ok := middleware.UserIDFromCtx(ctx)
	if !ok || userID == 0 {
		return t.returnSoftError("user not authenticated")
	}
	if t.store == nil {
		return nil, fmt.Errorf("xhs_note_list: XHS store is not configured")
	}

	normalized, validationErr := normalizeXhsNoteListInput(input)
	if validationErr != nil {
		return t.returnSoftError("%v", validationErr)
	}
	span := startSafePipelineToolSpan(ctx, "tool.xhs_note_list.execute", map[string]any{
		"projection":   string(normalized.projection),
		"filter_kinds": xhsNoteListFilterKinds(normalized.filter),
		"limit":        normalized.limit,
	})
	spanOutput := map[string]any{"returned_count": 0, "has_more": false}
	spanErrorClass := pipelineToolTraceNoError
	defer func() { span.End(spanOutput, spanErrorClass) }()

	query := store.XhsSnapshotQuery{
		Filter:     normalized.filter,
		Projection: normalized.projection,
		Limit:      normalized.limit,
	}
	if normalized.cursor != nil {
		query.AfterID = normalized.cursor.AfterID
		query.SnapshotMaxID = normalized.cursor.SnapshotMaxID
		query.SnapshotTotal = normalized.cursor.SnapshotTotal
	}

	page, err := t.store.ListSnapshot(ctx, userID, query)
	if err != nil {
		spanErrorClass = "store_error"
		return nil, fmt.Errorf("xhs_note_list snapshot: %w", err)
	}

	items := make([]any, 0, len(page.Notes))
	for i := range page.Notes {
		indexItem := xhsIndexItem(page.Notes[i])
		if normalized.projection == store.XhsSnapshotProjectionIndex {
			items = append(items, indexItem)
			continue
		}
		items = append(items, xhsNoteListFullItem{
			xhsNoteListIndexItem: indexItem,
			NoteType:             nullableXhsNoteType(page.Notes[i].NoteType),
			Title:                nullableXhsString(page.Notes[i].Title),
			Content:              nullableXhsString(page.Notes[i].Content),
			VideoTranscript:      page.Notes[i].VideoTranscript,
			LikeCount:            page.Notes[i].LikeCount,
			CollectCount:         page.Notes[i].CollectCount,
			CommentCount:         page.Notes[i].CommentCount,
			CommentTexts:         xhsCommentTexts(json.RawMessage(page.Notes[i].Comments)),
			NoteURL:              nullableXhsString(page.Notes[i].NoteURL),
		})
	}

	out := xhsNoteListOutput{
		SchemaVersion:  xhsNoteListSchemaVersion,
		Projection:     string(normalized.projection),
		Items:          items,
		SnapshotTotal:  page.SnapshotTotal,
		ReturnedCount:  len(items),
		HasMore:        page.HasMore,
		CountSemantics: xhsCountSemantics,
	}
	if page.HasMore {
		nextCursor, cursorErr := encodeXhsNoteListCursor(xhsNoteListCursor{
			Version:       xhsNoteListCursorVersion,
			AfterID:       page.NextAfterID,
			SnapshotMaxID: page.SnapshotMaxID,
			SnapshotTotal: page.SnapshotTotal,
			FilterSHA256:  normalized.filterSHA256,
			Projection:    string(normalized.projection),
		})
		if cursorErr != nil {
			spanErrorClass = "cursor_encoding_error"
			return nil, fmt.Errorf("xhs_note_list encode cursor: %w", cursorErr)
		}
		out.NextCursor = nextCursor
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		spanErrorClass = "result_encoding_error"
		return nil, fmt.Errorf("xhs_note_list marshal result: %w", err)
	}
	spanOutput["returned_count"] = len(items)
	spanOutput["has_more"] = page.HasMore
	return encoded, nil
}

func xhsNoteListFilterKinds(filter store.XhsSnapshotFilter) []string {
	kinds := make([]string, 0, 4)
	if len(filter.XhsNoteIDs) > 0 {
		kinds = append(kinds, "xhs_note_ids")
	}
	if filter.Keyword != "" {
		kinds = append(kinds, "keyword")
	}
	if filter.CollectedFrom != nil {
		kinds = append(kinds, "collected_from")
	}
	if filter.CollectedTo != nil {
		kinds = append(kinds, "collected_to")
	}
	return kinds
}

func normalizeXhsNoteListInput(input ToolInput) (*normalizedXhsNoteListInput, error) {
	var in xhsNoteListInput
	decoder := json.NewDecoder(bytes.NewReader(input))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&in); err != nil {
		return nil, fmt.Errorf("invalid input JSON: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return nil, err
	}

	projection := store.XhsSnapshotProjection(strings.TrimSpace(in.Projection))
	if projection == "" {
		projection = store.XhsSnapshotProjectionIndex
	}
	if projection != store.XhsSnapshotProjectionIndex && projection != store.XhsSnapshotProjectionFull {
		return nil, fmt.Errorf("projection must be index or full")
	}
	limit := xhsNoteListDefaultLimit
	if in.Limit != nil {
		limit = *in.Limit
	}
	if limit < 1 || limit > xhsNoteListMaxLimit {
		return nil, fmt.Errorf("limit must be between 1 and 100")
	}

	if len(in.XhsNoteIDs) > 100 {
		return nil, fmt.Errorf("xhs_note_ids must contain at most 100 values")
	}
	ids := append([]string(nil), in.XhsNoteIDs...)
	seen := make(map[string]struct{}, len(ids))
	for i := range ids {
		ids[i] = strings.TrimSpace(ids[i])
		if utf8.RuneCountInString(ids[i]) < 1 || utf8.RuneCountInString(ids[i]) > 128 {
			return nil, fmt.Errorf("each xhs_note_id must contain 1 to 128 characters")
		}
		if _, exists := seen[ids[i]]; exists {
			return nil, fmt.Errorf("xhs_note_ids must be unique")
		}
		seen[ids[i]] = struct{}{}
	}
	sort.Strings(ids)

	keyword := ""
	if in.Keyword != nil {
		keyword = strings.TrimSpace(*in.Keyword)
		if utf8.RuneCountInString(keyword) < 1 || utf8.RuneCountInString(keyword) > 100 {
			return nil, fmt.Errorf("keyword must contain 1 to 100 characters")
		}
	}

	from, fromCanonical, err := parseXhsNoteListTime(in.CollectedFrom, "collected_from")
	if err != nil {
		return nil, err
	}
	to, toCanonical, err := parseXhsNoteListTime(in.CollectedTo, "collected_to")
	if err != nil {
		return nil, err
	}
	if from != nil && to != nil && !from.Before(*to) {
		return nil, fmt.Errorf("collected_from must be before collected_to")
	}

	fingerprint := xhsNoteListFilterFingerprint{
		XhsNoteIDs:    ids,
		Keyword:       keyword,
		CollectedFrom: fromCanonical,
		CollectedTo:   toCanonical,
	}
	fingerprintJSON, err := json.Marshal(fingerprint)
	if err != nil {
		return nil, fmt.Errorf("marshal filter fingerprint: %w", err)
	}
	hash := sha256.Sum256(fingerprintJSON)
	filterSHA256 := hex.EncodeToString(hash[:])

	normalized := &normalizedXhsNoteListInput{
		projection: projection,
		limit:      limit,
		filter: store.XhsSnapshotFilter{
			XhsNoteIDs:    ids,
			Keyword:       keyword,
			CollectedFrom: from,
			CollectedTo:   to,
		},
		filterSHA256: filterSHA256,
	}
	if in.Cursor != "" {
		cursor, cursorErr := decodeXhsNoteListCursor(in.Cursor)
		if cursorErr != nil {
			return nil, cursorErr
		}
		if cursor.Projection != string(projection) {
			return nil, fmt.Errorf("cursor projection does not match this request")
		}
		if cursor.FilterSHA256 != filterSHA256 {
			return nil, fmt.Errorf("cursor filters do not match this request")
		}
		normalized.cursor = cursor
	}
	return normalized, nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("invalid input JSON: multiple values")
		}
		return fmt.Errorf("invalid input JSON: %w", err)
	}
	return nil
}

func parseXhsNoteListTime(raw *string, field string) (*time.Time, string, error) {
	if raw == nil {
		return nil, "", nil
	}
	parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(*raw))
	if err != nil {
		return nil, "", fmt.Errorf("%s must be RFC3339: %w", field, err)
	}
	canonical := parsed.UTC().Format(time.RFC3339Nano)
	return &parsed, canonical, nil
}

func encodeXhsNoteListCursor(cursor xhsNoteListCursor) (string, error) {
	encoded, err := json.Marshal(cursor)
	if err != nil {
		return "", err
	}
	payload := base64.RawURLEncoding.EncodeToString(encoded)
	mac := hmac.New(sha256.New, xhsNoteListCursorSigningKey())
	_, _ = mac.Write([]byte(payload))
	signature := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return payload + "." + signature, nil
}

func decodeXhsNoteListCursor(raw string) (*xhsNoteListCursor, error) {
	if len(raw) > xhsNoteListMaxCursorLen {
		return nil, fmt.Errorf("cursor is too long")
	}
	payload, signature, ok := strings.Cut(raw, ".")
	if !ok || payload == "" || signature == "" || strings.Contains(signature, ".") {
		return nil, fmt.Errorf("invalid cursor signature")
	}
	providedMAC, err := base64.RawURLEncoding.DecodeString(signature)
	if err != nil {
		return nil, fmt.Errorf("invalid cursor signature")
	}
	expectedMAC := hmac.New(sha256.New, xhsNoteListCursorSigningKey())
	_, _ = expectedMAC.Write([]byte(payload))
	if !hmac.Equal(providedMAC, expectedMAC.Sum(nil)) {
		return nil, fmt.Errorf("invalid cursor signature")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return nil, fmt.Errorf("invalid cursor encoding")
	}
	var cursor xhsNoteListCursor
	decoder := json.NewDecoder(bytes.NewReader(decoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cursor); err != nil {
		return nil, fmt.Errorf("invalid cursor payload")
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return nil, fmt.Errorf("invalid cursor payload")
	}
	if cursor.Version != xhsNoteListCursorVersion || cursor.SnapshotMaxID == 0 || cursor.SnapshotTotal < 1 || cursor.AfterID == 0 || cursor.AfterID > cursor.SnapshotMaxID {
		return nil, fmt.Errorf("invalid cursor state")
	}
	if cursor.Projection != string(store.XhsSnapshotProjectionIndex) && cursor.Projection != string(store.XhsSnapshotProjectionFull) {
		return nil, fmt.Errorf("invalid cursor projection")
	}
	if len(cursor.FilterSHA256) != sha256.Size*2 {
		return nil, fmt.Errorf("invalid cursor filter fingerprint")
	}
	if _, err := hex.DecodeString(cursor.FilterSHA256); err != nil {
		return nil, fmt.Errorf("invalid cursor filter fingerprint")
	}
	return &cursor, nil
}

func xhsNoteListCursorSigningKey() []byte {
	secret := strings.TrimSpace(viper.GetString("jwt.secret"))
	if secret == "" {
		// Unit tests and narrowly composed tools may run without application
		// configuration. A process-random key keeps those cursors tamper-evident;
		// production always derives a stable, domain-separated key from jwt.secret.
		return xhsNoteListFallbackCursorKey
	}
	digest := sha256.Sum256([]byte("numind/xhs-note-list/cursor/v1\x00" + secret))
	return digest[:]
}

func xhsIndexItem(note model.XhsTopicNote) xhsNoteListIndexItem {
	var collectedAt *string
	if note.CollectedAt != nil {
		formatted := note.CollectedAt.Format(time.RFC3339Nano)
		collectedAt = &formatted
	}
	return xhsNoteListIndexItem{ID: note.ID, XhsNoteID: note.XhsNoteID, CollectedAt: collectedAt}
}

func nullableXhsString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func nullableXhsNoteType(value string) *string {
	if value != model.XhsNoteTypeNormal && value != model.XhsNoteTypeVideo {
		return nil
	}
	return &value
}

func xhsCommentTexts(raw json.RawMessage) []string {
	texts := []string{}
	if len(raw) == 0 {
		return texts
	}
	var comments []xhsNoteListComment
	if err := json.Unmarshal(raw, &comments); err != nil {
		return texts
	}
	for _, comment := range comments {
		if text := strings.TrimSpace(comment.Text); text != "" {
			texts = append(texts, text)
		}
		for _, reply := range comment.Replies {
			if text := strings.TrimSpace(reply.Text); text != "" {
				texts = append(texts, text)
			}
		}
	}
	return texts
}
