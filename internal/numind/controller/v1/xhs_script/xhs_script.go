package xhs_script

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	paymentbiz "numind-server/internal/numind/biz/payment"
	xhsscriptbiz "numind-server/internal/numind/biz/xhsscript"
	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/model"
)

type Controller struct {
	biz        *xhsscriptbiz.Service
	paymentBiz paymentbiz.IPaymentBiz
}

func NewController(biz *xhsscriptbiz.Service, paymentBiz paymentbiz.IPaymentBiz) *Controller {
	return &Controller{biz: biz, paymentBiz: paymentBiz}
}

type trialRequest struct {
	AnonymousID string `json:"anonymous_id"`
}

type authRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type profileRequest struct {
	ProfileText string `json:"profile_text"`
}

type notesRequest struct {
	Notes []xhsscriptbiz.CapturePayload `json:"notes"`
}

type analyticsRequest struct {
	Events []xhsscriptbiz.AnalyticsEventInput `json:"events"`
}

type orderRequest struct {
	Quantity   int    `json:"quantity"`
	PayChannel string `json:"pay_channel"`
}

func (ctl *Controller) Trial(c *gin.Context) {
	var req trialRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("%s", err.Error()), nil)
		return
	}
	session, err := ctl.biz.EnsureTrial(c.Request.Context(), req.AnonymousID)
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}
	setSessionCookie(c, session.AccessToken, session.ExpiresAt)
	core.WriteResponse(c, nil, session)
}

func (ctl *Controller) Register(c *gin.Context) {
	var req authRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("%s", err.Error()), nil)
		return
	}
	current, _ := ctl.optionalCurrentUser(c, false)
	session, err := ctl.biz.Register(c.Request.Context(), current, req.Username, req.Password)
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}
	setSessionCookie(c, session.AccessToken, session.ExpiresAt)
	core.WriteResponse(c, nil, session)
}

func (ctl *Controller) Login(c *gin.Context) {
	var req authRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("%s", err.Error()), nil)
		return
	}
	session, err := ctl.biz.Login(c.Request.Context(), req.Username, req.Password)
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}
	setSessionCookie(c, session.AccessToken, session.ExpiresAt)
	core.WriteResponse(c, nil, session)
}

func (ctl *Controller) Logout(c *gin.Context) {
	clearSessionCookie(c)
	core.WriteResponse(c, nil, gin.H{"ok": true})
}

func (ctl *Controller) Me(c *gin.Context) {
	user, ok := ctl.requireCurrentUser(c, false)
	if !ok {
		return
	}
	home, err := ctl.biz.GetHome(c.Request.Context(), user)
	core.WriteResponse(c, err, home)
}

func (ctl *Controller) SaveProfile(c *gin.Context) {
	user, ok := ctl.requireCurrentUser(c, false)
	if !ok {
		return
	}
	var req profileRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("%s", err.Error()), nil)
		return
	}
	dto, err := ctl.biz.SaveProfile(c.Request.Context(), user.ID, req.ProfileText)
	core.WriteResponse(c, err, dto)
}

func (ctl *Controller) ExtToken(c *gin.Context) {
	user, ok := ctl.requireCurrentUser(c, false)
	if !ok {
		return
	}
	token, expiresAt, err := ctl.biz.IssueExtToken(c.Request.Context(), user.ID)
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}
	core.WriteResponse(c, nil, gin.H{
		"token":      token,
		"expires_at": expiresAt.Format(time.RFC3339),
	})
}

func (ctl *Controller) Ingest(c *gin.Context) {
	user, ok := ctl.requireCurrentUser(c, true)
	if !ok {
		return
	}
	var req notesRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("%s", err.Error()), nil)
		return
	}
	notes, err := ctl.biz.IngestNotes(c.Request.Context(), user.ID, req.Notes)
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}
	ids := make([]uint64, 0, len(notes))
	for _, note := range notes {
		ids = append(ids, note.ID)
	}
	core.WriteResponse(c, nil, gin.H{
		"ingested": len(notes),
		"ids":      ids,
		"notes":    notes,
	})
}

func (ctl *Controller) ListNotes(c *gin.Context) {
	user, ok := ctl.requireCurrentUser(c, false)
	if !ok {
		return
	}
	limit, offset, err := parseListNotesPagination(c)
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}
	notes, err := ctl.biz.ListNoteDTOs(c.Request.Context(), user.ID, limit, offset)
	core.WriteResponse(c, err, notes)
}

func (ctl *Controller) GetNote(c *gin.Context) {
	user, ok := ctl.requireCurrentUser(c, false)
	if !ok {
		return
	}
	id, err := parseID(c.Param("id"))
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}
	dto, err := ctl.biz.GetNoteDTO(c.Request.Context(), user.ID, id)
	core.WriteResponse(c, err, dto)
}

func (ctl *Controller) GetQuota(c *gin.Context) {
	user, ok := ctl.requireCurrentUser(c, false)
	if !ok {
		return
	}
	quota, err := ctl.biz.GetQuota(c.Request.Context(), user.ID)
	core.WriteResponse(c, err, quota)
}

func (ctl *Controller) Generate(c *gin.Context) {
	user, ok := ctl.requireCurrentUser(c, false)
	if !ok {
		return
	}
	id, err := parseID(c.Param("id"))
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}
	dto, err := ctl.biz.GenerateScript(c.Request.Context(), user.ID, id)
	core.WriteResponse(c, err, dto)
}

func (ctl *Controller) TrackEvents(c *gin.Context) {
	var req analyticsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("%s", err.Error()), nil)
		return
	}
	var userID *uint
	if user, err := ctl.optionalCurrentUser(c, false); err == nil && user != nil {
		userID = &user.ID
	}
	err := ctl.biz.TrackEvents(c.Request.Context(), userID, req.Events)
	core.WriteResponse(c, err, gin.H{"accepted": len(req.Events)})
}

func (ctl *Controller) AdminAnalytics(c *gin.Context) {
	days := 30
	if raw := strings.TrimSpace(c.Query("days")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			core.WriteResponse(c, errno.ErrBind.SetMessage("invalid days: %s", raw), nil)
			return
		}
		days = parsed
	}
	summary, err := ctl.biz.GetAnalyticsSummary(c.Request.Context(), days)
	core.WriteResponse(c, err, summary)
}

func (ctl *Controller) CreateOrder(c *gin.Context) {
	user, ok := ctl.requireCurrentUser(c, false)
	if !ok {
		return
	}
	var req orderRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("%s", err.Error()), nil)
		return
	}
	quantity := req.Quantity
	if quantity == 0 {
		quantity = 1
	}
	payChannel := strings.TrimSpace(req.PayChannel)
	if payChannel == "" {
		payChannel = model.PayChannelWechat
	}
	idempotencyKey := strings.TrimSpace(c.GetHeader("Idempotency-Key"))
	order, err := ctl.paymentBiz.CreateOrder(
		c.Request.Context(),
		user.ID,
		user.ID,
		model.ProductTypeXhsScriptPack,
		quantity,
		payChannel,
		idempotencyKey,
	)
	if err == nil && order != nil {
		ctl.biz.RecordEventWithIDBestEffort(c.Request.Context(), user.ID, "backend:xhs_script:order_created:"+order.OrderNo, "order_created", map[string]interface{}{
			"order_id":     order.ID,
			"order_no":     order.OrderNo,
			"amount_cents": order.Amount,
			"quantity":     order.Quantity,
			"channel":      order.PayChannel,
			"product_type": order.ProductType,
			"pay_status":   order.PayStatus,
		})
	}
	core.WriteResponse(c, err, order)
}

func (ctl *Controller) GetOrderStatus(c *gin.Context) {
	user, ok := ctl.requireCurrentUser(c, false)
	if !ok {
		return
	}
	id, err := parseID(c.Param("id"))
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}
	order, err := ctl.paymentBiz.GetOrder(c.Request.Context(), id)
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}
	if order.UserID != user.ID && order.PayerID != user.ID {
		core.WriteResponse(c, errno.ErrForbidden.SetMessage("无权查看该订单"), nil)
		return
	}
	core.WriteResponse(c, nil, gin.H{
		"id":         order.ID,
		"order_no":   order.OrderNo,
		"pay_status": order.PayStatus,
		"amount":     order.Amount,
		"code_url":   order.CodeURL,
		"paid_at":    order.PaidAt,
		"expired_at": order.ExpiredAt,
	})
}

func (ctl *Controller) requireCurrentUser(c *gin.Context, allowExtToken bool) (*model.User, bool) {
	user, err := ctl.optionalCurrentUser(c, allowExtToken)
	if err != nil {
		core.WriteResponse(c, err, nil)
		return nil, false
	}
	if user == nil {
		core.WriteResponse(c, errno.ErrTokenInvalid.SetMessage("请先登录或领取试用次数"), nil)
		return nil, false
	}
	return user, true
}

func (ctl *Controller) optionalCurrentUser(c *gin.Context, allowExtToken bool) (*model.User, error) {
	token := tokenFromRequest(c)
	if token == "" {
		return nil, nil
	}
	return ctl.biz.Authenticate(c.Request.Context(), token, allowExtToken)
}

func tokenFromRequest(c *gin.Context) string {
	authHeader := strings.TrimSpace(c.GetHeader("Authorization"))
	if authHeader != "" {
		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
			return strings.TrimSpace(parts[1])
		}
	}
	if cookie, err := c.Cookie(xhsscriptbiz.SessionCookieName); err == nil {
		return strings.TrimSpace(cookie)
	}
	return ""
}

func setSessionCookie(c *gin.Context, token string, expiresAt time.Time) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     xhsscriptbiz.SessionCookieName,
		Value:    token,
		Path:     "/",
		Expires:  expiresAt,
		MaxAge:   int(time.Until(expiresAt).Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   isSecureRequest(c),
	})
}

func clearSessionCookie(c *gin.Context) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     xhsscriptbiz.SessionCookieName,
		Value:    "",
		Path:     "/",
		Expires:  time.Unix(0, 0),
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   isSecureRequest(c),
	})
}

func isSecureRequest(c *gin.Context) bool {
	return c.Request.TLS != nil || strings.EqualFold(c.GetHeader("X-Forwarded-Proto"), "https")
}

func parseID(raw string) (uint64, error) {
	id, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || id == 0 {
		return 0, errno.ErrBind.SetMessage("invalid id: %s", raw)
	}
	return id, nil
}

func parseListNotesPagination(c *gin.Context) (int, int, error) {
	limit, err := parsePositiveIntQuery(c, "limit", 40)
	if err != nil {
		return 0, 0, err
	}
	if limit > 100 {
		limit = 100
	}
	offset, err := parseNonNegativeIntQuery(c, "offset", 0)
	if err != nil {
		return 0, 0, err
	}
	return limit, offset, nil
}

func parsePositiveIntQuery(c *gin.Context, name string, defaultValue int) (int, error) {
	raw := strings.TrimSpace(c.Query(name))
	if raw == "" {
		return defaultValue, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return 0, errno.ErrBind.SetMessage("invalid %s: %s", name, raw)
	}
	return value, nil
}

func parseNonNegativeIntQuery(c *gin.Context, name string, defaultValue int) (int, error) {
	raw := strings.TrimSpace(c.Query(name))
	if raw == "" {
		return defaultValue, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 {
		return 0, errno.ErrBind.SetMessage("invalid %s: %s", name, raw)
	}
	return value, nil
}
