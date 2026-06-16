package v1

import "numind-server/internal/pkg/model"

// LoginRequest 指定了 `POST /login` 接口的请求参数.
type LoginRequest struct {
	Username string `json:"username" valid:"alphanum,required,stringlength(1|255)"`
	Password string `json:"password" valid:"required,stringlength(6|18)"`
}

// LoginResponse 指定了 `POST /login` 接口的返回参数.
type LoginResponse struct {
	Token string `json:"token"`
}

// ChangePasswordRequest 指定了 `POST /v1/users/{name}/change-password` 接口的请求参数.
type ChangePasswordRequest struct {
	// 旧密码.
	OldPassword string `json:"oldPassword" valid:"required,stringlength(6|18)"`

	// 新密码.
	NewPassword string `json:"newPassword" valid:"required,stringlength(6|18)"`
}

// CreateUserRequest 指定了 `POST /v1/users` 接口的请求参数.
type CreateUserRequest struct {
	Username string `json:"username" valid:"alphanum,required,stringlength(1|255)"`
	Password string `json:"password" valid:"required,stringlength(6|18)"`
	Nickname string `json:"nickname" valid:"required,stringlength(1|255)"`
	Email    string `json:"email" valid:"required,email"`
	Phone    string `json:"phone" valid:"required,stringlength(11|11)"`
}

// GetUserResponse 指定了 `GET /v1/users/{name}` 接口的返回参数.
type GetUserResponse UserInfo

// UserInfo 指定了用户的详细信息.
type UserInfo struct {
	Username  string `json:"username"`
	Nickname  string `json:"nickname"`
	Email     string `json:"email"`
	Phone     string `json:"phone"`
	PostCount int64  `json:"postCount"`
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
}

// ListUserRequest 指定了 `GET /v1/users` 接口的请求参数.
type ListUserRequest struct {
	Offset int `form:"offset"`
	Limit  int `form:"limit"`
}

// ListUserResponse 指定了 `GET /v1/users` 接口的返回参数.
type ListUserResponse struct {
	TotalCount int64       `json:"totalCount"`
	Users      []*UserInfo `json:"users"`
}

// UpdateUserRequest 指定了 `PUT /v1/users/{name}` 接口的请求参数.
type UpdateUserRequest struct {
	Nickname *string `json:"nickname" valid:"stringlength(1|255)"`
	Email    *string `json:"email" valid:"email"`
	Phone    *string `json:"phone" valid:"stringlength(11|11)"`
}

// UpdateUserProfileRequest 指定了修改用户个人信息的请求参数
type UpdateUserProfileRequest struct {
	// Nickname 昵称：nil=不修改。上限 10 字符（产品约束，与前端 maxlength 一致）；
	// govalidator stringlength 按 rune 计数，故 10 = 10 个汉字/字符。DB 列 size:100 为安全上限不变。
	Nickname  *string `json:"nickname" valid:"stringlength(1|10)"`
	AvatarURL *string `json:"avatar_url" valid:"stringlength(1|512)"`
	// CompanyName 机构品牌名（org-branding）：nil=不修改，传空串=清空回兜底"有数AI"。
	// 仅父账户可写（biz 层守卫），子账户传入会被静默忽略。
	CompanyName *string `json:"company_name" valid:"stringlength(0|100)"`
}

// WebLoginRequest Web端用户名密码登录请求
type WebLoginRequest struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
}

// WebLoginResponse Web端登录响应
type WebLoginResponse struct {
	AccessToken string      `json:"access_token"`
	TokenType   string      `json:"token_type"`
	User        *model.User `json:"user"`
}
