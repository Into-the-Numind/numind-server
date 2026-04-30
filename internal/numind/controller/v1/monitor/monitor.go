package monitor

import (
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/robfig/cron/v3"

	"numind-server/internal/numind/biz/monitor"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/middleware"
	"numind-server/internal/pkg/model"
)

// MonitorController handles HTTP requests for the content monitor feature.
type MonitorController struct {
	monitorBiz monitor.IMonitorBiz
	store      store.IStore
}

// NewMonitorController creates a new MonitorController.
func NewMonitorController(monitorBiz monitor.IMonitorBiz, s store.IStore) *MonitorController {
	return &MonitorController{
		monitorBiz: monitorBiz,
		store:      s,
	}
}

// ---------- helpers ----------

// getUserID extracts the authenticated user ID from the gin context.
func getUserID(c *gin.Context) (uint, bool) {
	user := middleware.GetCurrentUser(c)
	if user == nil {
		core.WriteResponse(c, errno.ErrTokenInvalid, nil)
		return 0, false
	}
	return user.ID, true
}

// parseUintParam parses a path parameter as uint.
func parseUintParam(c *gin.Context, name string) (uint, bool) {
	v, err := strconv.ParseUint(c.Param(name), 10, 32)
	if err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("invalid %s", name), nil)
		return 0, false
	}
	return uint(v), true
}

// parsePagination extracts offset and limit from query string with defaults.
func parsePagination(c *gin.Context) (int, int) {
	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
	if limit > 100 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}
	return offset, limit
}

// cronParser is the standard 5-field cron parser (minute hour dom month dow).
var cronParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)

// ========== User endpoints ==========

// CheckPermission checks if the current user has the content monitor feature.
// GET /monitor/check-permission
func (ctrl *MonitorController) CheckPermission(c *gin.Context) {
	userID, ok := getUserID(c)
	if !ok {
		return
	}

	allowed, err := ctrl.monitorBiz.CheckPermission(c, userID)
	if err != nil {
		log.C(c).Errorw("CheckPermission failed", "user_id", userID, "err", err)
		core.WriteResponse(c, errno.InternalServerError.SetMessage("%s", err.Error()), nil)
		return
	}

	core.WriteResponse(c, nil, gin.H{"allowed": allowed})
}

// AddBlogger adds a new blogger to monitor.
// POST /monitor/bloggers  body: {"xhs_user_id": "..."}
func (ctrl *MonitorController) AddBlogger(c *gin.Context) {
	userID, ok := getUserID(c)
	if !ok {
		return
	}

	var req struct {
		XhsUserID string `json:"xhs_user_id" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("request body error: %s", err.Error()), nil)
		return
	}

	blogger, err := ctrl.monitorBiz.AddBlogger(c, userID, req.XhsUserID)
	if err != nil {
		log.C(c).Errorw("AddBlogger failed", "user_id", userID, "xhs_user_id", req.XhsUserID, "err", err)
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, blogger)
}

// ListBloggers returns paginated bloggers for the current user.
// GET /monitor/bloggers?offset=0&limit=20
func (ctrl *MonitorController) ListBloggers(c *gin.Context) {
	userID, ok := getUserID(c)
	if !ok {
		return
	}

	offset, limit := parsePagination(c)

	list, total, err := ctrl.monitorBiz.ListBloggers(c, userID, offset, limit)
	if err != nil {
		log.C(c).Errorw("ListBloggers failed", "user_id", userID, "err", err)
		core.WriteResponse(c, errno.InternalServerError.SetMessage("%s", err.Error()), nil)
		return
	}

	core.WriteResponse(c, nil, gin.H{"list": list, "total": total})
}

// GetBlogger returns a single blogger by ID.
// GET /monitor/bloggers/:id
func (ctrl *MonitorController) GetBlogger(c *gin.Context) {
	userID, ok := getUserID(c)
	if !ok {
		return
	}

	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}

	blogger, err := ctrl.monitorBiz.GetBlogger(c, userID, id)
	if err != nil {
		log.C(c).Errorw("GetBlogger failed", "user_id", userID, "id", id, "err", err)
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, blogger)
}

// UpdateBlogger updates a blogger's category and/or active status.
// PUT /monitor/bloggers/:id  body: {"category": "...", "is_active": true}
func (ctrl *MonitorController) UpdateBlogger(c *gin.Context) {
	userID, ok := getUserID(c)
	if !ok {
		return
	}

	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}

	var req struct {
		Category *string `json:"category"`
		IsActive *bool   `json:"is_active"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("request body error: %s", err.Error()), nil)
		return
	}

	if err := ctrl.monitorBiz.UpdateBlogger(c, userID, id, req.Category, req.IsActive); err != nil {
		log.C(c).Errorw("UpdateBlogger failed", "user_id", userID, "id", id, "err", err)
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, gin.H{"message": "ok"})
}

// DeleteBlogger soft-deletes a blogger.
// DELETE /monitor/bloggers/:id
func (ctrl *MonitorController) DeleteBlogger(c *gin.Context) {
	userID, ok := getUserID(c)
	if !ok {
		return
	}

	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}

	if err := ctrl.monitorBiz.DeleteBlogger(c, userID, id); err != nil {
		log.C(c).Errorw("DeleteBlogger failed", "user_id", userID, "id", id, "err", err)
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, gin.H{"message": "ok"})
}

// CheckBlogger triggers a crawl check for a single blogger.
// POST /monitor/bloggers/:id/check
func (ctrl *MonitorController) CheckBlogger(c *gin.Context) {
	userID, ok := getUserID(c)
	if !ok {
		return
	}

	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}

	if err := ctrl.monitorBiz.CheckBlogger(c, userID, id); err != nil {
		log.C(c).Errorw("CheckBlogger failed", "user_id", userID, "id", id, "err", err)
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, gin.H{"message": "ok"})
}

// CheckBatch triggers a crawl check for multiple bloggers.
// POST /monitor/check-batch  body: {"blogger_ids": [1,2,3]}
func (ctrl *MonitorController) CheckBatch(c *gin.Context) {
	userID, ok := getUserID(c)
	if !ok {
		return
	}

	var req struct {
		BloggerIDs []uint `json:"blogger_ids" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("request body error: %s", err.Error()), nil)
		return
	}

	if err := ctrl.monitorBiz.CheckBatch(c, userID, req.BloggerIDs); err != nil {
		log.C(c).Errorw("CheckBatch failed", "user_id", userID, "ids", req.BloggerIDs, "err", err)
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, gin.H{"message": "ok"})
}

// ListNotes returns paginated notes with optional filters.
// GET /monitor/notes?offset=0&limit=20&blogger_id=1&date_from=2026-01-01&date_to=2026-01-31&sort_by=likes desc
func (ctrl *MonitorController) ListNotes(c *gin.Context) {
	userID, ok := getUserID(c)
	if !ok {
		return
	}

	offset, limit := parsePagination(c)

	var bloggerID *uint
	if v := c.Query("blogger_id"); v != "" {
		if bid, err := strconv.ParseUint(v, 10, 32); err == nil {
			bidu := uint(bid)
			bloggerID = &bidu
		}
	}

	var dateFrom, dateTo *time.Time
	if v := c.Query("date_from"); v != "" {
		if t, err := time.Parse("2006-01-02", v); err == nil {
			dateFrom = &t
		}
	}
	if v := c.Query("date_to"); v != "" {
		if t, err := time.Parse("2006-01-02", v); err == nil {
			dateTo = &t
		}
	}

	sortBy := c.DefaultQuery("sort_by", "")

	list, total, err := ctrl.monitorBiz.ListNotes(c, userID, bloggerID, dateFrom, dateTo, sortBy, offset, limit)
	if err != nil {
		log.C(c).Errorw("ListNotes failed", "user_id", userID, "err", err)
		core.WriteResponse(c, errno.InternalServerError.SetMessage("%s", err.Error()), nil)
		return
	}

	core.WriteResponse(c, nil, gin.H{"list": list, "total": total})
}

// GetNote returns a single note by ID.
// GET /monitor/notes/:id
func (ctrl *MonitorController) GetNote(c *gin.Context) {
	userID, ok := getUserID(c)
	if !ok {
		return
	}

	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}

	note, err := ctrl.monitorBiz.GetNote(c, userID, id)
	if err != nil {
		log.C(c).Errorw("GetNote failed", "user_id", userID, "id", id, "err", err)
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, note)
}

// AnalyzeNote triggers AI analysis for a single note.
// POST /monitor/notes/:id/analyze
func (ctrl *MonitorController) AnalyzeNote(c *gin.Context) {
	userID, ok := getUserID(c)
	if !ok {
		return
	}

	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}

	if err := ctrl.monitorBiz.AnalyzeNote(c, userID, id); err != nil {
		log.C(c).Errorw("AnalyzeNote failed", "user_id", userID, "id", id, "err", err)
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, gin.H{"message": "ok"})
}

// ListBriefings returns paginated briefings.
// GET /monitor/briefings?offset=0&limit=20
func (ctrl *MonitorController) ListBriefings(c *gin.Context) {
	userID, ok := getUserID(c)
	if !ok {
		return
	}

	offset, limit := parsePagination(c)

	list, total, err := ctrl.monitorBiz.ListBriefings(c, userID, offset, limit)
	if err != nil {
		log.C(c).Errorw("ListBriefings failed", "user_id", userID, "err", err)
		core.WriteResponse(c, errno.InternalServerError.SetMessage("%s", err.Error()), nil)
		return
	}

	core.WriteResponse(c, nil, gin.H{"list": list, "total": total})
}

// GetBriefing returns a single briefing by ID.
// GET /monitor/briefings/:id
func (ctrl *MonitorController) GetBriefing(c *gin.Context) {
	userID, ok := getUserID(c)
	if !ok {
		return
	}

	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}

	briefing, err := ctrl.monitorBiz.GetBriefing(c, userID, id)
	if err != nil {
		log.C(c).Errorw("GetBriefing failed", "user_id", userID, "id", id, "err", err)
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, briefing)
}

// GenerateBriefing triggers briefing generation for the current user.
// POST /monitor/briefings/generate
func (ctrl *MonitorController) GenerateBriefing(c *gin.Context) {
	userID, ok := getUserID(c)
	if !ok {
		return
	}

	briefing, err := ctrl.monitorBiz.GenerateBriefing(c, userID)
	if err != nil {
		log.C(c).Errorw("GenerateBriefing failed", "user_id", userID, "err", err)
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, briefing)
}

// GetConfig returns the user's monitor configuration.
// GET /monitor/config
func (ctrl *MonitorController) GetConfig(c *gin.Context) {
	userID, ok := getUserID(c)
	if !ok {
		return
	}

	cfg, err := ctrl.monitorBiz.GetConfig(c, userID)
	if err != nil {
		log.C(c).Errorw("GetConfig failed", "user_id", userID, "err", err)
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, cfg)
}

// UpdateConfig updates the user's monitor configuration.
// PUT /monitor/config  body: MonitorConfig fields
func (ctrl *MonitorController) UpdateConfig(c *gin.Context) {
	userID, ok := getUserID(c)
	if !ok {
		return
	}

	var req model.MonitorConfig
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("request body error: %s", err.Error()), nil)
		return
	}

	// Validate cron expressions if provided
	if req.CrawlCron != "" {
		if _, err := cronParser.Parse(req.CrawlCron); err != nil {
			core.WriteResponse(c, errno.ErrInvalidCronExpression.SetMessage("crawl_cron: %s", err.Error()), nil)
			return
		}
	}
	if req.BriefingCron != "" {
		if _, err := cronParser.Parse(req.BriefingCron); err != nil {
			core.WriteResponse(c, errno.ErrInvalidCronExpression.SetMessage("briefing_cron: %s", err.Error()), nil)
			return
		}
	}

	req.UserID = userID

	if err := ctrl.monitorBiz.UpdateConfig(c, userID, &req); err != nil {
		log.C(c).Errorw("UpdateConfig failed", "user_id", userID, "err", err)
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, gin.H{"message": "ok"})
}

// GetStats returns monitor statistics for the current user.
// GET /monitor/stats
func (ctrl *MonitorController) GetStats(c *gin.Context) {
	userID, ok := getUserID(c)
	if !ok {
		return
	}

	stats, err := ctrl.monitorBiz.GetStats(c, userID)
	if err != nil {
		log.C(c).Errorw("GetStats failed", "user_id", userID, "err", err)
		core.WriteResponse(c, errno.InternalServerError.SetMessage("%s", err.Error()), nil)
		return
	}

	core.WriteResponse(c, nil, stats)
}

// ========== XHS Account Binding endpoints ==========

// CreateXhsQR starts a QR code login flow for XHS account binding.
// POST /monitor/xhs/qr/create
func (ctrl *MonitorController) CreateXhsQR(c *gin.Context) {
	userID, ok := getUserID(c)
	if !ok {
		return
	}

	qrID, code, qrURL, err := ctrl.monitorBiz.CreateQRLogin(c, userID)
	if err != nil {
		log.C(c).Errorw("CreateXhsQR failed", "user_id", userID, "err", err)
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, gin.H{
		"qr_id":  qrID,
		"code":   code,
		"qr_url": qrURL,
	})
}

// CheckXhsQRStatus polls the QR scan status.
// GET /monitor/xhs/qr/status/:qr_id
func (ctrl *MonitorController) CheckXhsQRStatus(c *gin.Context) {
	userID, ok := getUserID(c)
	if !ok {
		return
	}

	qrID := c.Param("qr_id")
	if qrID == "" {
		core.WriteResponse(c, errno.ErrBind.SetMessage("missing qr_id"), nil)
		return
	}

	status, message, err := ctrl.monitorBiz.CheckQRStatus(c, userID, qrID)
	if err != nil {
		log.C(c).Errorw("CheckXhsQRStatus failed", "user_id", userID, "qr_id", qrID, "err", err)
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, gin.H{
		"status":  status,
		"message": message,
	})
}

// CompleteXhsQR completes the QR login after user confirms.
// POST /monitor/xhs/qr/complete/:qr_id
func (ctrl *MonitorController) CompleteXhsQR(c *gin.Context) {
	userID, ok := getUserID(c)
	if !ok {
		return
	}

	qrID := c.Param("qr_id")
	if qrID == "" {
		core.WriteResponse(c, errno.ErrBind.SetMessage("missing qr_id"), nil)
		return
	}

	if err := ctrl.monitorBiz.CompleteQRLogin(c, userID, qrID); err != nil {
		log.C(c).Errorw("CompleteXhsQR failed", "user_id", userID, "qr_id", qrID, "err", err)
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, gin.H{"message": "ok"})
}

// GetXhsBindStatus returns the XHS account bind status for the current user.
// GET /monitor/xhs/bind-status
func (ctrl *MonitorController) GetXhsBindStatus(c *gin.Context) {
	userID, ok := getUserID(c)
	if !ok {
		return
	}

	bound, nickname, xhsUserID, err := ctrl.monitorBiz.GetXhsBindStatus(c, userID)
	if err != nil {
		log.C(c).Errorw("GetXhsBindStatus failed", "user_id", userID, "err", err)
		core.WriteResponse(c, errno.InternalServerError.SetMessage("%s", err.Error()), nil)
		return
	}

	core.WriteResponse(c, nil, gin.H{
		"bound":       bound,
		"nickname":    nickname,
		"xhs_user_id": xhsUserID,
	})
}

// UnbindXhs removes the XHS account binding.
// POST /monitor/xhs/unbind
func (ctrl *MonitorController) UnbindXhs(c *gin.Context) {
	userID, ok := getUserID(c)
	if !ok {
		return
	}

	if err := ctrl.monitorBiz.UnbindXhs(c, userID); err != nil {
		log.C(c).Errorw("UnbindXhs failed", "user_id", userID, "err", err)
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, gin.H{"message": "ok"})
}

// ========== Admin endpoints ==========

// AdminOverview returns a system-wide monitor overview.
// GET /admin/monitor/overview
func (ctrl *MonitorController) AdminOverview(c *gin.Context) {
	db := ctrl.store.DB().WithContext(c)

	var totalBloggers, totalNotes, totalBriefings, totalUsers int64
	db.Model(&model.MonitorBlogger{}).Count(&totalBloggers)
	db.Model(&model.MonitorNote{}).Count(&totalNotes)
	db.Model(&model.MonitorBriefing{}).Count(&totalBriefings)
	db.Model(&model.MonitorConfig{}).Count(&totalUsers)

	core.WriteResponse(c, nil, gin.H{
		"total_bloggers":  totalBloggers,
		"total_notes":     totalNotes,
		"total_briefings": totalBriefings,
		"total_users":     totalUsers,
	})
}

// AdminListBloggers returns paginated bloggers, optionally filtered by user_id.
// GET /admin/monitor/bloggers?offset=0&limit=20&user_id=1
func (ctrl *MonitorController) AdminListBloggers(c *gin.Context) {
	offset, limit := parsePagination(c)

	db := ctrl.store.DB().WithContext(c).Model(&model.MonitorBlogger{})

	if v := c.Query("user_id"); v != "" {
		db = db.Where("user_id = ?", v)
	}

	var total int64
	db.Count(&total)

	var list []model.MonitorBlogger
	if err := db.Offset(offset).Limit(limit).Order("created_at DESC").Find(&list).Error; err != nil {
		log.C(c).Errorw("AdminListBloggers failed", "err", err)
		core.WriteResponse(c, errno.InternalServerError.SetMessage("%s", err.Error()), nil)
		return
	}

	core.WriteResponse(c, nil, gin.H{"list": list, "total": total})
}

// AdminListNotes returns paginated notes, optionally filtered by user_id.
// GET /admin/monitor/notes?offset=0&limit=20&user_id=1
func (ctrl *MonitorController) AdminListNotes(c *gin.Context) {
	offset, limit := parsePagination(c)

	db := ctrl.store.DB().WithContext(c).Model(&model.MonitorNote{})

	if v := c.Query("user_id"); v != "" {
		db = db.Where("user_id = ?", v)
	}

	var total int64
	db.Count(&total)

	var list []model.MonitorNote
	if err := db.Offset(offset).Limit(limit).Order("created_at DESC").Find(&list).Error; err != nil {
		log.C(c).Errorw("AdminListNotes failed", "err", err)
		core.WriteResponse(c, errno.InternalServerError.SetMessage("%s", err.Error()), nil)
		return
	}

	core.WriteResponse(c, nil, gin.H{"list": list, "total": total})
}

// AdminListBriefings returns paginated briefings, optionally filtered by user_id.
// GET /admin/monitor/briefings?offset=0&limit=20&user_id=1
func (ctrl *MonitorController) AdminListBriefings(c *gin.Context) {
	offset, limit := parsePagination(c)

	db := ctrl.store.DB().WithContext(c).Model(&model.MonitorBriefing{})

	if v := c.Query("user_id"); v != "" {
		db = db.Where("user_id = ?", v)
	}

	var total int64
	db.Count(&total)

	var list []model.MonitorBriefing
	if err := db.Offset(offset).Limit(limit).Order("created_at DESC").Find(&list).Error; err != nil {
		log.C(c).Errorw("AdminListBriefings failed", "err", err)
		core.WriteResponse(c, errno.InternalServerError.SetMessage("%s", err.Error()), nil)
		return
	}

	core.WriteResponse(c, nil, gin.H{"list": list, "total": total})
}

// AdminGetUserConfig returns a specific user's monitor config.
// GET /admin/monitor/users/:user_id/config
func (ctrl *MonitorController) AdminGetUserConfig(c *gin.Context) {
	userID, ok := parseUintParam(c, "user_id")
	if !ok {
		return
	}

	cfg, err := ctrl.monitorBiz.GetConfig(c, userID)
	if err != nil {
		log.C(c).Errorw("AdminGetUserConfig failed", "user_id", userID, "err", err)
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, cfg)
}

// AdminUpdateUserConfig updates a specific user's monitor config.
// PUT /admin/monitor/users/:user_id/config
func (ctrl *MonitorController) AdminUpdateUserConfig(c *gin.Context) {
	userID, ok := parseUintParam(c, "user_id")
	if !ok {
		return
	}

	var req model.MonitorConfig
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("request body error: %s", err.Error()), nil)
		return
	}

	// Validate cron expressions if provided
	if req.CrawlCron != "" {
		if _, err := cronParser.Parse(req.CrawlCron); err != nil {
			core.WriteResponse(c, errno.ErrInvalidCronExpression.SetMessage("crawl_cron: %s", err.Error()), nil)
			return
		}
	}
	if req.BriefingCron != "" {
		if _, err := cronParser.Parse(req.BriefingCron); err != nil {
			core.WriteResponse(c, errno.ErrInvalidCronExpression.SetMessage("briefing_cron: %s", err.Error()), nil)
			return
		}
	}

	req.UserID = userID

	if err := ctrl.monitorBiz.UpdateConfig(c, userID, &req); err != nil {
		log.C(c).Errorw("AdminUpdateUserConfig failed", "user_id", userID, "err", err)
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, gin.H{"message": "ok"})
}
