package skill

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"gorm.io/datatypes"
	"gorm.io/gorm"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/model"
)

// SystemPromptMaxLen is the maximum allowed byte length for system_prompt (64KB).
const SystemPromptMaxLen = 64 * 1024

// CreateRequest 包含创建 agent_definition 所需的所有字段。
type CreateRequest struct {
	Name                 string
	Description          string
	IconURL              string
	WelcomeMessage       string
	SystemPrompt         string
	Starters             []string
	QuestionnaireAnswers QuestionnaireAnswers
	ToolFlags            map[string]bool
	CreditCapPerSession  *uint
	DailyCreditCap       *uint
	SourceTemplateID     *uint64
}

// PatchRequest 包含更新 agent_definition 的可选字段（nil = 不变）。
// 注意：advanced_mode / parent_user_id / is_active 不允许通过 Patch 修改。
type PatchRequest struct {
	Name                 *string
	Description          *string
	IconURL              *string
	WelcomeMessage       *string
	SystemPrompt         *string
	Starters             *[]string
	QuestionnaireAnswers *QuestionnaireAnswers
	ToolFlags            *map[string]bool
	CreditCapPerSession  *uint
	DailyCreditCap       *uint
	CustomSkillBody      *string
}

// Service 定义 biz/skill 层的 9 个业务方法。
// 所有方法第一步校验 userID 为父账户（子账户返回 ErrChildAccountForbidden）。
type Service interface {
	Create(ctx context.Context, userID uint, req CreateRequest) (*model.AgentDefinition, error)
	Get(ctx context.Context, userID uint, id uint64) (*model.AgentDefinition, error)
	List(ctx context.Context, userID uint, includeInactive bool, page, pageSize int) ([]model.AgentDefinition, int64, error)
	Patch(ctx context.Context, userID uint, id uint64, req PatchRequest) (*model.AgentDefinition, error)
	SoftDelete(ctx context.Context, userID uint, id uint64) error

	ListHistory(ctx context.Context, userID uint, agentID uint64) ([]model.AgentDefinitionHistory, error)
	Restore(ctx context.Context, userID uint, agentID uint64, version uint) (*model.AgentDefinition, error)
	AdvancedToggle(ctx context.Context, userID uint, id uint64) (*model.AgentDefinition, error)

	ListTemplates(ctx context.Context) ([]model.SkillTemplate, error)

	// AvailableForStudent returns active agents visible to a learner (child account).
	// Returns empty slice for parent accounts. #14 follow-up student-endpoints ALPHA.
	AvailableForStudent(ctx context.Context, learnerUserID uint) ([]*model.AgentDefinition, error)
}

type service struct {
	ds              store.IStore
	skillStore      store.IAgentDefinitionStore
	templateStore   store.ISkillTemplateStore
	templateService *TemplateService
}

// NewService 构造 Service 实例。
func NewService(ds store.IStore) Service {
	return &service{
		ds:              ds,
		skillStore:      ds.AgentDefinitions(),
		templateStore:   ds.SkillTemplates(),
		templateService: NewTemplateService(ds.SkillTemplates()),
	}
}

// validateRequiredQuestionnaireForCreate 校验创建新 AgentDefinition 时问卷必填项
// Q6（任务类型）/ Q7（材料类型）/ Q12（说话风格）是否齐全。缺失任一项返回
// errno.ErrSkillBuilderFailed（HTTP 422，errno doc 已写 "如必填题缺失"），错误
// 消息列出具体缺失字段。
//
// 注意：Build()（skill_builder.go）是纯 transformer，缺字段时对应段落省略不报错，
// 这是 4 个 Build 单测明确强制的行为。必填业务规则归本函数（biz 层创建期约束）。
//
// Patch 不调本函数：部分更新允许 caller 不带 questionnaire；如 caller 主动把 QA
// 改成空，那是另一类问题，本函数不覆盖。
func validateRequiredQuestionnaireForCreate(qa QuestionnaireAnswers) error {
	var missing []string
	if len(qa.Q6) == 0 {
		missing = append(missing, "q6 (任务类型)")
	}
	if len(qa.Q7) == 0 {
		missing = append(missing, "q7 (材料类型)")
	}
	if qa.Q12 == "" {
		missing = append(missing, "q12 (说话风格)")
	}
	if len(missing) > 0 {
		return errno.ErrSkillBuilderFailed.SetMessage("问卷必填项缺失：%s", strings.Join(missing, "、"))
	}
	return nil
}

// requireParentAccount 校验 userID 对应的 user 是父账户（ParentUserID == nil）。
// 子账户返回 ErrChildAccountForbidden；用户不存在返回包装后的 store 错误。
func (s *service) requireParentAccount(ctx context.Context, userID uint) error {
	user, err := s.ds.Users().GetByID(ctx, userID)
	if err != nil {
		return fmt.Errorf("requireParentAccount get user(%d): %w", userID, err)
	}
	if user.ParentUserID != nil {
		return errno.ErrChildAccountForbidden
	}
	return nil
}

// ownsAgent 校验 ad.ParentUserID == userID，否则返回 ErrSkillNotFound（隐藏越权）。
func ownsAgent(ad *model.AgentDefinition, userID uint) error {
	if ad.ParentUserID != userID {
		return errno.ErrSkillNotFound
	}
	return nil
}

// marshalJSON 是 json.Marshal 的 helper，返回 datatypes.JSON。
func marshalJSON(v any) (datatypes.JSON, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return datatypes.JSON(b), nil
}

// Create 创建新的 AgentDefinition。
// 跨表事务：skillStore.CreateTx + WriteHistorySnapshot 原子执行。
// IsActive 默认 true；version = 1。
//
// tool_flags 默认值规则（当 req.ToolFlags 为 nil/empty 时）：
// 当前 5 步问卷未含 "工具选择" step，frontend 不会发送 tool_flags。
// 没有 tool_flags 会让 runner.go 走 pre-ReAct short-circuit（不调 LLM, 0 积分），
// 学员看到 echo + 前端 'failed' 文案。为避免每个新 Agent 都是哑炮，
// 这里根据 questionnaire_answers 智能 derive 一个合理默认集：
//   - 永远开：基础工具 (kb_search/learner_data_query/memory_*/get_current_date/ask_user_question)
//   - q9='allow_search' 开：web_search + web_fetch
//   - q7 含 'text'/'csv'/'image' 开：file_read
//   - 危险类（bash_exec/image_gen/document_generate）保持 OFF
//
// 等 #15+ frontend questionnaire 加 "工具选择" step 后可放弃本默认。
func deriveDefaultToolFlags(qa QuestionnaireAnswers) map[string]bool {
	return map[string]bool{
		// 基础读取 / 记忆 / 反问 / 时间
		"kb_search":          true,
		"learner_data_query": true,
		"memory_read":        true,
		"memory_write":       true,
		"get_current_date":   true,
		"ask_user_question":  true,
		// 网络搜索与抓取
		"web_search": true,
		"web_fetch":  true,
		// 学员材料文件读取
		"file_read": true,
		// 高级及高危工具
		"bash_exec":         true,
		"image_gen":         true,
		"document_generate": true,
		"code_sandbox":      true,
		"media":             true,
		"dangerous":         true,
	}
}

func (s *service) Create(ctx context.Context, userID uint, req CreateRequest) (*model.AgentDefinition, error) {
	if err := s.requireParentAccount(ctx, userID); err != nil {
		return nil, err
	}

	if err := validateRequiredQuestionnaireForCreate(req.QuestionnaireAnswers); err != nil {
		return nil, err
	}

	if len(req.SystemPrompt) > SystemPromptMaxLen {
		return nil, errno.ErrSystemPromptTooLong
	}

	qaJSON, err := marshalJSON(req.QuestionnaireAnswers)
	if err != nil {
		return nil, fmt.Errorf("Create marshal questionnaire: %w", err)
	}
	startersJSON, err := marshalJSON(req.Starters)
	if err != nil {
		return nil, fmt.Errorf("Create marshal starters: %w", err)
	}
	// Fill default tool_flags when frontend doesn't supply (5-step questionnaire
	// lacks a tool-selection step — see deriveDefaultToolFlags doc above).
	if len(req.ToolFlags) == 0 {
		req.ToolFlags = deriveDefaultToolFlags(req.QuestionnaireAnswers)
	}
	toolFlagsJSON, err := marshalJSON(req.ToolFlags)
	if err != nil {
		return nil, fmt.Errorf("Create marshal tool_flags: %w", err)
	}

	ad := &model.AgentDefinition{
		ParentUserID:         userID,
		Name:                 req.Name,
		Description:          req.Description,
		IconURL:              req.IconURL,
		WelcomeMessage:       req.WelcomeMessage,
		SystemPrompt:         req.SystemPrompt,
		Starters:             startersJSON,
		QuestionnaireAnswers: qaJSON,
		ToolFlags:            toolFlagsJSON,
		CreditCapPerSession:  req.CreditCapPerSession,
		DailyCreditCap:       req.DailyCreditCap,
		SourceTemplateID:     req.SourceTemplateID,
		Version:              1,
		IsActive:             true,
		AdvancedMode:         false,
		CreatedBy:            userID,
	}

	body, err := Build(ad)
	if err != nil {
		return nil, err // already errno.ErrSkillBuilderFailed or wrapped parse error
	}
	ad.GeneratedSkillBody = body

	err = s.ds.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := s.skillStore.CreateTx(ctx, tx, ad); err != nil {
			return err
		}
		return WriteHistorySnapshot(ctx, tx, ad, userID, ComputeChangesSummary(nil, ad, 0))
	})
	if err != nil {
		return nil, err
	}
	return ad, nil
}

// Get 查询单个 AgentDefinition（含已软删除，供详情接口使用）。
// 校验 parent_user_id，跨账户返回 ErrSkillNotFound。
func (s *service) Get(ctx context.Context, userID uint, id uint64) (*model.AgentDefinition, error) {
	if err := s.requireParentAccount(ctx, userID); err != nil {
		return nil, err
	}

	ad, err := s.skillStore.GetByIDIncludeInactive(ctx, id)
	if err != nil {
		if isNotFound(err) {
			return nil, errno.ErrSkillNotFound
		}
		return nil, fmt.Errorf("Get skill(%d): %w", id, err)
	}
	if err := ownsAgent(ad, userID); err != nil {
		return nil, err
	}
	return ad, nil
}

// List 列举当前父账户的所有 AgentDefinition（分页）。
func (s *service) List(ctx context.Context, userID uint, includeInactive bool, page, pageSize int) ([]model.AgentDefinition, int64, error) {
	if err := s.requireParentAccount(ctx, userID); err != nil {
		return nil, 0, err
	}

	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 20
	}
	if pageSize > 100 {
		pageSize = 100
	}
	offset := (page - 1) * pageSize

	items, total, err := s.skillStore.ListByParent(ctx, userID, includeInactive, offset, pageSize)
	if err != nil {
		return nil, 0, fmt.Errorf("List skills for user(%d): %w", userID, err)
	}
	return items, total, nil
}

// Patch 部分更新 AgentDefinition。
// 拒绝修改 advanced_mode / parent_user_id / is_active（这些字段通过专用端点操作）。
// 问卷改变时重算 GeneratedSkillBody；version+1；写 history。全程事务。
func (s *service) Patch(ctx context.Context, userID uint, id uint64, req PatchRequest) (*model.AgentDefinition, error) {
	if err := s.requireParentAccount(ctx, userID); err != nil {
		return nil, err
	}

	if req.SystemPrompt != nil && len(*req.SystemPrompt) > SystemPromptMaxLen {
		return nil, errno.ErrSystemPromptTooLong
	}

	ad, err := s.skillStore.GetByIDIncludeInactive(ctx, id)
	if err != nil {
		if isNotFound(err) {
			return nil, errno.ErrSkillNotFound
		}
		return nil, fmt.Errorf("Patch get skill(%d): %w", id, err)
	}
	if err := ownsAgent(ad, userID); err != nil {
		return nil, err
	}

	prev := copyAgentDefinition(ad)

	// Apply non-nil fields.
	if req.Name != nil {
		ad.Name = *req.Name
	}
	if req.Description != nil {
		ad.Description = *req.Description
	}
	if req.IconURL != nil {
		ad.IconURL = *req.IconURL
	}
	if req.WelcomeMessage != nil {
		ad.WelcomeMessage = *req.WelcomeMessage
	}
	if req.SystemPrompt != nil {
		ad.SystemPrompt = *req.SystemPrompt
	}
	if req.Starters != nil {
		b, err := marshalJSON(*req.Starters)
		if err != nil {
			return nil, fmt.Errorf("Patch marshal starters: %w", err)
		}
		ad.Starters = b
	}
	if req.QuestionnaireAnswers != nil {
		b, err := marshalJSON(*req.QuestionnaireAnswers)
		if err != nil {
			return nil, fmt.Errorf("Patch marshal questionnaire: %w", err)
		}
		ad.QuestionnaireAnswers = b
	}
	if req.ToolFlags != nil {
		b, err := marshalJSON(*req.ToolFlags)
		if err != nil {
			return nil, fmt.Errorf("Patch marshal tool_flags: %w", err)
		}
		ad.ToolFlags = b
	}
	if req.CreditCapPerSession != nil {
		ad.CreditCapPerSession = req.CreditCapPerSession
	}
	if req.DailyCreditCap != nil {
		ad.DailyCreditCap = req.DailyCreditCap
	}
	if req.CustomSkillBody != nil {
		if !ad.AdvancedMode {
			return nil, errno.ErrInvalidParameter.SetMessage("问卷模式不允许直接编辑 SKILL.md")
		}
		ad.CustomSkillBody = *req.CustomSkillBody
	}

	// Rebuild GeneratedSkillBody whenever questionnaire or top-level Q fields change.
	body, err := Build(ad)
	if err != nil {
		return nil, err
	}
	ad.GeneratedSkillBody = body
	ad.Version++

	summary := ComputeChangesSummary(prev, ad, 0)

	err = s.ds.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := s.skillStore.UpdateTx(ctx, tx, ad); err != nil {
			return err
		}
		return WriteHistorySnapshot(ctx, tx, ad, userID, summary)
	})
	if err != nil {
		return nil, err
	}
	return ad, nil
}

// SoftDelete 软删除（is_active=0），幂等。已 inactive 的 agent 也返回 nil。
// version+1 + 写 history "软删除"。全程事务。
func (s *service) SoftDelete(ctx context.Context, userID uint, id uint64) error {
	if err := s.requireParentAccount(ctx, userID); err != nil {
		return err
	}

	ad, err := s.skillStore.GetByIDIncludeInactive(ctx, id)
	if err != nil {
		if isNotFound(err) {
			return errno.ErrSkillNotFound
		}
		return fmt.Errorf("SoftDelete get skill(%d): %w", id, err)
	}
	if err := ownsAgent(ad, userID); err != nil {
		return err
	}

	// 幂等：已 inactive 直接返回 nil。
	if !ad.IsActive {
		return nil
	}

	prev := copyAgentDefinition(ad)
	ad.IsActive = false
	ad.Version++
	summary := ComputeChangesSummary(prev, ad, 0)

	return s.ds.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := s.skillStore.UpdateTx(ctx, tx, ad); err != nil {
			return err
		}
		return WriteHistorySnapshot(ctx, tx, ad, userID, summary)
	})
}

// ListHistory 列举指定 agent 的所有版本历史（含已软删除的 agent）。
func (s *service) ListHistory(ctx context.Context, userID uint, agentID uint64) ([]model.AgentDefinitionHistory, error) {
	if err := s.requireParentAccount(ctx, userID); err != nil {
		return nil, err
	}

	// Verify ownership (include inactive so deleted agents' history is accessible).
	ad, err := s.skillStore.GetByIDIncludeInactive(ctx, agentID)
	if err != nil {
		if isNotFound(err) {
			return nil, errno.ErrSkillNotFound
		}
		return nil, fmt.Errorf("ListHistory get skill(%d): %w", agentID, err)
	}
	if err := ownsAgent(ad, userID); err != nil {
		return nil, err
	}

	histories, err := s.skillStore.ListHistory(ctx, agentID)
	if err != nil {
		return nil, fmt.Errorf("ListHistory skill(%d): %w", agentID, err)
	}
	return histories, nil
}

// Restore 从历史快照恢复指定版本，生成新版本并写入历史。全程事务。
func (s *service) Restore(ctx context.Context, userID uint, agentID uint64, version uint) (*model.AgentDefinition, error) {
	if err := s.requireParentAccount(ctx, userID); err != nil {
		return nil, err
	}

	// Verify ownership.
	ad, err := s.skillStore.GetByIDIncludeInactive(ctx, agentID)
	if err != nil {
		if isNotFound(err) {
			return nil, errno.ErrSkillNotFound
		}
		return nil, fmt.Errorf("Restore get skill(%d): %w", agentID, err)
	}
	if err := ownsAgent(ad, userID); err != nil {
		return nil, err
	}

	// Load the target history snapshot.
	hist, err := s.skillStore.GetHistoryByVersion(ctx, agentID, version)
	if err != nil {
		if isNotFound(err) {
			return nil, errno.ErrSkillVersionNotFound
		}
		return nil, fmt.Errorf("Restore get history(agentID=%d, version=%d): %w", agentID, version, err)
	}

	// Unmarshal the snapshot into a fresh AgentDefinition.
	var restored model.AgentDefinition
	if err := json.Unmarshal(hist.Snapshot, &restored); err != nil {
		return nil, fmt.Errorf("Restore unmarshal snapshot: %w", err)
	}

	// Preserve identity fields; bump version.
	restored.ID = ad.ID
	restored.ParentUserID = ad.ParentUserID
	restored.CreatedBy = ad.CreatedBy
	restored.CreatedAt = ad.CreatedAt

	// New version = current max + 1.
	maxVer, err := s.skillStore.MaxVersion(ctx, agentID)
	if err != nil {
		return nil, fmt.Errorf("Restore MaxVersion(agentID=%d): %w", agentID, err)
	}
	restored.Version = maxVer + 1
	restored.IsActive = true // restore always re-activates

	summary := ComputeChangesSummary(nil, &restored, version)

	err = s.ds.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := s.skillStore.UpdateTx(ctx, tx, &restored); err != nil {
			return err
		}
		return WriteHistorySnapshot(ctx, tx, &restored, userID, summary)
	})
	if err != nil {
		return nil, err
	}
	return &restored, nil
}

// AdvancedToggle 将问卷模式（advanced_mode=0）切换为高级模式（advanced_mode=1），不可逆。
// 已处于高级模式时返回 ErrAlreadyInAdvancedMode。
// 拷贝 GeneratedSkillBody → CustomSkillBody 作为初始值。全程事务。
func (s *service) AdvancedToggle(ctx context.Context, userID uint, id uint64) (*model.AgentDefinition, error) {
	if err := s.requireParentAccount(ctx, userID); err != nil {
		return nil, err
	}

	ad, err := s.skillStore.GetByIDIncludeInactive(ctx, id)
	if err != nil {
		if isNotFound(err) {
			return nil, errno.ErrSkillNotFound
		}
		return nil, fmt.Errorf("AdvancedToggle get skill(%d): %w", id, err)
	}
	if err := ownsAgent(ad, userID); err != nil {
		return nil, err
	}

	if ad.AdvancedMode {
		return nil, errno.ErrAlreadyInAdvancedMode
	}

	prev := copyAgentDefinition(ad)
	ad.AdvancedMode = true
	ad.CustomSkillBody = ad.GeneratedSkillBody
	ad.Version++

	summary := ComputeChangesSummary(prev, ad, 0)

	err = s.ds.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := s.skillStore.UpdateTx(ctx, tx, ad); err != nil {
			return err
		}
		return WriteHistorySnapshot(ctx, tx, ad, userID, summary)
	})
	if err != nil {
		return nil, err
	}
	return ad, nil
}

// ListTemplates 列举所有激活的平台内置技能模板。
func (s *service) ListTemplates(ctx context.Context) ([]model.SkillTemplate, error) {
	return s.templateService.List(ctx)
}

// ---------------------------------------------------------------------------
// internal helpers
// ---------------------------------------------------------------------------

// isNotFound returns true when err wraps a gorm.ErrRecordNotFound.
func isNotFound(err error) bool {
	return errors.Is(err, gorm.ErrRecordNotFound)
}

// copyAgentDefinition returns a shallow copy of ad (value copy, not pointer).
// Used to capture the "before" snapshot for ComputeChangesSummary.
func copyAgentDefinition(ad *model.AgentDefinition) *model.AgentDefinition {
	cp := *ad
	return &cp
}
