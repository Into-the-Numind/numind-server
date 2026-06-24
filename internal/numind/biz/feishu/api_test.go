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

// fakeOpsCLIScript stands in for the real `lark-cli api ...` ops calls. It is
// HOME-aware and emits the lark-cli {ok,error,data} envelope, mirroring the real
// 飞书 response data shapes for the three ops. It also records the invoked path +
// payloads into $HOME/.ops-log so tests can assert the params/body passed.
const fakeOpsCLIScript = `#!/bin/sh
set -e
# Args layout: api <METHOD> <PATH> --as user --json [--params P] [--data D]
method="$2"
path="$3"
params=""
data=""
shift 3
while [ $# -gt 0 ]; do
  case "$1" in
    --params) params="$2"; shift 2 ;;
    --data)   data="$2";   shift 2 ;;
    *)        shift ;;
  esac
done
mkdir -p "$HOME/.ops-log"
printf '%s %s\nPARAMS:%s\nDATA:%s\n' "$method" "$path" "$params" "$data" >> "$HOME/.ops-log/calls"

case "$path" in
  /open-apis/docx/v1/documents)
    printf '{"ok":true,"data":{"document":{"document_id":"docx_new_1","title":"T"}}}\n' ;;
  */blocks/*/children)
    printf '{"ok":true,"data":{"children":[]}}\n' ;;
  /open-apis/im/v1/messages)
    printf '{"ok":true,"data":{"message_id":"om_msg_1"}}\n' ;;
  *records*)
    printf '{"ok":true,"data":{"has_more":true,"page_token":"pt_next","total":2,"items":[{"record_id":"rec1","fields":{"Name":"A"}},{"record_id":"rec2","fields":{"Name":"B"}}]}}\n' ;;
  *)
    printf '{"ok":false,"error":{"type":"validation","subtype":"unknown_path","message":"unhandled %s"}}\n' "$path" ;;
esac
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

func TestOpsCreateDoc_ParsesDocumentID(t *testing.T) {
	r, _ := newOpsTestRunner(t, fakeOpsCLIScript)
	res, err := r.CreateDoc(context.Background(), 7, "我的标题", "正文内容")
	if err != nil {
		t.Fatalf("CreateDoc: %v", err)
	}
	if res.DocumentID != "docx_new_1" {
		t.Fatalf("document_id mismatch: %q", res.DocumentID)
	}
	if !strings.Contains(res.URL, "docx_new_1") {
		t.Fatalf("URL must embed the doc id: %q", res.URL)
	}
	// Body write should have been issued (a second call to the children endpoint).
	calls := readOpsCalls(t, r, 7)
	if !strings.Contains(calls, "/open-apis/docx/v1/documents") {
		t.Fatalf("expected a create-doc call, got:\n%s", calls)
	}
	if !strings.Contains(calls, "/children") {
		t.Fatalf("expected a content-write call, got:\n%s", calls)
	}
	// The title must be passed in the body JSON (single argv element).
	if !strings.Contains(calls, "我的标题") {
		t.Fatalf("title must be passed in --data, got:\n%s", calls)
	}
}

func TestOpsCreateDoc_NoContent_SkipsBodyWrite(t *testing.T) {
	r, _ := newOpsTestRunner(t, fakeOpsCLIScript)
	if _, err := r.CreateDoc(context.Background(), 7, "标题", ""); err != nil {
		t.Fatalf("CreateDoc: %v", err)
	}
	calls := readOpsCalls(t, r, 7)
	if strings.Contains(calls, "/children") {
		t.Fatalf("empty content must NOT issue a body-write call, got:\n%s", calls)
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

func TestOpsSendMessage_ParsesMessageIDAndPassesReceiveIDType(t *testing.T) {
	r, _ := newOpsTestRunner(t, fakeOpsCLIScript)
	content := `{"text":"hi"}`
	res, err := r.SendMessage(context.Background(), 7, "open_id", "ou_abc", "text", content)
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if res.MessageID != "om_msg_1" {
		t.Fatalf("message_id mismatch: %q", res.MessageID)
	}
	calls := readOpsCalls(t, r, 7)
	if !strings.Contains(calls, "receive_id_type") || !strings.Contains(calls, "open_id") {
		t.Fatalf("receive_id_type must be passed as a query param, got:\n%s", calls)
	}
	if !strings.Contains(calls, "ou_abc") {
		t.Fatalf("receive_id must be passed in the body, got:\n%s", calls)
	}
}

func TestOpsSendMessage_BusinessError_WrapsLarkCallFailed(t *testing.T) {
	r, _ := newOpsTestRunner(t, fakeOpsErrorScript)
	_, err := r.SendMessage(context.Background(), 7, "open_id", "ou_abc", "text", `{"text":"x"}`)
	if !errors.Is(err, errno.ErrLarkCallFailed) {
		t.Fatalf("business error must wrap ErrLarkCallFailed, got %v", err)
	}
}

// --- ReadBitable ------------------------------------------------------------

func TestOpsReadBitable_ParsesRecordsAndPaging(t *testing.T) {
	r, _ := newOpsTestRunner(t, fakeOpsCLIScript)
	res, err := r.ReadBitable(context.Background(), 7, "bascABC", "tblXYZ", 20, "")
	if err != nil {
		t.Fatalf("ReadBitable: %v", err)
	}
	if len(res.Records) != 2 {
		t.Fatalf("expected 2 records, got %d", len(res.Records))
	}
	if res.Records[0].RecordID != "rec1" || res.Records[0].Fields["Name"] != "A" {
		t.Fatalf("record parse mismatch: %+v", res.Records[0])
	}
	if !res.HasMore || res.PageToken != "pt_next" || res.Total != 2 {
		t.Fatalf("paging parse mismatch: has_more=%t token=%q total=%d", res.HasMore, res.PageToken, res.Total)
	}
	calls := readOpsCalls(t, r, 7)
	if !strings.Contains(calls, "bascABC") || !strings.Contains(calls, "tblXYZ") {
		t.Fatalf("app_token/table_id must be in the path, got:\n%s", calls)
	}
	if !strings.Contains(calls, "page_size") {
		t.Fatalf("page_size must be passed as a query param, got:\n%s", calls)
	}
}

func TestOpsReadBitable_BusinessError_WrapsLarkCallFailed(t *testing.T) {
	r, _ := newOpsTestRunner(t, fakeOpsErrorScript)
	_, err := r.ReadBitable(context.Background(), 7, "basc", "tbl", 10, "")
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
