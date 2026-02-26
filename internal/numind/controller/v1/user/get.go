// Copyright 2022 Innkeeper Belm(孔令飞) <nosbelm@qq.com>. All rights reserved.
// Use of this source code is governed by a MIT style
// license that can be found in the LICENSE file. The original repo for
// this file is https://github.com/marmotedu/miniblog.

package user

import (
	"github.com/gin-gonic/gin"

	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/middleware"
	"numind-server/internal/pkg/util"
)

// Get 获取一个用户的详细信息.
func (ctrl *UserController) Get(c *gin.Context) {
	log.C(c).Infow("Get user function called")

	user, err := ctrl.b.Users().Get(c, c.Param("name"))
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, user)
}

// GetCurrentUser 获取当前登录用户的详细信息
func (ctrl *UserController) GetCurrentUser(c *gin.Context) {
	log.C(c).Infow("Get current user function called")

	// 从中间件中获取当前用户
	currentUser := middleware.GetCurrentUser(c)
	if currentUser == nil {
		core.WriteResponse(c, errno.ErrUnauthorized, nil)
		return
	}

	// 通过用户ID获取完整的用户信息（包含统计信息）
	userWithStats, err := ctrl.b.Users().GetCurrentUserWithStats(c, currentUser.ID)
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}
	if userWithStats.AvatarURL != "" {
		userWithStats.AvatarURL = util.GetAvatarWithCOS(c, userWithStats.ID, userWithStats.AvatarURL)
	}

	// 构建响应数据，包含完整的 SOP 统计和会员等级
	response := gin.H{
		"id":                           userWithStats.ID,
		"openid":                       userWithStats.OpenID,
		"unionid":                      userWithStats.UnionID,
		"phone":                        userWithStats.Phone,
		"nickname":                     userWithStats.Nickname,
		"avatar_url":                   userWithStats.AvatarURL,
		"is_pro":                       userWithStats.IsPro,
		"membership_type":              userWithStats.MembershipType,
		"membership_expires":           userWithStats.MembershipExpires,
		"membership_start_date":        userWithStats.MembershipStartDate,
		"package_count":                userWithStats.PackageCount,
		"book_num":                     userWithStats.BookNum,
		"book_all_num":                 userWithStats.BookAllNum,
		"monthly_book_count":           userWithStats.MonthlyBookCount,
		"free_user_monthly_book_count": userWithStats.FreeUserMonthlyBookCount,
		"free_user_last_reset_date":    userWithStats.FreeUserLastResetDate,
		"card_num":                     userWithStats.CardNum,
		"chat_num":                     userWithStats.ChatNum,
		"created_at":                   userWithStats.CreatedAt,
		"updated_at":                   userWithStats.UpdatedAt,

		// 新增：SOP 统计与等级信息，确保前端设置页面秒开
		"user_tier":          userWithStats.GetActualUserTier(),
		"tier_expires":       userWithStats.TierExpires,
		"total_sop_runs":     userWithStats.TotalSopRuns,
		"monthly_sop_runs":   userWithStats.MonthlySopRuns,
		"remaining_sop_runs": userWithStats.GetRemainingSOPRuns(),
	}

	core.WriteResponse(c, nil, response)
}

// GetUserV2 独立的用户信息获取接口
func (ctrl *UserController) GetUserV2(c *gin.Context) {
	log.C(c).Infow("Get user v2 function called")

	user, err := ctrl.b.Users().Get(c, c.Param("username"))
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, user)
}
