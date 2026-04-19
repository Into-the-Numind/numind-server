package store

import (
	"context"
	"numind-server/internal/pkg/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// IUserModelPreferenceStore 用户模型偏好数据访问接口
type IUserModelPreferenceStore interface {
	Get(ctx context.Context, userID uint, feature string) (*model.UserModelPreference, error)
	GetAll(ctx context.Context, userID uint) ([]model.UserModelPreference, error)
	Upsert(ctx context.Context, pref *model.UserModelPreference) error
}

type userModelPreferenceStore struct {
	db *gorm.DB
}

var _ IUserModelPreferenceStore = (*userModelPreferenceStore)(nil)

// NewUserModelPreferenceStore 创建用户模型偏好 Store 实例
func NewUserModelPreferenceStore(db *gorm.DB) IUserModelPreferenceStore {
	return &userModelPreferenceStore{db: db}
}

// Get 获取指定用户在指定功能下的模型偏好
func (s *userModelPreferenceStore) Get(ctx context.Context, userID uint, feature string) (*model.UserModelPreference, error) {
	var pref model.UserModelPreference
	if err := s.db.WithContext(ctx).
		Where("user_id = ? AND feature = ?", userID, feature).
		First(&pref).Error; err != nil {
		return nil, err
	}
	return &pref, nil
}

// GetAll 获取指定用户的所有模型偏好
func (s *userModelPreferenceStore) GetAll(ctx context.Context, userID uint) ([]model.UserModelPreference, error) {
	var prefs []model.UserModelPreference
	if err := s.db.WithContext(ctx).
		Where("user_id = ?", userID).
		Find(&prefs).Error; err != nil {
		return nil, err
	}
	return prefs, nil
}

// Upsert 创建或更新用户模型偏好（ON CONFLICT (user_id, feature) DO UPDATE）
func (s *userModelPreferenceStore) Upsert(ctx context.Context, pref *model.UserModelPreference) error {
	return s.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "user_id"}, {Name: "feature"}},
			DoUpdates: clause.AssignmentColumns([]string{"model_key", "thinking", "updated_at"}),
		}).
		Create(pref).Error
}
