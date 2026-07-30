package sandboxbroker

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"time"
)

var (
	// ErrConnectionLimit means all established Unix connection slots are used.
	ErrConnectionLimit = errors.New("sandbox broker connection limit reached")
	// ErrCopyStreamLimit means global or per-lease stream capacity is used.
	ErrCopyStreamLimit = errors.New("sandbox broker copy stream limit reached")
)

type limitedListener struct {
	net.Listener
	slots chan struct{}
	done  chan struct{}
	once  sync.Once
}

func newLimitedListener(listener net.Listener, maximum int) (*limitedListener, error) {
	if listener == nil || maximum <= 0 || maximum > ServerMaxConnections {
		return nil, ErrInvalidServerConfig
	}
	return &limitedListener{
		Listener: listener,
		slots:    make(chan struct{}, maximum),
		done:     make(chan struct{}),
	}, nil
}

func (l *limitedListener) Accept() (net.Conn, error) {
	select {
	case l.slots <- struct{}{}:
	case <-l.done:
		return nil, net.ErrClosed
	}
	connection, err := l.Listener.Accept()
	if err != nil {
		<-l.slots
		return nil, err
	}
	return &limitedConnection{
		Conn: connection,
		release: func() {
			<-l.slots
		},
	}, nil
}

func (l *limitedListener) Close() error {
	var err error
	l.once.Do(func() {
		close(l.done)
		err = l.Listener.Close()
	})
	return err
}

type limitedConnection struct {
	net.Conn
	release func()
	once    sync.Once
}

func (c *limitedConnection) Unwrap() net.Conn {
	return c.Conn
}

func (c *limitedConnection) Close() error {
	err := c.Conn.Close()
	c.once.Do(c.release)
	return err
}

type copyStreamKey struct {
	leaseID   string
	direction CopyDirection
}

type copyStreamLimiter struct {
	mu sync.Mutex

	maximum      int
	perDirection int
	active       int
	byLease      map[copyStreamKey]int
	rate         *aggregateByteRate
}

func newCopyStreamLimiter(
	maximum int,
	perDirection int,
	bytesPerSecond int64,
) (*copyStreamLimiter, error) {
	if maximum <= 0 ||
		maximum > ServerMaxCopyStreams ||
		perDirection != ServerMaxLeaseDirectionStreams ||
		bytesPerSecond <= 0 ||
		bytesPerSecond > ServerCopyBytesPerSecond {
		return nil, ErrInvalidServerConfig
	}
	return &copyStreamLimiter{
		maximum:      maximum,
		perDirection: perDirection,
		byLease:      make(map[copyStreamKey]int),
		rate:         &aggregateByteRate{bytesPerSecond: bytesPerSecond},
	}, nil
}

func (l *copyStreamLimiter) acquire(
	leaseID string,
	direction CopyDirection,
) (func(), error) {
	if !safeRuntimeToken(leaseID) ||
		(direction != CopyInDirection && direction != CopyOutDirection) {
		return nil, ErrRuntimePolicyDenied
	}
	key := copyStreamKey{leaseID: leaseID, direction: direction}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.active >= l.maximum || l.byLease[key] >= l.perDirection {
		return nil, ErrCopyStreamLimit
	}
	l.active++
	l.byLease[key]++
	var once sync.Once
	return func() {
		once.Do(func() {
			l.mu.Lock()
			defer l.mu.Unlock()
			l.active--
			l.byLease[key]--
			if l.byLease[key] == 0 {
				delete(l.byLease, key)
			}
		})
	}, nil
}

func (l *copyStreamLimiter) reader(
	ctx context.Context,
	source io.Reader,
) io.Reader {
	return &rateLimitedReader{
		ctx:    ctx,
		source: source,
		rate:   l.rate,
	}
}

func (l *copyStreamLimiter) writer(
	ctx context.Context,
	destination io.Writer,
) io.Writer {
	return &rateLimitedWriter{
		ctx:         ctx,
		destination: destination,
		rate:        l.rate,
	}
}

type aggregateByteRate struct {
	mu             sync.Mutex
	bytesPerSecond int64
	next           time.Time
}

func (r *aggregateByteRate) wait(ctx context.Context, size int) error {
	if size <= 0 {
		return nil
	}
	now := time.Now()
	r.mu.Lock()
	if r.next.Before(now) {
		r.next = now
	}
	start := r.next
	duration := time.Duration(
		(int64(size)*int64(time.Second) + r.bytesPerSecond - 1) /
			r.bytesPerSecond,
	)
	r.next = r.next.Add(duration)
	r.mu.Unlock()

	delay := time.Until(start)
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

type rateLimitedReader struct {
	ctx    context.Context
	source io.Reader
	rate   *aggregateByteRate
}

type hardLimitReader struct {
	source   io.Reader
	remain   int64
	limitErr error
}

func (r *hardLimitReader) Read(buffer []byte) (int, error) {
	if r.remain > 0 {
		if int64(len(buffer)) > r.remain {
			buffer = buffer[:r.remain]
		}
		count, err := r.source.Read(buffer)
		r.remain -= int64(count)
		return count, err
	}
	var probe [1]byte
	count, err := r.source.Read(probe[:])
	if count > 0 {
		return 0, r.limitErr
	}
	return 0, err
}

func (r *rateLimitedReader) Read(buffer []byte) (int, error) {
	if len(buffer) > ServerCopyBufferBytes {
		buffer = buffer[:ServerCopyBufferBytes]
	}
	count, err := r.source.Read(buffer)
	if waitErr := r.rate.wait(r.ctx, count); waitErr != nil {
		return 0, waitErr
	}
	return count, err
}

type rateLimitedWriter struct {
	ctx         context.Context
	destination io.Writer
	rate        *aggregateByteRate
}

func (w *rateLimitedWriter) Write(buffer []byte) (int, error) {
	if len(buffer) > ServerCopyBufferBytes {
		buffer = buffer[:ServerCopyBufferBytes]
	}
	if err := w.rate.wait(w.ctx, len(buffer)); err != nil {
		return 0, err
	}
	return w.destination.Write(buffer)
}
