package adminaccount

import (
	"context"
	"errors"
	"time"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/model"

	"numind-server/pkg/auth"

	"gorm.io/gorm"
)

// AdminAccountBiz 管理员账户业务接口
type AdminAccountBiz interface {
	Login(ctx context.Context, username, password string) (*model.Admin, error)
	Create(ctx context.Context, req *CreateAdminRequest) (*model.Admin, error)
	GetByID(ctx context.Context, id uint) (*model.Admin, error)
	GetByUsername(ctx context.Context, username string) (*model.Admin, error)
	Update(ctx context.Context, id uint, req *UpdateAdminRequest) (*model.Admin, error)
	List(ctx context.Context, offset, limit int) (int64, []*model.Admin, error)
	Delete(ctx context.Context, id uint) error
	ChangePassword(ctx context.Context, id uint, oldPassword, newPassword string) error
}

// adminAccountBiz 管理员账户业务实现
type adminAccountBiz struct {
	ds store.IStore
}

var _ AdminAccountBiz = (*adminAccountBiz)(nil)

// NewAdminAccountBiz 创建管理员账户业务实例
func NewAdminAccountBiz(ds store.IStore) AdminAccountBiz {
	return &adminAccountBiz{ds: ds}
}

// CreateAdminRequest 创建管理员请求
type CreateAdminRequest struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
	Nickname string `json:"nickname"`
	Email    string `json:"email"`
	Status   int    `json:"status"`
	Remark   string `json:"remark"`
}

// UpdateAdminRequest 更新管理员请求
type UpdateAdminRequest struct {
	Nickname *string `json:"nickname"`
	Email    *string `json:"email"`
	Status   *int    `json:"status"`
	Remark   *string `json:"remark"`
}

// Login 管理员登录
func (b *adminAccountBiz) Login(ctx context.Context, username, password string) (*model.Admin, error) {
	admin, err := b.ds.AdminAccounts().GetByUsername(ctx, username)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, err
		}
		return nil, err
	}

	// 检查状态
	if admin.Status != model.AdminStatusEnabled {
		return nil, gorm.ErrRecordNotFound
	}

	// 验证密码
	if err := auth.Compare(admin.Password, password); err != nil {
		return nil, err
	}

	// 更新最后登录时间
	now := time.Now()
	admin.LastLogin = &now
	if err := b.ds.AdminAccounts().UpdateLastLogin(ctx, admin.ID); err != nil {
		// 登录时间更新失败不影响登录流程
	}

	return admin, nil
}

// Create 创建管理员账户
func (b *adminAccountBiz) Create(ctx context.Context, req *CreateAdminRequest) (*model.Admin, error) {
	// 检查用户名是否已存在
	_, err := b.ds.AdminAccounts().GetByUsername(ctx, req.Username)
	if err == nil {
		return nil, errors.New("用户名已存在")
	}
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}

	// 加密密码
	hashedPassword, err := auth.Encrypt(req.Password)
	if err != nil {
		return nil, err
	}

	admin := &model.Admin{
		Username: req.Username,
		Password: hashedPassword,
		Nickname: req.Nickname,
		Email:    req.Email,
		Status:   req.Status,
		Remark:   req.Remark,
	}

	if admin.Status == 0 {
		admin.Status = model.AdminStatusEnabled
	}

	if err := b.ds.AdminAccounts().Create(ctx, admin); err != nil {
		return nil, err
	}

	// 清除密码字段
	admin.Password = ""

	return admin, nil
}

// GetByID 根据ID获取管理员账户
func (b *adminAccountBiz) GetByID(ctx context.Context, id uint) (*model.Admin, error) {
	admin, err := b.ds.AdminAccounts().GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	// 清除密码字段
	admin.Password = ""
	return admin, nil
}

// GetByUsername 根据用户名获取管理员账户
func (b *adminAccountBiz) GetByUsername(ctx context.Context, username string) (*model.Admin, error) {
	admin, err := b.ds.AdminAccounts().GetByUsername(ctx, username)
	if err != nil {
		return nil, err
	}
	// 清除密码字段
	admin.Password = ""
	return admin, nil
}

// Update 更新管理员账户
func (b *adminAccountBiz) Update(ctx context.Context, id uint, req *UpdateAdminRequest) (*model.Admin, error) {
	admin, err := b.ds.AdminAccounts().GetByID(ctx, id)
	if err != nil {
		return nil, err
	}

	if req.Nickname != nil {
		admin.Nickname = *req.Nickname
	}
	if req.Email != nil {
		admin.Email = *req.Email
	}
	if req.Status != nil {
		admin.Status = *req.Status
	}
	if req.Remark != nil {
		admin.Remark = *req.Remark
	}

	if err := b.ds.AdminAccounts().Update(ctx, admin); err != nil {
		return nil, err
	}

	// 清除密码字段
	admin.Password = ""

	return admin, nil
}

// List 获取管理员账户列表
func (b *adminAccountBiz) List(ctx context.Context, offset, limit int) (int64, []*model.Admin, error) {
	count, admins, err := b.ds.AdminAccounts().List(ctx, offset, limit)
	if err != nil {
		return 0, nil, err
	}

	// 清除所有密码字段
	for i := range admins {
		admins[i].Password = ""
	}

	return count, admins, nil
}

// Delete 删除管理员账户
func (b *adminAccountBiz) Delete(ctx context.Context, id uint) error {
	return b.ds.AdminAccounts().Delete(ctx, id)
}

// ChangePassword 修改密码
func (b *adminAccountBiz) ChangePassword(ctx context.Context, id uint, oldPassword, newPassword string) error {
	admin, err := b.ds.AdminAccounts().GetByID(ctx, id)
	if err != nil {
		return err
	}

	// 验证旧密码
	if err := auth.Compare(admin.Password, oldPassword); err != nil {
		return err
	}

	// 加密新密码
	hashedPassword, err := auth.Encrypt(newPassword)
	if err != nil {
		return err
	}

	admin.Password = hashedPassword
	return b.ds.AdminAccounts().Update(ctx, admin)
}
