package diarize

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// msBytes returns the byte length of d milliseconds of 16k mono s16le PCM.
func msBytes(ms int) int { return ms * bytesPerMs }

// patternPCM builds n bytes whose value at index i is byte(start+i). Useful to
// assert exact bytes survived a copy / slice without corruption.
func patternPCM(start, n int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = byte(start + i)
	}
	return out
}

// ---------------------------------------------------------------------------
// P0-1: reused underlying buffer —每帧必须立即 copy，切出的字节不被后续读复用污染。
// ---------------------------------------------------------------------------

// TestWrite_ReusedUnderlyingBuffer_SliceBytesCorrect simulates the exact gorilla
// conn.ReadMessage hazard: the caller hands Write a slice backed by a buffer it
// then overwrites in place (as the next ReadMessage would). The ring must have
// copied the bytes on Write, so a later slice returns the ORIGINAL audio, not the
// overwritten garbage. This is the load-bearing P0-1 assertion (DIARIZATION_SPEC §4).
func TestWrite_ReusedUnderlyingBuffer_SliceBytesCorrect(t *testing.T) {
	b := NewSessionBuffer()

	// One shared backing buffer the "websocket read loop" reuses for every frame.
	frameLen := msBytes(100) // 100ms per frame
	shared := make([]byte, frameLen)

	// Frame 0: fill shared with a distinct pattern, Write it, then immediately
	// overwrite shared in place (as ReadMessage would on the next read).
	copy(shared, patternPCM(0, frameLen))
	b.Write(shared)
	wantFrame0 := patternPCM(0, frameLen) // capture the ORIGINAL bytes
	copy(shared, patternPCM(200, frameLen))
	b.Write(shared)
	wantFrame1 := patternPCM(200, frameLen)
	// Overwrite shared a third time to prove neither frame aliases it.
	copy(shared, patternPCM(99, frameLen))

	// Slice frame 0: [0,100)ms.
	got0 := b.slice(0, 100)
	require.Len(t, got0, frameLen)
	assert.Equal(t, wantFrame0, got0, "frame 0 bytes must be the original, not overwritten by buffer reuse (P0-1)")

	// Slice frame 1: [100,200)ms.
	got1 := b.slice(100, 200)
	require.Len(t, got1, frameLen)
	assert.Equal(t, wantFrame1, got1, "frame 1 bytes must be the original, not overwritten by buffer reuse (P0-1)")
}

// TestSliceAndDispatch_PayloadIsCopy proves the dispatched SegmentSlice.PCM is an
// independent copy: mutating the ring afterward (more Writes) does not change the
// already-dispatched bytes.
func TestSliceAndDispatch_PayloadIsCopy(t *testing.T) {
	b := NewSessionBuffer()
	frameLen := msBytes(100)

	shared := make([]byte, frameLen)
	copy(shared, patternPCM(0, frameLen))
	b.Write(shared)

	ok := b.SliceAndDispatch(1, 0, 100)
	require.True(t, ok)
	slice := <-b.Pending()
	require.Len(t, slice.PCM, frameLen)
	want := patternPCM(0, frameLen)

	// Keep writing (and reusing shared) — must not retroactively corrupt slice.PCM.
	copy(shared, patternPCM(50, frameLen))
	b.Write(shared)
	b.Write(shared)

	assert.Equal(t, want, slice.PCM, "dispatched PCM must be a copy independent of subsequent ring writes")
	assert.EqualValues(t, 1, slice.SegmentID)
	assert.EqualValues(t, 0, slice.BeginMs)
	assert.EqualValues(t, 100, slice.EndMs)
}

// ---------------------------------------------------------------------------
// P1: channel 满即丢弃，绝不阻塞。
// ---------------------------------------------------------------------------

// TestSliceAndDispatch_ChannelFullDropsNonBlocking fills the pending channel to
// capacity (no consumer), then asserts further dispatches return false (dropped),
// increment the dropped counter, and — critically — return promptly without
// blocking. A timeout guard fails the test if any call ever blocks.
func TestSliceAndDispatch_ChannelFullDropsNonBlocking(t *testing.T) {
	b := NewSessionBuffer()
	frameLen := msBytes(10)
	shared := make([]byte, frameLen)
	copy(shared, patternPCM(0, frameLen))

	// Write enough audio to back every slice with resident PCM.
	for i := 0; i < pendingChanCap+8; i++ {
		b.Write(shared)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		// First pendingChanCap dispatches fill the buffered channel (no consumer).
		for i := 0; i < pendingChanCap; i++ {
			// Each slice spans a distinct 10ms window so it is non-empty.
			begin := int64(i * 10)
			ok := b.SliceAndDispatch(int64(i), begin, begin+10)
			assert.True(t, ok, "dispatch %d should enqueue while channel has room", i)
		}
		// Channel now full: subsequent dispatches must drop (false) and NOT block.
		for i := 0; i < 8; i++ {
			begin := int64((pendingChanCap + i) * 10)
			ok := b.SliceAndDispatch(int64(1000+i), begin, begin+10)
			assert.False(t, ok, "dispatch into full channel must drop (P1)")
		}
	}()

	select {
	case <-done:
		// Completed without blocking — good.
	case <-time.After(2 * time.Second):
		t.Fatal("SliceAndDispatch blocked on a full channel — P1 non-blocking contract violated")
	}

	assert.EqualValues(t, 8, b.Dropped(), "dropped counter must equal the over-capacity dispatch count")
	assert.Len(t, b.Pending(), pendingChanCap, "channel should be exactly full")
}

// ---------------------------------------------------------------------------
// Slicing math: window mapping, alignment, scroll-out, invalid windows.
// ---------------------------------------------------------------------------

func TestSlice_WindowMapping(t *testing.T) {
	b := NewSessionBuffer()
	// Write 1s of audio whose bytes encode their absolute index (mod 256).
	total := msBytes(1000)
	b.Write(patternPCM(0, total))

	// Slice [250,500)ms => absolute bytes [250*32, 500*32).
	got := b.slice(250, 500)
	wantLen := msBytes(250)
	require.Len(t, got, wantLen)
	startByte := 250 * bytesPerMs
	for i := range got {
		assert.Equal(t, byte((startByte+i)%256), got[i], "byte %d mismatch", i)
	}
}

func TestSlice_InvalidWindows(t *testing.T) {
	b := NewSessionBuffer()
	b.Write(patternPCM(0, msBytes(500)))

	assert.Nil(t, b.slice(100, 100), "zero-length window => nil")
	assert.Nil(t, b.slice(200, 100), "end<begin => nil")
	assert.Nil(t, b.slice(-10, 50), "negative begin => nil")
}

// TestSlice_ScrolledOutWindowEmpty writes more than the ring window and asserts a
// request for audio that has scrolled out returns nil (clamped to resident).
func TestSlice_ScrolledOutWindowEmpty(t *testing.T) {
	b := NewSessionBuffer()
	// Write RingWindowSeconds+10s of audio in 1s chunks so the first 10s scroll out.
	chunk := patternPCM(0, bytesPerSecond)
	for i := 0; i < RingWindowSeconds+10; i++ {
		b.Write(chunk)
	}

	// The first 5 seconds are long gone => empty slice.
	assert.Nil(t, b.slice(0, 5000), "fully scrolled-out window => nil")

	// A recent window (last second) is still resident.
	lastBeginMs := int64((RingWindowSeconds + 10 - 1) * 1000)
	got := b.slice(lastBeginMs, lastBeginMs+1000)
	assert.Len(t, got, msBytes(1000), "most recent second must still be resident")
}

// TestSlice_PartialOverlapClampsToResident verifies a window straddling the
// scroll-out boundary returns only the still-resident tail.
func TestSlice_PartialOverlapClampsToResident(t *testing.T) {
	b := NewSessionBuffer()
	chunk := patternPCM(0, bytesPerSecond)
	for i := 0; i < RingWindowSeconds+5; i++ { // 5s scrolled out
		b.Write(chunk)
	}
	// Resident absolute byte range is [5s, 65s). Ask for [4s, 7s): only [5s,7s) survives.
	got := b.slice(4000, 7000)
	assert.Len(t, got, msBytes(2000), "partial-overlap window clamps to resident portion")
}

// ---------------------------------------------------------------------------
// Ring wraparound: writing past capacity then slicing the most recent window.
// ---------------------------------------------------------------------------

func TestWrite_RingWraparound(t *testing.T) {
	b := NewSessionBuffer()
	// Write exactly capacity, then one more second, forcing wraparound.
	b.Write(patternPCM(0, ringCapBytes))
	tail := patternPCM(7, bytesPerSecond) // distinct pattern for the wrapped tail
	b.Write(tail)

	// Most recent second (absolute [ringCapBytes, ringCapBytes+1s)) maps to the
	// wrapped region; its bytes must equal `tail`.
	beginMs := int64(RingWindowSeconds * 1000) // == ringCapBytes in ms
	got := b.slice(beginMs, beginMs+1000)
	require.Len(t, got, bytesPerSecond)
	assert.Equal(t, tail, got, "wrapped tail must read back intact")
}

// ---------------------------------------------------------------------------
// Close semantics + nil-safety.
// ---------------------------------------------------------------------------

func TestClose_DrainsChannelAndIsInert(t *testing.T) {
	b := NewSessionBuffer()
	b.Write(patternPCM(0, msBytes(100)))
	require.True(t, b.SliceAndDispatch(1, 0, 100))

	b.Close()
	b.Close() // idempotent — must not panic / double-close.

	// Ranging the closed channel drains the one queued slice then exits.
	var drained []SegmentSlice
	for s := range b.Pending() {
		drained = append(drained, s)
	}
	assert.Len(t, drained, 1)

	// Post-close Write / dispatch are inert no-ops (no panic, no send on closed chan).
	b.Write(patternPCM(0, msBytes(100)))
	assert.False(t, b.SliceAndDispatch(2, 0, 100), "dispatch after Close must be a no-op")
}

func TestNilSessionBuffer_AllOpsSafe(t *testing.T) {
	var b *SessionBuffer
	assert.NotPanics(t, func() {
		b.Write(patternPCM(0, 100))
		assert.False(t, b.SliceAndDispatch(1, 0, 100))
		b.Close()
	}, "nil SessionBuffer ops must be safe no-ops (flag-off path)")
}

// TestEmptyWriteAndOversizeFrame covers degenerate Write inputs.
func TestEmptyWriteAndOversizeFrame(t *testing.T) {
	b := NewSessionBuffer()
	b.Write(nil)      // no-op
	b.Write([]byte{}) // no-op
	assert.EqualValues(t, 0, b.totalWritten)

	// A frame larger than the whole ring keeps only its trailing window but still
	// advances totalWritten by the full length (absolute-offset alignment).
	big := patternPCM(0, ringCapBytes+bytesPerSecond)
	b.Write(big)
	assert.EqualValues(t, ringCapBytes+bytesPerSecond, b.totalWritten)
	// The most-recent second is the tail of `big`.
	beginMs := int64((RingWindowSeconds + 1 - 1) * 1000)
	got := b.slice(beginMs, beginMs+1000)
	require.Len(t, got, bytesPerSecond)
	wantTail := big[len(big)-bytesPerSecond:]
	assert.Equal(t, wantTail, got)
}

// ---------------------------------------------------------------------------
// Concurrency: Write (relay loop) and SliceAndDispatch (handleFinal) race-free.
// Run with -race.
// ---------------------------------------------------------------------------

func TestConcurrentWriteAndDispatch_NoRace(t *testing.T) {
	b := NewSessionBuffer()
	frame := patternPCM(0, msBytes(20))

	var wg sync.WaitGroup
	wg.Add(2)

	// Writer goroutine (simulates relay read loop).
	go func() {
		defer wg.Done()
		for i := 0; i < 2000; i++ {
			b.Write(frame)
		}
	}()

	// Dispatcher goroutine (simulates handleFinal). No consumer => most drop, fine.
	go func() {
		defer wg.Done()
		for i := 0; i < 2000; i++ {
			begin := int64(i * 20)
			b.SliceAndDispatch(int64(i), begin, begin+20)
		}
	}()

	wg.Wait()
	// Drain whatever made it into the channel; must not deadlock.
	b.Close()
	for range b.Pending() {
	}
}

// TestConcurrentDispatchAndClose_NoSendOnClosedPanic is the P0 regression for the
// send-on-closed-channel race: SliceAndDispatch (dashscope reader goroutine) and
// Close (controller teardown goroutine) run concurrently. Before the fix, Close()
// could close(b.pending) in the window between SliceAndDispatch's closed-check and
// its select-send, panicking with "send on closed channel" in a recover-less
// goroutine and crashing the whole process — the exact soft-degrade violation
// (DIARIZATION_SPEC §4 P1). The fix makes the send and close mutually exclusive
// under b.mu, so this must never panic. Run under -race.
//
// Looped many times to make the interleaving reliably reproducible: each iteration
// starts a fresh buffer, spawns a dispatcher hammering SliceAndDispatch while the
// main goroutine concurrently calls Close(), and a consumer drains so the channel
// genuinely accepts sends (forcing the live send path, not just the drop path).
func TestConcurrentDispatchAndClose_NoSendOnClosedPanic(t *testing.T) {
	for iter := 0; iter < 500; iter++ {
		b := NewSessionBuffer()
		// Pre-fill the ring so every SliceAndDispatch resolves to resident PCM and
		// actually attempts a channel send (exercises the panic-prone path).
		frame := patternPCM(0, msBytes(20))
		for i := 0; i < 64; i++ {
			b.Write(frame)
		}

		// Consumer drains Pending() so sends succeed (not all dropped) — this keeps
		// the live `b.pending <- slice` case hot, which is the case that panics on a
		// closed channel.
		consumerDone := make(chan struct{})
		go func() {
			defer close(consumerDone)
			for range b.Pending() {
			}
		}()

		// Dispatcher goroutine simulates handleFinal on the dashscope reader goroutine.
		dispatchDone := make(chan struct{})
		go func() {
			defer close(dispatchDone)
			for i := 0; i < 200; i++ {
				begin := int64((i % 32) * 20)
				// Must never panic even as Close() races underneath.
				b.SliceAndDispatch(int64(i), begin, begin+20)
			}
		}()

		// Race Close() against the in-flight dispatcher (controller teardown path).
		b.Close()

		<-dispatchDone
		<-consumerDone // Pending() closed by Close() => consumer's range exits.
	}
}
