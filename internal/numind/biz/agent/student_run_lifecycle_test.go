package agent

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
	"gorm.io/datatypes"
	"gorm.io/gorm"

	"numind-server/internal/numind/biz/agent/stream"
	"numind-server/internal/numind/biz/narration"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/model"
)

// ---------------------------------------------------------------------------
// Mock implementations (prefixed "lifecycle" to avoid collision with runner_test.go)
// ---------------------------------------------------------------------------

// lifecycleRunner implements AgentRunner for lifecycle tests.
type lifecycleRunner struct {
	cancelCalled map[uint64]bool
	runResult    *RunResult
	runErr       error
}

func (m *lifecycleRunner) Run(_ context.Context, _ RunRequest) (*RunResult, error) {
	return m.runResult, m.runErr
}
func (m *lifecycleRunner) RunStream(_ context.Context, _ RunRequest, _ uint64, _ chan<- stream.Event) (*RunResult, error) {
	return m.runResult, m.runErr
}
func (m *lifecycleRunner) Cancel(runID uint64) bool {
	if m.cancelCalled == nil {
		m.cancelCalled = make(map[uint64]bool)
	}
	m.cancelCalled[runID] = true
	return true
}

// lifecycleRunStore implements store.IAgentRunStore for lifecycle tests.
type lifecycleRunStore struct {
	runs    map[uint64]*model.AgentRun
	nextID  uint64
	listErr error // when set, ListBySession returns this error (fail-open path test)
}

func newLifecycleRunStore() *lifecycleRunStore {
	return &lifecycleRunStore{runs: make(map[uint64]*model.AgentRun)}
}

func (s *lifecycleRunStore) Create(_ context.Context, run *model.AgentRun) error {
	s.nextID++
	run.ID = s.nextID
	s.runs[run.ID] = run
	return nil
}
func (s *lifecycleRunStore) Get(_ context.Context, id uint64) (*model.AgentRun, error) {
	r, ok := s.runs[id]
	if !ok {
		return nil, errors.New("record not found")
	}
	return r, nil
}
func (s *lifecycleRunStore) UpdateState(_ context.Context, id uint64, status, _ string, _ *time.Time) error {
	r, ok := s.runs[id]
	if !ok {
		return errors.New("not found")
	}
	r.Status = status
	return nil
}
func (s *lifecycleRunStore) WriteTurn(_ context.Context, _ uint64, _ json.RawMessage) error {
	return nil
}
func (s *lifecycleRunStore) ListBySession(_ context.Context, sessionID string, offset, limit int) ([]model.AgentRun, int64, error) {
	if s.listErr != nil {
		return nil, 0, s.listErr
	}
	var matched []model.AgentRun
	for _, r := range s.runs {
		if r.SessionID == sessionID {
			matched = append(matched, *r)
		}
	}
	// Mirror the real store: order by StartedAt DESC (newest first).
	sort.Slice(matched, func(i, j int) bool { return matched[i].StartedAt.After(matched[j].StartedAt) })
	total := int64(len(matched))
	if offset >= len(matched) {
		return nil, total, nil
	}
	matched = matched[offset:]
	if limit > 0 && limit < len(matched) {
		matched = matched[:limit]
	}
	return matched, total, nil
}
func (s *lifecycleRunStore) UpdateTerminalMetadata(_ context.Context, id uint64, meta datatypes.JSON) error {
	r, ok := s.runs[id]
	if !ok {
		return errors.New("not found")
	}
	r.TerminalMetadata = meta
	return nil
}
func (s *lifecycleRunStore) SetCancellationRequested(_ context.Context, id uint64, _ datatypes.JSON) error {
	r, ok := s.runs[id]
	if !ok {
		return errors.New("not found")
	}
	now := time.Now()
	r.CancellationRequestedAt = &now
	return nil
}
func (s *lifecycleRunStore) ListByParentUserIDAndStatus(_ context.Context, _ uint, _ string, _, _ int) ([]model.AgentRun, int64, error) {
	return nil, 0, nil
}
func (s *lifecycleRunStore) ListByUser(_ context.Context, _ uint, _ *time.Time, _ int) ([]model.AgentRun, error) {
	return nil, nil
}
func (s *lifecycleRunStore) MergeTerminalMetadata(_ context.Context, _ uint64, _ map[string]interface{}) error {
	return nil
}
func (s *lifecycleRunStore) UpdatePendingQuestion(_ context.Context, id uint64, payloadJSON []byte) error {
	r, ok := s.runs[id]
	if !ok {
		return errors.New("not found")
	}
	r.StateReason = "waiting_for_user_choice"
	_ = payloadJSON
	return nil
}
func (s *lifecycleRunStore) ClearPendingQuestion(_ context.Context, id uint64) error {
	r, ok := s.runs[id]
	if !ok {
		return errors.New("not found")
	}
	r.StateReason = "running"
	r.PendingQuestionJSON = nil
	r.PendingQuestionAt = nil
	return nil
}
func (s *lifecycleRunStore) AppendUserMessage(_ context.Context, _ uint64, _ string) error {
	return nil
}

func (s *lifecycleRunStore) AnswerAndClear(_ context.Context, _ uint64, _ json.RawMessage) error {
	return nil
}
func (s *lifecycleRunStore) UpdateSessionPinned(_ context.Context, _ string, _ bool) error {
	return nil
}
func (s *lifecycleRunStore) UpdateSessionName(_ context.Context, _ string, _ string) error {
	return nil
}

func (s *lifecycleRunStore) UpdateSessionNameIfEmpty(_ context.Context, _ string, _ string) (bool, error) {
	return false, nil
}
func (s *lifecycleRunStore) UpdateSessionDeleted(_ context.Context, _ string, _ bool) error {
	return nil
}

// lifecycleSkillStore implements store.IAgentDefinitionStore for lifecycle tests.
type lifecycleSkillStore struct {
	defs map[uint64]*model.AgentDefinition
}

func newLifecycleSkillStore() *lifecycleSkillStore {
	return &lifecycleSkillStore{defs: make(map[uint64]*model.AgentDefinition)}
}

func (s *lifecycleSkillStore) GetByIDIncludeInactive(_ context.Context, id uint64) (*model.AgentDefinition, error) {
	d, ok := s.defs[id]
	if !ok {
		return nil, errors.New("record not found")
	}
	return d, nil
}
func (s *lifecycleSkillStore) Create(_ context.Context, _ *model.AgentDefinition) error { return nil }
func (s *lifecycleSkillStore) CreateTx(_ context.Context, _ *gorm.DB, _ *model.AgentDefinition) error {
	return nil
}
func (s *lifecycleSkillStore) GetByID(_ context.Context, id uint64) (*model.AgentDefinition, error) {
	return s.GetByIDIncludeInactive(context.Background(), id)
}
func (s *lifecycleSkillStore) ListByParent(_ context.Context, _ uint, _ bool, _, _ int) ([]model.AgentDefinition, int64, error) {
	return nil, 0, nil
}
func (s *lifecycleSkillStore) Update(_ context.Context, _ *model.AgentDefinition) error { return nil }
func (s *lifecycleSkillStore) UpdateTx(_ context.Context, _ *gorm.DB, _ *model.AgentDefinition) error {
	return nil
}
func (s *lifecycleSkillStore) SoftDelete(_ context.Context, _ uint64) error { return nil }
func (s *lifecycleSkillStore) SoftDeleteTx(_ context.Context, _ *gorm.DB, _ uint64) error {
	return nil
}
func (s *lifecycleSkillStore) WriteHistory(_ context.Context, _ *model.AgentDefinitionHistory) error {
	return nil
}
func (s *lifecycleSkillStore) WriteHistoryTx(_ context.Context, _ *gorm.DB, _ *model.AgentDefinitionHistory) error {
	return nil
}
func (s *lifecycleSkillStore) ListHistory(_ context.Context, _ uint64) ([]model.AgentDefinitionHistory, error) {
	return nil, nil
}
func (s *lifecycleSkillStore) GetHistoryByVersion(_ context.Context, _ uint64, _ uint) (*model.AgentDefinitionHistory, error) {
	return nil, nil
}
func (s *lifecycleSkillStore) MaxVersion(_ context.Context, _ uint64) (uint, error) { return 0, nil }

// ---------------------------------------------------------------------------
// Estimate tests
// ---------------------------------------------------------------------------

func TestStudentRunService_Estimate_HappyPath(t *testing.T) {
	skillStore := newLifecycleSkillStore()
	userID := uint(10)
	adID := uint64(1)
	skillStore.defs[adID] = &model.AgentDefinition{
		ID:           adID,
		ParentUserID: userID,
		IsActive:     true,
	}

	svc := NewStudentRunService(nil, nil, skillStore, nil, nil, nil)
	resp, err := svc.Estimate(context.Background(), userID, EstimateRunRequest{
		AgentDefinitionID: adID,
		Message:           "Hello agent",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Min <= 0 || resp.Max < resp.Min {
		t.Errorf("expected positive min/max (max>=min), got min=%d max=%d", resp.Min, resp.Max)
	}
}

func TestStudentRunService_Estimate_WrongOwner(t *testing.T) {
	skillStore := newLifecycleSkillStore()
	adID := uint64(2)
	skillStore.defs[adID] = &model.AgentDefinition{
		ID:           adID,
		ParentUserID: 999, // different user
		IsActive:     true,
	}

	svc := NewStudentRunService(nil, nil, skillStore, nil, nil, nil)
	_, err := svc.Estimate(context.Background(), uint(10), EstimateRunRequest{
		AgentDefinitionID: adID,
		Message:           "test",
	})
	if err == nil {
		t.Fatal("expected error for wrong owner")
	}
	if !errors.Is(err, errno.ErrSkillNotFound) {
		t.Errorf("expected ErrSkillNotFound, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// B2B2C tenant-access tests — gate #1 (resolveDefinition via Estimate)
// ---------------------------------------------------------------------------

// TestStudentRunService_Estimate_ChildOfParent_Allowed is the gate #1 form of
// the core fix: a child account may run its parent's active agent.
// (childUserRec helper lives in runner_tenant_access_test.go, same package.)
func TestStudentRunService_Estimate_ChildOfParent_Allowed(t *testing.T) {
	skillStore := newLifecycleSkillStore()
	const parentID, childID = uint(10), uint(20)
	adID := uint64(3)
	skillStore.defs[adID] = &model.AgentDefinition{ID: adID, ParentUserID: parentID, IsActive: true}

	svc := NewStudentRunService(nil, nil, skillStore, nil, nil, nil)
	svc.userStore = &mockUserByIDGetter{user: childUserRec(childID, parentID)}

	resp, err := svc.Estimate(context.Background(), childID, EstimateRunRequest{
		AgentDefinitionID: adID,
		Message:           "Hello from a learner",
	})
	if err != nil {
		t.Fatalf("expected child to access parent agent, got error: %v", err)
	}
	if resp == nil || resp.Min <= 0 {
		t.Fatalf("expected a valid estimate, got %+v", resp)
	}
}

// TestStudentRunService_Estimate_ChildTenantDenials covers R9 (inactive) and
// cross-tenant denial through gate #1, plus the unwired-store contrast.
func TestStudentRunService_Estimate_ChildTenantDenials(t *testing.T) {
	const parentID, childID, otherParent = uint(10), uint(20), uint(99)
	adID := uint64(4)

	cases := []struct {
		name      string
		adParent  uint
		adActive  bool
		userStore userByIDGetter
	}{
		{"child + inactive agent (R9)", parentID, false, &mockUserByIDGetter{user: childUserRec(childID, parentID)}},
		{"child + other tenant", otherParent, true, &mockUserByIDGetter{user: childUserRec(childID, parentID)}},
		{"child + active but userStore unwired", parentID, true, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			skillStore := newLifecycleSkillStore()
			skillStore.defs[adID] = &model.AgentDefinition{ID: adID, ParentUserID: tc.adParent, IsActive: tc.adActive}
			svc := NewStudentRunService(nil, nil, skillStore, nil, nil, nil)
			svc.userStore = tc.userStore

			_, err := svc.Estimate(context.Background(), childID, EstimateRunRequest{
				AgentDefinitionID: adID,
				Message:           "x",
			})
			if !errors.Is(err, errno.ErrSkillNotFound) {
				t.Fatalf("expected ErrSkillNotFound, got %v", err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Cancel tests
// ---------------------------------------------------------------------------

func TestStudentRunService_Cancel_HappyPath(t *testing.T) {
	runner := &lifecycleRunner{}
	runStore := newLifecycleRunStore()

	userID := uint(5)
	run := &model.AgentRun{UserID: userID, Status: "running"}
	if err := runStore.Create(context.Background(), run); err != nil {
		t.Fatal(err)
	}

	svc := NewStudentRunService(runner, runStore, nil, nil, nil, nil)
	if err := svc.Cancel(context.Background(), userID, run.ID); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !runner.cancelCalled[run.ID] {
		t.Error("Cancel was not forwarded to runner")
	}
}

func TestStudentRunService_Cancel_WrongOwner(t *testing.T) {
	runner := &lifecycleRunner{}
	runStore := newLifecycleRunStore()

	run := &model.AgentRun{UserID: 999, Status: "running"}
	if err := runStore.Create(context.Background(), run); err != nil {
		t.Fatal(err)
	}

	svc := NewStudentRunService(runner, runStore, nil, nil, nil, nil)
	err := svc.Cancel(context.Background(), uint(1), run.ID)
	if err == nil {
		t.Fatal("expected error for wrong owner")
	}
	if !errors.Is(err, errno.ErrAgentRunNotFound) {
		t.Errorf("expected ErrAgentRunNotFound, got %v", err)
	}
}

func TestStudentRunService_Cancel_AlreadyTerminal(t *testing.T) {
	runner := &lifecycleRunner{}
	runStore := newLifecycleRunStore()

	userID := uint(5)
	run := &model.AgentRun{UserID: userID, Status: "terminated"}
	if err := runStore.Create(context.Background(), run); err != nil {
		t.Fatal(err)
	}

	svc := NewStudentRunService(runner, runStore, nil, nil, nil, nil)
	err := svc.Cancel(context.Background(), userID, run.ID)
	if err == nil {
		t.Fatal("expected error for terminal run")
	}
	if !errors.Is(err, errno.ErrAgentRunNotCancellable) {
		t.Errorf("expected ErrAgentRunNotCancellable, got %v", err)
	}
}


// TestStudentRunService_forwardNarration_BridgeProviderToBuffer is the
// regression for the hotfix narration-buffer-bridge. Before the fix,
// Provider.Emit pushed events to an in-memory channel that nobody read,
// the parallel NarrationBuffer (which PollNarration queries) stayed empty
// forever, and the learner UI showed no tool-call narration despite tools
// actually firing. This test seeds a real Provider, spawns the forwarder
// goroutine the way Create does, emits a few events on the provider's
// behalf, then verifies the buffer surfaces them via QuerySince.
func TestStudentRunService_forwardNarration_BridgeProviderToBuffer(t *testing.T) {
	// Minimal YAML covering the two tools this test emits for. A real provider
	// is needed end-to-end (the goroutine subscribes on a real channel).
	yaml := []byte(`tools:
  web_search:
    verb: "正在搜索"
    detail_template: "网络"
    use_template: "{{ .verb }} {{ .detail }}"
    result_template: "搜索完成"
    error_template: "搜索失败"
    rejected_template: "搜索被拦截"
defaults:
  verb: "正在执行"
  detail_template: "{{ .ToolName }}"
  use_template: "{{ .verb }}"
  result_template: "执行完成"
  error_template: "执行失败"
  rejected_template: "执行被拦截"
`)
	prov, err := narration.NewProvider(narration.Config{
		YAMLBytes:  yaml,
		BufferSize: 8,
	})
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}

	buf := NewNarrationBuffer(16, 5*time.Minute)
	svc := NewStudentRunService(nil, nil, nil, nil, prov, buf)

	runID := uint64(4242)
	done := make(chan struct{})
	go func() {
		svc.forwardNarration(runID)
		close(done)
	}()

	// Emit two events on the provider; the forwarder should AppendEvent both
	// into the buffer.
	prov.Emit(context.Background(), runID, "web_search", narration.StateUse, narration.EmitPayload{})
	prov.Emit(context.Background(), runID, "web_search", narration.StateResult, narration.EmitPayload{})

	// Give the forwarder a moment to drain the channel. The events are
	// already in the channel buffer (sized 8 in this test), so the goroutine
	// scheduler is the only thing we are racing against.
	deadline := time.Now().Add(500 * time.Millisecond)
	var got []*narration.Event
	for time.Now().Before(deadline) {
		got = buf.QuerySince(runID, time.Time{})
		if len(got) >= 2 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	if len(got) != 2 {
		t.Fatalf("buffer.QuerySince: got %d events, want 2 (forwarder did not drain channel)", len(got))
	}
	if got[0].State != narration.StateUse {
		t.Errorf("first event State: got %v, want StateUse", got[0].State)
	}
	if got[1].State != narration.StateResult {
		t.Errorf("second event State: got %v, want StateResult", got[1].State)
	}

	// Closing the run unblocks the forwarder so the goroutine exits cleanly.
	prov.CloseRun(runID)
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("forwarder did not exit after CloseRun (channel close did not propagate)")
	}
}

// TestStudentRunService_forwardNarration_NilProviderIsNoop guards the
// graceful-degrade path: if Provider init failed earlier (yaml missing),
// the service still functions, the bridge just becomes a no-op.
func TestStudentRunService_forwardNarration_NilProviderIsNoop(t *testing.T) {
	buf := NewNarrationBuffer(16, time.Minute)
	svc := NewStudentRunService(nil, nil, nil, nil, nil, buf)
	// Must return immediately — no goroutine to block on. If forwardNarration
	// blocked, this test would hang.
	svc.forwardNarration(1)
	if got := buf.QuerySince(1, time.Time{}); len(got) != 0 {
		t.Errorf("buffer should be empty when provider is nil; got %d events", len(got))
	}
}

// ---------------------------------------------------------------------------
// buildAgentInput tests (bug-from-customer 2026-05-22)
//
// Reproduce: customer uploaded a file via UI, LLM replied "you didn't upload
// anything". Root cause was three-layer schema mismatch (frontend sent
// attachment_ids, backend expected attachment_urls); plus the input format
// "[attachments: <JSON>]" was too implicit — LLM ignored it.
//
// These tests guard the new explicit Chinese instruction. If anyone changes
// the prompt and drops "file_read" or the Chinese hint marker, the LLM will
// silently regress to "no upload" replies and these tests will catch it.
// ---------------------------------------------------------------------------

// TestCreateRunRequest_AttachmentOnlyIsSendable reproduces the customer-reported bug
// (2026-06-18): uploading ONLY an attachment (e.g. a docx) and clicking send was
// rejected with "message is required". An attachment alone IS the content the user
// wants the agent to process, so an attachment-only request must be sendable.
func TestCreateRunRequest_AttachmentOnlyIsSendable(t *testing.T) {
	// attachment URL only, no text → must be sendable
	if (CreateRunRequest{Message: "", AttachmentURLs: []string{"https://cos/x.docx"}}).hasNoSendable() {
		t.Error("attachment-only (URL) must be sendable — the uploaded file IS the content")
	}
	// attachment ID only, whitespace text → must be sendable
	if (CreateRunRequest{Message: "  ", AttachmentIDs: []uint64{42}}).hasNoSendable() {
		t.Error("attachment-only (ID) must be sendable")
	}
	// genuinely empty: no text, no attachment → NOT sendable (reject)
	if !(CreateRunRequest{Message: "   "}).hasNoSendable() {
		t.Error("no text and no attachment → must be rejected")
	}
	// text only → sendable
	if (CreateRunRequest{Message: "hi"}).hasNoSendable() {
		t.Error("text-only must be sendable")
	}
}

func TestBuildAgentInput_NoAttachments(t *testing.T) {
	got := buildAgentInput("hello agent", nil)
	if got != "hello agent" {
		t.Errorf("expected bare message unchanged; got %q", got)
	}
	got2 := buildAgentInput("hello", []string{})
	if got2 != "hello" {
		t.Errorf("empty slice should be treated same as nil; got %q", got2)
	}
	got3 := buildAgentInput("", nil)
	if got3 != "" {
		t.Errorf("empty message + nil should return empty string; got %q", got3)
	}
}

func TestBuildAgentInput_WithAttachments_ContainsExplicitHint(t *testing.T) {
	urls := []string{
		"https://cos.example/agent-attachments/1/aa-report.pdf",
		"https://cos.example/agent-attachments/1/bb-chart.jpg",
	}
	got := buildAgentInput("请分析这些文件", urls)

	// Critical assertions — if any of these fails, LLM may not see the hint.
	wantSubstrings := []string{
		"请分析这些文件",    // user message preserved
		"【系统提示】",     // explicit hint marker — LLM-recognizable
		"请立即调用",      // unconditional imperative (NOT "如需…") — review P1 lock
		"file_read",  // tool name the LLM must invoke
		"file_url",   // tool parameter name
		"然后再回答用户的问题", // makes the tool call a prerequisite, not optional
		urls[0],      // first URL must be in output
		urls[1],      // second URL must be in output
	}
	for _, s := range wantSubstrings {
		if !strings.Contains(got, s) {
			t.Errorf("buildAgentInput output missing %q\nFull output:\n%s", s, got)
		}
	}

	// Both URLs must be on their own line (bulleted) — LLM parses lists well.
	for _, u := range urls {
		if !strings.Contains(got, "- "+u) {
			t.Errorf("URL %q should be on its own bulleted line; got:\n%s", u, got)
		}
	}

	// Position lock: the hint MUST appear AFTER the user message. Hoisting it
	// to the top would break the ack-then-act priming and weak models may
	// re-anchor on the system instruction and ignore the user request.
	hintIdx := strings.Index(got, "【系统提示】")
	msgIdx := strings.Index(got, "请分析这些文件")
	if hintIdx < 0 || msgIdx < 0 {
		t.Fatalf("missing required marker(s); msgIdx=%d hintIdx=%d", msgIdx, hintIdx)
	}
	if hintIdx <= msgIdx {
		t.Errorf("hint must come AFTER user message; got msgIdx=%d hintIdx=%d (full output below)\n%s", msgIdx, hintIdx, got)
	}

	// Reject the soft "如需查看" phrasing — this is the regression that caused
	// the original customer bug (LLM saw the conditional and skipped the call).
	if strings.Contains(got, "如需查看") {
		t.Errorf("output must NOT use conditional 如需查看 phrasing (use 请立即调用 instead); got:\n%s", got)
	}
}

func TestBuildAgentInput_SingleAttachment(t *testing.T) {
	got := buildAgentInput("看看这个", []string{"https://cos.example/agent-attachments/1/x.txt"})
	if !strings.Contains(got, "看看这个") {
		t.Errorf("user message must be preserved; got %q", got)
	}
	if !strings.Contains(got, "file_read") {
		t.Errorf("output must mention file_read tool; got %q", got)
	}
	if !strings.Contains(got, "https://cos.example/agent-attachments/1/x.txt") {
		t.Errorf("output must contain the URL; got %q", got)
	}
}

// TestLoadSessionHistory_ReturnsPriorTurnsChronological verifies the multi-turn
// context fix end-to-end at the biz layer: prior runs of the same session are
// loaded, ordered chronologically, the current run is excluded, and tool/empty
// turns are dropped. This is the integration counterpart to the buildEinoMessages
// unit repro (runner_history_test.go).
func TestLoadSessionHistory_ReturnsPriorTurnsChronological(t *testing.T) {
	runStore := newLifecycleRunStore()
	svc := NewStudentRunService(nil, runStore, nil, nil, nil, nil)

	const sid = "session-abc"
	base := time.Date(2026, 6, 8, 16, 0, 0, 0, time.UTC)

	// Turn 1 (oldest): user asks for research, assistant answers.
	runStore.runs[1] = &model.AgentRun{
		ID: 1, SessionID: sid, StartedAt: base, Status: "terminated",
		Messages: datatypes.JSON([]byte(`[{"role":"user","content":"帮我做调研"},{"role":"assistant","content":"调研结果A"}]`)),
	}
	// Turn 2 (newer): a tool turn (must be dropped) + user + assistant.
	runStore.runs[2] = &model.AgentRun{
		ID: 2, SessionID: sid, StartedAt: base.Add(time.Minute), Status: "terminated",
		Messages: datatypes.JSON([]byte(`[{"role":"tool","content":"raw tool output"},{"role":"user","content":"做成PPT"},{"role":"assistant","content":"PPT已生成"}]`)),
	}
	// A run from a DIFFERENT session must be ignored.
	runStore.runs[3] = &model.AgentRun{
		ID: 3, SessionID: "other", StartedAt: base.Add(2 * time.Minute), Status: "terminated",
		Messages: datatypes.JSON([]byte(`[{"role":"user","content":"不相关"}]`)),
	}
	// The current run (excluded) with empty messages — must not appear.
	runStore.runs[4] = &model.AgentRun{
		ID: 4, SessionID: sid, StartedAt: base.Add(3 * time.Minute), Status: "running",
		Messages: datatypes.JSON([]byte(`[]`)),
	}
	// A still-running (non-terminal) prior row with partial content — must be
	// skipped (only terminated turns contribute history).
	runStore.runs[5] = &model.AgentRun{
		ID: 5, SessionID: sid, StartedAt: base.Add(30 * time.Second), Status: "running",
		Messages: datatypes.JSON([]byte(`[{"role":"user","content":"半截消息"}]`)),
	}
	// A soft-deleted prior turn — must be skipped.
	runStore.runs[6] = &model.AgentRun{
		ID: 6, SessionID: sid, StartedAt: base.Add(20 * time.Second), Status: "terminated", IsDeleted: true,
		Messages: datatypes.JSON([]byte(`[{"role":"user","content":"已删除"},{"role":"assistant","content":"已删除回复"}]`)),
	}

	msgs := svc.loadSessionHistory(context.Background(), sid, 4)

	want := []struct {
		role    schema.RoleType
		content string
	}{
		{schema.User, "帮我做调研"},
		{schema.Assistant, "调研结果A"},
		{schema.User, "做成PPT"},
		{schema.Assistant, "PPT已生成"},
	}
	if len(msgs) != len(want) {
		t.Fatalf("expected %d history messages, got %d: %+v", len(want), len(msgs), msgs)
	}
	for i, w := range want {
		if msgs[i].Role != w.role || msgs[i].Content != w.content {
			t.Errorf("msgs[%d] = {%v,%q}, want {%v,%q}", i, msgs[i].Role, msgs[i].Content, w.role, w.content)
		}
	}
}

// TestLoadSessionHistory_EmptySessionID returns nil (fail-open / first turn).
func TestLoadSessionHistory_EmptySessionID(t *testing.T) {
	svc := NewStudentRunService(nil, newLifecycleRunStore(), nil, nil, nil, nil)
	if got := svc.loadSessionHistory(context.Background(), "", 0); got != nil {
		t.Errorf("empty sessionID should return nil, got %+v", got)
	}
}

// TestLoadSessionHistory_StoreError_FailOpen verifies a ListBySession error does
// not block the run: loadSessionHistory returns nil instead of propagating.
func TestLoadSessionHistory_StoreError_FailOpen(t *testing.T) {
	runStore := newLifecycleRunStore()
	runStore.listErr = errors.New("db unavailable")
	svc := NewStudentRunService(nil, runStore, nil, nil, nil, nil)
	if got := svc.loadSessionHistory(context.Background(), "session-x", 0); got != nil {
		t.Errorf("store error must fail-open to nil, got %+v", got)
	}
}

// TestHistoryTurnText_Multimodal verifies an OAI-style array content value has its
// text parts extracted rather than being silently dropped.
func TestHistoryTurnText_Multimodal(t *testing.T) {
	if got := historyTurnText("plain string"); got != "plain string" {
		t.Errorf("string content: got %q", got)
	}
	arr := []any{
		map[string]any{"type": "text", "text": "看看这张图："},
		map[string]any{"type": "image_url", "image_url": map[string]any{"url": "https://x/y.png"}},
		map[string]any{"type": "text", "text": "谢谢"},
	}
	if got := historyTurnText(arr); got != "看看这张图：谢谢" {
		t.Errorf("multimodal content: got %q, want concatenated text parts", got)
	}
	if got := historyTurnText(map[string]any{"unexpected": true}); got != "" {
		t.Errorf("unknown content shape should yield empty, got %q", got)
	}
}
