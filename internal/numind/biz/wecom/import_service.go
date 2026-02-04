package wecom

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"gorm.io/gorm"
)

// ImportService 微信存档服务
// 提供归档会话列表和会话消息详情的查询
type ImportService struct {
	db *gorm.DB
}

func NewImportService(db *gorm.DB) *ImportService {
	return &ImportService{db: db}
}

// ArchiveSession 会话列表项结构
type ArchiveSession struct {
	SessionID  string `json:"session_id"`
	Title      string `json:"title"`
	LastActive string `json:"last_active"`
	MsgCount   int64  `json:"msg_count"`
	Type       string `json:"type"` // "system" (个人/普通聚合) 或 "customer" (合并聊天记录)
}

// ArchiveMessage 统一消息结构 (用于前端展示)
type ArchiveMessage struct {
	MsgID       string `json:"msg_id"`
	MsgTime     int64  `json:"msg_time"`
	MsgType     string `json:"msg_type"` // text, image, file, etc.
	Content     string `json:"content"`  // 展示内容
	SpeakerName string `json:"speaker"`  // 发送者名称 (显示用)
	FromUserID  string `json:"from_user_id"`
}

// GetArchiveSessions 获取归档会话列表
// 逻辑：
// 1. "个人记录 (My History)": 聚合所有非 chatrecord 类型的消息。
// 2. "合并记录 (Merged Records)": 每一条 chatrecord 类型的消息作为一个独立会话。
func (s *ImportService) GetArchiveSessions(userID int64) ([]ArchiveSession, error) {
	var sessions []ArchiveSession

	// 1. 获取 "个人记录" (聚合 MsgType != 'chatrecord')
	var myCount int64
	var lastMsg WecomMessage

	// 统计数量
	if err := s.db.Model(&WecomMessage{}).
		Where("msg_type != ?", "chatrecord").
		Count(&myCount).Error; err != nil {
		return nil, err
	}

	// 只有当有消息时才添加
	if myCount > 0 {
		// 获取最后一条消息的时间作为 LastActive
		s.db.Model(&WecomMessage{}).
			Where("msg_type != ?", "chatrecord").
			Order("msg_time desc").
			First(&lastMsg)

		lastActiveStr := ""
		if !lastMsg.CreatedAt.IsZero() {
			lastActiveStr = lastMsg.CreatedAt.Format(time.RFC3339)
		} else {
			lastActiveStr = time.Now().Format(time.RFC3339)
		}

		sessions = append(sessions, ArchiveSession{
			SessionID:  "myself",
			Title:      "个人记录 (Personal History)",
			LastActive: lastActiveStr,
			MsgCount:   myCount,
			Type:       "system",
		})
	}

	// 2. 获取 "合并记录" (MsgType == 'chatrecord')
	// 直接查询 WecomMessage 表中的 chatrecord 记录
	var chatRecords []WecomMessage
	if err := s.db.Model(&WecomMessage{}).
		Where("msg_type = ?", "chatrecord").
		Order("msg_time desc").
		Find(&chatRecords).Error; err != nil {
		return nil, err
	}

	for _, m := range chatRecords {
		// 尝试从 Content JSON 中提取 title
		title := "合并聊天记录"
		var meta struct {
			Title string `json:"title"`
		}
		// 简单的 JSON 解析尝试
		if json.Unmarshal([]byte(m.Content), &meta) == nil && meta.Title != "" {
			title = meta.Title
		} else {
			// 如果没有 title，使用时间作为标题
			title = fmt.Sprintf("记录 %s", time.UnixMilli(m.MsgTime).Format("01-02 15:04"))
		}

		sessions = append(sessions, ArchiveSession{
			SessionID:  m.MsgID, // 使用 MsgID 作为会话 Key
			Title:      title,
			LastActive: m.CreatedAt.Format(time.RFC3339),
			MsgCount:   1, // 每个记录视为 1 个条目 (包含多条内部消息)
			Type:       "customer",
		})
	}

	return sessions, nil
}

// GetSessionMessages 获取会话消息详情
// logic:
// - key == "myself": 返回所有 MsgType != 'chatrecord' 的消息列表。
// - key == [MsgID]:  返回该条 chatrecord 消息，并在后端解析其 JSON 内容，展平为 ArchiveMessage 列表。
func (s *ImportService) GetSessionMessages(userID int64, sessionKey string) ([]ArchiveMessage, error) {
	var result []ArchiveMessage

	if sessionKey == "myself" {
		// 获取个人聚合记录
		var msgs []WecomMessage
		if err := s.db.Model(&WecomMessage{}).
			Where("msg_type != ?", "chatrecord").
			Order("msg_time asc").
			Find(&msgs).Error; err != nil {
			return nil, err
		}

		// 转换为 UI 结构
		for _, m := range msgs {
			result = append(result, ArchiveMessage{
				MsgID:       m.MsgID,
				MsgTime:     m.MsgTime,
				MsgType:     m.MsgType,
				Content:     m.Content,
				FromUserID:  m.FromUserID,
				SpeakerName: "", // 普通消息由前端根据 UserID 显示名字/头像
			})
		}

	} else {
		// 获取合并记录 (单条 chatrecord)
		var msg WecomMessage
		if err := s.db.Model(&WecomMessage{}).
			Where("msg_id = ?", sessionKey).
			First(&msg).Error; err != nil {
			return nil, err
		}

		// 解析 JSON 内容
		// 结构通常为 { "chatrecord": { "item": [ ... ] } } 或直接 { "item": ... }
		items := parseChatRecordItems(msg.Content)

		for _, item := range items {
			result = append(result, item)
		}
	}

	return result, nil
}

// parseChatRecordItems 解析 chatrecord 的 JSON 内容并展平为消息列表
func parseChatRecordItems(jsonContent string) []ArchiveMessage {
	var result []ArchiveMessage
	var raw map[string]interface{}

	if err := json.Unmarshal([]byte(jsonContent), &raw); err != nil {
		// 解析失败，返回原始内容作为一条文本
		return []ArchiveMessage{{
			MsgType: "text",
			Content: "无法解析记录内容: " + jsonContent,
		}}
	}

	// 寻找 item 数组
	var items []interface{}

	// 尝试常见路径
	// 1. root["item"]
	if list, ok := raw["item"].([]interface{}); ok {
		items = list
	} else if cr, ok := raw["chatrecord"].(map[string]interface{}); ok {
		// 2. root["chatrecord"]["item"]
		if list, ok := cr["item"].([]interface{}); ok {
			items = list
		}
	} else {
		// 3. 深度搜索 (防止有时候结构不同)
		for _, v := range raw {
			if subMap, ok := v.(map[string]interface{}); ok {
				if list, ok := subMap["item"].([]interface{}); ok {
					items = list
					break
				}
			}
		}
	}

	if items == nil {
		return []ArchiveMessage{{
			MsgType: "text",
			Content: "记录为空或格式不支持",
		}}
	}

	// 遍历 item 解析
	for _, it := range items {
		m, ok := it.(map[string]interface{})
		if !ok {
			continue
		}

		// 提取字段
		// type: "ChatRecordText" -> "text"
		// content: string or json string
		// msgtime: number
		// sourcename: string (Speaker)

		rawType, _ := m["type"].(string)
		rawContent, _ := m["content"].(string) // 有可能是 JSON 字符串
		sourceName, _ := m["sourcename"].(string)

		var msgTime int64
		if tVal, ok := m["msgtime"].(float64); ok {
			msgTime = int64(tVal) * 1000 // 通常是秒，转毫秒? 需确认。WeCom SDK通常是秒。Archived MsgTime 是毫秒。
			// 假设是秒 (UNIX timestamp)
		}

		// 规范化 MsgType 和 Content
		finalType := "text"
		finalContent := rawContent

		switch rawType {
		case "ChatRecordText":
			finalType = "text"
			// ChatRecordText 的 content 有时是 JSON `{"content":"..."}`
			var txtObj struct {
				Content string `json:"content"`
			}
			if json.Unmarshal([]byte(rawContent), &txtObj) == nil && txtObj.Content != "" {
				finalContent = txtObj.Content
			}
		case "ChatRecordImage":
			finalType = "image"
			finalContent = "[图片]" // 图片内容可能是 xml 或 details，前端暂只显示占位或如果有URL则解析
		case "ChatRecordVoice":
			finalType = "voice"
			finalContent = "[语音]"
		case "ChatRecordVideo":
			finalType = "video"
			finalContent = "[视频]"
		case "ChatRecordFile":
			finalType = "file"
			finalContent = "[文件]"
		case "ChatRecordLocation":
			finalType = "location"
			finalContent = "[位置]"
		case "ChatRecordLink":
			finalType = "link"
			finalContent = "[链接]"
		default:
			finalType = "unknown"
			finalContent = fmt.Sprintf("[未知类型: %s]", rawType)
		}

		// 生成 ID (虚拟)
		tempID := fmt.Sprintf("%d_%s", msgTime, sourceName)

		result = append(result, ArchiveMessage{
			MsgID:       tempID,
			MsgTime:     msgTime,
			MsgType:     finalType,
			Content:     finalContent,
			SpeakerName: sourceName,
		})
	}

	// 按时间排序
	sort.Slice(result, func(i, j int) bool {
		return result[i].MsgTime < result[j].MsgTime
	})

	return result
}
