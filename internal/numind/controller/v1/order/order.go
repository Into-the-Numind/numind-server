package order

import (
	"fmt"
	orderbiz "numind-server/internal/numind/biz/order"
	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/model"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/spf13/viper"
)

type OrderController struct {
	b orderbiz.OrderBiz
}

func New(b orderbiz.OrderBiz) *OrderController {
	return &OrderController{b: b}
}

// getUserIDFromToken 从JWT token中获取用户ID
func getUserIDFromToken(c *gin.Context) (uint, error) {
	header := c.Request.Header.Get("Authorization")
	if len(header) == 0 {
		return 0, fmt.Errorf("missing authorization header")
	}

	var tokenString string
	fmt.Sscanf(header, "Bearer %s", &tokenString)

	// 使用viper获取JWT密钥
	jwtSecret := viper.GetString("jwt.secret")
	if jwtSecret == "" {
		return 0, fmt.Errorf("jwt secret not configured")
	}

	token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return []byte(jwtSecret), nil
	})

	if err != nil {
		return 0, err
	}

	if claims, ok := token.Claims.(jwt.MapClaims); ok && token.Valid {
		if userID, exists := claims["user_id"]; exists {
			return uint(userID.(float64)), nil
		}
	}

	return 0, fmt.Errorf("invalid token or missing user_id")
}

// 下单接口
func (ctrl *OrderController) Create(c *gin.Context) {
	var req struct {
		Amount      int64  `json:"amount" binding:"required"`
		Description string `json:"description" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("参数错误: "+err.Error()), nil)
		return
	}
	userID, err := getUserIDFromToken(c)
	if err != nil {
		core.WriteResponse(c, errno.ErrUnauthorized.SetMessage("用户未登录"), nil)
		return
	}
	outTradeNo := fmt.Sprintf("wx_%d_%d", userID, time.Now().UnixNano())
	order := &model.Order{
		UserID:      userID,
		OutTradeNo:  outTradeNo,
		Amount:      req.Amount,
		Description: req.Description,
		Status:      "pending",
	}
	if err := ctrl.b.Create(c, order); err != nil {
		core.WriteResponse(c, errno.InternalServerError.SetMessage("创建订单失败: "+err.Error()), nil)
		return
	}
	core.WriteResponse(c, nil, gin.H{
		"order_id":     order.ID,
		"out_trade_no": outTradeNo,
	})
}

// 微信支付回调
func (ctrl *OrderController) WechatNotify(c *gin.Context) {
	outTradeNo := c.PostForm("out_trade_no") // 实际应从微信回调体解析

	// 使用新的支付成功处理方法
	if err := ctrl.b.HandlePaymentSuccess(c, outTradeNo); err != nil {
		core.WriteResponse(c, errno.InternalServerError.SetMessage("处理支付成功失败: "+err.Error()), nil)
		return
	}

	core.WriteResponse(c, nil, gin.H{"code": "SUCCESS", "message": "成功"})
}

// 用户账单查询
func (ctrl *OrderController) ListByUser(c *gin.Context) {
	userID, err := getUserIDFromToken(c)
	if err != nil {
		core.WriteResponse(c, errno.ErrUnauthorized.SetMessage("用户未登录"), nil)
		return
	}
	orders, err := ctrl.b.ListByUser(c, userID, 0, 20)
	if err != nil {
		core.WriteResponse(c, errno.InternalServerError.SetMessage("查询失败: "+err.Error()), nil)
		return
	}
	core.WriteResponse(c, nil, orders)
}
