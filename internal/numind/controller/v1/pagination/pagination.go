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
			Type: pagination.ElementTypeBody,
			Content: "因为流不动，容易被误解，甚至被"拒收"。价值越高的东西，越考验人的理解能力。",
		},
		{
			Type: pagination.ElementTypeList,
			Content: []string{
				"《道德经》中的"无为"被解读成"什么都不做"。",
				""以德报怨"，被误解为"被人欺负要用爱心感化"。",
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