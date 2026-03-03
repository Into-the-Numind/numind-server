package user

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/jinzhu/copier"
	"github.com/spf13/viper"
	"golang.org/x/sync/errgroup"
	"gorm.io/gorm"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"
	v1 "numind-server/pkg/api/numind/v1"
	"numind-server/pkg/auth"
	"numind-server/pkg/token"
)

// UserBiz 定义了 user 模块在 biz 层所实现的方法.
type UserBiz interface {
	ChangePassword(ctx context.Context, username string, r *v1.ChangePasswordRequest) error
	Login(ctx context.Context, r *v1.LoginRequest) (*v1.LoginResponse, error)
	Create(ctx context.Context, r *v1.CreateUserRequest) error
	Get(ctx context.Context, username string) (*v1.GetUserResponse, error)
	GetByID(ctx context.Context, userID uint) (*v1.GetUserResponse, error)
	List(ctx context.Context, offset, limit int) (*v1.ListUserResponse, error)
	Update(ctx context.Context, username string, r *v1.UpdateUserRequest) error
	Delete(ctx context.Context, username string) error

	// 基于 User model 的方法
	GetCurrentUser(ctx context.Context, userID uint) (*model.User, error)
	GetCurrentUserWithStats(ctx context.Context, userID uint) (*model.User, error)
	UpdateUserProfile(ctx context.Context, userID uint, req *v1.UpdateUserProfileRequest) error
	UpdateUserAvatar(ctx context.Context, userID uint, avatarURL string) error

	// Web端登录
	WebLogin(req *v1.WebLoginRequest) (*v1.WebLoginResponse, error)

	// 客户管理
	CreateCustomer(ctx context.Context, parentUserID uint, r *v1.CreateCustomerRequest) error
	CheckUsernameUsage(ctx context.Context, username string) error
}

// UserBiz 接口的实现.
type userBiz struct {
	ds store.IStore
}

// 确保 userBiz 实现了 UserBiz 接口.
var _ UserBiz = (*userBiz)(nil)

// New 创建一个实现了 UserBiz 接口的实例.
func New(ds store.IStore) *userBiz {
	return &userBiz{ds: ds}
}

// ChangePassword 是 UserBiz 接口中 `ChangePassword` 方法的实现.
func (b *userBiz) ChangePassword(ctx context.Context, username string, r *v1.ChangePasswordRequest) error {
	user, err := b.ds.Users().Get(ctx, username)
	if err != nil {
		return err
	}

	if err := auth.Compare(user.Password, r.OldPassword); err != nil {
		return errno.ErrPasswordIncorrect
	}

	user.Password, _ = auth.Encrypt(r.NewPassword)
	if err := b.ds.Users().Update(ctx, user); err != nil {
		return err
	}

	return nil
}

// Login 是 UserBiz 接口中 `Login` 方法的实现.
func (b *userBiz) Login(ctx context.Context, r *v1.LoginRequest) (*v1.LoginResponse, error) {
	// 获取登录用户的所有信息
	user, err := b.ds.Users().Get(ctx, r.Username)
	if err != nil {
		return nil, errno.ErrUserNotFound
	}

	// 对比传入的明文密码和数据库中已加密过的密码是否匹配
	if err := auth.Compare(user.Password, r.Password); err != nil {
		return nil, errno.ErrPasswordIncorrect
	}

	// 如果匹配成功，说明登录成功，签发 token 并返回
	t, err := token.Sign(r.Username)
	if err != nil {
		return nil, errno.ErrSignToken
	}

	return &v1.LoginResponse{Token: t}, nil
}

// Create 是 UserBiz 接口中 `Create` 方法的实现.
func (b *userBiz) Create(ctx context.Context, r *v1.CreateUserRequest) error {
	var user model.User
	_ = copier.Copy(&user, r)

	if err := b.ds.Users().Create(ctx, &user); err != nil {
		if match, _ := regexp.MatchString("Duplicate entry '.*' for key 'username'", err.Error()); match {
			return errno.ErrUserAlreadyExist
		}

		return err
	}

	return nil
}

// Get 是 UserBiz 接口中 `Get` 方法的实现.
func (b *userBiz) Get(ctx context.Context, username string) (*v1.GetUserResponse, error) {
	user, err := b.ds.Users().Get(ctx, username)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errno.ErrUserNotFound
		}

		return nil, err
	}

	var resp v1.GetUserResponse
	_ = copier.Copy(&resp, user)

	resp.CreatedAt = user.CreatedAt.Format("2006-01-02 15:04:05")
	resp.UpdatedAt = user.UpdatedAt.Format("2006-01-02 15:04:05")

	return &resp, nil
}

// GetByID 是 UserBiz 接口中 `GetByID` 方法的实现.
func (b *userBiz) GetByID(ctx context.Context, userID uint) (*v1.GetUserResponse, error) {
	user, err := b.ds.Users().GetByID(ctx, userID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errno.ErrUserNotFound
		}
		return nil, err
	}

	var resp v1.GetUserResponse
	_ = copier.Copy(&resp, user)

	resp.CreatedAt = user.CreatedAt.Format("2006-01-02 15:04:05")
	resp.UpdatedAt = user.UpdatedAt.Format("2006-01-02 15:04:05")

	return &resp, nil
}

// GetCurrentUser 获取当前用户信息（基于 User model）
func (b *userBiz) GetCurrentUser(ctx context.Context, userID uint) (*model.User, error) {
	user, err := b.ds.Users().GetUserByID(ctx, userID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errno.ErrUserNotFound
		}
		return nil, err
	}

	return user, nil
}

// GetCurrentUserWithStats 获取当前用户信息（包含统计信息）
func (b *userBiz) GetCurrentUserWithStats(ctx context.Context, userID uint) (*model.User, error) {
	user, err := b.ds.Users().GetUserByID(ctx, userID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errno.ErrUserNotFound
		}
		return nil, err
	}

	// 直接从user结构体中获取book_num和book_all_num，这些字段在创建、状态变更、删除时已经实时更新
	return user, nil
}

// UpdateUserProfile 是 UserBiz 接口中 `UpdateUserProfile` 方法的实现.
func (b *userBiz) UpdateUserProfile(ctx context.Context, userID uint, req *v1.UpdateUserProfileRequest) error {
	user, err := b.ds.Users().GetUserByID(ctx, userID)
	if err != nil {
		return err
	}

	// 只更新允许的字段
	if req.Nickname != nil {
		user.Nickname = *req.Nickname
	}
	if req.AvatarURL != nil {
		user.AvatarURL = *req.AvatarURL
	}

	// 需要添加一个基于 User model 的更新方法
	if err := b.ds.Users().UpdateUser(ctx, user); err != nil {
		return err
	}

	return nil
}

// UpdateUserAvatar 是 UserBiz 接口中 `UpdateUserAvatar` 方法的实现.
func (b *userBiz) UpdateUserAvatar(ctx context.Context, userID uint, avatarURL string) error {
	user, err := b.ds.Users().GetUserByID(ctx, userID)
	if err != nil {
		return err
	}

	user.AvatarURL = avatarURL

	if err := b.ds.Users().UpdateUser(ctx, user); err != nil {
		return err
	}

	return nil
}

// List 是 UserBiz 接口中 `List` 方法的实现.
func (b *userBiz) List(ctx context.Context, offset, limit int) (*v1.ListUserResponse, error) {
	count, list, err := b.ds.Users().List(ctx, offset, limit)
	if err != nil {
		log.C(ctx).Errorw("Failed to list users from storage", "err", err)
		return nil, err
	}

	var m sync.Map
	eg, ctx := errgroup.WithContext(ctx)
	// 使用 goroutine 提高接口性能
	for _, item := range list {
		user := item
		eg.Go(func() error {
			select {
			case <-ctx.Done():
				return nil
			default:

				m.Store(user.ID, &v1.UserInfo{
					Username:  user.Username,
					Nickname:  user.Nickname,
					Email:     "", // User 模型没有 Email 字段
					Phone:     user.Phone,
					PostCount: 0,
					CreatedAt: user.CreatedAt.Format("2006-01-02 15:04:05"),
					UpdatedAt: user.UpdatedAt.Format("2006-01-02 15:04:05"),
				})

				return nil
			}
		})
	}

	if err := eg.Wait(); err != nil {
		log.C(ctx).Errorw("Failed to wait all function calls returned", "err", err)
		return nil, err
	}

	users := make([]*v1.UserInfo, 0, len(list))
	for _, item := range list {
		user, _ := m.Load(item.ID)
		users = append(users, user.(*v1.UserInfo))
	}

	log.C(ctx).Debugw("Get users from backend storage", "count", len(users))

	return &v1.ListUserResponse{TotalCount: count, Users: users}, nil
}

// Update 是 UserBiz 接口中 `Update` 方法的实现.
func (b *userBiz) Update(ctx context.Context, username string, user *v1.UpdateUserRequest) error {
	userModel, err := b.ds.Users().Get(ctx, username)
	if err != nil {
		return err
	}

	// User 模型没有 Email 字段，跳过 Email 更新
	// if user.Email != nil {
	// 	userModel.Email = *user.Email
	// }

	if user.Nickname != nil {
		userModel.Nickname = *user.Nickname
	}

	if user.Phone != nil {
		userModel.Phone = *user.Phone
	}

	if err := b.ds.Users().Update(ctx, userModel); err != nil {
		return err
	}

	return nil
}

// Delete 是 UserBiz 接口中 `Delete` 方法的实现.
func (b *userBiz) Delete(ctx context.Context, username string) error {
	if err := b.ds.Users().Delete(ctx, username); err != nil {
		return err
	}

	return nil
}

// generateWebToken 生成Web端登录JWT token（7天有效期）
func (s *userBiz) generateWebToken(user *model.User) (string, error) {
	// Web端登录token有效期为7天
	expireDays := 7
	expireHours := expireDays * 24 // 7天 = 168小时

	claims := jwt.MapClaims{
		"user_id": user.ID,
		"exp":     time.Now().Add(time.Duration(expireHours) * time.Hour).Unix(),
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(viper.GetString("jwt.secret")))
}

// WebLogin Web端用户名密码登录
func (s *userBiz) WebLogin(req *v1.WebLoginRequest) (*v1.WebLoginResponse, error) {
	// 根据用户名查找用户
	var user model.User
	err := s.ds.DB().Where("username = ?", req.Username).First(&user).Error

	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, fmt.Errorf("用户名或密码错误")
		}
		return nil, fmt.Errorf("查询用户失败: %v", err)
	}

	// 验证密码（明文比对）
	if user.Password != req.Password {
		return nil, fmt.Errorf("用户名或密码错误")
	}

	// 更新最后登录时间
	now := time.Now()
	user.LastLogin = &now
	s.ds.DB().Save(&user)

	// 生成JWT token（Web端登录使用7天有效期）
	token, err := s.generateWebToken(&user)
	if err != nil {
		return nil, fmt.Errorf("生成token失败: %v", err)
	}

	return &v1.WebLoginResponse{
		AccessToken: token,
		TokenType:   "Bearer",
		User:        &user,
	}, nil
}

// CreateCustomer 是 UserBiz 接口中 `CreateCustomer` 方法的实现.
func (b *userBiz) CreateCustomer(ctx context.Context, parentUserID uint, r *v1.CreateCustomerRequest) error {
	// 1. 权限校验：检查当前用户是否是父级账户（ParentUserID为0或NULL）
	// 注意：这里传入的 parentUserID 是调用者的 ID
	parentUser, err := b.ds.Users().GetUserByID(ctx, parentUserID)
	if err != nil {
		return err
	}

	if parentUser.ParentUserID != nil {
		return fmt.Errorf("操作失败：只有一级客户可以创建子客户")
	}

	// 2. 查重：检查用户名和手机号是否已存在
	var count int64
	query := b.ds.DB().Model(&model.User{})
	if r.Phone != "" {
		query = query.Where("username = ? OR phone = ?", r.Username, r.Phone)
	} else {
		query = query.Where("username = ?", r.Username)
	}

	if err := query.Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return errno.ErrUserAlreadyExist
	}

	// 3. 校验可选的会员等级参数
	tier := r.Tier
	if tier == "" {
		tier = model.UserTierFree
	}
	if tier != model.UserTierFree && tier != model.UserTierStandard && tier != model.UserTierPremium {
		return errno.ErrInvalidParameter.SetMessage("无效的会员等级: %s", tier)
	}
	if tier != model.UserTierFree && (r.Months < 1 || r.Months > 12) {
		return errno.ErrInvalidParameter.SetMessage("开通时长需在 1-12 个月之间")
	}

	// 4. 数据构造
	user := model.User{
		Username:     r.Username,
		Password:     r.Password,
		Nickname:     r.Nickname,
		Phone:        r.Phone,
		UserTier:     model.UserTierFree,
		ParentUserID: &parentUserID,
		Status:       1,
	}

	// 如果指定了付费等级，设置 UserTier、TierExpires 和会员月周期起点
	now := time.Now()
	var tierExpires time.Time
	if tier != model.UserTierFree {
		user.UserTier = tier
		tierExpires = now.AddDate(0, 0, r.Months*30)
		user.TierExpires = &tierExpires
		user.MonthlyResetAt = &now
	}

	// 5. 入库 + 授权（事务）
	return b.ds.DB().Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&user).Error; err != nil {
			return err
		}

		// 如果指定了付费等级，记录等级变更日志
		if tier != model.UserTierFree {
			changeLog := model.TierChangeLog{
				ParentUserID:   parentUserID,
				SubUserID:      user.ID,
				OldTier:        model.UserTierFree,
				NewTier:        tier,
				Months:         r.Months,
				OldTierExpires: nil,
				NewTierExpires: tierExpires,
			}
			if err := tx.Create(&changeLog).Error; err != nil {
				return fmt.Errorf("记录等级变更日志失败: %w", err)
			}
		}

		// 继承父账户的所有SOP模板权限（单条SQL批量插入）
		if err := b.ds.Customers().BulkGrantAllTemplates(ctx, parentUserID, user.ID); err != nil {
			log.C(ctx).Warnw("Failed to grant SOP templates to new sub-user", "sub_user_id", user.ID, "err", err)
		}

		return nil
	})
}

// CheckUsernameUsage 检查用户名是否已被占用.
func (b *userBiz) CheckUsernameUsage(ctx context.Context, username string) error {
	var count int64
	if err := b.ds.DB().Model(&model.User{}).Where("username = ?", username).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return errno.ErrUserAlreadyExist
	}
	return nil
}
