package feishu

import (
	"context"
	"errors"
	"testing"
	"time"

	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/model"

	"gorm.io/gorm"
)

// --- test doubles for the client (device-code gate) -------------------------

// fakeAccountStore is a single-row in-memory IThirdPartyAccountStore for the
// client gate tests. Under device-code only the connected metadata matters here.
type fakeAccountStore struct {
	acc *model.UserThirdPartyAccount
}

func (f *fakeAccountStore) Get(_ context.Context, _ uint, _ string) (*model.UserThirdPartyAccount, error) {
	if f.acc == nil {
		return nil, gorm.ErrRecordNotFound
	}
	cp := *f.acc
	return &cp, nil
}
func (f *fakeAccountStore) Upsert(_ context.Context, acc *model.UserThirdPartyAccount) error {
	cp := *acc
	f.acc = &cp
	return nil
}
func (f *fakeAccountStore) EnsurePlaceholder(_ context.Context, userID uint, provider string) (*model.UserThirdPartyAccount, error) {
	if f.acc == nil {
		f.acc = &model.UserThirdPartyAccount{UserID: userID, Provider: provider, ConnectionState: model.FeishuConnectionNone, Generation: 1}
	}
	cp := *f.acc
	return &cp, nil
}
func (f *fakeAccountStore) Delete(_ context.Context, _ uint, _ string) error {
	f.acc = nil
	return nil
}
func (f *fakeAccountStore) UpdateTokens(_ context.Context, _ uint, _ string, _, _ []byte, _ *time.Time) error {
	return nil
}
func (f *fakeAccountStore) MarkConnected(_ context.Context, _ uint, _ string, at time.Time) error {
	if f.acc == nil {
		return gorm.ErrRecordNotFound
	}
	f.acc.Connected = true
	f.acc.ConnectedAt = &at
	return nil
}

// fakeOpsRunner scripts the opsRunner seam for the client gate (only AuthStatus is
// exercised here; the ops methods are covered by api_test.go via the fake lark-cli).
type fakeOpsRunner struct {
	authOK  bool
	authErr error
}

func (f *fakeOpsRunner) CreateDoc(context.Context, uint, string, string) (*DocResult, error) {
	return &DocResult{DocumentID: "doc"}, nil
}
func (f *fakeOpsRunner) SendMessage(context.Context, uint, string, string, string, string) (*MsgResult, error) {
	return &MsgResult{MessageID: "msg"}, nil
}
func (f *fakeOpsRunner) ReadBitable(context.Context, uint, string, string, int, int) (*BitableResult, error) {
	return &BitableResult{}, nil
}
func (f *fakeOpsRunner) AuthStatus(context.Context, uint) (bool, error) {
	return f.authOK, f.authErr
}

func newGateTestClient(t *testing.T, store *fakeAccountStore, ops *fakeOpsRunner) *Client {
	t.Helper()
	c, err := NewClient(store, ops)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return c
}

// --- APIFor gate ------------------------------------------------------------

func TestAPIFor_NoRow_NotConnected(t *testing.T) {
	c := newGateTestClient(t, &fakeAccountStore{acc: nil}, &fakeOpsRunner{authOK: true})
	_, err := c.APIFor(context.Background(), 7)
	if !errors.Is(err, errno.ErrLarkNotConnected) {
		t.Fatalf("no row must be ErrLarkNotConnected, got %v", err)
	}
}

func TestAPIFor_RowNotConnected_NotConnected(t *testing.T) {
	store := &fakeAccountStore{acc: &model.UserThirdPartyAccount{
		UserID: 7, Provider: ProviderLark, AppID: "cli_x", Connected: false,
	}}
	c := newGateTestClient(t, store, &fakeOpsRunner{authOK: true})
	_, err := c.APIFor(context.Background(), 7)
	if !errors.Is(err, errno.ErrLarkNotConnected) {
		t.Fatalf("not-connected row must be ErrLarkNotConnected, got %v", err)
	}
}

func TestAPIFor_ConnectedButAuthStatusFalse_ReauthRequired(t *testing.T) {
	store := &fakeAccountStore{acc: &model.UserThirdPartyAccount{
		UserID: 7, Provider: ProviderLark, AppID: "cli_x", Connected: true,
	}}
	// DB says connected but lark-cli no longer holds a usable authorization.
	c := newGateTestClient(t, store, &fakeOpsRunner{authOK: false})
	_, err := c.APIFor(context.Background(), 7)
	if !errors.Is(err, errno.ErrLarkReauthRequired) {
		t.Fatalf("lost authorization must be ErrLarkReauthRequired, got %v", err)
	}
}

func TestAPIFor_AuthStatusError_ReauthRequired(t *testing.T) {
	store := &fakeAccountStore{acc: &model.UserThirdPartyAccount{
		UserID: 7, Provider: ProviderLark, AppID: "cli_x", Connected: true,
	}}
	c := newGateTestClient(t, store, &fakeOpsRunner{authErr: errors.New("cli boom")})
	_, err := c.APIFor(context.Background(), 7)
	if !errors.Is(err, errno.ErrLarkReauthRequired) {
		t.Fatalf("auth status failure must fail closed as ErrLarkReauthRequired, got %v", err)
	}
}

func TestAPIFor_ConnectedAndAuthorized_OK(t *testing.T) {
	store := &fakeAccountStore{acc: &model.UserThirdPartyAccount{
		UserID: 7, Provider: ProviderLark, AppID: "cli_x", Connected: true,
	}}
	c := newGateTestClient(t, store, &fakeOpsRunner{authOK: true})
	api, err := c.APIFor(context.Background(), 7)
	if err != nil {
		t.Fatalf("connected+authorized must succeed, got %v", err)
	}
	if api == nil {
		t.Fatal("APIFor must return a non-nil LarkAPI on success")
	}
	// And the returned API delegates to the ops runner.
	res, err := api.CreateDoc(context.Background(), "t", "")
	if err != nil {
		t.Fatalf("CreateDoc: %v", err)
	}
	if res.DocumentID != "doc" {
		t.Fatalf("CreateDoc result mismatch: %q", res.DocumentID)
	}
}

func TestNewClient_NilDeps(t *testing.T) {
	if _, err := NewClient(nil, &fakeOpsRunner{}); err == nil {
		t.Fatal("nil store must error")
	}
	if _, err := NewClient(&fakeAccountStore{}, nil); err == nil {
		t.Fatal("nil ops runner must error")
	}
}
