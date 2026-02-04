package wecom

import "time"

// WecomUser 微信外部联系人与 Numind 用户绑定表
// 用于将企业微信的 external_userid 与 Numind 平台用户关联
type WecomUser struct {
	ID        string     `gorm:"column:id;primaryKey;type:varchar(64)" json:"id"` // ExternalUserID (wm_xxx)
	UserID    *int64     `gorm:"column:user_id;index" json:"user_id"`
	Name      string     `gorm:"column:name;type:varchar(128)" json:"name"`     // 微信昵称
	Avatar    string     `gorm:"column:avatar;type:varchar(512)" json:"avatar"` // 头像URL
	BoundAt   *time.Time `gorm:"column:bound_at" json:"bound_at"`               // 绑定时间
	CreatedAt time.Time  `gorm:"column:created_at" json:"created_at"`
	UpdatedAt time.Time  `gorm:"column:updated_at" json:"updated_at"`
}

func (WecomUser) TableName() string {
	return "wecom_users"
}

// WecomMessage 企业微信存档消息表
// 存储从会话存档 SDK 拉取并解密后的消息
type WecomMessage struct {
	MsgID      string `gorm:"column:msg_id;primaryKey;type:varchar(64)" json:"msg_id"`        // 全局唯一MsgID
	Seq        uint64 `gorm:"column:seq;index" json:"seq"`                                    // 消息序列号
	Action     string `gorm:"column:action;type:varchar(32)" json:"action"`                   // send, recall, etc.
	FromUserID string `gorm:"column:from_user_id;index;type:varchar(64)" json:"from_user_id"` // 发送者ID
	ToUserID   string `gorm:"column:to_user_id;index;type:varchar(64)" json:"to_user_id"`     // 接收者ID
	RoomID     string `gorm:"column:room_id;type:varchar(64)" json:"room_id"`                 // 群ID，单聊为空
	MsgTime    int64  `gorm:"column:msg_time;index" json:"msg_time"`                          // 毫秒级时间戳
	MsgType    string `gorm:"column:msg_type;type:varchar(32)" json:"msg_type"`               // text, image, voice, video...

	// 内容字段 (根据不同类型存储)
	Content    string `gorm:"column:content;type:text" json:"content"`           // 文本内容或JSON
	MediaID    string `gorm:"column:media_id;type:varchar(256)" json:"media_id"` // sdkfileid
	MediaSize  int64  `gorm:"column:media_size" json:"media_size"`               // 文件大小
	IsExternal bool   `gorm:"column:is_external" json:"is_external"`             // 是否来自外部联系人

	CreatedAt time.Time `gorm:"column:created_at" json:"created_at"`
}

func (WecomMessage) TableName() string {
	return "wecom_messages"
}

// WecomCursor 用于记录上次拉取到的 Seq，防止重复拉取
type WecomCursor struct {
	ID        string    `gorm:"column:id;primaryKey;type:varchar(32)" json:"id"` // 固定为 "global_seq"
	Seq       uint64    `gorm:"column:seq" json:"seq"`
	UpdatedAt time.Time `gorm:"column:updated_at" json:"updated_at"`
}

func (WecomCursor) TableName() string {
	return "wecom_cursors"
}

// MsgType 常量定义
const (
	MsgTypeText     = "text"
	MsgTypeImage    = "image"
	MsgTypeVoice    = "voice"
	MsgTypeVideo    = "video"
	MsgTypeFile     = "file"
	MsgTypeEmotion  = "emotion"
	MsgTypeLocation = "location"
	MsgTypeLink     = "link"
	MsgTypeWeapp    = "weapp"
	MsgTypeCard     = "card"
	MsgTypeRevoke   = "revoke"
	MsgTypeAgree    = "agree"
	MsgTypeDisagree = "disagree"
	MsgTypeMixed    = "mixed"
	MsgTypeNote     = "note"
)

// IsExternalUser 判断是否为外部联系人（wm_ 或 wo_ 开头）
func IsExternalUser(userID string) bool {
	if len(userID) < 2 {
		return false
	}
	prefix := userID[:2]
	return prefix == "wm" || prefix == "wo"
}

// IsRobotUser 判断是否为机器人（wb_ 开头）
func IsRobotUser(userID string) bool {
	if len(userID) < 2 {
		return false
	}
	return userID[:2] == "wb"
}

// WecomBindCode 绑定验证码表
type WecomBindCode struct {
	Code      string    `gorm:"column:code;primaryKey;type:varchar(10)"`
	UserID    int64     `gorm:"column:user_id;index"`
	ExpiredAt time.Time `gorm:"column:expired_at"`
	CreatedAt time.Time `gorm:"column:created_at"`
}

func (WecomBindCode) TableName() string {
	return "wecom_bind_codes"
}
