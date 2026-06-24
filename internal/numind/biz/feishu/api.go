// Package feishu — api.go is the narrow 飞书 (Lark) business API the agent tools
// call. Under the 甲方案 G3-ops redesign it is backed by lark-cli (not oapi-sdk-go):
// each method runs a higher-level lark-cli shortcut verb (`docs +create` /
// `im +messages-send` / `base +record-list`) with HOME pinned to the user's home, so
// lark-cli uses that user's auto-refreshed token.
//
//	client.APIFor(ctx, userID)  ── gates connected+authorized, returns a LarkAPI
//	                               bound to userID (token managed by lark-cli)
//	LarkAPI.CreateDoc/SendMessage/ReadBitable ── run one lark-cli ops command
//
// Why a narrow interface: it keeps the agent-tool layer free of lark-cli specifics
// and unit-testable with a hand-written fake LarkAPI (no live 飞书 / no real CLI).
// Every method returns a wrapped errno.ErrLarkCallFailed on any failure so the tool
// layer maps it to a SOFT tool result (never a Go error that kills the agent run).
//
// Observability: spans are recorded by the TOOL layer (design.md §9), not here.
//
// NOT routed through aiservice: 飞书 is an external business API.
package feishu

import (
	"context"

	"numind-server/internal/pkg/errno"
)

// DocResult is the outcome of CreateDoc: the new document's id and a best-effort
// web URL (constructed from the id; 飞书 docs live at /docx/<id>).
type DocResult struct {
	DocumentID string
	Title      string
	URL        string
}

// MsgResult is the outcome of SendMessage: the 飞书 message id.
type MsgResult struct {
	MessageID string
}

// BitableRecord is one bitable row: its record id + raw field map (飞书 returns
// heterogeneous field value shapes; the tool serialises them verbatim for the LLM).
type BitableRecord struct {
	RecordID string         `json:"record_id"`
	Fields   map[string]any `json:"fields"`
}

// BitableResult is the outcome of ReadBitable: the page of records + paging info.
// PageToken is surfaced when the `base +record-list` shortcut includes it in its
// response (it wraps the bitable list API), but the request pages by offset/limit
// (the shortcut's model), so callers advance pages by increasing the offset, not by
// passing PageToken back.
type BitableResult struct {
	Records   []BitableRecord
	HasMore   bool
	PageToken string
	Total     int
}

// LarkAPI is the narrow 飞书 surface the agent tools depend on. Each method acts
// on behalf of the single user the implementation was built for (the user is bound
// at construction via APIFor; lark-cli holds that user's token). Implementations
// MUST return a wrapped errno.ErrLarkCallFailed on any failure so the tool layer
// can classify it as a soft error.
type LarkAPI interface {
	// CreateDoc creates a new 飞书 docx document with the given title and, when
	// contentMD is non-empty, writes it as the document body.
	CreateDoc(ctx context.Context, title, contentMD string) (*DocResult, error)
	// SendMessage sends an im message. receiveIDType is one of the 飞书 enums
	// (open_id / user_id / union_id / email / chat_id); msgType is e.g. "text"
	// (content is then the JSON `{"text":"..."}`).
	SendMessage(ctx context.Context, receiveIDType, receiveID, msgType, content string) (*MsgResult, error)
	// ReadBitable lists records of a bitable table (read-only). pageSize is clamped
	// by the caller; pageOffset is the number of records to skip (0 = first page) —
	// the lark-cli `base +record-list` shortcut pages by offset/limit, not a cursor.
	ReadBitable(ctx context.Context, appToken, tableID string, pageSize, pageOffset int) (*BitableResult, error)
}

// LarkAPIProvider builds a per-user LarkAPI. *Client satisfies it (APIFor below).
// The tool layer holds a LarkAPIProvider, not a concrete *Client, so tests inject
// a fake that returns a fake LarkAPI (and can also return ErrLarkNotConnected /
// ErrLarkReauthRequired to exercise the soft-error paths).
type LarkAPIProvider interface {
	APIFor(ctx context.Context, userID uint) (LarkAPI, error)
}

// APIFor builds a LarkAPI for userID after gating connected+authorized. The gate
// errors (ErrLarkNotConnected / ErrLarkReauthRequired) propagate unchanged so the
// tool layer maps them to the right soft-error prompt.
func (c *Client) APIFor(ctx context.Context, userID uint) (LarkAPI, error) {
	if err := c.gate(ctx, userID); err != nil {
		return nil, err
	}
	return &cliLarkAPI{ops: c.ops, userID: userID}, nil
}

// compile-time guard: *Client satisfies LarkAPIProvider.
var _ LarkAPIProvider = (*Client)(nil)

// cliLarkAPI is the production LarkAPI backed by lark-cli. It binds the userID +
// the ops runner; each call delegates to the runner, which runs lark-cli with
// HOME=userID's home (the token is managed by lark-cli, never handled here).
type cliLarkAPI struct {
	ops    opsRunner
	userID uint
}

var _ LarkAPI = (*cliLarkAPI)(nil)

func (a *cliLarkAPI) CreateDoc(ctx context.Context, title, contentMD string) (*DocResult, error) {
	res, err := a.ops.CreateDoc(ctx, a.userID, title, contentMD)
	if err != nil {
		return res, err // res may be non-nil on a partial (doc created, content failed)
	}
	if res == nil || res.DocumentID == "" {
		return nil, errno.ErrLarkCallFailed.SetMessage("飞书创建文档未返回 document_id")
	}
	return res, nil
}

func (a *cliLarkAPI) SendMessage(ctx context.Context, receiveIDType, receiveID, msgType, content string) (*MsgResult, error) {
	return a.ops.SendMessage(ctx, a.userID, receiveIDType, receiveID, msgType, content)
}

func (a *cliLarkAPI) ReadBitable(ctx context.Context, appToken, tableID string, pageSize, pageOffset int) (*BitableResult, error) {
	return a.ops.ReadBitable(ctx, a.userID, appToken, tableID, pageSize, pageOffset)
}
