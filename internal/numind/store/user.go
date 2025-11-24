package store

import (
	"context"
	"errors"

	"gorm.io/gorm"

	"numind-server/internal/pkg/model"
)

// UserStore 定义了 user 模块在 store 层所实现的方法.
type UserStore interface {
	Create(ctx context.Context, user *model.User) error
	Get(ctx context.Context, username string) (*model.User, error)
	GetByID(ctx context.Context, userID uint) (*model.User, error)
	Update(ctx context.Context, user *model.User) error
	List(ctx context.Context, offset, limit int) (int64, []*model.User, error)
	Delete(ctx context.Context, username string) error
	GetUser(userID uint) (*model.User, error)
	GetUserByID(ctx context.Context, userID uint) (*model.User, error)
	UpdateUser(ctx context.Context, user *model.User) error
	UpdateWechatUser(ctx context.Context, openid string, update map[string]interface{}) error
}

// UserStore 接口的实现.
type users struct {
	db *gorm.DB
}

// 确保 users 实现了 UserStore 接口.
var _ UserStore = (*users)(nil)

func newUsers(db *gorm.DB) *users {
	return &users{db}
}

// Create 插入一条 user 记录.
func (u *users) Create(ctx context.Context, user *model.User) error {
	return u.db.Create(&user).Error
}

// Get 根据用户名查询指定 user 的数据库记录.
func (u *users) Get(ctx context.Context, username string) (*model.User, error) {
	var user model.User
	if err := u.db.Where("username = ?", username).First(&user).Error; err != nil {
		return nil, err
	}

	return &user, nil
}

// GetByID 根据用户ID查询指定 user 的数据库记录.
func (u *users) GetByID(ctx context.Context, userID uint) (*model.User, error) {
	var user model.User
	if err := u.db.Where("id = ?", userID).First(&user).Error; err != nil {
		return nil, err
	}

	return &user, nil
}

// Update 更新一条 user 数据库记录.
func (u *users) Update(ctx context.Context, user *model.User) error {
	return u.db.Save(user).Error
}

// List 根据 offset 和 limit 返回 user 列表.
func (u *users) List(ctx context.Context, offset, limit int) (count int64, ret []*model.User, err error) {
	err = u.db.Offset(offset).Limit(defaultLimit(limit)).Order("id desc").Find(&ret).
		Offset(-1).
		Limit(-1).
		Count(&count).
		Error

	return
}

// Delete 根据 username 删除数据库 user 记录.
func (u *users) Delete(ctx context.Context, username string) error {
	err := u.db.Where("username = ?", username).Delete(&model.User{}).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}

	return nil
}

func (u *users) GetUser(userID uint) (*model.User, error) {
	var user model.User
	if err := u.db.Where("id = ?", userID).First(&user).Error; err != nil {
		return nil, err
	}
	return &user, nil
}

func (u *users) GetUserByID(ctx context.Context, userID uint) (*model.User, error) {
	var user model.User
	if err := u.db.Where("id = ?", userID).First(&user).Error; err != nil {
		return nil, err
	}
	return &user, nil
}

func (u *users) UpdateUser(ctx context.Context, user *model.User) error {
	// 使用Select只更新特定字段，避免更新UnionID等敏感字段
	return u.db.Model(user).Select("nickname", "avatar_url", "updated_at").Updates(user).Error
}

func (u *users) UpdateWechatUser(ctx context.Context, openid string, update map[string]interface{}) error {
	return u.db.Model(&model.User{}).Where("open_id = ?", openid).Updates(update).Error
}

func NewUserStore(db *gorm.DB) UserStore {
	return &users{db}
}
