package admin_credit

import (
	"strconv"

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

	// 同时返回 `items`（前端 DataTable 约定字段）和 `accounts`（向后兼容）。
	// admin-web CreditUsersView 读 items.length — 不填会崩 DataTable。
	core.WriteResponse(c, nil, gin.H{
		"total":    total,
		"items":    accounts,
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
