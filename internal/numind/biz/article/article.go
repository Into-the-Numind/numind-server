package article

import (
	"context"
	"encoding/json"
	"fmt"
	"numind-server/internal/pkg/model"
	"numind-server/internal/numind/store"
	"time"
)

type IArticleBiz interface {
	FetchArticle(ctx context.Context, userID uint, url string) (*model.ArticleM, error)
	GetArticles(ctx context.Context, req *store.ArticleListRequest) ([]model.ArticleM, int64, error)
	GetArticle(ctx context.Context, id uint) (*model.ArticleM, error)
	UpdateArticleCategory(ctx context.Context, id, userID uint, categoryID *uint) error
	DeleteArticle(ctx context.Context, id, userID uint) error
	AddFavorite(ctx context.Context, userID, articleID uint) error
	RemoveFavorite(ctx context.Context, userID, articleID uint) error
	GetFavorites(ctx context.Context, userID uint, page, limit int) ([]model.ArticleM, int64, error)
	ParaphraseText(ctx context.Context, text string) (string, error)
}

type ArticleBiz struct {
	store store.IArticleStore
}

func NewArticleBiz(store store.IArticleStore) IArticleBiz {
	return &ArticleBiz{store: store}
}

func (b *ArticleBiz) FetchArticle(ctx context.Context, userID uint, url string) (*model.ArticleM, error) {
	// 检查文章是否已存在
	existingArticle, err := b.store.GetArticleByURL(url)
	if err == nil {
		// 文章已存在，返回现有文章
		return existingArticle, nil
	}

	// 获取文章内容（这里需要实现网页抓取逻辑）
	articleData, err := b.scrapeArticle(url)
	if err != nil {
		return nil, fmt.Errorf("获取文章内容失败: %v", err)
	}

	// 将Content转换为JSON
	contentJSON, err := json.Marshal(articleData.Content)
	if err != nil {
		return nil, fmt.Errorf("序列化内容失败: %v", err)
	}

	// 创建新文章
	article := &model.ArticleM{
		UserID:      userID,
		URL:         url,
		Title:       articleData.Title,
		AccountName: articleData.AccountName,
		PublishTime: articleData.PublishTime,
		Content:     model.JSON(contentJSON),
		ContentTxt:  articleData.ContentTxt,
		CreatedAt:   time.Now(),
		CategoryAt:  time.Now(),
	}

	if err := b.store.CreateArticle(article); err != nil {
		return nil, fmt.Errorf("保存文章失败: %v", err)
	}

	return article, nil
}

func (b *ArticleBiz) GetArticles(ctx context.Context, req *store.ArticleListRequest) ([]model.ArticleM, int64, error) {
	return b.store.GetArticles(req)
}

func (b *ArticleBiz) GetArticle(ctx context.Context, id uint) (*model.ArticleM, error) {
	return b.store.GetArticleByID(id)
}

func (b *ArticleBiz) UpdateArticleCategory(ctx context.Context, id, userID uint, categoryID *uint) error {
	return b.store.UpdateArticleCategory(id, userID, categoryID)
}

func (b *ArticleBiz) DeleteArticle(ctx context.Context, id, userID uint) error {
	// 检查文章是否存在且属于该用户
	article, err := b.store.GetArticleByID(id)
	if err != nil {
		return err
	}
	if article.UserID != userID {
		return fmt.Errorf("文章不存在或无权限")
	}
	return b.store.DeleteArticle(id)
}

func (b *ArticleBiz) AddFavorite(ctx context.Context, userID, articleID uint) error {
	return b.store.AddFavorite(userID, articleID)
}

func (b *ArticleBiz) RemoveFavorite(ctx context.Context, userID, articleID uint) error {
	return b.store.RemoveFavorite(userID, articleID)
}

func (b *ArticleBiz) GetFavorites(ctx context.Context, userID uint, page, limit int) ([]model.ArticleM, int64, error) {
	return b.store.GetFavorites(userID, page, limit)
}

func (b *ArticleBiz) ParaphraseText(ctx context.Context, text string) (string, error) {
	// 这里需要调用AI服务进行文本释义
	// 暂时返回原文
	return text, nil
}

// scrapeArticle 抓取文章内容（简化实现）
func (b *ArticleBiz) scrapeArticle(url string) (*ArticleCreateRequest, error) {
	// 这里应该实现实际的网页抓取逻辑
	// 暂时返回模拟数据
	return &ArticleCreateRequest{
		URL:         url,
		Title:       "示例文章标题",
		AccountName: "示例公众号",
		PublishTime: time.Now().Format("2006-01-02 15:04:05"),
		Content: []map[string]interface{}{
			{"type": "text", "content": "示例文章内容"},
		},
		ContentTxt: "示例文章纯文本内容",
	}, nil
}

type ArticleCreateRequest struct {
	URL         string                   `json:"url" binding:"required"`
	Title       string                   `json:"title" binding:"required"`
	AccountName string                   `json:"account_name" binding:"required"`
	PublishTime string                   `json:"publish_time" binding:"required"`
	Content     []map[string]interface{} `json:"content" binding:"required"`
	RawHTML     string                   `json:"raw_html"`
	CategoryID  *uint                    `json:"category_id"`
	ContentTxt  string                   `json:"content_txt"`
}
