package seed

import (
	"bytes"
	"context"
	"fmt"
	"log"

	"numind-server/internal/pkg/model"

	"gorm.io/gorm"
)

// trackDef 赛道定义
type trackDef struct {
	Slug     string
	Name     string
	Desc     string
	DataFile string
	Order    int
}

var tracks = []trackDef{
	{"overseas_property", "海外房产", "海外房产投资与IP打造相关观点", "data/overseas_property.txt", 1},
	{"insurance", "保险", "保险产品销售与IP建设相关观点", "data/insurance.txt", 2},
	{"overseas_ip", "通用观点库", "赛道通用销售观点", "data/overseas_ip.txt", 3},
	{"study_immigration", "留学移民", "留学移民行业销售与IP相关观点", "data/study_immigration.txt", 4},
}

// Seeder 观点库初始化器
type Seeder struct {
	db *gorm.DB
}

// NewSeeder 创建观点库初始化器
func NewSeeder(db *gorm.DB) *Seeder {
	return &Seeder{db: db}
}

// SeedOpinionTracks 初始化系统内置观点赛道（幂等）
// 返回需要 Ingest 的赛道列表（slug → markdown 内容）
func (s *Seeder) SeedOpinionTracks(ctx context.Context) map[string]SeedResult {
	results := make(map[string]SeedResult)

	for _, t := range tracks {
		// 1. 幂等检查
		var existing model.OpinionTrack
		err := s.db.WithContext(ctx).Where("slug = ?", t.Slug).First(&existing).Error
		if err == nil && existing.DocID > 0 {
			// 检查关联文档状态
			var doc model.KnowledgeDocument
			if s.db.First(&doc, existing.DocID).Error == nil && doc.Status == "COMPLETED" {
				log.Printf("[Seed] Opinion track '%s' already seeded (doc_id=%d), skipping", t.Slug, existing.DocID)
				continue
			}
			// 文档状态不是 COMPLETED，删除旧文档重新创建
			log.Printf("[Seed] Opinion track '%s' doc_id=%d not COMPLETED (status=%s), will re-seed",
				t.Slug, existing.DocID, doc.Status)
		}

		// 2. 读取嵌入的 TXT 数据，直接作为 Markdown 使用
		data, readErr := OpinionData.ReadFile(t.DataFile)
		if readErr != nil {
			log.Printf("[Seed] Failed to read embedded file %s: %v", t.DataFile, readErr)
			continue
		}

		markdown := string(data)

		// 3. 返回结果，由调用方执行 Ingest
		results[t.Slug] = SeedResult{
			Track:    t,
			Markdown: markdown,
			Count:    0,
			Existing: existing.ID > 0,
			TrackID:  existing.ID,
		}

		log.Printf("[Seed] Prepared track '%s': %d bytes markdown", t.Slug, len(markdown))
	}

	return results
}

// CreateOrUpdateTrack 创建或更新赛道记录
func (s *Seeder) CreateOrUpdateTrack(ctx context.Context, slug string, docID uint) error {
	td := findTrackDef(slug)
	if td == nil {
		return fmt.Errorf("unknown track slug: %s", slug)
	}

	var existing model.OpinionTrack
	err := s.db.WithContext(ctx).Where("slug = ?", slug).First(&existing).Error
	if err == nil {
		// 更新
		existing.DocID = docID
		existing.Name = td.Name
		existing.Description = td.Desc
		return s.db.WithContext(ctx).Save(&existing).Error
	}

	// 创建
	track := model.OpinionTrack{
		Slug:        td.Slug,
		Name:        td.Name,
		Description: td.Desc,
		IsEnabled:   true,
		SortOrder:   td.Order,
		DocID:       docID,
	}
	return s.db.WithContext(ctx).Create(&track).Error
}

// SeedResult 单个赛道的 seed 结果
type SeedResult struct {
	Track    trackDef
	Markdown string
	Count    int
	Existing bool
	TrackID  uint
}

// MarkdownReader 返回 Markdown 内容的 Reader
func (r *SeedResult) MarkdownReader() *bytes.Reader {
	return bytes.NewReader([]byte(r.Markdown))
}

func findTrackDef(slug string) *trackDef {
	for _, t := range tracks {
		if t.Slug == slug {
			return &t
		}
	}
	return nil
}
