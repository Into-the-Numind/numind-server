package pay

import (
	"context"
	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"time"

	"numind-server/internal/numind/biz/wechat"

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
		core.WriteResponse(c, errno.InternalServerError.SetMessage("获取证书状态失败: "+err.Error()), nil)
		return
	}

	// 检查证书健康状态
	certInfo, err := certManager.CheckCertificateHealth()
	if err != nil {
		core.WriteResponse(c, errno.InternalServerError.SetMessage("证书健康检查失败: "+err.Error()), map[string]interface{}{
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
		core.WriteResponse(c, errno.ErrBind.SetMessage("参数错误: "+err.Error()), nil)
		return
	}
	cfg := getWechatPayConfig()
	resp, err := wechat.CreateNativeOrder(cfg, req.OutTradeNo, req.Description, req.Amount)
	if err != nil {
		core.WriteResponse(c, errno.InternalServerError.SetMessage(err.Error()), nil)
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
		core.WriteResponse(c, errno.ErrBind.SetMessage("参数错误: "+err.Error()), nil)
		return
	}
	cfg := getWechatPayConfig()
	resp, err := wechat.CreateMiniProgramOrder(cfg, req.OutTradeNo, req.Description, req.Amount, req.OpenID)
	if err != nil {
		core.WriteResponse(c, errno.InternalServerError.SetMessage(err.Error()), nil)
		return
	}
	core.WriteResponse(c, nil, resp)
}

// 支付回调
func WechatPayNotify(c *gin.Context) {
	cfg := getWechatPayConfig()
	transaction, err := wechat.ParsePayNotify(cfg, c.Request.Context(), c.Request)
	if err != nil {
		core.WriteResponse(c, errno.InternalServerError.SetMessage("回调解析失败: "+err.Error()), nil)
		return
	}

	// 从微信回调中提取订单号
	// 注意：这里需要根据实际的微信支付回调格式来提取 out_trade_no
	// 由于微信支付回调的格式可能因配置而异，这里提供一个通用的处理方式

	// 如果回调解析成功，说明支付已经成功
	// 我们需要调用订单的回调接口来处理订单状态更新
	// 这里可以通过 HTTP 请求调用订单回调接口，或者通过其他方式

	// TODO: 实现订单状态更新逻辑
	// 方案1: 通过 HTTP 请求调用订单回调接口
	// 方案2: 通过消息队列通知订单服务
	// 方案3: 直接调用订单业务层（需要重构架构）

	core.WriteResponse(c, nil, gin.H{"code": "SUCCESS", "message": "成功", "transaction": transaction})
}
