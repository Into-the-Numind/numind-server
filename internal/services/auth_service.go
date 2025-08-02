package services

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"numind-server/internal/pkg/model"
	"time"

	"numind-server/configs/config"

	"github.com/golang-jwt/jwt/v5"
	"github.com/spf13/viper"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

type AuthService struct {
	db  *gorm.DB
	cfg *config.Config
}

type WechatLoginRequest struct {
	Code      string `json:"code" binding:"required"`
	PhoneCode string `json:"phone_code"`
}

type WechatLoginResponse struct {
	AccessToken string      `json:"access_token"`
	TokenType   string      `json:"token_type"`
	User        *model.User `json:"user"`
}

type WechatTokenResponse struct {
	AccessToken  string `json:"access_token"`
	ExpiresIn    int    `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
	OpenID       string `json:"openid"`
	Scope        string `json:"scope"`
	UnionID      string `json:"unionid"`
}

type WechatPhoneResponse struct {
	PhoneInfo struct {
		PhoneNumber     string `json:"phoneNumber"`
		PurePhoneNumber string `json:"purePhoneNumber"`
		CountryCode     string `json:"countryCode"`
	} `json:"phone_info"`
}

type AdminLoginRequest struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
}

type ChangePasswordRequest struct {
	OldPassword string `json:"old_password" binding:"required"`
	NewPassword string `json:"new_password" binding:"required"`
}

func NewAuthService(db *gorm.DB) *AuthService {
	return &AuthService{
		db: db,
	}
}

// WechatLogin 微信登录
func (s *AuthService) WechatLogin(req *WechatLoginRequest) (*WechatLoginResponse, error) {
	// 获取微信access_token
	tokenResp, err := s.getWechatToken(req.Code)
	if err != nil {
		return nil, fmt.Errorf("获取微信token失败: %v", err)
	}

	// 查找或创建用户
	user, err := s.findOrCreateUser(tokenResp.OpenID)
	if err != nil {
		return nil, fmt.Errorf("用户处理失败: %v", err)
	}

	// 如果有phone_code，获取手机号
	if req.PhoneCode != "" {
		phoneResp, err := s.getWechatPhone(req.PhoneCode, tokenResp.AccessToken)
		if err == nil && phoneResp.PhoneInfo.PurePhoneNumber != "" {
			user.Phone = phoneResp.PhoneInfo.PurePhoneNumber
			s.db.Save(user)
		}
	}

	// 生成JWT token
	token, err := s.generateToken(user)
	if err != nil {
		return nil, fmt.Errorf("生成token失败: %v", err)
	}

	return &WechatLoginResponse{
		AccessToken: token,
		TokenType:   "Bearer",
		User:        user,
	}, nil
}

// AdminLogin 管理员登录
func (s *AuthService) AdminLogin(req *AdminLoginRequest) (*WechatLoginResponse, error) {
	var user model.User
	if err := s.db.Where("username = ?", req.Username).First(&user).Error; err != nil {
		return nil, fmt.Errorf("用户不存在")
	}

	if !user.IsAdmin {
		return nil, fmt.Errorf("非管理员用户")
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(req.Password)); err != nil {
		return nil, fmt.Errorf("密码错误")
	}

	// 更新最后登录时间
	now := time.Now()
	user.LastLogin = &now
	s.db.Save(&user)

	// 生成JWT token
	token, err := s.generateToken(&user)
	if err != nil {
		return nil, fmt.Errorf("生成token失败: %v", err)
	}

	return &WechatLoginResponse{
		AccessToken: token,
		TokenType:   "Bearer",
		User:        &user,
	}, nil
}

// ValidateToken 验证token
func (s *AuthService) ValidateToken(tokenString string) (*model.User, error) {
	token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		fmt.Println("jwt.secret:", viper.GetString("jwt.secret"))
		return []byte(viper.GetString("jwt.secret")), nil
	})

	if err != nil {
		return nil, err
	}

	if claims, ok := token.Claims.(jwt.MapClaims); ok && token.Valid {
		userID := uint(claims["user_id"].(float64))

		var user model.User
		if err := s.db.First(&user, userID).Error; err != nil {
			return nil, err
		}

		return &user, nil
	}

	return nil, fmt.Errorf("invalid token")
}

// ChangePassword 修改密码
func (s *AuthService) ChangePassword(userID uint, req *ChangePasswordRequest) error {
	var user model.User
	if err := s.db.First(&user, userID).Error; err != nil {
		return fmt.Errorf("用户不存在")
	}

	// 验证旧密码
	if err := bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(req.OldPassword)); err != nil {
		return fmt.Errorf("旧密码错误")
	}

	// 加密新密码
	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(req.NewPassword), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("密码加密失败")
	}

	user.Password = string(hashedPassword)
	return s.db.Save(&user).Error
}

// getWechatToken 获取微信access_token
func (s *AuthService) getWechatToken(code string) (*WechatTokenResponse, error) {
	url := fmt.Sprintf("https://api.weixin.qq.com/sns/oauth2/access_token?appid=%s&secret=%s&code=%s&grant_type=authorization_code",
		viper.GetString("wechat.app_id"),
		viper.GetString("wechat.app_secret"),
		code)

	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var tokenResp WechatTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return nil, err
	}

	if tokenResp.AccessToken == "" {
		return nil, fmt.Errorf("获取微信token失败")
	}

	return &tokenResp, nil
}

// getWechatPhone 获取微信手机号
func (s *AuthService) getWechatPhone(phoneCode, accessToken string) (*WechatPhoneResponse, error) {
	url := fmt.Sprintf("https://api.weixin.qq.com/wxa/business/getuserphonenumber?access_token=%s", accessToken)

	reqBody := map[string]string{"code": phoneCode}
	jsonBody, _ := json.Marshal(reqBody)

	resp, err := http.Post(url, "application/json", bytes.NewBuffer(jsonBody))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var phoneResp WechatPhoneResponse
	if err := json.NewDecoder(resp.Body).Decode(&phoneResp); err != nil {
		return nil, err
	}

	return &phoneResp, nil
}

// findOrCreateUser 查找或创建用户
func (s *AuthService) findOrCreateUser(openID string) (*model.User, error) {
	var user model.User
	err := s.db.Where("open_id = ?", openID).First(&user).Error

	if err == gorm.ErrRecordNotFound {
		// 创建新用户
		user = model.User{
			OpenID: openID,
		}

		if err := s.db.Create(&user).Error; err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}

	return &user, nil
}

// generateToken 生成JWT token
func (s *AuthService) generateToken(user *model.User) (string, error) {
	claims := jwt.MapClaims{
		"user_id": user.ID,
		"openid":  user.OpenID,
		"exp":     time.Now().Add(time.Duration(viper.GetInt64("jwt.expire-hours")) * time.Hour).Unix(),
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(viper.GetString("jwt.secret")))
}
