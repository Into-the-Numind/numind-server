package admin

import (
	"context"
	"fmt"
	"numind-server/internal/pkg/model"
	"numind-server/internal/numind/store"
	"time"
)

type IAdminBiz interface {
	GetArticles(ctx context.Context, req *store.AdminArticleListRequest) ([]model.ArticleM, int64, error)
	GetArticle(ctx context.Context, id uint) (*model.ArticleM, error)
	CreateArticle(ctx context.Context, req *AdminArticleCreateRequest) (*model.ArticleM, error)
	UpdateArticle(ctx context.Context, id uint, req *AdminArticleUpdateRequest) error
	DeleteArticle(ctx context.Context, id uint) error
	BulkDeleteArticles(ctx context.Context, ids []uint) error
	GetUsers(ctx context.Context, page, limit int) ([]model.User, int64, error)
	UpdateUser(ctx context.Context, id uint, req *AdminUserUpdateRequest) error
	DeleteUser(ctx context.Context, id uint) error
	GetCategories(ctx context.Context) ([]model.CategoryM, error)
	CreateCategory(ctx context.Context, req *AdminCategoryCreateRequest) (*model.CategoryM, error)
	UpdateCategory(ctx context.Context, id uint, req *AdminCategoryUpdateRequest) error
	DeleteCategory(ctx context.Context, id uint) error
	GetStats(ctx context.Context) (*store.AdminStats, error)
}

type AdminBiz struct {
	store store.IAdminStore
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

func NewAdminBiz(store store.IAdminStore) IAdminBiz {
	return &AdminBiz{store: store}
}

func (b *AdminBiz) GetArticles(ctx context.Context, req *store.AdminArticleListRequest) ([]model.ArticleM, int64, error) {
	return b.store.GetArticles(req)
}

func (b *AdminBiz) GetArticle(ctx context.Context, id uint) (*model.ArticleM, error) {
	return b.store.GetArticle(id)
}

func (b *AdminBiz) CreateArticle(ctx context.Context, req *AdminArticleCreateRequest) (*model.ArticleM, error) {
	article := &model.ArticleM{
		Title:       req.Title,
		CategoryID:  req.CategoryID,
		Summary:     req.Summary,
		ContentTxt:  req.ContentTxt,
		AccountName: req.AccountName,
		CreatedAt:   time.Now(),
		CategoryAt:  time.Now(),
	}

	if err := b.store.CreateArticle(article); err != nil {
		return nil, err
	}

	return article, nil
}

func (b *AdminBiz) UpdateArticle(ctx context.Context, id uint, req *AdminArticleUpdateRequest) error {
	updates := make(map[string]interface{})
	
	if req.Title != nil {
		updates["title"] = *req.Title
	}
	if req.CategoryID != nil {
		updates["category_id"] = req.CategoryID
	}
	if req.Summary != nil {
		updates["summary"] = *req.Summary
	}
	if req.ContentTxt != nil {
		updates["content_txt"] = *req.ContentTxt
	}
	if req.AccountName != nil {
		updates["account_name"] = *req.AccountName
	}
	if req.Status != nil {
		updates["status"] = *req.Status
	}

	if len(updates) == 0 {
		return fmt.Errorf("没有需要更新的字段")
	}

	return b.store.UpdateArticle(id, updates)
}

func (b *AdminBiz) DeleteArticle(ctx context.Context, id uint) error {
	return b.store.DeleteArticle(id)
}

func (b *AdminBiz) BulkDeleteArticles(ctx context.Context, ids []uint) error {
	return b.store.BulkDeleteArticles(ids)
}

func (b *AdminBiz) GetUsers(ctx context.Context, page, limit int) ([]model.User, int64, error) {
	return b.store.GetUsers(page, limit)
}

func (b *AdminBiz) UpdateUser(ctx context.Context, id uint, req *AdminUserUpdateRequest) error {
	updates := make(map[string]interface{})
	
	if req.Username != nil {
		updates["username"] = *req.Username
	}
	if req.Nickname != nil {
		updates["nickname"] = *req.Nickname
	}
	if req.Phone != nil {
		updates["phone"] = *req.Phone
	}
	if req.Status != nil {
		updates["status"] = *req.Status
	}

	if len(updates) == 0 {
		return fmt.Errorf("没有需要更新的字段")
	}

	return b.store.UpdateUser(id, updates)
}

func (b *AdminBiz) DeleteUser(ctx context.Context, id uint) error {
	return b.store.DeleteUser(id)
}

func (b *AdminBiz) GetCategories(ctx context.Context) ([]model.CategoryM, error) {
	return b.store.GetCategories()
}

func (b *AdminBiz) CreateCategory(ctx context.Context, req *AdminCategoryCreateRequest) (*model.CategoryM, error) {
	category := &model.CategoryM{
		Name: req.Name,
	}

	if err := b.store.CreateCategory(category); err != nil {
		return nil, err
	}

	return category, nil
}

func (b *AdminBiz) UpdateCategory(ctx context.Context, id uint, req *AdminCategoryUpdateRequest) error {
	updates := make(map[string]interface{})
	
	if req.Name != nil {
		updates["name"] = *req.Name
	}

	if len(updates) == 0 {
		return fmt.Errorf("没有需要更新的字段")
	}

	return b.store.UpdateCategory(id, updates)
}

func (b *AdminBiz) DeleteCategory(ctx context.Context, id uint) error {
	return b.store.DeleteCategory(id)
}

func (b *AdminBiz) GetStats(ctx context.Context) (*store.AdminStats, error) {
	return b.store.GetStats()
}
