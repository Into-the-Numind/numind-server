package admin_credit

import (
	"errors"
	"math"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
	v1 "numind-server/pkg/api/numind/v1"
)

// ListUserTypeConfigs GET /v1/admin/credits/user-types — list all per-user-type credit multipliers.
func (ctrl *AdminCreditController) ListUserTypeConfigs(c *gin.Context) {
	rows, err := ctrl.ds.Credits().ListUserTypeConfigs(c)
	if err != nil {
		log.C(c).Errorw("ListUserTypeConfigs failed", "err", err)
		core.WriteResponse(c, errno.InternalServerError, nil)
		return
	}

	items := make([]v1.AdminCreditUserTypeConfigItem, 0, len(rows))
	for _, r := range rows {
		items = append(items, v1.AdminCreditUserTypeConfigItem{
			UserType:         r.UserType,
			CreditMultiplier: r.CreditMultiplier,
			Description:      r.Description,
			IsActive:         r.IsActive,
			CreatedAt:        r.CreatedAt,
			UpdatedAt:        r.UpdatedAt,
		})
	}
	core.WriteResponse(c, nil, v1.AdminListCreditUserTypeConfigsResponse{Items: items})
}

// UpdateUserTypeConfig PUT /v1/admin/credits/user-types/:user_type — update fields on
// the row keyed by user_type. Returns 404 when no row matches user_type.
func (ctrl *AdminCreditController) UpdateUserTypeConfig(c *gin.Context) {
	userType := c.Param("user_type")
	if userType == "" {
		core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage("user_type 不能为空"), nil)
		return
	}

	var req v1.AdminUpdateCreditUserTypeConfigRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("%s", err.Error()), nil)
		return
	}

	updates := map[string]interface{}{}
	if req.CreditMultiplier != nil {
		m := *req.CreditMultiplier
		// Mirror pricing_rule.credit_multiplier validation: (0, 100], reject NaN/Inf.
		if m <= 0 || math.IsNaN(m) || math.IsInf(m, 0) || m > 100 {
			core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage("积分倍率必须在 0.01 到 100 之间"), nil)
			return
		}
		updates["credit_multiplier"] = m
	}
	if req.Description != nil {
		updates["description"] = *req.Description
	}
	if req.IsActive != nil {
		updates["is_active"] = *req.IsActive
	}

	if len(updates) == 0 {
		core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage("没有可更新的字段"), nil)
		return
	}

	if err := ctrl.ds.Credits().UpdateUserTypeConfig(c, userType, updates); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			core.WriteResponse(c, errno.ErrPageNotFound.SetMessage("用户类型 %q 不存在", userType), nil)
			return
		}
		log.C(c).Errorw("UpdateUserTypeConfig failed", "user_type", userType, "err", err)
		core.WriteResponse(c, errno.InternalServerError, nil)
		return
	}

	log.C(c).Infow("Admin updated credit user-type config", "user_type", userType, "fields", len(updates))
	core.WriteResponse(c, nil, gin.H{"user_type": userType, "updated_fields": len(updates)})
}
