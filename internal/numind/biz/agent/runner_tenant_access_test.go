package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"numind-server/internal/numind/biz/agent/stream"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/model"
)

// B2B2C tenant-access integration tests for the two runner-level gates:
//   - gate #2: agentRunner.Run        (runner.go — the line answer.go's
//                                       ask_user_question resume path relies on)
//   - gate #3: agentRunner.RunStream  (production streaming path)
// gate #1 (resolveDefinition) is covered in student_run_lifecycle_test.go.
//
// Each gate is proven to consult the wired userStore by the contrast between
// "child + active + userStore wired → allowed" and "child + active +
// userStore nil → denied" — without consulting the caller's parent_user_id a
// child can never be recognized.

const (
	taParentID      = uint(10)
	taChildID       = uint(20)
	taOtherParentID = uint(99)
)

// childUserRec builds a learner User whose parent is parentID.
func childUserRec(childID, parentID uint) *model.User {
	u := &model.User{ParentUserID: uptr(parentID)}
	u.ID = childID
	return u
}

// gateSkillStore returns a skill store holding one agent owned by parentID.
func gateSkillStore(adID uint64, parentID uint, active bool) *lifecycleSkillStore {
	s := newLifecycleSkillStore()
	s.defs[adID] = &model.AgentDefinition{ID: adID, ParentUserID: parentID, IsActive: active}
	return s
}

// gateDenialCases are the deny scenarios shared by both runner gates.
func gateDenialCases(adID uint64) []struct {
	name  string
	store *lifecycleSkillStore
	users userByIDGetter
} {
	return []struct {
		name  string
		store *lifecycleSkillStore
		users userByIDGetter
	}{
		{"child + inactive agent (R9)", gateSkillStore(adID, taParentID, false), &mockUserByIDGetter{user: childUserRec(taChildID, taParentID)}},
		{"child + other tenant", gateSkillStore(adID, taOtherParentID, true), &mockUserByIDGetter{user: childUserRec(taChildID, taParentID)}},
		{"child + active but userStore unwired", gateSkillStore(adID, taParentID, true), nil},
	}
}

// ---- gate #2: agentRunner.Run ----

func TestRun_ChildOfParent_PassesAccessGate(t *testing.T) {
	const adID = uint64(7)
	r := &agentRunner{
		runStore:   newMockStore(),
		cancels:    make(map[uint64]context.CancelFunc),
		skillStore: gateSkillStore(adID, taParentID, true),
		userStore:  &mockUserByIDGetter{user: childUserRec(taChildID, taParentID)},
		// registry nil → short-circuit immediately after the access gate passes
	}
	result, err := r.Run(context.Background(), RunRequest{
		UserID:            taChildID,
		AgentDefinitionID: adID,
		Input:             "hi",
	})
	require.NoError(t, err) // gate allowed the child of the owning parent
	assert.Equal(t, TerminalCompleted, result.TerminalReason)
}

func TestRun_TenantAccessDenials(t *testing.T) {
	const adID = uint64(7)
	for _, tc := range gateDenialCases(adID) {
		t.Run(tc.name, func(t *testing.T) {
			r := &agentRunner{
				runStore:   newMockStore(),
				cancels:    make(map[uint64]context.CancelFunc),
				skillStore: tc.store,
				userStore:  tc.users,
			}
			_, err := r.Run(context.Background(), RunRequest{
				UserID:            taChildID,
				AgentDefinitionID: adID,
				Input:             "x",
			})
			if !errors.Is(err, errno.ErrSkillNotFound) {
				t.Fatalf("expected ErrSkillNotFound, got %v", err)
			}
		})
	}
}

// TestRun_ChildResume_AccessGate covers the ask_user_question Answer/resume path
// (spec §5 case 10): answer.go re-enters runner.Run with ExistingRunID set, so
// the gate fires again on resume. Confirms a child resumes an active parent
// agent, and the documented mid-session de-list trap (S2-D6): R9 still denies
// resume of a now-inactive agent (Cancel — not exercised here — remains the
// child's escape since it skips the access gate).
func TestRun_ChildResume_AccessGate(t *testing.T) {
	const adID = uint64(9)
	childUsers := func() userByIDGetter {
		return &mockUserByIDGetter{user: childUserRec(taChildID, taParentID)}
	}

	t.Run("active parent agent → resume allowed", func(t *testing.T) {
		ms := newMockStore()
		run := makeStreamRun(t, ms, taChildID)
		r := &agentRunner{
			runStore:   ms,
			cancels:    make(map[uint64]context.CancelFunc),
			skillStore: gateSkillStore(adID, taParentID, true),
			userStore:  childUsers(),
		}
		result, err := r.Run(context.Background(), RunRequest{
			UserID:            taChildID,
			AgentDefinitionID: adID,
			ExistingRunID:     run.ID,
			Input:             "my answer",
		})
		require.NoError(t, err)
		assert.Equal(t, TerminalCompleted, result.TerminalReason)
	})

	t.Run("de-listed mid-session → resume denied (R9 trap)", func(t *testing.T) {
		ms := newMockStore()
		run := makeStreamRun(t, ms, taChildID)
		r := &agentRunner{
			runStore:   ms,
			cancels:    make(map[uint64]context.CancelFunc),
			skillStore: gateSkillStore(adID, taParentID, false),
			userStore:  childUsers(),
		}
		_, err := r.Run(context.Background(), RunRequest{
			UserID:            taChildID,
			AgentDefinitionID: adID,
			ExistingRunID:     run.ID,
			Input:             "my answer",
		})
		if !errors.Is(err, errno.ErrSkillNotFound) {
			t.Fatalf("expected R9 denial resuming a de-listed agent, got %v", err)
		}
	})
}

// ---- gate #3: agentRunner.RunStream (production streaming path) ----

func makeStreamRun(t *testing.T, ms *mockAgentRunStore, userID uint) *model.AgentRun {
	t.Helper()
	run := &model.AgentRun{UserID: userID, SessionID: "sess-b2b2c", Status: "running"}
	require.NoError(t, ms.Create(context.Background(), run))
	require.NotZero(t, run.ID)
	return run
}

func TestRunStream_ChildOfParent_PassesAccessGate(t *testing.T) {
	const adID = uint64(8)
	ms := newMockStore()
	run := makeStreamRun(t, ms, taChildID)
	r := &agentRunner{
		runStore:   ms,
		cancels:    make(map[uint64]context.CancelFunc),
		skillStore: gateSkillStore(adID, taParentID, true),
		userStore:  &mockUserByIDGetter{user: childUserRec(taChildID, taParentID)},
	}
	ch := make(chan stream.Event, 256)
	result, err := r.RunStream(context.Background(), RunRequest{
		UserID:            taChildID,
		AgentDefinitionID: adID,
		Input:             "hi",
	}, run.ID, ch)
	close(ch)
	require.NoError(t, err) // gate allowed the child on the streaming path
	assert.Equal(t, TerminalCompleted, result.TerminalReason)
}

func TestRunStream_TenantAccessDenials(t *testing.T) {
	const adID = uint64(8)
	for _, tc := range gateDenialCases(adID) {
		t.Run(tc.name, func(t *testing.T) {
			ms := newMockStore()
			run := makeStreamRun(t, ms, taChildID)
			r := &agentRunner{
				runStore:   ms,
				cancels:    make(map[uint64]context.CancelFunc),
				skillStore: tc.store,
				userStore:  tc.users,
			}
			ch := make(chan stream.Event, 256)
			_, err := r.RunStream(context.Background(), RunRequest{
				UserID:            taChildID,
				AgentDefinitionID: adID,
				Input:             "x",
			}, run.ID, ch)
			close(ch)
			if !errors.Is(err, errno.ErrSkillNotFound) {
				t.Fatalf("expected ErrSkillNotFound, got %v", err)
			}
		})
	}
}
