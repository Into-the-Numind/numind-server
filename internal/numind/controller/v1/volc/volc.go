package volc

import (
	"net/http"
	"numind-server/internal/numind/biz/volc"

	"github.com/gin-gonic/gin"
)

type GenerateRequest struct {
	Content     string `json:"content" binding:"required"`
	ContentType string `json:"content_type" binding:"required"` // "summary" or "annotation"
	MaxLength   int    `json:"max_length"`
	Prompt      string `json:"prompt"`
}

type BatchGenerateRequest struct {
	Contents    []string `json:"contents" binding:"required"`
	ContentType string   `json:"content_type" binding:"required"`
	MaxLength   int      `json:"max_length"`
	Prompt      string   `json:"prompt"`
}

func GenerateArticleContentHandler(b volc.VolcBiz) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req GenerateRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		cfg := &volc.OpenAIConfig{
			APIKey:      "60dbceec-5407-45c3-8d29-c26a47b77884",
			APIBase:     "https://ark.cn-beijing.volces.com/api/v3",
			Model:       "deepseek-v3-250324",
			Temperature: 0.5,
			MaxTokens:   2000,
		}
		result, err := b.GenerateArticleContent(c.Request.Context(), req.Content, req.ContentType, req.MaxLength, cfg, req.Prompt)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"result": result})
	}
}

func BatchGenerateArticleContentHandler(b volc.VolcBiz) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req BatchGenerateRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		cfg := &volc.OpenAIConfig{
			APIKey:      "60dbceec-5407-45c3-8d29-c26a47b77884",
			APIBase:     "https://ark.cn-beijing.volces.com/api/v3",
			Model:       "deepseek-v3-250324",
			Temperature: 0.5,
			MaxTokens:   2000,
		}
		results := make([]string, 0, len(req.Contents))
		for _, content := range req.Contents {
			result, err := b.GenerateArticleContent(c.Request.Context(), content, req.ContentType, req.MaxLength, cfg, req.Prompt)
			if err != nil {
				results = append(results, "ERROR: "+err.Error())
			} else {
				results = append(results, result)
			}
		}
		c.JSON(http.StatusOK, gin.H{"results": results})
	}
}
