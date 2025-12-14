package user

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
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
	"numind-server/pkg/api/wechat"
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

	// 微信小程序
	WechatLogin(req *v1.WechatLoginRequest) (*v1.WechatLoginResponse, error)
	ValidateToken(tokenString string) (*model.User, error)
	UpdateWechatUser(ctx context.Context, openid string, r *v1.UpdateUserRequest) error

	// Web端登录
	WebLogin(req *v1.WebLoginRequest) (*v1.WebLoginResponse, error)

	// 用户统计更新
	IncrementUserBookNum(ctx context.Context, userID uint) error
	IncrementUserCardNum(ctx context.Context, userID uint) error
	IncrementUserChatNum(ctx context.Context, userID uint) error
	DecrementUserBookNum(ctx context.Context, userID uint) error
	UpdateUserBookStatsOnStatusChange(ctx context.Context, userID uint, oldStatus, newStatus string) error

	// 用户权限更新
	SetUserPro(ctx context.Context, userID uint, isPro bool) error

	// 会员相关方法
	UpdateUserMembership(ctx context.Context, userID uint, membershipType string, packageCount int) error
	AddUserPackageCount(ctx context.Context, userID uint, count int) error
	ConsumePackageCount(ctx context.Context, userID uint, count int) error
	UpdateMonthlyBookCount(ctx context.Context, userID uint, count int) error
	IncrementMonthlyBookCount(ctx context.Context, userID uint) error

	// 免费用户月度限制相关方法
	ResetFreeUserMonthlyBookCount(ctx context.Context, userID uint) error
	IncrementFreeUserMonthlyBookCount(ctx context.Context, userID uint) error

	// 会员类型同步方法
	SyncMembershipType(ctx context.Context, userID uint, newType string, resetMonthly bool) error
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

// getWechatPhone 获取微信手机号
func (s *userBiz) getWechatPhone(phoneCode, accessToken string) (*wechat.WechatPhoneResponse, error) {
	url := fmt.Sprintf("https://api.weixin.qq.com/wxa/business/getuserphonenumber?access_token=%s", accessToken)

	reqBody := map[string]string{"code": phoneCode}
	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("序列化请求体失败: %v", err)
	}

	log.C(context.Background()).Infow("请求微信手机号API", "url", url, "phone_code", phoneCode)

	resp, err := http.Post(url, "application/json", bytes.NewBuffer(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("请求微信手机号API失败: %v", err)
	}
	defer resp.Body.Close()

	// 读取响应体
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("读取响应体失败: %v", err)
	}

	log.C(context.Background()).Infow("微信手机号API响应", "status", resp.StatusCode, "body", string(body))

	// 检查HTTP状态码
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("微信手机号API返回错误状态码: %d, 响应: %s", resp.StatusCode, string(body))
	}

	var phoneResp wechat.WechatPhoneResponse
	if err := json.Unmarshal(body, &phoneResp); err != nil {
		return nil, fmt.Errorf("解析微信手机号API响应失败: %v, 响应体: %s", err, string(body))
	}

	// 检查微信API错误
	if phoneResp.PhoneInfo.PurePhoneNumber == "" {
		// 尝试解析错误信息
		var errorResp struct {
			ErrCode int    `json:"errcode"`
			ErrMsg  string `json:"errmsg"`
		}
		if err := json.Unmarshal(body, &errorResp); err == nil && errorResp.ErrCode != 0 {
			return nil, fmt.Errorf("微信手机号API错误: %d - %s", errorResp.ErrCode, errorResp.ErrMsg)
		}
		return nil, fmt.Errorf("获取微信手机号失败，响应: %s", string(body))
	}

	log.C(context.Background()).Infow("成功获取微信手机号", "phone", phoneResp.PhoneInfo.PurePhoneNumber)

	return &phoneResp, nil
}

// getWechatToken 获取微信access_token
func (s *userBiz) getWechatToken(code string) (*wechat.WechatTokenResponse, error) {
	url := fmt.Sprintf("https://api.weixin.qq.com/sns/jscode2session?appid=%s&secret=%s&js_code=%s&grant_type=authorization_code",
		viper.GetString("wechat.app_id"),
		viper.GetString("wechat.app_secret"),
		code,
	)

	log.C(context.Background()).Infow("请求微信API", "url", url, "code", code)

	resp, err := http.Get(url)
	if err != nil {
		return nil, fmt.Errorf("请求微信API失败: %v", err)
	}
	defer resp.Body.Close()

	// 读取响应体
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("读取响应体失败: %v", err)
	}

	log.C(context.Background()).Infow("微信API响应", "status", resp.StatusCode, "body", string(body))

	// 检查HTTP状态码
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("微信API返回错误状态码: %d, 响应: %s", resp.StatusCode, string(body))
	}

	var tokenResp wechat.WechatTokenResponse
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return nil, fmt.Errorf("解析微信API响应失败: %v, 响应体: %s", err, string(body))
	}

	// 检查微信API错误
	if tokenResp.ErrCode != 0 {
		return nil, fmt.Errorf("微信API错误: %d - %s", tokenResp.ErrCode, tokenResp.ErrMsg)
	}

	if tokenResp.OpenID == "" {
		return nil, fmt.Errorf("获取微信token失败，响应: %s", string(body))
	}

	log.C(context.Background()).Infow("成功获取微信token", "openid", tokenResp.OpenID, "expires_in", tokenResp.ExpiresIn)

	return &tokenResp, nil
}

// generateToken 生成JWT token
func (s *userBiz) generateToken(user *model.User) (string, error) {
	expireHours := viper.GetInt("jwt.expire-hours")
	if expireHours == 0 {
		expireHours = 24 // 默认24小时
	}

	claims := jwt.MapClaims{
		"user_id": user.ID,
		"openid":  user.OpenID,
		"exp":     time.Now().Add(time.Duration(expireHours) * time.Hour).Unix(),
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(viper.GetString("jwt.secret")))
}

// findOrCreateUser 查找或创建用户
func (s *userBiz) findOrCreateUser(openID string) (*model.User, error) {
	var user model.User
	err := s.ds.DB().Where("open_id = ?", openID).First(&user).Error

	if err == gorm.ErrRecordNotFound {
		// 创建新用户，设置唯一的username
		// 使用Omit排除union_id字段，避免空字符串触发唯一索引冲突
		user = model.User{
			OpenID:   openID,
			Username: fmt.Sprintf("user_%s", openID), // 使用openid生成唯一username
		}

		if err := s.ds.DB().Omit("union_id").Create(&user).Error; err != nil {
			return nil, fmt.Errorf("创建用户失败: %v", err)
		}
	} else if err != nil {
		return nil, err
	}

	return &user, nil
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

	// 生成JWT token
	token, err := s.generateToken(&user)
	if err != nil {
		return nil, fmt.Errorf("生成token失败: %v", err)
	}

	return &v1.WebLoginResponse{
		AccessToken: token,
		TokenType:   "Bearer",
		User:        &user,
	}, nil
}

// WechatLogin 微信登录
func (s *userBiz) WechatLogin(req *v1.WechatLoginRequest) (*v1.WechatLoginResponse, error) {
	// 获取微信access_token
	var tokenResp *wechat.WechatTokenResponse
	var err error

	// 尝试获取真实的微信token
	tokenResp, err = s.getWechatToken(req.Code)
	if err != nil {
		log.C(context.Background()).Errorw("获取微信token失败", "err", err)

		// 在测试模式下，使用模拟的openid
		tokenResp = &wechat.WechatTokenResponse{
			OpenID: "666",
		}
	}

	log.C(context.Background()).Infow("微信登录处理", "openid", tokenResp.OpenID)

	// 查找或创建用户
	user, err := s.findOrCreateUser(tokenResp.OpenID)
	if err != nil {
		return nil, fmt.Errorf("用户处理失败: %v", err)
	}

	// 如果有phone_code且access_token有效，获取手机号
	if req.PhoneCode != "" && tokenResp.AccessToken != "" {
		phoneResp, err := s.getWechatPhone(req.PhoneCode, tokenResp.AccessToken)
		if err == nil && phoneResp.PhoneInfo.PurePhoneNumber != "" {
			user.Phone = phoneResp.PhoneInfo.PurePhoneNumber
			s.ds.DB().Save(user)
		}
	}

	// 生成JWT token
	token, err := s.generateToken(user)
	if err != nil {
		return nil, fmt.Errorf("生成token失败: %v", err)
	}

	return &v1.WechatLoginResponse{
		AccessToken: token,
		TokenType:   "Bearer",
		User:        user,
	}, nil
}

// ValidateToken 验证token
func (s *userBiz) ValidateToken(tokenString string) (*model.User, error) {
	token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return []byte(viper.GetString("jwt.secret")), nil
	})

	if err != nil {
		return nil, err
	}

	if claims, ok := token.Claims.(jwt.MapClaims); ok && token.Valid {
		userID := uint(claims["user_id"].(float64))

		var user model.User
		if err := s.ds.DB().First(&user, userID).Error; err != nil {
			return nil, err
		}

		return &user, nil
	}

	return nil, fmt.Errorf("invalid token")
}

// UpdateWechatUser 是 UserBiz 接口中 `UpdateWechatUser` 方法的实现.
func (b *userBiz) UpdateWechatUser(ctx context.Context, openid string, r *v1.UpdateUserRequest) error {
	updateMap := make(map[string]interface{})
	if r.Nickname != nil {
		updateMap["nickname"] = *r.Nickname
	}
	if r.Email != nil {
		updateMap["email"] = *r.Email
	}
	if r.Phone != nil {
		updateMap["phone"] = *r.Phone
	}
	if len(updateMap) == 0 {
		return nil // 没有需要更新的内容
	}
	return b.ds.Users().UpdateWechatUser(ctx, openid, updateMap)
}

// IncrementUserBookNum 增加用户的书籍数量（创建book时调用，状态为creating）
func (b *userBiz) IncrementUserBookNum(ctx context.Context, userID uint) error {
	// 使用数据库的原子操作来同时增加BookNum和BookAllNum字段
	return b.ds.DB().Model(&model.User{}).Where("id = ?", userID).
		UpdateColumns(map[string]interface{}{
			"book_num":     gorm.Expr("book_num + ?", 1),
			"book_all_num": gorm.Expr("book_all_num + ?", 1),
		}).Error
}

// DecrementUserBookNum 减少用户的书籍数量（删除book时调用）
func (b *userBiz) DecrementUserBookNum(ctx context.Context, userID uint) error {
	// 只减少book_num，book_all_num保持不变
	return b.ds.DB().Model(&model.User{}).Where("id = ?", userID).
		UpdateColumn("book_num", gorm.Expr("book_num - ?", 1)).Error
}

// UpdateUserBookStatsOnStatusChange 当book状态变化时更新用户统计
func (b *userBiz) UpdateUserBookStatsOnStatusChange(ctx context.Context, userID uint, oldStatus, newStatus string) error {
	// 如果状态从非failed变为failed，需要减少book_num和book_all_num
	if oldStatus != model.BookStatusFailed && newStatus == model.BookStatusFailed {
		return b.ds.DB().Model(&model.User{}).Where("id = ?", userID).
			UpdateColumns(map[string]interface{}{
				"book_num":     gorm.Expr("book_num - ?", 1),
				"book_all_num": gorm.Expr("book_all_num - ?", 1),
			}).Error
	}

	// 如果状态从failed变为非failed，需要增加book_all_num
	if oldStatus == model.BookStatusFailed && newStatus != model.BookStatusFailed {
		return b.ds.DB().Model(&model.User{}).Where("id = ?", userID).
			UpdateColumn("book_all_num", gorm.Expr("book_all_num + ?", 1)).Error
	}

	return nil
}

// IncrementUserCardNum 增加用户的卡片数量
func (b *userBiz) IncrementUserCardNum(ctx context.Context, userID uint) error {
	// 使用数据库的原子操作来增加CardNum字段
	return b.ds.DB().Model(&model.User{}).Where("id = ?", userID).
		UpdateColumn("card_num", gorm.Expr("card_num + ?", 1)).Error
}

// DecrementUserCardNum 减少用户的卡片数量
func (b *userBiz) DecrementUserCardNum(ctx context.Context, userID uint, count int64) error {
	// 使用数据库的原子操作来减少CardNum字段
	if count <= 0 {
		return nil
	}
	return b.ds.DB().Model(&model.User{}).Where("id = ?", userID).
		UpdateColumn("card_num", gorm.Expr("card_num - ?", count)).Error
}

// IncrementUserChatNum 增加用户的聊天数量
func (b *userBiz) IncrementUserChatNum(ctx context.Context, userID uint) error {
	// 使用数据库的原子操作来增加ChatNum字段
	return b.ds.DB().Model(&model.User{}).Where("id = ?", userID).
		UpdateColumn("chat_num", gorm.Expr("chat_num + ?", 1)).Error
}

// SetUserPro 设置用户的付费状态
func (b *userBiz) SetUserPro(ctx context.Context, userID uint, isPro bool) error {
	return b.ds.DB().Model(&model.User{}).Where("id = ?", userID).
		UpdateColumn("is_pro", isPro).Error
}

// UpdateUserMembership 更新用户会员状态
func (b *userBiz) UpdateUserMembership(ctx context.Context, userID uint, membershipType string, packageCount int) error {
	now := time.Now()
	var expiresAt *time.Time

	// 根据会员类型设置到期时间
	switch membershipType {
	case model.MembershipTypeSubscription:
		// 订阅会员需要根据订阅类型设置到期时间
		// 这里假设传入的是月度订阅，实际应该传入订阅类型参数
		expires := now.AddDate(0, 1, 0) // 1个月后
		expiresAt = &expires
	case model.MembershipTypePackage:
		// 包次数类型不需要设置到期时间，只需要增加次数
		expiresAt = nil
	default:
		return fmt.Errorf("不支持的会员类型: %s", membershipType)
	}

	// 更新用户会员信息
	updateData := map[string]interface{}{
		"membership_type": membershipType,
		"is_pro":          true, // 购买会员后设置为付费用户
	}

	if expiresAt != nil {
		updateData["membership_expires"] = expiresAt
	}

	if membershipType == model.MembershipTypePackage {
		// 包次数类型，增加次数
		updateData["package_count"] = gorm.Expr("package_count + ?", packageCount)
	}

	return b.ds.DB().Model(&model.User{}).Where("id = ?", userID).
		Updates(updateData).Error
}

// AddUserPackageCount 增加用户包次数
func (b *userBiz) AddUserPackageCount(ctx context.Context, userID uint, count int) error {
	return b.ds.DB().Model(&model.User{}).Where("id = ?", userID).
		UpdateColumn("package_count", gorm.Expr("package_count + ?", count)).Error
}

// ConsumePackageCount 消费用户包次数
func (b *userBiz) ConsumePackageCount(ctx context.Context, userID uint, count int) error {
	// 先检查用户是否有足够的包次数
	var user model.User
	if err := b.ds.DB().First(&user, userID).Error; err != nil {
		return fmt.Errorf("获取用户信息失败: %w", err)
	}

	if user.PackageCount < count {
		return fmt.Errorf("包次数不足，当前剩余%d次，需要%d次", user.PackageCount, count)
	}

	// 扣除包次数
	return b.ds.DB().Model(&model.User{}).Where("id = ?", userID).
		UpdateColumn("package_count", gorm.Expr("package_count - ?", count)).Error
}

// UpdateMonthlyBookCount 更新用户月度卡册计数
func (b *userBiz) UpdateMonthlyBookCount(ctx context.Context, userID uint, count int) error {
	return b.ds.DB().Model(&model.User{}).Where("id = ?", userID).
		UpdateColumn("monthly_book_count", count).Error
}

// IncrementMonthlyBookCount 增加用户月度卡册计数
func (b *userBiz) IncrementMonthlyBookCount(ctx context.Context, userID uint) error {
	return b.ds.DB().Model(&model.User{}).Where("id = ?", userID).
		UpdateColumn("monthly_book_count", gorm.Expr("monthly_book_count + 1")).Error
}

// ResetFreeUserMonthlyBookCount 重置免费用户月度卡册计数
func (b *userBiz) ResetFreeUserMonthlyBookCount(ctx context.Context, userID uint) error {
	now := time.Now()
	return b.ds.DB().Model(&model.User{}).Where("id = ?", userID).
		Updates(map[string]interface{}{
			"free_user_monthly_book_count": 0,
			"free_user_last_reset_date":    now,
		}).Error
}

// IncrementFreeUserMonthlyBookCount 增加免费用户月度卡册计数
func (b *userBiz) IncrementFreeUserMonthlyBookCount(ctx context.Context, userID uint) error {
	return b.ds.DB().Model(&model.User{}).Where("id = ?", userID).
		UpdateColumn("free_user_monthly_book_count", gorm.Expr("free_user_monthly_book_count + 1")).Error
}

// SyncMembershipType 同步会员类型（处理过期自动降级）
func (b *userBiz) SyncMembershipType(ctx context.Context, userID uint, newType string, resetMonthly bool) error {
	updateData := map[string]interface{}{
		"membership_type": newType,
	}

	// 如果需要重置月度计数和订阅相关字段
	if resetMonthly {
		updateData["monthly_book_count"] = 0
		updateData["membership_start_date"] = nil
	}

	return b.ds.DB().Model(&model.User{}).Where("id = ?", userID).
		Updates(updateData).Error
}
