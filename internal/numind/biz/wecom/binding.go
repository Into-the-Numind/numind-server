package wecom

import (
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
)

// BindingService 身份绑定服务
// 负责将微信外部联系人与 Numind 平台用户关联
type BindingService struct {
	db *gorm.DB
}

// NewBindingService 创建绑定服务实例
func NewBindingService(db *gorm.DB) *BindingService {
	return &BindingService{db: db}
}

// BindUserRequest 绑定请求
type BindUserRequest struct {
	NumindUserID   int64  `json:"numind_user_id"`   // Numind 平台用户 ID
	ExternalUserID string `json:"external_user_id"` // 微信外部联系人 ID (wm_xxx)
}

// BindUser 将微信外部联系人绑定到 Numind 用户
// 场景：用户在 Numind 前端扫码/输入验证码完成绑定
func (s *BindingService) BindUser(req *BindUserRequest) error {
	if req.NumindUserID <= 0 {
		return errors.New("invalid numind_user_id")
	}
	if req.ExternalUserID == "" {
		return errors.New("external_user_id is required")
	}

	// 检查是否已被其他 Numind 用户绑定
	var existingUser WecomUser
	err := s.db.First(&existingUser, "id = ?", req.ExternalUserID).Error
	if err == nil && existingUser.NumindUserID != nil && *existingUser.NumindUserID != req.NumindUserID {
		return errors.New("此微信账号已被其他用户绑定")
	}

	// 检查该 Numind 用户是否已绑定其他微信
	var boundUser WecomUser
	err = s.db.First(&boundUser, "numind_user_id = ?", req.NumindUserID).Error
	if err == nil && boundUser.ID != req.ExternalUserID {
		return errors.New("您已绑定其他微信账号，请先解绑")
	}

	// 执行绑定
	now := time.Now()
	if err == gorm.ErrRecordNotFound {
		// 新建用户记录
		user := WecomUser{
			ID:           req.ExternalUserID,
			NumindUserID: &req.NumindUserID,
			BoundAt:      &now,
		}
		return s.db.Create(&user).Error
	}

	// 更新现有记录
	return s.db.Model(&WecomUser{}).Where("id = ?", req.ExternalUserID).Updates(map[string]interface{}{
		"numind_user_id": req.NumindUserID,
		"bound_at":       now,
	}).Error
}

// UnbindUser 解除绑定
func (s *BindingService) UnbindUser(numindUserID int64) error {
	return s.db.Model(&WecomUser{}).Where("numind_user_id = ?", numindUserID).Updates(map[string]interface{}{
		"numind_user_id": nil,
		"bound_at":       nil,
	}).Error
}

// GetBindingByNumindUser 根据 Numind 用户 ID 查询绑定的微信账号
func (s *BindingService) GetBindingByNumindUser(numindUserID int64) (*WecomUser, error) {
	var user WecomUser
	err := s.db.First(&user, "numind_user_id = ?", numindUserID).Error
	if err != nil {
		return nil, err
	}
	return &user, nil
}

// GetBindingByExternalUser 根据微信外部联系人 ID 查询绑定信息
func (s *BindingService) GetBindingByExternalUser(externalUserID string) (*WecomUser, error) {
	var user WecomUser
	err := s.db.First(&user, "id = ?", externalUserID).Error
	if err != nil {
		return nil, err
	}
	return &user, nil
}

// GetMessagesByNumindUser 获取指定 Numind 用户的所有微信消息
// 这是前端展示消息的核心接口
func (s *BindingService) GetMessagesByNumindUser(numindUserID int64, limit, offset int) ([]WecomMessage, int64, error) {
	// 先查询用户绑定的微信账号
	binding, err := s.GetBindingByNumindUser(numindUserID)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return []WecomMessage{}, 0, nil // 未绑定，返回空
		}
		return nil, 0, err
	}

	// 查询该微信账号收发的所有消息
	var messages []WecomMessage
	var total int64

	query := s.db.Model(&WecomMessage{}).Where(
		"from_user_id = ? OR to_user_id = ?",
		binding.ID, binding.ID,
	)

	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	if err := query.Order("msg_time DESC").Limit(limit).Offset(offset).Find(&messages).Error; err != nil {
		return nil, 0, err
	}

	return messages, total, nil
}

// GetConversationMessages 获取两个用户之间的对话消息
// externalUserID: 用户绑定的微信 ID
// partnerID: 聊天对象的 ID (通常是机器人 ID)
func (s *BindingService) GetConversationMessages(externalUserID, partnerID string, limit, offset int) ([]WecomMessage, int64, error) {
	var messages []WecomMessage
	var total int64

	query := s.db.Model(&WecomMessage{}).Where(
		"(from_user_id = ? AND to_user_id = ?) OR (from_user_id = ? AND to_user_id = ?)",
		externalUserID, partnerID, partnerID, externalUserID,
	)

	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	if err := query.Order("msg_time ASC").Limit(limit).Offset(offset).Find(&messages).Error; err != nil {
		return nil, 0, err
	}

	return messages, total, nil
}

// ContactConversation 联系人会话摘要
type ContactConversation struct {
	PartnerID   string    `json:"partner_id"`   // 对方ID (User ID or Room ID)
	PartnerName string    `json:"partner_name"` // 对方名称 (Mocked for now if not stored)
	LastMsg     string    `json:"last_msg"`     // 最后一条消息内容
	LastTime    time.Time `json:"last_time"`    // 最后一条消息时间
	Avatar      string    `json:"avatar"`       // 头像 (Mocked or from DB)
}

// GetContacts 获取最近联系人列表
func (s *BindingService) GetContacts(numindUserID int64) ([]ContactConversation, error) {
	// 1. 获取绑定身份
	binding, err := s.GetBindingByNumindUser(numindUserID)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return []ContactConversation{}, nil
		}
		return nil, err
	}
	myID := binding.ID

	// 2. 聚合查询最近联系人
	type Result struct {
		PartnerID   string
		LastMsgTime int64
	}
	var results []Result

	// Use Gorm Raw
	err = s.db.Raw(`
		SELECT 
			CASE WHEN from_user_id = ? THEN to_user_id ELSE from_user_id END as partner_id,
			MAX(msg_time) as last_msg_time
		FROM wecom_messages
		WHERE from_user_id = ? OR to_user_id = ?
		GROUP BY partner_id
		ORDER BY last_msg_time DESC
		LIMIT 50
	`, myID, myID, myID).Scan(&results).Error

	if err != nil {
		return nil, err
	}

	var contacts []ContactConversation
	for _, r := range results {
		// Get Last Message Content
		var lastMsg WecomMessage
		s.db.Where("(from_user_id = ? AND to_user_id = ? AND msg_time = ?) OR (from_user_id = ? AND to_user_id = ? AND msg_time = ?)",
			myID, r.PartnerID, r.LastMsgTime,
			r.PartnerID, myID, r.LastMsgTime,
		).First(&lastMsg)

		// Mock Name/Avatar for now or query User table if exists
		// In a real system we would join WecomUser table
		var partnerName = "用户 " + r.PartnerID
		var partnerAvatar = "https://ui-avatars.com/api/?name=" + r.PartnerID + "&background=random"

		// Try to find partner info
		var partnerUser WecomUser
		if err := s.db.First(&partnerUser, "id = ?", r.PartnerID).Error; err == nil {
			if partnerUser.Name != "" {
				partnerName = partnerUser.Name
			}
			if partnerUser.Avatar != "" {
				partnerAvatar = partnerUser.Avatar
			}
		}

		contacts = append(contacts, ContactConversation{
			PartnerID:   r.PartnerID,
			PartnerName: partnerName,
			LastMsg:     lastMsg.Content,
			LastTime:    time.UnixMilli(r.LastMsgTime),
			Avatar:      partnerAvatar,
		})
	}

	return contacts, nil
}

// GenerateBindCode 生成绑定验证码
func (s *BindingService) GenerateBindCode(numindUserID int64) (string, error) {
	// 简单的 6 位随机数生成
	code := fmt.Sprintf("%06d", time.Now().UnixNano()%1000000)

	bindCode := WecomBindCode{
		Code:      code,
		UserID:    numindUserID,
		ExpiredAt: time.Now().Add(5 * time.Minute), // 5分钟有效
		CreatedAt: time.Now(),
	}

	if err := s.db.Create(&bindCode).Error; err != nil {
		return "", err
	}
	return code, nil
}

// VerifyAndBind 验证并绑定
func (s *BindingService) VerifyAndBind(code string, externalUserID string) error {
	var bindCode WecomBindCode
	// 查找并校验
	if err := s.db.Where("code = ? AND expired_at > ?", code, time.Now()).First(&bindCode).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return errors.New("验证码无效或已过期")
		}
		return err
	}

	// 执行绑定
	req := &BindUserRequest{
		NumindUserID:   bindCode.UserID,
		ExternalUserID: externalUserID,
	}
	if err := s.BindUser(req); err != nil {
		return err
	}

	// 绑定成功后删除验证码（防止重复使用）
	s.db.Delete(&bindCode)

	return nil
}
