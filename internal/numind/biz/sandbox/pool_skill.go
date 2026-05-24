package sandbox

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// SkillSession is the invoke_skill tool's sandbox borrowing result.
// It wraps a base Session (borrowed from Pool.Borrow) with skill-specific
// context: which skill is mounted, which input files are injected, and the
// host-side temporary output directory.
//
// SkillSession is safe to use only from a single goroutine (the invoke_skill
// tool call); it is NOT concurrency-safe across goroutines.
type SkillSession struct {
	*Session // embeds: ContainerID, ImageTag, Config, BorrowedAt

	// SkillName is the skill mounted in this session (e.g. "xlsx-author").
	SkillName string

	// UserID is the owning user's ID — used for COS multi-tenant path isolation
	// and for disambiguating the host-side temp directory name.
	UserID uint

	// InputFiles lists the filenames successfully injected into
	// the container's /workdir/input/ directory via CopyFileIn.
	InputFiles []string

	// OutputDir is the host-side temporary directory that CollectOutputs
	// copies /workdir/output/ into. Cleaned up by ReturnSkillSession.
	OutputDir string
}

// OutputFile describes a single sandbox-produced file that was collected from
// /workdir/output/ and uploaded to COS.
type OutputFile struct {
	Filename  string
	MimeType  string
	SizeBytes int64
	COSURL    string
	LocalPath string // ephemeral; removed after ReturnSkillSession
}

// SkillPool extends Pool with the four methods needed by the invoke_skill flow.
// The concrete implementations are on agentSandboxPool and disabledPool.
type SkillPool interface {
	Pool

	// AcquireForSkill borrows a container from the warm pool, prepares
	// /workdir/input and /workdir/output inside the container, copies the
	// skill directory into /skills/<skillName>/ inside the container, and
	// returns a SkillSession. Returns ErrSkillNotFound if
	// cfg.SkillsRoot/<skillName>/ does not exist.
	// userID is stored in the session for COS path isolation and temp-dir naming.
	AcquireForSkill(ctx context.Context, skillName string, userID uint) (*SkillSession, error)

	// CopyFileIn injects a file into the container's /workdir/input/<filename>.
	// The filename is sanitized (SanitizeInputFile) before injection.
	CopyFileIn(ctx context.Context, sess *SkillSession, filename string, data []byte) error

	// CollectOutputs reads all files from /workdir/output/ in the container,
	// runs ScanOutput on each, and uploads survivors to COS. Returns the
	// list of uploaded files (may be empty if /output/ is empty or all files
	// failed scanning). Caller must invoke ReturnSkillSession afterwards.
	CollectOutputs(ctx context.Context, sess *SkillSession, userID uint) ([]OutputFile, error)

	// ReturnSkillSession cleans up the host-side OutputDir, then returns the
	// underlying Session to the pool (destroying the container and requesting
	// a replacement). Idempotent: second call returns ErrSessionReturned.
	ReturnSkillSession(sess *SkillSession, exitCode int, errMsg string) error
}

// ===========================================================================
// agentSandboxPool — SkillPool implementation
// ===========================================================================

var _ SkillPool = (*agentSandboxPool)(nil)

// AcquireForSkill implements SkillPool.AcquireForSkill.
//
// Sequence:
//  1. Validate skill directory exists under cfg.SkillsRoot.
//  2. Borrow a container from the warm pool (may block up to PoolMaxWaitMs).
//  3. Create /workdir/input, /workdir/output, /skills/<skillName> inside the container.
//  4. COPY all files from the host skill directory into /skills/<skillName>/ in the container.
//  5. Create a host-side temp directory (named with userID) for CollectOutputs.
//  6. Wrap in SkillSession and return.
//
// Decision (pragmatic v1): Since warm-pool containers are pre-spawned without
// skill mounts, we COPY the skill directory into the container at acquire time
// via CopyToContainer (filepath.WalkDir + dc.CopyToContainer per file).
// For v2 we can switch to named profiles / bind mounts.
func (p *agentSandboxPool) AcquireForSkill(ctx context.Context, skillName string, userID uint) (*SkillSession, error) {
	// 1. Validate skill exists.
	skillDir := filepath.Join(p.cfg.SkillsRoot, skillName)
	if _, err := os.Stat(skillDir); os.IsNotExist(err) {
		return nil, fmt.Errorf("AcquireForSkill: %w (skill=%s, root=%s)", ErrSkillNotFound, skillName, p.cfg.SkillsRoot)
	} else if err != nil {
		return nil, fmt.Errorf("AcquireForSkill: stat skill dir: %w", err)
	}

	// 2. Borrow from warm pool.
	sess, err := p.Borrow(ctx)
	if err != nil {
		return nil, err
	}

	// 3. Prepare directories inside the container.
	if err := p.dc.ExecMkdir(ctx, sess.ContainerID,
		"/workdir/input",
		"/workdir/output",
		"/skills/"+skillName,
	); err != nil {
		// Clean up and return — don't orphan the session.
		_ = p.Return(sess, 1, "mkdir failed: "+err.Error())
		return nil, fmt.Errorf("AcquireForSkill: prepare container dirs: %w", err)
	}

	// 4. Copy skill files into the container at /skills/<skillName>/.
	// Walk all files under the host-side skill directory and CopyToContainer each one.
	walkErr := filepath.WalkDir(skillDir, func(hostPath string, d fs.DirEntry, walkDirErr error) error {
		if walkDirErr != nil {
			return walkDirErr
		}
		if d.IsDir() {
			// Ensure the directory exists inside the container (mkdir -p).
			rel, relErr := filepath.Rel(skillDir, hostPath)
			if relErr != nil {
				return fmt.Errorf("WalkDir rel: %w", relErr)
			}
			if rel == "." {
				return nil // root already created in step 3
			}
			containerDir := "/skills/" + skillName + "/" + filepath.ToSlash(rel)
			return p.dc.ExecMkdir(ctx, sess.ContainerID, containerDir)
		}
		// Regular file — read and copy.
		data, readErr := os.ReadFile(hostPath)
		if readErr != nil {
			return fmt.Errorf("AcquireForSkill: read skill file %s: %w", hostPath, readErr)
		}
		rel, relErr := filepath.Rel(skillDir, hostPath)
		if relErr != nil {
			return fmt.Errorf("AcquireForSkill: rel skill file: %w", relErr)
		}
		containerPath := "/skills/" + skillName + "/" + filepath.ToSlash(rel)
		return p.dc.CopyToContainer(ctx, sess.ContainerID, containerPath, bytes.NewReader(data))
	})
	if walkErr != nil {
		_ = p.Return(sess, 1, "copy skill files failed: "+walkErr.Error())
		return nil, fmt.Errorf("AcquireForSkill: copy skill files: %w", walkErr)
	}

	// 5. Create host-side output temp directory — name includes userID for traceability.
	outputDir, err := os.MkdirTemp("", fmt.Sprintf("sandbox-output-u%d-%d-*", userID, time.Now().UnixNano()))
	if err != nil {
		_ = p.Return(sess, 1, "output tmpdir failed: "+err.Error())
		return nil, fmt.Errorf("AcquireForSkill: create output tmpdir: %w", err)
	}

	return &SkillSession{
		Session:   sess,
		SkillName: skillName,
		UserID:    userID,
		OutputDir: outputDir,
	}, nil
}

// CopyFileIn implements SkillPool.CopyFileIn.
func (p *agentSandboxPool) CopyFileIn(ctx context.Context, sess *SkillSession, filename string, data []byte) error {
	if sess == nil {
		return ErrSandboxDisabled
	}
	// Security: sanitize before injection.
	if err := SanitizeInputFile(filename, data); err != nil {
		return err
	}
	containerPath := "/workdir/input/" + filename
	if err := p.dc.CopyToContainer(ctx, sess.ContainerID, containerPath, bytes.NewReader(data)); err != nil {
		return fmt.Errorf("CopyFileIn %s: %w", filename, err)
	}
	sess.InputFiles = append(sess.InputFiles, filename)
	return nil
}

// CollectOutputs implements SkillPool.CollectOutputs.
//
// Steps:
//  1. docker cp container:/workdir/output/ → sess.OutputDir
//  2. Walk sess.OutputDir, run ScanOutput on each file.
//  3. Upload survivors to COS concurrently (up to cfg.COSUploadConcurrency).
//  4. Return the list of uploaded OutputFile descriptors.
func (p *agentSandboxPool) CollectOutputs(ctx context.Context, sess *SkillSession, userID uint) ([]OutputFile, error) {
	if sess == nil {
		return nil, ErrSandboxDisabled
	}

	// 1. Copy /workdir/output/ from container to host.
	if err := p.dc.CopyFromContainer(ctx, sess.ContainerID, "/workdir/output/.", sess.OutputDir); err != nil {
		return nil, fmt.Errorf("CollectOutputs: docker cp output: %w", err)
	}

	// 2. Walk the output directory.
	entries, err := os.ReadDir(sess.OutputDir)
	if err != nil {
		return nil, fmt.Errorf("CollectOutputs: read output dir: %w", err)
	}

	type scanResult struct {
		name      string
		localPath string
		data      []byte
		mimeType  string
		size      int64
	}

	var toUpload []scanResult
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		localPath := filepath.Join(sess.OutputDir, entry.Name())

		// Security: sanitize output filename (prevent path traversal in URL/key).
		safeName := sanitiseOutputFilename(entry.Name())
		if safeName == "" {
			p.logger.Warnw("CollectOutputs: skipping unsafe filename", "filename", entry.Name())
			continue
		}

		data, readErr := os.ReadFile(localPath)
		if readErr != nil {
			p.logger.Warnw("CollectOutputs: read file failed, skipping", "path", localPath, "error", readErr)
			continue
		}

		// Detect MIME type.
		mimeType := http.DetectContentType(data)
		if idx := strings.Index(mimeType, ";"); idx != -1 {
			mimeType = strings.TrimSpace(mimeType[:idx])
		}

		// 2a. Run ScanOutput with configurable maxBytes and detected MIME for validation.
		maxBytes := int64(p.cfg.OutputMaxSizeMB) * 1024 * 1024
		if scanErr := ScanOutput(localPath, mimeType, maxBytes); scanErr != nil {
			p.logger.Warnw("CollectOutputs: scan failed, dropping file",
				"filename", entry.Name(), "error", scanErr)
			// Drop this file — continue to next.
			continue
		}

		info, _ := os.Stat(localPath)
		var size int64
		if info != nil {
			size = info.Size()
		}

		toUpload = append(toUpload, scanResult{
			name:      safeName,
			localPath: localPath,
			data:      data,
			mimeType:  mimeType,
			size:      size,
		})
	}

	if len(toUpload) == 0 {
		return []OutputFile{}, nil
	}

	// 3. Upload to COS concurrently.
	cosPrefix := BuildCOSPrefix(userID)
	concurrency := p.cfg.COSUploadConcurrency
	if concurrency <= 0 {
		concurrency = 3
	}

	type uploadResult struct {
		of  OutputFile
		err error
	}

	sem := make(chan struct{}, concurrency)
	results := make([]uploadResult, len(toUpload))
	var wg sync.WaitGroup

	for i, item := range toUpload {
		wg.Add(1)
		go func(idx int, sr scanResult) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			cosURL, uploadErr := UploadOutputFile(ctx, COSUploadConfig{Prefix: cosPrefix},
				sr.localPath, sr.name, sr.mimeType, sr.data)
			if uploadErr != nil {
				p.logger.Warnw("CollectOutputs: COS upload failed",
					"filename", sr.name, "error", uploadErr)
				results[idx] = uploadResult{err: uploadErr}
				return
			}
			results[idx] = uploadResult{of: OutputFile{
				Filename:  sr.name,
				MimeType:  sr.mimeType,
				SizeBytes: sr.size,
				COSURL:    cosURL,
				LocalPath: sr.localPath,
			}}
		}(i, item)
	}
	wg.Wait()

	// 4. Collect results; return ErrCOSUploadFailed if ANY upload failed.
	var outputs []OutputFile
	var uploadFailed bool
	for _, r := range results {
		if r.err != nil {
			uploadFailed = true
			continue
		}
		outputs = append(outputs, r.of)
	}

	if uploadFailed {
		// Return partial results + error. Caller can inspect outputs for
		// successfully uploaded files.
		return outputs, ErrCOSUploadFailed
	}
	return outputs, nil
}

// ReturnSkillSession implements SkillPool.ReturnSkillSession.
// Cleans up the host-side temp output directory, then returns the underlying
// Session to the pool (which destroys the container).
func (p *agentSandboxPool) ReturnSkillSession(sess *SkillSession, exitCode int, errMsg string) error {
	if sess == nil {
		return nil
	}
	// Best-effort cleanup of host-side temp directory.
	if sess.OutputDir != "" {
		if err := os.RemoveAll(sess.OutputDir); err != nil {
			p.logger.Warnw("ReturnSkillSession: cleanup output dir failed (non-fatal)",
				"dir", sess.OutputDir, "error", err)
		}
	}
	return p.Return(sess.Session, exitCode, errMsg)
}

// ===========================================================================
// disabledPool — SkillPool stub (all methods return ErrSandboxDisabled)
// ===========================================================================

var _ SkillPool = (*disabledPool)(nil)

func (p *disabledPool) AcquireForSkill(_ context.Context, _ string, _ uint) (*SkillSession, error) {
	return nil, ErrSandboxDisabled
}

func (p *disabledPool) CopyFileIn(_ context.Context, _ *SkillSession, _ string, _ []byte) error {
	return ErrSandboxDisabled
}

func (p *disabledPool) CollectOutputs(_ context.Context, _ *SkillSession, _ uint) ([]OutputFile, error) {
	return nil, ErrSandboxDisabled
}

func (p *disabledPool) ReturnSkillSession(_ *SkillSession, _ int, _ string) error {
	return nil // idempotent / no-op for disabled pool
}

// ===========================================================================
// Helpers
// ===========================================================================

// sanitiseOutputFilename replaces unsafe characters in an output filename.
// Returns an empty string if the filename cannot be made safe.
func sanitiseOutputFilename(name string) string {
	// Strip directory components (container may produce nested paths).
	name = filepath.Base(name)
	// Only allow [a-zA-Z0-9._-].
	var safe strings.Builder
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-' {
			safe.WriteRune(r)
		} else {
			safe.WriteByte('_')
		}
	}
	result := safe.String()
	// Reject if still empty or consists only of dots.
	if result == "" || strings.Trim(result, ".") == "" {
		return ""
	}
	return result
}
