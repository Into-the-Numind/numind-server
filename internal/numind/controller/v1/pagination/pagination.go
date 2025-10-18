package pagination

import (
	"numind-server/internal/numind/biz/pagination"
	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"

	"github.com/gin-gonic/gin"
)

// PaginationController 分页控制器
type PaginationController struct {
	biz pagination.PaginationBiz
}

// NewPaginationController 创建新的分页控制器
func NewPaginationController(biz pagination.PaginationBiz) *PaginationController {
	return &PaginationController{
		biz: biz,
	}
}

// PaginateRequest 分页请求结构
type PaginateRequest struct {
	Elements []pagination.Element `json:"elements" binding:"required"`
}

// PaginateResponse 分页响应结构
type PaginateResponse struct {
	Cards []pagination.Card `json:"cards"`
}

// Paginate 执行分页
func (pc *PaginationController) Paginate(c *gin.Context) {
	var req PaginateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind, nil)
		return
	}

	result, err := pc.biz.PaginateElements(req.Elements)
	if err != nil {
		core.WriteResponse(c, errno.ErrInternalServer, nil)
		return
	}

	response := &PaginateResponse{
		Cards: result.Cards,
	}

	core.WriteResponse(c, nil, response)
}

// PaginateFromJSON 从JSON字符串分页
func (pc *PaginationController) PaginateFromJSON(c *gin.Context) {
	jsonStr := c.PostForm("json")
	if jsonStr == "" {
		core.WriteResponse(c, errno.ErrBind, nil)
		return
	}

	result, err := pc.biz.PaginateFromJSON(jsonStr)
	if err != nil {
		core.WriteResponse(c, errno.ErrInternalServer, nil)
		return
	}

	response := &PaginateResponse{
		Cards: result.Cards,
	}

	core.WriteResponse(c, nil, response)
}

// GetConfig 获取配置
func (pc *PaginationController) GetConfig(c *gin.Context) {
	config := pc.biz.GetConfig()
	core.WriteResponse(c, nil, config)
}

// UpdateConfig 更新配置
func (pc *PaginationController) UpdateConfig(c *gin.Context) {
	var config pagination.PaginationConfig
	if err := c.ShouldBindJSON(&config); err != nil {
		core.WriteResponse(c, errno.ErrBind, nil)
		return
	}

	if err := pc.biz.UpdateConfig(&config); err != nil {
		core.WriteResponse(c, errno.ErrInternalServer, nil)
		return
	}

	core.WriteResponse(c, nil, gin.H{"message": "配置更新成功"})
}

// TestPagination 测试分页功能
func (pc *PaginationController) TestPagination(c *gin.Context) {
	// 使用示例数据进行测试
	elements := []pagination.Element{
		{
			Type:    pagination.ElementTypeTitle,
			Content: "为什么高价值的信息几乎从不流向普通人？",
		},
		{
			Type:    pagination.ElementTypeBody,
			Content: "因为流不动，容易被误解，甚至被拒收。价值越高的东西，越考验人的理解能力。",
		},
		{
			Type: pagination.ElementTypeList,
			Content: []string{
				"《道德经》中的无为被解读成什么都不做。",
				"以德报怨，被误解为被人欺负要用爱心感化。",
			},
		},
	}

	result, err := pc.biz.PaginateElements(elements)
	if err != nil {
		core.WriteResponse(c, errno.ErrInternalServer, nil)
		return
	}

	response := &PaginateResponse{
		Cards: result.Cards,
	}

	core.WriteResponse(c, nil, response)
}

// StyleConfigResponse 样式配置响应结构
type StyleConfigResponse struct {
	Card   pagination.CardConfig                             `json:"card"`
	Styles map[pagination.ElementType]pagination.StyleConfig `json:"styles"`
	Rules  StyleRules                                        `json:"rules"`
}

// StyleRules 样式规则
type StyleRules struct {
	FontSizes    map[string]int     `json:"fontSizes"`
	Colors       map[string]string  `json:"colors"`
	Alignments   []string           `json:"alignments"`
	LineHeights  map[string]float64 `json:"lineHeights"`
	Spacings     map[string]int     `json:"spacings"`
	ElementTypes []string           `json:"elementTypes"`
}

// GetStyleConfig 获取样式配置
func (pc *PaginationController) GetStyleConfig(c *gin.Context) {
	config := pc.biz.GetConfig()

	// 构建样式规则
	rules := StyleRules{
		FontSizes: map[string]int{
			"title":    64, // 标题: 64rpx（最大）
			"subtitle": 48, // 副标题: 48rpx（中等）
			"body":     36, // 正文: 36rpx（标准）
			"quote":    36, // 引用: 36rpx（强调）
			"tag":      28, // 标签: 28rpx（最小）
		},
		Colors: map[string]string{
			"title":    "#333333", // 主标题: #333333（深灰）
			"subtitle": "#666666", // 副标题: #666666（中灰）
			"body":     "#333333", // 正文: #333333（深灰）
			"quote":    "#1E90FF", // 引用: #1E90FF（蓝色）
			"tag":      "#1E90FF", // 标签: #1E90FF（蓝色）
		},
		Alignments: []string{"left", "center", "right"},
		LineHeights: map[string]float64{
			"body":  1.6, // 正文: 1.6（标准行高）
			"quote": 1.5, // 引用: 1.5（紧凑行高）
			"list":  1.5, // 列表: 1.5（紧凑行高）
		},
		Spacings: map[string]int{
			"title_bottom":    30, // 标题下方: 30rpx
			"subtitle_bottom": 25, // 副标题下方: 25rpx
			"body_bottom":     30, // 正文下方: 30rpx
			"image_bottom":    30, // 图片下方: 30rpx
			"list_item":       8,  // 列表项间距: 8rpx
		},
		ElementTypes: []string{"title", "subtitle", "body", "list", "quote", "tag"},
	}

	response := &StyleConfigResponse{
		Card:   config.Card,
		Styles: config.Styles,
		Rules:  rules,
	}

	core.WriteResponse(c, nil, response)
}
