package sandboxbroker

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestStreamCopyBudgetHardBoundaries(t *testing.T) {
	if MaxSingleFileBytes != 50<<20 ||
		MaxCopyInBytes != 100<<20 ||
		MaxCopyOutBytes != 200<<20 ||
		MaxCopyFiles != 10 ||
		StreamBufferSize != 64<<10 {
		t.Fatalf(
			"unexpected hard limits single=%d in=%d out=%d files=%d buffer=%d",
			MaxSingleFileBytes,
			MaxCopyInBytes,
			MaxCopyOutBytes,
			MaxCopyFiles,
			StreamBufferSize,
		)
	}
	if err := CheckCopyBudget(
		CopyInDirection,
		9,
		50<<20,
		50<<20,
	); err != nil {
		t.Fatalf("exact 10-file/100MiB input boundary rejected: %v", err)
	}
	if err := CheckCopyBudget(
		CopyOutDirection,
		9,
		150<<20,
		50<<20,
	); err != nil {
		t.Fatalf("exact 10-file/200MiB output boundary rejected: %v", err)
	}
	for name, call := range map[string]func() error{
		"single over 50MiB": func() error {
			return CheckCopyBudget(CopyInDirection, 0, 0, MaxSingleFileBytes+1)
		},
		"input over 100MiB": func() error {
			return CheckCopyBudget(CopyInDirection, 9, 50<<20, (50<<20)+1)
		},
		"output over 200MiB": func() error {
			return CheckCopyBudget(CopyOutDirection, 9, 150<<20, (50<<20)+1)
		},
		"eleventh input file": func() error {
			return CheckCopyBudget(CopyInDirection, 10, 0, 0)
		},
		"eleventh output file": func() error {
			return CheckCopyBudget(CopyOutDirection, 10, 0, 0)
		},
	} {
		t.Run(name, func(t *testing.T) {
			err := call()
			if err == nil {
				t.Fatal("over-limit budget accepted")
			}
		})
	}
}

func TestStreamCanonicalCopyPaths(t *testing.T) {
	for _, test := range []struct {
		raw    string
		skills []string
		want   string
	}{
		{raw: "/workdir/task.py", want: "/workdir/task.py"},
		{raw: "/workdir/input/source.csv", want: "/workdir/input/source.csv"},
		{
			raw:    "/skills/document-system/SKILL.md",
			skills: []string{"document-system"},
			want:   "/skills/document-system/SKILL.md",
		},
	} {
		got, err := CanonicalCopyInPath(test.raw, test.skills)
		if err != nil || got != test.want {
			t.Errorf("CanonicalCopyInPath(%q) = %q, %v", test.raw, got, err)
		}
	}
	for _, denied := range []string{
		"relative.txt",
		"/workdir",
		"/workdir/../etc/passwd",
		"/workdir//input.txt",
		"/skills/document-system",
		"/skills/other/SKILL.md",
		"/etc/passwd",
	} {
		if _, err := CanonicalCopyInPath(
			denied,
			[]string{"document-system"},
		); !errors.Is(err, ErrStreamPolicyDenied) {
			t.Errorf("CanonicalCopyInPath(%q) err = %v", denied, err)
		}
	}

	for _, test := range []struct {
		raw  string
		want CopyOutSource
	}{
		{
			raw: "/workdir/output",
			want: CopyOutSource{
				Root:        "/workdir",
				Relative:    "output",
				ArchiveName: "output",
			},
		},
		{
			raw:  "/workdir/output/.",
			want: CopyOutSource{Root: "/workdir", Relative: "output"},
		},
		{
			raw: "/workdir/output/report.pdf",
			want: CopyOutSource{
				Root:        "/workdir",
				Relative:    "output/report.pdf",
				ArchiveName: "report.pdf",
			},
		},
	} {
		got, err := CanonicalCopyOutPath(test.raw)
		if err != nil || got != test.want {
			t.Errorf("CanonicalCopyOutPath(%q) = %#v, %v", test.raw, got, err)
		}
	}
	for _, denied := range []string{
		"output",
		"/workdir",
		"/workdir/output/../secret",
		"/workdir/output//report.pdf",
		"/skills/output/report.pdf",
	} {
		if _, err := CanonicalCopyOutPath(denied); !errors.Is(err, ErrStreamPolicyDenied) {
			t.Errorf("CanonicalCopyOutPath(%q) err = %v", denied, err)
		}
	}
}

func TestStreamCopyBoundedUses64KiBAndExactLimit(t *testing.T) {
	payload := bytes.Repeat([]byte("x"), (3*StreamBufferSize)+17)
	reader := &recordingReadCloser{reader: bytes.NewReader(payload)}
	var output bytes.Buffer
	n, err := CopyBounded(
		context.Background(),
		&output,
		reader,
		int64(len(payload)),
		ErrStreamInputTooLarge,
	)
	if err != nil || n != int64(len(payload)) || !bytes.Equal(output.Bytes(), payload) {
		t.Fatalf("CopyBounded n=%d err=%v output=%d", n, err, output.Len())
	}
	if reader.maxReadRequest > StreamBufferSize {
		t.Fatalf("max Read buffer = %d; want <= %d", reader.maxReadRequest, StreamBufferSize)
	}

	reader = &recordingReadCloser{reader: strings.NewReader("123456")}
	output.Reset()
	n, err = CopyBounded(
		context.Background(),
		&output,
		reader,
		5,
		ErrStreamInputTooLarge,
	)
	if !errors.Is(err, ErrStreamInputTooLarge) || n != 5 || output.String() != "12345" {
		t.Fatalf("over-limit CopyBounded n=%d output=%q err=%v", n, output.String(), err)
	}
}

func TestStreamCopyBoundedCancellationClosesSlowReader(t *testing.T) {
	reader, writer := io.Pipe()
	t.Cleanup(func() { _ = writer.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := CopyBounded(
			ctx,
			io.Discard,
			reader,
			MaxSingleFileBytes,
			ErrStreamInputTooLarge,
		)
		result <- err
	}()
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("CopyBounded err = %v; want context canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("CopyBounded did not stop after cancellation")
	}
}

func TestStreamCopyBoundedCancellationClosesBlockedDestination(t *testing.T) {
	destinationReader, destinationWriter := io.Pipe()
	t.Cleanup(func() { _ = destinationReader.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := CopyBounded(
			ctx,
			destinationWriter,
			io.NopCloser(strings.NewReader("payload")),
			MaxSingleFileBytes,
			ErrStreamInputTooLarge,
		)
		result <- err
	}()
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("CopyBounded err = %v; want context canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("CopyBounded did not close a blocked destination after cancellation")
	}
}

func TestStreamCopyExactCancellationClosesBlockedDestination(t *testing.T) {
	destinationReader, destinationWriter := io.Pipe()
	t.Cleanup(func() { _ = destinationReader.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := copyExactCancelable(
			ctx,
			destinationWriter,
			strings.NewReader("payload"),
			int64(len("payload")),
		)
		result <- err
	}()
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("copyExactCancelable err = %v; want context canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("copyExactCancelable did not close a blocked destination")
	}
}

func TestStreamExtractTarRoundTripAndBoundaries(t *testing.T) {
	archive := buildTestTar(t, map[string][]byte{
		"report.txt":        []byte("report"),
		"nested/result.csv": []byte("a,b\n1,2\n"),
	}, nil)
	dest := secureTempDir(t)
	stats, err := ExtractTarStream(
		context.Background(),
		io.NopCloser(bytes.NewReader(archive)),
		dest,
		StreamLimits{MaxSingleBytes: 16, MaxTotalBytes: 32, MaxFiles: 2},
	)
	if err != nil || stats.Files != 2 || stats.Bytes != 14 {
		t.Fatalf("ExtractTarStream stats=%#v err=%v", stats, err)
	}
	for name, want := range map[string]string{
		"report.txt":        "report",
		"nested/result.csv": "a,b\n1,2\n",
	} {
		got, readErr := os.ReadFile(filepath.Join(dest, name))
		if readErr != nil || string(got) != want {
			t.Fatalf("%s = %q err=%v", name, got, readErr)
		}
		info, statErr := os.Stat(filepath.Join(dest, name))
		if statErr != nil {
			t.Fatalf("%s stat err=%v", name, statErr)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode=%v", name, info.Mode().Perm())
		}
	}

	tenFiles := make(map[string][]byte, MaxCopyFiles)
	for index := range MaxCopyFiles {
		tenFiles[filepath.Join("files", string(rune('a'+index)))] = []byte{byte(index)}
	}
	archive = buildTestTar(t, tenFiles, nil)
	stats, err = ExtractTarStream(
		context.Background(),
		io.NopCloser(bytes.NewReader(archive)),
		secureTempDir(t),
		StreamLimits{
			MaxSingleBytes: MaxSingleFileBytes,
			MaxTotalBytes:  MaxCopyOutBytes,
			MaxFiles:       MaxCopyFiles,
		},
	)
	if err != nil || stats.Files != MaxCopyFiles {
		t.Fatalf("10-file boundary stats=%#v err=%v", stats, err)
	}
}

func TestStreamExtractTarRejectsLimitsAboveHardCeilings(t *testing.T) {
	for name, limits := range map[string]StreamLimits{
		"single file": {
			MaxSingleBytes: MaxSingleFileBytes + 1,
			MaxTotalBytes:  MaxCopyOutBytes,
			MaxFiles:       MaxCopyFiles,
		},
		"aggregate bytes": {
			MaxSingleBytes: MaxSingleFileBytes,
			MaxTotalBytes:  MaxCopyOutBytes + 1,
			MaxFiles:       MaxCopyFiles,
		},
		"file count": {
			MaxSingleBytes: MaxSingleFileBytes,
			MaxTotalBytes:  MaxCopyOutBytes,
			MaxFiles:       MaxCopyFiles + 1,
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ExtractTarStream(
				context.Background(),
				io.NopCloser(strings.NewReader("")),
				secureTempDir(t),
				limits,
			)
			if !errors.Is(err, ErrStreamPolicyDenied) {
				t.Fatalf("ExtractTarStream err = %v; want policy denied", err)
			}
		})
	}
}

func TestStreamExtractTarRejectsArchiveAttacks(t *testing.T) {
	tests := []struct {
		name   string
		header tar.Header
	}{
		{name: "absolute", header: tar.Header{Name: "/etc/passwd", Typeflag: tar.TypeReg}},
		{name: "parent", header: tar.Header{Name: "../escape", Typeflag: tar.TypeReg}},
		{name: "symlink", header: tar.Header{Name: "link", Typeflag: tar.TypeSymlink, Linkname: "target"}},
		{name: "hardlink", header: tar.Header{Name: "link", Typeflag: tar.TypeLink, Linkname: "target"}},
		{name: "character device", header: tar.Header{Name: "dev", Typeflag: tar.TypeChar}},
		{name: "block device", header: tar.Header{Name: "dev", Typeflag: tar.TypeBlock}},
		{name: "fifo", header: tar.Header{Name: "fifo", Typeflag: tar.TypeFifo}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			archive := buildTestTar(t, nil, []tar.Header{test.header})
			_, err := ExtractTarStream(
				context.Background(),
				io.NopCloser(bytes.NewReader(archive)),
				secureTempDir(t),
				StreamLimits{MaxSingleBytes: 16, MaxTotalBytes: 32, MaxFiles: 2},
			)
			if !errors.Is(err, ErrStreamPolicyDenied) {
				t.Fatalf("ExtractTarStream err = %v; want policy denied", err)
			}
		})
	}
}

func TestStreamExtractTarRejectsOversizeCountOverwriteAndDestinationSymlink(t *testing.T) {
	oversized := buildTestTar(
		t,
		nil,
		[]tar.Header{{Name: "large.bin", Typeflag: tar.TypeReg, Size: 17}},
	)
	_, err := ExtractTarStream(
		context.Background(),
		io.NopCloser(bytes.NewReader(oversized)),
		secureTempDir(t),
		StreamLimits{MaxSingleBytes: 16, MaxTotalBytes: 32, MaxFiles: 2},
	)
	if !errors.Is(err, ErrStreamOutputTooLarge) {
		t.Fatalf("oversized err = %v", err)
	}

	archive := buildTestTar(t, map[string][]byte{
		"a": []byte("1"),
		"b": []byte("2"),
		"c": []byte("3"),
	}, nil)
	_, err = ExtractTarStream(
		context.Background(),
		io.NopCloser(bytes.NewReader(archive)),
		secureTempDir(t),
		StreamLimits{MaxSingleBytes: 16, MaxTotalBytes: 32, MaxFiles: 2},
	)
	if !errors.Is(err, ErrStreamOutputTooLarge) {
		t.Fatalf("file-count err = %v", err)
	}

	dest := secureTempDir(t)
	if err := os.WriteFile(filepath.Join(dest, "existing"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	archive = buildTestTar(t, map[string][]byte{"existing": []byte("replace")}, nil)
	_, err = ExtractTarStream(
		context.Background(),
		io.NopCloser(bytes.NewReader(archive)),
		dest,
		StreamLimits{MaxSingleBytes: 16, MaxTotalBytes: 32, MaxFiles: 2},
	)
	if !errors.Is(err, ErrStreamPolicyDenied) {
		t.Fatalf("overwrite err = %v", err)
	}
	body, readErr := os.ReadFile(filepath.Join(dest, "existing"))
	if readErr != nil || string(body) != "keep" {
		t.Fatalf("existing file changed: %q err=%v", body, readErr)
	}

	dest = secureTempDir(t)
	victim := t.TempDir()
	if err := os.Symlink(victim, filepath.Join(dest, "nested")); err != nil {
		t.Fatal(err)
	}
	archive = buildTestTar(t, map[string][]byte{"nested/escape": []byte("no")}, nil)
	_, err = ExtractTarStream(
		context.Background(),
		io.NopCloser(bytes.NewReader(archive)),
		dest,
		StreamLimits{MaxSingleBytes: 16, MaxTotalBytes: 32, MaxFiles: 2},
	)
	if !errors.Is(err, ErrStreamPolicyDenied) {
		t.Fatalf("destination symlink err = %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(victim, "escape")); !os.IsNotExist(statErr) {
		t.Fatalf("archive escaped through destination symlink: %v", statErr)
	}
}

func TestStreamExtractTarRejectsDestinationAncestorSymlink(t *testing.T) {
	outer := secureTempDir(t)
	victim := secureTempDir(t)
	if err := os.Mkdir(filepath.Join(victim, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, filepath.Join(outer, "linked")); err != nil {
		t.Fatal(err)
	}
	archive := buildTestTar(t, map[string][]byte{"escape": []byte("no")}, nil)
	_, err := ExtractTarStream(
		context.Background(),
		io.NopCloser(bytes.NewReader(archive)),
		filepath.Join(outer, "linked", "nested"),
		StreamLimits{MaxSingleBytes: 16, MaxTotalBytes: 32, MaxFiles: 2},
	)
	if !errors.Is(err, ErrStreamPolicyDenied) {
		t.Fatalf("destination ancestor symlink err = %v; want policy denied", err)
	}
	if _, statErr := os.Stat(filepath.Join(victim, "nested", "escape")); !os.IsNotExist(statErr) {
		t.Fatalf("archive escaped through destination ancestor symlink: %v", statErr)
	}
}

func TestStreamExtractTarCancellationClosesSlowReader(t *testing.T) {
	reader, writer := io.Pipe()
	headerReady := make(chan struct{})
	releaseWriter := make(chan struct{})
	writerDone := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	dest := secureTempDir(t)
	go func() {
		_, err := ExtractTarStream(
			ctx,
			reader,
			dest,
			StreamLimits{MaxSingleBytes: 16, MaxTotalBytes: 32, MaxFiles: 2},
		)
		result <- err
	}()
	go func() {
		defer close(writerDone)
		tw := tar.NewWriter(writer)
		_ = tw.WriteHeader(&tar.Header{Name: "slow.bin", Typeflag: tar.TypeReg, Size: 16})
		close(headerReady)
		<-releaseWriter
		_ = tw.Close()
		_ = writer.Close()
	}()
	<-headerReady
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("ExtractTarStream err = %v; want context canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("ExtractTarStream did not stop after cancellation")
	}
	close(releaseWriter)
	select {
	case <-writerDone:
	case <-time.After(time.Second):
		t.Fatal("tar writer did not stop after reader cancellation")
	}
}

type recordingReadCloser struct {
	mu             sync.Mutex
	reader         io.Reader
	maxReadRequest int
}

func (r *recordingReadCloser) Read(p []byte) (int, error) {
	r.mu.Lock()
	if len(p) > r.maxReadRequest {
		r.maxReadRequest = len(p)
	}
	r.mu.Unlock()
	return r.reader.Read(p)
}

func (r *recordingReadCloser) Close() error {
	return nil
}

func buildTestTar(t *testing.T, files map[string][]byte, extra []tar.Header) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	for name, body := range files {
		if err := writer.WriteHeader(&tar.Header{
			Name:     name,
			Typeflag: tar.TypeReg,
			Mode:     0o777,
			Size:     int64(len(body)),
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	for index := range extra {
		header := extra[index]
		if header.Mode == 0 {
			header.Mode = 0o777
		}
		if err := writer.WriteHeader(&header); err != nil {
			t.Fatal(err)
		}
	}
	_ = writer.Close()
	return buffer.Bytes()
}

func secureTempDir(t *testing.T) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}
