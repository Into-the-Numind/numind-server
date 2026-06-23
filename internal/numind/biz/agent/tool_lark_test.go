package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"numind-server/internal/numind/biz/feishu"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/middleware"
)

// --- test doubles -----------------------------------------------------------

// fakeLarkAPI is a scripted feishu.LarkAPI for the tool unit tests.
type fakeLarkAPI struct {
	doc     *feishu.DocResult
	docErr  error
	msg     *feishu.MsgResult
	msgErr  error
	bit     *feishu.BitableResult
	bitErr  error
	lastDoc struct{ title, content string }
	lastMsg struct{ idType, id, msgType, content string }
	lastBit struct {
		appToken, tableID, pageToken string
		pageSize                     int
	}
}

func (f *fakeLarkAPI) CreateDoc(_ context.Context, title, content string) (*feishu.DocResult, error) {
	f.lastDoc.title, f.lastDoc.content = title, content
	return f.doc, f.docErr
}

func (f *fakeLarkAPI) SendMessage(_ context.Context, idType, id, msgType, content string) (*feishu.MsgResult, error) {
	f.lastMsg.idType, f.lastMsg.id, f.lastMsg.msgType, f.lastMsg.content = idType, id, msgType, content
	return f.msg, f.msgErr
}

func (f *fakeLarkAPI) ReadBitable(_ context.Context, appToken, tableID string, pageSize int, pageToken string) (*feishu.BitableResult, error) {
	f.lastBit.appToken, f.lastBit.tableID, f.lastBit.pageSize, f.lastBit.pageToken = appToken, tableID, pageSize, pageToken
	return f.bit, f.bitErr
}

// fakeLarkProvider returns a fixed LarkAPI (or a fixed error).
type fakeLarkProvider struct {
	api feishu.LarkAPI
	err error
}

func (p *fakeLarkProvider) APIFor(_ context.Context, _ uint) (feishu.LarkAPI, error) {
	return p.api, p.err
}

// ctxWithUser builds a context carrying a userID (the runner injects this).
func ctxWithUser(uid uint) context.Context {
	return middleware.NewContextWithUserID(context.Background(), uid)
}

// decodeErr extracts the "error" field from a soft-error tool result.
func decodeErr(t *testing.T, raw ToolResult) string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal tool result: %v (raw=%s)", err, raw)
	}
	if e, ok := m["error"].(string); ok {
		return e
	}
	return ""
}

// --- lark_create_doc --------------------------------------------------------

func TestLarkCreateDoc_Success(t *testing.T) {
	api := &fakeLarkAPI{doc: &feishu.DocResult{DocumentID: "doc123", Title: "T", URL: "https://feishu.cn/docx/doc123"}}
	tool := &larkCreateDocTool{provider: &fakeLarkProvider{api: api}}

	in, _ := json.Marshal(larkCreateDocInput{Title: "T", Content: "hello"})
	raw, err := tool.Execute(ctxWithUser(7), ToolInput(in))
	if err != nil {
		t.Fatalf("Execute returned a Go error (would kill the run): %v", err)
	}
	var out larkCreateDocOutput
	if jerr := json.Unmarshal(raw, &out); jerr != nil {
		t.Fatalf("unmarshal: %v", jerr)
	}
	if out.DocumentID != "doc123" {
		t.Fatalf("document_id: want doc123, got %q", out.DocumentID)
	}
	if out.Error != "" {
		t.Fatalf("success should have no error field; got %q", out.Error)
	}
	if api.lastDoc.title != "T" || api.lastDoc.content != "hello" {
		t.Fatalf("API not called with the input args: %+v", api.lastDoc)
	}
}

func TestLarkCreateDoc_ReauthRequired_SoftError(t *testing.T) {
	// Provider can't build an API because the token expired → ErrLarkReauthRequired.
	tool := &larkCreateDocTool{provider: &fakeLarkProvider{err: fmt.Errorf("for: %w", errno.ErrLarkReauthRequired)}}

	in, _ := json.Marshal(larkCreateDocInput{Title: "T"})
	raw, err := tool.Execute(ctxWithUser(7), ToolInput(in))
	// CRITICAL: must NOT return a Go error (that would kill the agent run).
	if err != nil {
		t.Fatalf("reauth must be a SOFT error, got Go error: %v", err)
	}
	if msg := decodeErr(t, raw); msg == "" {
		t.Fatal("expected an error field in the soft result")
	}
}

func TestLarkCreateDoc_NotConnected_SoftError(t *testing.T) {
	tool := &larkCreateDocTool{provider: &fakeLarkProvider{err: fmt.Errorf("for: %w", errno.ErrLarkNotConnected)}}

	in, _ := json.Marshal(larkCreateDocInput{Title: "T"})
	raw, err := tool.Execute(ctxWithUser(7), ToolInput(in))
	if err != nil {
		t.Fatalf("not-connected must be a SOFT error, got Go error: %v", err)
	}
	if msg := decodeErr(t, raw); msg == "" {
		t.Fatal("expected an error field in the soft result")
	}
}

func TestLarkCreateDoc_NilProvider_SoftError(t *testing.T) {
	tool := &larkCreateDocTool{provider: nil} // 飞书 off
	in, _ := json.Marshal(larkCreateDocInput{Title: "T"})
	raw, err := tool.Execute(ctxWithUser(7), ToolInput(in))
	if err != nil {
		t.Fatalf("nil provider must be a SOFT error, got Go error: %v", err)
	}
	if decodeErr(t, raw) == "" {
		t.Fatal("expected an error field")
	}
}

func TestLarkCreateDoc_MalformedInput_SoftError(t *testing.T) {
	tool := &larkCreateDocTool{provider: &fakeLarkProvider{api: &fakeLarkAPI{}}}
	raw, err := tool.Execute(ctxWithUser(7), ToolInput(`{"title": 123}`)) // wrong type
	if err != nil {
		t.Fatalf("malformed input must be SOFT, got Go error: %v", err)
	}
	if decodeErr(t, raw) == "" {
		t.Fatal("expected an error field for malformed input")
	}
}

func TestLarkCreateDoc_EmptyTitle_SoftError(t *testing.T) {
	tool := &larkCreateDocTool{provider: &fakeLarkProvider{api: &fakeLarkAPI{}}}
	in, _ := json.Marshal(larkCreateDocInput{Title: "   "})
	raw, err := tool.Execute(ctxWithUser(7), ToolInput(in))
	if err != nil {
		t.Fatalf("empty title must be SOFT, got Go error: %v", err)
	}
	if decodeErr(t, raw) == "" {
		t.Fatal("expected an error field for empty title")
	}
}

func TestLarkCreateDoc_PartialSuccess_ReportsDocID(t *testing.T) {
	// API created the doc but failed to write content → returns DocResult + error.
	api := &fakeLarkAPI{
		doc:    &feishu.DocResult{DocumentID: "doc999", Title: "T", URL: "u"},
		docErr: fmt.Errorf("%w: write content boom", errno.ErrLarkCallFailed),
	}
	tool := &larkCreateDocTool{provider: &fakeLarkProvider{api: api}}
	in, _ := json.Marshal(larkCreateDocInput{Title: "T", Content: "body"})
	raw, err := tool.Execute(ctxWithUser(7), ToolInput(in))
	if err != nil {
		t.Fatalf("partial failure must be SOFT, got Go error: %v", err)
	}
	var out larkCreateDocOutput
	_ = json.Unmarshal(raw, &out)
	if out.DocumentID != "doc999" {
		t.Fatalf("partial success should still report document_id; got %q", out.DocumentID)
	}
	if out.Error == "" {
		t.Fatal("partial success should carry an error note about the content write")
	}
}

func TestLarkCreateDoc_NoUserInCtx_SoftError(t *testing.T) {
	tool := &larkCreateDocTool{provider: &fakeLarkProvider{api: &fakeLarkAPI{}}}
	in, _ := json.Marshal(larkCreateDocInput{Title: "T"})
	raw, err := tool.Execute(context.Background(), ToolInput(in)) // no userID
	if err != nil {
		t.Fatalf("missing user must be SOFT, got Go error: %v", err)
	}
	if decodeErr(t, raw) == "" {
		t.Fatal("expected an error field when user is unknown")
	}
}

// --- lark_send_message ------------------------------------------------------

func TestLarkSendMessage_Success_DefaultsIDType(t *testing.T) {
	api := &fakeLarkAPI{msg: &feishu.MsgResult{MessageID: "om_1"}}
	tool := &larkSendMessageTool{provider: &fakeLarkProvider{api: api}}

	in, _ := json.Marshal(larkSendMessageInput{ReceiveID: "ou_x", Text: "hi"})
	raw, err := tool.Execute(ctxWithUser(7), ToolInput(in))
	if err != nil {
		t.Fatalf("Execute Go error: %v", err)
	}
	var out larkSendMessageOutput
	_ = json.Unmarshal(raw, &out)
	if out.MessageID != "om_1" {
		t.Fatalf("message_id: want om_1, got %q", out.MessageID)
	}
	if api.lastMsg.idType != "open_id" {
		t.Fatalf("receive_id_type should default to open_id; got %q", api.lastMsg.idType)
	}
	if api.lastMsg.msgType != "text" {
		t.Fatalf("msg_type should be text; got %q", api.lastMsg.msgType)
	}
	// content must be JSON {"text":"hi"}
	var c map[string]string
	if jerr := json.Unmarshal([]byte(api.lastMsg.content), &c); jerr != nil || c["text"] != "hi" {
		t.Fatalf("content should be JSON text envelope; got %q (err=%v)", api.lastMsg.content, jerr)
	}
}

func TestLarkSendMessage_ReauthRequired_SoftError(t *testing.T) {
	tool := &larkSendMessageTool{provider: &fakeLarkProvider{err: fmt.Errorf("for: %w", errno.ErrLarkReauthRequired)}}
	in, _ := json.Marshal(larkSendMessageInput{ReceiveID: "ou_x", Text: "hi"})
	raw, err := tool.Execute(ctxWithUser(7), ToolInput(in))
	if err != nil {
		t.Fatalf("reauth must be SOFT, got Go error: %v", err)
	}
	if decodeErr(t, raw) == "" {
		t.Fatal("expected error field")
	}
}

func TestLarkSendMessage_NotConnected_SoftError(t *testing.T) {
	tool := &larkSendMessageTool{provider: &fakeLarkProvider{err: fmt.Errorf("for: %w", errno.ErrLarkNotConnected)}}
	in, _ := json.Marshal(larkSendMessageInput{ReceiveID: "ou_x", Text: "hi"})
	raw, err := tool.Execute(ctxWithUser(7), ToolInput(in))
	if err != nil {
		t.Fatalf("not-connected must be SOFT, got Go error: %v", err)
	}
	if decodeErr(t, raw) == "" {
		t.Fatal("expected error field")
	}
}

func TestLarkSendMessage_CallFailed_SoftError(t *testing.T) {
	api := &fakeLarkAPI{msgErr: fmt.Errorf("%w: 飞书 code 230002", errno.ErrLarkCallFailed)}
	tool := &larkSendMessageTool{provider: &fakeLarkProvider{api: api}}
	in, _ := json.Marshal(larkSendMessageInput{ReceiveID: "ou_x", Text: "hi"})
	raw, err := tool.Execute(ctxWithUser(7), ToolInput(in))
	if err != nil {
		t.Fatalf("call-failed must be SOFT, got Go error: %v", err)
	}
	if decodeErr(t, raw) == "" {
		t.Fatal("expected error field")
	}
}

func TestLarkSendMessage_InvalidIDType_SoftError(t *testing.T) {
	tool := &larkSendMessageTool{provider: &fakeLarkProvider{api: &fakeLarkAPI{}}}
	in, _ := json.Marshal(larkSendMessageInput{ReceiveID: "x", ReceiveIDType: "bogus", Text: "hi"})
	raw, err := tool.Execute(ctxWithUser(7), ToolInput(in))
	if err != nil {
		t.Fatalf("invalid id type must be SOFT, got Go error: %v", err)
	}
	if decodeErr(t, raw) == "" {
		t.Fatal("expected error field for invalid receive_id_type")
	}
}

func TestLarkSendMessage_EmptyText_SoftError(t *testing.T) {
	tool := &larkSendMessageTool{provider: &fakeLarkProvider{api: &fakeLarkAPI{}}}
	in, _ := json.Marshal(larkSendMessageInput{ReceiveID: "x", Text: ""})
	raw, err := tool.Execute(ctxWithUser(7), ToolInput(in))
	if err != nil {
		t.Fatalf("empty text must be SOFT, got Go error: %v", err)
	}
	if decodeErr(t, raw) == "" {
		t.Fatal("expected error field for empty text")
	}
}

// --- lark_read_bitable ------------------------------------------------------

func TestLarkReadBitable_Success(t *testing.T) {
	api := &fakeLarkAPI{bit: &feishu.BitableResult{
		Records: []feishu.BitableRecord{{RecordID: "r1", Fields: map[string]any{"name": "alice"}}},
		HasMore: true, PageToken: "pt2", Total: 1,
	}}
	tool := &larkReadBitableTool{provider: &fakeLarkProvider{api: api}}

	in, _ := json.Marshal(map[string]any{"app_token": "app1", "table_id": "tbl1", "page_size": 5})
	raw, err := tool.Execute(ctxWithUser(7), ToolInput(in))
	if err != nil {
		t.Fatalf("Execute Go error: %v", err)
	}
	var out larkReadBitableOutput
	_ = json.Unmarshal(raw, &out)
	if len(out.Records) != 1 || out.Records[0].RecordID != "r1" {
		t.Fatalf("records mismatch: %+v", out.Records)
	}
	if !out.HasMore || out.PageToken != "pt2" || out.Total != 1 {
		t.Fatalf("paging fields mismatch: %+v", out)
	}
	if api.lastBit.appToken != "app1" || api.lastBit.tableID != "tbl1" || api.lastBit.pageSize != 5 {
		t.Fatalf("API args mismatch: %+v", api.lastBit)
	}
}

func TestLarkReadBitable_PageSizeAsString_Tolerated(t *testing.T) {
	api := &fakeLarkAPI{bit: &feishu.BitableResult{}}
	tool := &larkReadBitableTool{provider: &fakeLarkProvider{api: api}}
	// LLM sends page_size as a string — must be tolerated (json.Number).
	raw, err := tool.Execute(ctxWithUser(7), ToolInput(`{"app_token":"a","table_id":"t","page_size":"30"}`))
	if err != nil {
		t.Fatalf("string page_size must be SOFT-tolerant, got Go error: %v", err)
	}
	if decodeErr(t, raw) != "" {
		t.Fatalf("string page_size should NOT error: %s", decodeErr(t, raw))
	}
	if api.lastBit.pageSize != 30 {
		t.Fatalf("page_size string should parse to 30; got %d", api.lastBit.pageSize)
	}
}

func TestLarkReadBitable_PageSizeClampedToMax(t *testing.T) {
	api := &fakeLarkAPI{bit: &feishu.BitableResult{}}
	tool := &larkReadBitableTool{provider: &fakeLarkProvider{api: api}}
	in, _ := json.Marshal(map[string]any{"app_token": "a", "table_id": "t", "page_size": 9999})
	if _, err := tool.Execute(ctxWithUser(7), ToolInput(in)); err != nil {
		t.Fatalf("Go error: %v", err)
	}
	if api.lastBit.pageSize != larkReadBitableMaxPageSize {
		t.Fatalf("page_size should clamp to %d; got %d", larkReadBitableMaxPageSize, api.lastBit.pageSize)
	}
}

func TestLarkReadBitable_DefaultPageSize(t *testing.T) {
	api := &fakeLarkAPI{bit: &feishu.BitableResult{}}
	tool := &larkReadBitableTool{provider: &fakeLarkProvider{api: api}}
	in, _ := json.Marshal(map[string]any{"app_token": "a", "table_id": "t"})
	if _, err := tool.Execute(ctxWithUser(7), ToolInput(in)); err != nil {
		t.Fatalf("Go error: %v", err)
	}
	if api.lastBit.pageSize != larkReadBitableDefaultPageSize {
		t.Fatalf("page_size should default to %d; got %d", larkReadBitableDefaultPageSize, api.lastBit.pageSize)
	}
}

func TestLarkReadBitable_ReauthRequired_SoftError(t *testing.T) {
	tool := &larkReadBitableTool{provider: &fakeLarkProvider{err: fmt.Errorf("for: %w", errno.ErrLarkReauthRequired)}}
	in, _ := json.Marshal(map[string]any{"app_token": "a", "table_id": "t"})
	raw, err := tool.Execute(ctxWithUser(7), ToolInput(in))
	if err != nil {
		t.Fatalf("reauth must be SOFT, got Go error: %v", err)
	}
	if decodeErr(t, raw) == "" {
		t.Fatal("expected error field")
	}
}

func TestLarkReadBitable_MissingTableID_SoftError(t *testing.T) {
	tool := &larkReadBitableTool{provider: &fakeLarkProvider{api: &fakeLarkAPI{}}}
	in, _ := json.Marshal(map[string]any{"app_token": "a"})
	raw, err := tool.Execute(ctxWithUser(7), ToolInput(in))
	if err != nil {
		t.Fatalf("missing table_id must be SOFT, got Go error: %v", err)
	}
	if decodeErr(t, raw) == "" {
		t.Fatal("expected error field for missing table_id")
	}
}

// --- metadata / interface guards --------------------------------------------

func TestLarkTools_ImplementFullTool(t *testing.T) {
	var _ FullTool = (*larkCreateDocTool)(nil)
	var _ FullTool = (*larkSendMessageTool)(nil)
	var _ FullTool = (*larkReadBitableTool)(nil)
}

// TestPlatformFactory_RegistersLarkTools_WhenProviderPresent verifies the
// acceptance criterion "factory LoadTools 数量+3": with a 飞书 provider injected,
// the three lark tools (and their metadata) are appended after the memory tools.
func TestPlatformFactory_RegistersLarkTools_WhenProviderPresent(t *testing.T) {
	db := newFactoryTestDB(t)
	ds := store.NewTestStore(db)
	f := &platformToolFactory{ds: ds, larkProviderOverride: &fakeLarkProvider{api: &fakeLarkAPI{}}}

	tools, metadata, err := f.LoadTools(context.Background())
	if err != nil {
		t.Fatalf("LoadTools: %v", err)
	}
	// Base 19 + memory_write + memory_read (21) + 3 lark = 24.
	if len(tools) != 24 {
		t.Fatalf("expected 24 tools (21 + 3 lark); got %d", len(tools))
	}
	if len(metadata) != 24 {
		t.Fatalf("expected 24 metadata entries; got %d", len(metadata))
	}
	got := map[string]bool{}
	for _, tl := range tools {
		got[tl.Name()] = true
	}
	for _, want := range []string{"lark_create_doc", "lark_send_message", "lark_read_bitable"} {
		if !got[want] {
			t.Fatalf("lark tool %q not registered", want)
		}
	}
}

// TestPlatformFactory_NoLarkTools_WhenProviderAbsent verifies the count is
// unchanged (21) when no provider is available (feature off / no Redis).
func TestPlatformFactory_NoLarkTools_WhenProviderAbsent(t *testing.T) {
	db := newFactoryTestDB(t)
	ds := store.NewTestStore(db)
	f := &platformToolFactory{ds: ds} // no override, flag off by default in tests

	tools, _, err := f.LoadTools(context.Background())
	if err != nil {
		t.Fatalf("LoadTools: %v", err)
	}
	if len(tools) != 21 {
		t.Fatalf("expected 21 tools (no lark when provider absent); got %d", len(tools))
	}
	for _, tl := range tools {
		if tl.Name() == "lark_create_doc" || tl.Name() == "lark_send_message" || tl.Name() == "lark_read_bitable" {
			t.Fatalf("lark tool %q should NOT be registered when provider absent", tl.Name())
		}
	}
}

func TestLarkTools_Metadata(t *testing.T) {
	cd := &larkCreateDocTool{}
	if cd.Name() != "lark_create_doc" || cd.IsReadOnly() {
		t.Fatalf("create_doc metadata wrong: name=%q readonly=%v", cd.Name(), cd.IsReadOnly())
	}
	sm := &larkSendMessageTool{}
	if sm.Name() != "lark_send_message" || sm.IsReadOnly() {
		t.Fatalf("send_message metadata wrong: name=%q readonly=%v", sm.Name(), sm.IsReadOnly())
	}
	rb := &larkReadBitableTool{}
	if rb.Name() != "lark_read_bitable" || !rb.IsReadOnly() {
		t.Fatalf("read_bitable metadata wrong: name=%q readonly=%v", rb.Name(), rb.IsReadOnly())
	}
	for _, tool := range []FullTool{cd, sm, rb} {
		if tool.UserFacingName() == "" || tool.NarrationVerb() == "" || tool.Description() == "" {
			t.Fatalf("%s missing narration/desc metadata", tool.Name())
		}
		if len(tool.InputSchema()) == 0 {
			t.Fatalf("%s missing input schema", tool.Name())
		}
	}
}
