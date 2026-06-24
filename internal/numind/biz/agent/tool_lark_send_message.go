package agent

// tool_lark_send_message.go — the lark_send_message agent tool (feishu-integration
// T10). Sends a 飞书 (Lark) im message on behalf of the run initiator. Scope:
// im:message.
//
// Failure policy (design.md §8): EVERY failure path returns a SOFT tool result,
// never a Go error (a Go error kills the agent run via Eino NodeRunError).

import (
	"context"
	"encoding/json"
	"strings"

	"numind-server/internal/numind/biz/feishu"
	"numind-server/internal/pkg/middleware"
)

// larkSendMessageTool implements FullTool for lark_send_message.
type larkSendMessageTool struct {
	BaseTool
	provider feishu.LarkAPIProvider // nil → 飞书 integration off (soft error at Execute)
}

var _ FullTool = (*larkSendMessageTool)(nil)

func (t *larkSendMessageTool) Name() string { return "lark_send_message" }
func (t *larkSendMessageTool) Description() string {
	return "Send a 飞书 (Lark) message to a user or chat on behalf of the connected user. " +
		"Requires the user to have connected 飞书 (scope im:message). " +
		"Input: { receive_id: string, receive_id_type?: \"open_id\" (ou_xxx) | \"chat_id\" (oc_xxx), text: string }. " +
		"receive_id_type defaults to open_id. Only open_id and chat_id are supported. " +
		"Returns: { message_id }."
}
func (t *larkSendMessageTool) UserFacingName() string { return "发送飞书消息" }
func (t *larkSendMessageTool) NarrationVerb() string  { return "发送飞书消息" }

// Sends a message → not read-only / not concurrency-safe.
func (t *larkSendMessageTool) IsReadOnly() bool                   { return false }
func (t *larkSendMessageTool) IsConcurrencySafe(_ ToolInput) bool { return false }

func (t *larkSendMessageTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"receive_id":      {"type": "string", "description": "The recipient id (interpretation depends on receive_id_type)."},
			"receive_id_type": {"type": "string", "enum": ["open_id", "chat_id"], "description": "How to interpret receive_id: open_id (ou_xxx) or chat_id (oc_xxx). Defaults to open_id."},
			"text":            {"type": "string", "description": "The plain text message to send."}
		},
		"required": ["receive_id", "text"]
	}`)
}

type larkSendMessageInput struct {
	ReceiveID     string `json:"receive_id"`
	ReceiveIDType string `json:"receive_id_type,omitempty"`
	Text          string `json:"text"`
}

type larkSendMessageOutput struct {
	MessageID string `json:"message_id,omitempty"`
	Error     string `json:"error,omitempty"`
}

// validReceiveIDTypes is the set of receive_id_type values the lark-cli ops shortcut
// surface supports. `im +messages-send` accepts exactly two recipient flags
// (--user-id for open_id, --chat-id for chat_id); user_id/union_id/email are NOT
// supported (lark-cli validates --user-id as an open_id `ou_` format and rejects the
// rest), so they are deliberately excluded from the enum.
var validReceiveIDTypes = map[string]bool{
	"open_id": true,
	"chat_id": true,
}

func (t *larkSendMessageTool) Execute(ctx context.Context, input ToolInput) (ToolResult, error) {
	const label = "发送飞书消息"

	var in larkSendMessageInput
	if err := json.Unmarshal(input, &in); err != nil {
		return larkSoftError("lark_send_message 输入格式错误：%s", err.Error())
	}
	if strings.TrimSpace(in.ReceiveID) == "" {
		return larkSoftError("lark_send_message 需要 receive_id 参数（接收者 id 不能为空）。")
	}
	if strings.TrimSpace(in.Text) == "" {
		return larkSoftError("lark_send_message 需要 text 参数（消息内容不能为空）。")
	}

	receiveIDType := in.ReceiveIDType
	if receiveIDType == "" {
		receiveIDType = "open_id" // 飞书 default
	}
	if !validReceiveIDTypes[receiveIDType] {
		return larkSoftError("lark_send_message 的 receive_id_type=%q 不合法，应为 open_id（ou_xxx）或 chat_id（oc_xxx）之一。", receiveIDType)
	}

	// 飞书 text messages take JSON content {"text":"..."}.
	contentJSON, err := json.Marshal(map[string]string{"text": in.Text})
	if err != nil {
		return larkSoftError("lark_send_message 构造消息内容失败：%s", err.Error())
	}

	userID, _ := middleware.UserIDFromCtx(ctx)
	endSpan := larkStartSpan(ctx, "send_message", userID, map[string]any{
		"receive_id_type": receiveIDType,
		// receive_id and text are user data; recorded for observability (no secrets).
		"receive_id": in.ReceiveID,
	})

	api, soft, proceed := larkAPIFor(ctx, t.provider, label)
	if !proceed {
		endSpan(map[string]any{"outcome": "precondition_failed"}, "precondition failed")
		return soft, nil
	}

	res, err := api.SendMessage(ctx, receiveIDType, in.ReceiveID, "text", string(contentJSON))
	if err != nil {
		endSpan(map[string]any{"outcome": "error"}, err.Error())
		return larkSoftErrorForAPIErr(label, err)
	}

	endSpan(map[string]any{"message_id": res.MessageID, "outcome": "ok"}, "")
	out, _ := json.Marshal(larkSendMessageOutput{MessageID: res.MessageID})
	return ToolResult(out), nil
}
