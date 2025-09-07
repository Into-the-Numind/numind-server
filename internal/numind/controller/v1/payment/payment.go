package payment

import (
	"time"

	"numind-server/internal/numind/biz"
	"numind-server/internal/numind/biz/wechat"
	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/middleware"
	"numind-server/internal/pkg/model"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/spf13/viper"
)

// PaymentController 支付控制器
type PaymentController struct {
	b biz.IBiz
}

// NewPaymentController 创建支付控制器实例
func NewPaymentController(b biz.IBiz) *PaymentController {
	return &PaymentController{b: b}
}

// CreatePayment 创建支付
func (pc *PaymentController) CreatePayment(c *gin.Context) {
	var req model.CreatePaymentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("参数错误: "+err.Error()), nil)
		return
	}

	// 获取当前用户ID
	userID := middleware.GetCurrentUser(c)
	if userID == nil {
		core.WriteResponse(c, errno.ErrUnauthorized.SetMessage("用户未登录"), nil)
		return
	}

	// 创建支付记录
	paymentResp, err := pc.b.Payments().CreatePayment(c, &req, userID.ID)
	if err != nil {
		core.WriteResponse(c, errno.InternalServerError.SetMessage("创建支付失败: "+err.Error()), nil)
		return
	}

	// 根据支付方式调用对应的支付接口
	switch req.PayMethod {
	case model.PaymentMethodNative:
		pc.createNativePayment(c, req, paymentResp)
		return
	case model.PaymentMethodMiniProgram:
		pc.createMiniProgramPayment(c, req, paymentResp)
		return
	case model.PaymentMethodJSAPI:
		pc.createJSAPIPayment(c, req, paymentResp)
		return
	default:
		core.WriteResponse(c, errno.ErrBind.SetMessage("不支持的支付方式"), nil)
		return
	}
}

// createNativePayment 创建扫码支付
func (pc *PaymentController) createNativePayment(c *gin.Context, req model.CreatePaymentRequest, paymentResp *model.CreatePaymentResponse) {
	cfg := getWechatPayConfig()
	resp, err := wechat.CreateNativeOrder(cfg, req.OutTradeNo, req.Description, req.Amount)
	if err != nil {
		// 支付创建失败，更新支付状态为失败
		pc.b.Payments().UpdatePaymentStatus(c, req.OutTradeNo, model.PaymentStatusFailed, "", nil)
		core.WriteResponse(c, errno.InternalServerError.SetMessage("创建微信支付失败: "+err.Error()), nil)
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

	core.WriteResponse(c, nil, paymentResp)
}

// createMiniProgramPayment 创建小程序支付
func (pc *PaymentController) createMiniProgramPayment(c *gin.Context, req model.CreatePaymentRequest, paymentResp *model.CreatePaymentResponse) {
	if req.OpenID == "" {
		core.WriteResponse(c, errno.ErrBind.SetMessage("小程序支付必须提供OpenID"), nil)
		return
	}

	cfg := getWechatPayConfig()
	resp, err := wechat.CreateMiniProgramOrder(cfg, req.OutTradeNo, req.Description, req.Amount, req.OpenID)
	if err != nil {
		// 支付创建失败，更新支付状态为失败
		pc.b.Payments().UpdatePaymentStatus(c, req.OutTradeNo, model.PaymentStatusFailed, "", nil)
		core.WriteResponse(c, errno.InternalServerError.SetMessage("创建小程序支付失败: "+err.Error()), nil)
		return
	}

	// 更新支付记录中的预支付ID等信息
	if respMap, ok := resp.(map[string]interface{}); ok {
		if prepayID, exists := respMap["prepay_id"]; exists {
			paymentResp.PrepayID = prepayID.(string)
		}
		if paySign, exists := respMap["pay_sign"]; exists {
			paymentResp.PaySign = paySign.(string)
		}
		if timeStamp, exists := respMap["time_stamp"]; exists {
			paymentResp.TimeStamp = timeStamp.(string)
		}
		if nonceStr, exists := respMap["nonce_str"]; exists {
			paymentResp.NonceStr = nonceStr.(string)
		}
		if packageStr, exists := respMap["package"]; exists {
			paymentResp.Package = packageStr.(string)
		}
		if signType, exists := respMap["sign_type"]; exists {
			paymentResp.SignType = signType.(string)
		}
	}

	core.WriteResponse(c, nil, paymentResp)
}

// createJSAPIPayment 创建JSAPI支付
func (pc *PaymentController) createJSAPIPayment(c *gin.Context, req model.CreatePaymentRequest, paymentResp *model.CreatePaymentResponse) {
	if req.OpenID == "" {
		core.WriteResponse(c, errno.ErrBind.SetMessage("JSAPI支付必须提供OpenID"), nil)
		return
	}

	// 这里可以调用JSAPI支付接口，暂时返回错误
	core.WriteResponse(c, errno.InternalServerError.SetMessage("JSAPI支付暂未实现"), nil)
}

// GetPayment 获取支付记录
func (pc *PaymentController) GetPayment(c *gin.Context) {
	outTradeNo := c.Param("out_trade_no")
	if outTradeNo == "" {
		core.WriteResponse(c, errno.ErrBind.SetMessage("订单号不能为空"), nil)
		return
	}

	payment, err := pc.b.Payments().GetPaymentByOutTradeNo(c, outTradeNo)
	if err != nil {
		core.WriteResponse(c, errno.ErrPageNotFound.SetMessage("支付记录不存在"), nil)
		return
	}

	// 检查权限：只能查看自己的支付记录
	userID := middleware.GetCurrentUser(c)
	if userID == nil {
		core.WriteResponse(c, errno.ErrUnauthorized.SetMessage("用户未登录"), nil)
		return
	}

	if payment.UserID != userID.ID {
		core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage("无权查看此支付记录"), nil)
		return
	}

	core.WriteResponse(c, nil, payment)
}

// ListPayments 获取支付记录列表
func (pc *PaymentController) ListPayments(c *gin.Context) {
	// 获取当前用户ID
	userID := middleware.GetCurrentUser(c)
	if userID == nil {
		core.WriteResponse(c, errno.ErrUnauthorized.SetMessage("用户未登录"), nil)
		return
	}

	// 获取查询参数
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	status := c.Query("status")
	channel := c.Query("channel")

	offset := (page - 1) * pageSize

	var payments []*model.PaymentM
	var total int64
	var err error

	if status != "" {
		payments, err = pc.b.Payments().ListPaymentsByStatus(c, status, offset, pageSize)
		if err != nil {
			core.WriteResponse(c, errno.InternalServerError.SetMessage("获取支付记录失败: "+err.Error()), nil)
			return
		}
		total, err = pc.b.Payments().CountPaymentsByStatus(c, status)
		if err != nil {
			core.WriteResponse(c, errno.InternalServerError.SetMessage("统计支付记录失败: "+err.Error()), nil)
			return
		}
	} else {
		payments, err = pc.b.Payments().ListPaymentsByUser(c, userID.ID, offset, pageSize)
		if err != nil {
			core.WriteResponse(c, errno.InternalServerError.SetMessage("获取支付记录失败: "+err.Error()), nil)
			return
		}
		total, err = pc.b.Payments().CountPaymentsByUser(c, userID.ID)
		if err != nil {
			core.WriteResponse(c, errno.InternalServerError.SetMessage("统计支付记录失败: "+err.Error()), nil)
			return
		}
	}

	// 过滤渠道
	if channel != "" {
		filteredPayments := make([]*model.PaymentM, 0)
		for _, payment := range payments {
			if payment.Channel == channel {
				filteredPayments = append(filteredPayments, payment)
			}
		}
		payments = filteredPayments
	}

	response := &model.PaymentListResponse{
		Total:    total,
		Payments: payments,
	}

	core.WriteResponse(c, nil, response)
}

// CancelPayment 取消支付
func (pc *PaymentController) CancelPayment(c *gin.Context) {
	outTradeNo := c.Param("out_trade_no")
	if outTradeNo == "" {
		core.WriteResponse(c, errno.ErrBind.SetMessage("订单号不能为空"), nil)
		return
	}

	// 获取当前用户ID
	userID := middleware.GetCurrentUser(c)
	if userID == nil {
		core.WriteResponse(c, errno.ErrUnauthorized.SetMessage("用户未登录"), nil)
		return
	}

	// 获取支付记录
	payment, err := pc.b.Payments().GetPaymentByOutTradeNo(c, outTradeNo)
	if err != nil {
		core.WriteResponse(c, errno.ErrPageNotFound.SetMessage("支付记录不存在"), nil)
		return
	}

	// 检查权限：只能取消自己的支付记录
	if payment.UserID != userID.ID {
		core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage("无权取消此支付记录"), nil)
		return
	}

	// 检查状态：只能取消待支付的记录
	if payment.Status != model.PaymentStatusPending {
		core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage("只能取消待支付的记录"), nil)
		return
	}

	// 更新状态为已取消
	if err := pc.b.Payments().UpdatePaymentStatus(c, outTradeNo, model.PaymentStatusCancelled, "", nil); err != nil {
		core.WriteResponse(c, errno.InternalServerError.SetMessage("取消支付失败: "+err.Error()), nil)
		return
	}

	core.WriteResponse(c, nil, gin.H{"message": "支付已取消"})
}

// WechatPayNotify 微信支付回调
func (pc *PaymentController) WechatPayNotify(c *gin.Context) {
	cfg := getWechatPayConfig()
	transaction, err := wechat.ParsePayNotify(cfg, c.Request.Context(), c.Request)
	if err != nil {
		core.WriteResponse(c, errno.InternalServerError.SetMessage("回调解析失败: "+err.Error()), nil)
		return
	}

	// 从微信支付回调中提取订单信息
	// 根据微信支付官方文档，Transaction结构包含以下字段：
	// - OutTradeNo: 商户订单号
	// - TransactionId: 微信支付订单号
	// - TradeState: 交易状态
	// - SuccessTime: 支付成功时间
	// - Amount: 订单金额信息

	// 直接使用Transaction对象
	trans := transaction

	// 检查交易状态
	if trans.TradeState == nil || *trans.TradeState != "SUCCESS" {
		core.WriteResponse(c, errno.InternalServerError.SetMessage("支付未成功"), nil)
		return
	}

	// 提取订单信息
	var outTradeNo, transactionID string
	var paidAt *time.Time

	if trans.OutTradeNo != nil {
		outTradeNo = *trans.OutTradeNo
	}
	if trans.TransactionId != nil {
		transactionID = *trans.TransactionId
	}
	if trans.SuccessTime != nil {
		// 解析微信支付的时间格式 "2023-12-01T12:00:00+08:00"
		if t, err := time.Parse(time.RFC3339, *trans.SuccessTime); err == nil {
			paidAt = &t
		}
	}

	// 验证必要字段
	if outTradeNo == "" {
		core.WriteResponse(c, errno.InternalServerError.SetMessage("订单号为空"), nil)
		return
	}

	// 更新支付状态
	if err := pc.b.Payments().UpdatePaymentStatus(c, outTradeNo, model.PaymentStatusSuccess, transactionID, paidAt); err != nil {
		core.WriteResponse(c, errno.InternalServerError.SetMessage("更新支付状态失败: "+err.Error()), nil)
		return
	}

	core.WriteResponse(c, nil, gin.H{"code": "SUCCESS", "message": "成功"})
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
