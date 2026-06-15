// Copyright 2022 Innkeeper Belm(孔令飞) <nosbelm@qq.com>. All rights reserved.
// Use of this source code is governed by a MIT style
// license that can be found in the LICENSE file. The original repo for
// this file is https://github.com/marmotedu/miniblog.

package user

import (
	"time"

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

	// org-branding: 解析有效机构品牌名（父账户用自己/子账户用父账户/空串=未设置）。
	// 失败不阻断 /me，降级为空串，前端兜底"有数AI"。
	brandName, rcErr := ctrl.b.Users().ResolveCompanyName(c, userWithStats)
	if rcErr != nil {
		log.C(c).Warnw("ResolveCompanyName failed", "user_id", userWithStats.ID, "err", rcErr)
		brandName = ""
	}

	// 构建响应数据（credits-only 计费体系，legacy_tier 已移除 2026-05）
	response := gin.H{
		"id":             userWithStats.ID,
		"phone":          userWithStats.Phone,
		"nickname":       userWithStats.Nickname,
		"avatar_url":     userWithStats.AvatarURL,
		"created_at":     userWithStats.CreatedAt,
		"updated_at":     userWithStats.UpdatedAt,
		"parent_user_id": userWithStats.ParentUserID,
		"company_name":   brandName,

		"total_sop_runs": userWithStats.TotalSopRuns,
	}

	// credits-mode membership 状态：附加订阅/试用到期时间供前端展示。
	if ctrl.membershipSvc != nil {
		now := time.Now().UTC()
		state, err := ctrl.membershipSvc.GetMembershipState(c, uint64(userWithStats.ID), now)
		if err != nil {
			log.C(c).Warnw("GetMembershipState failed", "user_id", userWithStats.ID, "err", err)
		} else {
			response["membership_state"] = state.DisplayState
			if state.SubExpiresAt != nil {
				response["sub_expires_at"] = state.SubExpiresAt
			}
			if state.TrialExpiresAt != nil {
				response["trial_expires_at"] = state.TrialExpiresAt
			}
		}
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
