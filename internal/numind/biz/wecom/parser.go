package wecom

import (
	"encoding/json"
	"fmt"
)

// RawChatData SDK 返回的原始加密数据
type RawChatData struct {
	Seq              uint64 `json:"seq"`
	MsgID            string `json:"msgid"`
	PublicKeyVer     int    `json:"publickey_ver"`
	EncryptRandomKey string `json:"encrypt_random_key"`
	EncryptChatMsg   string `json:"encrypt_chat_msg"`
}

// ChatDataResponse GetChatData 接口响应
type ChatDataResponse struct {
	ErrCode  int           `json:"errcode"`
	ErrMsg   string        `json:"errmsg"`
	ChatData []RawChatData `json:"chatdata"`
}

// DecryptedMessage 解密后的消息结构
type DecryptedMessage struct {
	MsgID   string   `json:"msgid"`
	Action  string   `json:"action"`
	From    string   `json:"from"`
	ToList  []string `json:"tolist"`
	RoomID  string   `json:"roomid"`
	MsgTime int64    `json:"msgtime"`
	MsgType string   `json:"msgtype"`

	// 不同类型消息的具体内容
	Text     *TextContent     `json:"text,omitempty"`
	Image    *ImageContent    `json:"image,omitempty"`
	Voice    *VoiceContent    `json:"voice,omitempty"`
	Video    *VideoContent    `json:"video,omitempty"`
	File     *FileContent     `json:"file,omitempty"`
	Emotion  *EmotionContent  `json:"emotion,omitempty"`
	Location *LocationContent `json:"location,omitempty"`
	Link     *LinkContent     `json:"link,omitempty"`
	Weapp    *WeappContent    `json:"weapp,omitempty"`
	Card     *CardContent     `json:"card,omitempty"`
	Revoke   *RevokeContent   `json:"revoke,omitempty"`
	Agree    *AgreeContent    `json:"agree,omitempty"`
	Disagree *DisagreeContent `json:"disagree,omitempty"`
	Info     *NoteContent     `json:"info,omitempty"` // 合并转发/笔记
}

// 各类型消息内容结构
type TextContent struct {
	Content string `json:"content"`
}

type ImageContent struct {
	MD5Sum    string `json:"md5sum"`
	FileSize  int64  `json:"filesize"`
	SDKFileID string `json:"sdkfileid"`
}

type VoiceContent struct {
	MD5Sum     string `json:"md5sum"`
	VoiceSize  int64  `json:"voice_size"`
	PlayLength int    `json:"play_length"` // 秒
	SDKFileID  string `json:"sdkfileid"`
}

type VideoContent struct {
	MD5Sum     string `json:"md5sum"`
	FileSize   int64  `json:"filesize"`
	PlayLength int    `json:"play_length"` // 秒
	SDKFileID  string `json:"sdkfileid"`
}

type FileContent struct {
	MD5Sum    string `json:"md5sum"`
	FileName  string `json:"filename"`
	FileExt   string `json:"fileext"`
	FileSize  int64  `json:"filesize"`
	SDKFileID string `json:"sdkfileid"`
}

type EmotionContent struct {
	Type      int    `json:"type"` // 1=gif, 2=png
	Width     int    `json:"width"`
	Height    int    `json:"height"`
	ImageSize int64  `json:"imagesize"`
	MD5Sum    string `json:"md5sum"`
	SDKFileID string `json:"sdkfileid"`
}

type LocationContent struct {
	Longitude float64 `json:"longitude"`
	Latitude  float64 `json:"latitude"`
	Address   string  `json:"address"`
	Title     string  `json:"title"`
	Zoom      int     `json:"zoom"`
}

type LinkContent struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	LinkURL     string `json:"link_url"`
	ImageURL    string `json:"image_url"`
}

type WeappContent struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	Username    string `json:"username"`
	DisplayName string `json:"displayname"`
	AppID       string `json:"appid"`
	PagePath    string `json:"pagepath"`
}

type CardContent struct {
	CorpName string `json:"corpname"`
	UserID   string `json:"userid"`
}

type RevokeContent struct {
	PreMsgID string `json:"pre_msgid"`
}

type AgreeContent struct {
	UserID    string `json:"userid"`
	AgreeTime int64  `json:"agree_time"`
}

type DisagreeContent struct {
	UserID       string `json:"userid"`
	DisagreeTime int64  `json:"disagree_time"`
}

// NoteContent 合并转发/笔记消息（可包含多条子消息）
type NoteContent struct {
	Items []NoteItem `json:"items"`
}

type NoteItem struct {
	Content string `json:"content"` // JSON 字符串，需要二次解析
	MsgType string `json:"msg_type"`
}

// ParseMessage 解析解密后的 JSON 消息
func ParseMessage(jsonContent string) (*WecomMessage, error) {
	var msg DecryptedMessage
	if err := json.Unmarshal([]byte(jsonContent), &msg); err != nil {
		return nil, fmt.Errorf("parse message failed: %w", err)
	}

	// 构建 WecomMessage
	wecomMsg := &WecomMessage{
		MsgID:      msg.MsgID,
		Action:     msg.Action,
		FromUserID: msg.From,
		MsgTime:    msg.MsgTime,
		MsgType:    msg.MsgType,
		RoomID:     msg.RoomID,
		IsExternal: IsExternalUser(msg.From),
	}

	// 处理接收者（可能是多个）
	if len(msg.ToList) > 0 {
		wecomMsg.ToUserID = msg.ToList[0]
	}

	// 根据消息类型提取内容
	switch msg.MsgType {
	case MsgTypeText:
		if msg.Text != nil {
			wecomMsg.Content = msg.Text.Content
		}
	case MsgTypeImage:
		if msg.Image != nil {
			wecomMsg.MediaID = msg.Image.SDKFileID
			wecomMsg.MediaSize = msg.Image.FileSize
		}
	case MsgTypeVoice:
		if msg.Voice != nil {
			wecomMsg.MediaID = msg.Voice.SDKFileID
			wecomMsg.MediaSize = msg.Voice.VoiceSize
			// 额外信息可以存入 Content JSON
			contentJSON, _ := json.Marshal(map[string]interface{}{
				"play_length": msg.Voice.PlayLength,
			})
			wecomMsg.Content = string(contentJSON)
		}
	case MsgTypeVideo:
		if msg.Video != nil {
			wecomMsg.MediaID = msg.Video.SDKFileID
			wecomMsg.MediaSize = msg.Video.FileSize
			contentJSON, _ := json.Marshal(map[string]interface{}{
				"play_length": msg.Video.PlayLength,
			})
			wecomMsg.Content = string(contentJSON)
		}
	case MsgTypeFile:
		if msg.File != nil {
			wecomMsg.MediaID = msg.File.SDKFileID
			wecomMsg.MediaSize = msg.File.FileSize
			contentJSON, _ := json.Marshal(map[string]interface{}{
				"filename": msg.File.FileName,
				"fileext":  msg.File.FileExt,
			})
			wecomMsg.Content = string(contentJSON)
		}
	case MsgTypeRevoke:
		if msg.Revoke != nil {
			contentJSON, _ := json.Marshal(map[string]interface{}{
				"pre_msgid": msg.Revoke.PreMsgID,
			})
			wecomMsg.Content = string(contentJSON)
		}
	case MsgTypeNote:
		// 合并转发：保存整个 items 结构
		if msg.Info != nil {
			contentJSON, _ := json.Marshal(msg.Info)
			wecomMsg.Content = string(contentJSON)
		}
	default:
		// 其他类型：将原始 JSON 存入 Content
		wecomMsg.Content = jsonContent
	}

	return wecomMsg, nil
}

// ParseNoteItems 解析合并转发中的子消息
// 返回每条子消息的发言人昵称和内容
func ParseNoteItems(noteContent *NoteContent) ([]map[string]string, error) {
	if noteContent == nil {
		return nil, nil
	}

	var results []map[string]string
	for _, item := range noteContent.Items {
		result := map[string]string{
			"type":    item.MsgType,
			"content": item.Content,
		}
		// 尝试解析 content 中的发言人信息
		// 通常合并转发的文本内容格式为 "昵称: 内容"
		// 这里需要根据实际返回格式进行调整
		results = append(results, result)
	}

	return results, nil
}

// ParseMergedHistoryJSON parses the JSON content of a WeCom chatrecord message
func ParseMergedHistoryJSON(jsonContent string) ([]ImportMessage, error) {
	var raw struct {
		ChatRecord struct {
			Item []struct {
				Type       string `json:"type"`
				MsgTime    int64  `json:"msgtime"`
				Content    string `json:"content"`
				SourceName string `json:"sourcename"`
			} `json:"item"`
		} `json:"chatrecord"`
	}
	if err := json.Unmarshal([]byte(jsonContent), &raw); err != nil {
		return nil, err
	}

	var result []ImportMessage
	for _, item := range raw.ChatRecord.Item {
		msg := ImportMessage{
			MsgTime: item.MsgTime,
			Speaker: item.SourceName,
			MsgType: "text", // Default to text
		}

		switch item.Type {
		case "ChatRecordText":
			var textContent struct {
				Content string `json:"content"`
			}
			if err := json.Unmarshal([]byte(item.Content), &textContent); err == nil {
				msg.Content = textContent.Content
			}
			msg.MsgType = "text"

		case "ChatRecordImage":
			msg.Content = item.Content // Store the full media JSON (contains sdkfileid)
			msg.MsgType = "image"

		case "ChatRecordVoice":
			msg.Content = item.Content
			msg.MsgType = "voice"

		case "ChatRecordVideo":
			msg.Content = item.Content
			msg.MsgType = "video"

		case "ChatRecordFile":
			msg.Content = item.Content
			msg.MsgType = "file"

		default:
			msg.Content = "[不支持的合并消息类型: " + item.Type + "]"
			msg.MsgType = "text"
		}
		result = append(result, msg)
	}
	return result, nil
}
