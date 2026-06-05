package agent

import (
	"context"
	"errors"
	"testing"

	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/model"
)

// uptr returns a pointer to a uint (for User.ParentUserID, a *uint).
func uptr(v uint) *uint { return &v }

// mockUserByIDGetter implements the narrow userByIDGetter interface for the
// B2B2C tenant-access tests. GetByID ignores its userID argument and returns
// the wired user/err verbatim.
type mockUserByIDGetter struct {
	user *model.User
	err  error
}

func (m *mockUserByIDGetter) GetByID(_ context.Context, _ uint) (*model.User, error) {
	return m.user, m.err
}

// agentDef builds a minimal AgentDefinition owned by parentID with the given
// active flag (the two fields agentTenantAccess inspects).
func agentDef(parentID uint, active bool) *model.AgentDefinition {
	return &model.AgentDefinition{ParentUserID: parentID, IsActive: active}
}

// TestAgentTenantAccess covers the full B2B2C access matrix (spec §2 truth
// table + S5 cases 1-7b, 9). This is the Rule 11 reproduction: case 3
// (child of owner, active agent) MUST be allowed — it fails against the
// parent-only stub and passes only once the real tenant rule lands.
func TestAgentTenantAccess(t *testing.T) {
	const parentID = uint(10)
	const childID = uint(20)
	const otherParentID = uint(99)

	// NOTE: mockUserByIDGetter.GetByID ignores its userID argument and returns
	// the wired user verbatim, so each case below pins the intended caller record
	// directly (the helper's branch logic, not the lookup key, is under test).
	//
	// childUser is a learner whose parent is parentID.
	childUser := &model.User{ParentUserID: uptr(parentID)}
	childUser.ID = childID
	// standaloneUser is a parent/standalone account (ParentUserID nil).
	standaloneUser := &model.User{}
	standaloneUser.ID = childID
	// callerParentUser is parentID's own record, ParentUserID nil (a parent).
	callerParentUser := &model.User{}
	callerParentUser.ID = parentID

	tests := []struct {
		name     string
		users    userByIDGetter
		callerID uint
		ad       *model.AgentDefinition
		wantErr  error // nil = allowed; otherwise errors.Is target
	}{
		{ // 1
			name:     "parent runs own active agent → allow",
			users:    &mockUserByIDGetter{user: callerParentUser},
			callerID: parentID,
			ad:       agentDef(parentID, true),
			wantErr:  nil,
		},
		{ // 2
			name:     "parent runs own inactive draft → allow (试聊)",
			users:    &mockUserByIDGetter{user: callerParentUser},
			callerID: parentID,
			ad:       agentDef(parentID, false),
			wantErr:  nil,
		},
		{ // 3 — CORE BUG / Rule 11 repro
			name:     "child runs parent active agent → allow",
			users:    &mockUserByIDGetter{user: childUser},
			callerID: childID,
			ad:       agentDef(parentID, true),
			wantErr:  nil,
		},
		{ // 4 — R9
			name:     "child runs parent inactive agent → deny (R9)",
			users:    &mockUserByIDGetter{user: childUser},
			callerID: childID,
			ad:       agentDef(parentID, false),
			wantErr:  errno.ErrSkillNotFound,
		},
		{ // 5 — cross-tenant
			name:     "child runs other-tenant agent → deny",
			users:    &mockUserByIDGetter{user: childUser},
			callerID: childID,
			ad:       agentDef(otherParentID, true),
			wantErr:  errno.ErrSkillNotFound,
		},
		{ // 6 — standalone (no parent) caller, not owner
			name:     "standalone user runs another's agent → deny",
			users:    &mockUserByIDGetter{user: standaloneUser},
			callerID: childID,
			ad:       agentDef(otherParentID, true),
			wantErr:  errno.ErrSkillNotFound,
		},
		{ // 7 — nil store + parent fast-path still works
			name:     "nil userStore + parent → allow (degrade)",
			users:    nil,
			callerID: parentID,
			ad:       agentDef(parentID, true),
			wantErr:  nil,
		},
		{ // 7b — nil store + child must NOT be allowed
			name:     "nil userStore + child → deny (degrade does not leak)",
			users:    nil,
			callerID: childID,
			ad:       agentDef(parentID, true),
			wantErr:  errno.ErrSkillNotFound,
		},
		{ // 9 — P1 nil-guard: parent runs ANOTHER parent's agent (slow path, *uint nil)
			name:     "parent runs other-parent agent → deny, no nil-panic",
			users:    &mockUserByIDGetter{user: callerParentUser},
			callerID: parentID,
			ad:       agentDef(otherParentID, true),
			wantErr:  errno.ErrSkillNotFound,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := agentTenantAccess(context.Background(), tc.users, tc.callerID, tc.ad)
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("expected access granted (nil), got %v", err)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("expected %v, got %v", tc.wantErr, err)
			}
		})
	}
}

// TestAgentTenantAccess_StoreError surfaces an unexpected store error (not
// record-not-found) as a wrapped error rather than a silent deny.
func TestAgentTenantAccess_StoreError(t *testing.T) {
	boom := errors.New("db down")
	users := &mockUserByIDGetter{err: boom}
	err := agentTenantAccess(context.Background(), users, 20, agentDef(10, true))
	if err == nil {
		t.Fatal("expected an error when the user lookup fails")
	}
	if errors.Is(err, errno.ErrSkillNotFound) {
		t.Fatalf("store error must not be masked as ErrSkillNotFound, got %v", err)
	}
	if !errors.Is(err, boom) {
		t.Fatalf("expected wrapped store error, got %v", err)
	}
}
