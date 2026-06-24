// Package feishu — ops_cli.go holds the PRODUCTION lark-cli-backed 飞书 ops
// (G2-authorize device-code redesign, 2026-06-24): create doc / send message /
// read bitable. They REPLACE the oapi-sdk-go implementation: each runs
// `lark-cli api ... --as user --json` with HOME pinned to the user's home, so
// lark-cli uses that user's auto-refreshed token (we never touch the token).
//
// We use the generic `lark-cli api` path (documented 飞书 REST endpoints) rather
// than the higher-level `docs/im/base +shortcut` verbs because it preserves the
// EXACT request shapes the SDK used (docx blocks, receive_id_type, bitable paging)
// and the same wire fields, so behaviour is unchanged — only the transport (lark-cli
// managing tokens) differs.
//
// lark-cli JSON envelope: {"ok":bool,"identity":...,"error":{...},"data":{...}}.
// On ok=true, `data` is the 飞书 response body (lark-cli unwraps the 飞书
// {code,msg,data} envelope). On ok=false we surface errno.ErrLarkCallFailed.
//
// Security: tool inputs (title/content/ids/text) are passed as --data/--params JSON
// (a single argv element), never shell-interpolated. No token / secret is handled
// or logged. NOT routed through aiservice (飞书 is an external business API).
package feishu

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"numind-server/internal/pkg/errno"
)

// opsTimeout bounds a single lark-cli ops API call.
const opsTimeout = 30 * time.Second

// docxTextBlockType is the 飞书 docx block_type for a plain text paragraph (2 =
// text). Used when appending the document body as a single text block.
const docxTextBlockType = 2

// opsEnvelope is the lark-cli `api` JSON envelope. data is the 飞书 response body
// (lark-cli already unwrapped the 飞书 code/msg/data layer) — kept raw so each op
// parses only the fields it needs.
type opsEnvelope struct {
	OK    bool            `json:"ok"`
	Error *larkCLIError   `json:"error"`
	Data  json.RawMessage `json:"data"`
}

// runOpsAPI runs `lark-cli api <method> <path> [--params ..] [--data ..] --as user
// --json` in userID's home and returns the unwrapped 飞书 `data` payload. On a
// lark-cli/飞书 error it returns a wrapped errno.ErrLarkCallFailed (the tool layer
// classifies it as a soft error).
func (r *LarkCLIRunner) runOpsAPI(ctx context.Context, userID uint, method, path, paramsJSON, dataJSON string) (json.RawMessage, error) {
	home := r.homeForUser(userID)
	args := []string{"api", method, path, "--as", "user", "--json"}
	if paramsJSON != "" {
		args = append(args, "--params", paramsJSON)
	}
	if dataJSON != "" {
		args = append(args, "--data", dataJSON)
	}

	raw, err := r.runCLI(ctx, home, opsTimeout, args...)
	if err != nil {
		return nil, err
	}
	var env opsEnvelope
	if jerr := json.Unmarshal(raw, &env); jerr != nil {
		return nil, fmt.Errorf("%w: parse lark-cli api output: %v", errno.ErrLarkCallFailed, jerr)
	}
	if !env.OK {
		return nil, fmt.Errorf("%w: 飞书 api %s %s: %s", errno.ErrLarkCallFailed, method, path, errMsg(env.Error))
	}
	return env.Data, nil
}

// --- CreateDoc --------------------------------------------------------------

// docCreateData is the relevant subset of POST /open-apis/docx/v1/documents data.
type docCreateData struct {
	Document struct {
		DocumentID string `json:"document_id"`
		Title      string `json:"title"`
	} `json:"document"`
}

// CreateDoc creates a docx document then (if contentMD is non-empty) writes it as a
// single text block under the document root. A content-write failure does NOT lose
// the created doc — it returns the DocResult alongside the error so the tool layer
// can report a partial success.
func (r *LarkCLIRunner) CreateDoc(ctx context.Context, userID uint, title, contentMD string) (*DocResult, error) {
	body, _ := json.Marshal(map[string]string{"title": title})
	data, err := r.runOpsAPI(ctx, userID, "POST", "/open-apis/docx/v1/documents", "", string(body))
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
	out := &DocResult{
		DocumentID: docID,
		Title:      title,
		URL:        "https://feishu.cn/docx/" + docID,
	}

	if contentMD != "" {
		if werr := r.appendDocText(ctx, userID, docID, contentMD); werr != nil {
			return out, werr // partial success: doc exists, content write failed
		}
	}
	return out, nil
}

// appendDocText writes a single text-paragraph block carrying content under the
// document root (the root block id equals the document id).
func (r *LarkCLIRunner) appendDocText(ctx context.Context, userID uint, docID, content string) error {
	// Mirror the SDK block shape: children=[{block_type:2,text:{elements:[{text_run:{content}}]}}].
	body := map[string]any{
		"index": 0,
		"children": []map[string]any{{
			"block_type": docxTextBlockType,
			"text": map[string]any{
				"elements": []map[string]any{{
					"text_run": map[string]any{"content": content},
				}},
			},
		}},
	}
	raw, _ := json.Marshal(body)
	path := "/open-apis/docx/v1/documents/" + docID + "/blocks/" + docID + "/children"
	if _, err := r.runOpsAPI(ctx, userID, "POST", path, "", string(raw)); err != nil {
		return err
	}
	return nil
}

// --- SendMessage ------------------------------------------------------------

// msgCreateData is the relevant subset of POST /open-apis/im/v1/messages data.
type msgCreateData struct {
	MessageID string `json:"message_id"`
}

// SendMessage sends an im message on behalf of the user. receiveIDType is passed as
// the query param; the body carries receive_id + msg_type + content.
func (r *LarkCLIRunner) SendMessage(ctx context.Context, userID uint, receiveIDType, receiveID, msgType, content string) (*MsgResult, error) {
	params, _ := json.Marshal(map[string]string{"receive_id_type": receiveIDType})
	body, _ := json.Marshal(map[string]string{
		"receive_id": receiveID,
		"msg_type":   msgType,
		"content":    content,
	})
	data, err := r.runOpsAPI(ctx, userID, "POST", "/open-apis/im/v1/messages", string(params), string(body))
	if err != nil {
		return nil, err
	}
	var mc msgCreateData
	if jerr := json.Unmarshal(data, &mc); jerr != nil {
		return nil, fmt.Errorf("%w: parse send-message response: %v", errno.ErrLarkCallFailed, jerr)
	}
	return &MsgResult{MessageID: mc.MessageID}, nil
}

// --- ReadBitable ------------------------------------------------------------

// bitableListData is the relevant subset of the bitable list-records response.
type bitableListData struct {
	HasMore   bool   `json:"has_more"`
	PageToken string `json:"page_token"`
	Total     int    `json:"total"`
	Items     []struct {
		RecordID string         `json:"record_id"`
		Fields   map[string]any `json:"fields"`
	} `json:"items"`
}

// ReadBitable lists a page of records from a bitable table (read-only).
func (r *LarkCLIRunner) ReadBitable(ctx context.Context, userID uint, appToken, tableID string, pageSize int, pageToken string) (*BitableResult, error) {
	params := map[string]any{"page_size": pageSize}
	if pageToken != "" {
		params["page_token"] = pageToken
	}
	paramsJSON, _ := json.Marshal(params)
	path := "/open-apis/bitable/v1/apps/" + appToken + "/tables/" + tableID + "/records"
	data, err := r.runOpsAPI(ctx, userID, "GET", path, string(paramsJSON), "")
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
