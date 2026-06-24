// Package feishu — ops_cli.go holds the PRODUCTION lark-cli-backed 飞书 ops
// (甲方案 G3-ops redesign, 2026-06-24): create doc / send message / read bitable.
// They REPLACE the oapi-sdk-go implementation AND the earlier generic `lark-cli api`
// transport: each op now runs the HIGHER-LEVEL lark-cli shortcut verb
// (`docs +create` / `im +messages-send` / `base +record-list`) with HOME pinned to
// the user's home, so lark-cli uses that user's auto-refreshed token (we never touch
// the token).
//
// Why the shortcut verbs (not `lark-cli api`): the shortcuts are the lark-cli's
// supported, version-matched surface — they own the request shaping (docx content
// import, receive-id flag selection, bitable paging) and emit a stable JSON envelope.
// Using them keeps this layer thin and insulates it from raw 飞书 REST endpoint drift.
//
// Verified shortcut shapes (本机 lark-cli 1.0.56, `lark-cli <svc> +<verb> --help` /
// `lark-cli skills read ...`, 2026-06-24):
//
//   - docs +create --doc-format markdown --content <md> --as user --json
//     title is the leading `# heading` of the markdown content (the shortcut rejects a
//     separate --title flag); body follows. Output:
//     {"ok":true,"data":{"document":{"document_id":"...","url":"...","revision_id":N}}}.
//     One call imports the whole doc — no separate block-append step.
//   - im +messages-send (--user-id ou_x | --chat-id oc_x) --text <t> --as user --json
//     --user-id (open_id) XOR --chat-id are the only recipient flags; --text auto-wraps
//     as a text message. Output: {"ok":true,"data":{"message_id":"om_..."}}.
//   - base +record-list --base-token <t> --table-id <id> --limit N --offset M
//     --format json --as user
//     offset/limit paging (NOT a page_token cursor). Output data carries
//     items[].record_id/fields, has_more, total (and page_token when present).
//
// lark-cli JSON envelope: {"ok":bool,"identity":...,"error":{...},"data":{...}}.
// On ok=true, `data` is the 飞书 response body (lark-cli unwraps the 飞书
// {code,msg,data} envelope). On ok=false we surface errno.ErrLarkCallFailed.
//
// Security: tool inputs (title/content/ids/text) are passed as single argv elements
// (--content/--text/--base-token/...), never shell-interpolated. No token / secret is
// handled or logged. NOT routed through aiservice (飞书 is an external business API).
package feishu

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"numind-server/internal/pkg/errno"
)

// opsTimeout bounds a single lark-cli ops shortcut call.
const opsTimeout = 30 * time.Second

// opsEnvelope is the lark-cli shortcut JSON envelope. data is the 飞书 response body
// (lark-cli already unwrapped the 飞书 code/msg/data layer) — kept raw so each op
// parses only the fields it needs.
type opsEnvelope struct {
	OK    bool            `json:"ok"`
	Error *larkCLIError   `json:"error"`
	Data  json.RawMessage `json:"data"`
}

// runOpsShortcut runs a lark-cli shortcut verb in userID's home and returns the
// unwrapped 飞书 `data` payload. On a lark-cli/飞书 error it returns a wrapped
// errno.ErrLarkCallFailed (the tool layer classifies it as a soft error). args is the
// full shortcut argv (e.g. {"docs","+create","--content",md,"--as","user","--json"}).
func (r *LarkCLIRunner) runOpsShortcut(ctx context.Context, userID uint, args ...string) (json.RawMessage, error) {
	home := r.homeForUser(userID)
	raw, err := r.runCLI(ctx, home, opsTimeout, args...)
	if err != nil {
		return nil, err
	}
	var env opsEnvelope
	if jerr := json.Unmarshal(raw, &env); jerr != nil {
		return nil, fmt.Errorf("%w: parse lark-cli %s output: %v", errno.ErrLarkCallFailed, shortcutLabel(args), jerr)
	}
	if !env.OK {
		return nil, fmt.Errorf("%w: 飞书 %s: %s", errno.ErrLarkCallFailed, shortcutLabel(args), errMsg(env.Error))
	}
	return env.Data, nil
}

// shortcutLabel renders a short "<svc> <verb>" label from the argv for diagnostics
// (no user data — only the leading service + verb tokens).
func shortcutLabel(args []string) string {
	switch len(args) {
	case 0:
		return "lark-cli"
	case 1:
		return args[0]
	default:
		return args[0] + " " + args[1]
	}
}

// --- CreateDoc --------------------------------------------------------------

// docCreateData is the relevant subset of `docs +create` data: the created
// document's id + best-effort web URL (the shortcut returns it directly).
type docCreateData struct {
	Document struct {
		DocumentID string `json:"document_id"`
		Title      string `json:"title"`
		URL        string `json:"url"`
	} `json:"document"`
}

// CreateDoc creates a docx document via `docs +create` (markdown import). The title
// is embedded as the leading `# heading` of the content (the shortcut extracts it);
// contentMD (when non-empty) follows as the body. A single shortcut call imports the
// whole document, so there is no separate content-write step / partial-success path.
func (r *LarkCLIRunner) CreateDoc(ctx context.Context, userID uint, title, contentMD string) (*DocResult, error) {
	content := buildDocMarkdown(title, contentMD)
	data, err := r.runOpsShortcut(ctx, userID,
		"docs", "+create",
		"--doc-format", "markdown",
		"--content", content,
		"--as", "user", "--json")
	if err != nil {
		return nil, err
	}
	var dc docCreateData
	if jerr := json.Unmarshal(data, &dc); jerr != nil {
		return nil, fmt.Errorf("%w: parse create-doc response: %v", errno.ErrLarkCallFailed, jerr)
	}
	docID := dc.Document.DocumentID
	if docID == "" {
		return nil, fmt.Errorf("%w: create doc returned no document_id", errno.ErrLarkCallFailed)
	}
	url := dc.Document.URL
	if url == "" {
		url = "https://feishu.cn/docx/" + docID // fallback if the shortcut omits url
	}
	docTitle := dc.Document.Title
	if docTitle == "" {
		docTitle = title
	}
	return &DocResult{DocumentID: docID, Title: docTitle, URL: url}, nil
}

// buildDocMarkdown composes the `docs +create --doc-format markdown` content: the
// title as the leading single level-1 heading, the body below. The shortcut takes the
// leading `# heading` as the document title (do NOT repeat it in the body). An empty
// body yields a title-only document.
func buildDocMarkdown(title, contentMD string) string {
	t := strings.TrimSpace(title)
	body := strings.TrimSpace(contentMD)
	if body == "" {
		return "# " + t
	}
	return "# " + t + "\n\n" + body
}

// --- SendMessage ------------------------------------------------------------

// msgCreateData is the relevant subset of `im +messages-send` data.
type msgCreateData struct {
	MessageID string `json:"message_id"`
}

// SendMessage sends an im message via `im +messages-send`. The shortcut accepts
// exactly one recipient flag: --chat-id (oc_xxx) or --user-id (open_id). We map the
// receiveIDType: "chat_id" → --chat-id, anything else (open_id / user_id / union_id /
// email — the tool defaults to open_id) → --user-id. The text is extracted from the
// content JSON ({"text":"..."}) and passed via --text (the shortcut auto-wraps it);
// msgType is ignored (the shortcut infers "text" from --text).
func (r *LarkCLIRunner) SendMessage(ctx context.Context, userID uint, receiveIDType, receiveID, msgType, content string) (*MsgResult, error) {
	text, err := extractMessageText(content)
	if err != nil {
		return nil, err
	}
	recipientFlag := "--user-id"
	if receiveIDType == "chat_id" {
		recipientFlag = "--chat-id"
	}
	data, err := r.runOpsShortcut(ctx, userID,
		"im", "+messages-send",
		recipientFlag, receiveID,
		"--text", text,
		"--as", "user", "--json")
	if err != nil {
		return nil, err
	}
	var mc msgCreateData
	if jerr := json.Unmarshal(data, &mc); jerr != nil {
		return nil, fmt.Errorf("%w: parse send-message response: %v", errno.ErrLarkCallFailed, jerr)
	}
	return &MsgResult{MessageID: mc.MessageID}, nil
}

// extractMessageText pulls the plain text out of the tool's content JSON
// ({"text":"..."}). The tool layer always builds this shape (and already rejects an
// empty text), so the happy path returns wrap.Text. Defensive fallbacks:
//   - content IS the wrapper JSON but text is empty → error (don't send a blank msg).
//   - content is NOT the wrapper JSON (some other payload) → use it verbatim so a
//     message is still sent, but reject an all-whitespace payload.
func extractMessageText(content string) (string, error) {
	var wrap struct {
		Text *string `json:"text"`
	}
	if jerr := json.Unmarshal([]byte(content), &wrap); jerr == nil && wrap.Text != nil {
		// content was the {"text":...} wrapper. Honour it (error if empty).
		if strings.TrimSpace(*wrap.Text) == "" {
			return "", fmt.Errorf("%w: empty message text", errno.ErrLarkCallFailed)
		}
		return *wrap.Text, nil
	}
	// Not the wrapper shape — use the raw content, but reject a blank payload.
	if strings.TrimSpace(content) == "" {
		return "", fmt.Errorf("%w: empty message content", errno.ErrLarkCallFailed)
	}
	return content, nil
}

// --- ReadBitable ------------------------------------------------------------

// bitableListData is the relevant subset of the `base +record-list --format json`
// response data. has_more / page_token are surfaced when the shortcut includes them
// (it wraps the bitable list API, which returns them).
type bitableListData struct {
	HasMore   bool   `json:"has_more"`
	PageToken string `json:"page_token"`
	Total     int    `json:"total"`
	Items     []struct {
		RecordID string         `json:"record_id"`
		Fields   map[string]any `json:"fields"`
	} `json:"items"`
}

// ReadBitable lists a page of records via `base +record-list` (read-only). The
// shortcut pages by --offset/--limit (NOT a page_token cursor): pageOffset is the
// number of records to skip, pageSize the page length. pageOffset 0 = first page.
func (r *LarkCLIRunner) ReadBitable(ctx context.Context, userID uint, appToken, tableID string, pageSize, pageOffset int) (*BitableResult, error) {
	args := []string{
		"base", "+record-list",
		"--base-token", appToken,
		"--table-id", tableID,
		"--limit", strconv.Itoa(pageSize),
		"--format", "json", "--as", "user",
	}
	if pageOffset > 0 {
		args = append(args, "--offset", strconv.Itoa(pageOffset))
	}
	data, err := r.runOpsShortcut(ctx, userID, args...)
	if err != nil {
		return nil, err
	}
	var bd bitableListData
	if jerr := json.Unmarshal(data, &bd); jerr != nil {
		return nil, fmt.Errorf("%w: parse bitable response: %v", errno.ErrLarkCallFailed, jerr)
	}
	out := &BitableResult{
		HasMore:   bd.HasMore,
		PageToken: bd.PageToken,
		Total:     bd.Total,
	}
	for _, it := range bd.Items {
		out.Records = append(out.Records, BitableRecord{RecordID: it.RecordID, Fields: it.Fields})
	}
	return out, nil
}

// compile-time guard: LarkCLIRunner satisfies opsRunner.
var _ opsRunner = (*LarkCLIRunner)(nil)
