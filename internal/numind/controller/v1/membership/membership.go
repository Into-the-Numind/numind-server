package membership

import (
	"context"
	"fmt"
	"time"

	"numind-server/internal/numind/biz"
	"numind-server/internal/numind/biz/wechat"
	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/middleware"
	"numind-server/internal/pkg/model"

	"github.com/gin-gonic/gin"
	"github.com/spf13/viper"
)

// MembershipController 会员控制器
type MembershipController struct {
	b biz.IBiz
}

// NewMembershipController 创建会员控制器实例
func NewMembershipController(b biz.IBiz) *MembershipController {
	return &MembershipController{b: b}
}

// getWechatPayConfig 获取微信支付配置
func getWechatPayConfig() map[string]string {
	return map[string]string{
		"app_id":                   viper.GetString("wechat.app_id"),
		"mch_id":                   viper.GetString("wechat.mch_id"),
		"mch_cert_serial_no":       viper.GetString("wechat.mch_cert_serial_no"),
		"mch_api_v3_key":           viper.GetString("wechat.mch_api_v3_key"),
		"mch_private_key_path":     viper.GetString("wechat.mch_private_key_path"),
		"wechatpay_cert_path":      viper.GetString("wechat.wechatpay_cert_path"),
		"notify_url":               viper.GetString("wechat.notify_url"),
		"use_wechatpay_public_key": viper.GetString("wechat.use_wechatpay_public_key"),
	}
}

// CreateMembershipPayment 创建会员购买支付
func (mc *MembershipController) CreateMembershipPayment(c *gin.Context) {
	var req struct {
		MembershipType   string `json:"membership_type" binding:"required,oneof=subscription"`
		SubscriptionDays int    `json:"subscription_days,omitempty"` // 仅当membership_type为subscription时使用，30或365
		PayMethod        string `json:"pay_method" binding:"required,oneof=native miniprogram jsapi"`
		OpenID           string `json:"openid,omitempty"` // 小程序支付必填
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("参数错误: %s", err.Error()), nil)
		return
	}

	// 获取当前用户ID
	userID := middleware.GetCurrentUser(c)
	if userID == nil {
		core.WriteResponse(c, errno.ErrUnauthorized.SetMessage("用户未登录"), nil)
		return
	}

	// 验证参数
	if req.MembershipType == model.MembershipTypeSubscription {
		// 添加调试日志
		log.C(c).Infow("订阅天数验证", "subscription_days", req.SubscriptionDays, "type", fmt.Sprintf("%T", req.SubscriptionDays))
		if req.SubscriptionDays != 30 && req.SubscriptionDays != 365 {
			core.WriteResponse(c, errno.ErrBind.SetMessage("订阅天数只支持30天和365天，当前值: %d", req.SubscriptionDays), nil)
			return
		}
	}

	// 生成订单号（确保不超过32字符）
	outTradeNo := fmt.Sprintf("mem_%d_%d", userID.ID, time.Now().UnixNano())

	// 根据会员类型设置金额和描述（服务端计算价格，防止前端篡改）
	var amount int64
	var description string

	switch req.MembershipType {
	case model.MembershipTypeSubscription:
		// 订阅会员：服务端根据天数计算价格
		amount = mc.calculateSubscriptionPrice(req.SubscriptionDays)
		if req.SubscriptionDays == 30 {
			description = "月度订阅会员（30天）"
		} else {
			description = "年度订阅会员（365天）"
		}
	}

	// 创建支付请求
	paymentReq := &model.CreatePaymentRequest{
		OutTradeNo:       outTradeNo,
		Description:      description,
		Amount:           amount,
		OpenID:           req.OpenID,
		PayMethod:        req.PayMethod,
		MembershipType:   req.MembershipType,
		SubscriptionDays: req.SubscriptionDays,
	}

	// 创建支付记录
	paymentResp, err := mc.b.Payments().CreatePayment(c, paymentReq, userID.ID)
	if err != nil {
		core.WriteResponse(c, errno.InternalServerError.SetMessage("创建支付失败: %s", err.Error()), nil)
		return
	}

	// 根据支付方式调用对应的支付接口
	switch req.PayMethod {
	case model.PaymentMethodNative:
		mc.createNativePayment(c, *paymentReq, paymentResp)
		return
	case model.PaymentMethodMiniProgram:
		mc.createMiniProgramPayment(c, *paymentReq, paymentResp)
		return
	case model.PaymentMethodJSAPI:
		mc.createJSAPIPayment(c, *paymentReq, paymentResp)
		return
	default:
		core.WriteResponse(c, errno.ErrBind.SetMessage("不支持的支付方式"), nil)
		return
	}
}

// createNativePayment 创建扫码支付
func (mc *MembershipController) createNativePayment(c *gin.Context, req model.CreatePaymentRequest, paymentResp *model.CreatePaymentResponse) {
	// 获取微信支付配置
	config := getWechatPayConfig()

	// 调用微信支付API创建Native支付订单
	resp, err := wechat.CreateNativeOrder(config, req.OutTradeNo, req.Description, req.Amount)
	if err != nil {
		// 支付创建失败，更新支付状态为失败
		mc.b.Payments().UpdatePaymentStatus(c, req.OutTradeNo, model.PaymentStatusFailed, "", nil)
		core.WriteResponse(c, errno.InternalServerError.SetMessage("创建微信支付失败: %s", err.Error()), nil)
		return
	}

	// 更新支付记录中的二维码链接等信息
	if respMap, ok := resp.(map[string]interface{}); ok {
		if codeURL, exists := respMap["code_url"]; exists {
			paymentResp.CodeURL = codeURL.(string)
		}
		if prepayID, exists := respMap["prepay_id"]; exists {
			paymentResp.PrepayID = prepayID.(string)
		}
	}

	core.WriteResponse(c, nil, gin.H{
		"out_trade_no": paymentResp.OutTradeNo,
		"code_url":     paymentResp.CodeURL,
		"prepay_id":    paymentResp.PrepayID,
		"message":      "请使用微信扫描二维码完成支付",
	})
}

// createMiniProgramPayment 创建小程序支付
func (mc *MembershipController) createMiniProgramPayment(c *gin.Context, req model.CreatePaymentRequest, paymentResp *model.CreatePaymentResponse) {
	if req.OpenID == "" {
		core.WriteResponse(c, errno.ErrBind.SetMessage("小程序支付必须提供OpenID"), nil)
		return
	}

	// 获取微信支付配置
	config := getWechatPayConfig()

	// 调用微信支付API创建小程序支付订单
	resp, err := wechat.CreateMiniProgramOrder(config, req.OutTradeNo, req.Description, req.Amount, req.OpenID)
	if err != nil {
		// 支付创建失败，更新支付状态为失败
		mc.b.Payments().UpdatePaymentStatus(c, req.OutTradeNo, model.PaymentStatusFailed, "", nil)
		core.WriteResponse(c, errno.InternalServerError.SetMessage("创建微信支付失败: %s", err.Error()), nil)
		return
	}

	// 返回小程序支付参数
	core.WriteResponse(c, nil, resp)
}

// createJSAPIPayment 创建JSAPI支付
func (mc *MembershipController) createJSAPIPayment(c *gin.Context, req model.CreatePaymentRequest, paymentResp *model.CreatePaymentResponse) {
	if req.OpenID == "" {
		core.WriteResponse(c, errno.ErrBind.SetMessage("JSAPI支付必须提供OpenID"), nil)
		return
	}

	// 获取微信支付配置
	config := getWechatPayConfig()

	// 调用微信支付API创建JSAPI支付订单（JSAPI和小程序支付使用相同的API）
	resp, err := wechat.CreateMiniProgramOrder(config, req.OutTradeNo, req.Description, req.Amount, req.OpenID)
	if err != nil {
		// 支付创建失败，更新支付状态为失败
		mc.b.Payments().UpdatePaymentStatus(c, req.OutTradeNo, model.PaymentStatusFailed, "", nil)
		core.WriteResponse(c, errno.InternalServerError.SetMessage("创建微信支付失败: %s", err.Error()), nil)
		return
	}

	// 返回JSAPI支付参数
	core.WriteResponse(c, nil, resp)
}

// GetMembershipInfo 获取用户会员信息
func (mc *MembershipController) GetMembershipInfo(c *gin.Context) {
	// 获取当前用户ID
	userID := middleware.GetCurrentUser(c)
	if userID == nil {
		core.WriteResponse(c, errno.ErrUnauthorized.SetMessage("用户未登录"), nil)
		return
	}

	// 获取用户信息
	user, err := mc.b.Users().GetCurrentUser(c, userID.ID)
	if err != nil {
		core.WriteResponse(c, errno.InternalServerError.SetMessage("获取用户信息失败: %s", err.Error()), nil)
		return
	}

	// 计算月度卡册信息
	var monthlyBookInfo gin.H
	if user.MembershipType == model.MembershipTypeSubscription || user.MembershipType == model.MembershipTypeBoth {
		monthStart := user.GetCurrentMembershipMonthStart()
		monthlyBookInfo = gin.H{
			"current_count":   user.MonthlyBookCount,
			"remaining_count": -1, // 无限制
			"month_start":     &monthStart,
			"month_end":       user.GetCurrentMembershipMonthEnd(),
			"can_create":      true,
			"description":     "会员无数量限制",
		}
	} else {
		monthlyBookInfo = gin.H{
			"current_count":   0,
			"remaining_count": -1, // 无限制
			"month_start":     nil,
			"month_end":       nil,
			"can_create":      true,
			"description":     "非订阅会员，无月度限制",
		}
	}

	// 返回会员信息
	core.WriteResponse(c, nil, gin.H{
		"membership_type":    user.MembershipType,
		"membership_expires": user.MembershipExpires,
		"monthly_book_info":  monthlyBookInfo,
		"is_pro":             user.IsPro,
		"membership_status":  user.GetMembershipStatus(),
		"is_active":          user.IsMembershipActive(),
	})
}

// GetMembershipPlans 获取会员套餐信息
func (mc *MembershipController) GetMembershipPlans(c *gin.Context) {
	// 检查是否为开发环境
	runmode := viper.GetString("runmode")
	isDev := runmode == "debug"

	var plans []gin.H
	if isDev {
		// 开发环境：1分钱用于测试
		plans = []gin.H{
			{
				"type":        "subscription",
				"name":        "月度订阅会员（测试）",
				"price":       1,  // 1分，单位分
				"days":        30, // 订阅天数
				"description": "享受月度订阅会员权益（开发环境测试价格）",
				"features":    []string{"30天会员权益", "无水印", "解锁全部模板", "高峰期优先处理"},
			},
			{
				"type":        "subscription",
				"name":        "年度订阅会员（测试）",
				"price":       1,   // 1分，单位分
				"days":        365, // 订阅天数
				"description": "享受年度订阅会员权益（开发环境测试价格）",
				"features":    []string{"365天会员权益", "无水印", "解锁全部模板", "高峰期优先处理", "年度优惠价格"},
			},
		}
	} else {
		// 生产环境：正常价格
		plans = []gin.H{
			{
				"type":        "subscription",
				"name":        "月度订阅会员",
				"price":       1600, // 16元，单位分
				"days":        30,   // 订阅天数
				"description": "享受月度订阅会员权益",
				"features":    []string{"30天会员权益", "无水印", "解锁全部模板", "高峰期优先处理"},
			},
			{
				"type":        "subscription",
				"name":        "年度订阅会员",
				"price":       11900, // 119元，单位分
				"days":        365,   // 订阅天数
				"description": "享受年度订阅会员权益，约9.92元/月，立省38%",
				"features":    []string{"365天会员权益", "无水印", "解锁全部模板", "高峰期优先处理", "年度优惠价格"},
			},
		}
	}

	core.WriteResponse(c, nil, gin.H{
		"plans": plans,
	})
}

// CheckCreatePermission 检查用户是否有创建卡册的权限
func (mc *MembershipController) CheckCreatePermission(c *gin.Context) {
	// 获取当前用户ID
	userID := middleware.GetCurrentUser(c)
	if userID == nil {
		core.WriteResponse(c, errno.ErrUnauthorized.SetMessage("用户未登录"), nil)
		return
	}

	// 获取用户信息
	user, err := mc.b.Users().GetCurrentUser(c, userID.ID)
	if err != nil {
		core.WriteResponse(c, errno.InternalServerError.SetMessage("获取用户信息失败: %s", err.Error()), nil)
		return
	}

	// 计算权限
	permission := mc.calculateCreatePermission(user)

	core.WriteResponse(c, nil, gin.H{
		"can_create":         permission.CanCreate,
		"reason":             permission.Reason,
		"membership_type":    user.MembershipType,
		"is_pro":             user.IsPro,
		"book_all_num":       user.BookAllNum,
		"membership_expires": user.MembershipExpires,
	})
}

// CreatePermission 创建权限信息
type CreatePermission struct {
	CanCreate bool   `json:"can_create"`
	Reason    string `json:"reason"`
}

// calculateCreatePermission 计算用户创建卡册权限
func (mc *MembershipController) calculateCreatePermission(user *model.User) *CreatePermission {
	// 首先检查并同步会员类型（处理过期情况）
	actualType := user.GetActualMembershipType()
	if actualType != user.MembershipType {
		// 需要更新数据库中的会员类型
		log.C(context.Background()).Infow("Membership type needs sync",
			"user_id", user.ID,
			"old_type", user.MembershipType,
			"new_type", actualType)

		// 判断是否需要重置月度计数
		oldType := user.MembershipType
		resetMonthly := (oldType == model.MembershipTypeSubscription || oldType == model.MembershipTypeBoth) &&
			(actualType == model.MembershipTypeFree)

		// 更新到数据库
		if err := mc.b.Users().SyncMembershipType(context.Background(), user.ID, actualType, resetMonthly); err != nil {
			log.C(context.Background()).Errorw("Failed to sync membership type",
				"user_id", user.ID,
				"error", err.Error())
		} else {
			// 更新本地用户对象
			user.MembershipType = actualType
			if resetMonthly {
				user.MonthlyBookCount = 0
				user.MembershipStartDate = nil
			}
			log.C(context.Background()).Infow("Membership type synced successfully",
				"user_id", user.ID,
				"new_type", actualType,
				"reset_monthly", resetMonthly)
		}
	}

	// 检查是否需要重置月度计数
	if user.IsInNewMembershipMonth() {
		// 重置月度计数
		if err := mc.resetMonthlyBookCount(user); err != nil {
			log.C(context.Background()).Errorw("Failed to reset monthly book count", "user_id", user.ID, "error", err.Error())
		}
	}

	// 检查免费用户是否需要重置月度计数
	if user.MembershipType == model.MembershipTypeFree && user.IsInNewFreeUserMonth() {
		// 重置免费用户月度计数
		if err := mc.resetFreeUserMonthlyBookCount(user); err != nil {
			log.C(context.Background()).Errorw("Failed to reset free user monthly book count", "user_id", user.ID, "error", err.Error())
		}
	}

	// 检查订阅会员
	if user.CanUseSubscription() {
		// 会员无数量限制，可以创建
		return &CreatePermission{
			CanCreate: true,
			Reason:    fmt.Sprintf("%s有效，无数量限制", user.GetMembershipStatus()),
		}
	}

	// 检查免费用户限制
	if user.MembershipType == model.MembershipTypeFree {
		// 检查免费用户月度限制
		if !user.CanCreateBookAsFreeUser() {
			remaining := user.GetRemainingFreeUserMonthlyBooks()
			return &CreatePermission{
				CanCreate: false,
				Reason:    fmt.Sprintf("免费用户本月已创建%d个卡册，达到月度限制5个，剩余%d个，下月1号重置", user.FreeUserMonthlyBookCount, remaining),
			}
		}

		// 免费用户月度限制未达到，可以创建
		remaining := user.GetRemainingFreeUserMonthlyBooks()
		return &CreatePermission{
			CanCreate: true,
			Reason:    fmt.Sprintf("免费用户，本月已创建%d个卡册，还可以创建%d个", user.FreeUserMonthlyBookCount, remaining),
		}
	}

	// 会员过期
	return &CreatePermission{
		CanCreate: false,
		Reason:    "会员已过期，请续费",
	}
}

// calculateSubscriptionPrice 根据订阅天数计算价格（单位：分）
func (mc *MembershipController) calculateSubscriptionPrice(days int) int64 {
	// 检查是否为开发环境
	runmode := viper.GetString("runmode")
	if runmode == "debug" {
		// 开发环境：1分钱用于测试
		if days == 30 || days == 365 {
			return 1
		}
		return 1 // 默认返回1分
	}

	// 生产环境：正常价格
	switch days {
	case 30:
		return 1600 // 16元
	case 365:
		return 11900 // 119元
	default:
		// 默认返回月度价格
		return 1600
	}
}

// ConsumeUsage 消费使用次数（优先使用订阅会员，其次资源包）
func (mc *MembershipController) ConsumeUsage(c *gin.Context) {
	// 获取当前用户ID
	userID := middleware.GetCurrentUser(c)
	if userID == nil {
		core.WriteResponse(c, errno.ErrUnauthorized.SetMessage("用户未登录"), nil)
		return
	}

	// 获取用户信息
	user, err := mc.b.Users().GetCurrentUser(c, userID.ID)
	if err != nil {
		core.WriteResponse(c, errno.InternalServerError.SetMessage("获取用户信息失败: %s", err.Error()), nil)
		return
	}

	// 检查权限
	permission := mc.calculateCreatePermission(user)
	if !permission.CanCreate {
		core.WriteResponse(c, errno.ErrForbidden.SetMessage("%s", permission.Reason), nil)
		return
	}

	// 消费使用次数
	usageType, err := mc.consumeUserUsage(c, user)
	if err != nil {
		core.WriteResponse(c, errno.InternalServerError.SetMessage("消费使用次数失败: %s", err.Error()), nil)
		return
	}

	core.WriteResponse(c, nil, gin.H{
		"message":         "使用次数消费成功",
		"usage_type":      usageType,
		"remaining":       -1,
		"membership_type": user.MembershipType,
	})
}

// resetMonthlyBookCount 重置用户月度卡册计数
func (mc *MembershipController) resetMonthlyBookCount(user *model.User) error {
	// 更新用户的月度计数为0
	if err := mc.b.Users().UpdateMonthlyBookCount(context.Background(), user.ID, 0); err != nil {
		return fmt.Errorf("重置月度卡册计数失败: %w", err)
	}

	// 更新本地用户对象
	user.MonthlyBookCount = 0

	log.C(context.Background()).Infow("Monthly book count reset", "user_id", user.ID, "membership_start_date", user.MembershipStartDate)
	return nil
}

// resetFreeUserMonthlyBookCount 重置免费用户月度卡册计数
func (mc *MembershipController) resetFreeUserMonthlyBookCount(user *model.User) error {
	// 更新免费用户的月度计数为0
	if err := mc.b.Users().ResetFreeUserMonthlyBookCount(context.Background(), user.ID); err != nil {
		return fmt.Errorf("重置免费用户月度卡册计数失败: %w", err)
	}

	// 更新本地用户对象
	user.FreeUserMonthlyBookCount = 0
	now := time.Now()
	user.FreeUserLastResetDate = &now

	log.C(context.Background()).Infow("Free user monthly book count reset", "user_id", user.ID, "reset_date", now)
	return nil
}

// consumeUserUsage 消费用户使用次数
func (mc *MembershipController) consumeUserUsage(c *gin.Context, user *model.User) (string, error) {
	// 使用订阅会员
	if user.CanUseSubscription() {
		// 订阅会员无限制，不需要扣除次数
		return "subscription", nil
	}

	return "", fmt.Errorf("没有可用的使用次数")
}
