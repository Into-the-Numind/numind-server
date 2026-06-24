package feishu

import (
	"context"
	"errors"
	"os"
	"runtime"
	"strings"
	"testing"

	"numind-server/internal/pkg/errno"
)

// fakeOpsCLIScript stands in for the real lark-cli ops SHORTCUTS (甲方案 G3-ops):
// `docs +create`, `im +messages-send`, `base +record-list`. It is HOME-aware and
// emits the lark-cli {ok,error,data} envelope, mirroring the real 飞书 response data
// shapes. It records the full argv into $HOME/.ops-log/calls so tests can assert the
// shortcut + flags passed (content / recipient flag / base-token / limit / offset).
const fakeOpsCLIScript = `#!/bin/sh
set -e
svc="$1"
verb="$2"
mkdir -p "$HOME/.ops-log"
# Record the whole argv (one call per line) so tests can grep flags + values.
printf '%s\n' "$*" >> "$HOME/.ops-log/calls"

if [ "$svc" = "docs" ] && [ "$verb" = "+create" ]; then
  printf '{"ok":true,"data":{"document":{"document_id":"docx_new_1","title":"T","url":"https://feishu.cn/docx/docx_new_1"}}}\n'
  exit 0
fi
if [ "$svc" = "im" ] && [ "$verb" = "+messages-send" ]; then
  printf '{"ok":true,"data":{"message_id":"om_msg_1"}}\n'
  exit 0
fi
if [ "$svc" = "base" ] && [ "$verb" = "+record-list" ]; then
  printf '{"ok":true,"data":{"has_more":true,"page_token":"pt_next","total":2,"items":[{"record_id":"rec1","fields":{"Name":"A"}},{"record_id":"rec2","fields":{"Name":"B"}}]}}\n'
  exit 0
fi
printf '{"ok":false,"error":{"type":"validation","subtype":"unknown_verb","message":"unhandled %s %s"}}\n' "$svc" "$verb"
exit 0
`

// fakeOpsErrorScript emits a business-error envelope for every ops call (to
// exercise the soft-error mapping at the runner boundary).
const fakeOpsErrorScript = `#!/bin/sh
printf '{"ok":false,"error":{"type":"api","subtype":"permission_denied","message":"no scope"}}\n'
exit 0
`

func newOpsTestRunner(t *testing.T, script string) (*LarkCLIRunner, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake lark-cli stub is a /bin/sh script")
	}
	homeBase := t.TempDir()
	bin := writeFakeLarkCLI(t, script)
	r, err := NewLarkCLIRunner(bin, homeBase)
	if err != nil {
		t.Fatalf("NewLarkCLIRunner: %v", err)
	}
	return r, homeBase
}

// --- CreateDoc --------------------------------------------------------------

func TestOpsCreateDoc_ParsesDocumentIDAndURL(t *testing.T) {
	r, _ := newOpsTestRunner(t, fakeOpsCLIScript)
	res, err := r.CreateDoc(context.Background(), 7, "我的标题", "正文内容")
	if err != nil {
		t.Fatalf("CreateDoc: %v", err)
	}
	if res.DocumentID != "docx_new_1" {
		t.Fatalf("document_id mismatch: %q", res.DocumentID)
	}
	// The shortcut returns the url directly; we must surface it.
	if res.URL != "https://feishu.cn/docx/docx_new_1" {
		t.Fatalf("URL should come from the shortcut response: %q", res.URL)
	}
	calls := readOpsCalls(t, r, 7)
	// Must use the docs +create shortcut (NOT the generic api path).
	if !strings.Contains(calls, "docs +create") {
		t.Fatalf("expected docs +create shortcut, got:\n%s", calls)
	}
	if !strings.Contains(calls, "--doc-format markdown") {
		t.Fatalf("expected markdown import, got:\n%s", calls)
	}
	// The title must be embedded as the leading # heading inside --content.
	if !strings.Contains(calls, "# 我的标题") {
		t.Fatalf("title must be the leading markdown heading in --content, got:\n%s", calls)
	}
	if !strings.Contains(calls, "正文内容") {
		t.Fatalf("body must be passed in --content, got:\n%s", calls)
	}
	// A single create call — no separate block-children write step anymore.
	if strings.Contains(calls, "children") || strings.Contains(calls, " api ") {
		t.Fatalf("must NOT issue a block-write / generic api call, got:\n%s", calls)
	}
}

func TestOpsCreateDoc_NoContent_TitleOnly(t *testing.T) {
	r, _ := newOpsTestRunner(t, fakeOpsCLIScript)
	if _, err := r.CreateDoc(context.Background(), 7, "标题", ""); err != nil {
		t.Fatalf("CreateDoc: %v", err)
	}
	calls := readOpsCalls(t, r, 7)
	// Exactly one docs +create call, content is just the title heading.
	if strings.Count(calls, "docs +create") != 1 {
		t.Fatalf("title-only doc must be a single create call, got:\n%s", calls)
	}
	if !strings.Contains(calls, "# 标题") {
		t.Fatalf("title heading must be present, got:\n%s", calls)
	}
}

func TestOpsCreateDoc_BusinessError_WrapsLarkCallFailed(t *testing.T) {
	r, _ := newOpsTestRunner(t, fakeOpsErrorScript)
	_, err := r.CreateDoc(context.Background(), 7, "标题", "")
	if !errors.Is(err, errno.ErrLarkCallFailed) {
		t.Fatalf("business error must wrap ErrLarkCallFailed, got %v", err)
	}
}

// --- SendMessage ------------------------------------------------------------

func TestOpsSendMessage_OpenID_UsesUserIDFlag(t *testing.T) {
	r, _ := newOpsTestRunner(t, fakeOpsCLIScript)
	res, err := r.SendMessage(context.Background(), 7, "open_id", "ou_abc", "text", `{"text":"hi there"}`)
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if res.MessageID != "om_msg_1" {
		t.Fatalf("message_id mismatch: %q", res.MessageID)
	}
	calls := readOpsCalls(t, r, 7)
	if !strings.Contains(calls, "im +messages-send") {
		t.Fatalf("expected im +messages-send shortcut, got:\n%s", calls)
	}
	// open_id → --user-id (the shortcut's recipient flag).
	if !strings.Contains(calls, "--user-id ou_abc") {
		t.Fatalf("open_id should map to --user-id, got:\n%s", calls)
	}
	// Text is extracted from the content JSON and passed via --text.
	if !strings.Contains(calls, "--text hi there") {
		t.Fatalf("text should be extracted and passed via --text, got:\n%s", calls)
	}
}

func TestOpsSendMessage_ChatID_UsesChatIDFlag(t *testing.T) {
	r, _ := newOpsTestRunner(t, fakeOpsCLIScript)
	_, err := r.SendMessage(context.Background(), 7, "chat_id", "oc_xyz", "text", `{"text":"team ping"}`)
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	calls := readOpsCalls(t, r, 7)
	if !strings.Contains(calls, "--chat-id oc_xyz") {
		t.Fatalf("chat_id should map to --chat-id, got:\n%s", calls)
	}
	if strings.Contains(calls, "--user-id") {
		t.Fatalf("chat_id must NOT also pass --user-id, got:\n%s", calls)
	}
}

func TestOpsSendMessage_BusinessError_WrapsLarkCallFailed(t *testing.T) {
	r, _ := newOpsTestRunner(t, fakeOpsErrorScript)
	_, err := r.SendMessage(context.Background(), 7, "open_id", "ou_abc", "text", `{"text":"x"}`)
	if !errors.Is(err, errno.ErrLarkCallFailed) {
		t.Fatalf("business error must wrap ErrLarkCallFailed, got %v", err)
	}
}

func TestOpsSendMessage_EmptyWrappedText_Errors(t *testing.T) {
	r, _ := newOpsTestRunner(t, fakeOpsCLIScript)
	// {"text":""} is the wrapper shape with an empty body → must NOT send a blank
	// message (and must NOT leak the literal JSON as the message text).
	_, err := r.SendMessage(context.Background(), 7, "open_id", "ou_abc", "text", `{"text":""}`)
	if !errors.Is(err, errno.ErrLarkCallFailed) {
		t.Fatalf("empty wrapped text must error with ErrLarkCallFailed, got %v", err)
	}
	// No shortcut call should have been issued for an empty message.
	if _, statErr := os.Stat(r.homeForUser(7) + "/.ops-log/calls"); statErr == nil {
		t.Fatal("empty-text message must not invoke the lark-cli shortcut")
	}
}

// --- ReadBitable ------------------------------------------------------------

func TestOpsReadBitable_ParsesRecordsAndPaging(t *testing.T) {
	r, _ := newOpsTestRunner(t, fakeOpsCLIScript)
	res, err := r.ReadBitable(context.Background(), 7, "bascABC", "tblXYZ", 20, 0)
	if err != nil {
		t.Fatalf("ReadBitable: %v", err)
	}
	if len(res.Records) != 2 {
		t.Fatalf("expected 2 records, got %d", len(res.Records))
	}
	if res.Records[0].RecordID != "rec1" || res.Records[0].Fields["Name"] != "A" {
		t.Fatalf("record parse mismatch: %+v", res.Records[0])
	}
	if !res.HasMore || res.Total != 2 {
		t.Fatalf("paging parse mismatch: has_more=%t total=%d", res.HasMore, res.Total)
	}
	calls := readOpsCalls(t, r, 7)
	if !strings.Contains(calls, "base +record-list") {
		t.Fatalf("expected base +record-list shortcut, got:\n%s", calls)
	}
	if !strings.Contains(calls, "--base-token bascABC") || !strings.Contains(calls, "--table-id tblXYZ") {
		t.Fatalf("base-token/table-id must be flags, got:\n%s", calls)
	}
	if !strings.Contains(calls, "--limit 20") {
		t.Fatalf("page size must be passed as --limit, got:\n%s", calls)
	}
	if !strings.Contains(calls, "--format json") {
		t.Fatalf("must request --format json, got:\n%s", calls)
	}
	// offset 0 → no --offset flag (first page).
	if strings.Contains(calls, "--offset") {
		t.Fatalf("offset 0 should omit --offset, got:\n%s", calls)
	}
}

func TestOpsReadBitable_OffsetPaging(t *testing.T) {
	r, _ := newOpsTestRunner(t, fakeOpsCLIScript)
	if _, err := r.ReadBitable(context.Background(), 7, "basc", "tbl", 50, 100); err != nil {
		t.Fatalf("ReadBitable: %v", err)
	}
	calls := readOpsCalls(t, r, 7)
	if !strings.Contains(calls, "--offset 100") {
		t.Fatalf("non-zero offset must be passed as --offset, got:\n%s", calls)
	}
	if !strings.Contains(calls, "--limit 50") {
		t.Fatalf("limit must be passed, got:\n%s", calls)
	}
}

func TestOpsReadBitable_BusinessError_WrapsLarkCallFailed(t *testing.T) {
	r, _ := newOpsTestRunner(t, fakeOpsErrorScript)
	_, err := r.ReadBitable(context.Background(), 7, "basc", "tbl", 10, 0)
	if !errors.Is(err, errno.ErrLarkCallFailed) {
		t.Fatalf("business error must wrap ErrLarkCallFailed, got %v", err)
	}
}

// --- compile-time guard -----------------------------------------------------

func TestClient_SatisfiesLarkAPIProvider(t *testing.T) {
	var _ LarkAPIProvider = (*Client)(nil)
}

// --- helpers ----------------------------------------------------------------

func readOpsCalls(t *testing.T, r *LarkCLIRunner, userID uint) string {
	t.Helper()
	raw, err := os.ReadFile(r.homeForUser(userID) + "/.ops-log/calls")
	if err != nil {
		t.Fatalf("read ops calls log: %v", err)
	}
	return string(raw)
}
