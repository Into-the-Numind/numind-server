// admin.go 是通知中心管理端（发布端）的 HTTP 控制层（notification-center T4b）。
//
// 职责边界（controller 硬规则）：本层只做参数绑定 + 鉴权上下文提取 + 调用 biz +
// core.WriteResponse(c, nil, dto)。RESPONSE DTO 由 biz 层拥有（json tag 严格匹配
// spec §3.2），controller 不二次包装、不含任何业务逻辑（Delete 的 {deleted:true}
// 是纯展示包装，非业务判定）。所有 domain error 由 biz 返回并直接透传。
//
// 与 user.go 同包：复用 currentUserID（提取 admin id；AdminAuthMiddleware 已强制
// IsAdmin）、parsePagination（page/page_size）等 helper。
//
// PUT（partial update）契约要点：spec §3.2 的 PUT 必须区分"字段缺省（不改）"与
// "显式置 null（清空 expires_at）"。gin 的 struct 绑定无法表达这一差异（缺省字段
// 与 JSON null 都解成零值/nil），故 Update 先把 body 解成 map[string]json.RawMessage
// 探测哪些 key 出现，再据此构造 biz.UpdateInput（仅出现的 key 才赋值；ExpiresAtSet
// 仅当 "expires_at" key 出现时为 true；Questions 仅当 "questions" key 出现时非 nil）。
package announcement

import (
	"encoding/json"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	announcementbiz "numind-server/internal/numind/biz/announcement"
	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
)

// QuestionItem 是创建/更新公告时单题的请求形状（spec §3.2 questions[]）。
// Required 用 *bool：缺省 → nil → biz 默认 true（与 DB default:1 一致）；显式 false
// 时尊重（配合 store 的 default-bool fixup，required=false 正确落库）。
type QuestionItem struct {
	OrderIndex   int      `json:"order_index"`
	QuestionType string   `json:"question_type" binding:"required"`
	Title        string   `json:"title" binding:"required"`
	Required     *bool    `json:"required"`
	Options      []string `json:"options"`
	RatingMax    *int     `json:"rating_max"`
	RatingStyle  *string  `json:"rating_style"`
}

// CreateAnnouncementRequest 是创建公告请求 body（spec §3.2 POST）。
// IsImportant 用 *bool 避免 false 被吞（GORM default bool 坑，spec §5）；biz 侧入参
// CreateInput.IsImportant 为值类型，controller 解引用（nil → false）后传入。
type CreateAnnouncementRequest struct {
	Type        string         `json:"type"`
	Title       string         `json:"title" binding:"required"`
	Content     string         `json:"content" binding:"required"` // spec §1.1 content NOT NULL — 不允许空内容公告
	IsImportant *bool          `json:"is_important"`
	ExpiresAt   *time.Time     `json:"expires_at"`
	Status      string         `json:"status"`
	Questions   []QuestionItem `json:"questions"`
}

// AdminController 是通知中心管理端控制器。
type AdminController struct {
	biz announcementbiz.IAnnouncementBiz
}

// NewAdminController 创建通知中心管理端控制器（沿用本项目 NewXxxController(biz) 模式）。
func NewAdminController(biz announcementbiz.IAnnouncementBiz) *AdminController {
	return &AdminController{biz: biz}
}

// mapQuestionItems 把请求题目映射为 biz.QuestionInput（biz 负责按题型校验）。
func mapQuestionItems(items []QuestionItem) []announcementbiz.QuestionInput {
	out := make([]announcementbiz.QuestionInput, 0, len(items))
	for _, q := range items {
		out = append(out, announcementbiz.QuestionInput{
			OrderIndex:   q.OrderIndex,
			QuestionType: q.QuestionType,
			Title:        q.Title,
			Options:      q.Options,
			RatingMax:    q.RatingMax,
			RatingStyle:  q.RatingStyle,
			Required:     q.Required,
		})
	}
	return out
}

// Create POST /v1/admin/announcements
// 创建公告/问卷，返回 AdminAnnouncementDTO（spec §3.2）。created_by=当前 admin id。
func (ctrl *AdminController) Create(c *gin.Context) {
	adminID, ok := currentUserID(c)
	if !ok {
		core.WriteResponse(c, errno.ErrUnauthorized.SetMessage("未找到管理员信息"), nil)
		return
	}

	var req CreateAnnouncementRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("请求参数错误: %s", err.Error()), nil)
		return
	}

	in := announcementbiz.CreateInput{
		Type:      req.Type,
		Title:     req.Title,
		Content:   req.Content,
		ExpiresAt: req.ExpiresAt,
		Status:    req.Status,
		Questions: mapQuestionItems(req.Questions),
	}
	if req.IsImportant != nil {
		in.IsImportant = *req.IsImportant
	}

	dto, err := ctrl.biz.Create(c, adminID, in)
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, dto)
}

// List GET /v1/admin/announcements?page&page_size&status&type
// 返回公告列表（含 read_count/target_count/response_count）+ total（spec §3.2）。
func (ctrl *AdminController) List(c *gin.Context) {
	page, pageSize := parsePagination(c)
	status := c.Query("status")
	annType := c.Query("type")

	dto, err := ctrl.biz.ListForAdmin(c, status, annType, page, pageSize)
	if err != nil {
		log.C(c).Errorw("Failed to list announcements for admin", "err", err)
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, dto)
}

// Get GET /v1/admin/announcements/:id
// 返回 admin 端公告详情（含 questions，spec §3.2）。
func (ctrl *AdminController) Get(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("无效的公告ID"), nil)
		return
	}

	dto, err := ctrl.biz.GetForAdmin(c, id)
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, dto)
}

// Update PUT /v1/admin/announcements/:id
// partial update（spec §3.2）：title/content/is_important/expires_at 任意状态可改；
// questions 仅 draft 可改（biz 校验）。
//
// 关键：先把 body 解成 map[string]json.RawMessage 探测出现的 key，再据此构造
// UpdateInput，正确区分"缺省（不改）"与"显式 null（清空）"——尤其 expires_at。
func (ctrl *AdminController) Update(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("无效的公告ID"), nil)
		return
	}

	// 1) 探测出现了哪些 key（区分缺省 vs 显式 null）。
	var raw map[string]json.RawMessage
	if err := c.ShouldBindJSON(&raw); err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("请求参数错误: %s", err.Error()), nil)
		return
	}

	in := announcementbiz.UpdateInput{}

	if v, present := raw["title"]; present {
		var title string
		if err := json.Unmarshal(v, &title); err != nil {
			core.WriteResponse(c, errno.ErrBind.SetMessage("title 格式错误: %s", err.Error()), nil)
			return
		}
		in.Title = &title
	}
	if v, present := raw["content"]; present {
		var content string
		if err := json.Unmarshal(v, &content); err != nil {
			core.WriteResponse(c, errno.ErrBind.SetMessage("content 格式错误: %s", err.Error()), nil)
			return
		}
		in.Content = &content
	}
	if v, present := raw["is_important"]; present {
		var imp bool
		if err := json.Unmarshal(v, &imp); err != nil {
			core.WriteResponse(c, errno.ErrBind.SetMessage("is_important 格式错误: %s", err.Error()), nil)
			return
		}
		in.IsImportant = &imp
	}
	if v, present := raw["expires_at"]; present {
		// present=true 时按 ExpiresAt 更新（含显式 null=清空/永不过期）。
		in.ExpiresAtSet = true
		var exp *time.Time
		if err := json.Unmarshal(v, &exp); err != nil {
			core.WriteResponse(c, errno.ErrBind.SetMessage("expires_at 格式错误: %s", err.Error()), nil)
			return
		}
		in.ExpiresAt = exp
	}
	if v, present := raw["questions"]; present {
		var items []QuestionItem
		if err := json.Unmarshal(v, &items); err != nil {
			core.WriteResponse(c, errno.ErrBind.SetMessage("questions 格式错误: %s", err.Error()), nil)
			return
		}
		// 非 nil 才表示"替换题目"。空数组也视为显式替换（biz 按题型校验）。
		in.Questions = mapQuestionItems(items)
		if in.Questions == nil {
			in.Questions = []announcementbiz.QuestionInput{}
		}
	}

	dto, err := ctrl.biz.Update(c, id, in)
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, dto)
}

// Publish POST /v1/admin/announcements/:id/publish
// draft→published（biz 校验非 draft 报 ErrAnnouncementStatus），返回更新后对象。
func (ctrl *AdminController) Publish(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("无效的公告ID"), nil)
		return
	}

	dto, err := ctrl.biz.Publish(c, id)
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, dto)
}

// Archive POST /v1/admin/announcements/:id/archive
// →archived（用户端不再展示），返回更新后对象（spec §3.2）。
func (ctrl *AdminController) Archive(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("无效的公告ID"), nil)
		return
	}

	dto, err := ctrl.biz.Archive(c, id)
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, dto)
}

// Delete DELETE /v1/admin/announcements/:id
// 软删，返回 {deleted:true}（spec §3.2）。biz.Delete 仅返回 error，成功时由
// controller 包装 {deleted:true}（纯展示，非业务判定）。
func (ctrl *AdminController) Delete(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("无效的公告ID"), nil)
		return
	}

	if err := ctrl.biz.Delete(c, id); err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, gin.H{"deleted": true})
}

// Stats GET /v1/admin/announcements/:id/stats
// 返回 target/read/response 计数 + 比例（spec §3.2）。
func (ctrl *AdminController) Stats(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("无效的公告ID"), nil)
		return
	}

	dto, err := ctrl.biz.Stats(c, id)
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, dto)
}

// Readers GET /v1/admin/announcements/:id/readers?page&page_size&status=read|unread
// 返回已读/未读用户分页列表（spec §3.2）。status 由 biz 校验（read|unread）。
func (ctrl *AdminController) Readers(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("无效的公告ID"), nil)
		return
	}

	page, pageSize := parsePagination(c)
	status := c.Query("status")

	dto, err := ctrl.biz.ListReaders(c, id, status, page, pageSize)
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, dto)
}

// SurveyResults GET /v1/admin/announcements/:id/survey-results
// 返回问卷聚合结果（spec §3.2）。
func (ctrl *AdminController) SurveyResults(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("无效的公告ID"), nil)
		return
	}

	dto, err := ctrl.biz.SurveyResults(c, id)
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, dto)
}

// Responses GET /v1/admin/announcements/:id/responses?page&page_size
// 按用户下钻答卷（spec §3.2）。
func (ctrl *AdminController) Responses(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("无效的公告ID"), nil)
		return
	}

	page, pageSize := parsePagination(c)

	dto, err := ctrl.biz.ListResponses(c, id, page, pageSize)
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, dto)
}
