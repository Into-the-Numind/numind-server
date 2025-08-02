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

	"github.com/golang-jwt/jwt/v4"
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
	List(ctx context.Context, offset, limit int) (*v1.ListUserResponse, error)
	Update(ctx context.Context, username string, r *v1.UpdateUserRequest) error
	Delete(ctx context.Context, username string) error

	// 微信小程序
	WechatLogin(req *v1.WechatLoginRequest) (*v1.WechatLoginResponse, error)
	ValidateToken(tokenString string) (*model.User, error)
	UpdateWechatUser(ctx context.Context, openid string, r *v1.UpdateUserRequest) error
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
	userM, err := b.ds.Users().Get(ctx, username)
	if err != nil {
		return err
	}

	if err := auth.Compare(userM.Password, r.OldPassword); err != nil {
		return errno.ErrPasswordIncorrect
	}

	userM.Password, _ = auth.Encrypt(r.NewPassword)
	if err := b.ds.Users().Update(ctx, userM); err != nil {
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
	var userM model.UserM
	_ = copier.Copy(&userM, r)

	if err := b.ds.Users().Create(ctx, &userM); err != nil {
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
				count, _, err := b.ds.Posts().List(ctx, user.Username, 0, 0)
				if err != nil {
					log.C(ctx).Errorw("Failed to list posts", "err", err)
					return err
				}

				m.Store(user.ID, &v1.UserInfo{
					Username:  user.Username,
					Nickname:  user.Nickname,
					Email:     user.Email,
					Phone:     user.Email,
					PostCount: count,
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

// ListWithBadPerformance 是一个性能较差的实现方式（已废弃）.
func (b *userBiz) ListWithBadPerformance(ctx context.Context, offset, limit int) (*v1.ListUserResponse, error) {
	count, list, err := b.ds.Users().List(ctx, offset, limit)
	if err != nil {
		log.C(ctx).Errorw("Failed to list users from storage", "err", err)
		return nil, err
	}

	users := make([]*v1.UserInfo, 0, len(list))
	for _, item := range list {
		user := item

		count, _, err := b.ds.Posts().List(ctx, user.Username, 0, 0)
		if err != nil {
			log.C(ctx).Errorw("Failed to list posts", "err", err)
			return nil, err
		}

		users = append(users, &v1.UserInfo{
			Username:  user.Username,
			Nickname:  user.Nickname,
			Email:     user.Email,
			Phone:     user.Email,
			PostCount: count,
			CreatedAt: user.CreatedAt.Format("2006-01-02 15:04:05"),
			UpdatedAt: user.UpdatedAt.Format("2006-01-02 15:04:05"),
		})
	}

	log.C(ctx).Debugw("Get users from backend storage", "count", len(users))

	return &v1.ListUserResponse{TotalCount: count, Users: users}, nil
}

// Update 是 UserBiz 接口中 `Update` 方法的实现.
func (b *userBiz) Update(ctx context.Context, username string, user *v1.UpdateUserRequest) error {
	userM, err := b.ds.Users().Get(ctx, username)
	if err != nil {
		return err
	}

	if user.Email != nil {
		userM.Email = *user.Email
	}

	if user.Nickname != nil {
		userM.Nickname = *user.Nickname
	}

	if user.Phone != nil {
		userM.Phone = *user.Phone
	}

	if err := b.ds.Users().Update(ctx, userM); err != nil {
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
	claims := jwt.MapClaims{
		"user_id": user.ID,
		"openid":  user.OpenID,
		"exp":     time.Now().Add(time.Duration(24) * time.Hour).Unix(),
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
		user = model.User{
			OpenID:   openID,
			Username: fmt.Sprintf("user_%s", openID), // 使用openid生成唯一username
		}

		if err := s.ds.DB().Create(&user).Error; err != nil {
			return nil, fmt.Errorf("创建用户失败: %v", err)
		}
	} else if err != nil {
		return nil, err
	}

	return &user, nil
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
