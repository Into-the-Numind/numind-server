package document

import (
	"context"
	"errors"
	"strings"

	"numind-server/internal/numind/biz/sandbox"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/model"
	"numind-server/internal/pkg/parser"
	"numind-server/internal/pkg/util"

	"gorm.io/gorm"
)

// IDocumentService 定义文档系统的业务接口（document-system v1）。
type IDocumentService interface {
	// OpenFromArtifact 打开一个 agent 生成产物为可编辑文档（首次懒建档，再次返回上次编辑版）。
	OpenFromArtifact(ctx context.Context, userID uint, parentUserID *uint, req OpenReq) (*DocumentDTO, error)
	// Get 取文档（含 ownership 校验）。
	Get(ctx context.Context, userID uint, id uint64) (*DocumentDTO, error)
	// Save 保存文档正文/标题（含 ownership 校验，last-write-wins）。
	Save(ctx context.Context, userID uint, id uint64, req SaveReq) (*DocumentDTO, error)
	// Export 导出文档为 md/pdf/docx，返回 (文件名, contentType, 数据, error)。含 ownership 校验。
	Export(ctx context.Context, userID uint, id uint64, format string) (string, string, []byte, error)
}

// cosDownloader 抽象 COS 下载，便于测试注入。
type cosDownloader func(ctx context.Context, objectKey string) ([]byte, error)

// service 是 IDocumentService 的实现。
type service struct {
	store       store.IDocumentStore
	download    cosDownloader
	parser      *parser.DocumentParser
	fallback    docxFallback // v1 注入 nil；v2 注入 qwen-long 兜底
	pool        sandbox.Pool // 导出用（pdf/docx 经 pandoc）；nil 时 pdf/docx 不可导
	exportGuard *userGuard   // 每用户单并发导出守卫（须单实例持久，故在 NewService 建一次）
}

// NewService 创建文档服务（v1：无 qwen-long 兜底）。pool 用于 pdf/docx 导出，可为 nil（仅 md 可导）。
func NewService(s store.IDocumentStore, pool sandbox.Pool) IDocumentService {
	return &service{
		store:       s,
		download:    util.DownloadFromCOS,
		parser:      parser.NewDocumentParser(),
		fallback:    nil,
		pool:        pool,
		exportGuard: newUserGuard(),
	}
}

// OpenFromArtifact 见接口注释。
func (s *service) OpenFromArtifact(ctx context.Context, userID uint, parentUserID *uint, req OpenReq) (*DocumentDTO, error) {
	key, err := deriveObjectKey(req.SourceURL)
	if err != nil {
		return nil, errno.ErrDocumentSourceForbidden
	}
	// IDOR 防线：key 必须严格属于调用者的 agent 产物目录（见 objectkey.go）。
	if !isOwnedAgentOutputKey(key, userID) {
		return nil, errno.ErrDocumentSourceForbidden
	}
	if !IsEditableMime(req.Mime, req.Filename) {
		return nil, errno.ErrDocumentNotEditable
	}

	// 命中：返回上次编辑版（US5）。
	if existing, err := s.store.GetByUserAndSource(ctx, userID, key); err == nil {
		return toDTO(existing), nil
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}

	// 未命中：拉源对象 → 解析 → 建档。
	data, err := s.download(ctx, key)
	if err != nil {
		if errors.Is(err, util.ErrCOSObjectNotFound) {
			return nil, errno.ErrDocumentSourceExpired
		}
		return nil, err
	}

	mdContent, method, err := parseToMarkdown(ctx, data, req.Filename, req.Mime, s.parser, s.fallback)
	if err != nil {
		return nil, err // 已是领域错误（ParseFailed / NotEditable）
	}
	if len(mdContent) > maxContentBytes {
		return nil, errno.ErrDocumentTooLarge
	}

	doc := &model.Document{
		UserID:          userID,
		ParentUserID:    parentUserID,
		SourceObjectKey: key,
		SourceRunID:     req.RunID,
		SourceMime:      req.Mime,
		Title:           titleFromFilename(req.Filename),
		ContentMD:       mdContent,
		ParseMethod:     method,
	}
	if err := s.store.Create(ctx, doc); err != nil {
		// 并发 open race：另一个请求已建同一 (user, key) → 唯一键冲突 → 回查返回。
		if existing, e2 := s.store.GetByUserAndSource(ctx, userID, key); e2 == nil {
			return toDTO(existing), nil
		}
		return nil, err
	}
	return toDTO(doc), nil
}

// Get 见接口注释。
func (s *service) Get(ctx context.Context, userID uint, id uint64) (*DocumentDTO, error) {
	d, err := s.store.GetByID(ctx, id)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errno.ErrDocumentNotFound
		}
		return nil, err
	}
	// ownership：跨用户返回 NotFound，不泄露存在性（AC6）。
	if d.UserID != userID {
		return nil, errno.ErrDocumentNotFound
	}
	return toDTO(d), nil
}

// Save 见接口注释。
func (s *service) Save(ctx context.Context, userID uint, id uint64, req SaveReq) (*DocumentDTO, error) {
	d, err := s.store.GetByID(ctx, id)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errno.ErrDocumentNotFound
		}
		return nil, err
	}
	if d.UserID != userID {
		return nil, errno.ErrDocumentNotFound
	}
	if len(req.ContentMD) > maxContentBytes {
		return nil, errno.ErrDocumentTooLarge
	}

	title := strings.TrimSpace(req.Title)
	if title == "" {
		title = d.Title // 不传 title 保持原标题
	}
	if err := s.store.UpdateContent(ctx, id, req.ContentMD, title); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errno.ErrDocumentNotFound
		}
		return nil, err
	}
	// 回读获取权威 updated_at（autoUpdateTime 由 GORM 刷新）；回读失败退回内存值（best-effort）。
	if updated, e := s.store.GetByID(ctx, id); e == nil {
		return toDTO(updated), nil
	}
	d.ContentMD = req.ContentMD
	d.Title = title
	return toDTO(d), nil
}
