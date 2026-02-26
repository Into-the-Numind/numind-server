package seed

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"numind-server/internal/pkg/model"

	"gorm.io/gorm"
)

// OpinionItem 观点库 JSON 结构
type OpinionItem struct {
	ID        string   `json:"观点ID"`
	Title     string   `json:"标题"`
	Insight   string   `json:"核心洞察"`
	Quote     string   `json:"金句"`
	CaseStudy string   `json:"案例"`
	Metaphor  string   `json:"比喻"`
	Scenarios []string `json:"适用场景"`
	ScriptSrc string   `json:"脚本来源"`
}

// trackDef 赛道定义
type trackDef struct {
	Slug     string
	Name     string
	Desc     string
	DataFile string
	Order    int
}

var tracks = []trackDef{
	{"overseas_property", "海外房产", "海外房产投资与IP打造相关观点", "data/overseas_property.json", 1},
	{"insurance", "保险", "保险产品销售与IP建设相关观点", "data/insurance.json", 2},
	{"overseas_ip", "海外IP", "海外高势能IP运营相关观点", "data/overseas_ip.json", 3},
	{"study_immigration", "留学移民", "留学移民行业销售与IP相关观点", "data/study_immigration.json", 4},
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

		// 2. 读取嵌入的 JSON 数据
		data, readErr := OpinionData.ReadFile(t.DataFile)
		if readErr != nil {
			log.Printf("[Seed] Failed to read embedded file %s: %v", t.DataFile, readErr)
			continue
		}

		// 3. 解析 JSON
		var opinions []OpinionItem
		if parseErr := json.Unmarshal(data, &opinions); parseErr != nil {
			log.Printf("[Seed] Failed to parse JSON for track '%s': %v", t.Slug, parseErr)
			continue
		}

		// 4. 转换为 Markdown
		markdown := convertToMarkdown(opinions, t.Name)

		// 5. 返回结果，由调用方执行 Ingest
		results[t.Slug] = SeedResult{
			Track:    t,
			Markdown: markdown,
			Count:    len(opinions),
			Existing: existing.ID > 0,
			TrackID:  existing.ID,
		}

		log.Printf("[Seed] Prepared track '%s': %d opinions, %d bytes markdown", t.Slug, len(opinions), len(markdown))
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

func convertToMarkdown(opinions []OpinionItem, trackName string) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("# %s 观点库\n\n", trackName))

	for _, op := range opinions {
		sb.WriteString(fmt.Sprintf("## %s · %s\n\n", op.ID, op.Title))

		if op.Insight != "" {
			sb.WriteString(fmt.Sprintf("**核心洞察**: %s\n\n", op.Insight))
		}
		if op.Quote != "" {
			sb.WriteString(fmt.Sprintf("**金句**: %s\n\n", op.Quote))
		}
		if op.CaseStudy != "" {
			sb.WriteString(fmt.Sprintf("**案例**: %s\n\n", op.CaseStudy))
		}
		if op.Metaphor != "" {
			sb.WriteString(fmt.Sprintf("**比喻**: %s\n\n", op.Metaphor))
		}
		if len(op.Scenarios) > 0 {
			sb.WriteString(fmt.Sprintf("**适用场景**: %s\n\n", strings.Join(op.Scenarios, "、")))
		}

		sb.WriteString("---\n\n")
	}

	return sb.String()
}
