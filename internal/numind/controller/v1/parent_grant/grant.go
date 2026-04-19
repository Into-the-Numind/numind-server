package parent_grant

import (
	"strconv"

	"github.com/gin-gonic/gin"

	"numind-server/internal/numind/biz/credit"
	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"
)

// ParentGrantController 父账户(B 端)为子账户赋予会员的控制器。
// 对应路由 POST /v1/users/children/:child_id/grant-membership
type ParentGrantController struct {
	creditBiz credit.ICreditBiz
}

// New 创建 ParentGrantController 实例。
func New(creditBiz credit.ICreditBiz) *ParentGrantController {
	return &ParentGrantController{creditBiz: creditBiz}
}

// GrantMembershipRequest 请求体：父账户指定子账户、产品类型、时长、理由。
type GrantMembershipRequest struct {
	ProductType string `json:"product_type" binding:"required,oneof=trial monthly"`
	Months      int    `json:"months" binding:"omitempty,min=1,max=12"` // trial 不需要传，固定 3 天
	Reason      string `json:"reason" binding:"omitempty,max=500"`
}

// GrantMembership handles POST /v1/users/children/:child_id/grant-membership.
//
// Auth context: current user = parent (B 端账户).
// Path param: :child_id = 目标子账户 ID.
// Body: { product_type, months, reason }.
//
// 委托 creditBiz.GrantMembership 执行全部业务逻辑（父子鉴权、防重复、
// credit_package 创建、billing_mode 切换、action_log 写入）。
func (ctrl *ParentGrantController) GrantMembership(c *gin.Context) {
	log.C(c).Infow("Grant membership called")

	// 1. 从 auth 上下文提取当前 parent user
	currentUser, exists := c.Get("current_user")
	if !exists {
		core.WriteResponse(c, errno.ErrUnauthorized.SetMessage("未找到用户信息"), nil)
		return
	}
	parent := currentUser.(*model.User)

	// 2. 解析 path param child_id
	childIDRaw := c.Param("child_id")
	childID64, err := strconv.ParseUint(childIDRaw, 10, 32)
	if err != nil || childID64 == 0 {
		core.WriteResponse(c, errno.ErrBind.SetMessage("无效的子账户 ID: %s", childIDRaw), nil)
		return
	}
	childID := uint(childID64)

	// 3. 绑定 JSON body
	var req GrantMembershipRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("请求参数错误: %s", err.Error()), nil)
		return
	}

	// 4. 委托 biz 执行
	err = ctrl.creditBiz.GrantMembership(c, credit.GrantMembershipReq{
		ParentUserID: parent.ID,
		ChildUserID:  childID,
		ProductType:  req.ProductType,
		Months:       req.Months,
		Reason:       req.Reason,
	})
	if err != nil {
		log.C(c).Errorw("Failed to grant membership",
			"parent_user_id", parent.ID,
			"child_user_id", childID,
			"product_type", req.ProductType,
			"months", req.Months,
			"err", err)
		core.WriteResponse(c, errno.InternalServerError.SetMessage("%s", err.Error()), nil)
		return
	}

	core.WriteResponse(c, nil, gin.H{
		"message":       "开通成功",
		"child_user_id": childID,
		"product_type":  req.ProductType,
		"months":        req.Months,
	})
}
