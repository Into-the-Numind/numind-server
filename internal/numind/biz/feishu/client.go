// Package feishu — client.go builds a per-user 飞书 (Lark) ops accessor backed by
// lark-cli (G2-authorize device-code redesign, 2026-06-24). It REPLACES the old
// oapi-sdk-go client: there is no token decrypt / expiry / refresh here anymore —
// lark-cli holds the user_access_token inside the user's persistent home and
// auto-refreshes it. We only:
//
//   - check the DB row reports the user is connected (else ErrLarkNotConnected),
//   - confirm lark-cli still has a usable authorization (else ErrLarkReauthRequired),
//   - hand back a LarkAPI that runs `lark-cli <docs|im|base>` (HOME=user home).
//
// NOT routed through aiservice: 飞书 is an external business API, not an LLM
// gateway. Every lark-cli ops invocation runs with HOME pinned to the user's home
// so it uses THAT user's token.
//
// Security: no token / app_secret ever enters this layer. lark-cli reads the token
// from the home; we never decrypt or pass it. Tool inputs (titles, ids, message
// text) are passed as single argv elements (never shell-interpolated).
package feishu

import (
	"context"
	"errors"
	"fmt"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/errno"

	"gorm.io/gorm"
)

// ProviderLark is the third-party provider key for 飞书 in
// user_third_party_account (design.md §3).
const ProviderLark = "lark"

// opsRunner is the per-user lark-cli ops seam the LarkAPI delegates to. The
// production *LarkCLIRunner satisfies it; tests inject a fake. All methods run
// lark-cli with HOME pinned to userID's persistent home, so they act with that
// user's token.
type opsRunner interface {
	CreateDoc(ctx context.Context, userID uint, title, contentMD string) (*DocResult, error)
	SendMessage(ctx context.Context, userID uint, receiveIDType, receiveID, msgType, content string) (*MsgResult, error)
	ReadBitable(ctx context.Context, userID uint, appToken, tableID string, pageSize int, pageToken string) (*BitableResult, error)
	// AuthStatus reports whether userID's home holds a usable authorization
	// (the device-code authRunner method, reused here as a reauth gate).
	AuthStatus(ctx context.Context, userID uint) (connected bool, err error)
}

// Client builds per-user LarkAPI accessors backed by lark-cli. Safe for concurrent
// use: it holds only immutable dependencies; per-user state lives in the DB row +
// the user's lark-cli home.
type Client struct {
	store store.IThirdPartyAccountStore
	ops   opsRunner

	// isNotFound classifies a store Get error as "no row" (→ ErrLarkNotConnected).
	// Defaults to gorm.ErrRecordNotFound matching; tests override for fake stores.
	isNotFound func(error) bool
}

// NewClient wires the lark-cli-backed client. All dependencies are required.
func NewClient(s store.IThirdPartyAccountStore, ops opsRunner) (*Client, error) {
	if s == nil {
		return nil, errors.New("feishu: nil store for client")
	}
	if ops == nil {
		return nil, errors.New("feishu: nil ops runner for client")
	}
	return &Client{
		store:      s,
		ops:        ops,
		isNotFound: func(err error) bool { return errors.Is(err, gorm.ErrRecordNotFound) },
	}, nil
}

// gate verifies userID is connected (DB) and still authorized (lark-cli) before a
// tool acts on their behalf. It returns:
//   - ErrLarkNotConnected when there is no row OR the row is not marked connected,
//   - ErrLarkReauthRequired when lark-cli no longer holds a usable authorization
//     (token revoked / home wiped) — the tool layer turns this into a soft "please
//     reconnect" message, never a hard run-killing error.
func (c *Client) gate(ctx context.Context, userID uint) error {
	acc, err := c.store.Get(ctx, userID, ProviderLark)
	if err != nil {
		if c.isNotFound(err) {
			return fmt.Errorf("%w: user %d has no 飞书 connection", errno.ErrLarkNotConnected, userID)
		}
		return fmt.Errorf("feishu: load account (user %d): %w", userID, err)
	}
	if !acc.Connected {
		return fmt.Errorf("%w: user %d 飞书 connection not completed", errno.ErrLarkNotConnected, userID)
	}
	// Confirm lark-cli still has a usable authorization in the home. A transport
	// failure here is treated as reauth-required (fail closed → soft reconnect
	// prompt) rather than guessing the token is fine.
	ok, serr := c.ops.AuthStatus(ctx, userID)
	if serr != nil {
		return fmt.Errorf("%w: 飞书 authorization check failed (user %d): %v", errno.ErrLarkReauthRequired, userID, serr)
	}
	if !ok {
		return fmt.Errorf("%w: 飞书 authorization no longer valid (user %d)", errno.ErrLarkReauthRequired, userID)
	}
	return nil
}
