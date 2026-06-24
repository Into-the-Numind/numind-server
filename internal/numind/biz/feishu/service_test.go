package feishu

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"

	"numind-server/internal/pkg/model"

	"gorm.io/gorm"
)

// --- service-test fakes (svc* prefix to avoid clashing with client/provisioner
// test doubles in the same package) -----------------------------------------

// svcAccountStore is a multi-row in-memory IThirdPartyAccountStore (shared by the
// service + orchestrator tests, which need >1 user).
type svcAccountStore struct {
	mu      sync.Mutex
	rows    map[string]*model.UserThirdPartyAccount // key: provider|userID
	upserts int
	deletes int
}

func newSvcAccountStore() *svcAccountStore {
	return &svcAccountStore{rows: map[string]*model.UserThirdPartyAccount{}}
}

func (f *svcAccountStore) key(userID uint, provider string) string {
	return provider + "|" + strconv.FormatUint(uint64(userID), 10)
}

func (f *svcAccountStore) put(acc *model.UserThirdPartyAccount) {
	cp := *acc
	f.rows[f.key(acc.UserID, acc.Provider)] = &cp
}

func (f *svcAccountStore) Get(_ context.Context, userID uint, provider string) (*model.UserThirdPartyAccount, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.rows[f.key(userID, provider)]
	if !ok {
		return nil, gorm.ErrRecordNotFound
	}
	cp := *r
	return &cp, nil
}

func (f *svcAccountStore) Upsert(_ context.Context, acc *model.UserThirdPartyAccount) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.upserts++
	f.put(acc)
	return nil
}

func (f *svcAccountStore) Delete(_ context.Context, userID uint, provider string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deletes++
	delete(f.rows, f.key(userID, provider))
	return nil
}

func (f *svcAccountStore) UpdateTokens(_ context.Context, _ uint, _ string, _, _ []byte, _ *time.Time) error {
	return nil
}

func (f *svcAccountStore) MarkConnected(_ context.Context, userID uint, provider string, connectedAt time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.rows[f.key(userID, provider)]
	if !ok {
		return gorm.ErrRecordNotFound
	}
	r.Connected = true
	at := connectedAt
	r.ConnectedAt = &at
	return nil
}

// svcStepper fakes the connectStepper (orchestrator) seam the service drives.
type svcStepper struct {
	step       *ConnectStep
	stepErr    error
	persistErr error
	pollCalls  int
	stepCalls  int
}

func (f *svcStepper) PollAndPersistApp(_ context.Context, _ uint) (bool, error) {
	f.pollCalls++
	return false, f.persistErr
}

func (f *svcStepper) NextConnectStep(_ context.Context, _ uint, _ uint64, _ string) (*ConnectStep, error) {
	f.stepCalls++
	return f.step, f.stepErr
}

// --- helpers ---------------------------------------------------------------

func newTestSvc(t *testing.T, step *ConnectStep) (*FeishuService, *svcAccountStore, *svcStepper) {
	t.Helper()
	st := newSvcAccountStore()
	stepper := &svcStepper{step: step}
	svc, err := NewFeishuService(Deps{Store: st, Orchestrator: stepper})
	if err != nil {
		t.Fatalf("NewFeishuService: %v", err)
	}
	return svc, st, stepper
}

// --- Status ----------------------------------------------------------------

func TestStatus_NotConnected(t *testing.T) {
	svc, _, _ := newTestSvc(t, &ConnectStep{Phase: ConnectPhaseDone})
	got, err := svc.Status(context.Background(), 42)
	if err != nil {
		t.Fatalf("Status: unexpected err: %v", err)
	}
	if got.Connected || got.Status != "none" {
		t.Fatalf("no row must be not-connected/none, got %+v", got)
	}
}

func TestStatus_Connected(t *testing.T) {
	svc, st, _ := newTestSvc(t, &ConnectStep{Phase: ConnectPhaseDone})
	at := time.Now()
	st.put(&model.UserThirdPartyAccount{
		UserID: 42, Provider: ProviderLark, AppID: "cli_app42", Connected: true, ConnectedAt: &at,
	})
	got, err := svc.Status(context.Background(), 42)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !got.Connected || got.Status != "connected" {
		t.Fatalf("connected row must report connected, got %+v", got)
	}
	if got.AppID != "cli_app42" {
		t.Fatalf("app_id mismatch: %q", got.AppID)
	}
}

func TestStatus_AppRowNotConnected_None(t *testing.T) {
	svc, st, _ := newTestSvc(t, &ConnectStep{Phase: ConnectPhaseDone})
	// App provisioned but not yet authorized → status none (app_id still surfaced).
	st.put(&model.UserThirdPartyAccount{UserID: 42, Provider: ProviderLark, AppID: "cli_x"})
	got, err := svc.Status(context.Background(), 42)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if got.Connected || got.Status != "none" {
		t.Fatalf("unconnected app row must be none, got %+v", got)
	}
	if got.AppID != "cli_x" {
		t.Fatalf("app_id should be surfaced: %q", got.AppID)
	}
}

// --- Connect ---------------------------------------------------------------

func TestConnect_CreateApp_ReturnsPageURL(t *testing.T) {
	svc, _, stepper := newTestSvc(t, &ConnectStep{Phase: ConnectPhaseCreateApp, URL: "https://open.feishu.cn/page/cli?u=1"})
	res, err := svc.Connect(context.Background(), 7)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if res.NextStep != NextStepCreateApp {
		t.Fatalf("next_step mismatch: %q", res.NextStep)
	}
	if res.URL == "" {
		t.Fatal("create_app must carry a URL")
	}
	// Connect always bridges app-create first, then asks for the step.
	if stepper.pollCalls != 1 || stepper.stepCalls != 1 {
		t.Fatalf("Connect must poll then step once each, got poll=%d step=%d", stepper.pollCalls, stepper.stepCalls)
	}
}

func TestConnect_Authorize_ReturnsVerificationURL(t *testing.T) {
	svc, _, _ := newTestSvc(t, &ConnectStep{Phase: ConnectPhaseAuthorize, URL: "https://open.feishu.cn/device?u=2"})
	res, err := svc.Connect(context.Background(), 7)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if res.NextStep != NextStepAuthorize {
		t.Fatalf("next_step mismatch: %q", res.NextStep)
	}
	if res.URL == "" {
		t.Fatal("authorize must carry a verification URL")
	}
}

func TestConnect_Done(t *testing.T) {
	svc, _, _ := newTestSvc(t, &ConnectStep{Phase: ConnectPhaseDone})
	res, err := svc.Connect(context.Background(), 7)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if res.NextStep != NextStepDone {
		t.Fatalf("done expected, got %q", res.NextStep)
	}
	if res.URL != "" {
		t.Fatalf("done must carry no URL, got %q", res.URL)
	}
}

func TestConnect_PollError_Propagates(t *testing.T) {
	svc, _, stepper := newTestSvc(t, &ConnectStep{Phase: ConnectPhaseDone})
	stepper.persistErr = errors.New("poll boom")
	if _, err := svc.Connect(context.Background(), 7); err == nil {
		t.Fatal("Connect must surface a poll error")
	}
}

func TestConnect_StepError_Propagates(t *testing.T) {
	svc, _, stepper := newTestSvc(t, nil)
	stepper.stepErr = errors.New("step boom")
	if _, err := svc.Connect(context.Background(), 7); err == nil {
		t.Fatal("Connect must surface a step error")
	}
}

// --- Unbind ----------------------------------------------------------------

func TestUnbind_DeletesRow(t *testing.T) {
	svc, st, _ := newTestSvc(t, &ConnectStep{Phase: ConnectPhaseDone})
	st.put(&model.UserThirdPartyAccount{UserID: 7, Provider: ProviderLark, AppID: "cli_x", Connected: true})
	if err := svc.Unbind(context.Background(), 7); err != nil {
		t.Fatalf("Unbind: %v", err)
	}
	if _, err := st.Get(context.Background(), 7, ProviderLark); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("row should be deleted, got %v", err)
	}
	if st.deletes != 1 {
		t.Fatalf("expected 1 delete, got %d", st.deletes)
	}
}

func TestUnbind_Idempotent(t *testing.T) {
	svc, _, _ := newTestSvc(t, &ConnectStep{Phase: ConnectPhaseDone})
	if err := svc.Unbind(context.Background(), 999); err != nil {
		t.Fatalf("Unbind on missing row must be idempotent, got %v", err)
	}
}

// --- constructor guards -----------------------------------------------------

func TestNewFeishuService_NilDeps(t *testing.T) {
	if _, err := NewFeishuService(Deps{Orchestrator: &svcStepper{}}); err == nil {
		t.Fatal("nil store must error")
	}
	if _, err := NewFeishuService(Deps{Store: newSvcAccountStore()}); err == nil {
		t.Fatal("nil orchestrator must error")
	}
}
