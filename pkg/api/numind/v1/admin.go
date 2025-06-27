package v1

import "numind-server/internal/pkg/model"

type AdminArticleListRequest struct {
	Page       int    `form:"page" binding:"min=1"`
	Limit      int    `form:"limit" binding:"min=1,max=100"`
	CategoryID *uint  `form:"category_id"`
	Keyword    string `form:"keyword"`
	UserID     *uint  `form:"user_id"`
}

type AdminArticleListResponse struct {
	Items []model.ArticleM `json:"items"`
	Total int64            `json:"total"`
	Page  int              `json:"page"`
	Limit int              `json:"limit"`
	Pages int              `json:"pages"`
}

type AdminArticleCreateRequest struct {
	Title       string `json:"title" binding:"required"`
	CategoryID  *uint  `json:"category_id"`
	Summary     string `json:"summary"`
	Content     string `json:"content"`
	ContentTxt  string `json:"content_txt"`
	AccountName string `json:"account_name"`
	Status      int    `json:"status"`
}

type AdminArticleUpdateRequest struct {
	Title       *string `json:"title"`
	CategoryID  *uint   `json:"category_id"`
	Summary     *string `json:"summary"`
	Content     *string `json:"content"`
	ContentTxt  *string `json:"content_txt"`
	AccountName *string `json:"account_name"`
	Status      *int    `json:"status"`
}

type AdminUserUpdateRequest struct {
	Username *string `json:"username"`
	Nickname *string `json:"nickname"`
	Phone    *string `json:"phone"`
	Password *string `json:"password"`
	Status   *int    `json:"status"`
}

type AdminCategoryCreateRequest struct {
	Name        string `json:"name" binding:"required"`
	Description string `json:"description"`
}

type AdminCategoryUpdateRequest struct {
	Name        *string `json:"name"`
	Description *string `json:"description"`
}

type AdminProxyCreateRequest struct {
	IPAddress string `json:"ip_address" binding:"required"`
	Port      int    `json:"port" binding:"required"`
	Protocol  string `json:"protocol"`
	Username  string `json:"username"`
	Password  string `json:"password"`
	Location  string `json:"location"`
	Remarks   string `json:"remarks"`
}

type AdminProxyUpdateRequest struct {
	IPAddress *string `json:"ip_address"`
	Port      *int    `json:"port"`
	Protocol  *string `json:"protocol"`
	Username  string  `json:"username"`
	Password  string  `json:"password"`
	Location  string  `json:"location"`
	Status    *int    `json:"status"`
	Remarks   string  `json:"remarks"`
}

type AdminFeedbackUpdateRequest struct {
	Status *int   `json:"status"`
	Reply  string `json:"reply"`
}

type AdminAboutUsUpdateRequest struct {
	Content string `json:"content" binding:"required"`
}

type AdminAgreementUpdateRequest struct {
	Content string `json:"content" binding:"required"`
}

type AdminStatsResponse struct {
	TotalUsers      int64 `json:"total_users"`
	TotalArticles   int64 `json:"total_articles"`
	TotalCategories int64 `json:"total_categories"`
	TotalProxies    int64 `json:"total_proxies"`
	TotalFeedbacks  int64 `json:"total_feedbacks"`
}
