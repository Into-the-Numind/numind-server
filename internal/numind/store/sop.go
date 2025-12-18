package store

import (
	"numind-server/internal/pkg/model"

	"gorm.io/gorm"
)

// ISopStore SOP数据访问接口
type ISopStore interface {
	// Template CRUD
	CreateTemplate(template *model.SopTemplate) error
	GetTemplate(id uint) (*model.SopTemplate, error)
	ListTemplates(offset, limit int) ([]model.SopTemplate, int64, error)
	UpdateTemplate(id uint, updates map[string]interface{}) error
	DeleteTemplate(id uint) error

	// Node CRUD
	CreateNode(node *model.SopNode) error
	GetNode(id uint) (*model.SopNode, error)
	ListNodesByTemplate(templateID uint) ([]model.SopNode, error)
	UpdateNode(id uint, updates map[string]interface{}) error
	DeleteNode(id uint) error

	// Run operations
	CreateRun(run *model.SopRun) error
	GetRun(id uint) (*model.SopRun, error)
	UpdateRun(id uint, updates map[string]interface{}) error
	ListRuns(offset, limit int, userID *uint) ([]model.SopRun, int64, error)

	// NodeRun operations
	CreateNodeRun(nodeRun *model.SopNodeRun) error
	GetNodeRun(id uint) (*model.SopNodeRun, error)
	ListNodeRunsByRun(runID uint) ([]model.SopNodeRun, error)
	UpdateNodeRun(id uint, updates map[string]interface{}) error

	// Note operations
	CreateNote(note *model.SopNote) error
	GetNote(id uint) (*model.SopNote, error)
	ListNotesByUser(userID uint, offset, limit int) ([]model.SopNote, int64, error)

	// File operations
	CreateFile(file *model.SopFile) error
	GetFile(id uint) (*model.SopFile, error)
	ListFilesByRun(runID uint) ([]model.SopFile, error)
	ListFilesByUser(userID uint, offset, limit int) ([]model.SopFile, int64, error)
	UpdateFile(id uint, updates map[string]interface{}) error
	DeleteFile(id uint) error
}

type sopStore struct {
	db *gorm.DB
}

// NewSopStore 创建SOP Store实例
func NewSopStore(db *gorm.DB) ISopStore {
	return &sopStore{db: db}
}

// Template operations
func (s *sopStore) CreateTemplate(template *model.SopTemplate) error {
	return s.db.Create(template).Error
}

func (s *sopStore) GetTemplate(id uint) (*model.SopTemplate, error) {
	var template model.SopTemplate
	err := s.db.First(&template, id).Error
	if err != nil {
		return nil, err
	}
	return &template, nil
}

func (s *sopStore) ListTemplates(offset, limit int) ([]model.SopTemplate, int64, error) {
	var templates []model.SopTemplate
	var total int64

	if err := s.db.Model(&model.SopTemplate{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}

	if err := s.db.Offset(offset).Limit(limit).Order("created_at DESC").Find(&templates).Error; err != nil {
		return nil, 0, err
	}

	return templates, total, nil
}

func (s *sopStore) UpdateTemplate(id uint, updates map[string]interface{}) error {
	return s.db.Model(&model.SopTemplate{}).Where("id = ?", id).Updates(updates).Error
}

func (s *sopStore) DeleteTemplate(id uint) error {
	return s.db.Delete(&model.SopTemplate{}, id).Error
}

// Node operations
func (s *sopStore) CreateNode(node *model.SopNode) error {
	return s.db.Create(node).Error
}

func (s *sopStore) GetNode(id uint) (*model.SopNode, error) {
	var node model.SopNode
	err := s.db.Preload("Template").First(&node, id).Error
	if err != nil {
		return nil, err
	}
	return &node, nil
}

func (s *sopStore) ListNodesByTemplate(templateID uint) ([]model.SopNode, error) {
	var nodes []model.SopNode
	err := s.db.Where("template_id = ?", templateID).Order("sort ASC").Find(&nodes).Error
	return nodes, err
}

func (s *sopStore) UpdateNode(id uint, updates map[string]interface{}) error {
	return s.db.Model(&model.SopNode{}).Where("id = ?", id).Updates(updates).Error
}

func (s *sopStore) DeleteNode(id uint) error {
	return s.db.Delete(&model.SopNode{}, id).Error
}

// Run operations
func (s *sopStore) CreateRun(run *model.SopRun) error {
	return s.db.Create(run).Error
}

func (s *sopStore) GetRun(id uint) (*model.SopRun, error) {
	var run model.SopRun
	// 不预加载FinalNote，因为没有外键关联（避免循环依赖）
	err := s.db.Preload("Template").Preload("User").First(&run, id).Error
	if err != nil {
		return nil, err
	}
	return &run, nil
}

func (s *sopStore) UpdateRun(id uint, updates map[string]interface{}) error {
	return s.db.Model(&model.SopRun{}).Where("id = ?", id).Updates(updates).Error
}

func (s *sopStore) ListRuns(offset, limit int, userID *uint) ([]model.SopRun, int64, error) {
	var runs []model.SopRun
	var total int64

	query := s.db.Model(&model.SopRun{})
	if userID != nil {
		query = query.Where("user_id = ?", *userID)
	}

	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	if err := query.Preload("Template").Preload("User").
		Offset(offset).Limit(limit).Order("created_at DESC").Find(&runs).Error; err != nil {
		return nil, 0, err
	}

	return runs, total, nil
}

// NodeRun operations
func (s *sopStore) CreateNodeRun(nodeRun *model.SopNodeRun) error {
	return s.db.Create(nodeRun).Error
}

func (s *sopStore) GetNodeRun(id uint) (*model.SopNodeRun, error) {
	var nodeRun model.SopNodeRun
	err := s.db.Preload("Node").Preload("Template").First(&nodeRun, id).Error
	if err != nil {
		return nil, err
	}
	return &nodeRun, nil
}

func (s *sopStore) ListNodeRunsByRun(runID uint) ([]model.SopNodeRun, error) {
	var nodeRuns []model.SopNodeRun
	err := s.db.Where("run_id = ?", runID).Preload("Node").Order("sort ASC").Find(&nodeRuns).Error
	return nodeRuns, err
}

func (s *sopStore) UpdateNodeRun(id uint, updates map[string]interface{}) error {
	return s.db.Model(&model.SopNodeRun{}).Where("id = ?", id).Updates(updates).Error
}

// Note operations
func (s *sopStore) CreateNote(note *model.SopNote) error {
	return s.db.Create(note).Error
}

func (s *sopStore) GetNote(id uint) (*model.SopNote, error) {
	var note model.SopNote
	err := s.db.Preload("Template").Preload("User").Preload("Run").First(&note, id).Error
	if err != nil {
		return nil, err
	}
	return &note, nil
}

func (s *sopStore) ListNotesByUser(userID uint, offset, limit int) ([]model.SopNote, int64, error) {
	var notes []model.SopNote
	var total int64

	query := s.db.Model(&model.SopNote{}).Where("user_id = ?", userID)

	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	if err := query.Preload("Template").Offset(offset).Limit(limit).
		Order("created_at DESC").Find(&notes).Error; err != nil {
		return nil, 0, err
	}

	return notes, total, nil
}

// File operations
func (s *sopStore) CreateFile(file *model.SopFile) error {
	return s.db.Create(file).Error
}

func (s *sopStore) GetFile(id uint) (*model.SopFile, error) {
	var file model.SopFile
	err := s.db.Preload("User").Preload("Run").Preload("Node").First(&file, id).Error
	if err != nil {
		return nil, err
	}
	return &file, nil
}

func (s *sopStore) ListFilesByRun(runID uint) ([]model.SopFile, error) {
	var files []model.SopFile
	err := s.db.Where("run_id = ?", runID).Order("created_at DESC").Find(&files).Error
	return files, err
}

func (s *sopStore) ListFilesByUser(userID uint, offset, limit int) ([]model.SopFile, int64, error) {
	var files []model.SopFile
	var total int64

	query := s.db.Model(&model.SopFile{}).Where("user_id = ?", userID)

	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	if err := query.Preload("Run").Preload("Node").
		Offset(offset).Limit(limit).Order("created_at DESC").Find(&files).Error; err != nil {
		return nil, 0, err
	}

	return files, total, nil
}

func (s *sopStore) UpdateFile(id uint, updates map[string]interface{}) error {
	return s.db.Model(&model.SopFile{}).Where("id = ?", id).Updates(updates).Error
}

func (s *sopStore) DeleteFile(id uint) error {
	return s.db.Delete(&model.SopFile{}, id).Error
}
