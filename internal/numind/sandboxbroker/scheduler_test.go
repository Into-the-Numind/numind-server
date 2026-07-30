package sandboxbroker

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSchedulerFiveSlotsAndSixthTakesReleasedHeadSlot(t *testing.T) {
	scheduler := newScheduler(5, 5, time.Second)
	for index := 0; index < 5; index++ {
		if err := scheduler.Acquire(
			context.Background(),
			testSchedulerRequest(index, "owner-blue"),
		); err != nil {
			t.Fatal(err)
		}
	}
	assertSchedulerCounts(t, scheduler, 5, 0, 0)

	sixthDone := make(chan error, 1)
	go func() {
		sixthDone <- scheduler.Acquire(
			context.Background(),
			testSchedulerRequest(5, "owner-green"),
		)
	}()
	waitForSchedulerQueue(t, scheduler, []string{"request-5"})
	assertSchedulerBlocked(t, sixthDone)

	if err := scheduler.Release("lease-0"); err != nil {
		t.Fatal(err)
	}
	if err := receiveSchedulerResult(t, sixthDone); err != nil {
		t.Fatal(err)
	}
	assertSchedulerCounts(t, scheduler, 5, 0, 0)
}

func TestSchedulerQueueTimeoutDoesNotLeakSlot(t *testing.T) {
	scheduler := newScheduler(1, 1, 30*time.Millisecond)
	if err := scheduler.Acquire(
		context.Background(),
		testSchedulerRequest(0, "owner-blue"),
	); err != nil {
		t.Fatal(err)
	}
	err := scheduler.Acquire(
		context.Background(),
		testSchedulerRequest(1, "owner-blue"),
	)
	if !errors.Is(err, ErrSchedulerQueueTimeout) {
		t.Fatalf("Acquire error = %v; want queue timeout", err)
	}
	assertSchedulerCounts(t, scheduler, 1, 0, 0)
}

func TestSchedulerCancellationRemovesOnlyCancelledFIFOEntry(t *testing.T) {
	scheduler := newScheduler(1, 1, time.Second)
	if err := scheduler.Acquire(
		context.Background(),
		testSchedulerRequest(0, "owner-blue"),
	); err != nil {
		t.Fatal(err)
	}

	secondContext, cancelSecond := context.WithCancel(context.Background())
	secondDone := make(chan error, 1)
	go func() {
		secondDone <- scheduler.Acquire(
			secondContext,
			testSchedulerRequest(1, "owner-blue"),
		)
	}()
	waitForSchedulerQueue(t, scheduler, []string{"request-1"})

	thirdDone := make(chan error, 1)
	go func() {
		thirdDone <- scheduler.Acquire(
			context.Background(),
			testSchedulerRequest(2, "owner-blue"),
		)
	}()
	waitForSchedulerQueue(t, scheduler, []string{"request-1", "request-2"})

	cancelSecond()
	if err := receiveSchedulerResult(t, secondDone); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled Acquire error = %v", err)
	}
	waitForSchedulerQueue(t, scheduler, []string{"request-2"})
	assertSchedulerBlocked(t, thirdDone)

	if err := scheduler.Release("lease-0"); err != nil {
		t.Fatal(err)
	}
	if err := receiveSchedulerResult(t, thirdDone); err != nil {
		t.Fatal(err)
	}
	assertSchedulerCounts(t, scheduler, 1, 0, 0)
}

func TestSchedulerReleaseGrantsStrictFIFOHeadOnly(t *testing.T) {
	scheduler := newScheduler(1, 1, time.Second)
	if err := scheduler.Acquire(
		context.Background(),
		testSchedulerRequest(0, "owner-blue"),
	); err != nil {
		t.Fatal(err)
	}

	secondDone := make(chan error, 1)
	go func() {
		secondDone <- scheduler.Acquire(
			context.Background(),
			testSchedulerRequest(1, "owner-green"),
		)
	}()
	waitForSchedulerQueue(t, scheduler, []string{"request-1"})

	thirdDone := make(chan error, 1)
	go func() {
		thirdDone <- scheduler.Acquire(
			context.Background(),
			testSchedulerRequest(2, "owner-blue"),
		)
	}()
	waitForSchedulerQueue(t, scheduler, []string{"request-1", "request-2"})

	if err := scheduler.Release("lease-0"); err != nil {
		t.Fatal(err)
	}
	if err := receiveSchedulerResult(t, secondDone); err != nil {
		t.Fatal(err)
	}
	assertSchedulerBlocked(t, thirdDone)
	waitForSchedulerQueue(t, scheduler, []string{"request-2"})

	if err := scheduler.Release("lease-1"); err != nil {
		t.Fatal(err)
	}
	if err := receiveSchedulerResult(t, thirdDone); err != nil {
		t.Fatal(err)
	}
	assertSchedulerCounts(t, scheduler, 1, 0, 0)
}

func TestSchedulerFiveActiveTasksDoNotCreateStandby(t *testing.T) {
	scheduler := newScheduler(6, 5, time.Second)
	for index := 0; index < 5; index++ {
		request := testSchedulerRequest(index, "owner-blue")
		if err := scheduler.Acquire(context.Background(), request); err != nil {
			t.Fatal(err)
		}
		if err := scheduler.MarkReady(request.LeaseID); err != nil {
			t.Fatal(err)
		}
		if err := scheduler.Activate(request.LeaseID); err != nil {
			t.Fatal(err)
		}
	}
	assertSchedulerCounts(t, scheduler, 5, 5, 0)

	standbyDone := make(chan error, 1)
	go func() {
		standbyDone <- scheduler.Acquire(
			context.Background(),
			testSchedulerRequest(5, "owner-green"),
		)
	}()
	waitForSchedulerQueue(t, scheduler, []string{"request-5"})
	assertSchedulerBlocked(t, standbyDone)

	if err := scheduler.Release("lease-0"); err != nil {
		t.Fatal(err)
	}
	if err := receiveSchedulerResult(t, standbyDone); err != nil {
		t.Fatal(err)
	}
	assertSchedulerCounts(t, scheduler, 5, 4, 0)
}

func TestSchedulerOwnersShareOneGlobalLimit(t *testing.T) {
	scheduler := newScheduler(5, 5, time.Second)
	owners := []string{
		"api-blue",
		"api-green",
		"api-blue",
		"api-green",
		"api-blue",
	}
	for index, owner := range owners {
		if err := scheduler.Acquire(
			context.Background(),
			testSchedulerRequest(index, owner),
		); err != nil {
			t.Fatal(err)
		}
	}

	waitContext, cancelWait := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- scheduler.Acquire(
			waitContext,
			testSchedulerRequest(5, "api-green"),
		)
	}()
	waitForSchedulerQueue(t, scheduler, []string{"request-5"})
	assertSchedulerCounts(t, scheduler, 5, 0, 1)
	cancelWait()
	if err := receiveSchedulerResult(t, done); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled Acquire error = %v", err)
	}
}

func TestSchedulerRequestReplayNeverOccupiesTwoSlots(t *testing.T) {
	scheduler := newScheduler(1, 1, time.Second)
	first := testSchedulerRequest(0, "owner-blue")
	if err := scheduler.Acquire(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	if err := scheduler.Acquire(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	assertSchedulerCounts(t, scheduler, 1, 0, 0)

	conflict := first
	conflict.LeaseID = "different-lease"
	if err := scheduler.Acquire(context.Background(), conflict); !errors.Is(
		err,
		ErrSchedulerIdempotencyConflict,
	) {
		t.Fatalf("conflicting replay error = %v", err)
	}

	queued := testSchedulerRequest(1, "owner-green")
	firstReplay := make(chan error, 1)
	secondReplay := make(chan error, 1)
	go func() {
		firstReplay <- scheduler.Acquire(context.Background(), queued)
	}()
	waitForSchedulerQueue(t, scheduler, []string{"request-1"})
	go func() {
		secondReplay <- scheduler.Acquire(context.Background(), queued)
	}()

	if err := scheduler.Release(first.LeaseID); err != nil {
		t.Fatal(err)
	}
	if err := receiveSchedulerResult(t, firstReplay); err != nil {
		t.Fatal(err)
	}
	if err := receiveSchedulerResult(t, secondReplay); err != nil {
		t.Fatal(err)
	}
	assertSchedulerCounts(t, scheduler, 1, 0, 0)
}

func TestSchedulerConcurrentAcquireNeverExceedsLimit(t *testing.T) {
	const (
		workers = 40
		limit   = 5
	)
	scheduler := newScheduler(limit, limit, 3*time.Second)
	start := make(chan struct{})
	var running atomic.Int64
	var observed atomic.Int64
	var waitGroup sync.WaitGroup
	errorsFound := make(chan error, workers)

	for index := 0; index < workers; index++ {
		waitGroup.Add(1)
		go func(index int) {
			defer waitGroup.Done()
			<-start
			request := testSchedulerRequest(index, "owner-shared")
			if err := scheduler.Acquire(context.Background(), request); err != nil {
				errorsFound <- err
				return
			}
			current := running.Add(1)
			for {
				maximum := observed.Load()
				if current <= maximum || observed.CompareAndSwap(maximum, current) {
					break
				}
			}
			time.Sleep(time.Millisecond)
			running.Add(-1)
			if err := scheduler.Release(request.LeaseID); err != nil {
				errorsFound <- err
			}
		}(index)
	}
	close(start)
	waitGroup.Wait()
	close(errorsFound)
	for err := range errorsFound {
		t.Error(err)
	}
	if maximum := observed.Load(); maximum > limit {
		t.Fatalf("observed %d concurrent slots; limit is %d", maximum, limit)
	}
	assertSchedulerCounts(t, scheduler, 0, 0, 0)
}

func TestSchedulerRejectsUnsafeTransitionsAndIdentity(t *testing.T) {
	scheduler := NewScheduler()
	if err := scheduler.Acquire(context.Background(), SchedulerRequest{}); !errors.Is(
		err,
		ErrInvalidSchedulerRequest,
	) {
		t.Fatalf("empty request error = %v", err)
	}
	request := testSchedulerRequest(0, "owner-blue")
	if err := scheduler.Acquire(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if err := scheduler.Activate(request.LeaseID); !errors.Is(
		err,
		ErrInvalidSchedulerTransition,
	) {
		t.Fatalf("activate before ready error = %v", err)
	}
	if err := scheduler.MarkReady(request.LeaseID); err != nil {
		t.Fatal(err)
	}
	if err := scheduler.MarkReady(request.LeaseID); err != nil {
		t.Fatalf("ready replay error = %v", err)
	}
	if err := scheduler.Activate(request.LeaseID); err != nil {
		t.Fatal(err)
	}
	if err := scheduler.Activate(request.LeaseID); err != nil {
		t.Fatalf("activate replay error = %v", err)
	}
	if err := scheduler.Release("missing"); !errors.Is(err, ErrSchedulerLeaseNotFound) {
		t.Fatalf("missing release error = %v", err)
	}
	if err := scheduler.Release(request.LeaseID); err != nil {
		t.Fatal(err)
	}
	if err := scheduler.Release(request.LeaseID); err != nil {
		t.Fatalf("release replay error = %v", err)
	}
}

func testSchedulerRequest(index int, owner string) SchedulerRequest {
	return SchedulerRequest{
		RequestID: fmt.Sprintf("request-%d", index),
		LeaseID:   fmt.Sprintf("lease-%d", index),
		OwnerID:   owner,
	}
}

func assertSchedulerCounts(
	t *testing.T,
	scheduler *Scheduler,
	containers int,
	active int,
	queued int,
) {
	t.Helper()
	snapshot := scheduler.Snapshot()
	if snapshot.Containers != containers ||
		snapshot.Active != active ||
		snapshot.Queued != queued {
		t.Fatalf(
			"snapshot = %#v; want containers=%d active=%d queued=%d",
			snapshot,
			containers,
			active,
			queued,
		)
	}
}

func waitForSchedulerQueue(
	t *testing.T,
	scheduler *Scheduler,
	requestIDs []string,
) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		snapshot := scheduler.Snapshot()
		if equalStrings(snapshot.QueueRequestIDs, requestIDs) {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf(
		"queue = %v; want %v",
		scheduler.Snapshot().QueueRequestIDs,
		requestIDs,
	)
}

func assertSchedulerBlocked(t *testing.T, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		t.Fatalf("scheduler call returned early: %v", err)
	case <-time.After(10 * time.Millisecond):
	}
}

func receiveSchedulerResult(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(2 * time.Second):
		t.Fatal("scheduler call did not return")
		return nil
	}
}

func equalStrings(left []string, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
