package pay

import (
	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"

	"numind-server/internal/numind/biz/wechat"

	"github.com/gin-gonic/gin"
	"github.com/spf13/viper"
)

// 获取微信支付配置（用viper）
func getWechatPayConfig() map[string]string {
	return map[string]string{
		"app_id":               viper.GetString("wechat.app_id"),
		"mch_id":               viper.GetString("wechat.mch_id"),
		"mch_cert_serial_no":   viper.GetString("wechat.mch_cert_serial_no"),
		"mch_api_v3_key":       viper.GetString("wechat.mch_api_v3_key"),
		"mch_private_key_path": viper.GetString("wechat.mch_private_key_path"),
		"wechatpay_cert_path":  viper.GetString("wechat.wechatpay_cert_path"),
		"notify_url":           viper.GetString("wechat.notify_url"),
	}
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

// 支付回调
func WechatPayNotify(c *gin.Context) {
	cfg := getWechatPayConfig()
	transaction, err := wechat.ParsePayNotify(cfg, c.Request.Context(), c.Request)
	if err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("回调解析失败: "+err.Error()), nil)
		return
	}
	// TODO: 处理订单状态
	core.WriteResponse(c, nil, gin.H{"code": "SUCCESS", "message": "成功", "transaction": transaction})
}
