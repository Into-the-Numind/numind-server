package narration

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func mkEvent(runID uint64, callID string) Event {
	return Event{
		RunID:      runID,
		ToolCallID: callID,
		ToolName:   "bash_exec",
		State:      StateUse,
		Message:    "msg",
		Timestamp:  time.Unix(0, 0),
	}
}

func TestMemStreamer_SendThenSubscribe_DeliversInOrder(t *testing.T) {
	s := newMemStreamer(10)
	for i := 0; i < 3; i++ {
		s.Send(mkEvent(1, fmt.Sprintf("1-%d", i)))
	}
	ch, cleanup := s.Subscribe(1)
	defer cleanup()
	for i := 0; i < 3; i++ {
		select {
		case ev := <-ch:
			want := fmt.Sprintf("1-%d", i)
			if ev.ToolCallID != want {
				t.Errorf("event %d: want ToolCallID %q, got %q", i, want, ev.ToolCallID)
			}
		case <-time.After(50 * time.Millisecond):
			t.Fatalf("timeout waiting for event %d", i)
		}
	}
}

func TestMemStreamer_SubscribeThenSend_DeliversInOrder(t *testing.T) {
	s := newMemStreamer(10)
	ch, cleanup := s.Subscribe(2)
	defer cleanup()
	for i := 0; i < 3; i++ {
		s.Send(mkEvent(2, fmt.Sprintf("2-%d", i)))
	}
	for i := 0; i < 3; i++ {
		select {
		case ev := <-ch:
			want := fmt.Sprintf("2-%d", i)
			if ev.ToolCallID != want {
				t.Errorf("event %d: want %q, got %q", i, want, ev.ToolCallID)
			}
		case <-time.After(50 * time.Millisecond):
			t.Fatalf("timeout waiting for event %d", i)
		}
	}
}

func TestMemStreamer_BufferOverflow_DropsOldest(t *testing.T) {
	s := newMemStreamer(1)
	s.Send(mkEvent(3, "first"))
	s.Send(mkEvent(3, "second")) // drops "first"
	s.Send(mkEvent(3, "third"))  // drops "second"
	ch, cleanup := s.Subscribe(3)
	defer cleanup()
	select {
	case ev := <-ch:
		if ev.ToolCallID != "third" {
			t.Errorf("want last-survivor 'third', got %q", ev.ToolCallID)
		}
	case <-time.After(50 * time.Millisecond):
		t.Fatal("timeout — buffer should have 1 element (the newest)")
	}
}

func TestMemStreamer_CloseRun_Idempotent(t *testing.T) {
	s := newMemStreamer(10)
	s.Send(mkEvent(4, "4-1"))
	s.CloseRun(4)
	s.CloseRun(4) // must not panic
}

func TestMemStreamer_SendAfterClose_NoPanic(t *testing.T) {
	s := newMemStreamer(10)
	s.Send(mkEvent(5, "5-1"))
	s.CloseRun(5)
	// Sending after close to the SAME runID should lazy-create a new channel
	// (S2-D2 documented behavior); event is delivered to the new orphan.
	// What we really test here is that we don't panic.
	s.Send(mkEvent(5, "5-2"))
}

func TestMemStreamer_SendOnClosedChannelDirectly_NoPanic(t *testing.T) {
	// Direct test of runChannel.send guard against send-on-closed-channel panic.
	rc := &runChannel{ch: make(chan Event, 1)}
	rc.close()
	dropped := rc.send(Event{RunID: 99, ToolCallID: "99-1"})
	if !dropped {
		t.Error("send on closed channel should report dropped=true")
	}
}

func TestMemStreamer_ConcurrentSendClose_RaceFree(t *testing.T) {
	// Race detector exercises the closed.Load + defer recover guard.
	// Spawn many concurrent sends and concurrent CloseRun on different runIDs.
	s := newMemStreamer(8)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		runID := uint64(100 + i)
		wg.Add(2)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				s.Send(mkEvent(runID, "x"))
			}
		}()
		go func() {
			defer wg.Done()
			s.CloseRun(runID)
		}()
	}
	wg.Wait()
}

func TestMemStreamer_SubscribeUnknownRun_LazyCreates(t *testing.T) {
	s := newMemStreamer(10)
	ch, cleanup := s.Subscribe(999)
	defer cleanup()
	if ch == nil {
		t.Fatal("Subscribe should return non-nil channel for unknown runID")
	}
	// Channel should be empty + open
	select {
	case <-ch:
		t.Fatal("channel should be empty on lazy-create")
	case <-time.After(10 * time.Millisecond):
		// expected: blocks (no events ever sent)
	}
}

func TestMemStreamer_RunChannelClose_Idempotent(t *testing.T) {
	rc := &runChannel{ch: make(chan Event, 1)}
	rc.close()
	rc.close() // mutex+closed-bool guard makes this a no-op
}

func TestMemStreamer_SubscribeAfterCloseRun_GetsNewOpenChan(t *testing.T) {
	// S2-D2 verification: Subscribe of a closed runID returns a NEW open
	// channel (lazy-create semantic). The channel will never receive events
	// because no further Send is expected.
	s := newMemStreamer(10)
	s.Send(mkEvent(7, "7-1"))
	s.CloseRun(7)

	ch, cleanup := s.Subscribe(7)
	defer cleanup()
	// Channel should be open and empty (NEW channel, not the closed original).
	select {
	case _, ok := <-ch:
		if !ok {
			t.Fatal("S2-D2 violated: Subscribe after CloseRun returned closed channel; expected new open channel")
		}
	case <-time.After(30 * time.Millisecond):
		// Expected: empty + open → blocks
	}
}
