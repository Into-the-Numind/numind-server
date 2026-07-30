package sandboxbroker

import (
	"archive/tar"
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

const (
	// StreamBufferSize is the fixed copy buffer used by broker file streams.
	StreamBufferSize = 64 << 10
	// MaxSingleFileBytes is the hard per-file ceiling in either direction.
	MaxSingleFileBytes int64 = 50 << 20
	// MaxCopyInBytes is the hard aggregate input ceiling per lease.
	MaxCopyInBytes int64 = 100 << 20
	// MaxCopyOutBytes is the hard aggregate output ceiling per lease.
	MaxCopyOutBytes int64 = 200 << 20
	// MaxCopyFiles is the hard file-count ceiling per lease and direction.
	MaxCopyFiles = 10
	// MaxExecOutputBytes is the combined stdout and stderr ceiling per exec.
	MaxExecOutputBytes int64 = 4 << 20
)

var (
	// ErrStreamPolicyDenied means a path, archive entry, or stream type is unsafe.
	ErrStreamPolicyDenied = errors.New("sandbox stream policy denied")
	// ErrStreamInputTooLarge means a copy-in limit was exceeded.
	ErrStreamInputTooLarge = errors.New("sandbox input stream too large")
	// ErrStreamOutputTooLarge means a copy-out or exec-output limit was exceeded.
	ErrStreamOutputTooLarge = errors.New("sandbox output stream too large")
	// ErrStreamUnavailable means a local stream or filesystem operation failed.
	ErrStreamUnavailable = errors.New("sandbox stream unavailable")
)

// CopyDirection identifies which aggregate lease budget applies to a file.
type CopyDirection string

const (
	// CopyInDirection applies the 100 MiB aggregate input budget.
	CopyInDirection CopyDirection = "in"
	// CopyOutDirection applies the 200 MiB aggregate output budget.
	CopyOutDirection CopyDirection = "out"
)

// StreamLimits bounds one tar extraction without relying on process memory.
type StreamLimits struct {
	MaxSingleBytes int64
	MaxTotalBytes  int64
	MaxFiles       int
}

// StreamStats reports the regular files and payload bytes accepted from a stream.
type StreamStats struct {
	Files int
	Bytes int64
}

// CopyOutSource is a validated source rooted at the fixed /workdir mount.
type CopyOutSource struct {
	Root        string
	Relative    string
	ArchiveName string
}

// DefaultCopyOutLimits returns the fixed broker copy-out ceilings.
func DefaultCopyOutLimits() StreamLimits {
	return StreamLimits{
		MaxSingleBytes: MaxSingleFileBytes,
		MaxTotalBytes:  MaxCopyOutBytes,
		MaxFiles:       MaxCopyFiles,
	}
}

// CheckCopyBudget validates one new file against the hard per-lease counters.
func CheckCopyBudget(
	direction CopyDirection,
	usedFiles int,
	usedBytes int64,
	nextBytes int64,
) error {
	if direction != CopyInDirection && direction != CopyOutDirection {
		return ErrStreamPolicyDenied
	}
	if usedFiles < 0 || usedBytes < 0 || nextBytes < 0 ||
		usedFiles >= MaxCopyFiles || nextBytes > MaxSingleFileBytes {
		return streamSizeError(direction)
	}
	var maxTotal int64
	switch direction {
	case CopyInDirection:
		maxTotal = MaxCopyInBytes
	case CopyOutDirection:
		maxTotal = MaxCopyOutBytes
	default:
		return ErrStreamPolicyDenied
	}
	if nextBytes > maxTotal-usedBytes {
		return streamSizeError(direction)
	}
	return nil
}

// CanonicalCopyInPath validates a single file target inside writable tmpfs roots.
func CanonicalCopyInPath(raw string, allowedSkills []string) (string, error) {
	if raw == "" || strings.ContainsRune(raw, 0) || !path.IsAbs(raw) ||
		hasParentSegment(raw) {
		return "", ErrStreamPolicyDenied
	}
	clean := path.Clean(raw)
	if clean != raw || clean == "/workdir" || clean == "/skills" {
		return "", ErrStreamPolicyDenied
	}
	if strings.HasPrefix(clean, "/workdir/") {
		return clean, nil
	}
	const skillsPrefix = "/skills/"
	if !strings.HasPrefix(clean, skillsPrefix) {
		return "", ErrStreamPolicyDenied
	}
	relative := strings.TrimPrefix(clean, skillsPrefix)
	parts := strings.Split(relative, "/")
	if len(parts) < 2 || !safeSkillName(parts[0]) {
		return "", ErrStreamPolicyDenied
	}
	allowed := false
	for _, skill := range allowedSkills {
		if skill == parts[0] {
			allowed = true
			break
		}
	}
	if !allowed {
		return "", ErrStreamPolicyDenied
	}
	for _, part := range parts[1:] {
		if !safePathSegment(part) {
			return "", ErrStreamPolicyDenied
		}
	}
	return clean, nil
}

// CanonicalCopyOutPath validates a source beneath /workdir/output.
func CanonicalCopyOutPath(raw string) (CopyOutSource, error) {
	if raw == "" || strings.ContainsRune(raw, 0) || !path.IsAbs(raw) ||
		hasParentSegment(raw) {
		return CopyOutSource{}, ErrStreamPolicyDenied
	}
	contentsOnly := strings.HasSuffix(raw, "/.")
	base := raw
	if contentsOnly {
		base = strings.TrimSuffix(raw, "/.")
	}
	clean := path.Clean(base)
	if clean != base ||
		(clean != "/workdir/output" && !strings.HasPrefix(clean, "/workdir/output/")) {
		return CopyOutSource{}, ErrStreamPolicyDenied
	}
	if contentsOnly {
		return CopyOutSource{
			Root:     "/workdir",
			Relative: strings.TrimPrefix(clean, "/workdir/"),
		}, nil
	}
	return CopyOutSource{
		Root:        "/workdir",
		Relative:    strings.TrimPrefix(clean, "/workdir/"),
		ArchiveName: path.Base(clean),
	}, nil
}

// CopyBounded streams at most maxBytes with a fixed 64 KiB buffer.
func CopyBounded(
	ctx context.Context,
	dst io.Writer,
	src io.ReadCloser,
	maxBytes int64,
	limitErr error,
) (int64, error) {
	if dst == nil || src == nil || maxBytes < 0 || limitErr == nil {
		return 0, ErrStreamPolicyDenied
	}
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = src.Close()
			if closer, ok := dst.(io.Closer); ok {
				_ = closer.Close()
			}
		case <-done:
		}
	}()
	defer close(done)

	writer := &maxBytesWriter{dst: dst, remain: maxBytes, limitErr: limitErr}
	n, err := io.CopyBuffer(writer, &contextReader{ctx: ctx, reader: src}, make([]byte, StreamBufferSize))
	if ctxErr := ctx.Err(); ctxErr != nil {
		return n, ctxErr
	}
	if err != nil {
		return n, err
	}
	return n, nil
}

// ExtractTarStream safely extracts a bounded tar stream without following links.
func ExtractTarStream(
	ctx context.Context,
	src io.ReadCloser,
	dest string,
	limits StreamLimits,
) (StreamStats, error) {
	if src == nil || limits.MaxSingleBytes <= 0 || limits.MaxTotalBytes <= 0 ||
		limits.MaxSingleBytes > limits.MaxTotalBytes || limits.MaxFiles <= 0 ||
		limits.MaxSingleBytes > MaxSingleFileBytes ||
		limits.MaxTotalBytes > MaxCopyOutBytes ||
		limits.MaxFiles > MaxCopyFiles {
		return StreamStats{}, ErrStreamPolicyDenied
	}
	rootFD, err := openExistingDirectoryNoFollow(dest, false)
	if err != nil {
		return StreamStats{}, stableStreamFileError(err)
	}
	defer unix.Close(rootFD)

	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = src.Close()
		case <-done:
		}
	}()
	defer close(done)

	rawMax := limits.MaxTotalBytes + int64(limits.MaxFiles+16)*StreamBufferSize
	tr := tar.NewReader(bufio.NewReaderSize(io.LimitReader(src, rawMax), StreamBufferSize))
	var stats StreamStats
	entries := 0
	for {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return stats, ctxErr
		}
		header, nextErr := tr.Next()
		if nextErr == io.EOF {
			return stats, nil
		}
		if nextErr != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return stats, ctxErr
			}
			return stats, fmt.Errorf("%w: read tar header", ErrStreamPolicyDenied)
		}
		entries++
		if entries > limits.MaxFiles*4+16 {
			return stats, ErrStreamOutputTooLarge
		}
		parts, pathErr := safeArchiveParts(header.Name)
		if pathErr != nil {
			return stats, pathErr
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if len(parts) == 0 {
				continue
			}
			dirFD, openErr := openStreamDirAt(rootFD, parts, true)
			if openErr != nil {
				return stats, stableStreamFileError(openErr)
			}
			_ = unix.Close(dirFD)
		case tar.TypeReg:
			if len(parts) == 0 || header.Size < 0 ||
				header.Size > limits.MaxSingleBytes ||
				stats.Files >= limits.MaxFiles ||
				header.Size > limits.MaxTotalBytes-stats.Bytes {
				return stats, ErrStreamOutputTooLarge
			}
			parentFD, openErr := openStreamDirAt(rootFD, parts[:len(parts)-1], true)
			if openErr != nil {
				return stats, stableStreamFileError(openErr)
			}
			name := parts[len(parts)-1]
			fd, openErr := unix.Openat(
				parentFD,
				name,
				unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW,
				0o600,
			)
			if openErr != nil {
				_ = unix.Close(parentFD)
				return stats, stableStreamFileError(openErr)
			}
			file := os.NewFile(uintptr(fd), filepath.Base(name))
			written, copyErr := copyExactCancelable(ctx, file, tr, header.Size)
			closeErr := file.Close()
			if copyErr != nil || closeErr != nil {
				_ = unix.Unlinkat(parentFD, name, 0)
				_ = unix.Close(parentFD)
				if ctxErr := ctx.Err(); ctxErr != nil {
					return stats, ctxErr
				}
				return stats, fmt.Errorf("%w: extract regular file", ErrStreamUnavailable)
			}
			_ = unix.Close(parentFD)
			stats.Files++
			stats.Bytes += written
		default:
			return stats, ErrStreamPolicyDenied
		}
	}
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

type maxBytesWriter struct {
	dst      io.Writer
	remain   int64
	limitErr error
}

func (w *maxBytesWriter) Write(p []byte) (int, error) {
	if w.remain == 0 {
		return 0, w.limitErr
	}
	if int64(len(p)) <= w.remain {
		n, err := w.dst.Write(p)
		w.remain -= int64(n)
		return n, err
	}
	n, err := w.dst.Write(p[:w.remain])
	w.remain -= int64(n)
	if err != nil {
		return n, err
	}
	return n, w.limitErr
}

func copyExactCancelable(
	ctx context.Context,
	dst io.WriteCloser,
	src io.Reader,
	size int64,
) (int64, error) {
	if size == 0 {
		return 0, nil
	}
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = dst.Close()
		case <-done:
		}
	}()
	defer close(done)

	written, err := io.CopyBuffer(
		dst,
		&contextReader{ctx: ctx, reader: io.LimitReader(src, size)},
		make([]byte, StreamBufferSize),
	)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return written, ctxErr
	}
	if err != nil {
		return written, err
	}
	if written != size {
		return written, io.ErrUnexpectedEOF
	}
	return written, nil
}

func openExistingDirectoryNoFollow(absolutePath string, requirePrivate bool) (int, error) {
	if absolutePath == "" ||
		strings.ContainsRune(absolutePath, 0) ||
		!filepath.IsAbs(absolutePath) ||
		filepath.Clean(absolutePath) != absolutePath ||
		absolutePath == string(filepath.Separator) {
		return -1, unix.EINVAL
	}
	parts := strings.Split(
		strings.TrimPrefix(absolutePath, string(filepath.Separator)),
		string(filepath.Separator),
	)
	currentFD, err := unix.Open(
		string(filepath.Separator),
		unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW,
		0,
	)
	if err != nil {
		return -1, err
	}
	for _, part := range parts {
		if !safePathSegment(part) {
			_ = unix.Close(currentFD)
			return -1, unix.EINVAL
		}
		nextFD, openErr := unix.Openat(
			currentFD,
			part,
			unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW,
			0,
		)
		_ = unix.Close(currentFD)
		if openErr != nil {
			return -1, openErr
		}
		if requirePrivate {
			var stat unix.Stat_t
			if statErr := unix.Fstat(nextFD, &stat); statErr != nil {
				_ = unix.Close(nextFD)
				return -1, statErr
			}
			ownerAllowed := stat.Uid == 0 || stat.Uid == uint32(os.Geteuid())
			if !ownerAllowed || os.FileMode(stat.Mode).Perm()&0o022 != 0 {
				_ = unix.Close(nextFD)
				return -1, unix.EPERM
			}
		}
		currentFD = nextFD
	}
	return currentFD, nil
}

func safeArchiveParts(name string) ([]string, error) {
	if name == "" || len(name) > 4096 || strings.ContainsRune(name, 0) ||
		path.IsAbs(name) || hasParentSegment(name) {
		return nil, ErrStreamPolicyDenied
	}
	clean := path.Clean(name)
	if clean == "." {
		return nil, nil
	}
	parts := strings.Split(clean, "/")
	for _, part := range parts {
		if !safePathSegment(part) {
			return nil, ErrStreamPolicyDenied
		}
	}
	return parts, nil
}

func openStreamDirAt(rootFD int, parts []string, create bool) (int, error) {
	currentFD, err := unix.Dup(rootFD)
	if err != nil {
		return -1, err
	}
	for _, part := range parts {
		if create {
			if mkdirErr := unix.Mkdirat(currentFD, part, 0o700); mkdirErr != nil &&
				!errors.Is(mkdirErr, unix.EEXIST) {
				_ = unix.Close(currentFD)
				return -1, mkdirErr
			}
		}
		nextFD, openErr := unix.Openat(
			currentFD,
			part,
			unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW,
			0,
		)
		_ = unix.Close(currentFD)
		if openErr != nil {
			return -1, openErr
		}
		currentFD = nextFD
	}
	return currentFD, nil
}

func stableStreamFileError(err error) error {
	if errors.Is(err, unix.ELOOP) ||
		errors.Is(err, unix.ENOTDIR) ||
		errors.Is(err, unix.EEXIST) ||
		errors.Is(err, unix.EINVAL) ||
		errors.Is(err, unix.EPERM) {
		return ErrStreamPolicyDenied
	}
	return fmt.Errorf("%w: filesystem operation", ErrStreamUnavailable)
}

func streamSizeError(direction CopyDirection) error {
	if direction == CopyInDirection {
		return ErrStreamInputTooLarge
	}
	return ErrStreamOutputTooLarge
}

func hasParentSegment(value string) bool {
	for _, part := range strings.Split(value, "/") {
		if part == ".." {
			return true
		}
	}
	return false
}

func safePathSegment(segment string) bool {
	return segment != "" &&
		segment != "." &&
		segment != ".." &&
		len(segment) <= 255 &&
		!strings.ContainsRune(segment, 0)
}

func safeSkillName(name string) bool {
	if name == "" || len(name) > 128 {
		return false
	}
	for _, char := range name {
		if (char < 'a' || char > 'z') &&
			(char < 'A' || char > 'Z') &&
			(char < '0' || char > '9') &&
			char != '.' && char != '_' && char != '-' {
			return false
		}
	}
	return true
}
