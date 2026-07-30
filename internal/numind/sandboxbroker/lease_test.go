package sandboxbroker

import (
	"errors"
	"math"
	"testing"
	"time"
)

func TestLeaseTransitionTable(t *testing.T) {
	legal := map[[2]LeaseState]bool{
		{LeaseQueued, LeaseCreating}:                  true,
		{LeaseQueued, LeaseDestroying}:                true,
		{LeaseCreating, LeaseReady}:                   true,
		{LeaseCreating, LeaseDestroying}:              true,
		{LeaseCreating, LeaseRecoveryPending}:         true,
		{LeaseReady, LeaseActive}:                     true,
		{LeaseReady, LeaseDestroying}:                 true,
		{LeaseReady, LeaseRecoveryPending}:            true,
		{LeaseActive, LeaseOutputPersisting}:          true,
		{LeaseActive, LeaseDestroying}:                true,
		{LeaseActive, LeaseRecoveryPending}:           true,
		{LeaseOutputPersisting, LeaseDestroying}:      true,
		{LeaseOutputPersisting, LeaseRecoveryPending}: true,
		{LeaseDestroying, LeaseTerminated}:            true,
		{LeaseDestroying, LeaseRecoveryPending}:       true,
		{LeaseRecoveryPending, LeaseDestroying}:       true,
		{LeaseRecoveryPending, LeaseTerminated}:       true,
	}
	states := []LeaseState{
		LeaseQueued,
		LeaseCreating,
		LeaseReady,
		LeaseActive,
		LeaseOutputPersisting,
		LeaseDestroying,
		LeaseTerminated,
		LeaseRecoveryPending,
	}
	for _, from := range states {
		if !IsValidLeaseState(from) {
			t.Fatalf("state %q is not valid", from)
		}
		for _, to := range states {
			got := CanTransition(from, to)
			if got != legal[[2]LeaseState{from, to}] {
				t.Errorf("CanTransition(%q,%q) = %v", from, to, got)
			}
		}
	}
	if IsValidLeaseState("unknown") {
		t.Fatal("unknown state accepted")
	}
}

func TestValidateCreateLeaseParams(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	valid := CreateLeaseParams{
		LeaseID:     "lease-1",
		RequestID:   "request-1",
		PeerUID:     1000,
		OwnerID:     "api-primary",
		OwnerBootID: "boot-1",
		CreatedAt:   now,
		ExpiresAt:   now.Add(time.Minute),
	}
	tests := []struct {
		name   string
		mutate func(*CreateLeaseParams)
	}{
		{name: "empty lease", mutate: func(p *CreateLeaseParams) { p.LeaseID = "" }},
		{name: "empty request", mutate: func(p *CreateLeaseParams) { p.RequestID = "" }},
		{name: "negative peer", mutate: func(p *CreateLeaseParams) { p.PeerUID = -1 }},
		{name: "empty owner", mutate: func(p *CreateLeaseParams) { p.OwnerID = "" }},
		{name: "empty boot", mutate: func(p *CreateLeaseParams) { p.OwnerBootID = "" }},
		{name: "bound create", mutate: func(p *CreateLeaseParams) {
			p.AgentRunID = 1
			p.SandboxSessionID = 2
		}},
		{name: "overflow binding", mutate: func(p *CreateLeaseParams) {
			p.AgentRunID = math.MaxInt64 + 1
			p.SandboxSessionID = math.MaxInt64 + 1
		}},
		{name: "zero expiry", mutate: func(p *CreateLeaseParams) { p.ExpiresAt = time.Time{} }},
		{name: "expired", mutate: func(p *CreateLeaseParams) { p.ExpiresAt = now }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params := valid
			tt.mutate(&params)
			if !errors.Is(validateCreateLeaseParams(params), ErrInvalidLease) {
				t.Fatalf("validateCreateLeaseParams(%s) accepted invalid input", tt.name)
			}
		})
	}
	if err := validateCreateLeaseParams(valid); err != nil {
		t.Fatalf("valid create rejected: %v", err)
	}
}

func TestValidateTransitionRequiresStateSpecificFields(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	current := Lease{
		LeaseID:   "lease-1",
		State:     LeaseCreating,
		UpdatedAt: now,
		ExpiresAt: now.Add(time.Minute),
	}
	containerID := "container-1"
	expiresAt := now.Add(2 * time.Minute)
	valid := TransitionParams{
		LeaseID:     current.LeaseID,
		RequestID:   "ready-1",
		To:          LeaseReady,
		At:          now.Add(time.Second),
		ContainerID: &containerID,
		ExpiresAt:   &expiresAt,
	}
	if err := validateTransitionParams(current, valid); err != nil {
		t.Fatalf("valid ready transition rejected: %v", err)
	}
	for _, mutate := range []func(*TransitionParams){
		func(p *TransitionParams) { p.ContainerID = nil },
		func(p *TransitionParams) { p.ExpiresAt = nil },
		func(p *TransitionParams) { p.At = now.Add(-time.Second) },
		func(p *TransitionParams) { p.To = LeaseActive },
	} {
		params := valid
		mutate(&params)
		if !errors.Is(validateTransitionParams(current, params), ErrInvalidTransition) {
			t.Fatalf("invalid transition accepted: %#v", params)
		}
	}

	activeCurrent := current
	activeCurrent.State = LeaseReady
	activeCurrent.ContainerID = containerID
	runID := uint64(7)
	sessionID := uint64(9)
	activeAt := now.Add(2 * time.Second)
	activeExpiry := activeAt.Add(MaxActiveLeaseDuration)
	active := TransitionParams{
		LeaseID:          current.LeaseID,
		RequestID:        "active-1",
		To:               LeaseActive,
		At:               activeAt,
		AgentRunID:       &runID,
		SandboxSessionID: &sessionID,
		ExpiresAt:        &activeExpiry,
	}
	if err := validateTransitionParams(activeCurrent, active); err != nil {
		t.Fatalf("valid active transition rejected: %v", err)
	}
	active.AgentRunID = nil
	if !errors.Is(validateTransitionParams(activeCurrent, active), ErrInvalidTransition) {
		t.Fatal("active transition without run id accepted")
	}
	active.AgentRunID = &runID
	wrongActiveExpiry := activeExpiry.Add(time.Second)
	active.ExpiresAt = &wrongActiveExpiry
	if !errors.Is(validateTransitionParams(activeCurrent, active), ErrInvalidTransition) {
		t.Fatal("active transition without exact absolute expiry accepted")
	}
	active.ExpiresAt = &activeExpiry

	outputCurrent := activeCurrent
	outputCurrent.State = LeaseActive
	outputCurrent.AgentRunID = runID
	outputCurrent.SandboxSessionID = sessionID
	outputCurrent.ContainerID = containerID
	output := TransitionParams{
		LeaseID:   current.LeaseID,
		RequestID: "output-1",
		To:        LeaseOutputPersisting,
		At:        now.Add(3 * time.Second),
	}
	if err := validateTransitionParams(outputCurrent, output); err != nil {
		t.Fatalf("valid output transition rejected: %v", err)
	}
	reboundRunID := uint64(11)
	output.AgentRunID = &reboundRunID
	output.SandboxSessionID = &sessionID
	if !errors.Is(validateTransitionParams(outputCurrent, output), ErrInvalidTransition) {
		t.Fatal("post-activation binding change accepted")
	}
}

func TestValidateQueryLimit(t *testing.T) {
	for _, limit := range []int{0, -1, MaxJournalQueryLimit + 1} {
		if !errors.Is(validateQueryLimit(limit), ErrInvalidQueryLimit) {
			t.Fatalf("limit %d accepted", limit)
		}
	}
	if err := validateQueryLimit(MaxJournalQueryLimit); err != nil {
		t.Fatalf("max limit rejected: %v", err)
	}
}
