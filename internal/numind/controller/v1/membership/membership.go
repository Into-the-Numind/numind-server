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
		MembershipType   string `json:"membership_type" binding:"required,oneof=subscription package"`
		PackageCount     int    `json:"package_count,omitempty"`     // 仅当membership_type为package时使用
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
	if req.MembershipType == model.MembershipTypePackage {
		if req.PackageCount <= 0 {
			core.WriteResponse(c, errno.ErrBind.SetMessage("包次数必须大于0"), nil)
			return
		}
		// 验证包次数是否在允许的范围内
		validCounts := []int{1, 5, 20, 50}
		isValidCount := false
		for _, count := range validCounts {
			if req.PackageCount == count {
				isValidCount = true
				break
			}
		}
		if !isValidCount {
			core.WriteResponse(c, errno.ErrBind.SetMessage("包次数只支持1、5、20、50次"), nil)
			return
		}
	} else if req.MembershipType == model.MembershipTypeSubscription {
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
	case model.MembershipTypePackage:
		// 资源包：服务端根据次数计算价格
		amount = mc.calculatePackagePrice(req.PackageCount)
		description = fmt.Sprintf("资源包会员（%d次）", req.PackageCount)
	}

	// 创建支付请求
	paymentReq := &model.CreatePaymentRequest{
		OutTradeNo:       outTradeNo,
		Description:      description,
		Amount:           amount,
		OpenID:           req.OpenID,
		PayMethod:        req.PayMethod,
		MembershipType:   req.MembershipType,
		PackageCount:     req.PackageCount,
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

	// 计算package剩余次数信息
	var packageInfo gin.H
	if user.MembershipType == model.MembershipTypePackage || user.MembershipType == model.MembershipTypeBoth {
		packageInfo = gin.H{
			"remaining_count": user.PackageCount,
			"description":     fmt.Sprintf("资源包剩余%d次", user.PackageCount),
			"can_use":         user.PackageCount > 0,
		}
	} else {
		packageInfo = gin.H{
			"remaining_count": 0,
			"description":     "非资源包会员",
			"can_use":         false,
		}
	}

	// 计算月度卡册信息
	var monthlyBookInfo gin.H
	if user.MembershipType == model.MembershipTypeSubscription || user.MembershipType == model.MembershipTypeBoth {
		monthStart := user.GetCurrentMembershipMonthStart()
		monthlyBookInfo = gin.H{
			"current_count":   user.MonthlyBookCount,
			"remaining_count": user.GetRemainingMonthlyBooks(),
			"month_start":     &monthStart,
			"month_end":       user.GetCurrentMembershipMonthEnd(),
			"can_create":      user.CanCreateBookInCurrentMonth(),
			"description":     fmt.Sprintf("本月已创建%d个卡册，还可以创建%d个", user.MonthlyBookCount, user.GetRemainingMonthlyBooks()),
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
		"package_count":      user.PackageCount,
		"package_info":       packageInfo,
		"monthly_book_info":  monthlyBookInfo,
		"is_pro":             user.IsPro,
		"membership_status":  user.GetMembershipStatus(),
		"is_active":          user.IsMembershipActive(),
	})
}

// GetMembershipPlans 获取会员套餐信息
func (mc *MembershipController) GetMembershipPlans(c *gin.Context) {
	plans := []gin.H{
		{
			"type":        "subscription",
			"name":        "月度订阅会员",
			"price":       2800, // 28元，单位分
			"days":        30,   // 订阅天数
			"description": "享受月度订阅会员权益",
			"features":    []string{"30天会员权益", "无水印", "解锁全部模板", "高峰期优先处理"},
		},
		{
			"type":        "subscription",
			"name":        "年度订阅会员",
			"price":       19800, // 198元，单位分
			"days":        365,   // 订阅天数
			"description": "享受年度订阅会员权益，约16.5元/月，立省40%",
			"features":    []string{"365天会员权益", "无水印", "解锁全部模板", "高峰期优先处理", "年度优惠价格"},
		},
		// 资源包选项 - 根据定价表
		{
			"type":        "package",
			"name":        "1次创作包",
			"price":       300, // 3元，单位分
			"count":       1,
			"unit_price":  300, // 3.0元/次，单位分
			"description": "单次使用，适合偶尔使用",
			"features":    []string{"按次计费", "灵活使用", "适合偶尔使用"},
		},
		{
			"type":        "package",
			"name":        "5次创作包",
			"price":       1200, // 12元，单位分
			"count":       5,
			"unit_price":  240, // 2.4元/次，单位分
			"description": "5次使用，单次成本2.4元",
			"features":    []string{"按次计费", "灵活使用", "单次成本优惠"},
		},
		{
			"type":        "package",
			"name":        "20次创作包",
			"price":       3800, // 38元，单位分
			"count":       20,
			"unit_price":  190, // 1.9元/次，单位分
			"description": "20次使用，单次成本1.9元",
			"features":    []string{"按次计费", "灵活使用", "单次成本更优惠"},
		},
		{
			"type":        "package",
			"name":        "50次创作包",
			"price":       5000, // 50元，单位分
			"count":       50,
			"unit_price":  100, // 1.0元/次，单位分
			"description": "50次使用，单次成本1.0元",
			"features":    []string{"按次计费", "灵活使用", "单次成本最优惠"},
		},
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
		"package_count":      user.PackageCount,
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
	// 检查是否需要重置月度计数
	if user.IsInNewMembershipMonth() {
		// 重置月度计数
		if err := mc.resetMonthlyBookCount(user); err != nil {
			log.C(context.Background()).Errorw("Failed to reset monthly book count", "user_id", user.ID, "error", err.Error())
		}
	}

	// 检查订阅会员月度限制
	if user.CanUseSubscription() {
		// 检查月度限制
		if !user.CanCreateBookInCurrentMonth() {
			remaining := user.GetRemainingMonthlyBooks()
			// 如果还有资源包，可以使用资源包创建
			if user.CanUsePackage() && user.PackageCount > 0 {
				return &CreatePermission{
					CanCreate: true,
					Reason:    fmt.Sprintf("本月已创建%d个卡册，达到月度限制30个，但资源包剩余%d次，可以继续创建", user.MonthlyBookCount, user.PackageCount),
				}
			}
			// 没有资源包或资源包用完了
			return &CreatePermission{
				CanCreate: false,
				Reason:    fmt.Sprintf("本月已创建%d个卡册，达到月度限制30个，剩余%d个", user.MonthlyBookCount, remaining),
			}
		}

		// 月度限制未达到，可以创建
		return &CreatePermission{
			CanCreate: true,
			Reason:    fmt.Sprintf("%s有效，本月已创建%d个卡册，还可以创建%d个", user.GetMembershipStatus(), user.MonthlyBookCount, user.GetRemainingMonthlyBooks()),
		}
	}

	// 检查资源包
	if user.CanUsePackage() {
		return &CreatePermission{
			CanCreate: true,
			Reason:    fmt.Sprintf("资源包剩余%d次，可以创建卡册", user.PackageCount),
		}
	}

	// 检查免费用户限制
	if user.MembershipType == model.MembershipTypeFree {
		// 免费用户限制：已创建的卡册数量不能超过一定限制
		// 这里假设免费用户最多可以创建3个卡册
		const freeUserMaxBooks = 3
		if user.BookAllNum < freeUserMaxBooks {
			return &CreatePermission{
				CanCreate: true,
				Reason:    fmt.Sprintf("免费用户，已创建%d个卡册，还可以创建%d个", user.BookAllNum, freeUserMaxBooks-user.BookAllNum),
			}
		}
		return &CreatePermission{
			CanCreate: false,
			Reason:    fmt.Sprintf("免费用户最多只能创建%d个卡册，请升级会员或购买资源包", freeUserMaxBooks),
		}
	}

	// 会员过期
	return &CreatePermission{
		CanCreate: false,
		Reason:    "会员已过期，请续费或购买资源包",
	}
}

// calculateSubscriptionPrice 根据订阅天数计算价格（单位：分）
func (mc *MembershipController) calculateSubscriptionPrice(days int) int64 {
	switch days {
	case 30:
		return 2800 // 28元
	case 365:
		return 19800 // 198元
	default:
		// 默认返回月度价格
		return 2800
	}
}

// calculatePackagePrice 根据包次数计算价格（单位：分）
func (mc *MembershipController) calculatePackagePrice(count int) int64 {
	switch count {
	case 1:
		return 300 // 3元
	case 5:
		return 1200 // 12元
	case 20:
		return 3800 // 38元
	case 50:
		return 5000 // 50元
	default:
		// 如果不是标准包次数，按单次3元计算
		return int64(count * 300)
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
		"remaining":       user.PackageCount,
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

// consumeUserUsage 消费用户使用次数
func (mc *MembershipController) consumeUserUsage(c *gin.Context, user *model.User) (string, error) {
	// 优先使用订阅会员
	if user.CanUseSubscription() {
		// 订阅会员无限制，不需要扣除次数
		return "subscription", nil
	}

	// 其次使用资源包
	if user.CanUsePackage() {
		// 扣除资源包次数
		if err := mc.b.Users().ConsumePackageCount(c, user.ID, 1); err != nil {
			return "", fmt.Errorf("扣除资源包次数失败: %w", err)
		}
		return "package", nil
	}

	return "", fmt.Errorf("没有可用的使用次数")
}
