package sandboxbroker

import (
	"bytes"
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"
)

func TestLimitCopyStreamsGlobalAndPerLeaseDirection(t *testing.T) {
	limiter, err := newCopyStreamLimiter(4, 1, ServerCopyBytesPerSecond)
	if err != nil {
		t.Fatal(err)
	}
	var releases []func()
	for index := 0; index < 4; index++ {
		release, err := limiter.acquire(
			"lease-"+string(rune('a'+index)),
			CopyInDirection,
		)
		if err != nil {
			t.Fatal(err)
		}
		releases = append(releases, release)
	}
	if _, err := limiter.acquire("lease-extra", CopyOutDirection); !errors.Is(
		err,
		ErrCopyStreamLimit,
	) {
		t.Fatalf("fifth stream error = %v", err)
	}
	releases[0]()
	releases[0]()

	inRelease, err := limiter.acquire("lease-shared", CopyInDirection)
	if err != nil {
		t.Fatal(err)
	}
	defer inRelease()
	if _, err := limiter.acquire("lease-shared", CopyInDirection); !errors.Is(
		err,
		ErrCopyStreamLimit,
	) {
		t.Fatalf("same-direction stream error = %v", err)
	}
	releases[1]()
	outRelease, err := limiter.acquire("lease-shared", CopyOutDirection)
	if err != nil {
		t.Fatal(err)
	}
	outRelease()
	for _, release := range releases[2:] {
		release()
	}
}

func TestLimitRateReaderUses64KiBAndHonorsCancellation(t *testing.T) {
	limiter, err := newCopyStreamLimiter(1, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	payload := bytes.Repeat([]byte("x"), ServerCopyBufferBytes+1)
	reader := limiter.reader(context.Background(), bytes.NewReader(payload))
	buffer := make([]byte, len(payload))
	count, err := reader.Read(buffer)
	if err != nil {
		t.Fatal(err)
	}
	if count != ServerCopyBufferBytes {
		t.Fatalf("first read = %d; want %d", count, ServerCopyBufferBytes)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cancelled := limiter.reader(ctx, bytes.NewReader([]byte("x")))
	if _, err := cancelled.Read(make([]byte, 1)); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled read error = %v", err)
	}
}

func TestLimitListenerBlocksThirtyThirdAcceptUntilClose(t *testing.T) {
	base := newQueuedListener()
	limited, err := newLimitedListener(base, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer limited.Close()

	firstServer, firstClient := net.Pipe()
	defer firstClient.Close()
	base.push(firstServer)
	first, err := limited.Accept()
	if err != nil {
		t.Fatal(err)
	}

	secondServer, secondClient := net.Pipe()
	defer secondClient.Close()
	base.push(secondServer)
	done := make(chan net.Conn, 1)
	go func() {
		connection, _ := limited.Accept()
		done <- connection
	}()
	select {
	case connection := <-done:
		if connection != nil {
			_ = connection.Close()
		}
		t.Fatal("second Accept returned before the first connection closed")
	case <-time.After(10 * time.Millisecond):
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case connection := <-done:
		if connection == nil {
			t.Fatal("second Accept returned nil")
		}
		_ = connection.Close()
	case <-time.After(time.Second):
		t.Fatal("second Accept remained blocked after capacity released")
	}
}

type queuedListener struct {
	connections chan net.Conn
	closed      chan struct{}
	once        sync.Once
}

func newQueuedListener() *queuedListener {
	return &queuedListener{
		connections: make(chan net.Conn, 4),
		closed:      make(chan struct{}),
	}
}

func (l *queuedListener) push(connection net.Conn) {
	l.connections <- connection
}

func (l *queuedListener) Accept() (net.Conn, error) {
	select {
	case connection := <-l.connections:
		return connection, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}

func (l *queuedListener) Close() error {
	l.once.Do(func() { close(l.closed) })
	return nil
}

func (l *queuedListener) Addr() net.Addr {
	return testNetworkAddress("unix")
}

type testNetworkAddress string

func (a testNetworkAddress) Network() string { return string(a) }
func (a testNetworkAddress) String() string  { return string(a) }
