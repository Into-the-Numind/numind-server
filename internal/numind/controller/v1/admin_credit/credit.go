package admin_credit

import (
	"fmt"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"numind-server/internal/numind/biz/credit"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
)

// AdminCreditController 管理员积分控制器
type AdminCreditController struct {
	creditBiz credit.ICreditBiz
	ds        store.IStore
}

// New 创建管理员积分控制器
func New(creditBiz credit.ICreditBiz, ds store.IStore) *AdminCreditController {
	return &AdminCreditController{creditBiz: creditBiz, ds: ds}
}

// parseDuration 解析时长字符串，支持 "30d"（天）和 "3M"（月）
func parseDuration(s string) (time.Time, error) {
	if len(s) < 2 {
		return time.Time{}, fmt.Errorf("invalid duration format: %q", s)
	}
	unit := s[len(s)-1]
	numStr := s[:len(s)-1]
	n, err := strconv.Atoi(numStr)
	if err != nil || n <= 0 {
		return time.Time{}, fmt.Errorf("invalid duration value: %q", s)
	}
	now := time.Now()
	switch unit {
	case 'd':
		return now.AddDate(0, 0, n), nil
	case 'M':
		return now.AddDate(0, n, 0), nil
	default:
		return time.Time{}, fmt.Errorf("unsupported duration unit %q, use 'd' (days) or 'M' (months)", string(unit))
	}
}

// ListUsers GET /credits/users — 分页查询所有积分账户
func (ctrl *AdminCreditController) ListUsers(c *gin.Context) {
	log.C(c).Infow("Admin list credit users called")

	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
	if limit > 100 {
		limit = 100
	}

	accounts, total, err := ctrl.ds.Credits().ListAllAccountsWithBalance(c, offset, limit)
	if err != nil {
		log.C(c).Errorw("Failed to list credit accounts", "error", err)
		core.WriteResponse(c, errno.InternalServerError.SetMessage("查询失败，请稍后重试"), nil)
		return
	}

	core.WriteResponse(c, nil, gin.H{
		"total":    total,
		"accounts": accounts,
	})
}

// GetUserDetail GET /credits/users/:id — 查询用户积分详情（账户 + 积分包 + 流水）
func (ctrl *AdminCreditController) GetUserDetail(c *gin.Context) {
	log.C(c).Infow("Admin get credit user detail called")

	idStr := c.Param("id")
	uid, err := strconv.ParseUint(idStr, 10, 32)
	if err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("无效的用户ID"), nil)
		return
	}
	userID := uint(uid)

	// 账户信息
	account, err := ctrl.ds.Credits().GetOrCreateAccount(c, userID)
	if err != nil {
		log.C(c).Errorw("Failed to get credit account", "user_id", userID, "error", err)
		core.WriteResponse(c, errno.InternalServerError.SetMessage("查询账户失败"), nil)
		return
	}

	// 积分包列表（最多 50 条）
	packages, _, err := ctrl.ds.Credits().ListPackagesByUser(c, userID, 0, 50)
	if err != nil {
		log.C(c).Errorw("Failed to list credit packages", "user_id", userID, "error", err)
		core.WriteResponse(c, errno.InternalServerError.SetMessage("查询积分包失败"), nil)
		return
	}

	// 流水记录（最多 50 条）
	transactions, _, err := ctrl.ds.Credits().ListTransactionsByUser(c, userID, 0, 50)
	if err != nil {
		log.C(c).Errorw("Failed to list credit transactions", "user_id", userID, "error", err)
		core.WriteResponse(c, errno.InternalServerError.SetMessage("查询流水失败"), nil)
		return
	}

	core.WriteResponse(c, nil, gin.H{
		"account":      account,
		"packages":     packages,
		"transactions": transactions,
	})
}

// rechargeRequest 充值请求体
type rechargeRequest struct {
	Type         string `json:"type"`          // trial / subscription / booster
	TotalCredits int64  `json:"total_credits"` // 充值积分数量
	ExpiresIn    string `json:"expires_in"`    // 时长字符串，如 "30d" 或 "3M"
}

// Recharge POST /credits/users/:id/recharge — 管理员手动充值积分
func (ctrl *AdminCreditController) Recharge(c *gin.Context) {
	log.C(c).Infow("Admin recharge credits called")

	idStr := c.Param("id")
	uid, err := strconv.ParseUint(idStr, 10, 32)
	if err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("无效的用户ID"), nil)
		return
	}
	userID := uint(uid)

	var req rechargeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("请求参数错误"), nil)
		return
	}

	if req.TotalCredits <= 0 {
		core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage("额度数量必须大于0"), nil)
		return
	}

	if req.Type == "" {
		core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage("额度包类型不能为空"), nil)
		return
	}

	if req.ExpiresIn == "" {
		core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage("有效期不能为空（如 '30d' 或 '3M'）"), nil)
		return
	}

	expiresAt, err := parseDuration(req.ExpiresIn)
	if err != nil {
		core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage("有效期格式错误，支持 '30d'（天）或 '3M'（月）"), nil)
		return
	}

	if err := ctrl.creditBiz.RechargeCredits(c, userID, req.Type, req.TotalCredits, expiresAt); err != nil {
		log.C(c).Errorw("Failed to recharge credits", "user_id", userID, "error", err)
		core.WriteResponse(c, errno.InternalServerError.SetMessage("充值失败，请稍后重试"), nil)
		return
	}

	log.C(c).Infow("Admin recharged credits", "user_id", userID, "type", req.Type, "total_credits", req.TotalCredits, "expires_at", expiresAt)
	core.WriteResponse(c, nil, gin.H{
		"message":    "充值成功",
		"user_id":    userID,
		"type":       req.Type,
		"credits":    req.TotalCredits,
		"expires_at": expiresAt.Format("2006-01-02 15:04:05"),
	})
}
