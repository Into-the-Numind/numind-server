package pay

import (
	"context"
	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"
	"time"

	"numind-server/internal/numind/biz"
	"numind-server/internal/numind/biz/wechat"
	"numind-server/internal/numind/store"

	"github.com/gin-gonic/gin"
	"github.com/spf13/viper"
)

// 获取微信支付配置（用viper）
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

// 检查微信支付证书状态
func WechatCertificateStatus(c *gin.Context) {
	cfg := getWechatPayConfig()

	// 创建证书管理器
	certManager := wechat.NewCertificateManager(
		cfg["wechatpay_cert_path"],
		cfg["mch_private_key_path"],
		cfg["mch_cert_serial_no"],
	)

	// 获取证书状态
	status, err := certManager.GetCertificateStatus()
	if err != nil {
		core.WriteResponse(c, errno.InternalServerError.SetMessage("获取证书状态失败: %s", err.Error()), nil)
		return
	}

	// 检查证书健康状态
	certInfo, err := certManager.CheckCertificateHealth()
	if err != nil {
		core.WriteResponse(c, errno.InternalServerError.SetMessage("证书健康检查失败: %s", err.Error()), map[string]interface{}{
			"status": status,
			"error":  err.Error(),
		})
		return
	}

	// 返回证书状态信息
	response := map[string]interface{}{
		"status":         status,
		"serial_number":  certInfo.SerialNumber,
		"valid_from":     certInfo.ValidFrom.Format("2006-01-02"),
		"valid_to":       certInfo.ValidTo.Format("2006-01-02"),
		"days_to_expire": certInfo.DaysToExpire,
		"is_expired":     certInfo.IsExpired,
		"is_healthy":     !certInfo.IsExpired && certInfo.DaysToExpire > 30,
	}

	core.WriteResponse(c, nil, response)
}

// 启动证书监控
func StartCertificateMonitoring(c *gin.Context) {
	cfg := getWechatPayConfig()

	// 创建证书管理器
	certManager := wechat.NewCertificateManager(
		cfg["wechatpay_cert_path"],
		cfg["mch_private_key_path"],
		cfg["mch_cert_serial_no"],
	)

	// 启动监控（每小时检查一次）
	ctx := context.Background()
	go certManager.MonitorCertificate(ctx, time.Hour)

	core.WriteResponse(c, nil, map[string]interface{}{
		"message": "证书监控已启动，每小时检查一次",
	})
}

// Native下单
func WechatNativePay(c *gin.Context) {
	var req struct {
		OutTradeNo  string `json:"out_trade_no" binding:"required"`
		Description string `json:"description" binding:"required"`
		Amount      int64  `json:"amount" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("参数错误: %s", err.Error()), nil)
		return
	}
	cfg := getWechatPayConfig()
	resp, err := wechat.CreateNativeOrder(cfg, req.OutTradeNo, req.Description, req.Amount)
	if err != nil {
		core.WriteResponse(c, errno.InternalServerError.SetMessage("%s", err.Error()), nil)
		return
	}
	core.WriteResponse(c, nil, resp)
}

// 小程序支付下单
func WechatMiniProgramPay(c *gin.Context) {
	var req struct {
		OutTradeNo  string `json:"out_trade_no" binding:"required"`
		Description string `json:"description" binding:"required"`
		Amount      int64  `json:"amount" binding:"required"`
		OpenID      string `json:"openid" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("参数错误: %s", err.Error()), nil)
		return
	}
	cfg := getWechatPayConfig()
	resp, err := wechat.CreateMiniProgramOrder(cfg, req.OutTradeNo, req.Description, req.Amount, req.OpenID)
	if err != nil {
		core.WriteResponse(c, errno.InternalServerError.SetMessage("%s", err.Error()), nil)
		return
	}
	core.WriteResponse(c, nil, resp)
}

// 支付回调
func WechatPayNotify(c *gin.Context) {
	// 记录回调请求开始
	log.C(c).Infow("WechatPayNotify callback received",
		"method", c.Request.Method,
		"url", c.Request.URL.String(),
		"remote_addr", c.ClientIP(),
		"user_agent", c.Request.UserAgent(),
		"content_type", c.ContentType())

	cfg := getWechatPayConfig()
	transaction, err := wechat.ParsePayNotify(cfg, c.Request.Context(), c.Request)
	if err != nil {
		log.C(c).Errorw("Failed to parse wechat pay notify",
			"error", err.Error(),
			"remote_addr", c.ClientIP())
		core.WriteResponse(c, errno.InternalServerError.SetMessage("回调解析失败: %s", err.Error()), nil)
		return
	}

	log.C(c).Infow("Wechat pay notify parsed successfully")

	// 从微信支付回调中提取订单信息
	trans := transaction

	// 记录交易状态
	if trans.TradeState != nil {
		log.C(c).Infow("Trade state received", "trade_state", *trans.TradeState)
	} else {
		log.C(c).Warnw("Trade state is nil")
	}

	// 检查交易状态
	if trans.TradeState == nil || *trans.TradeState != "SUCCESS" {
		tradeState := "nil"
		if trans.TradeState != nil {
			tradeState = *trans.TradeState
		}
		outTradeNoForLog := ""
		if trans.OutTradeNo != nil {
			outTradeNoForLog = *trans.OutTradeNo
		}
		log.C(c).Warnw("Payment not successful",
			"trade_state", tradeState,
			"out_trade_no", outTradeNoForLog)
		core.WriteResponse(c, errno.InternalServerError.SetMessage("支付未成功"), nil)
		return
	}

	// 提取订单信息
	var outTradeNo, transactionID string
	var paidAt *time.Time

	if trans.OutTradeNo != nil {
		outTradeNo = *trans.OutTradeNo
		log.C(c).Infow("Out trade no extracted", "out_trade_no", outTradeNo)
	} else {
		log.C(c).Warnw("Out trade no is nil")
	}

	if trans.TransactionId != nil {
		transactionID = *trans.TransactionId
		log.C(c).Infow("Transaction ID extracted", "transaction_id", transactionID)
	} else {
		log.C(c).Warnw("Transaction ID is nil")
	}

	if trans.SuccessTime != nil {
		// 解析微信支付的时间格式 "2023-12-01T12:00:00+08:00"
		if t, err := time.Parse(time.RFC3339, *trans.SuccessTime); err == nil {
			paidAt = &t
			log.C(c).Infow("Success time parsed", "paid_at", paidAt)
		} else {
			log.C(c).Warnw("Failed to parse success time",
				"success_time", *trans.SuccessTime,
				"error", err.Error())
		}
	} else {
		log.C(c).Warnw("Success time is nil")
	}

	// 记录金额信息（如果有）
	if trans.Amount != nil && trans.Amount.Total != nil {
		currency := ""
		if trans.Amount.Currency != nil {
			currency = *trans.Amount.Currency
		}
		log.C(c).Infow("Payment amount",
			"total", *trans.Amount.Total,
			"currency", currency)
	}

	// 验证必要字段
	if outTradeNo == "" {
		log.C(c).Errorw("Out trade no is empty, cannot process payment")
		core.WriteResponse(c, errno.InternalServerError.SetMessage("订单号为空"), nil)
		return
	}

	// 创建 biz 实例来处理支付状态更新
	b := biz.NewBiz(store.S)
	paymentBiz := b.Payments()

	// 更新支付状态
	log.C(c).Infow("Starting to update payment status",
		"out_trade_no", outTradeNo,
		"status", model.PaymentStatusSuccess,
		"transaction_id", transactionID)

	if err := paymentBiz.UpdatePaymentStatus(c, outTradeNo, model.PaymentStatusSuccess, transactionID, paidAt); err != nil {
		log.C(c).Errorw("Failed to update payment status",
			"error", err.Error(),
			"out_trade_no", outTradeNo,
			"transaction_id", transactionID)
		core.WriteResponse(c, errno.InternalServerError.SetMessage("更新支付状态失败: %s", err.Error()), nil)
		return
	}

	log.C(c).Infow("Payment status updated successfully",
		"out_trade_no", outTradeNo,
		"transaction_id", transactionID)

	core.WriteResponse(c, nil, gin.H{"code": "SUCCESS", "message": "成功"})
}
