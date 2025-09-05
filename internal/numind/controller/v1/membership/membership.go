package membership

import (
	"fmt"
	"time"

	"numind-server/internal/numind/biz"
	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/middleware"
	"numind-server/internal/pkg/model"

	"github.com/gin-gonic/gin"
)

// MembershipController 会员控制器
type MembershipController struct {
	b biz.IBiz
}

// NewMembershipController 创建会员控制器实例
func NewMembershipController(b biz.IBiz) *MembershipController {
	return &MembershipController{b: b}
}

// CreateMembershipPayment 创建会员购买支付
func (mc *MembershipController) CreateMembershipPayment(c *gin.Context) {
	var req struct {
		MembershipType string `json:"membership_type" binding:"required,oneof=monthly yearly package"`
		PackageCount   int    `json:"package_count,omitempty"` // 仅当membership_type为package时使用
		PayMethod      string `json:"pay_method" binding:"required,oneof=native miniprogram jsapi"`
		OpenID         string `json:"openid,omitempty"` // 小程序支付必填
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

	// 验证包次数参数
	if req.MembershipType == model.MembershipTypePackage && req.PackageCount <= 0 {
		core.WriteResponse(c, errno.ErrBind.SetMessage("包次数必须大于0"), nil)
		return
	}

	// 生成订单号
	outTradeNo := fmt.Sprintf("membership_%d_%d", userID.ID, time.Now().UnixNano())

	// 根据会员类型设置金额和描述
	var amount int64
	var description string

	switch req.MembershipType {
	case model.MembershipTypeMonthly:
		amount = 2800 // 28元，单位分
		description = "月度会员"
	case model.MembershipTypeYearly:
		amount = 19800 // 198元，单位分
		description = "年度会员"
	case model.MembershipTypePackage:
		// 根据包次数计算价格，使用定价表
		amount = mc.calculatePackagePrice(req.PackageCount)
		description = fmt.Sprintf("资源包会员（%d次）", req.PackageCount)
	}

	// 创建支付请求
	paymentReq := &model.CreatePaymentRequest{
		OutTradeNo:     outTradeNo,
		Description:    description,
		Amount:         amount,
		OpenID:         req.OpenID,
		PayMethod:      req.PayMethod,
		MembershipType: req.MembershipType,
		PackageCount:   req.PackageCount,
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
	// 这里需要调用微信支付API，暂时返回模拟数据
	core.WriteResponse(c, nil, gin.H{
		"out_trade_no": paymentResp.OutTradeNo,
		"code_url":     "weixin://wxpay/bizpayurl?pr=example",
		"message":      "请使用微信扫描二维码完成支付",
	})
}

// createMiniProgramPayment 创建小程序支付
func (mc *MembershipController) createMiniProgramPayment(c *gin.Context, req model.CreatePaymentRequest, paymentResp *model.CreatePaymentResponse) {
	if req.OpenID == "" {
		core.WriteResponse(c, errno.ErrBind.SetMessage("小程序支付必须提供OpenID"), nil)
		return
	}

	// 这里需要调用微信小程序支付API，暂时返回模拟数据
	core.WriteResponse(c, nil, gin.H{
		"out_trade_no": paymentResp.OutTradeNo,
		"prepay_id":    "wx123456789",
		"pay_sign":     "example_sign",
		"time_stamp":   "1234567890",
		"nonce_str":    "example_nonce",
		"package":      "prepay_id=wx123456789",
		"sign_type":    "RSA",
		"message":      "请调用微信支付完成支付",
	})
}

// createJSAPIPayment 创建JSAPI支付
func (mc *MembershipController) createJSAPIPayment(c *gin.Context, req model.CreatePaymentRequest, paymentResp *model.CreatePaymentResponse) {
	if req.OpenID == "" {
		core.WriteResponse(c, errno.ErrBind.SetMessage("JSAPI支付必须提供OpenID"), nil)
		return
	}

	// 这里需要调用微信JSAPI支付API，暂时返回模拟数据
	core.WriteResponse(c, nil, gin.H{
		"out_trade_no": paymentResp.OutTradeNo,
		"prepay_id":    "wx123456789",
		"pay_sign":     "example_sign",
		"time_stamp":   "1234567890",
		"nonce_str":    "example_nonce",
		"package":      "prepay_id=wx123456789",
		"sign_type":    "RSA",
		"message":      "请调用微信支付完成支付",
	})
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
	if user.MembershipType == model.MembershipTypePackage {
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

	// 返回会员信息
	core.WriteResponse(c, nil, gin.H{
		"membership_type":    user.MembershipType,
		"membership_expires": user.MembershipExpires,
		"package_count":      user.PackageCount,
		"package_info":       packageInfo,
		"is_pro":             user.IsPro,
		"membership_status":  user.GetMembershipStatus(),
		"is_active":          user.IsMembershipActive(),
	})
}

// GetMembershipPlans 获取会员套餐信息
func (mc *MembershipController) GetMembershipPlans(c *gin.Context) {
	plans := []gin.H{
		{
			"type":        "monthly",
			"name":        "月度会员",
			"price":       2800, // 28元，单位分
			"description": "享受月度会员权益",
			"features":    []string{"30次/月卡册创建", "无水印", "解锁全部模板", "高峰期优先处理"},
		},
		{
			"type":        "yearly",
			"name":        "年度会员",
			"price":       19800, // 198元，单位分
			"description": "享受年度会员权益，约16.5元/月，立省40%",
			"features":    []string{"30次/月卡册创建", "无水印", "解锁全部模板", "高峰期优先处理", "年度优惠价格"},
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
	// 检查会员状态
	if user.IsMembershipActive() {
		// 会员有效，可以创建
		return &CreatePermission{
			CanCreate: true,
			Reason:    "会员有效，可以创建卡册",
		}
	}

	// 检查包次数
	if user.MembershipType == model.MembershipTypePackage && user.PackageCount > 0 {
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
