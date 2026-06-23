package feishu

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"

	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/model"

	"gorm.io/gorm"
)

// --- service-test fakes (svc* prefix to avoid clashing with client/provisioner
// test doubles in the same package) -----------------------------------------

// svcAccountStore is a multi-row in-memory IThirdPartyAccountStore (the client
// test's fakeAccountStore is single-row; the service flows need >1 user).
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

// svcExchanger fakes CodeExchanger + AppStarter (the service's provisioner seam).
type svcExchanger struct {
	access, refresh []byte
	exp             *time.Time
	scopes          string
	err             error
	calls           int

	pageURL    string
	sessionRef string
	startErr   error
}

func (f *svcExchanger) ExchangeCode(_ context.Context, _, _ string) (access, refresh []byte, exp *time.Time, scopes string, err error) {
	f.calls++
	if f.err != nil {
		return nil, nil, nil, "", f.err
	}
	return f.access, f.refresh, f.exp, f.scopes, nil
}

func (f *svcExchanger) StartProvision(_ context.Context, _ uint) (string, string, error) {
	if f.startErr != nil {
		return "", "", f.startErr
	}
	return f.pageURL, f.sessionRef, nil
}

// svcAnswerResumer records the resume call (cross-user / idempotency assertions).
type svcAnswerResumer struct {
	gotUserID uint
	gotRunID  uint64
	gotKeys   []string
	gotText   string
	err       error
	calls     int
}

func (f *svcAnswerResumer) ResumeWithAnswer(_ context.Context, userID uint, runID uint64, questionText, freeText string) error {
	f.calls++
	f.gotUserID = userID
	f.gotRunID = runID
	f.gotKeys = append(f.gotKeys, questionText)
	f.gotText = freeText
	return f.err
}

// svcRunReader fakes RunStateReader.
type svcRunReader struct {
	run *model.AgentRun
	err error
}

func (f *svcRunReader) GetRun(_ context.Context, _ uint64) (*model.AgentRun, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.run, nil
}

// --- helpers ---------------------------------------------------------------

func newTestSvc(t *testing.T) (*FeishuService, *svcAccountStore, *svcExchanger, *svcAnswerResumer, *svcRunReader, *StateSigner) {
	t.Helper()
	st := newSvcAccountStore()
	ex := &svcExchanger{pageURL: "https://open.feishu.cn/page/cli?user_code=ABCD", sessionRef: "sess-1"}
	ans := &svcAnswerResumer{}
	rr := &svcRunReader{}
	signer, err := NewStateSigner(testKey, newFakeNonceStore())
	if err != nil {
		t.Fatalf("NewStateSigner: %v", err)
	}
	svc, err := NewFeishuService(Deps{
		Store:        st,
		Signer:       signer,
		Provisioner:  ex,
		Answer:       ans,
		Runs:         rr,
		WebBaseURL:   "https://youshu.asia",
		ScopesCSV:    "docx:document im:message bitable:app:readonly",
		AuthorizeURL: "https://open.feishu.cn/open-apis/authen/v1/authorize",
		RedirectURI:  "https://youshu.asia/api/v1/feishu/oauth/callback",
	})
	if err != nil {
		t.Fatalf("NewFeishuService: %v", err)
	}
	return svc, st, ex, ans, rr, signer
}

func makeRun(userID uint, runID uint64, state, questionText string) *model.AgentRun {
	pending := `{"questions":[{"question":"` + questionText + `"}],"pause_type":"auth"}`
	return &model.AgentRun{
		ID:                  runID,
		UserID:              userID,
		StateReason:         state,
		PendingQuestionJSON: []byte(pending),
	}
}

func signCallbackState(t *testing.T, signer *StateSigner, userID uint, runID uint64, qText string) string {
	t.Helper()
	state, err := signer.Sign(context.Background(), Payload{
		UserID: userID, RunID: strconv.FormatUint(runID, 10), Step: "authorize", QuestionText: qText,
	}, 10*time.Minute)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	return state
}

// --- Status ----------------------------------------------------------------

func TestStatus_NotConnected(t *testing.T) {
	svc, _, _, _, _, _ := newTestSvc(t)
	got, err := svc.Status(context.Background(), 42)
	if err != nil {
		t.Fatalf("Status: unexpected err: %v", err)
	}
	if got.Connected {
		t.Fatalf("expected not connected")
	}
	if got.Status != "none" {
		t.Fatalf("status = %q, want none", got.Status)
	}
}

func TestStatus_Active(t *testing.T) {
	svc, st, _, _, _, _ := newTestSvc(t)
	exp := time.Now().Add(time.Hour)
	st.put(&model.UserThirdPartyAccount{
		UserID: 42, Provider: ProviderLark, AppID: "cli_app", Scopes: "docx:document im:message",
		TokenExpiresAt: &exp,
	})
	got, err := svc.Status(context.Background(), 42)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !got.Connected || got.Status != "active" {
		t.Fatalf("got %+v, want connected/active", got)
	}
	if got.AppID != "cli_app" {
		t.Fatalf("app_id = %q", got.AppID)
	}
	if len(got.Scopes) != 2 {
		t.Fatalf("scopes = %v, want 2", got.Scopes)
	}
}

func TestStatus_Expired(t *testing.T) {
	svc, st, _, _, _, _ := newTestSvc(t)
	past := time.Now().Add(-time.Hour)
	st.put(&model.UserThirdPartyAccount{
		UserID: 42, Provider: ProviderLark, AppID: "cli_app", TokenExpiresAt: &past,
	})
	got, err := svc.Status(context.Background(), 42)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !got.Connected || got.Status != "expired" {
		t.Fatalf("got %+v, want connected/expired", got)
	}
}

// --- Connect ---------------------------------------------------------------

func TestConnect_NoAppYet_ReturnsCreateApp(t *testing.T) {
	svc, _, _, _, _, _ := newTestSvc(t)
	got, err := svc.Connect(context.Background(), 42, 100, "请完成授权")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if got.NextStep != NextStepCreateApp {
		t.Fatalf("next_step = %q, want %q", got.NextStep, NextStepCreateApp)
	}
	if got.URL == "" {
		t.Fatalf("expected a non-empty url")
	}
}

func TestConnect_HasApp_ReturnsAuthorizeURLWithState(t *testing.T) {
	svc, st, _, _, _, signer := newTestSvc(t)
	st.put(&model.UserThirdPartyAccount{UserID: 42, Provider: ProviderLark, AppID: "cli_app"})
	got, err := svc.Connect(context.Background(), 42, 100, "请完成授权")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if got.NextStep != NextStepAuthorize {
		t.Fatalf("next_step = %q, want %q", got.NextStep, NextStepAuthorize)
	}
	if got.State == "" {
		t.Fatalf("expected a non-empty state")
	}
	p, verr := signer.Verify(context.Background(), got.State)
	if verr != nil {
		t.Fatalf("Verify(state): %v", verr)
	}
	if p.UserID != 42 || p.RunID != "100" || p.QuestionText != "请完成授权" {
		t.Fatalf("payload mismatch: %+v", p)
	}
}

// --- Unbind ----------------------------------------------------------------

func TestUnbind_DeletesRow(t *testing.T) {
	svc, st, _, _, _, _ := newTestSvc(t)
	st.put(&model.UserThirdPartyAccount{UserID: 42, Provider: ProviderLark, AppID: "x"})
	if err := svc.Unbind(context.Background(), 42); err != nil {
		t.Fatalf("Unbind: %v", err)
	}
	if st.deletes != 1 {
		t.Fatalf("deletes = %d, want 1", st.deletes)
	}
}

// --- HandleCallback --------------------------------------------------------

func TestHandleCallback_StateInvalid(t *testing.T) {
	svc, _, _, _, _, _ := newTestSvc(t)
	res, err := svc.HandleCallback(context.Background(), "the-code", "not-a-valid-state")
	if err == nil {
		t.Fatalf("expected error for invalid state")
	}
	if !errors.Is(err, errno.ErrLarkStateInvalid) {
		t.Fatalf("err = %v, want ErrLarkStateInvalid", err)
	}
	if res.RedirectURL == "" {
		t.Fatalf("expected a redirect URL even on error")
	}
	if res.Success {
		t.Fatalf("invalid state must be an error redirect")
	}
}

func TestHandleCallback_HappyPath_ExchangesUpsertsAndResumes(t *testing.T) {
	svc, st, ex, ans, rr, signer := newTestSvc(t)
	const userID = uint(42)
	const runID = uint64(100)
	const qText = "请完成授权"

	st.put(&model.UserThirdPartyAccount{UserID: userID, Provider: ProviderLark, AppID: "cli_app"})
	rr.run = makeRun(userID, runID, "waiting_for_user_choice", qText)
	exp := time.Now().Add(time.Hour)
	ex.access = []byte("enc-access")
	ex.refresh = []byte("enc-refresh")
	ex.exp = &exp
	ex.scopes = "docx:document im:message"

	state := signCallbackState(t, signer, userID, runID, qText)

	res, err := svc.HandleCallback(context.Background(), "the-code", state)
	if err != nil {
		t.Fatalf("HandleCallback: %v", err)
	}
	if ex.calls != 1 {
		t.Fatalf("exchange calls = %d, want 1", ex.calls)
	}
	if st.upserts != 1 {
		t.Fatalf("upserts = %d, want 1", st.upserts)
	}
	row, _ := st.Get(context.Background(), userID, ProviderLark)
	if string(row.AccessTokenEnc) != "enc-access" || string(row.RefreshTokenEnc) != "enc-refresh" {
		t.Fatalf("stored tokens mismatch: %+v", row)
	}
	if ans.calls != 1 || ans.gotUserID != userID || ans.gotRunID != runID {
		t.Fatalf("resume mismatch: calls=%d user=%d run=%d", ans.calls, ans.gotUserID, ans.gotRunID)
	}
	if len(ans.gotKeys) != 1 || ans.gotKeys[0] != qText {
		t.Fatalf("resume key = %v, want [%q]", ans.gotKeys, qText)
	}
	if ans.gotText != resumeFreeText {
		t.Fatalf("resume free text = %q, want %q", ans.gotText, resumeFreeText)
	}
	if res.RedirectURL == "" || !res.Success {
		t.Fatalf("expected success redirect, got %+v", res)
	}
}

func TestHandleCallback_CrossUser_Rejected(t *testing.T) {
	svc, st, ex, ans, rr, signer := newTestSvc(t)
	const stateUserID = uint(42) // state claims user 42
	const runOwnerID = uint(99)  // run actually belongs to 99
	const runID = uint64(100)
	const qText = "请完成授权"

	st.put(&model.UserThirdPartyAccount{UserID: stateUserID, Provider: ProviderLark, AppID: "cli_app"})
	rr.run = makeRun(runOwnerID, runID, "waiting_for_user_choice", qText)

	state := signCallbackState(t, signer, stateUserID, runID, qText)
	res, err := svc.HandleCallback(context.Background(), "the-code", state)
	if err == nil {
		t.Fatalf("expected cross-user rejection")
	}
	if !errors.Is(err, errno.ErrLarkStateInvalid) {
		t.Fatalf("err = %v, want ErrLarkStateInvalid", err)
	}
	if ex.calls != 0 || st.upserts != 0 || ans.calls != 0 {
		t.Fatalf("cross-user must not act: exchange=%d upsert=%d resume=%d", ex.calls, st.upserts, ans.calls)
	}
	if res.RedirectURL == "" || res.Success {
		t.Fatalf("expected error redirect, got %+v", res)
	}
}

func TestHandleCallback_Idempotent_RunNotWaiting(t *testing.T) {
	svc, st, ex, ans, rr, signer := newTestSvc(t)
	const userID = uint(42)
	const runID = uint64(100)
	const qText = "请完成授权"

	st.put(&model.UserThirdPartyAccount{
		UserID: userID, Provider: ProviderLark, AppID: "cli_app", AccessTokenEnc: []byte("enc-access"),
	})
	rr.run = makeRun(userID, runID, "completed", qText) // already left waiting

	state := signCallbackState(t, signer, userID, runID, qText)
	res, err := svc.HandleCallback(context.Background(), "the-code", state)
	if err != nil {
		t.Fatalf("idempotent callback should not error, got: %v", err)
	}
	if ex.calls != 0 {
		t.Fatalf("idempotent must not re-exchange, calls=%d", ex.calls)
	}
	if ans.calls != 0 {
		t.Fatalf("idempotent must not re-resume, calls=%d", ans.calls)
	}
	if !res.Success {
		t.Fatalf("idempotent callback should still redirect to success, got %+v", res)
	}
}

func TestHandleCallback_RunNotWaiting_NoTokenYet_StillExchanges(t *testing.T) {
	svc, st, ex, ans, rr, signer := newTestSvc(t)
	const userID = uint(42)
	const runID = uint64(100)
	const qText = "请完成授权"

	st.put(&model.UserThirdPartyAccount{UserID: userID, Provider: ProviderLark, AppID: "cli_app"})
	rr.run = makeRun(userID, runID, "completed", qText)
	ex.access = []byte("enc-access")

	state := signCallbackState(t, signer, userID, runID, qText)
	res, err := svc.HandleCallback(context.Background(), "the-code", state)
	if err != nil {
		t.Fatalf("HandleCallback: %v", err)
	}
	if ex.calls != 1 || st.upserts != 1 {
		t.Fatalf("expected one exchange+upsert, exchange=%d upsert=%d", ex.calls, st.upserts)
	}
	if ans.calls != 0 {
		t.Fatalf("run not waiting → no resume, calls=%d", ans.calls)
	}
	if !res.Success {
		t.Fatalf("expected success redirect")
	}
}

func TestHandleCallback_ExchangeFails_ErrorRedirect(t *testing.T) {
	svc, st, ex, ans, rr, signer := newTestSvc(t)
	const userID = uint(42)
	const runID = uint64(100)
	const qText = "请完成授权"

	st.put(&model.UserThirdPartyAccount{UserID: userID, Provider: ProviderLark, AppID: "cli_app"})
	rr.run = makeRun(userID, runID, "waiting_for_user_choice", qText)
	ex.err = errors.New("upstream 飞书 down")

	state := signCallbackState(t, signer, userID, runID, qText)
	res, err := svc.HandleCallback(context.Background(), "the-code", state)
	if err == nil {
		t.Fatalf("expected error on exchange failure")
	}
	if st.upserts != 0 || ans.calls != 0 {
		t.Fatalf("failed exchange must not upsert/resume")
	}
	if res.RedirectURL == "" || res.Success {
		t.Fatalf("expected error redirect")
	}
}
