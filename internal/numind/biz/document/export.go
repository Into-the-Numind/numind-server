package document

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"numind-server/internal/numind/biz/sandbox"
	"numind-server/internal/pkg/errno"

	"gorm.io/gorm"
)

const docxContentType = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"

// userGuard 是每用户单并发导出守卫：同一用户同时只允许 1 个 pdf/docx 导出在跑，
// 防止导出与 agent run 争抢共享 sandbox pool 时把池子吃满。
type userGuard struct {
	mu     sync.Mutex
	active map[uint]bool
}

func newUserGuard() *userGuard { return &userGuard{active: make(map[uint]bool)} }

// tryAcquire 尝试占用某用户的导出槽；已占用返回 false。
func (g *userGuard) tryAcquire(userID uint) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.active[userID] {
		return false
	}
	g.active[userID] = true
	return true
}

// release 释放某用户的导出槽。
func (g *userGuard) release(userID uint) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.active, userID)
}

// Export 导出文档为指定格式。
//   - md:   直接返回 content_md（不走沙箱，始终可用）
//   - pdf/docx: 借 sandbox + pandoc 转换（每用户单并发；沙箱未启用优雅降级 ErrDocumentExportUnavailable）
//
// 返回 (下载文件名, contentType, 数据字节, error)。含 ownership 校验。
func (s *service) Export(ctx context.Context, userID uint, id uint64, format string) (string, string, []byte, error) {
	d, err := s.store.GetByID(ctx, id)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", "", nil, errno.ErrDocumentNotFound
		}
		return "", "", nil, err
	}
	if d.UserID != userID {
		return "", "", nil, errno.ErrDocumentNotFound
	}

	base := safeFilename(d.Title)
	switch format {
	case "md":
		return base + ".md", "text/markdown; charset=utf-8", []byte(d.ContentMD), nil

	case "pdf", "docx":
		if s.exportGuard != nil {
			if !s.exportGuard.tryAcquire(userID) {
				return "", "", nil, errno.ErrDocumentExportBusy
			}
			defer s.exportGuard.release(userID)
		}
		data, err := s.exportViaPandoc(ctx, d.ContentMD, format)
		if err != nil {
			return "", "", nil, err
		}
		ctype := docxContentType
		if format == "pdf" {
			ctype = "application/pdf"
		}
		return base + "." + format, ctype, data, nil

	default:
		return "", "", nil, errno.ErrDocumentExportFormat
	}
}

// exportViaPandoc 在 sandbox 内用 pandoc 把 markdown 转成 pdf/docx，返回字节。
// 沙箱未启用 → ErrDocumentExportUnavailable（md 路径不受影响，调用方仍可导 md）。
func (s *service) exportViaPandoc(ctx context.Context, md, format string) ([]byte, error) {
	if s.pool == nil {
		return nil, errno.ErrDocumentExportUnavailable
	}
	sess, err := s.pool.Borrow(ctx)
	if err != nil {
		if errors.Is(err, sandbox.ErrSandboxDisabled) {
			return nil, errno.ErrDocumentExportUnavailable
		}
		if errors.Is(err, sandbox.ErrPoolExhausted) {
			return nil, errno.ErrDocumentExportBusy // 池满（与 agent run 争用）→ 稍后重试
		}
		return nil, fmt.Errorf("exportViaPandoc: borrow sandbox: %w", err)
	}
	exitCode := 0
	errMsg := ""
	defer func() { _ = s.pool.Return(sess, exitCode, errMsg) }()

	dc := s.pool.DockerClient()
	// fresh 容器只有 /workdir tmpfs；CopyToContainer 用 tar -C path.Dir(dst)，父目录须先建。
	if err := dc.ExecMkdir(ctx, sess.ContainerID, "/workdir/input", "/workdir/output"); err != nil {
		errMsg = err.Error()
		return nil, fmt.Errorf("exportViaPandoc: mkdir: %w", err)
	}
	// WriteFile 第二参是相对 /workdir 的路径（内部前缀 /workdir/）；传绝对路径会路径翻倍。
	if err := sandbox.WriteFile(ctx, sess, "input/doc.md", []byte(md), dc); err != nil {
		errMsg = err.Error()
		return nil, fmt.Errorf("exportViaPandoc: write input: %w", err)
	}

	outName := "out." + format
	cmd := "pandoc /workdir/input/doc.md -o /workdir/output/" + outName
	if format == "pdf" {
		// weasyprint 已装于 skill 镜像（含 CJK 字体 wqy-zenhei）。S4 须实测此调用方式；
		// 若 pandoc 内置 weasyprint 引擎不可用，改两步 md→html→weasyprint。
		cmd += " --pdf-engine=weasyprint"
	}

	res, err := sandbox.ExecCommand(ctx, sess, cmd, dc)
	if err != nil {
		errMsg = err.Error()
		return nil, fmt.Errorf("exportViaPandoc: exec pandoc: %w", err)
	}
	exitCode = res.ExitCode
	if res.ExitCode != 0 {
		errMsg = res.Stderr
		return nil, fmt.Errorf("exportViaPandoc: pandoc exit %d: %s", res.ExitCode, strings.TrimSpace(res.Stderr))
	}

	// 二进制安全：用 docker cp（CopyFromContainer）取产物，避免 cat-over-exec 损坏二进制。
	hostDir, err := os.MkdirTemp("", "docexport-")
	if err != nil {
		return nil, fmt.Errorf("exportViaPandoc: tmpdir: %w", err)
	}
	defer func() { _ = os.RemoveAll(hostDir) }()

	if err := dc.CopyFromContainer(ctx, sess.ContainerID, "/workdir/output/"+outName, hostDir); err != nil {
		return nil, fmt.Errorf("exportViaPandoc: copy out: %w", err)
	}
	data, err := os.ReadFile(filepath.Join(hostDir, outName))
	if err != nil {
		return nil, fmt.Errorf("exportViaPandoc: read out: %w", err)
	}
	return data, nil
}

// safeFilename 把文档标题清洗成可放进 Content-Disposition 的基名（去路径分隔符/控制字符）。
func safeFilename(title string) string {
	repl := func(r rune) rune {
		if r == '/' || r == '\\' || r == '\n' || r == '\r' || r == '\t' || r == '"' || r == 0 {
			return '_'
		}
		return r
	}
	out := strings.TrimSpace(strings.Map(repl, title))
	if out == "" {
		return "document"
	}
	return out
}
