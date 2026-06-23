// Package feishu — api.go is the narrow, SDK-backed 飞书 (Lark) business API the
// agent tools (feishu-integration T10) call. It sits one layer ABOVE client.go:
//
//	client.For(ctx, userID)  ── builds an authenticated *LarkClient (token
//	                            decrypt + expiry refresh, design.md §7)
//	APIFor(ctx, userID)      ── wraps that *LarkClient in a LarkAPI whose three
//	                            methods (CreateDoc / SendMessage / ReadBitable)
//	                            map to the oapi-sdk-go builder calls.
//
// Why a narrow interface instead of letting the tools touch the SDK directly:
//   - The SDK's request/response types are deeply nested builder chains. A small
//     domain interface keeps the agent-tool layer free of SDK imports and makes
//     the tools unit-testable with a hand-written fake LarkAPI (no live 飞书).
//   - Every method returns the 飞书 business code as errno.ErrLarkCallFailed
//     (wrapped) on a non-zero code, so the tool layer maps ANY failure to a SOFT
//     tool result (never a Go error that kills the agent run — design.md §8).
//
// Observability: spans are recorded by the TOOL layer (design.md §9), not here —
// this layer is a thin SDK adapter and stays trace-agnostic.
//
// NOT routed through aiservice: 飞书 is an external business API.
package feishu

import (
	"context"
	"fmt"

	"numind-server/internal/pkg/errno"

	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	larkbitable "github.com/larksuite/oapi-sdk-go/v3/service/bitable/v1"
	larkdocx "github.com/larksuite/oapi-sdk-go/v3/service/docx/v1"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

// docxTextBlockType is the 飞书 docx block_type for a plain text paragraph. The
// SDK does not export a named constant; 2 is the documented "text" block type.
const docxTextBlockType = 2

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
type BitableResult struct {
	Records   []BitableRecord
	HasMore   bool
	PageToken string
	Total     int
}

// LarkAPI is the narrow 飞书 surface the agent tools depend on. Each method acts
// on behalf of the single user the implementation was built for (the user access
// token is bound at construction via APIFor). Implementations MUST return a
// wrapped errno.ErrLarkCallFailed on any 飞书 business-code or transport failure
// so the tool layer can classify it as a soft error.
type LarkAPI interface {
	// CreateDoc creates a new 飞书 docx document with the given title and, when
	// contentMD is non-empty, appends it as a single text block under the root.
	CreateDoc(ctx context.Context, title, contentMD string) (*DocResult, error)
	// SendMessage sends an im message. receiveIDType is one of the 飞书 enums
	// (open_id / user_id / union_id / email / chat_id); msgType is e.g. "text"
	// (content is then the JSON `{"text":"..."}`).
	SendMessage(ctx context.Context, receiveIDType, receiveID, msgType, content string) (*MsgResult, error)
	// ReadBitable lists records of a bitable table (read-only). pageSize is clamped
	// by the caller; pageToken empty = first page.
	ReadBitable(ctx context.Context, appToken, tableID string, pageSize int, pageToken string) (*BitableResult, error)
}

// LarkAPIProvider builds a per-user LarkAPI. *Client satisfies it (APIFor below).
// The tool layer holds a LarkAPIProvider, not a concrete *Client, so tests inject
// a fake that returns a fake LarkAPI (and can also return ErrLarkNotConnected /
// ErrLarkReauthRequired to exercise the soft-error paths).
type LarkAPIProvider interface {
	APIFor(ctx context.Context, userID uint) (LarkAPI, error)
}

// APIFor builds an authenticated LarkAPI for userID. It delegates to Client.For
// (token decrypt + expiry refresh) and wraps the resulting *LarkClient. Errors
// from For (ErrLarkNotConnected / ErrLarkReauthRequired) propagate unchanged so
// the tool layer maps them to the right soft-error prompt.
func (c *Client) APIFor(ctx context.Context, userID uint) (LarkAPI, error) {
	lc, err := c.For(ctx, userID)
	if err != nil {
		return nil, err
	}
	return &sdkLarkAPI{lc: lc}, nil
}

// compile-time guard: *Client satisfies LarkAPIProvider.
var _ LarkAPIProvider = (*Client)(nil)

// sdkLarkAPI is the production LarkAPI over oapi-sdk-go. It holds a built
// *LarkClient and passes the user access token per request via
// larkcore.WithUserAccessToken (the token is plaintext by SDK necessity; it is
// NEVER logged).
type sdkLarkAPI struct {
	lc *LarkClient
}

var _ LarkAPI = (*sdkLarkAPI)(nil)

// opt returns the per-request option binding the user access token.
func (a *sdkLarkAPI) opt() larkcore.RequestOptionFunc {
	return larkcore.WithUserAccessToken(a.lc.UserAccessToken)
}

// CreateDoc creates a docx document then (if contentMD is non-empty) appends the
// content as a single text block under the document root.
func (a *sdkLarkAPI) CreateDoc(ctx context.Context, title, contentMD string) (*DocResult, error) {
	req := larkdocx.NewCreateDocumentReqBuilder().
		Body(larkdocx.NewCreateDocumentReqBodyBuilder().
			Title(title).
			Build()).
		Build()

	resp, err := a.lc.API.Docx.Document.Create(ctx, req, a.opt())
	if err != nil {
		return nil, fmt.Errorf("%w: create doc: %v", errno.ErrLarkCallFailed, err)
	}
	if !resp.Success() {
		return nil, fmt.Errorf("%w: create doc 飞书 code %d (%s)", errno.ErrLarkCallFailed, resp.Code, resp.Msg)
	}
	if resp.Data == nil || resp.Data.Document == nil || resp.Data.Document.DocumentId == nil {
		return nil, fmt.Errorf("%w: create doc returned no document id", errno.ErrLarkCallFailed)
	}

	docID := *resp.Data.Document.DocumentId
	out := &DocResult{
		DocumentID: docID,
		Title:      title,
		URL:        "https://feishu.cn/docx/" + docID,
	}

	// Append content as a text block. A write failure must NOT lose the doc that
	// was already created — surface it wrapped so the tool layer can report a
	// partial success ("doc created, content write failed") via soft error.
	if contentMD != "" {
		if werr := a.appendText(ctx, docID, contentMD); werr != nil {
			return out, werr
		}
	}
	return out, nil
}

// appendText writes a single text-paragraph block carrying content under the
// document root (the root block id equals the document id).
func (a *sdkLarkAPI) appendText(ctx context.Context, docID, content string) error {
	block := larkdocx.NewBlockBuilder().
		BlockType(docxTextBlockType).
		Text(larkdocx.NewTextBuilder().
			Elements([]*larkdocx.TextElement{
				larkdocx.NewTextElementBuilder().
					TextRun(larkdocx.NewTextRunBuilder().Content(content).Build()).
					Build(),
			}).
			Build()).
		Build()

	req := larkdocx.NewCreateDocumentBlockChildrenReqBuilder().
		DocumentId(docID).
		BlockId(docID).
		Body(larkdocx.NewCreateDocumentBlockChildrenReqBodyBuilder().
			Children([]*larkdocx.Block{block}).
			Index(0).
			Build()).
		Build()

	resp, err := a.lc.API.Docx.DocumentBlockChildren.Create(ctx, req, a.opt())
	if err != nil {
		return fmt.Errorf("%w: write doc content: %v", errno.ErrLarkCallFailed, err)
	}
	if !resp.Success() {
		return fmt.Errorf("%w: write doc content 飞书 code %d (%s)", errno.ErrLarkCallFailed, resp.Code, resp.Msg)
	}
	return nil
}

// SendMessage sends an im message on behalf of the user.
func (a *sdkLarkAPI) SendMessage(ctx context.Context, receiveIDType, receiveID, msgType, content string) (*MsgResult, error) {
	req := larkim.NewCreateMessageReqBuilder().
		ReceiveIdType(receiveIDType).
		Body(larkim.NewCreateMessageReqBodyBuilder().
			ReceiveId(receiveID).
			MsgType(msgType).
			Content(content).
			Build()).
		Build()

	resp, err := a.lc.API.Im.Message.Create(ctx, req, a.opt())
	if err != nil {
		return nil, fmt.Errorf("%w: send message: %v", errno.ErrLarkCallFailed, err)
	}
	if !resp.Success() {
		return nil, fmt.Errorf("%w: send message 飞书 code %d (%s)", errno.ErrLarkCallFailed, resp.Code, resp.Msg)
	}
	out := &MsgResult{}
	if resp.Data != nil && resp.Data.MessageId != nil {
		out.MessageID = *resp.Data.MessageId
	}
	return out, nil
}

// ReadBitable lists a page of records from a bitable table (read-only).
func (a *sdkLarkAPI) ReadBitable(ctx context.Context, appToken, tableID string, pageSize int, pageToken string) (*BitableResult, error) {
	builder := larkbitable.NewListAppTableRecordReqBuilder().
		AppToken(appToken).
		TableId(tableID).
		PageSize(pageSize)
	if pageToken != "" {
		builder = builder.PageToken(pageToken)
	}

	resp, err := a.lc.API.Bitable.V1.AppTableRecord.List(ctx, builder.Build(), a.opt())
	if err != nil {
		return nil, fmt.Errorf("%w: read bitable: %v", errno.ErrLarkCallFailed, err)
	}
	if !resp.Success() {
		return nil, fmt.Errorf("%w: read bitable 飞书 code %d (%s)", errno.ErrLarkCallFailed, resp.Code, resp.Msg)
	}

	out := &BitableResult{}
	if resp.Data != nil {
		if resp.Data.HasMore != nil {
			out.HasMore = *resp.Data.HasMore
		}
		if resp.Data.PageToken != nil {
			out.PageToken = *resp.Data.PageToken
		}
		if resp.Data.Total != nil {
			out.Total = *resp.Data.Total
		}
		for _, item := range resp.Data.Items {
			if item == nil {
				continue
			}
			rec := BitableRecord{Fields: item.Fields}
			if item.RecordId != nil {
				rec.RecordID = *item.RecordId
			}
			out.Records = append(out.Records, rec)
		}
	}
	return out, nil
}
