package xhsscript

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/spf13/viper"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"numind-server/internal/pkg/errno"
	importMw "numind-server/internal/pkg/middleware"
	"numind-server/internal/pkg/model"
	passwordauth "numind-server/pkg/auth"
)

type TrialClaimInput struct {
	Type  string
	Value string
}

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
	return nil, errno.ErrInvalidParameter.SetMessage("请先注册账号，再领取 3 次免费生成")
}

func (s *Service) Register(ctx context.Context, current *model.User, username, password string, trialClaims ...TrialClaimInput) (*SessionDTO, error) {
	username = normalizeAccountUsername(username)
	password = strings.TrimSpace(password)
	if !validAccountUsername(username) {
		return nil, errno.ErrInvalidParameter.SetMessage("账号只能使用 3-20 位英文或数字")
	}
	if len(password) < 6 || len(password) > 20 {
		return nil, errno.ErrInvalidParameter.SetMessage("密码需要 6-20 个字符")
	}
	hashedPassword, err := passwordauth.Encrypt(password)
	if err != nil {
		return nil, fmt.Errorf("encrypt password: %w", err)
	}

	var user model.User
	err = s.ds.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if current != nil && IsAnonymousUsername(current.Username) {
			current = nil
		}
		var existing model.User
		existingErr := tx.Where("username = ?", username).First(&existing).Error
		if existingErr == nil && (current == nil || existing.ID != current.ID) {
			return errno.ErrInvalidParameter.SetMessage("这个账号已经被注册")
		}
		if existingErr != nil && !errors.Is(existingErr, gorm.ErrRecordNotFound) {
			return existingErr
		}

		if existingErr == nil && current != nil && existing.ID == current.ID {
			if err := tx.Model(&model.User{}).Where("id = ?", existing.ID).Updates(map[string]interface{}{
				"password": hashedPassword,
				"nickname": username,
				"status":   1,
			}).Error; err != nil {
				return err
			}
			user = existing
			user.Password = hashedPassword
			user.Nickname = username
			return nil
		}

		user = model.User{
			Username: username,
			Password: hashedPassword,
			Nickname: username,
			Status:   1,
		}
		if err := tx.Create(&user).Error; err != nil {
			return err
		}
		freeGrant, err := s.claimTrialGrant(ctx, tx, user.ID, trialClaims)
		if err != nil {
			return err
		}
		account := model.XhsScriptQuotaAccount{
			UserID:        user.ID,
			FreeRemaining: freeGrant,
			PaidRemaining: 0,
		}
		if err := tx.WithContext(ctx).
			Select("UserID", "FreeRemaining", "PaidRemaining").
			Clauses(clause.OnConflict{DoNothing: true}).
			Create(&account).Error; err != nil {
			return fmt.Errorf("create xhs script quota account: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s.sessionForUser(ctx, &user, "")
}

func (s *Service) claimTrialGrant(ctx context.Context, tx *gorm.DB, userID uint, inputs []TrialClaimInput) (int64, error) {
	claims := normalizeTrialClaimRows(userID, inputs)
	if len(claims) == 0 {
		return TrialFreeGenerations, nil
	}
	result := tx.WithContext(ctx).
		Clauses(clause.OnConflict{DoNothing: true}).
		Create(&claims)
	if result.Error != nil {
		return 0, fmt.Errorf("claim trial grant: %w", result.Error)
	}
	if result.RowsAffected != int64(len(claims)) {
		return 0, nil
	}
	return TrialFreeGenerations, nil
}

func (s *Service) Login(ctx context.Context, username, password string) (*SessionDTO, error) {
	username = normalizeAccountUsername(username)
	password = strings.TrimSpace(password)
	var user model.User
	if err := s.ds.DB().WithContext(ctx).Where("username = ?", username).First(&user).Error; err != nil {
		return nil, errno.ErrTokenInvalid.SetMessage("账号或密码不正确")
	}
	if IsAnonymousUsername(user.Username) || !s.passwordMatches(ctx, &user, password) {
		return nil, errno.ErrTokenInvalid.SetMessage("账号或密码不正确")
	}
	now := time.Now()
	_ = s.ds.DB().WithContext(ctx).Model(&model.User{}).Where("id = ?", user.ID).Update("last_login", &now).Error
	user.LastLogin = &now
	return s.sessionForUser(ctx, &user, "")
}

func (s *Service) passwordMatches(ctx context.Context, user *model.User, password string) bool {
	if user == nil || strings.TrimSpace(user.Password) == "" {
		return false
	}
	if err := passwordauth.Compare(user.Password, password); err == nil {
		return true
	}
	if user.Password != password {
		return false
	}
	hashed, err := passwordauth.Encrypt(password)
	if err != nil {
		return false
	}
	if err := s.ds.DB().WithContext(ctx).Model(&model.User{}).Where("id = ?", user.ID).Update("password", hashed).Error; err != nil {
		return false
	}
	user.Password = hashed
	return true
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

func normalizeTrialClaimRows(userID uint, inputs []TrialClaimInput) []model.XhsScriptTrialClaim {
	claims := make([]model.XhsScriptTrialClaim, 0, len(inputs))
	seen := map[string]struct{}{}
	for _, input := range inputs {
		claimType := normalizeTrialClaimType(input.Type)
		value := strings.TrimSpace(input.Value)
		if claimType == "" || value == "" {
			continue
		}
		sum := sha256.Sum256([]byte(claimType + ":" + value))
		valueHash := hex.EncodeToString(sum[:])
		claimKey := claimType + ":" + valueHash
		if _, ok := seen[claimKey]; ok {
			continue
		}
		seen[claimKey] = struct{}{}
		claims = append(claims, model.XhsScriptTrialClaim{
			UserID:         userID,
			ClaimKey:       claimKey,
			ClaimType:      claimType,
			ClaimValueHash: valueHash,
		})
	}
	return claims
}

func normalizeTrialClaimType(claimType string) string {
	switch strings.ToLower(strings.TrimSpace(claimType)) {
	case "visitor", "browser", "anonymous_id":
		return "visitor"
	case "ip", "client_ip":
		return "ip"
	default:
		return ""
	}
}

func normalizeAccountUsername(username string) string {
	return strings.ToLower(strings.TrimSpace(username))
}

func validAccountUsername(username string) bool {
	if len(username) < 3 || len(username) > 20 {
		return false
	}
	for _, r := range username {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			continue
		}
		return false
	}
	return true
}
