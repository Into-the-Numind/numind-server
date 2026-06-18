// Package diarize is the speaker-diarization online relay infrastructure for
// the meeting-copilot speaker-diarization feature (DIARIZATION_SPEC.md §3/§4).
//
// T6 scope (THIS FILE): the per-session PCM plumbing that sits beside the realtime
// ASR relay. It does NOT do clustering, embedding, or any voiceprint call — that
// is T7 (the channel consumer). T6 only provides:
//
//	(a) a per-session rolling PCM ring buffer (~60s window) into which the relay
//	    feeds every uplink PCM frame. P0-1: each frame is copied into the ring's own
//	    backing storage on Write, so the caller's gorilla-owned buffer (reused by the
//	    next conn.ReadMessage) is never retained;
//	(b) on ASR sentence-final (handleFinal), a slice of the ring covering the
//	    sentence's [beginMs,endMs] window is pushed to a per-session buffered channel
//	    for T7 to consume. P1: the push is strictly non-blocking — a full channel
//	    drops the slice (and counts it) rather than ever back-pressuring the ASR
//	    relay loop.
//
// SOFT-DEGRADE INVARIANT (DIARIZATION_SPEC.md §4 P1): nothing in this package may
// ever block the relay or kill a meeting. Ring writes are O(1) under a mutex;
// channel pushes are select/default non-blocking. Both the relay read loop and the
// handleFinal callback (two goroutines) touch the ring, so all ring state is mutex
// guarded.
//
// FLAG (DIARIZATION_SPEC.md §4 P1-flag): a SessionBuffer is created only when the
// effective flag is on — features.meeting_copilot.enabled && features.meeting_diarization.enabled.
// When off the relay never constructs a SessionBuffer and never calls Write/SliceAndDispatch,
// so this package is wholly inert (the integration hook in realtime.go is nil-safe).
package diarize

import (
	"sync"
	"sync/atomic"
)

// Audio framing constants — 16kHz / 16-bit / mono PCM (matches the realtime ASR
// pipeline: asr_stream_client.go run-task uses format=pcm, sample_rate=16000).
const (
	// sampleRate is the PCM sample rate in Hz.
	sampleRate = 16000
	// bytesPerSample is 16-bit mono => 2 bytes per sample.
	bytesPerSample = 2
	// bytesPerSecond is the uplink PCM byte rate (16000 * 2 = 32000 B/s).
	bytesPerSecond = sampleRate * bytesPerSample
	// bytesPerMs is the byte count per millisecond of audio (32 B/ms).
	bytesPerMs = bytesPerSecond / 1000
)

const (
	// RingWindowSeconds is the rolling window the ring retains (~60s). At 32 KB/s
	// that is ~1.92 MB per active session — cheap, and 60s comfortably covers a
	// finalized sentence's [beginMs,endMs] span even with ASR end-of-sentence lag.
	RingWindowSeconds = 60
	// ringCapBytes is the fixed ring capacity in bytes (60 * 32000 = 1,920,000).
	ringCapBytes = RingWindowSeconds * bytesPerSecond

	// pendingChanCap is the per-session buffered-channel depth for ready segment
	// slices awaiting T7 consumption. Sized to absorb a short burst of finalized
	// sentences without blocking; when full, SliceAndDispatch drops (P1) rather
	// than back-pressure the ASR relay.
	pendingChanCap = 32
)

// SegmentSlice is one finalized sentence's PCM, carved from the ring buffer and
// queued for the T7 voiceprint/clustering consumer.
//
// PCM is a freshly allocated copy owned by the slice (never aliases the ring's
// backing storage), so the consumer may hold it across subsequent ring writes.
type SegmentSlice struct {
	// SegmentID is the persisted meeting_segment row id this PCM belongs to.
	SegmentID int64
	// BeginMs / EndMs are the sentence window (relative to this stream's start),
	// echoed for the consumer's logging/diagnostics.
	BeginMs int64
	EndMs   int64
	// PCM is 16k mono s16le audio for [BeginMs,EndMs], owned by this struct.
	PCM []byte
}

// SessionBuffer is the per-session PCM ring buffer plus the ready-slice channel.
//
// Lifecycle: the relay constructs one per realtime ASR session (only when the
// effective diarization flag is on), feeds every uplink frame via Write, and on
// each sentence-final calls SliceAndDispatch. T7 ranges over Pending() to consume
// slices and calls Close() when the relay tears down.
//
// Concurrency: Write (relay read-loop goroutine) and SliceAndDispatch (dashscope
// reader-goroutine handleFinal callback) both touch the ring; all ring state is
// guarded by mu and every guarded section is O(1) (or O(window) bounded for a
// slice copy — never unbounded, never blocking).
type SessionBuffer struct {
	mu sync.Mutex
	// ring is the fixed-size backing storage (ringCapBytes). It is a true ring:
	// the most recent up to ringCapBytes of audio live here.
	ring []byte
	// writePos is the next write index into ring (modulo ringCapBytes).
	writePos int
	// totalWritten is the absolute count of PCM bytes ever Written for this stream
	// (monotonic). The byte offset of audio still resident in the ring is
	// [totalWritten-len, totalWritten); converting a sentence's [beginMs,endMs] to
	// absolute byte offsets and intersecting with that resident window yields the
	// slice. This is what makes the ring tolerant of overwrite: older audio that
	// has scrolled out is simply unavailable and the slice is clamped/empty.
	totalWritten int64

	// pending is the buffered channel of ready segment slices for T7.
	pending chan SegmentSlice
	// dropped counts slices discarded because pending was full (P1 metric).
	dropped atomic.Int64
	// emptySlices counts SliceAndDispatch calls that resolved to no resident PCM
	// (window fully scrolled out or zero-length) — diagnostic only, not dispatched.
	emptySlices atomic.Int64

	// closed guards Close idempotency / post-close Write & dispatch no-ops.
	closeOnce sync.Once
	closed    atomic.Bool
}

// NewSessionBuffer allocates a fresh per-session ring buffer + ready channel.
//
// Callers MUST gate construction behind the effective diarization flag; this
// package does not read viper (the relay owns flag resolution, see realtime.go).
func NewSessionBuffer() *SessionBuffer {
	return &SessionBuffer{
		ring:    make([]byte, ringCapBytes),
		pending: make(chan SegmentSlice, pendingChanCap),
	}
}

// Write appends one uplink PCM frame to the ring.
//
// P0-1 (DATA CORRUPTION, MUST): the caller's pcm slice may be the gorilla
// websocket read buffer, which conn.ReadMessage reuses on the next read. Write
// therefore COPIES the bytes into the ring's own backing storage immediately and
// retains nothing aliasing pcm. (The copy-into-ring IS the mandated make+copy:
// the ring owns its memory, so once Write returns the caller is free to overwrite
// pcm.)
//
// O(1) amortized under the mutex (a frame larger than the ring is truncated to its
// last ringCapBytes, an impossible case in practice but handled for safety). Never
// blocks. No-op after Close.
func (b *SessionBuffer) Write(pcm []byte) {
	if b == nil || len(pcm) == 0 || b.closed.Load() {
		return
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	src := pcm
	// A frame longer than the whole ring can only keep its trailing ringCapBytes;
	// drop the unreachable prefix so totalWritten still reflects true audio length.
	if len(src) > ringCapBytes {
		src = src[len(src)-ringCapBytes:]
	}

	// Copy into the ring, wrapping at the end (at most two memcpys). copy() reads
	// from the caller's slice into b.ring (our storage) — this is the P0-1 copy.
	n := len(src)
	first := ringCapBytes - b.writePos
	if first >= n {
		copy(b.ring[b.writePos:], src)
	} else {
		copy(b.ring[b.writePos:], src[:first])
		copy(b.ring[0:], src[first:])
	}
	b.writePos = (b.writePos + n) % ringCapBytes

	// totalWritten advances by the full original frame length (including any prefix
	// we could not keep), so absolute-offset math stays aligned to the real stream.
	b.totalWritten += int64(len(pcm))
}

// SliceAndDispatch carves the PCM covering [beginMs,endMs] out of the ring and
// non-blockingly pushes it to the pending channel for T7.
//
// P1 (NON-BLOCKING, MUST): the push is select{ case ch<-x: default: drop+metric }.
// A full channel drops the slice and increments the dropped counter — it MUST
// NEVER block or back-pressure the ASR relay. The ring slice copy is taken under
// the mutex (bounded by the window); the channel push then runs under the same
// mutex but is select/default (O(1), non-blocking) so holding the lock briefly is
// safe and a slow consumer can never stall a Write.
//
// P0 (NO SEND-ON-CLOSED-CHANNEL PANIC, MUST): the closed-check and the channel
// send are performed together under b.mu, and Close() closes the channel under the
// same b.mu. This makes "is the channel still open?" and "send on the channel"
// atomic with respect to close, so SliceAndDispatch (dashscope reader goroutine)
// can never send on a channel that Close() (controller teardown goroutine) just
// closed. A bare select{ case ch<-x: default } does NOT protect against this: a
// closed channel makes the send case immediately ready and panics. The reader
// goroutine has no recover(), so such a panic would crash the whole process and
// take the relay/transcription down — the exact catastrophic violation of the
// soft-degrade invariant (DIARIZATION_SPEC.md §4 P1 / file header).
//
// Returns true iff a slice was actually queued (false => empty window, dropped, or
// closed). Callers (T7 hook) treat false as a soft skip — the meeting carries on.
func (b *SessionBuffer) SliceAndDispatch(segmentID, beginMs, endMs int64) bool {
	if b == nil || b.closed.Load() {
		return false
	}

	pcm := b.slice(beginMs, endMs)
	if len(pcm) == 0 {
		b.emptySlices.Add(1)
		return false
	}

	slice := SegmentSlice{SegmentID: segmentID, BeginMs: beginMs, EndMs: endMs, PCM: pcm}

	// Send under b.mu, mutually exclusive with Close()'s close(b.pending). Re-check
	// closed inside the lock: Close() may have fired between b.slice() above and here.
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed.Load() {
		return false
	}
	// P1 non-blocking push: full channel => drop + metric, never block the relay.
	select {
	case b.pending <- slice:
		return true
	default:
		b.dropped.Add(1)
		return false
	}
}

// slice returns a freshly allocated copy of the resident PCM covering
// [beginMs,endMs] (relative to stream start), intersected with whatever is still
// in the ring. Returns nil when the window is empty / invalid / fully scrolled out.
//
// Held under the mutex. The returned slice is independent of the ring storage, so
// SliceAndDispatch can release the lock before the (non-blocking) channel push.
func (b *SessionBuffer) slice(beginMs, endMs int64) []byte {
	if endMs <= beginMs || beginMs < 0 {
		return nil
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	// Desired absolute byte range for the sentence (aligned down to sample frames).
	wantStart := alignDown(beginMs * bytesPerMs)
	wantEnd := alignDown(endMs * bytesPerMs)
	if wantEnd <= wantStart {
		return nil
	}

	// Resident absolute byte range still in the ring: [residStart, residEnd).
	residEnd := b.totalWritten
	residStart := residEnd - int64(ringCapBytes)
	if residStart < 0 {
		residStart = 0
	}

	// Intersect want with resident.
	from := maxInt64(wantStart, residStart)
	to := minInt64(wantEnd, residEnd)
	if to <= from {
		return nil // window fully scrolled out or no overlap.
	}

	out := make([]byte, to-from)
	// Map absolute offsets into ring indices. The ring index of absolute offset o
	// (for o in resident range) is o % ringCapBytes.
	for written := int64(0); written < to-from; {
		abs := from + written
		ringIdx := int(abs % int64(ringCapBytes))
		chunk := ringCapBytes - ringIdx
		remaining := int(to - from - written)
		if chunk > remaining {
			chunk = remaining
		}
		copy(out[written:], b.ring[ringIdx:ringIdx+chunk])
		written += int64(chunk)
	}
	return out
}

// Pending exposes the read side of the ready-slice channel for the T7 consumer.
func (b *SessionBuffer) Pending() <-chan SegmentSlice {
	return b.pending
}

// Dropped reports the count of slices dropped due to a full channel (P1 metric).
func (b *SessionBuffer) Dropped() int64 { return b.dropped.Load() }

// EmptySlices reports the count of dispatch calls that resolved to no resident PCM
// (diagnostic).
func (b *SessionBuffer) EmptySlices() int64 { return b.emptySlices.Load() }

// Close marks the buffer closed (Write/SliceAndDispatch become no-ops) and closes
// the pending channel so a ranging T7 consumer drains and exits. Idempotent.
//
// P0 (NO SEND-ON-CLOSED-CHANNEL PANIC, MUST): closed=true and close(b.pending) are
// performed under b.mu, the same mutex SliceAndDispatch holds around its channel
// send. This serializes close against any in-flight dispatch so the channel can
// never be closed mid-send. close() itself is O(1), so holding the lock here does
// not violate the non-blocking contract.
func (b *SessionBuffer) Close() {
	if b == nil {
		return
	}
	b.closeOnce.Do(func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		b.closed.Store(true)
		close(b.pending)
	})
}

// alignDown rounds an absolute byte offset down to a whole 16-bit sample frame so
// slices never split a sample (which would corrupt the PCM the consumer reads).
func alignDown(n int64) int64 {
	return n - (n % bytesPerSample)
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func minInt64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}
