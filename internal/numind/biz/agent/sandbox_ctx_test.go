package agent

import (
	"context"
	"sync"
	"testing"
)

func TestWithRunID_Roundtrip(t *testing.T) {
	ctx := WithRunID(context.Background(), 42)
	if got := RunIDFromContext(ctx); got != 42 {
		t.Errorf("RunIDFromContext = %d; want 42", got)
	}
}

func TestRunIDFromContext_AbsentReturnsZero(t *testing.T) {
	if got := RunIDFromContext(context.Background()); got != 0 {
		t.Errorf("RunIDFromContext on plain ctx = %d; want 0", got)
	}
}

func TestRunIDFromContext_WrongTypeReturnsZero(t *testing.T) {
	// If someone misuses WithValue with the same key but wrong type,
	// the type assertion in RunIDFromContext should fall through to 0.
	type stranger struct{}
	ctx := context.WithValue(context.Background(), stranger{}, "not-a-runID")
	if got := RunIDFromContext(ctx); got != 0 {
		t.Errorf("RunIDFromContext with stranger key = %d; want 0", got)
	}
}

func TestSetDefaultHookManager_ConcurrentRaceDetector(t *testing.T) {
	// Reset
	SetDefaultHookManager(nil)
	t.Cleanup(func() { SetDefaultHookManager(nil) })

	const goroutines = 50
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func(i int) {
			defer wg.Done()
			if i%2 == 0 {
				SetDefaultHookManager(&SandboxHookManager{})
			} else {
				_ = DefaultHookManager()
			}
		}(i)
	}
	wg.Wait()
}

func TestSandboxSessionForCurrentCall_NoManager(t *testing.T) {
	SetDefaultHookManager(nil)
	t.Cleanup(func() { SetDefaultHookManager(nil) })
	if got := sandboxSessionForCurrentCall(context.Background(), "bash_exec"); got != nil {
		t.Errorf("no manager → got non-nil session: %v", got)
	}
}

func TestSandboxSessionForCurrentCall_NoRunID(t *testing.T) {
	SetDefaultHookManager(&SandboxHookManager{})
	t.Cleanup(func() { SetDefaultHookManager(nil) })
	// ctx has no runID
	if got := sandboxSessionForCurrentCall(context.Background(), "bash_exec"); got != nil {
		t.Errorf("no runID → got non-nil session: %v", got)
	}
}

func TestDockerClientForCurrentCall_NoManager(t *testing.T) {
	SetDefaultHookManager(nil)
	t.Cleanup(func() { SetDefaultHookManager(nil) })
	if got := dockerClientForCurrentCall(context.Background()); got != nil {
		t.Errorf("no manager → got non-nil dc: %v", got)
	}
}
