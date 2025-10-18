package v1

import "numind-server/internal/pkg/model"

type ArticleUpdateRequest struct {
	Title       *string `json:"title"`
	AccountName *string `json:"account_name"`
	CategoryID  *uint   `json:"category_id"`
	Summary     *string `json:"summary"`
	ContentTxt  *string `json:"content_txt"`
}

type ArticleListRequest struct {
	Page       int    `form:"page" binding:"min=1"`
	Limit      int    `form:"limit" binding:"min=1,max=100"`
	CategoryID *uint  `form:"category_id"`
	Keyword    string `form:"keyword"`
	UserID     *uint  `form:"user_id"`
}

type ArticleListResponse struct {
	Items []model.ArticleM `json:"items"`
	Total int64            `json:"total"`
	Page  int              `json:"page"`
	Limit int              `json:"limit"`
	Pages int              `json:"pages"`
}
