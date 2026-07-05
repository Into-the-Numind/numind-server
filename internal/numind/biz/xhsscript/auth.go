package xhsscript

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/spf13/viper"
	"gorm.io/gorm"

	"numind-server/internal/pkg/errno"
	importMw "numind-server/internal/pkg/middleware"
	"numind-server/internal/pkg/model"
)

func (s *Service) Authenticate(ctx context.Context, token string, allowExtToken bool) (*model.User, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, errno.ErrTokenInvalid.SetMessage("未提供认证令牌")
	}
	user, err := importMw.ValidateToken(ctx, token)
	if err != nil {
		return nil, errno.ErrTokenInvalid.SetMessage("认证已失效，请重新进入页面")
	}
	scope := tokenScope(token)
	if scope == "" {
		return user, nil
	}
	if allowExtToken && scope == ExtTokenScope {
		return user, nil
	}
	return nil, errno.ErrForbidden.SetMessage("该令牌不能访问此接口")
}

func (s *Service) EnsureTrial(ctx context.Context, anonymousID string) (*SessionDTO, error) {
	anonymousID = strings.TrimSpace(anonymousID)
	if anonymousID == "" {
		anonymousID = randomAnonymousID()
	}
	username := anonymousUsername(anonymousID)
	var user model.User
	err := s.ds.DB().WithContext(ctx).Where("username = ?", username).First(&user).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		user = model.User{
			Username: username,
			Nickname: "口播试用用户",
			Password: "",
			Status:   1,
		}
		if createErr := s.ds.DB().WithContext(ctx).Create(&user).Error; createErr != nil {
			if reloadErr := s.ds.DB().WithContext(ctx).Where("username = ?", username).First(&user).Error; reloadErr != nil {
				return nil, fmt.Errorf("create anonymous user: %w", createErr)
			}
		}
	} else if err != nil {
		return nil, fmt.Errorf("load anonymous user: %w", err)
	}
	return s.sessionForUser(ctx, &user, "")
}

func (s *Service) Register(ctx context.Context, current *model.User, username, password string) (*SessionDTO, error) {
	username = strings.TrimSpace(username)
	password = strings.TrimSpace(password)
	if len(username) < 3 || len(username) > 50 {
		return nil, errno.ErrInvalidParameter.SetMessage("账号需要 3-50 个字符")
	}
	if len(password) < 6 || len(password) > 72 {
		return nil, errno.ErrInvalidParameter.SetMessage("密码需要至少 6 个字符")
	}

	var user model.User
	err := s.ds.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing model.User
		existingErr := tx.Where("username = ?", username).First(&existing).Error
		if existingErr == nil && (current == nil || existing.ID != current.ID) {
			return errno.ErrInvalidParameter.SetMessage("这个账号已经被注册")
		}
		if existingErr != nil && !errors.Is(existingErr, gorm.ErrRecordNotFound) {
			return existingErr
		}

		if current != nil && current.ID > 0 && IsAnonymousUsername(current.Username) {
			updates := map[string]interface{}{
				"username": username,
				"password": password,
				"nickname": username,
				"status":   1,
			}
			if err := tx.Model(&model.User{}).Where("id = ?", current.ID).Updates(updates).Error; err != nil {
				return err
			}
			return tx.Where("id = ?", current.ID).First(&user).Error
		}

		if existingErr == nil && current != nil && existing.ID == current.ID {
			if err := tx.Model(&model.User{}).Where("id = ?", existing.ID).Updates(map[string]interface{}{
				"password": password,
				"nickname": username,
				"status":   1,
			}).Error; err != nil {
				return err
			}
			user = existing
			user.Password = password
			user.Nickname = username
			return nil
		}

		user = model.User{
			Username: username,
			Password: password,
			Nickname: username,
			Status:   1,
		}
		return tx.Create(&user).Error
	})
	if err != nil {
		return nil, err
	}
	return s.sessionForUser(ctx, &user, "")
}

func (s *Service) Login(ctx context.Context, username, password string) (*SessionDTO, error) {
	username = strings.TrimSpace(username)
	password = strings.TrimSpace(password)
	var user model.User
	if err := s.ds.DB().WithContext(ctx).Where("username = ?", username).First(&user).Error; err != nil {
		return nil, errno.ErrTokenInvalid.SetMessage("账号或密码不正确")
	}
	if user.Password != password || IsAnonymousUsername(user.Username) {
		return nil, errno.ErrTokenInvalid.SetMessage("账号或密码不正确")
	}
	now := time.Now()
	_ = s.ds.DB().WithContext(ctx).Model(&model.User{}).Where("id = ?", user.ID).Update("last_login", &now).Error
	user.LastLogin = &now
	return s.sessionForUser(ctx, &user, "")
}

func (s *Service) IssueExtToken(ctx context.Context, userID uint) (string, time.Time, error) {
	return signToken(userID, ExtTokenScope, 30*24*time.Hour)
}

func (s *Service) sessionForUser(ctx context.Context, user *model.User, existingToken string) (*SessionDTO, error) {
	token := existingToken
	expiresAt := time.Now().Add(30 * 24 * time.Hour)
	if token == "" {
		var err error
		token, expiresAt, err = signToken(user.ID, "", 30*24*time.Hour)
		if err != nil {
			return nil, err
		}
	}
	account, err := s.ds.XhsScript().CreateOrGetQuotaAccount(ctx, user.ID)
	if err != nil {
		return nil, err
	}
	quota, err := s.quotaDTO(ctx, account)
	if err != nil {
		return nil, err
	}
	return &SessionDTO{
		AccessToken: token,
		ExpiresAt:   expiresAt,
		User:        userDTO(user),
		Quota:       quota,
	}, nil
}

func signToken(userID uint, scope string, ttl time.Duration) (string, time.Time, error) {
	expiresAt := time.Now().Add(ttl)
	claims := jwt.MapClaims{
		"user_id": userID,
		"exp":     expiresAt.Unix(),
	}
	if scope != "" {
		claims["scope"] = scope
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	secret := strings.TrimSpace(viper.GetString("jwt.secret"))
	if secret == "" {
		return "", time.Time{}, errno.ErrSignToken.SetMessage("JWT 密钥未配置")
	}
	signed, err := token.SignedString([]byte(secret))
	if err != nil {
		return "", time.Time{}, errno.ErrSignToken
	}
	return signed, expiresAt, nil
}

func tokenScope(tokenString string) string {
	parsed, _, err := jwt.NewParser().ParseUnverified(tokenString, jwt.MapClaims{})
	if err != nil {
		return ""
	}
	claims, ok := parsed.Claims.(jwt.MapClaims)
	if !ok {
		return ""
	}
	scope, _ := claims["scope"].(string)
	return scope
}

func anonymousUsername(anonymousID string) string {
	sum := sha256.Sum256([]byte(anonymousID))
	return AnonymousPrefix + hex.EncodeToString(sum[:8])
}

func randomAnonymousID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}
