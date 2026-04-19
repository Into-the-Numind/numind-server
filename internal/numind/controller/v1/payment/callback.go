package payment

import (
	"net/http"

	"github.com/gin-gonic/gin"

	paymentbiz "numind-server/internal/numind/biz/payment"
	"numind-server/internal/pkg/log"
)

// CallbackController 支付回调控制器（无需鉴权）
type CallbackController struct {
	paymentBiz paymentbiz.IPaymentBiz
}

// New 创建支付回调控制器实例
func New(paymentBiz paymentbiz.IPaymentBiz) *CallbackController {
	return &CallbackController{paymentBiz: paymentBiz}
}

// WechatNotify POST /v1/payment/wechat/notify — 微信支付异步回调
func (ctrl *CallbackController) WechatNotify(c *gin.Context) {
	if err := ctrl.paymentBiz.HandleWechatNotify(c, c.Request); err != nil {
		log.C(c).Errorw("WechatNotify failed", "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"code":    "FAIL",
			"message": "处理失败",
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"code":    "SUCCESS",
		"message": "OK",
	})
}

// AlipayNotify POST /v1/payment/alipay/notify — 支付宝异步回调
func (ctrl *CallbackController) AlipayNotify(c *gin.Context) {
	if err := ctrl.paymentBiz.HandleAlipayNotify(c, c.Request); err != nil {
		log.C(c).Errorw("AlipayNotify failed", "err", err)
		c.String(http.StatusInternalServerError, "fail")
		return
	}

	c.String(http.StatusOK, "success")
}
