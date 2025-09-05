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
		core.WriteResponse(c, errno.ErrBind.SetMessage(fmt.Sprintf("参数错误: %s", err.Error())), nil)
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
		amount = 3000 // 30元，单位分
		description = "月度会员"
	case model.MembershipTypeYearly:
		amount = 30000 // 300元，单位分
		description = "年度会员"
	case model.MembershipTypePackage:
		amount = int64(req.PackageCount * 100) // 1元/次，单位分
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
		core.WriteResponse(c, errno.InternalServerError.SetMessage(fmt.Sprintf("创建支付失败: %s", err.Error())), nil)
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
		core.WriteResponse(c, errno.InternalServerError.SetMessage(fmt.Sprintf("获取用户信息失败: %s", err.Error())), nil)
		return
	}

	// 返回会员信息
	core.WriteResponse(c, nil, gin.H{
		"membership_type":    user.MembershipType,
		"membership_expires": user.MembershipExpires,
		"package_count":      user.PackageCount,
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
			"price":       3000, // 30元，单位分
			"description": "享受月度会员权益",
			"features":    []string{"无限次生成", "高级模板", "优先客服"},
		},
		{
			"type":        "yearly",
			"name":        "年度会员",
			"price":       30000, // 300元，单位分
			"description": "享受年度会员权益，更优惠",
			"features":    []string{"无限次生成", "高级模板", "优先客服", "专属功能"},
		},
		{
			"type":        "package",
			"name":        "资源包",
			"price":       100, // 1元/次，单位分
			"description": "按次购买，灵活使用",
			"features":    []string{"按需购买", "永久有效", "灵活使用"},
		},
	}

	core.WriteResponse(c, nil, gin.H{
		"plans": plans,
	})
}
